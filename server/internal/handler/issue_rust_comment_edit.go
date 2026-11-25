package handler

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type rustCommentEditRequest struct {
	Content          string  `json:"content"`
	ContentBase      *string `json:"content_base,omitempty"`
	ExpectedRevision int64   `json:"expected_revision"`
}

type rustCommentEditReceipt struct {
	Changed       *bool            `json:"changed"`
	Comment       *CommentResponse `json:"comment"`
	IssueRevision *int64           `json:"issue_revision,omitempty"`
}

type commentEditRequest struct {
	Content          string
	ContentBase      *string
	ExpectedRevision int64
	ReplaceAttach    bool
	SuppressAgents   bool
}

func (h *Handler) commentEditUsesRust(
	r *http.Request,
	issue db.Issue,
	existing db.Comment,
	actorType, actorID string,
	req commentEditRequest,
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
	if req.ReplaceAttach || req.SuppressAgents {
		return false, nil
	}
	if isNoteComment(existing.Content) || isNoteComment(req.Content) ||
		len(util.ParseMentions(existing.Content)) > 0 || len(util.ParseMentions(req.Content)) > 0 {
		return false, nil
	}
	if req.ExpectedRevision < 1 || req.Content == "" {
		return false, nil
	}
	queued, err := h.Queries.CommentHasActiveTriggerTask(r.Context(), db.CommentHasActiveTriggerTaskParams{
		CommentID: existing.ID,
		IssueID:   existing.IssueID,
	})
	if err != nil {
		return false, err
	}
	if queued {
		return false, nil
	}
	return true, nil
}

func (h *Handler) commentEditPilotEnabled(
	ctx context.Context,
	workspaceID pgtype.UUID,
) (bool, error) {
	if h.IssueReadBridge == nil || len(h.IssueReadBridge.CommentEditPilotScopes) == 0 {
		return false, nil
	}
	return matchesIssuePilot(ctx, h.Queries, h.IssueReadBridge.CommentEditPilotScopes, uuidToString(workspaceID), workspaceID)
}

func isAuthorMember(comment db.Comment, actorType, actorID string) bool {
	return comment.AuthorType == actorType && uuidToString(comment.AuthorID) == actorID
}

func (h *Handler) updateCommentViaRust(
	w http.ResponseWriter,
	r *http.Request,
	existing db.Comment,
	workspaceID, actorType, actorID string,
	req commentEditRequest,
) {
	body, err := json.Marshal(rustCommentEditRequest{
		Content:          req.Content,
		ContentBase:      req.ContentBase,
		ExpectedRevision: req.ExpectedRevision,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode comment edit")
		return
	}
	path := "/internal/comments/" + uuidToString(existing.ID)
	respBody, status, ok := h.readRustCommentEditResponse(w, r, path, workspaceID, actorID, body)
	if !ok {
		return
	}
	if status == http.StatusConflict {
		if routeChanged, _ := parseRustRouteChanged(respBody); routeChanged {
			writeErrorCode(w, http.StatusConflict, "comment_edit_route_changed", "task comment requirements changed; reload before editing")
			return
		}
		if actual, recognized := parseRustCommentEditRevisionConflict(respBody); recognized {
			writeRevisionConflict(w, "comment", existing.ID, req.ExpectedRevision, actual)
			return
		}
		if fieldConflict, recognized := parseRustCommentEditFieldConflict(respBody); recognized && fieldConflict {
			actual := existing.Revision
			if current, err := h.Queries.GetCommentInWorkspace(r.Context(), db.GetCommentInWorkspaceParams{
				ID: existing.ID, WorkspaceID: existing.WorkspaceID,
			}); err == nil {
				actual = current.Revision
			}
			writeRevisionConflict(w, "comment", existing.ID, req.ExpectedRevision, actual)
			return
		}
		writeRawRustUpstreamResponse(w, status, respBody)
		return
	}
	if status == http.StatusNotFound {
		writeError(w, http.StatusNotFound, "comment not found")
		return
	}
	if status != http.StatusOK {
		writeRawRustUpstreamResponse(w, status, respBody)
		return
	}
	var receipt rustCommentEditReceipt
	if json.Unmarshal(respBody, &receipt) != nil {
		writeError(w, http.StatusBadGateway, "invalid rust comment edit response")
		return
	}
	comment, issueRevision, ok := h.confirmRustCommentEditReceipt(r.Context(), existing, req.Content, receipt)
	if !ok {
		writeError(w, http.StatusBadGateway, "invalid rust comment edit response")
		return
	}
	h.finishCommentEdit(w, r, workspaceID, actorType, actorID, issueRevision, comment)
}

func (h *Handler) readRustCommentEditResponse(
	w http.ResponseWriter,
	r *http.Request,
	path, workspaceID, userID string,
	payload []byte,
) (body []byte, status int, ok bool) {
	unavailable := "rust comment edit service unavailable"
	if !h.IssueReadBridge.Enabled() {
		writeError(w, http.StatusServiceUnavailable, unavailable)
		return nil, 0, false
	}
	resp, routed := h.requestRustIssueWithUnavailable(w, r, http.MethodPut, path, workspaceID, userID, payload, unavailable)
	if !routed {
		return nil, 0, false
	}
	defer resp.Body.Close()
	limit := int64(maxRustIssueReadBody) + int64(len(payload))*2
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || int64(len(raw)) > limit {
		if !writeQueryCancellation(w, r, err) {
			writeError(w, http.StatusBadGateway, "failed to read rust comment edit response")
		}
		return nil, 0, false
	}
	return raw, resp.StatusCode, true
}

func parseRustCommentEditRevisionConflict(body []byte) (actual int64, ok bool) {
	var conflict struct {
		ActualRevision int64 `json:"actual_revision"`
	}
	if json.Unmarshal(body, &conflict) != nil || conflict.ActualRevision < 1 {
		return 0, false
	}
	return conflict.ActualRevision, true
}

func parseRustCommentEditFieldConflict(body []byte) (bool, bool) {
	var conflict struct {
		FieldConflict bool `json:"field_conflict"`
	}
	if json.Unmarshal(body, &conflict) != nil {
		return false, false
	}
	return conflict.FieldConflict, true
}

func (h *Handler) confirmRustCommentEditReceipt(
	ctx context.Context,
	scope db.Comment,
	content string,
	receipt rustCommentEditReceipt,
) (db.Comment, int64, bool) {
	if receipt.Changed == nil || receipt.Comment == nil || receipt.Comment.ID != uuidToString(scope.ID) {
		return db.Comment{}, 0, false
	}
	changed := *receipt.Changed
	if changed && (receipt.IssueRevision == nil || *receipt.IssueRevision <= 0) {
		return db.Comment{}, 0, false
	}
	if !changed && receipt.IssueRevision != nil {
		return db.Comment{}, 0, false
	}
	committedRevision := scope.Revision
	if changed {
		committedRevision++
	}
	if receipt.Comment.Content != content || receipt.Comment.Revision != committedRevision ||
		receipt.Comment.DeletedAt != nil {
		return db.Comment{}, 0, false
	}
	row, ok := h.confirmRustCommentVersion(ctx, scope, *receipt.Comment)
	if !ok || row.Content != content {
		return db.Comment{}, 0, false
	}
	var issueRevision int64
	if changed && receipt.IssueRevision != nil {
		issueRevision = *receipt.IssueRevision
		current, err := h.Queries.GetIssue(ctx, row.IssueID)
		if err != nil || current.Revision < issueRevision {
			return db.Comment{}, 0, false
		}
	}
	return row, issueRevision, true
}

func (h *Handler) finishCommentEdit(
	w http.ResponseWriter,
	r *http.Request,
	workspaceID, actorType, actorID string,
	issueRevision int64,
	comment db.Comment,
) {
	grouped := h.groupReactions(r, []pgtype.UUID{comment.ID})
	groupedAtt := h.groupAttachments(r, []pgtype.UUID{comment.ID})
	cid := uuidToString(comment.ID)
	resp := commentToResponse(comment, grouped[cid], groupedAtt[cid])
	if issueRevision > 0 {
		resp.IssueRevision = issueRevision
	}
	slog.Info("comment updated", append(logger.RequestAttrs(r), "comment_id", cid)...)
	eventPayload := map[string]any{"comment": resp}
	if issueRevision > 0 {
		eventPayload["issue_revision"] = issueRevision
	}
	h.publish(protocol.EventCommentUpdated, workspaceID, actorType, actorID, eventPayload)
	writeJSON(w, http.StatusOK, resp)
}
