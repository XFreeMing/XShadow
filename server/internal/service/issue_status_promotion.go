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

// IssueStatusPromotionEnqueue is the trusted queue insert Go prepares for a
// backlog→active promotion the Rust status pilot commits atomically with the
// issue row.
type IssueStatusPromotionEnqueue struct {
	Task            db.CreateAgentTaskParams
	QueueKeyHeadSha string
}

// PrepareIssueStatusPromotionEnqueue mirrors the direct-agent assign/promote
// enqueue plan without writing the queue row.
func (s *TaskService) PrepareIssueStatusPromotionEnqueue(
	ctx context.Context,
	issue db.Issue,
	handoffNote string,
	actorUserID pgtype.UUID,
) (IssueStatusPromotionEnqueue, error) {
	if !issue.AssigneeID.Valid || issue.AssigneeType.String != "agent" {
		return IssueStatusPromotionEnqueue{}, fmt.Errorf("issue is not assigned to a direct agent")
	}
	params, headSha, err := s.prepareIssueAssigneeEnqueueParams(
		ctx, issue, pgtype.UUID{}, nil, false, handoffNote, actorUserID,
		pgtype.UUID{}, OriginDerived, false,
	)
	if err != nil {
		return IssueStatusPromotionEnqueue{}, err
	}
	params.WorkflowStep = pgtype.Text{String: issue.Status, Valid: true}
	return IssueStatusPromotionEnqueue{Task: params, QueueKeyHeadSha: headSha}, nil
}

// CompleteIssueStatusPromotionAfterNativeWrite performs post-commit wakeups only
// for a newly inserted queue row, never for a coalesced pending run.
func (s *TaskService) CompleteIssueStatusPromotionAfterNativeWrite(ctx context.Context, taskID pgtype.UUID) {
	if !taskID.Valid {
		return
	}
	task, err := s.Queries.GetAgentTask(ctx, taskID)
	if err != nil {
		slog.Warn("native status promotion: load queued task failed",
			"task_id", util.UUIDToString(taskID), "error", err)
		return
	}
	if task.Status == "queued" {
		s.broadcastTaskEvent(ctx, protocol.EventTaskQueued, task)
		s.NotifyTaskEnqueued(ctx, task)
	}
}
