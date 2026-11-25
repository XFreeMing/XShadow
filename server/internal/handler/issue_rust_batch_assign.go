package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

// batchOwnedDirectAgentAssignPilotActive reports whether the batch request may
// consider native owned-direct assignment per row. It does not route the whole
// batch away from Go.
func (h *Handler) batchOwnedDirectAgentAssignPilotActive(
	r *http.Request,
	workspaceID pgtype.UUID,
	rawUpdates map[string]json.RawMessage,
	updates UpdateIssueRequest,
) (bool, error) {
	if h.IssueReadBridge == nil ||
		len(h.IssueReadBridge.BatchAssignPilotScopes) == 0 ||
		len(h.IssueReadBridge.AssignPilotScopes) == 0 {
		return false, nil
	}
	if len(rawUpdates) == 0 {
		return false, nil
	}
	actorType, _ := h.resolveActor(r, requestUserID(r), uuidToString(workspaceID))
	if actorType != "member" {
		return false, nil
	}
	_, touchedType := rawUpdates["assignee_type"]
	_, touchedID := rawUpdates["assignee_id"]
	if !touchedType || !touchedID {
		return false, nil
	}
	if updates.AssigneeType == nil || *updates.AssigneeType != "agent" {
		return false, nil
	}
	for key := range rawUpdates {
		switch key {
		case "assignee_type", "assignee_id", "suppress_run":
		default:
			return false, nil
		}
	}
	batchPilot, err := matchesIssuePilot(r.Context(), h.Queries, h.IssueReadBridge.BatchAssignPilotScopes, uuidToString(workspaceID), workspaceID)
	if err != nil || !batchPilot {
		return false, err
	}
	return matchesIssuePilot(r.Context(), h.Queries, h.IssueReadBridge.AssignPilotScopes, uuidToString(workspaceID), workspaceID)
}

func batchAssignRowEligibleForRust(prev db.Issue, assigneeType pgtype.Text, assigneeID pgtype.UUID) bool {
	if !assigneeType.Valid || assigneeType.String != "agent" || !assigneeID.Valid {
		return false
	}
	if !assignEditSourceAllowed(prev) {
		return false
	}
	if !isActiveBuiltInIssueStatus(prev.Status) {
		return false
	}
	return !prev.TriageState.Valid
}

func (h *Handler) batchAssignIssueUsesOwnedNative(
	ctx context.Context,
	memberID string,
	prev db.Issue,
	params db.UpdateIssueParams,
) bool {
	if !batchAssignRowEligibleForRust(prev, params.AssigneeType, params.AssigneeID) {
		return false
	}
	if !h.batchAssignAgentOwnedByMember(ctx, prev.WorkspaceID, params.AssigneeID, memberID) {
		return false
	}
	if !prev.ProjectID.Valid {
		return true
	}
	project, err := h.Queries.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{
		ID: prev.ProjectID, WorkspaceID: prev.WorkspaceID,
	})
	return err == nil && !project.WorkflowID.Valid
}

func (h *Handler) batchAssignAgentOwnedByMember(ctx context.Context, wsUUID, agentID pgtype.UUID, memberUserID string) bool {
	agent, err := h.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
		ID: agentID, WorkspaceID: wsUUID,
	})
	if err != nil || !agent.OwnerID.Valid {
		return false
	}
	return uuidToString(agent.OwnerID) == memberUserID
}

type batchAssignRustOutcome int

const (
	batchAssignRustUseGo batchAssignRustOutcome = iota
	batchAssignRustDone
	batchAssignRustAbort
	batchAssignRustSkipRow
)

func (h *Handler) tryBatchUpdateIssueAssignViaRust(
	w http.ResponseWriter,
	r *http.Request,
	userID string,
	prev db.Issue,
	params db.UpdateIssueParams,
	suppressRun bool,
	actorType, actorID string,
) (batchAssignRustOutcome, rustAssignWriteResult) {
	projected := prev
	projected.AssigneeType = params.AssigneeType
	projected.AssigneeID = params.AssigneeID
	assigneeChanged := prev.AssigneeType != params.AssigneeType || prev.AssigneeID != params.AssigneeID
	var assignPrepared *service.IssuePreparedAgentEnqueue
	if !suppressRun && prev.Status != issuestatus.Backlog && assigneeChanged {
		_, assignWillRun := h.IssueService.WillEnqueueRun(r.Context(),
			service.IssueTriggerInput{
				Issue:           projected,
				PrevStatus:      prev.Status,
				AssigneeChanged: true,
			},
			h.issueTriggerWriteProbe(r, actorType, actorID, projected),
		)
		if !assignWillRun {
			return batchAssignRustUseGo, rustAssignWriteResult{}
		}
		prep, err := h.prepareNativeIssueEnqueue(r.Context(), projected, "", actorType, actorID)
		if err != nil {
			return batchAssignRustUseGo, rustAssignWriteResult{}
		}
		assignPrepared = prep
	}

	plannedActivityID := dbid.NewV7()
	edit := issueAssignEdit{
		ExpectedRevision: prev.Revision,
		AssigneeType:     params.AssigneeType.String,
		AssigneeID:       uuidToString(params.AssigneeID),
		SourceTaskID:     uuidToPtr(h.wakeupSourceTaskID(r)),
		SuppressRun:      suppressRun,
		ActivityID:       uuidToString(plannedActivityID),
		OwnedDirectBatch: &ownedDirectBatchTrust{ExpectedProjectID: uuidToPtr(prev.ProjectID)},
	}
	if assignPrepared != nil {
		raw, err := marshalIssueStatusPreparedEnqueue(*assignPrepared)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to encode issue assign enqueue")
			return batchAssignRustAbort, rustAssignWriteResult{}
		}
		rawMsg := json.RawMessage(raw)
		edit.PreparedEnqueue = &rawMsg
	}
	payload, err := json.Marshal(edit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode issue assign edit")
		return batchAssignRustAbort, rustAssignWriteResult{}
	}
	path := "/internal/issues/" + uuidToString(prev.ID) + "/assign"
	resp, requested := h.requestRustIssue(w, r, http.MethodPut, path, uuidToString(prev.WorkspaceID), userID, payload)
	if !requested {
		return batchAssignRustAbort, rustAssignWriteResult{}
	}
	limit := rustAssignResponseLimit(prev, resp.StatusCode)
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	_ = resp.Body.Close()
	if err != nil || int64(len(body)) > limit {
		if writeQueryCancellation(w, r, err) {
			return batchAssignRustAbort, rustAssignWriteResult{}
		}
		writeError(w, http.StatusBadGateway, "failed to read rust issue assign response")
		return batchAssignRustAbort, rustAssignWriteResult{}
	}
	switch resp.StatusCode {
	case http.StatusNotFound, http.StatusForbidden, http.StatusBadRequest, http.StatusConflict:
		return batchAssignRustSkipRow, rustAssignWriteResult{}
	case http.StatusInternalServerError:
		slog.Warn("batch owned-direct issue assign failed", "issue_id", uuidToString(prev.ID), "status", resp.StatusCode)
		return batchAssignRustSkipRow, rustAssignWriteResult{}
	case http.StatusOK:
		result, receiptErr := h.validateRustAssignWriteReceipt(
			r.Context(), plannedActivityID, prev, prev.Revision,
			params.AssigneeType, params.AssigneeID, assignPrepared, body,
		)
		if receiptErr != nil {
			writeError(w, http.StatusBadGateway, receiptErr.Error())
			return batchAssignRustAbort, rustAssignWriteResult{}
		}
		return batchAssignRustDone, result
	default:
		writeRustIssueResponse(w, resp, bytes.NewReader(body))
		return batchAssignRustAbort, rustAssignWriteResult{}
	}
}
