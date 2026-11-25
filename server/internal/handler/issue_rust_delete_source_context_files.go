package handler

import (
	"context"
	"sort"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/storage"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func (h *Handler) buildRustIssueDeleteSourceContextClonePlan(source db.IssueSourceContext, attachments []db.Attachment) []rustIssueDeleteAttachmentPlanItem {
	ws := uuidToString(source.WorkspaceID)
	plan := make([]rustIssueDeleteAttachmentPlanItem, 0, len(attachments))
	for _, att := range attachments {
		if att.IssueID.Valid || att.CommentID.Valid || att.ChatSessionID.Valid ||
			att.ChatMessageID.Valid || att.TaskID.Valid || !att.SourceContextID.Valid ||
			att.SourceContextID != source.ID {
			return nil
		}
		cloneID := uuidToString(att.ID)
		key, keyOK := storage.ManagedSourceContextCloneKey(h.Storage, ws, cloneID, att.Filename, att.Url)
		if !keyOK {
			return nil
		}
		plan = append(plan, rustIssueDeleteAttachmentPlanItem{
			AttachmentID: cloneID,
			ObjectURL:    att.Url,
			StorageKey:   key,
		})
	}
	sort.Slice(plan, func(i, j int) bool { return plan[i].AttachmentID < plan[j].AttachmentID })
	return plan
}

func (h *Handler) sourceContextCloneCleanupCommitted(ctx context.Context, workspace, source pgtype.UUID, plan []rustIssueDeleteAttachmentPlanItem) bool {
	intents, err := h.Queries.ListUnreferencedSourceContextDeleteIntents(ctx, db.ListUnreferencedSourceContextDeleteIntentsParams{
		WorkspaceID: workspace, SourceContextID: source,
	})
	if err != nil || len(intents) != len(plan) {
		return false
	}
	byKey := make(map[string]db.ListUnreferencedSourceContextDeleteIntentsRow, len(intents))
	for _, intent := range intents {
		byKey[intent.StorageKey] = intent
	}
	for _, item := range plan {
		intent, ok := byKey[item.StorageKey]
		if !ok || uuidToString(intent.AttachmentID) != item.AttachmentID || intent.ObjectUrl != item.ObjectURL {
			return false
		}
	}
	return true
}

func planSourceContextCloneURLs(plan []rustIssueDeleteAttachmentPlanItem) []string {
	out := make([]string, len(plan))
	for i, item := range plan {
		out[i] = item.ObjectURL
	}
	return out
}
