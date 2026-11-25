package handler

import (
	"context"
	"errors"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/storage"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type rustIssueDeleteAttachmentPlanItem struct {
	AttachmentID string `json:"attachment_id"`
	ObjectURL    string `json:"object_url"`
	StorageKey   string `json:"storage_key"`
}

func (h *Handler) buildRustIssueDeleteAttachmentPlan(ctx context.Context, issue db.Issue) ([]rustIssueDeleteAttachmentPlanItem, bool, error) {
	ok, err := h.issueOrdinaryDeleteWithDirectUploadsEligible(ctx, issue)
	if err != nil || !ok {
		return nil, false, err
	}
	rows, err := h.Queries.ListDirectIssueAttachmentsForDelete(ctx, db.ListDirectIssueAttachmentsForDeleteParams{
		WorkspaceID: issue.WorkspaceID,
		IssueID:     issue.ID,
	})
	if err != nil {
		return nil, false, err
	}
	ws := uuidToString(issue.WorkspaceID)
	plan := make([]rustIssueDeleteAttachmentPlanItem, 0, len(rows))
	for _, att := range rows {
		id := uuidToString(att.ID)
		key, keyOK := storage.ManagedWorkspaceUploadKey(h.Storage, ws, id, att.Url)
		if !keyOK {
			return nil, false, nil
		}
		plan = append(plan, rustIssueDeleteAttachmentPlanItem{
			AttachmentID: id,
			ObjectURL:    att.Url,
			StorageKey:   key,
		})
	}
	sort.Slice(plan, func(i, j int) bool { return plan[i].AttachmentID < plan[j].AttachmentID })
	return plan, true, nil
}

func (h *Handler) issueOrdinaryDeleteWithDirectUploadsEligible(ctx context.Context, issue db.Issue) (bool, error) {
	ok, err := h.issueOrdinaryDeleteMemberRuntimeBaseline(ctx, issue)
	if err != nil || !ok {
		return false, err
	}
	if _, err := h.Queries.GetIssueSourceContextByIssue(ctx, db.GetIssueSourceContextByIssueParams{
		WorkspaceID: issue.WorkspaceID,
		IssueID:     issue.ID,
	}); err == nil {
		return false, nil
	} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	hasCommentAtts, err := h.Queries.HasCommentAttachmentsOnIssue(ctx, db.HasCommentAttachmentsOnIssueParams{
		WorkspaceID: issue.WorkspaceID,
		IssueID:     issue.ID,
	})
	if err != nil {
		return false, err
	}
	if hasCommentAtts {
		return false, nil
	}
	attachments, err := h.Queries.ListDirectIssueAttachmentsForDelete(ctx, db.ListDirectIssueAttachmentsForDeleteParams{
		WorkspaceID: issue.WorkspaceID,
		IssueID:     issue.ID,
	})
	if err != nil {
		return false, err
	}
	if len(attachments) == 0 {
		return false, nil
	}
	ws := uuidToString(issue.WorkspaceID)
	for _, att := range attachments {
		if _, ok := storage.ManagedWorkspaceUploadKey(h.Storage, ws, uuidToString(att.ID), att.Url); !ok {
			return false, nil
		}
	}
	return true, nil
}

func rustIssueDeleteAttachmentIDsMatchPlan(plan []rustIssueDeleteAttachmentPlanItem, ids []string) bool {
	if len(plan) != len(ids) {
		return false
	}
	want := make([]string, len(plan))
	for i, item := range plan {
		want[i] = item.AttachmentID
	}
	got := append([]string(nil), ids...)
	sort.Strings(want)
	sort.Strings(got)
	for i := range want {
		if want[i] != got[i] {
			return false
		}
	}
	return true
}

func planAttachmentIDs(plan []rustIssueDeleteAttachmentPlanItem) []pgtype.UUID {
	out := make([]pgtype.UUID, len(plan))
	for i, item := range plan {
		out[i] = parseUUID(item.AttachmentID)
	}
	return out
}
