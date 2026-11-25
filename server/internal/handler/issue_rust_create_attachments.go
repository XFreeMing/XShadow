package handler

import (
	"context"
	"sort"

	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func attachmentIsFreeIssueUpload(att db.Attachment) bool {
	return !att.IssueID.Valid &&
		!att.CommentID.Valid &&
		!att.ChatSessionID.Valid &&
		!att.ChatMessageID.Valid &&
		!att.TaskID.Valid &&
		!att.SourceContextID.Valid
}

func dedupeAttachmentIDs(ids []pgtype.UUID) []pgtype.UUID {
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(ids))
	out := make([]pgtype.UUID, 0, len(ids))
	for _, id := range ids {
		if !id.Valid {
			continue
		}
		key := uuidToString(id)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, id)
	}
	return out
}

func canonicalAttachmentIDStrings(ids []pgtype.UUID) []string {
	deduped := dedupeAttachmentIDs(ids)
	if len(deduped) == 0 {
		return nil
	}
	out := make([]string, len(deduped))
	for i, id := range deduped {
		out[i] = uuidToString(id)
	}
	sort.Strings(out)
	return out
}

func (h *Handler) issueCreateAttachmentsPreflight(
	ctx context.Context,
	workspaceID pgtype.UUID,
	ids []pgtype.UUID,
) (bool, error) {
	deduped := dedupeAttachmentIDs(ids)
	if len(deduped) == 0 {
		return true, nil
	}
	rows, err := h.Queries.ListAttachmentsByIDs(ctx, db.ListAttachmentsByIDsParams{
		AttachmentIds: deduped,
		WorkspaceID:   workspaceID,
	})
	if err != nil {
		return false, err
	}
	byID := make(map[string]db.Attachment, len(rows))
	for _, row := range rows {
		byID[uuidToString(row.ID)] = row
	}
	for _, id := range deduped {
		row, ok := byID[uuidToString(id)]
		if !ok || !attachmentIsFreeIssueUpload(row) {
			return false, nil
		}
	}
	return true, nil
}

func rustIssueCreateLinkedAttachmentsValid(requested []string, linked []string) bool {
	if len(linked) == 0 {
		return true
	}
	allowed := make(map[string]struct{}, len(requested))
	for _, id := range requested {
		if id == "" {
			return false
		}
		allowed[id] = struct{}{}
	}
	seen := make(map[string]struct{}, len(linked))
	for _, id := range linked {
		if id == "" {
			return false
		}
		if _, ok := allowed[id]; !ok {
			return false
		}
		if _, dup := seen[id]; dup {
			return false
		}
		seen[id] = struct{}{}
	}
	return true
}

func attachmentRowsMatchLinkedIDs(atts []db.Attachment, linked []string) bool {
	if len(linked) == 0 {
		return len(atts) == 0
	}
	if len(atts) != len(linked) {
		return false
	}
	want := make(map[string]struct{}, len(linked))
	for _, id := range linked {
		want[id] = struct{}{}
	}
	for _, att := range atts {
		if _, ok := want[uuidToString(att.ID)]; !ok {
			return false
		}
	}
	return true
}
