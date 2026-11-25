package handler

import (
	"context"
	"errors"
	"fmt"

	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var errNativeSquadLeaderGate = errors.New("native squad leader invocation gate")

// prepareNativeIssueEnqueue builds the trusted queue row for a native assign or
// promotion when the write will enqueue. Squad plans pass the same
// canEnqueueSquadLeader gate enqueueSquadLeaderTask uses before prepare.
func (h *Handler) prepareNativeIssueEnqueue(
	ctx context.Context,
	issue db.Issue,
	handoffNote string,
	actorType, actorID string,
) (*service.IssuePreparedAgentEnqueue, error) {
	actorUserID := memberActorUserID(actorType, actorID)
	switch issue.AssigneeType.String {
	case "agent":
		prep, err := h.TaskService.PrepareIssueAgentEnqueue(ctx, issue, handoffNote, actorUserID)
		if err != nil {
			return nil, err
		}
		return &prep, nil
	case "squad":
		squad, err := h.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{
			ID: issue.AssigneeID, WorkspaceID: issue.WorkspaceID,
		})
		if err != nil {
			return nil, fmt.Errorf("load squad for native enqueue: %w", err)
		}
		originator := h.squadLeaderIssueOriginator(ctx, actorType, actorID, issue)
		workspaceID := uuidToString(issue.WorkspaceID)
		if !h.canEnqueueSquadLeader(ctx, squad.LeaderID, actorType, actorID, originator, workspaceID) {
			return nil, errNativeSquadLeaderGate
		}
		prep, err := h.TaskService.PrepareIssueSquadLeaderEnqueue(ctx, issue, squad, handoffNote, actorUserID)
		if err != nil {
			return nil, err
		}
		return &prep, nil
	default:
		return nil, fmt.Errorf("issue has no runnable assignee")
	}
}
