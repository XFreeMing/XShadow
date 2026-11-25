package handler

import (
	"net/http"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func (h *Handler) selectRustManualSourceContextDelete(r *http.Request, issue db.Issue, source db.IssueSourceContext) (rustIssueDeleteSelection, error) {
	var none rustIssueDeleteSelection
	if source.OriginTaskID.Valid || len(h.IssueReadBridge.DeleteSourceContextPilotScopes) == 0 {
		return none, nil
	}
	ctx := r.Context()
	inSourcePilot, err := matchesIssuePilot(ctx, h.Queries, h.IssueReadBridge.DeleteSourceContextPilotScopes, uuidToString(issue.WorkspaceID), issue.WorkspaceID)
	if err != nil || !inSourcePilot {
		return none, err
	}
	if issue.ProjectID.Valid {
		if len(h.IssueReadBridge.DeleteSourceProjectPilotScopes) == 0 {
			return none, nil
		}
		inProjectPilot, err := matchesIssuePilot(ctx, h.Queries, h.IssueReadBridge.DeleteSourceProjectPilotScopes, uuidToString(issue.WorkspaceID), issue.WorkspaceID)
		if err != nil || !inProjectPilot {
			return none, err
		}
	}
	contextFiles, noPendingCleanup, err := h.sourceContextFilesForDelete(ctx, source)
	if err != nil || !noPendingCleanup {
		return none, err
	}
	selection := rustIssueDeleteSelection{
		UseRust:         true,
		SourceContextID: source.ID,
	}
	if issue.ProjectID.Valid {
		selection.ExpectedProjectID = issue.ProjectID
	}
	if len(contextFiles) == 0 {
		return selection, nil
	}
	if len(h.IssueReadBridge.DeleteSourceContextFilesPilotScopes) == 0 {
		return none, nil
	}
	inFilesPilot, err := matchesIssuePilot(ctx, h.Queries, h.IssueReadBridge.DeleteSourceContextFilesPilotScopes, uuidToString(issue.WorkspaceID), issue.WorkspaceID)
	if err != nil || !inFilesPilot {
		return none, err
	}
	clonePlan := h.buildRustIssueDeleteSourceContextClonePlan(source, contextFiles)
	if len(clonePlan) == 0 {
		return none, nil
	}
	selection.SourceContextClonePlan = clonePlan
	return selection, nil
}
