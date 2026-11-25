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

type rustCommentDeleteRequest struct {
	GoMode string `json:"go_mode,omitempty"`
}

type rustCommentDeleteReceipt struct {
	CommentID     string           `json:"comment_id"`
	IssueID       string           `json:"issue_id"`
	IssueRevision int64            `json:"issue_revision"`
	Tombstone     *CommentResponse `json:"tombstone,omitempty"`
}

func (h *Handler) commentDeleteRustEligible(
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
	return true, nil
}

func (h *Handler) commentDeleteUsesRust(
	r *http.Request,
	issue db.Issue,
	existing db.Comment,
	actorType, actorID string,
	hasReplies bool,
) (bool, error) {
	if hasReplies {
		return false, nil
	}
	return h.commentDeleteRustEligible(r, issue, existing, actorType, actorID)
}

func (h *Handler) commentDeleteUsesRustTombstone(
	r *http.Request,
	issue db.Issue,
	existing db.Comment,
	actorType, actorID string,
	hasReplies bool,
) (bool, error) {
	if !hasReplies {
		return false, nil
	}
	return h.commentDeleteRustEligible(r, issue, existing, actorType, actorID)
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

func (h *Handler) commentDeleteTombstonePilotEnabled(
	ctx context.Context,
	workspaceID pgtype.UUID,
) (bool, error) {
	if h.IssueReadBridge == nil || len(h.IssueReadBridge.CommentDeleteTombstonePilotScopes) == 0 {
		return false, nil
	}
	return matchesIssuePilot(ctx, h.Queries, h.IssueReadBridge.CommentDeleteTombstonePilotScopes, uuidToString(workspaceID), workspaceID)
}

func (h *Handler) deleteCommentViaRust(
	w http.ResponseWriter,
	r *http.Request,
	existing db.Comment,
	previousIssueRevision int64,
	workspaceID, actorType, actorID string,
	tombstone bool,
) {
	path := "/internal/comments/" + uuidToString(existing.ID)
	var payload []byte
	if tombstone {
		var err error
		payload, err = json.Marshal(rustCommentDeleteRequest{GoMode: "tombstone"})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to encode comment delete")
			return
		}
	}
	body, status, ok := h.readRustCommentDeleteResponse(w, r, path, workspaceID, actorID, payload)
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
	if tombstone {
		tombstoneRow, ok := h.confirmRustCommentDeleteTombstoneReceipt(r.Context(), existing, previousIssueRevision, receipt)
		if !ok {
			writeError(w, http.StatusBadGateway, "invalid rust comment delete response")
			return
		}
		slog.Info("comment deleted", append(logger.RequestAttrs(r),
			"comment_id", uuidToString(existing.ID),
			"issue_id", uuidToString(existing.IssueID),
			"tombstoned", true,
			"removed_count", 0,
		)...)
		resp := commentToResponse(tombstoneRow, nil, nil)
		resp.IssueRevision = receipt.IssueRevision
		eventPayload := map[string]any{"comment": resp}
		if receipt.IssueRevision > 0 {
			eventPayload["issue_revision"] = receipt.IssueRevision
		}
		h.publish(protocol.EventCommentUpdated, workspaceID, actorType, actorID, eventPayload)
		w.WriteHeader(http.StatusNoContent)
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
	payload []byte,
) (body []byte, status int, ok bool) {
	unavailable := "rust comment delete service unavailable"
	if !h.IssueReadBridge.Enabled() {
		writeError(w, http.StatusServiceUnavailable, unavailable)
		return nil, 0, false
	}
	resp, routed := h.requestRustIssueWithUnavailable(w, r, http.MethodDelete, path, workspaceID, userID, payload, unavailable)
	if !routed {
		return nil, 0, false
	}
	defer resp.Body.Close()
	limit := int64(maxRustIssueReadBody) + int64(len(payload))*2
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || int64(len(raw)) > limit {
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
	if receipt.Tombstone != nil {
		return false
	}
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

func tombstoneClaimMatchesRoute(scope db.Comment, claim *CommentResponse) bool {
	if claim.ID != uuidToString(scope.ID) || claim.IssueID != uuidToString(scope.IssueID) {
		return false
	}
	if claim.AuthorType != scope.AuthorType || claim.AuthorID != uuidToString(scope.AuthorID) {
		return false
	}
	if claim.Type != scope.Type || claim.ParentID != nil || claim.SourceTaskID != nil {
		return false
	}
	if claim.Content != "" || claim.DeletedAt == nil {
		return false
	}
	if claim.ResolvedAt != nil || claim.ResolvedByID != nil || claim.ResolvedByType != nil {
		return false
	}
	if claim.Revision <= scope.Revision {
		return false
	}
	claimCreated, ok := commentResolutionTimestamp(&claim.CreatedAt)
	if !ok || !scope.CreatedAt.Time.Equal(claimCreated.Time) {
		return false
	}
	return true
}

func commentTombstoneRowMatchesClaim(current db.Comment, claim *CommentResponse) bool {
	if current.Revision != claim.Revision || current.Content != "" || !current.DeletedAt.Valid {
		return false
	}
	if current.AuthorType != claim.AuthorType || uuidToString(current.AuthorID) != claim.AuthorID {
		return false
	}
	if current.Type != claim.Type || current.ParentID.Valid {
		return false
	}
	if current.SourceTaskID.Valid {
		return false
	}
	if current.ResolvedAt.Valid || current.ResolvedByID.Valid ||
		(current.ResolvedByType.Valid && current.ResolvedByType.String != "") {
		return false
	}
	deletedAt, ok := commentResolutionTimestamp(claim.DeletedAt)
	if !ok || !current.DeletedAt.Time.Equal(deletedAt.Time) {
		return false
	}
	updatedAt, ok := commentResolutionTimestamp(&claim.UpdatedAt)
	if !ok || !current.UpdatedAt.Time.Equal(updatedAt.Time) {
		return false
	}
	claimCreated, ok := commentResolutionTimestamp(&claim.CreatedAt)
	if !ok || !current.CreatedAt.Time.Equal(claimCreated.Time) {
		return false
	}
	return true
}

func (h *Handler) confirmRustCommentDeleteTombstoneReceipt(
	ctx context.Context,
	scope db.Comment,
	previousIssueRevision int64,
	receipt rustCommentDeleteReceipt,
) (db.Comment, bool) {
	claim := receipt.Tombstone
	if claim == nil {
		return db.Comment{}, false
	}
	if receipt.CommentID != uuidToString(scope.ID) || receipt.IssueID != uuidToString(scope.IssueID) {
		return db.Comment{}, false
	}
	if receipt.IssueRevision <= previousIssueRevision {
		return db.Comment{}, false
	}
	if !tombstoneClaimMatchesRoute(scope, claim) {
		return db.Comment{}, false
	}

	issue, err := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{
		ID: scope.IssueID, WorkspaceID: scope.WorkspaceID,
	})
	if err != nil {
		return db.Comment{}, false
	}

	current, err := h.Queries.GetCommentInWorkspace(ctx, db.GetCommentInWorkspaceParams{
		ID: scope.ID, WorkspaceID: scope.WorkspaceID,
	})
	if err == nil {
		if issue.Revision < receipt.IssueRevision {
			return db.Comment{}, false
		}
		if !commentTombstoneRowMatchesClaim(current, claim) {
			return db.Comment{}, false
		}
		return current, true
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return db.Comment{}, false
	}
	if issue.Revision <= receipt.IssueRevision {
		return db.Comment{}, false
	}
	return commentFromDeleteTombstoneReceipt(scope, *claim)
}

func commentFromDeleteTombstoneReceipt(scope db.Comment, claim CommentResponse) (db.Comment, bool) {
	deletedAt, ok := commentResolutionTimestamp(claim.DeletedAt)
	if !ok {
		return db.Comment{}, false
	}
	updatedAt, ok := commentResolutionTimestamp(&claim.UpdatedAt)
	if !ok || !updatedAt.Time.Equal(deletedAt.Time) {
		return db.Comment{}, false
	}
	row := scope
	row.Content = ""
	row.Revision = claim.Revision
	row.UpdatedAt = updatedAt
	row.DeletedAt = deletedAt
	row.ResolvedAt = pgtype.Timestamptz{}
	row.ResolvedByID = pgtype.UUID{}
	row.ResolvedByType = pgtype.Text{}
	row.SourceTaskID = pgtype.UUID{}
	return row, true
}
