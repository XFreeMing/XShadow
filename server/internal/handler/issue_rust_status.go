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

type ordinaryActiveBatchTrust struct {
	ExpectedProjectID *string `json:"expected_project_id,omitempty"`
}

type issueStatusEdit struct {
	ExpectedRevision    int64                     `json:"expected_revision"`
	Status              string                    `json:"status"`
	Position            *float64                  `json:"position,omitempty"`
	SourceTaskID        *string                   `json:"source_task_id,omitempty"`
	SuppressRun         bool                      `json:"suppress_run"`
	ActivityID          string                    `json:"activity_id"`
	PreparedEnqueue     *json.RawMessage          `json:"prepared_enqueue,omitempty"`
	TerminalScope       *issueTerminalScope       `json:"terminal_scope,omitempty"`
	OrdinaryActiveBatch *ordinaryActiveBatchTrust `json:"ordinary_active_batch,omitempty"`
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
// handoff, and no fields outside the grid payload. Direct-agent and ordinary
// squad backlog promotions are native; workflow advancement stays on Go.
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
	if req.ExpectedRevision == nil || req.Status == nil {
		return false, nil
	}
	if handoff != nil {
		return h.statusWorkflowSquadHandoffUsesRust(
			r, workspaceID, prev, rawFields, req, handoff, statusKeyForGuard, callerRun,
		)
	}
	if _, dup := rawFields["duplicate_of_issue_id"]; dup {
		return false, nil
	}
	if !isActiveBuiltInIssueStatus(statusKeyForGuard) || !issuestatus.IsBuiltIn(prev.Status) {
		return false, nil
	}
	if statusEditDispatchSensitive(prev, statusKeyForGuard, req) {
		switch prev.AssigneeType.String {
		case "agent", "squad":
		default:
			return false, nil
		}
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
	prepared *service.IssuePreparedAgentEnqueue,
	terminalClose bool,
) (after, before db.Issue, statusActivityID, queuedTaskID string, cancelled []db.AgentTaskQueue, ok bool) {
	plannedActivityID := dbid.NewV7()
	edit := issueStatusEdit{
		ExpectedRevision: *req.ExpectedRevision,
		Status:           params.Status.String,
		SourceTaskID:     uuidToPtr(h.wakeupSourceTaskID(r)),
		SuppressRun:      req.SuppressRun,
		ActivityID:       uuidToString(plannedActivityID),
	}
	if terminalClose {
		edit.TerminalScope = &issueTerminalScope{ProjectID: uuidToPtr(current.ProjectID)}
	}
	if params.Position.Valid {
		edit.Position = &params.Position.Float64
	}
	if prepared != nil {
		raw, err := marshalIssueStatusPreparedEnqueue(*prepared)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to encode issue status promotion")
			return after, before, "", "", nil, false
		}
		rawMsg := json.RawMessage(raw)
		edit.PreparedEnqueue = &rawMsg
	}
	resp, body, transportOK := h.callRustIssueStatusPUT(w, r, userID, current, edit)
	if !transportOK {
		return after, before, "", "", nil, false
	}
	if resp.StatusCode == http.StatusConflict {
		if conflict, recognized := parseRustIssueStatusConflict(body); recognized {
			if conflict.DispatchSensitive {
				writeErrorCode(w, http.StatusConflict, "status_route_changed", "task status requirements changed; reload before moving the task")
				return after, before, "", "", nil, false
			}
			if conflict.ActualRevision > 0 {
				writeRevisionConflict(w, "issue", current.ID, *req.ExpectedRevision, conflict.ActualRevision)
				return after, before, "", "", nil, false
			}
		}
		writeError(w, http.StatusBadGateway, "invalid rust issue status revision conflict")
		return after, before, "", "", nil, false
	}
	if resp.StatusCode != http.StatusOK {
		writeRustIssueResponse(w, resp, bytes.NewReader(body))
		return after, before, "", "", nil, false
	}
	receipt, decoded := decodeRustIssueStatusWriteReceipt(body, current.ID, current.WorkspaceID)
	if !decoded {
		writeError(w, http.StatusBadGateway, "invalid rust issue status snapshots")
		return after, before, "", "", nil, false
	}
	result, valid := h.checkRustIssueStatusReceipt(w, r.Context(), current, edit, plannedActivityID, receipt)
	if !valid {
		return after, before, "", "", nil, false
	}
	return h.finishRustIssueStatusSingleWrite(
		w, r.Context(), current, result, receipt, prepared, terminalClose,
	)
}

type rustIssueStatusWriteReceipt struct {
	Before                ordinaryIssueSnapshot `json:"before"`
	After                 ordinaryIssueSnapshot `json:"after"`
	StatusActivityID      *string               `json:"status_activity_id"`
	QueuedTaskID          *string               `json:"queued_task_id"`
	CoalescedQueuedTaskID *string               `json:"coalesced_queued_task_id"`
	CancelledTasks        json.RawMessage       `json:"cancelled_tasks"`
}

type rustStatusWriteResult struct {
	before, after    db.Issue
	statusActivityID string
}

type rustIssueStatusConflict struct {
	ActualRevision    int64 `json:"actual_revision"`
	DispatchSensitive bool  `json:"dispatch_sensitive"`
	BatchRollback     bool  `json:"batch_rollback"`
}

func rustIssueStatusReadBodyLimit(issue db.Issue, statusCode int) int64 {
	limit := int64(maxRustIssueReadBody)
	if statusCode == http.StatusOK {
		limit += int64(len(issue.Description.String)+len(issue.AcceptanceCriteria)) * 2
	}
	return limit
}

func (h *Handler) callRustIssueStatusPUT(
	w http.ResponseWriter,
	r *http.Request,
	userID string,
	issue db.Issue,
	edit issueStatusEdit,
) (*http.Response, []byte, bool) {
	payload, err := json.Marshal(edit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode issue status edit")
		return nil, nil, false
	}
	path := "/internal/issues/" + uuidToString(issue.ID) + "/status"
	resp, requested := h.requestRustIssue(w, r, http.MethodPut, path, uuidToString(issue.WorkspaceID), userID, payload)
	if !requested {
		return nil, nil, false
	}
	defer resp.Body.Close()
	limit := rustIssueStatusReadBodyLimit(issue, resp.StatusCode)
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || int64(len(body)) > limit {
		if writeQueryCancellation(w, r, err) {
			return nil, nil, false
		}
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "rust issue write service unavailable")
		} else {
			writeError(w, http.StatusBadGateway, "failed to read rust issue status response")
		}
		return nil, nil, false
	}
	return resp, body, true
}

func decodeRustIssueStatusWriteReceipt(body []byte, issueID, workspaceID pgtype.UUID) (rustIssueStatusWriteReceipt, bool) {
	var wire rustIssueStatusWriteReceipt
	if json.Unmarshal(body, &wire) != nil ||
		wire.Before.ID != issueID || wire.Before.WorkspaceID != workspaceID ||
		wire.After.ID != issueID || wire.After.WorkspaceID != workspaceID {
		return rustIssueStatusWriteReceipt{}, false
	}
	return wire, true
}

func parseRustIssueStatusConflict(body []byte) (conflict rustIssueStatusConflict, recognized bool) {
	if json.Unmarshal(body, &conflict) != nil {
		return conflict, false
	}
	switch {
	case conflict.DispatchSensitive:
		return conflict, conflict.ActualRevision == 0 && !conflict.BatchRollback
	case conflict.BatchRollback:
		return conflict, conflict.ActualRevision == 0
	default:
		return conflict, conflict.ActualRevision > 0
	}
}

func rustIssueStatusTrustedProjectMatch(before, after ordinaryIssueSnapshot, trusted pgtype.UUID) bool {
	return before.ProjectID == trusted && after.ProjectID == trusted
}

func rustIssueStatusReceiptHasBatchScopeExclusives(receipt rustIssueStatusWriteReceipt) bool {
	return len(receipt.CancelledTasks) > 0 ||
		derefString(receipt.QueuedTaskID) != "" || derefString(receipt.CoalescedQueuedTaskID) != ""
}

func (h *Handler) checkRustIssueStatusReceipt(
	w http.ResponseWriter,
	ctx context.Context,
	issue db.Issue,
	edit issueStatusEdit,
	plannedID pgtype.UUID,
	receipt rustIssueStatusWriteReceipt,
) (rustStatusWriteResult, bool) {
	if !rustStatusWriteSnapshotsValid(edit.ExpectedRevision, edit.Status, edit.Position, receipt.Before, receipt.After) ||
		(edit.OrdinaryActiveBatch != nil && !rustIssueStatusTrustedProjectMatch(receipt.Before, receipt.After, issue.ProjectID)) {
		writeError(w, http.StatusBadGateway, "invalid rust issue status snapshots")
		return rustStatusWriteResult{}, false
	}
	beforeIssue := receipt.Before.issue()
	afterIssue := receipt.After.issue()
	statusChanged := beforeIssue.Status != afterIssue.Status
	statusReceipt := derefString(receipt.StatusActivityID)
	if !rustStatusActivityReceiptValid(ctx, h.Queries, plannedID, issue.WorkspaceID, issue.ID, beforeIssue.Status, afterIssue.Status, statusChanged, statusReceipt) {
		writeError(w, http.StatusBadGateway, "invalid rust issue status activity receipt")
		return rustStatusWriteResult{}, false
	}
	return rustStatusWriteResult{before: beforeIssue, after: afterIssue, statusActivityID: statusReceipt}, true
}

func (h *Handler) finishRustIssueStatusSingleWrite(
	w http.ResponseWriter,
	ctx context.Context,
	current db.Issue,
	result rustStatusWriteResult,
	receipt rustIssueStatusWriteReceipt,
	prepared *service.IssuePreparedAgentEnqueue,
	terminalClose bool,
) (after, before db.Issue, statusActivityID, queuedTaskID string, cancelled []db.AgentTaskQueue, ok bool) {
	taskReceipt := derefString(receipt.QueuedTaskID)
	coalescedReceipt := derefString(receipt.CoalescedQueuedTaskID)
	var cancelledTasks *[]rustCancelledTaskReceipt
	if len(receipt.CancelledTasks) > 0 && json.Unmarshal(receipt.CancelledTasks, &cancelledTasks) != nil {
		writeError(w, http.StatusBadGateway, "invalid rust issue terminal cancellation receipt")
		return after, before, "", "", nil, false
	}
	if terminalClose {
		if prepared != nil || taskReceipt != "" || coalescedReceipt != "" {
			writeError(w, http.StatusBadGateway, "unexpected rust issue status queue receipt")
			return after, before, "", "", nil, false
		}
		if cancelledTasks == nil {
			writeError(w, http.StatusBadGateway, "invalid rust issue terminal cancellation receipt")
			return after, before, "", "", nil, false
		}
		cancelled, ok := rustTerminalCancelledTasksValid(ctx, h.Queries, current.ID, current.WorkspaceID, *cancelledTasks)
		if !ok {
			writeError(w, http.StatusBadGateway, "invalid rust issue terminal cancellation receipt")
			return after, before, "", "", nil, false
		}
		return result.after, result.before, result.statusActivityID, "", cancelled, true
	}
	if cancelledTasks != nil {
		writeError(w, http.StatusBadGateway, "unexpected rust issue terminal cancellation receipt")
		return after, before, "", "", nil, false
	}
	if prepared != nil {
		switch {
		case taskReceipt != "" && coalescedReceipt != "":
			writeError(w, http.StatusBadGateway, "invalid rust issue status queue receipt")
			return after, before, "", "", nil, false
		case taskReceipt != "":
			if !rustStatusQueuedTaskValid(ctx, h.Queries, taskReceipt, *prepared) {
				writeError(w, http.StatusBadGateway, "invalid rust issue status queue receipt")
				return after, before, "", "", nil, false
			}
		case coalescedReceipt != "":
			if !rustStatusCoalescedTaskValid(ctx, h.Queries, coalescedReceipt, *prepared) {
				writeError(w, http.StatusBadGateway, "invalid rust issue status queue receipt")
				return after, before, "", "", nil, false
			}
		default:
			writeError(w, http.StatusBadGateway, "invalid rust issue status queue receipt")
			return after, before, "", "", nil, false
		}
	} else if taskReceipt != "" || coalescedReceipt != "" {
		writeError(w, http.StatusBadGateway, "unexpected rust issue status queue receipt")
		return after, before, "", "", nil, false
	}
	return result.after, result.before, result.statusActivityID, taskReceipt, nil, true
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
