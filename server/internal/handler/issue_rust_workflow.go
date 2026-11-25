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

type issueWorkflowSquadEdit struct {
	ExpectedRevision      int64               `json:"expected_revision"`
	Status                string              `json:"status"`
	AssigneeType          string              `json:"assignee_type"`
	AssigneeID            string              `json:"assignee_id"`
	Position              *float64            `json:"position,omitempty"`
	SourceTaskID          *string             `json:"source_task_id,omitempty"`
	SuppressRun           bool                `json:"suppress_run"`
	ActivityID            string              `json:"activity_id"`
	WorkflowPlan          workflowHandoffPlan `json:"workflow_plan"`
	ActivityHandoff       map[string]string   `json:"activity_handoff,omitempty"`
	PreparedWorkflowQueue *json.RawMessage    `json:"prepared_workflow_enqueue,omitempty"`
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
	projected := current
	projected.Status = params.Status.String
	projected.AssigneeType = params.AssigneeType
	projected.AssigneeID = params.AssigneeID
	edit := issueWorkflowSquadEdit{
		ExpectedRevision: *req.ExpectedRevision,
		Status:           params.Status.String,
		AssigneeType:     params.AssigneeType.String,
		AssigneeID:       uuidToString(params.AssigneeID),
		SourceTaskID:     uuidToPtr(h.wakeupSourceTaskID(r)),
		SuppressRun:      req.SuppressRun,
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
			h.workflowHandoffBrief(r.Context(), projected, handoff),
			runStarted,
		),
	}
	if params.Position.Valid {
		edit.Position = &params.Position.Float64
	}
	if prepared != nil {
		raw, err := marshalIssueWorkflowHandoffEnqueue(*prepared)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to encode workflow handoff enqueue")
			return after, before, "", "", false
		}
		rawMsg := json.RawMessage(raw)
		edit.PreparedWorkflowQueue = &rawMsg
	}
	payload, err := json.Marshal(edit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode workflow squad handoff edit")
		return after, before, "", "", false
	}
	path := "/internal/issues/" + uuidToString(current.ID) + "/workflow-squad-handoff"
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
				writeError(w, http.StatusBadGateway, "failed to read rust workflow squad handoff response")
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
		!rustWorkflowHandoffWriteSnapshotsValid(*req.ExpectedRevision, params, snapshots.Before, snapshots.After) {
		writeError(w, http.StatusBadGateway, "invalid rust workflow squad handoff snapshots")
		return after, before, "", "", false
	}
	beforeIssue := snapshots.Before.issue()
	afterIssue := snapshots.After.issue()
	statusChanged := beforeIssue.Status != afterIssue.Status
	receipt := derefString(snapshots.StatusActivityID)
	if !rustStatusActivityReceiptValid(r.Context(), h.Queries, plannedActivityID, current.WorkspaceID, current.ID, beforeIssue.Status, afterIssue.Status, statusChanged, receipt) {
		writeError(w, http.StatusBadGateway, "invalid rust workflow squad handoff activity receipt")
		return after, before, "", "", false
	}
	deferredReceipt := derefString(snapshots.DeferredTaskID)
	coalescedReceipt := derefString(snapshots.CoalescedDeferredTaskID)
	if prepared != nil {
		switch {
		case deferredReceipt != "" && coalescedReceipt != "":
			writeError(w, http.StatusBadGateway, "invalid rust workflow squad handoff queue receipt")
			return after, before, "", "", false
		case deferredReceipt != "":
			if !rustWorkflowDeferredTaskValid(r.Context(), h.Queries, deferredReceipt, *prepared) {
				writeError(w, http.StatusBadGateway, "invalid rust workflow squad handoff queue receipt")
				return after, before, "", "", false
			}
		case coalescedReceipt != "":
			if !rustWorkflowCoalescedDeferredTaskValid(r.Context(), h.Queries, coalescedReceipt, *prepared) {
				writeError(w, http.StatusBadGateway, "invalid rust workflow squad handoff queue receipt")
				return after, before, "", "", false
			}
		default:
			writeError(w, http.StatusBadGateway, "invalid rust workflow squad handoff queue receipt")
			return after, before, "", "", false
		}
	} else if deferredReceipt != "" || coalescedReceipt != "" {
		writeError(w, http.StatusBadGateway, "unexpected rust workflow squad handoff queue receipt")
		return after, before, "", "", false
	}
	taskReceipt := cmp.Or(deferredReceipt, coalescedReceipt)
	return afterIssue, beforeIssue, receipt, taskReceipt, true
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
