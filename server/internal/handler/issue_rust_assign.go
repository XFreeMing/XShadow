package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

type issueAssignEdit struct {
	ExpectedRevision int64            `json:"expected_revision"`
	AssigneeType     string           `json:"assignee_type"`
	AssigneeID       string           `json:"assignee_id"`
	SourceTaskID     *string          `json:"source_task_id,omitempty"`
	SuppressRun      bool             `json:"suppress_run"`
	ActivityID       string           `json:"activity_id"`
	PreparedEnqueue  *json.RawMessage `json:"prepared_enqueue,omitempty"`
}

func issueHasStaleDuplicateMark(issue db.Issue) bool {
	return issue.DuplicateOfIssueID.Valid && issue.Status != issuestatus.Cancelled
}

func assignEditSourceAllowed(prev db.Issue) bool {
	if !prev.AssigneeType.Valid {
		return true
	}
	switch prev.AssigneeType.String {
	case "member", "agent":
		return true
	default:
		return false
	}
}

// assignIssueEditUsesRust selects grid-style direct-agent assignments the native
// writer can own: expected_revision present, target is agent, source is
// unassigned/member/agent, built-in status, no workflow handoff, and no fields
// outside the assign payload. Squad targets and mixed mutations stay on Go.
func (h *Handler) assignIssueEditUsesRust(
	r *http.Request,
	workspaceID pgtype.UUID,
	prev db.Issue,
	rawFields map[string]json.RawMessage,
	req UpdateIssueRequest,
	handoff *workflowHandoff,
	params db.UpdateIssueParams,
) (bool, error) {
	if h.IssueReadBridge == nil || len(h.IssueReadBridge.AssignPilotScopes) == 0 {
		return false, nil
	}
	if req.ExpectedRevision == nil || handoff != nil {
		return false, nil
	}
	_, touchedType := rawFields["assignee_type"]
	_, touchedID := rawFields["assignee_id"]
	if !touchedType || !touchedID {
		return false, nil
	}
	if !params.AssigneeType.Valid || params.AssigneeType.String != "agent" || !params.AssigneeID.Valid {
		return false, nil
	}
	if !assignEditSourceAllowed(prev) {
		return false, nil
	}
	if !isActiveBuiltInIssueStatus(prev.Status) {
		return false, nil
	}
	if prev.TriageState.Valid {
		return false, nil
	}
	if req.Status != nil {
		return false, nil
	}
	_, dup := rawFields["duplicate_of_issue_id"]
	if dup {
		return false, nil
	}
	for key := range rawFields {
		switch key {
		case "assignee_type", "assignee_id", "expected_revision", "suppress_run", "handoff_note":
		default:
			return false, nil
		}
	}
	return matchesIssuePilot(r.Context(), h.Queries, h.IssueReadBridge.AssignPilotScopes, uuidToString(workspaceID), workspaceID)
}

func (h *Handler) updateIssueAssignViaRust(
	w http.ResponseWriter,
	r *http.Request,
	userID string,
	current db.Issue,
	params db.UpdateIssueParams,
	req UpdateIssueRequest,
	prepared *service.IssuePreparedAgentEnqueue,
) (after, before db.Issue, assignActivityID, queuedTaskID string, ok bool) {
	plannedActivityID := dbid.NewV7()
	edit := issueAssignEdit{
		ExpectedRevision: *req.ExpectedRevision,
		AssigneeType:     params.AssigneeType.String,
		AssigneeID:       uuidToString(params.AssigneeID),
		SourceTaskID:     uuidToPtr(h.wakeupSourceTaskID(r)),
		SuppressRun:      req.SuppressRun,
		ActivityID:       uuidToString(plannedActivityID),
	}
	if prepared != nil {
		raw, err := marshalIssueStatusPreparedEnqueue(*prepared)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to encode issue assign enqueue")
			return after, before, "", "", false
		}
		rawMsg := json.RawMessage(raw)
		edit.PreparedEnqueue = &rawMsg
	}
	payload, err := json.Marshal(edit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode issue assign edit")
		return after, before, "", "", false
	}
	path := "/internal/issues/" + uuidToString(current.ID) + "/assign"
	resp, requested := h.requestRustIssue(w, r, http.MethodPut, path, uuidToString(current.WorkspaceID), userID, payload)
	if !requested {
		return after, before, "", "", false
	}
	defer resp.Body.Close()
	limit := int64(maxRustIssueReadBody)
	if resp.StatusCode == http.StatusOK {
		limit += int64(len(current.Description.String)+len(current.AcceptanceCriteria)) * 2
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || int64(len(body)) > limit {
		if !writeQueryCancellation(w, r, err) {
			if err != nil {
				writeError(w, http.StatusServiceUnavailable, "rust issue write service unavailable")
			} else {
				writeError(w, http.StatusBadGateway, "failed to read rust issue assign response")
			}
		}
		return after, before, "", "", false
	}
	if resp.StatusCode == http.StatusConflict {
		var conflict struct {
			ActualRevision    int64 `json:"actual_revision"`
			DispatchSensitive bool  `json:"dispatch_sensitive"`
		}
		if json.Unmarshal(body, &conflict) == nil {
			if conflict.DispatchSensitive {
				writeErrorCode(w, http.StatusConflict, "assign_route_changed", "task assignment requirements changed; reload before assigning")
				return after, before, "", "", false
			}
			if conflict.ActualRevision > 0 {
				writeRevisionConflict(w, "issue", current.ID, *req.ExpectedRevision, conflict.ActualRevision)
				return after, before, "", "", false
			}
		}
		writeError(w, http.StatusBadGateway, "invalid rust issue assign revision conflict")
		return after, before, "", "", false
	}
	if resp.StatusCode != http.StatusOK {
		writeRustIssueResponse(w, resp, bytes.NewReader(body))
		return after, before, "", "", false
	}
	var snapshots struct {
		Before                ordinaryIssueSnapshot `json:"before"`
		After                 ordinaryIssueSnapshot `json:"after"`
		AssigneeActivityID    *string               `json:"assignee_activity_id"`
		QueuedTaskID          *string               `json:"queued_task_id"`
		CoalescedQueuedTaskID *string               `json:"coalesced_queued_task_id"`
	}
	if json.Unmarshal(body, &snapshots) != nil ||
		snapshots.Before.ID != current.ID || snapshots.Before.WorkspaceID != current.WorkspaceID ||
		snapshots.After.ID != current.ID || snapshots.After.WorkspaceID != current.WorkspaceID ||
		!rustAssignWriteSnapshotsValid(*req.ExpectedRevision, params.AssigneeType, params.AssigneeID, snapshots.Before, snapshots.After, issueHasStaleDuplicateMark(current)) {
		writeError(w, http.StatusBadGateway, "invalid rust issue assign snapshots")
		return after, before, "", "", false
	}
	beforeIssue := snapshots.Before.issue()
	afterIssue := snapshots.After.issue()
	assigneeChanged := beforeIssue.AssigneeType != afterIssue.AssigneeType || beforeIssue.AssigneeID != afterIssue.AssigneeID
	receipt := derefString(snapshots.AssigneeActivityID)
	if !rustAssignActivityReceiptValid(r.Context(), h.Queries, plannedActivityID, current.WorkspaceID, current.ID, beforeIssue, afterIssue, assigneeChanged, receipt) {
		writeError(w, http.StatusBadGateway, "invalid rust issue assign activity receipt")
		return after, before, "", "", false
	}
	taskReceipt := derefString(snapshots.QueuedTaskID)
	coalescedReceipt := derefString(snapshots.CoalescedQueuedTaskID)
	if prepared != nil {
		switch {
		case taskReceipt != "" && coalescedReceipt != "":
			writeError(w, http.StatusBadGateway, "invalid rust issue assign queue receipt")
			return after, before, "", "", false
		case taskReceipt != "":
			if !rustStatusQueuedTaskValid(r.Context(), h.Queries, taskReceipt, *prepared) {
				writeError(w, http.StatusBadGateway, "invalid rust issue assign queue receipt")
				return after, before, "", "", false
			}
		case coalescedReceipt != "":
			if !rustStatusCoalescedTaskValid(r.Context(), h.Queries, coalescedReceipt, *prepared) {
				writeError(w, http.StatusBadGateway, "invalid rust issue assign queue receipt")
				return after, before, "", "", false
			}
		default:
			writeError(w, http.StatusBadGateway, "invalid rust issue assign queue receipt")
			return after, before, "", "", false
		}
	} else if taskReceipt != "" || coalescedReceipt != "" {
		writeError(w, http.StatusBadGateway, "unexpected rust issue assign queue receipt")
		return after, before, "", "", false
	}
	return afterIssue, beforeIssue, receipt, taskReceipt, true
}

func rustAssignActivityReceiptValid(
	ctx context.Context,
	q *db.Queries,
	plannedID pgtype.UUID,
	workspaceID, issueID pgtype.UUID,
	before, after db.Issue,
	assigneeChanged bool,
	receipt string,
) bool {
	if !assigneeChanged {
		return receipt == ""
	}
	if receipt == "" || receipt != uuidToString(plannedID) {
		return false
	}
	row, err := q.GetActivity(ctx, plannedID)
	if err != nil {
		return false
	}
	if row.IssueID != issueID || row.WorkspaceID != workspaceID || row.Action != "assignee_changed" {
		return false
	}
	var details map[string]string
	if json.Unmarshal(row.Details, &details) != nil {
		return false
	}
	if details["to_type"] != after.AssigneeType.String || details["to_id"] != uuidToString(after.AssigneeID) {
		return false
	}
	prevType := textToString(before.AssigneeType)
	prevID := uuidToString(before.AssigneeID)
	if prevType != "" && details["from_type"] != prevType {
		return false
	}
	if prevID != "" && details["from_id"] != prevID {
		return false
	}
	return true
}

func rustAssignWriteSnapshotsValid(
	expectedRevision int64,
	assigneeType pgtype.Text,
	assigneeID pgtype.UUID,
	before, after ordinaryIssueSnapshot,
	staleDuplicateOnRequest bool,
) bool {
	b := before.issue()
	a := after.issue()
	if b.Revision != expectedRevision {
		return false
	}
	if !assigneeType.Valid || a.AssigneeType != assigneeType || a.AssigneeID != assigneeID {
		return false
	}
	assigneeChanged := b.AssigneeType != a.AssigneeType || b.AssigneeID != a.AssigneeID
	duplicateSanitized := b.DuplicateOfIssueID.Valid && !a.DuplicateOfIssueID.Valid && a.Status != issuestatus.Cancelled
	if assigneeChanged || duplicateSanitized {
		if a.Revision != b.Revision+1 {
			return false
		}
		if assigneeChanged && (!a.UpdatedAt.Valid || !a.LastActivityAt.Valid) {
			return false
		}
		if duplicateSanitized && !staleDuplicateOnRequest {
			return false
		}
		if duplicateSanitized && !a.LastActivityAt.Valid {
			return false
		}
	} else if a.Revision != b.Revision {
		return false
	}
	if b.Status != a.Status || b.Position != a.Position {
		return false
	}
	return true
}
