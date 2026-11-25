package handler

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

var errNativeSquadLeaderGate = errors.New("native squad leader invocation gate")

// loadMemberOwnedSquad resolves a squad whose leader is owned by memberUserID.
// When requireReady is set, the leader must also be enqueue-ready.
func (h *Handler) loadMemberOwnedSquad(
	ctx context.Context,
	wsUUID, squadID pgtype.UUID,
	memberUserID string,
	requireReady bool,
) (db.Squad, db.Agent, bool) {
	squad, err := h.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{
		ID: squadID, WorkspaceID: wsUUID,
	})
	if err != nil || squad.ArchivedAt.Valid {
		return db.Squad{}, db.Agent{}, false
	}
	leader, err := h.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
		ID: squad.LeaderID, WorkspaceID: wsUUID,
	})
	if err != nil || leader.ArchivedAt.Valid || uuidToString(leader.OwnerID) != memberUserID {
		return db.Squad{}, db.Agent{}, false
	}
	if requireReady {
		verdict, err := service.AgentReadiness(ctx, h.runtimeLookup(uuidToString(wsUUID)), leader)
		if err != nil || !verdict.Ready() {
			return db.Squad{}, db.Agent{}, false
		}
	}
	return squad, leader, true
}

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
