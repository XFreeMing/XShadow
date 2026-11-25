package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/issuestatus"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

type issueStatusEdit struct {
	ExpectedRevision int64    `json:"expected_revision"`
	Status           string   `json:"status"`
	Position         *float64 `json:"position,omitempty"`
	SourceTaskID     *string  `json:"source_task_id,omitempty"`
	SuppressRun      bool     `json:"suppress_run"`
	ActivityID       string   `json:"activity_id"`
}

func isActiveBuiltInIssueStatus(key string) bool {
	switch key {
	case issuestatus.Backlog, issuestatus.Todo, issuestatus.InProgress, issuestatus.InReview, issuestatus.Blocked:
		return true
	default:
		return false
	}
}

// statusEditDispatchSensitive keeps promotions that can enqueue on the Go path
// until the status writer can record the dispatch intent in the same transaction.
func statusEditDispatchSensitive(prev db.Issue, targetStatus string, req UpdateIssueRequest) bool {
	if req.SuppressRun {
		return false
	}
	if prev.Status != issuestatus.Backlog {
		return false
	}
	if !isActiveBuiltInIssueStatus(targetStatus) || targetStatus == issuestatus.Backlog {
		return false
	}
	if !prev.AssigneeType.Valid {
		return false
	}
	switch prev.AssigneeType.String {
	case "agent", "squad":
		return prev.AssigneeID.Valid
	default:
		return false
	}
}

// statusEditNeedsGoRunAdvance reports whether Go must advance the caller's
// workflow step after the write — something only the Go route does today.
func statusEditNeedsGoRunAdvance(callerRun *db.AgentTaskQueue, prev db.Issue, targetStatus string) bool {
	if callerRun == nil || !callerRun.WorkflowStep.Valid {
		return false
	}
	return prev.Status != targetStatus
}

// statusIssueEditUsesRust selects grid-style status moves the native writer can
// own: built-in active targets only, expected_revision present, no workflow
// handoff, no dispatch-sensitive promotion, and no fields outside the grid
// payload.
func (h *Handler) statusIssueEditUsesRust(
	r *http.Request,
	workspaceID pgtype.UUID,
	prev db.Issue,
	rawFields map[string]json.RawMessage,
	req UpdateIssueRequest,
	handoff *workflowHandoff,
	statusKeyForGuard string,
	callerRun *db.AgentTaskQueue,
) (bool, error) {
	if h.IssueReadBridge == nil || len(h.IssueReadBridge.StatusPilotScopes) == 0 {
		return false, nil
	}
	if req.ExpectedRevision == nil || req.Status == nil || handoff != nil {
		return false, nil
	}
	if _, dup := rawFields["duplicate_of_issue_id"]; dup {
		return false, nil
	}
	if !isActiveBuiltInIssueStatus(statusKeyForGuard) || !issuestatus.IsBuiltIn(prev.Status) {
		return false, nil
	}
	if statusEditDispatchSensitive(prev, statusKeyForGuard, req) {
		return false, nil
	}
	if statusEditNeedsGoRunAdvance(callerRun, prev, statusKeyForGuard) {
		return false, nil
	}
	if prev.Status == issuestatus.Cancelled && prev.DuplicateOfIssueID.Valid &&
		isActiveBuiltInIssueStatus(statusKeyForGuard) {
		return false, nil
	}
	for key := range rawFields {
		switch key {
		case "status", "expected_revision", "position", "suppress_run":
		default:
			return false, nil
		}
	}
	return matchesIssuePilot(r.Context(), h.Queries, h.IssueReadBridge.StatusPilotScopes, uuidToString(workspaceID), workspaceID)
}

func (h *Handler) updateIssueStatusViaRust(
	w http.ResponseWriter,
	r *http.Request,
	userID string,
	current db.Issue,
	params db.UpdateIssueParams,
	req UpdateIssueRequest,
) (after, before db.Issue, statusActivityID string, ok bool) {
	plannedActivityID := dbid.NewV7()
	edit := issueStatusEdit{
		ExpectedRevision: *req.ExpectedRevision,
		Status:           params.Status.String,
		SourceTaskID:     uuidToPtr(h.wakeupSourceTaskID(r)),
		SuppressRun:      req.SuppressRun,
		ActivityID:       uuidToString(plannedActivityID),
	}
	var explicitPosition *float64
	if params.Position.Valid {
		edit.Position = &params.Position.Float64
		explicitPosition = edit.Position
	}
	payload, err := json.Marshal(edit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode issue status edit")
		return after, before, "", false
	}
	path := "/internal/issues/" + uuidToString(current.ID) + "/status"
	resp, requested := h.requestRustIssue(w, r, http.MethodPut, path, uuidToString(current.WorkspaceID), userID, payload)
	if !requested {
		return after, before, "", false
	}
	defer resp.Body.Close()
	limit := int64(maxRustIssueReadBody)
	if resp.StatusCode == http.StatusOK {
		limit += int64(len(current.Description.String)+len(current.AcceptanceCriteria)) * 2
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || int64(len(body)) > limit {
		if !writeQueryCancellation(w, r, err) {
			writeError(w, http.StatusBadGateway, "failed to read rust issue status response")
		}
		return after, before, "", false
	}
	if resp.StatusCode == http.StatusConflict {
		var conflict struct {
			ActualRevision    int64 `json:"actual_revision"`
			DispatchSensitive bool  `json:"dispatch_sensitive"`
		}
		if json.Unmarshal(body, &conflict) == nil {
			if conflict.DispatchSensitive {
				writeErrorCode(w, http.StatusConflict, "status_route_changed", "task status requirements changed; reload before moving the task")
				return after, before, "", false
			}
			if conflict.ActualRevision > 0 {
				writeRevisionConflict(w, "issue", current.ID, *req.ExpectedRevision, conflict.ActualRevision)
				return after, before, "", false
			}
		}
		writeError(w, http.StatusBadGateway, "invalid rust issue status revision conflict")
		return after, before, "", false
	}
	if resp.StatusCode != http.StatusOK {
		writeRustIssueResponse(w, resp, bytes.NewReader(body))
		return after, before, "", false
	}
	var snapshots struct {
		Before           ordinaryIssueSnapshot `json:"before"`
		After            ordinaryIssueSnapshot `json:"after"`
		StatusActivityID *string               `json:"status_activity_id"`
	}
	if json.Unmarshal(body, &snapshots) != nil ||
		snapshots.Before.ID != current.ID || snapshots.Before.WorkspaceID != current.WorkspaceID ||
		snapshots.After.ID != current.ID || snapshots.After.WorkspaceID != current.WorkspaceID ||
		!rustStatusWriteSnapshotsValid(*req.ExpectedRevision, params.Status.String, explicitPosition, snapshots.Before, snapshots.After) {
		writeError(w, http.StatusBadGateway, "invalid rust issue status snapshots")
		return after, before, "", false
	}
	beforeIssue := snapshots.Before.issue()
	afterIssue := snapshots.After.issue()
	statusChanged := beforeIssue.Status != afterIssue.Status
	receipt := derefString(snapshots.StatusActivityID)
	if !rustStatusActivityReceiptValid(r.Context(), h.Queries, plannedActivityID, current.WorkspaceID, current.ID, beforeIssue.Status, afterIssue.Status, statusChanged, receipt) {
		writeError(w, http.StatusBadGateway, "invalid rust issue status activity receipt")
		return after, before, "", false
	}
	return afterIssue, beforeIssue, receipt, true
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func rustStatusActivityReceiptValid(
	ctx context.Context,
	q *db.Queries,
	plannedID pgtype.UUID,
	workspaceID, issueID pgtype.UUID,
	prevStatus, nextStatus string,
	statusChanged bool,
	receipt string,
) bool {
	if !statusChanged {
		return receipt == ""
	}
	if receipt == "" || receipt != uuidToString(plannedID) {
		return false
	}
	row, err := q.GetActivity(ctx, plannedID)
	if err != nil {
		return false
	}
	if row.IssueID != issueID || row.WorkspaceID != workspaceID || row.Action != "status_changed" {
		return false
	}
	var details map[string]string
	if json.Unmarshal(row.Details, &details) != nil {
		return false
	}
	return details["from"] == prevStatus && details["to"] == nextStatus
}

func rustStatusWriteSnapshotsValid(
	expectedRevision int64,
	requestedStatus string,
	explicitPosition *float64,
	before, after ordinaryIssueSnapshot,
) bool {
	b := before.issue()
	a := after.issue()
	if b.Revision != expectedRevision {
		return false
	}
	if a.Status != requestedStatus {
		return false
	}
	statusChanged := b.Status != a.Status
	positionChanged := b.Position != a.Position
	dupChanged := b.DuplicateOfIssueID != a.DuplicateOfIssueID
	changed := statusChanged || positionChanged || dupChanged
	if changed {
		if a.Revision != b.Revision+1 || !a.UpdatedAt.Valid {
			return false
		}
	} else if a.Revision != b.Revision {
		return false
	}
	activity := statusChanged || dupChanged
	if activity {
		if !a.LastActivityAt.Valid {
			return false
		}
	} else if a.LastActivityAt.Valid != b.LastActivityAt.Valid ||
		(a.LastActivityAt.Valid && !a.LastActivityAt.Time.Equal(b.LastActivityAt.Time)) {
		return false
	}
	if explicitPosition != nil && a.Position != *explicitPosition {
		return false
	}
	return true
}
