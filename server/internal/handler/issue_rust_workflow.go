package handler

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/issueworkflow"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

type workflowHandoffPlan struct {
	ProjectID     string `json:"project_id"`
	WorkflowID    string `json:"workflow_id"`
	WorkflowName  string `json:"workflow_name"`
	TargetStatus  string `json:"target_status"`
	SquadID       string `json:"squad_id"`
	Instructions  string `json:"instructions"`
	NextStatusKey string `json:"next_status_key,omitempty"`
	BackStatusKey string `json:"back_status_key,omitempty"`
}

type workflowSquadBatchTrust struct {
	LeaderID  string `json:"leader_id"`
	RuntimeID string `json:"runtime_id"`
}

type issueWorkflowSquadEdit struct {
	ExpectedRevision      int64                    `json:"expected_revision"`
	Status                string                   `json:"status"`
	AssigneeType          string                   `json:"assignee_type"`
	AssigneeID            string                   `json:"assignee_id"`
	Position              *float64                 `json:"position,omitempty"`
	SourceTaskID          *string                  `json:"source_task_id,omitempty"`
	SuppressRun           bool                     `json:"suppress_run"`
	ActivityID            string                   `json:"activity_id"`
	WorkflowPlan          workflowHandoffPlan      `json:"workflow_plan"`
	ActivityHandoff       map[string]string        `json:"activity_handoff,omitempty"`
	PreparedWorkflowQueue *json.RawMessage         `json:"prepared_workflow_enqueue,omitempty"`
	WorkflowSquadBatch    *workflowSquadBatchTrust `json:"workflow_squad_batch,omitempty"`
}

type rustWorkflowWriteResult struct {
	before, after    db.Issue
	statusActivityID string
	queuedTaskID     string
}

type rustWorkflowSquadConflict struct {
	ActualRevision    int64 `json:"actual_revision"`
	DispatchSensitive bool  `json:"dispatch_sensitive"`
	BatchRollback     bool  `json:"batch_rollback"`
}

// statusWorkflowSquadHandoffUsesRust selects squad workflow handoffs the native
// writer can own: expected_revision present, only status on the public payload,
// built-in statuses, squad handler, and matching workflow/status pilots. Agent
// handoffs and mixed mutations stay on Go.
func (h *Handler) statusWorkflowSquadHandoffUsesRust(
	r *http.Request,
	workspaceID pgtype.UUID,
	prev db.Issue,
	rawFields map[string]json.RawMessage,
	req UpdateIssueRequest,
	handoff *workflowHandoff,
	statusKeyForGuard string,
	callerRun *db.AgentTaskQueue,
) (bool, error) {
	if h.IssueReadBridge == nil ||
		len(h.IssueReadBridge.StatusPilotScopes) == 0 ||
		len(h.IssueReadBridge.WorkflowPilotScopes) == 0 {
		return false, nil
	}
	if handoff == nil || req.ExpectedRevision == nil || req.Status == nil {
		return false, nil
	}
	if handoff.step.Handler.Type != issueworkflow.HandlerSquad {
		return false, nil
	}
	if req.EnterWorkflowStep || req.StopPreviousAssigneeRuns {
		return false, nil
	}
	if _, dup := rawFields["duplicate_of_issue_id"]; dup {
		return false, nil
	}
	if !isActiveBuiltInIssueStatus(statusKeyForGuard) || !issuestatus.IsBuiltIn(prev.Status) {
		return false, nil
	}
	if statusEditNeedsGoRunAdvance(callerRun, prev, statusKeyForGuard) {
		return false, nil
	}
	if prev.Status == issuestatus.Cancelled && prev.DuplicateOfIssueID.Valid &&
		isActiveBuiltInIssueStatus(statusKeyForGuard) {
		return false, nil
	}
	if prev.TriageState.Valid || len(prev.WorkMethodRun) > 0 {
		return false, nil
	}
	for key := range rawFields {
		switch key {
		case "status", "expected_revision", "position", "suppress_run":
		default:
			return false, nil
		}
	}
	inStatus, err := matchesIssuePilot(r.Context(), h.Queries, h.IssueReadBridge.StatusPilotScopes, uuidToString(workspaceID), workspaceID)
	if err != nil || !inStatus {
		return inStatus, err
	}
	return matchesIssuePilot(r.Context(), h.Queries, h.IssueReadBridge.WorkflowPilotScopes, uuidToString(workspaceID), workspaceID)
}

func (h *Handler) buildIssueWorkflowSquadEdit(
	ctx context.Context,
	current db.Issue,
	expectedRevision int64,
	params db.UpdateIssueParams,
	handoff *workflowHandoff,
	sourceTaskID pgtype.UUID,
	suppressRun bool,
	plannedActivityID pgtype.UUID,
	prepared *service.IssuePreparedWorkflowHandoffEnqueue,
	runStarted bool,
	batch *workflowSquadBatchTrust,
) (issueWorkflowSquadEdit, error) {
	projected := current
	projected.Status = params.Status.String
	projected.AssigneeType = params.AssigneeType
	projected.AssigneeID = params.AssigneeID
	edit := issueWorkflowSquadEdit{
		ExpectedRevision: expectedRevision,
		Status:           params.Status.String,
		AssigneeType:     params.AssigneeType.String,
		AssigneeID:       uuidToString(params.AssigneeID),
		SourceTaskID:     uuidToPtr(sourceTaskID),
		SuppressRun:      suppressRun,
		ActivityID:       uuidToString(plannedActivityID),
		WorkflowPlan: workflowHandoffPlan{
			ProjectID:     uuidToString(current.ProjectID),
			WorkflowID:    uuidToString(handoff.def.ID),
			WorkflowName:  handoff.def.Name,
			TargetStatus:  params.Status.String,
			SquadID:       uuidToString(params.AssigneeID),
			Instructions:  handoff.step.Instructions,
			NextStatusKey: handoff.step.NextStatusKey,
			BackStatusKey: handoff.step.BackStatusKey,
		},
		ActivityHandoff: workflowHandoffPayload(
			projected, handoff,
			h.workflowHandoffBrief(ctx, projected, handoff),
			runStarted,
		),
		WorkflowSquadBatch: batch,
	}
	if params.Position.Valid {
		edit.Position = &params.Position.Float64
	}
	if prepared != nil {
		raw, err := marshalIssueWorkflowHandoffEnqueue(*prepared)
		if err != nil {
			return issueWorkflowSquadEdit{}, err
		}
		rawMsg := json.RawMessage(raw)
		edit.PreparedWorkflowQueue = &rawMsg
	}
	return edit, nil
}

func parseRustWorkflowSquadConflict(body []byte) (rustWorkflowSquadConflict, bool) {
	var conflict rustWorkflowSquadConflict
	if json.Unmarshal(body, &conflict) != nil {
		return conflict, false
	}
	switch {
	case conflict.DispatchSensitive:
		return conflict, conflict.ActualRevision == 0 && !conflict.BatchRollback
	case conflict.BatchRollback:
		return conflict, conflict.ActualRevision == 0 && !conflict.DispatchSensitive
	default:
		return conflict, conflict.ActualRevision > 0 && !conflict.BatchRollback && !conflict.DispatchSensitive
	}
}

func rustWorkflowSquadHandoffReadBodyLimit(issue db.Issue, statusCode int) int64 {
	limit := int64(maxRustIssueReadBody)
	if statusCode == http.StatusOK {
		limit += int64(len(issue.Description.String)+len(issue.AcceptanceCriteria)) * 2
	}
	return limit
}

func (h *Handler) callRustWorkflowSquadHandoffPUT(
	w http.ResponseWriter,
	r *http.Request,
	userID string,
	issue db.Issue,
	edit issueWorkflowSquadEdit,
) (*http.Response, []byte, bool) {
	payload, err := json.Marshal(edit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode workflow squad handoff edit")
		return nil, nil, false
	}
	path := "/internal/issues/" + uuidToString(issue.ID) + "/workflow-squad-handoff"
	resp, requested := h.requestRustIssue(w, r, http.MethodPut, path, uuidToString(issue.WorkspaceID), userID, payload)
	if !requested {
		return nil, nil, false
	}
	defer resp.Body.Close()
	limit := rustWorkflowSquadHandoffReadBodyLimit(issue, resp.StatusCode)
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || int64(len(body)) > limit {
		if writeQueryCancellation(w, r, err) {
			return nil, nil, false
		}
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "rust issue write service unavailable")
		} else {
			writeError(w, http.StatusBadGateway, "failed to read rust workflow squad handoff response")
		}
		return nil, nil, false
	}
	return resp, body, true
}

func (h *Handler) validateRustWorkflowSquadHandoffWriteBody(
	w http.ResponseWriter,
	ctx context.Context,
	current db.Issue,
	expectedRevision int64,
	params db.UpdateIssueParams,
	plannedActivityID pgtype.UUID,
	prepared *service.IssuePreparedWorkflowHandoffEnqueue,
	body []byte,
) (rustWorkflowWriteResult, bool) {
	var snapshots struct {
		Before                  ordinaryIssueSnapshot `json:"before"`
		After                   ordinaryIssueSnapshot `json:"after"`
		StatusActivityID        *string               `json:"status_activity_id"`
		DeferredTaskID          *string               `json:"deferred_task_id"`
		CoalescedDeferredTaskID *string               `json:"coalesced_deferred_task_id"`
	}
	if json.Unmarshal(body, &snapshots) != nil ||
		snapshots.Before.ID != current.ID || snapshots.Before.WorkspaceID != current.WorkspaceID ||
		snapshots.After.ID != current.ID || snapshots.After.WorkspaceID != current.WorkspaceID ||
		!rustWorkflowHandoffWriteSnapshotsValid(expectedRevision, params, snapshots.Before, snapshots.After) {
		writeError(w, http.StatusBadGateway, "invalid rust workflow squad handoff snapshots")
		return rustWorkflowWriteResult{}, false
	}
	beforeIssue := snapshots.Before.issue()
	afterIssue := snapshots.After.issue()
	if params.ProjectID.Valid &&
		!rustIssueStatusTrustedProjectMatch(snapshots.Before, snapshots.After, current.ProjectID) {
		writeError(w, http.StatusBadGateway, "invalid rust workflow squad handoff snapshots")
		return rustWorkflowWriteResult{}, false
	}
	statusChanged := beforeIssue.Status != afterIssue.Status
	receipt := derefString(snapshots.StatusActivityID)
	if !rustStatusActivityReceiptValid(ctx, h.Queries, plannedActivityID, current.WorkspaceID, current.ID, beforeIssue.Status, afterIssue.Status, statusChanged, receipt) {
		writeError(w, http.StatusBadGateway, "invalid rust workflow squad handoff activity receipt")
		return rustWorkflowWriteResult{}, false
	}
	deferredReceipt := derefString(snapshots.DeferredTaskID)
	coalescedReceipt := derefString(snapshots.CoalescedDeferredTaskID)
	if prepared != nil {
		switch {
		case deferredReceipt != "" && coalescedReceipt != "":
			writeError(w, http.StatusBadGateway, "invalid rust workflow squad handoff queue receipt")
			return rustWorkflowWriteResult{}, false
		case deferredReceipt != "":
			if !rustWorkflowDeferredTaskValid(ctx, h.Queries, deferredReceipt, *prepared) {
				writeError(w, http.StatusBadGateway, "invalid rust workflow squad handoff queue receipt")
				return rustWorkflowWriteResult{}, false
			}
		case coalescedReceipt != "":
			if !rustWorkflowCoalescedDeferredTaskValid(ctx, h.Queries, coalescedReceipt, *prepared) {
				writeError(w, http.StatusBadGateway, "invalid rust workflow squad handoff queue receipt")
				return rustWorkflowWriteResult{}, false
			}
		default:
			writeError(w, http.StatusBadGateway, "invalid rust workflow squad handoff queue receipt")
			return rustWorkflowWriteResult{}, false
		}
	} else if deferredReceipt != "" || coalescedReceipt != "" {
		writeError(w, http.StatusBadGateway, "unexpected rust workflow squad handoff queue receipt")
		return rustWorkflowWriteResult{}, false
	}
	return rustWorkflowWriteResult{
		before:           beforeIssue,
		after:            afterIssue,
		statusActivityID: receipt,
		queuedTaskID:     cmp.Or(deferredReceipt, coalescedReceipt),
	}, true
}

func (h *Handler) updateIssueWorkflowSquadHandoffViaRust(
	w http.ResponseWriter,
	r *http.Request,
	userID string,
	current db.Issue,
	params db.UpdateIssueParams,
	req UpdateIssueRequest,
	handoff *workflowHandoff,
	prepared *service.IssuePreparedWorkflowHandoffEnqueue,
	runStarted bool,
) (after, before db.Issue, statusActivityID, deferredTaskID string, ok bool) {
	plannedActivityID := dbid.NewV7()
	edit, err := h.buildIssueWorkflowSquadEdit(
		r.Context(), current, *req.ExpectedRevision, params, handoff,
		h.wakeupSourceTaskID(r), req.SuppressRun, plannedActivityID, prepared, runStarted, nil,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode workflow handoff enqueue")
		return after, before, "", "", false
	}
	resp, body, transportOK := h.callRustWorkflowSquadHandoffPUT(w, r, userID, current, edit)
	if !transportOK {
		return after, before, "", "", false
	}
	if resp.StatusCode == http.StatusConflict {
		if conflict, recognized := parseRustWorkflowSquadConflict(body); recognized {
			if conflict.DispatchSensitive {
				writeErrorCode(w, http.StatusConflict, "status_route_changed", "task status requirements changed; reload before moving the task")
				return after, before, "", "", false
			}
			if conflict.ActualRevision > 0 {
				writeRevisionConflict(w, "issue", current.ID, *req.ExpectedRevision, conflict.ActualRevision)
				return after, before, "", "", false
			}
		}
		writeError(w, http.StatusBadGateway, "invalid rust workflow squad handoff revision conflict")
		return after, before, "", "", false
	}
	if resp.StatusCode != http.StatusOK {
		writeRustIssueResponse(w, resp, bytes.NewReader(body))
		return after, before, "", "", false
	}
	result, ok := h.validateRustWorkflowSquadHandoffWriteBody(
		w, r.Context(), current, *req.ExpectedRevision, params, plannedActivityID, prepared, body,
	)
	if !ok {
		return after, before, "", "", false
	}
	return result.after, result.before, result.statusActivityID, result.queuedTaskID, true
}

func rustWorkflowHandoffWriteSnapshotsValid(
	expectedRevision int64,
	params db.UpdateIssueParams,
	before, after ordinaryIssueSnapshot,
) bool {
	b := before.issue()
	a := after.issue()
	if b.Revision != expectedRevision {
		return false
	}
	if !params.Status.Valid || a.Status != params.Status.String {
		return false
	}
	if !params.AssigneeType.Valid || a.AssigneeType != params.AssigneeType {
		return false
	}
	if !params.AssigneeID.Valid || a.AssigneeID != params.AssigneeID {
		return false
	}
	statusChanged := b.Status != a.Status
	assigneeChanged := b.AssigneeType != a.AssigneeType || b.AssigneeID != a.AssigneeID
	if statusChanged || assigneeChanged {
		if a.Revision != b.Revision+1 || !a.UpdatedAt.Valid || !a.LastActivityAt.Valid {
			return false
		}
	} else if a.Revision != b.Revision {
		return false
	}
	return true
}

func (h *Handler) prepareWorkflowSquadHandoffNativeEnqueue(
	ctx context.Context,
	issue db.Issue,
	squad db.Squad,
	handoffNote string,
	actorType, actorID string,
	postRevision int64,
) (*service.IssuePreparedWorkflowHandoffEnqueue, error) {
	actorUserID := memberActorUserID(actorType, actorID)
	originator := h.squadLeaderIssueOriginator(ctx, actorType, actorID, issue)
	if !h.canEnqueueSquadLeader(ctx, squad.LeaderID, actorType, actorID, originator, uuidToString(issue.WorkspaceID)) {
		return nil, errNativeSquadLeaderGate
	}
	prep, err := h.TaskService.PrepareIssueWorkflowSquadHandoffEnqueue(
		ctx, issue, squad, handoffNote, actorUserID, postRevision,
	)
	if err != nil {
		return nil, err
	}
	return &prep, nil
}
