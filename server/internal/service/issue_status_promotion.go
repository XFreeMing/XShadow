package service

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/attribution"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

var errIssueNotDirectAgent = fmt.Errorf("issue is not assigned to a direct agent")

var errIssueNotSquadAssignee = fmt.Errorf("issue is not assigned to a squad")

// IssuePreparedAgentEnqueue is the trusted queue insert Go prepares for a
// direct-agent write the Rust pilot commits atomically with the issue row.
type IssuePreparedAgentEnqueue struct {
	Task            db.CreateAgentTaskParams
	QueueKeyHeadSha string
}

// IssuePreparedWorkflowHandoffEnqueue is the trusted deferred queue insert for a
// squad workflow handoff the Rust writer commits with the issue row.
type IssuePreparedWorkflowHandoffEnqueue struct {
	Task                    db.CreateAgentTaskParams
	QueueKeyHeadSha         string
	WorkflowHandoffRevision int64
}

// PrepareIssueAgentEnqueue mirrors the direct-agent assign/promote enqueue plan
// without writing the queue row. WorkflowStep is stamped from the projected issue
// status so the native insert matches the Go CreateAgentTask path.
func (s *TaskService) PrepareIssueAgentEnqueue(
	ctx context.Context,
	issue db.Issue,
	handoffNote string,
	actorUserID pgtype.UUID,
) (IssuePreparedAgentEnqueue, error) {
	if !issue.AssigneeID.Valid || issue.AssigneeType.String != "agent" {
		return IssuePreparedAgentEnqueue{}, errIssueNotDirectAgent
	}
	params, headSha, err := s.prepareIssueAssigneeEnqueueParams(
		ctx, issue, pgtype.UUID{}, nil, false, handoffNote, actorUserID,
		pgtype.UUID{}, OriginDerived, false,
	)
	if err != nil {
		return IssuePreparedAgentEnqueue{}, err
	}
	params.WorkflowStep = pgtype.Text{String: issue.Status, Valid: true}
	return IssuePreparedAgentEnqueue{Task: params, QueueKeyHeadSha: headSha}, nil
}

// PrepareIssueSquadLeaderEnqueue mirrors the ordinary squad assign/promote
// leader enqueue plan without writing the queue row. The prepared task targets
// the squad's leader with is_leader_task and squad_id stamped like
// EnqueueTaskForSquadLeaderWithHandoff.
func (s *TaskService) PrepareIssueSquadLeaderEnqueue(
	ctx context.Context,
	issue db.Issue,
	squad db.Squad,
	handoffNote string,
	actorUserID pgtype.UUID,
) (IssuePreparedAgentEnqueue, error) {
	if !issue.AssigneeID.Valid || issue.AssigneeType.String != "squad" ||
		squad.ID != issue.AssigneeID || squad.WorkspaceID != issue.WorkspaceID {
		return IssuePreparedAgentEnqueue{}, errIssueNotSquadAssignee
	}
	if squad.ArchivedAt.Valid {
		return IssuePreparedAgentEnqueue{}, fmt.Errorf("squad is archived")
	}
	attr := s.attributionForIssueTask(ctx, issue, pgtype.UUID{}, attribution.SourceDelegation, actorUserID)
	params, err := s.prepareMentionTask(
		ctx, issue, squad.LeaderID, pgtype.UUID{}, nil, true, issue.AssigneeID,
		false, handoffNote, attr, pgtype.UUID{}, OriginDerived, false,
	)
	if err != nil {
		return IssuePreparedAgentEnqueue{}, err
	}
	params.WorkflowStep = pgtype.Text{String: issue.Status, Valid: true}
	return IssuePreparedAgentEnqueue{Task: params, QueueKeyHeadSha: params.HeadSha.String}, nil
}

// PrepareIssueWorkflowSquadHandoffEnqueue mirrors EnqueueTaskForWorkflowSquadLeader
// without writing the deferred queue row.
func (s *TaskService) PrepareIssueWorkflowSquadHandoffEnqueue(
	ctx context.Context,
	issue db.Issue,
	squad db.Squad,
	handoffNote string,
	actorUserID pgtype.UUID,
	postRevision int64,
) (IssuePreparedWorkflowHandoffEnqueue, error) {
	if !issue.AssigneeID.Valid || issue.AssigneeType.String != "squad" ||
		squad.ID != issue.AssigneeID || squad.WorkspaceID != issue.WorkspaceID {
		return IssuePreparedWorkflowHandoffEnqueue{}, errIssueNotSquadAssignee
	}
	if squad.ArchivedAt.Valid {
		return IssuePreparedWorkflowHandoffEnqueue{}, fmt.Errorf("squad is archived")
	}
	attr := s.attributionForIssueTask(ctx, issue, pgtype.UUID{}, attribution.SourceDelegation, actorUserID)
	params, err := s.prepareMentionTask(
		ctx, issue, squad.LeaderID, pgtype.UUID{}, nil, true, issue.AssigneeID,
		false, handoffNote, attr, pgtype.UUID{}, OriginDerived, false,
	)
	if err != nil {
		return IssuePreparedWorkflowHandoffEnqueue{}, err
	}
	params.WorkflowStep = pgtype.Text{String: issue.Status, Valid: true}
	params.WorkflowHandoffRevision = pgtype.Int8{Int64: postRevision, Valid: true}
	params.FireAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
	return IssuePreparedWorkflowHandoffEnqueue{
		Task:                    params,
		QueueKeyHeadSha:         params.HeadSha.String,
		WorkflowHandoffRevision: postRevision,
	}, nil
}

// CompleteWorkflowSquadHandoffAfterNativeWrite performs post-commit wakeups for a
// newly inserted deferred squad handoff, matching the Go enqueue path.
func (s *TaskService) CompleteWorkflowSquadHandoffAfterNativeWrite(ctx context.Context, taskID pgtype.UUID) {
	if !taskID.Valid {
		return
	}
	task, err := s.Queries.GetAgentTask(ctx, taskID)
	if err != nil {
		slog.Warn("native workflow handoff: load deferred task failed",
			"task_id", util.UUIDToString(taskID), "error", err)
		return
	}
	if task.Status != "deferred" {
		return
	}
	s.broadcastTaskEvent(ctx, protocol.EventTaskQueued, task)
	s.notifyTaskAvailable(task)
	if err := s.PromoteDueDeferredTasksForRuntime(ctx, task.RuntimeID); err != nil {
		slog.Warn("promote native workflow squad handoff failed",
			"task_id", util.UUIDToString(taskID), "error", err)
	}
}

// CompleteNativeTerminalCloseAfterWrite delivers post-commit task:cancelled
// client notifications and runtime invalidation obligations recorded by the
// native terminal transaction. Client delivery is durable via
// agent_task_queue_cancelled_client_outbox; runtime wakeups use an empty task
// hint so daemons never execute the cancelled run.
func (s *TaskService) CompleteNativeTerminalCloseAfterWrite(ctx context.Context, workspaceID string, cancelled []db.AgentTaskQueue) {
	if len(cancelled) == 0 {
		return
	}
	for _, task := range cancelled {
		if _, err := s.deliverNativeCancelledClientNotification(ctx, workspaceID, task.ID); err != nil {
			slog.Warn("native terminal close: cancelled client notification delivery failed",
				"task_id", util.UUIDToString(task.ID), "error", err)
		}
		if !task.RuntimeID.Valid {
			continue
		}
		if _, err := s.deliverNativeQueueNotification(ctx, task.ID); err != nil {
			slog.Warn("native terminal close: queue notification delivery failed",
				"task_id", util.UUIDToString(task.ID), "error", err)
		}
	}
}

// CompleteIssueStatusPromotionAfterNativeWrite performs post-commit wakeups only
// for a newly inserted queue row, never for a coalesced pending run.
func (s *TaskService) CompleteIssueStatusPromotionAfterNativeWrite(ctx context.Context, taskID pgtype.UUID) {
	if !taskID.Valid {
		return
	}
	task, err := s.Queries.GetAgentTask(ctx, taskID)
	if err != nil {
		slog.Warn("native agent enqueue: load queued task failed",
			"task_id", util.UUIDToString(taskID), "error", err)
		return
	}
	if task.Status != "queued" {
		return
	}
	s.broadcastTaskEvent(ctx, protocol.EventTaskQueued, task)
	s.captureTaskQueued(ctx, task)
	if _, err := s.deliverNativeQueueNotification(ctx, taskID); err != nil {
		slog.Warn("native agent enqueue: queue notification delivery failed",
			"task_id", util.UUIDToString(taskID), "error", err)
	}
}
