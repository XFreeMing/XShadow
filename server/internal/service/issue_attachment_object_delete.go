package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/storage"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

const (
	IssueAttachmentObjectDeleteSweepBatchSize = QueueNotificationSweepBatchSize
	issueAttachmentObjectDeleteLeaseDuration  = 30 * time.Second
	issueAttachmentObjectDeleteTimeout        = 10 * time.Second
)

type issueAttachmentObjectDeleteOutcome int

const (
	issueAttachmentObjectDeleteSkipped issueAttachmentObjectDeleteOutcome = iota
	issueAttachmentObjectDeleteDelivered
	issueAttachmentObjectDeleteRetry
)

// IssueAttachmentObjectDeleteSweepResult summarizes one attachment object cleanup pass.
type IssueAttachmentObjectDeleteSweepResult struct {
	Scanned   int
	Delivered int
	Retried   int
}

// TryDeliverIssueAttachmentObjectDeletes attempts immediate delivery for the given
// outbox rows. It never reads HTTP receipt metadata — only persisted obligations.
func (s *TaskService) TryDeliverIssueAttachmentObjectDeletes(ctx context.Context, attachmentIDs []pgtype.UUID) (int, error) {
	var delivered int
	var errs []error
	for _, attachmentID := range attachmentIDs {
		outcome, err := s.deliverIssueAttachmentObjectDelete(ctx, attachmentID)
		if outcome == issueAttachmentObjectDeleteDelivered {
			delivered++
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	return delivered, errors.Join(errs...)
}

// RecoverPendingIssueAttachmentObjectDeletes deletes workspace upload objects
// recorded before native issue attachment row removal.
func (s *TaskService) RecoverPendingIssueAttachmentObjectDeletes(ctx context.Context, maxPerTick int32) (IssueAttachmentObjectDeleteSweepResult, error) {
	result := IssueAttachmentObjectDeleteSweepResult{}
	if maxPerTick <= 0 || s.SourceContextStorage == nil {
		return result, nil
	}
	pending, err := s.Queries.ListDueIssueAttachmentObjectDeletes(ctx, maxPerTick)
	if err != nil {
		return result, fmt.Errorf("list due issue attachment object deletes: %w", err)
	}
	result.Scanned = len(pending)

	var errs []error
	for _, attachmentID := range pending {
		outcome, deliverErr := s.deliverIssueAttachmentObjectDelete(ctx, attachmentID)
		switch outcome {
		case issueAttachmentObjectDeleteDelivered:
			result.Delivered++
		case issueAttachmentObjectDeleteRetry:
			result.Retried++
		}
		if deliverErr != nil {
			errs = append(errs, deliverErr)
		}
	}
	return result, errors.Join(errs...)
}

func (s *TaskService) deliverIssueAttachmentObjectDelete(ctx context.Context, attachmentID pgtype.UUID) (issueAttachmentObjectDeleteOutcome, error) {
	lease := dbid.NewV7()
	row, err := s.Queries.ClaimIssueAttachmentObjectDeleteLease(ctx, db.ClaimIssueAttachmentObjectDeleteLeaseParams{
		AttachmentID: attachmentID,
		LeaseToken:   lease,
		LeaseSeconds: int32(issueAttachmentObjectDeleteLeaseDuration / time.Second),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return issueAttachmentObjectDeleteSkipped, nil
	}
	if err != nil {
		return issueAttachmentObjectDeleteRetry, fmt.Errorf("claim issue attachment delete %s: %w", util.UUIDToString(attachmentID), err)
	}
	workspaceID := util.UUIDToString(row.WorkspaceID)
	attachmentStr := util.UUIDToString(row.AttachmentID)
	key, ok := storage.ManagedWorkspaceUploadKey(
		s.SourceContextStorage,
		workspaceID,
		attachmentStr,
		row.ObjectUrl,
	)
	if !ok || key != row.StorageKey {
		return s.releaseIssueAttachmentObjectDelete(ctx, row.AttachmentID, lease, errors.New("stored attachment key does not match workspace upload shape"))
	}
	if _, err := s.Queries.GetAttachmentByIDOnly(ctx, row.AttachmentID); err == nil {
		return s.releaseIssueAttachmentObjectDelete(ctx, row.AttachmentID, lease, errors.New("attachment still has a live record"))
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return s.releaseIssueAttachmentObjectDelete(ctx, row.AttachmentID, lease, err)
	}
	deleteCtx, cancel := context.WithTimeout(ctx, issueAttachmentObjectDeleteTimeout)
	err = s.SourceContextStorage.DeleteObject(deleteCtx, key)
	cancel()
	if err != nil {
		return s.releaseIssueAttachmentObjectDelete(ctx, row.AttachmentID, lease, err)
	}
	acked, err := s.Queries.AckIssueAttachmentObjectDeleteOutbox(ctx, db.AckIssueAttachmentObjectDeleteOutboxParams{
		AttachmentID: row.AttachmentID,
		LeaseToken:   lease,
	})
	if err != nil {
		return issueAttachmentObjectDeleteRetry, fmt.Errorf("ack issue attachment delete %s: %w", util.UUIDToString(row.AttachmentID), err)
	}
	if acked == 0 {
		return issueAttachmentObjectDeleteSkipped, nil
	}
	return issueAttachmentObjectDeleteDelivered, nil
}

func (s *TaskService) releaseIssueAttachmentObjectDelete(ctx context.Context, attachmentID pgtype.UUID, lease pgtype.UUID, cause error) (issueAttachmentObjectDeleteOutcome, error) {
	msg := ""
	if cause != nil {
		msg = cause.Error()
	}
	if err := s.Queries.ReleaseIssueAttachmentObjectDeleteLease(ctx, db.ReleaseIssueAttachmentObjectDeleteLeaseParams{
		AttachmentID:    attachmentID,
		LeaseToken:      lease,
		LastError:       pgtype.Text{String: msg, Valid: msg != ""},
		RetryCapSeconds: int32(queueNotificationRetryCap / time.Second),
	}); err != nil {
		return issueAttachmentObjectDeleteRetry, fmt.Errorf("release issue attachment delete %s: %w", util.UUIDToString(attachmentID), err)
	}
	return issueAttachmentObjectDeleteRetry, cause
}
