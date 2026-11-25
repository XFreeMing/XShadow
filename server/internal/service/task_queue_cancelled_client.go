package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	CancelledClientNotificationSweepBatchSize = QueueNotificationSweepBatchSize
	cancelledClientNotificationRetryCap       = queueNotificationRetryCap
	// cancelledClientNotificationLeaseDuration bounds how long one delivery
	// attempt may hold an obligation before another worker may reclaim it.
	cancelledClientNotificationLeaseDuration = 30 * time.Second
)

type nativeCancelledClientOutcome int

const (
	nativeCancelledClientSkipped nativeCancelledClientOutcome = iota
	nativeCancelledClientDelivered
	nativeCancelledClientSettled
	nativeCancelledClientRetry
)

// CancelledClientNotificationSweepResult summarizes one durable task:cancelled
// client-notification replay pass.
type CancelledClientNotificationSweepResult struct {
	Scanned   int
	Delivered int
	Settled   int
	Retried   int
}

// RecoverPendingCancelledClientNotifications replays undelivered task:cancelled
// events for native terminal-close cancellations.
func (s *TaskService) RecoverPendingCancelledClientNotifications(ctx context.Context, maxPerTick int32) (CancelledClientNotificationSweepResult, error) {
	result := CancelledClientNotificationSweepResult{}
	if maxPerTick <= 0 {
		return result, nil
	}
	pending, err := s.Queries.ListDueCancelledClientNotifications(ctx, maxPerTick)
	if err != nil {
		return result, fmt.Errorf("list due cancelled client notifications: %w", err)
	}
	result.Scanned = len(pending)

	var errs []error
	for _, taskID := range pending {
		outcome, deliverErr := s.deliverNativeCancelledClientNotification(ctx, "", taskID)
		switch outcome {
		case nativeCancelledClientDelivered:
			result.Delivered++
		case nativeCancelledClientSettled:
			result.Settled++
		case nativeCancelledClientRetry:
			result.Retried++
		}
		if deliverErr != nil {
			errs = append(errs, deliverErr)
		}
	}
	return result, errors.Join(errs...)
}

// deliverNativeCancelledClientNotification claims one durable obligation with a
// short committed lease, publishes task:cancelled outside the claim transaction,
// and removes the obligation only after the in-process bus finishes its
// synchronous dispatch. Acknowledging dispatch does not guarantee WebSocket
// receipt on a connected client — only that registered bus listeners ran.
func (s *TaskService) deliverNativeCancelledClientNotification(ctx context.Context, workspaceHint string, taskID pgtype.UUID) (nativeCancelledClientOutcome, error) {
	lease := dbid.NewV7()
	_, err := s.Queries.ClaimCancelledClientNotificationLease(ctx, db.ClaimCancelledClientNotificationLeaseParams{
		TaskID:       taskID,
		LeaseToken:   lease,
		LeaseSeconds: int32(cancelledClientNotificationLeaseDuration / time.Second),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nativeCancelledClientSkipped, nil
	}
	if err != nil {
		return nativeCancelledClientRetry, fmt.Errorf("claim cancelled client notification %s: %w", util.UUIDToString(taskID), err)
	}

	task, err := s.Queries.GetAgentTask(ctx, taskID)
	if errors.Is(err, pgx.ErrNoRows) {
		return s.ackCancelledClientNotification(ctx, taskID, lease, nativeCancelledClientSettled)
	}
	if err != nil {
		return s.retryCancelledClientNotification(ctx, taskID, lease, err)
	}
	if !task.IssueID.Valid || !IsTerminalCloseCancellationTask(task) {
		return s.ackCancelledClientNotification(ctx, taskID, lease, nativeCancelledClientSettled)
	}

	issue, err := s.Queries.GetIssue(ctx, task.IssueID)
	if errors.Is(err, pgx.ErrNoRows) {
		return s.ackCancelledClientNotification(ctx, taskID, lease, nativeCancelledClientSettled)
	}
	if err != nil {
		return s.retryCancelledClientNotification(ctx, taskID, lease, err)
	}
	workspaceID := util.UUIDToString(issue.WorkspaceID)
	if workspaceID == "" || (workspaceHint != "" && workspaceHint != workspaceID) {
		return s.retryCancelledClientNotification(ctx, taskID, lease, fmt.Errorf("cancelled task %s workspace mismatch", util.UUIDToString(taskID)))
	}

	s.captureTaskCancelled(ctx, task)
	s.ReconcileAgentStatus(ctx, task.AgentID)
	if ctx.Err() != nil {
		return nativeCancelledClientRetry, ctx.Err()
	}
	s.publishTaskEvent(protocol.EventTaskCancelled, workspaceID, task)
	return s.ackCancelledClientNotification(ctx, taskID, lease, nativeCancelledClientDelivered)
}

func (s *TaskService) ackCancelledClientNotification(
	ctx context.Context, taskID, lease pgtype.UUID, outcome nativeCancelledClientOutcome,
) (nativeCancelledClientOutcome, error) {
	rows, err := s.Queries.AckCancelledClientNotificationOutbox(ctx, db.AckCancelledClientNotificationOutboxParams{
		TaskID:     taskID,
		LeaseToken: lease,
	})
	if err != nil {
		return nativeCancelledClientRetry, fmt.Errorf("ack cancelled client notification %s: %w", util.UUIDToString(taskID), err)
	}
	if rows == 0 {
		return nativeCancelledClientSkipped, nil
	}
	return outcome, nil
}

func (s *TaskService) retryCancelledClientNotification(
	ctx context.Context, taskID, lease pgtype.UUID, cause error,
) (nativeCancelledClientOutcome, error) {
	releaseErr := s.Queries.ReleaseCancelledClientNotificationLease(ctx, db.ReleaseCancelledClientNotificationLeaseParams{
		TaskID:          taskID,
		LeaseToken:      lease,
		RetryCapSeconds: int32(cancelledClientNotificationRetryCap / time.Second),
		LastError:       pgtype.Text{String: cause.Error(), Valid: true},
	})
	return nativeCancelledClientRetry, errors.Join(cause, releaseErr)
}
