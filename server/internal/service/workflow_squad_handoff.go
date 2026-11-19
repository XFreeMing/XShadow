package service

import (
	"context"
	"errors"
	"github.com/multica-ai/multica/server/internal/attribution"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// EnqueueTaskForWorkflowSquadLeader preserves each stage handoff even when an
// older stage or another squad already holds the leader's pending-task slot.
func (s *TaskService) EnqueueTaskForWorkflowSquadLeader(ctx context.Context, issue db.Issue, leaderID, squadID pgtype.UUID, handoffNote string, actorUserID pgtype.UUID) (db.AgentTaskQueue, error) {
	return s.enqueueMentionTaskWithCommentPlan(ctx, issue, leaderID, pgtype.UUID{}, nil, true, squadID, false, handoffNote, actorUserID, pgtype.UUID{}, OriginDerived, false, true)
}

func (s *TaskService) createWorkflowSquadHandoffTask(ctx context.Context, issue db.Issue, params db.CreateAgentTaskParams) (db.AgentTaskQueue, error) {
	if s.TxStarter == nil {
		return db.AgentTaskQueue{}, errors.New("workflow squad handoff requires a transaction")
	}
	// Deferred rows can coexist with an occupied queued/dispatched slot. The
	// existing promoter releases them when that slot frees; never rewrite the
	// binding, role, comments or attribution of a task already offered to a daemon.
	params.FireAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
	params.WorkflowStep = pgtype.Text{String: issue.Status, Valid: true}
	params.WorkflowHandoffRevision = pgtype.Int8{Int64: issue.Revision, Valid: true}
	var task db.AgentTaskQueue
	err := s.runInTx(ctx, func(q *db.Queries) error {
		if err := q.LockWorkflowSquadHandoff(ctx, issue.ID); err != nil {
			return err
		}
		_, err := q.GetWorkflowSquadHandoffTask(ctx, db.GetWorkflowSquadHandoffTaskParams{
			IssueID: issue.ID, AgentID: params.AgentID, SquadID: params.SquadID,
			IssueRevision: issue.Revision,
		})
		if err == nil {
			return ErrDuplicatePendingTask
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		task, err = q.CreateAgentTask(ctx, params)
		return err
	})
	return task, err
}

// CreateWorkMethodHandoff writes the next stage's task alongside the issue
// revision that selected it. The due row survives a currently occupied slot.
func (s *TaskService) CreateWorkMethodHandoff(ctx context.Context, q *db.Queries, issue db.Issue, agentID, squadID pgtype.UUID, methodRevision int, note string, actorUserID pgtype.UUID, parent *db.AgentTaskQueue) (db.AgentTaskQueue, error) {
	scoped := &TaskService{Queries: q, Composio: s.Composio, FeatureFlags: s.FeatureFlags}
	attr := attribution.ClassifyDirect(attribution.DirectFacts{IssueID: issue.ID, ActorUserID: actorUserID})
	if parent != nil {
		attr = attribution.DelegatedIssueRun(issue.ID, parent.ID, parent.OriginatorUserID, parent.AccountableUserID)
	}
	params, err := scoped.prepareMentionTask(ctx, issue, agentID, pgtype.UUID{}, nil, squadID.Valid, squadID, false, note, attr, pgtype.UUID{}, OriginDerived, false)
	if err != nil {
		return db.AgentTaskQueue{}, err
	}
	params.FireAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
	params.WorkMethodRevision = pgtype.Int8{Int64: int64(methodRevision), Valid: true}
	return q.CreateAgentTask(ctx, params)
}

// PublishWorkMethodHandoff wakes the runtime after the method transition commits.
func (s *TaskService) PublishWorkMethodHandoff(ctx context.Context, task db.AgentTaskQueue) error {
	s.BroadcastTaskQueued(ctx, task)
	s.notifyTaskAvailable(task)
	return s.PromoteDueDeferredTasksForRuntime(ctx, task.RuntimeID)
}
