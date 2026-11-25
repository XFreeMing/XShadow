package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	QueueNotificationSweepBatchSize = 64
	queueNotificationRetryCap       = time.Minute
	QueueNotificationSweepInterval  = 500 * time.Millisecond
)

// nativeQueueNotifyOutcome classifies a shared native notification delivery.
type nativeQueueNotifyOutcome int

const (
	nativeQueueNotifySkipped nativeQueueNotifyOutcome = iota
	nativeQueueNotifyDelivered
	nativeQueueNotifySettled
	nativeQueueNotifyRetry
)

// QueueNotificationSweepResult summarizes one durable native-queue notify pass.
type QueueNotificationSweepResult struct {
	Scanned   int
	Delivered int
	Settled   int
	Retried   int
}

// CompleteQuickCreateAfterNativeWrite records the accepted enqueue and uses the
// same outbox delivery as recovery to invalidate the daemon's empty-claim cache.
func (s *TaskService) CompleteQuickCreateAfterNativeWrite(ctx context.Context, task db.AgentTaskQueue) {
	s.captureTaskQueued(ctx, task)
	if _, err := s.deliverNativeQueueNotification(ctx, task.ID); err != nil {
		slog.Warn("native quick-create notification delivery failed", "task_id", util.UUIDToString(task.ID), "error", err)
	}
}

// RecoverPendingQueueNotifications replays undelivered native enqueue wakeups.
// Recovery performs bump-before-wakeup only; UI events and enqueue analytics stay
// on the HTTP fast path.
func (s *TaskService) RecoverPendingQueueNotifications(ctx context.Context, maxPerTick int32) (QueueNotificationSweepResult, error) {
	result := QueueNotificationSweepResult{}
	if maxPerTick <= 0 {
		return result, nil
	}
	pending, err := s.Queries.ListDueQueueNotifications(ctx, maxPerTick)
	if err != nil {
		return result, fmt.Errorf("list due queue notifications: %w", err)
	}
	result.Scanned = len(pending)

	var errs []error
	for _, taskID := range pending {
		outcome, deliverErr := s.deliverNativeQueueNotification(ctx, taskID)
		switch outcome {
		case nativeQueueNotifyDelivered:
			result.Delivered++
		case nativeQueueNotifySettled:
			result.Settled++
		case nativeQueueNotifyRetry:
			result.Retried++
		}
		if deliverErr != nil {
			errs = append(errs, deliverErr)
		}
	}
	return result, errors.Join(errs...)
}

// deliverNativeQueueNotification coordinates invalidation, daemon wakeup, and
// outbox removal for one task. HTTP success owns UI/analytics; this path only
// bumps Redis and sends the at-least-once wakeup hint.
func (s *TaskService) deliverNativeQueueNotification(ctx context.Context, taskID pgtype.UUID) (nativeQueueNotifyOutcome, error) {
	if s.TxStarter == nil {
		return nativeQueueNotifyRetry, errors.New("native queue notification delivery requires a transaction")
	}
	var outcome nativeQueueNotifyOutcome
	var invalidateErr error
	err := s.runInTx(ctx, func(q *db.Queries) error {
		row, err := q.LockQueueNotificationForDelivery(ctx, taskID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("lock queue notification %s: %w", util.UUIDToString(taskID), err)
		}
		task, err := q.GetAgentTask(ctx, taskID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("load task %s for queue notification: %w", util.UUIDToString(taskID), err)
		}
		if !task.RuntimeID.Valid {
			outcome = nativeQueueNotifySettled
			return q.DeleteQueueNotificationOutbox(ctx, taskID)
		}
		runtimeKey := util.UUIDToString(task.RuntimeID)
		hintTaskID := util.UUIDToString(taskID)
		if IsTerminalCloseCancellationTask(task) {
			s.forgetTaskReclaim(task)
			hintTaskID = ""
		} else if task.Status != "queued" {
			outcome = nativeQueueNotifySettled
			return q.DeleteQueueNotificationOutbox(ctx, taskID)
		}
		if invalidateErr = s.bumpRuntimeQueueHint(ctx, runtimeKey, hintTaskID); invalidateErr != nil {
			outcome = nativeQueueNotifyRetry
			return recordQueueNotificationRetry(ctx, q, row, invalidateErr)
		}
		outcome = nativeQueueNotifyDelivered
		return q.DeleteQueueNotificationOutbox(ctx, taskID)
	})
	if err != nil {
		return nativeQueueNotifyRetry, errors.Join(invalidateErr, err)
	}
	return outcome, invalidateErr
}

func (s *TaskService) bumpRuntimeQueueHint(ctx context.Context, runtimeKey, taskID string) error {
	if invalidateErr := s.EmptyClaim.bump(ctx, runtimeKey); invalidateErr != nil {
		return invalidateErr
	}
	if s.Wakeup != nil {
		s.Wakeup.NotifyTaskAvailable(runtimeKey, taskID)
	}
	return nil
}

func recordQueueNotificationRetry(ctx context.Context, q *db.Queries, row db.AgentTaskQueueNotifyOutbox, cause error) error {
	next := time.Now().Add(min(time.Duration(row.AttemptCount+1)*time.Second, queueNotificationRetryCap))
	msg := cause.Error()
	return q.BumpQueueNotificationRetry(ctx, db.BumpQueueNotificationRetryParams{
		TaskID:        row.TaskID,
		NextAttemptAt: pgtype.Timestamptz{Time: next, Valid: true},
		LastError:     pgtype.Text{String: msg, Valid: msg != ""},
	})
}
