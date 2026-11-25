package handler

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var terminalCloseCancellationErrors = map[string]struct{}{
	"Issue closed; wakeup disabled":      {},
	"Issue closed; continuation stopped": {},
}

type issueTerminalScope struct {
	ProjectID *string `json:"project_id"`
}

type rustCancelledTaskReceipt struct {
	ID      string `json:"id"`
	IssueID string `json:"issue_id"`
	AgentID string `json:"agent_id"`
	Status  string `json:"status"`
	Error   string `json:"error"`
}

func rustTerminalCancelledTasksValid(
	ctx context.Context,
	q *db.Queries,
	issueID, workspaceID pgtype.UUID,
	receipts []rustCancelledTaskReceipt,
) ([]db.AgentTaskQueue, bool) {
	issueStr := uuidToString(issueID)
	out := make([]db.AgentTaskQueue, 0, len(receipts))
	seen := make(map[string]struct{}, len(receipts))
	for _, receipt := range receipts {
		if receipt.ID == "" || receipt.IssueID != issueStr || receipt.Status != "cancelled" {
			return nil, false
		}
		if _, ok := terminalCloseCancellationErrors[receipt.Error]; !ok {
			return nil, false
		}
		if _, dup := seen[receipt.ID]; dup {
			return nil, false
		}
		seen[receipt.ID] = struct{}{}
		taskID, err := util.ParseUUID(receipt.ID)
		if err != nil {
			return nil, false
		}
		row, err := q.GetAgentTaskInWorkspace(ctx, db.GetAgentTaskInWorkspaceParams{
			ID: taskID, WorkspaceID: workspaceID,
		})
		if err != nil {
			return nil, false
		}
		if row.IssueID != issueID || uuidToString(row.AgentID) != receipt.AgentID {
			return nil, false
		}
		if row.Status != "cancelled" || row.Error.String != receipt.Error {
			return nil, false
		}
		if row.StartedAt.Valid || !row.CompletedAt.Valid {
			return nil, false
		}
		if len(row.Context) == 0 {
			return nil, false
		}
		var payload map[string]any
		if json.Unmarshal(row.Context, &payload) != nil {
			return nil, false
		}
		wakeup := payload["wakeup_id"] != nil
		_, continuation := payload["continuation_gap"]
		if !wakeup && !continuation {
			return nil, false
		}
		if continuation && receipt.Error != "Issue closed; continuation stopped" {
			return nil, false
		}
		if wakeup && !continuation && receipt.Error != "Issue closed; wakeup disabled" {
			return nil, false
		}
		out = append(out, row)
	}
	return out, true
}

// terminalStatusIssueEditUsesRust selects ordinary builtin done/cancelled status
// moves the native writer can own when the terminal pilot and status pilot both
// match. Work-method rows, triage, named project workflows, duplicate reopen,
// and mixed payloads stay on Go.
func (h *Handler) terminalStatusIssueEditUsesRust(
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
		len(h.IssueReadBridge.TerminalStatusPilotScopes) == 0 {
		return false, nil
	}
	if req.ExpectedRevision == nil || req.Status == nil {
		return false, nil
	}
	if handoff != nil {
		return false, nil
	}
	if statusKeyForGuard != issuestatus.Done && statusKeyForGuard != issuestatus.Cancelled {
		return false, nil
	}
	if _, dup := rawFields["duplicate_of_issue_id"]; dup {
		return false, nil
	}
	if !issuestatus.IsBuiltIn(prev.Status) {
		return false, nil
	}
	if prev.TriageState.Valid || len(prev.WorkMethodRun) > 0 {
		return false, nil
	}
	actorType, _ := h.resolveActor(r, requestUserID(r), uuidToString(workspaceID))
	if actorType != "member" || statusEditNeedsGoRunAdvance(callerRun, prev, statusKeyForGuard) ||
		prev.DuplicateOfIssueID.Valid {
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
		return false, err
	}
	inTerminal, err := matchesIssuePilot(r.Context(), h.Queries, h.IssueReadBridge.TerminalStatusPilotScopes, uuidToString(workspaceID), workspaceID)
	if err != nil || !inTerminal || !prev.ProjectID.Valid {
		return inTerminal, err
	}
	project, err := h.Queries.GetProjectInWorkspace(r.Context(), db.GetProjectInWorkspaceParams{
		ID: prev.ProjectID, WorkspaceID: workspaceID,
	})
	return err == nil && !project.WorkflowID.Valid, err
}
