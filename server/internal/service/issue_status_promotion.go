package service

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

var errIssueNotDirectAgent = fmt.Errorf("issue is not assigned to a direct agent")

// IssuePreparedAgentEnqueue is the trusted queue insert Go prepares for a
// direct-agent write the Rust pilot commits atomically with the issue row.
type IssuePreparedAgentEnqueue struct {
	Task            db.CreateAgentTaskParams
	QueueKeyHeadSha string
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
