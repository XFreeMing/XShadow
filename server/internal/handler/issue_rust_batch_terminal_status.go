package handler

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/issuestatus"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func isTerminalBuiltInIssueStatus(key string) bool {
	return key == issuestatus.Done || key == issuestatus.Cancelled
}

// batchOrdinaryTerminalStatusPilotActive reports whether the batch request may
// consider native builtin done/cancelled updates per row.
func (h *Handler) batchOrdinaryTerminalStatusPilotActive(
	r *http.Request,
	workspaceID pgtype.UUID,
	rawUpdates map[string]json.RawMessage,
	updates UpdateIssueRequest,
	targetStatus string,
) (bool, error) {
	if h.IssueReadBridge == nil ||
		len(h.IssueReadBridge.BatchTerminalPilotScopes) == 0 ||
		len(h.IssueReadBridge.TerminalStatusPilotScopes) == 0 ||
		len(h.IssueReadBridge.StatusPilotScopes) == 0 {
		return false, nil
	}
	if updates.Status == nil || !isTerminalBuiltInIssueStatus(targetStatus) {
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
		r.Context(), h.Queries, h.IssueReadBridge.BatchTerminalPilotScopes,
		uuidToString(workspaceID), workspaceID,
	)
	if err != nil || !batchPilot {
		return false, err
	}
	terminalPilot, err := matchesIssuePilot(
		r.Context(), h.Queries, h.IssueReadBridge.TerminalStatusPilotScopes,
		uuidToString(workspaceID), workspaceID,
	)
	if err != nil || !terminalPilot {
		return false, err
	}
	return matchesIssuePilot(r.Context(), h.Queries, h.IssueReadBridge.StatusPilotScopes, uuidToString(workspaceID), workspaceID)
}

func batchTerminalStatusRowEligibleForRust(prev db.Issue, targetStatus string) bool {
	if !issuestatus.IsBuiltIn(prev.Status) || !isTerminalBuiltInIssueStatus(targetStatus) {
		return false
	}
	if prev.DuplicateOfIssueID.Valid {
		return false
	}
	return !prev.TriageState.Valid && len(prev.WorkMethodRun) == 0 && assignEditSourceAllowed(prev)
}

func (h *Handler) batchTerminalStatusIssueUsesRust(
	ctx context.Context,
	prev db.Issue,
	targetStatus string,
	handoff *workflowHandoff,
) bool {
	if handoff != nil || !batchTerminalStatusRowEligibleForRust(prev, targetStatus) {
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
