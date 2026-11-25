package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

// batchOrdinaryActiveStatusPilotActive reports whether the batch request may
// consider native ordinary active status updates per row. It does not route the
// whole batch away from Go.
func (h *Handler) batchOrdinaryActiveStatusPilotActive(
	r *http.Request,
	workspaceID pgtype.UUID,
	rawUpdates map[string]json.RawMessage,
	updates UpdateIssueRequest,
	targetStatus string,
) (bool, error) {
	if h.IssueReadBridge == nil ||
		len(h.IssueReadBridge.BatchStatusPilotScopes) == 0 ||
		len(h.IssueReadBridge.StatusPilotScopes) == 0 {
		return false, nil
	}
	if updates.Status == nil || !isActiveBuiltInIssueStatus(targetStatus) {
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
	batchPilot, err := matchesIssuePilot(r.Context(), h.Queries, h.IssueReadBridge.BatchStatusPilotScopes, uuidToString(workspaceID), workspaceID)
	if err != nil || !batchPilot {
		return false, err
	}
	return matchesIssuePilot(r.Context(), h.Queries, h.IssueReadBridge.StatusPilotScopes, uuidToString(workspaceID), workspaceID)
}

func batchStatusRowEligibleForRust(prev db.Issue) bool {
	return isActiveBuiltInIssueStatus(prev.Status) && !prev.TriageState.Valid &&
		len(prev.WorkMethodRun) == 0 && assignEditSourceAllowed(prev)
}

func (h *Handler) batchStatusIssueUsesRust(
	ctx context.Context,
	prev db.Issue,
	targetStatus string,
	handoff *workflowHandoff,
	suppressRun bool,
) bool {
	if handoff != nil || !isActiveBuiltInIssueStatus(targetStatus) || !batchStatusRowEligibleForRust(prev) {
		return false
	}
	if statusEditDispatchSensitive(prev, targetStatus, UpdateIssueRequest{SuppressRun: suppressRun}) {
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

type batchStatusRustOutcome int

const (
	batchStatusRustDone batchStatusRustOutcome = iota
	batchStatusRustAbort
	batchStatusRustSkipRow
)

func (h *Handler) tryBatchUpdateIssueStatusViaRust(
	w http.ResponseWriter,
	r *http.Request,
	userID string,
	prev db.Issue,
	targetStatus string,
	suppressRun bool,
	terminalBatch bool,
) (batchStatusRustOutcome, rustStatusWriteResult) {
	plannedActivityID := dbid.NewV7()
	edit := issueStatusEdit{
		ExpectedRevision: prev.Revision,
		Status:           targetStatus,
		SourceTaskID:     uuidToPtr(h.wakeupSourceTaskID(r)),
		SuppressRun:      suppressRun,
		ActivityID:       uuidToString(plannedActivityID),
	}
	if terminalBatch {
		edit.TerminalScope = &issueTerminalScope{ProjectID: uuidToPtr(prev.ProjectID)}
		edit.OrdinaryTerminalBatch = true
	} else {
		edit.OrdinaryActiveBatch = &ordinaryActiveBatchTrust{
			ExpectedProjectID: uuidToPtr(prev.ProjectID),
		}
	}
	resp, body, transportOK := h.callRustIssueStatusPUT(w, r, userID, prev, edit)
	if !transportOK {
		return batchStatusRustAbort, rustStatusWriteResult{}
	}
	switch resp.StatusCode {
	case http.StatusNotFound, http.StatusForbidden, http.StatusBadRequest:
		return batchStatusRustSkipRow, rustStatusWriteResult{}
	case http.StatusConflict:
		if _, recognized := parseRustIssueStatusConflict(body); recognized {
			return batchStatusRustSkipRow, rustStatusWriteResult{}
		}
		writeError(w, http.StatusBadGateway, "invalid rust issue status revision conflict")
		return batchStatusRustAbort, rustStatusWriteResult{}
	case http.StatusOK:
		receipt, decoded := decodeRustIssueStatusWriteReceipt(body, prev.ID, prev.WorkspaceID)
		if !decoded {
			writeError(w, http.StatusBadGateway, "invalid rust issue status snapshots")
			return batchStatusRustAbort, rustStatusWriteResult{}
		}
		if receipt.hasQueue() || (!terminalBatch && len(receipt.CancelledTasks) > 0) {
			writeError(w, http.StatusBadGateway, "invalid rust issue status snapshots")
			return batchStatusRustAbort, rustStatusWriteResult{}
		}
		result, ok := h.checkRustIssueStatusReceipt(w, r.Context(), prev, edit, plannedActivityID, receipt)
		if !ok {
			return batchStatusRustAbort, rustStatusWriteResult{}
		}
		return batchStatusRustDone, result
	default:
		writeRustIssueResponse(w, resp, bytes.NewReader(body))
		return batchStatusRustAbort, rustStatusWriteResult{}
	}
}
