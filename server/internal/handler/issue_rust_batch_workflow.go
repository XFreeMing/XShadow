package handler

import (
	"bytes"
	"encoding/json"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/issueworkflow"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

// batchWorkflowSquadHandoffPilotActive reports whether the batch request may
// consider native squad workflow handoffs per row.
func (h *Handler) batchWorkflowSquadHandoffPilotActive(
	r *http.Request,
	workspaceID pgtype.UUID,
	rawUpdates map[string]json.RawMessage,
	updates UpdateIssueRequest,
) (bool, error) {
	if h.IssueReadBridge == nil ||
		len(h.IssueReadBridge.BatchWorkflowPilotScopes) == 0 ||
		len(h.IssueReadBridge.WorkflowPilotScopes) == 0 ||
		len(h.IssueReadBridge.StatusPilotScopes) == 0 {
		return false, nil
	}
	if updates.Status == nil {
		return false, nil
	}
	if updates.HandoffNote != "" || updates.StopPreviousAssigneeRuns || updates.EnterWorkflowStep {
		return false, nil
	}
	actorType, _ := h.resolveActor(r, requestUserID(r), uuidToString(workspaceID))
	if actorType != "member" {
		return false, nil
	}
	for key := range rawUpdates {
		switch key {
		case "status", "suppress_run":
		default:
			return false, nil
		}
	}
	batchPilot, err := matchesIssuePilot(
		r.Context(), h.Queries, h.IssueReadBridge.BatchWorkflowPilotScopes,
		uuidToString(workspaceID), workspaceID,
	)
	if err != nil || !batchPilot {
		return false, err
	}
	workflowPilot, err := matchesIssuePilot(
		r.Context(), h.Queries, h.IssueReadBridge.WorkflowPilotScopes,
		uuidToString(workspaceID), workspaceID,
	)
	if err != nil || !workflowPilot {
		return false, err
	}
	return matchesIssuePilot(r.Context(), h.Queries, h.IssueReadBridge.StatusPilotScopes, uuidToString(workspaceID), workspaceID)
}

func batchWorkflowSquadHandoffIssueEligible(
	prev db.Issue,
	targetStatus string,
	handoff *workflowHandoff,
	suppressRun bool,
	callerRun *db.AgentTaskQueue,
) bool {
	if handoff == nil ||
		handoff.def.IsDefault ||
		handoff.step.Handler.Type != issueworkflow.HandlerSquad {
		return false
	}
	if !batchStatusRowEligibleForRust(prev) ||
		!isActiveBuiltInIssueStatus(targetStatus) ||
		!issuestatus.IsBuiltIn(prev.Status) {
		return false
	}
	if statusEditDispatchSensitive(prev, targetStatus, UpdateIssueRequest{SuppressRun: suppressRun}) {
		return false
	}
	if statusEditNeedsGoRunAdvance(callerRun, prev, targetStatus) {
		return false
	}
	if prev.Status == issuestatus.Cancelled && prev.DuplicateOfIssueID.Valid &&
		isActiveBuiltInIssueStatus(targetStatus) {
		return false
	}
	return prev.ProjectID.Valid
}

type batchWorkflowRustOutcome int

const (
	batchWorkflowRustUseGo batchWorkflowRustOutcome = iota
	batchWorkflowRustDone
	batchWorkflowRustAbort
	batchWorkflowRustSkipRow
)

func (h *Handler) tryBatchUpdateIssueWorkflowSquadHandoffViaRust(
	w http.ResponseWriter,
	r *http.Request,
	userID string,
	prev db.Issue,
	params db.UpdateIssueParams,
	handoff *workflowHandoff,
	suppressRun bool,
	actorType, actorID string,
) (batchWorkflowRustOutcome, rustWorkflowWriteResult) {
	squad, leader, ok := h.loadMemberOwnedSquad(r.Context(), prev.WorkspaceID, params.AssigneeID, userID, true)
	if !ok {
		return batchWorkflowRustUseGo, rustWorkflowWriteResult{}
	}

	projected := prev
	projected.Status = params.Status.String
	projected.AssigneeType = params.AssigneeType
	projected.AssigneeID = params.AssigneeID
	_, workflowWillRun := h.IssueService.WillEnqueueRun(r.Context(),
		service.IssueTriggerInput{
			Issue:              projected,
			PrevStatus:         prev.Status,
			StatusChanged:      true,
			AssigneeChanged:    true,
			EnteredHandoffStep: true,
		},
		h.issueTriggerWriteProbe(r, actorType, actorID, projected),
	)
	workflowWillRun = workflowWillRun && !suppressRun
	var prepared *service.IssuePreparedWorkflowHandoffEnqueue
	if workflowWillRun {
		postRevision := prev.Revision + 1
		prep, prepErr := h.prepareWorkflowSquadHandoffNativeEnqueue(
			r.Context(), projected, squad, "", actorType, actorID, postRevision,
		)
		if prepErr != nil {
			return batchWorkflowRustUseGo, rustWorkflowWriteResult{}
		}
		prepared = prep
	}

	plannedActivityID := dbid.NewV7()
	batchTrust := &workflowSquadBatchTrust{
		LeaderID: uuidToString(leader.ID), RuntimeID: uuidToString(leader.RuntimeID),
	}
	edit, err := h.buildIssueWorkflowSquadEdit(
		r.Context(), prev, prev.Revision, params, handoff,
		h.wakeupSourceTaskID(r), suppressRun, plannedActivityID, prepared, workflowWillRun, batchTrust,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode workflow handoff enqueue")
		return batchWorkflowRustAbort, rustWorkflowWriteResult{}
	}
	resp, body, transportOK := h.callRustWorkflowSquadHandoffPUT(w, r, userID, prev, edit)
	if !transportOK {
		return batchWorkflowRustAbort, rustWorkflowWriteResult{}
	}
	switch classifyBatchWorkflowSquadHandoffHTTP(resp.StatusCode, body) {
	case batchWorkflowRustSkipRow:
		return batchWorkflowRustSkipRow, rustWorkflowWriteResult{}
	case batchWorkflowRustAbort:
		switch {
		case resp.StatusCode == http.StatusConflict:
			writeError(w, http.StatusBadGateway, "invalid rust workflow squad handoff revision conflict")
		case resp.StatusCode < http.StatusBadRequest:
			writeError(w, http.StatusBadGateway, "invalid rust batch workflow handoff response")
		default:
			writeRustIssueResponse(w, resp, bytes.NewReader(body))
		}
		return batchWorkflowRustAbort, rustWorkflowWriteResult{}
	case batchWorkflowRustDone:
		result, ok := h.validateRustWorkflowSquadHandoffWriteBody(
			w, r.Context(), prev, prev.Revision, params, plannedActivityID, prepared, body,
		)
		if !ok {
			return batchWorkflowRustAbort, rustWorkflowWriteResult{}
		}
		return batchWorkflowRustDone, result
	default:
		return batchWorkflowRustAbort, rustWorkflowWriteResult{}
	}
}

func classifyBatchWorkflowSquadHandoffHTTP(statusCode int, body []byte) batchWorkflowRustOutcome {
	switch statusCode {
	case http.StatusNotFound, http.StatusForbidden, http.StatusBadRequest:
		return batchWorkflowRustSkipRow
	case http.StatusConflict:
		if _, recognized := parseRustWorkflowSquadConflict(body); recognized {
			return batchWorkflowRustSkipRow
		}
		return batchWorkflowRustAbort
	case http.StatusOK:
		return batchWorkflowRustDone
	default:
		return batchWorkflowRustAbort
	}
}
