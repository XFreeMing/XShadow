package handler

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/storage"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type rustAgentSourceContextFilePlan struct {
	WorkspaceID pgtype.UUID
	ContextID   pgtype.UUID
	CapturedBy  pgtype.UUID
	HasFiles    bool
	Clones      []rustSourceContextCloneTrust
}

func (h *Handler) buildAgentSourceContextFilePlan(
	wsUUID, contextID, capturedBy pgtype.UUID,
	snapshotJSON []byte,
	rows []db.Attachment,
) (rustAgentSourceContextFilePlan, bool) {
	var snapshot service.SourceContextSnapshot
	if len(snapshotJSON) == 0 || json.Unmarshal(snapshotJSON, &snapshot) != nil {
		return rustAgentSourceContextFilePlan{}, false
	}
	frozenBySource := sourceContextAttachmentsByOriginalID(snapshot, true)
	if frozenBySource == nil || len(rows) != len(frozenBySource) {
		return rustAgentSourceContextFilePlan{}, false
	}
	plan := rustAgentSourceContextFilePlan{
		WorkspaceID: wsUUID,
		ContextID:   contextID,
		CapturedBy:  capturedBy,
		HasFiles:    len(rows) != 0,
	}
	if !plan.HasFiles {
		return plan, true
	}
	frozenByClone := make(map[string]service.SourceContextAttachment, len(frozenBySource))
	for _, frozen := range frozenBySource {
		frozenByClone[frozen.ID] = frozen
	}
	if len(frozenByClone) != len(frozenBySource) {
		return rustAgentSourceContextFilePlan{}, false
	}
	workspaceKey := uuidToString(wsUUID)
	plan.Clones = make([]rustSourceContextCloneTrust, 0, len(rows))
	for _, att := range rows {
		if att.IssueID.Valid || att.CommentID.Valid || att.ChatSessionID.Valid ||
			att.ChatMessageID.Valid || att.TaskID.Valid || att.WorkspaceID != wsUUID ||
			att.SourceContextID != contextID {
			return rustAgentSourceContextFilePlan{}, false
		}
		if att.UploaderType != "member" || att.UploaderID != capturedBy {
			return rustAgentSourceContextFilePlan{}, false
		}
		cloneID := uuidToString(att.ID)
		frozen, ok := frozenByClone[cloneID]
		if !ok || frozen.Filename != att.Filename || frozen.ContentType != att.ContentType ||
			frozen.SizeBytes != att.SizeBytes {
			return rustAgentSourceContextFilePlan{}, false
		}
		key, keyOK := storage.ManagedSourceContextCloneKey(h.Storage, workspaceKey, cloneID, att.Filename, att.Url)
		if !keyOK {
			return rustAgentSourceContextFilePlan{}, false
		}
		plan.Clones = append(plan.Clones, rustSourceContextCloneTrust{
			CloneID:            cloneID,
			SourceAttachmentID: frozen.SourceAttachmentID,
			Filename:           att.Filename,
			URL:                att.Url,
			ContentType:        att.ContentType,
			SizeBytes:          att.SizeBytes,
			StorageKey:         key,
		})
	}
	sort.Slice(plan.Clones, func(i, j int) bool {
		return plan.Clones[i].CloneID < plan.Clones[j].CloneID
	})
	return plan, true
}

func (h *Handler) verifyAgentSourceContextFilePlanCommitted(
	ctx context.Context,
	plan rustAgentSourceContextFilePlan,
	snapshotJSON []byte,
) bool {
	if !plan.WorkspaceID.Valid || !plan.ContextID.Valid || !plan.CapturedBy.Valid {
		return false
	}
	rows, err := h.Queries.ListAttachmentsBySourceContext(ctx, db.ListAttachmentsBySourceContextParams{
		WorkspaceID: plan.WorkspaceID, SourceContextID: plan.ContextID,
	})
	if err != nil {
		return false
	}
	rebuilt, ok := h.buildAgentSourceContextFilePlan(
		plan.WorkspaceID, plan.ContextID, plan.CapturedBy, snapshotJSON, rows,
	)
	if !ok || rebuilt.HasFiles != plan.HasFiles || len(rebuilt.Clones) != len(plan.Clones) {
		return false
	}
	for i := range plan.Clones {
		if rebuilt.Clones[i] != plan.Clones[i] {
			return false
		}
	}
	return true
}
