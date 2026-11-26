package handler

import (
	"context"
	"net/http"
	"sort"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/storage"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type rustCommentDeleteLeafPlan struct {
	UseRust        bool
	AttachmentPlan []rustIssueDeleteAttachmentPlanItem
}

func (h *Handler) commentDeleteUploadsPilotEnabled(
	ctx context.Context,
	workspaceID pgtype.UUID,
) (bool, error) {
	if h.IssueReadBridge == nil || len(h.IssueReadBridge.CommentDeleteUploadsPilotScopes) == 0 {
		return false, nil
	}
	return matchesIssuePilot(ctx, h.Queries, h.IssueReadBridge.CommentDeleteUploadsPilotScopes, uuidToString(workspaceID), workspaceID)
}

func (h *Handler) planRustTopLevelCommentDelete(
	r *http.Request,
	issue db.Issue,
	existing db.Comment,
	actorType, actorID string,
	hasReplies bool,
) (rustCommentDeleteLeafPlan, error) {
	var none rustCommentDeleteLeafPlan
	if hasReplies || existing.ParentID.Valid {
		return none, nil
	}
	shared, err := h.commentDeleteRustSharedEligible(r, issue, existing, actorType, actorID)
	if err != nil || !shared {
		return none, err
	}
	attached, err := h.Queries.CommentHasFileAttachments(r.Context(), db.CommentHasFileAttachmentsParams{
		CommentID: existing.ID, WorkspaceID: existing.WorkspaceID,
	})
	if err != nil {
		return none, err
	}
	if !attached {
		return rustCommentDeleteLeafPlan{UseRust: true}, nil
	}
	uploadOn, err := h.commentDeleteUploadsPilotEnabled(r.Context(), existing.WorkspaceID)
	if err != nil || !uploadOn {
		return none, err
	}
	plan, ok, err := h.buildRustCommentDeleteAttachmentPlan(r.Context(), existing)
	if err != nil {
		return none, err
	}
	if !ok {
		return none, nil
	}
	return rustCommentDeleteLeafPlan{UseRust: true, AttachmentPlan: plan}, nil
}

func (h *Handler) buildRustCommentDeleteAttachmentPlan(
	ctx context.Context,
	comment db.Comment,
) ([]rustIssueDeleteAttachmentPlanItem, bool, error) {
	if h.Storage == nil {
		return nil, false, nil
	}
	rows, err := h.Queries.ListCommentAttachmentsForRustDelete(ctx, db.ListCommentAttachmentsForRustDeleteParams{
		CommentID:   comment.ID,
		WorkspaceID: comment.WorkspaceID,
		IssueID:     comment.IssueID,
	})
	if err != nil {
		return nil, false, err
	}
	if len(rows) == 0 {
		return nil, false, nil
	}
	ws := uuidToString(comment.WorkspaceID)
	issueID := uuidToString(comment.IssueID)
	commentID := uuidToString(comment.ID)
	plan := make([]rustIssueDeleteAttachmentPlanItem, 0, len(rows))
	for _, row := range rows {
		if uuidToString(row.WorkspaceID) != ws || uuidToString(row.IssueID) != issueID {
			return nil, false, nil
		}
		if !row.CommentID.Valid || uuidToString(row.CommentID) != commentID {
			return nil, false, nil
		}
		if !row.ScopedOrdinary.Valid || !row.ScopedOrdinary.Bool || !row.UnsharedUrl {
			return nil, false, nil
		}
		id := uuidToString(row.ID)
		key, keyOK := storage.ManagedWorkspaceUploadKey(h.Storage, ws, id, row.Url)
		if !keyOK {
			return nil, false, nil
		}
		plan = append(plan, rustIssueDeleteAttachmentPlanItem{
			AttachmentID: id,
			ObjectURL:    row.Url,
			StorageKey:   key,
		})
	}
	sort.Slice(plan, func(i, j int) bool { return plan[i].AttachmentID < plan[j].AttachmentID })
	return plan, true, nil
}