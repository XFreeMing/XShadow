package handler

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func (h *Handler) sourceContextFilesForDelete(ctx context.Context, source db.IssueSourceContext) ([]db.Attachment, bool, error) {
	attachments, err := h.Queries.ListAttachmentsBySourceContext(ctx, db.ListAttachmentsBySourceContextParams{
		WorkspaceID: source.WorkspaceID, SourceContextID: source.ID,
	})
	if err != nil {
		return nil, false, err
	}
	intentCount, err := h.Queries.CountSourceContextObjectIntents(ctx, db.CountSourceContextObjectIntentsParams{
		WorkspaceID: source.WorkspaceID, SourceContextID: source.ID,
	})
	return attachments, intentCount == 0 && err == nil, err
}

func (h *Handler) sourceContextDeleteCommitted(ctx context.Context, issue db.Issue, contextID pgtype.UUID) bool {
	if _, err := h.Queries.GetIssueSourceContextByID(ctx, db.GetIssueSourceContextByIDParams{
		WorkspaceID: issue.WorkspaceID, ID: contextID,
	}); !errors.Is(err, pgx.ErrNoRows) {
		return false
	}
	_, err := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{
		WorkspaceID: issue.WorkspaceID, ID: issue.ID,
	})
	return errors.Is(err, pgx.ErrNoRows)
}
