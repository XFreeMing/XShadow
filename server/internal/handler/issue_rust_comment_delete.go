package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type rustCommentDeleteReceipt struct {
	CommentID     string `json:"comment_id"`
	IssueID       string `json:"issue_id"`
	IssueRevision int64  `json:"issue_revision"`
}

func (h *Handler) commentDeleteUsesRust(
	r *http.Request,
	issue db.Issue,
	existing db.Comment,
	actorType, actorID string,
) (bool, error) {
	if actorType != "member" {
		return false, nil
	}
	if !isAuthorMember(existing, actorType, actorID) {
		return false, nil
	}
	if existing.ParentID.Valid || existing.Type != "comment" {
		return false, nil
	}
	if existing.DeletedAt.Valid {
		return false, nil
	}
	if existing.QuickActionID.Valid || existing.ViaPluginID.Valid {
		return false, nil
	}
	if issue.ProjectID.Valid {
		return false, nil
	}
	if !commentCreateRouteAllowed(issue) {
		return false, nil
	}
	if existing.SourceTaskID.Valid || len(existing.SuppressedAgentIds) > 0 {
		return false, nil
	}
	if isNoteComment(existing.Content) || len(util.ParseMentions(existing.Content)) > 0 {
		return false, nil
	}
	if queued, err := h.Queries.CommentHasActiveTriggerTask(r.Context(), db.CommentHasActiveTriggerTaskParams{
		CommentID: existing.ID,
		IssueID:   existing.IssueID,
	}); err != nil {
		return false, err
	} else if queued {
		return false, nil
	}
	if bound, err := h.Queries.CommentHasTaskSupplement(r.Context(), db.CommentHasTaskSupplementParams{
		CommentID: existing.ID, WorkspaceID: existing.WorkspaceID,
	}); err != nil {
		return false, err
	} else if bound {
		return false, nil
	}
	if attached, err := h.Queries.CommentHasFileAttachments(r.Context(), db.CommentHasFileAttachmentsParams{
		CommentID: existing.ID, WorkspaceID: existing.WorkspaceID,
	}); err != nil {
		return false, err
	} else if attached {
		return false, nil
	}
	hasReplies, err := h.Queries.CommentHasReplies(r.Context(), db.CommentHasRepliesParams{
		ID: existing.ID, WorkspaceID: existing.WorkspaceID,
	})
	if err != nil {
		return false, err
	}
	if hasReplies {
		return false, nil
	}
	return true, nil
}

func (h *Handler) commentDeletePilotEnabled(
	ctx context.Context,
	workspaceID pgtype.UUID,
) (bool, error) {
	if h.IssueReadBridge == nil || len(h.IssueReadBridge.CommentDeletePilotScopes) == 0 {
		return false, nil
	}
	return matchesIssuePilot(ctx, h.Queries, h.IssueReadBridge.CommentDeletePilotScopes, uuidToString(workspaceID), workspaceID)
}

func (h *Handler) deleteCommentViaRust(
	w http.ResponseWriter,
	r *http.Request,
	existing db.Comment,
	previousIssueRevision int64,
	workspaceID, actorType, actorID string,
) {
	path := "/internal/comments/" + uuidToString(existing.ID)
	body, status, ok := h.readRustCommentDeleteResponse(w, r, path, workspaceID, actorID)
	if !ok {
		return
	}
	if status == http.StatusConflict {
		if routeChanged, _ := parseRustRouteChanged(body); routeChanged {
			writeErrorCode(w, http.StatusConflict, "comment_delete_route_changed", "task comment requirements changed; reload before deleting")
			return
		}
		writeRawRustUpstreamResponse(w, status, body)
		return
	}
	if status == http.StatusNotFound {
		writeError(w, http.StatusNotFound, "comment not found")
		return
	}
	if status != http.StatusOK {
		writeRawRustUpstreamResponse(w, status, body)
		return
	}
	var receipt rustCommentDeleteReceipt
	if json.Unmarshal(body, &receipt) != nil {
		writeError(w, http.StatusBadGateway, "invalid rust comment delete response")
		return
	}
	if !h.confirmRustCommentDeleteReceipt(r.Context(), existing, previousIssueRevision, receipt) {
		writeError(w, http.StatusBadGateway, "invalid rust comment delete response")
		return
	}
	slog.Info("comment deleted", append(logger.RequestAttrs(r),
		"comment_id", uuidToString(existing.ID),
		"issue_id", uuidToString(existing.IssueID),
		"tombstoned", false,
		"removed_count", 1,
	)...)
	eventPayload := map[string]any{
		"comment_id":     receipt.CommentID,
		"issue_id":       receipt.IssueID,
		"issue_revision": receipt.IssueRevision,
	}
	h.publish(protocol.EventCommentDeleted, workspaceID, actorType, actorID, eventPayload)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) readRustCommentDeleteResponse(
	w http.ResponseWriter,
	r *http.Request,
	path, workspaceID, userID string,
) (body []byte, status int, ok bool) {
	unavailable := "rust comment delete service unavailable"
	if !h.IssueReadBridge.Enabled() {
		writeError(w, http.StatusServiceUnavailable, unavailable)
		return nil, 0, false
	}
	resp, routed := h.requestRustIssueWithUnavailable(w, r, http.MethodDelete, path, workspaceID, userID, nil, unavailable)
	if !routed {
		return nil, 0, false
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxRustIssueReadBody)+1))
	if err != nil || len(raw) > maxRustIssueReadBody {
		if !writeQueryCancellation(w, r, err) {
			writeError(w, http.StatusBadGateway, "failed to read rust comment delete response")
		}
		return nil, 0, false
	}
	return raw, resp.StatusCode, true
}

func (h *Handler) confirmRustCommentDeleteReceipt(
	ctx context.Context,
	scope db.Comment,
	previousIssueRevision int64,
	receipt rustCommentDeleteReceipt,
) bool {
	if receipt.CommentID != uuidToString(scope.ID) || receipt.IssueID != uuidToString(scope.IssueID) {
		return false
	}
	if receipt.IssueRevision <= previousIssueRevision {
		return false
	}
	_, err := h.Queries.GetCommentInWorkspace(ctx, db.GetCommentInWorkspaceParams{
		ID: scope.ID, WorkspaceID: scope.WorkspaceID,
	})
	if err == nil {
		return false
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return false
	}
	issue, err := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{
		ID: scope.IssueID, WorkspaceID: scope.WorkspaceID,
	})
	if err != nil || issue.Revision < receipt.IssueRevision {
		return false
	}
	return true
}
