package handler

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type rustCommentResolutionReceipt struct {
	TargetChanged *bool             `json:"target_changed"`
	Target        CommentResponse   `json:"target"`
	Cleared       []CommentResponse `json:"cleared"`
}

func (h *Handler) commentResolutionUsesRust(
	r *http.Request,
	issue db.Issue,
	actorType string,
) (bool, error) {
	if h.IssueReadBridge == nil || len(h.IssueReadBridge.CommentResolutionPilotScopes) == 0 {
		return false, nil
	}
	if actorType != "member" {
		return false, nil
	}
	if issue.ProjectID.Valid {
		return false, nil
	}
	return matchesIssuePilot(r.Context(), h.Queries, h.IssueReadBridge.CommentResolutionPilotScopes, uuidToString(issue.WorkspaceID), issue.WorkspaceID)
}

func (h *Handler) resolveCommentViaRust(
	w http.ResponseWriter,
	r *http.Request,
	comment db.Comment,
	workspaceID, actorType, actorID string,
) {
	path := "/internal/comments/" + uuidToString(comment.ID) + "/resolve"
	body, status, ok := h.readRustCommentResolutionResponse(w, r, http.MethodPost, path, workspaceID, actorID, nil)
	if !ok {
		return
	}
	if status == http.StatusConflict {
		if routeChanged, _ := parseRustRouteChanged(body); routeChanged {
			writeErrorCode(w, http.StatusConflict, "comment_resolution_route_changed", "task comment requirements changed; reload before resolving")
			return
		}
		writeRawRustUpstreamResponse(w, status, body)
		return
	}
	if status != http.StatusOK {
		writeRawRustUpstreamResponse(w, status, body)
		return
	}
	var receipt rustCommentResolutionReceipt
	if json.Unmarshal(body, &receipt) != nil {
		writeError(w, http.StatusBadGateway, "invalid rust comment resolution response")
		return
	}
	cleared, target, wasResolvedBefore, ok := h.confirmRustCommentResolutionReceipt(r.Context(), comment, actorType, actorID, receipt, true)
	if !ok {
		writeError(w, http.StatusBadGateway, "invalid rust comment resolution response")
		return
	}
	h.finishCommentResolution(w, r, workspaceID, actorType, actorID, wasResolvedBefore, true, cleared, target)
}

func (h *Handler) unresolveCommentViaRust(
	w http.ResponseWriter,
	r *http.Request,
	comment db.Comment,
	workspaceID, actorType, actorID string,
) {
	path := "/internal/comments/" + uuidToString(comment.ID) + "/resolve"
	body, status, ok := h.readRustCommentResolutionResponse(w, r, http.MethodDelete, path, workspaceID, actorID, nil)
	if !ok {
		return
	}
	if status == http.StatusConflict {
		if routeChanged, _ := parseRustRouteChanged(body); routeChanged {
			writeErrorCode(w, http.StatusConflict, "comment_resolution_route_changed", "task comment requirements changed; reload before resolving")
			return
		}
		writeRawRustUpstreamResponse(w, status, body)
		return
	}
	if status != http.StatusOK {
		writeRawRustUpstreamResponse(w, status, body)
		return
	}
	var receipt rustCommentResolutionReceipt
	if json.Unmarshal(body, &receipt) != nil {
		writeError(w, http.StatusBadGateway, "invalid rust comment resolution response")
		return
	}
	cleared, target, wasResolvedBefore, ok := h.confirmRustCommentResolutionReceipt(r.Context(), comment, actorType, actorID, receipt, false)
	if !ok {
		writeError(w, http.StatusBadGateway, "invalid rust comment resolution response")
		return
	}
	h.finishCommentResolution(w, r, workspaceID, actorType, actorID, wasResolvedBefore, false, cleared, target)
}

func (h *Handler) readRustCommentResolutionResponse(
	w http.ResponseWriter,
	r *http.Request,
	method, path, workspaceID, userID string,
	payload []byte,
) (body []byte, status int, ok bool) {
	unavailable := "rust comment resolution service unavailable"
	if !h.IssueReadBridge.Enabled() {
		writeError(w, http.StatusServiceUnavailable, unavailable)
		return nil, 0, false
	}
	resp, routed := h.requestRustIssueWithUnavailable(w, r, method, path, workspaceID, userID, payload, unavailable)
	if !routed {
		return nil, 0, false
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxRustIssueReadBody)+1))
	if err != nil || len(raw) > maxRustIssueReadBody {
		if !writeQueryCancellation(w, r, err) {
			writeError(w, http.StatusBadGateway, "failed to read rust comment resolution response")
		}
		return nil, 0, false
	}
	return raw, resp.StatusCode, true
}

func (h *Handler) confirmRustCommentResolutionReceipt(
	ctx context.Context,
	routeComment db.Comment,
	actorType, actorID string,
	receipt rustCommentResolutionReceipt,
	resolve bool,
) (cleared []db.Comment, target db.Comment, wasResolvedBefore bool, ok bool) {
	if receipt.TargetChanged == nil || receipt.Target.ID != uuidToString(routeComment.ID) ||
		receipt.Target.DeletedAt != nil || receipt.Target.Revision < routeComment.Revision ||
		(receipt.Target.ResolvedAt != nil) != resolve ||
		(!resolve && len(receipt.Cleared) != 0) {
		return nil, db.Comment{}, false, false
	}
	changed := *receipt.TargetChanged
	if changed && receipt.Target.Revision <= routeComment.Revision {
		return nil, db.Comment{}, false, false
	}
	target, ok = h.confirmRustCommentVersion(ctx, routeComment, receipt.Target)
	if !ok || (resolve && changed &&
		(target.ResolvedByType.String != actorType || uuidToString(target.ResolvedByID) != actorID)) {
		return nil, db.Comment{}, false, false
	}
	if len(receipt.Cleared) == 0 {
		return nil, target, resolve != changed, true
	}
	targetRoot, err := h.Queries.GetCommentThreadRootID(ctx, target.ID)
	if err != nil || !targetRoot.Valid {
		return nil, db.Comment{}, false, false
	}
	seen := map[pgtype.UUID]bool{target.ID: true}
	outCleared := make([]db.Comment, 0, len(receipt.Cleared))
	for _, claim := range receipt.Cleared {
		if claim.ResolvedAt != nil {
			return nil, db.Comment{}, false, false
		}
		row, ok := h.confirmRustCommentVersion(ctx, routeComment, claim)
		if !ok || seen[row.ID] {
			return nil, db.Comment{}, false, false
		}
		root, err := h.Queries.GetCommentThreadRootID(ctx, row.ID)
		if err != nil || root != targetRoot {
			return nil, db.Comment{}, false, false
		}
		seen[row.ID] = true
		outCleared = append(outCleared, row)
	}
	return outCleared, target, resolve != changed, true
}

func commentResolutionTimestamp(value *string) (pgtype.Timestamptz, bool) {
	if value == nil {
		return pgtype.Timestamptz{}, true
	}
	parsed, err := time.Parse(time.RFC3339Nano, *value)
	return pgtype.Timestamptz{Time: parsed.Local(), Valid: err == nil}, err == nil
}

func (h *Handler) confirmRustCommentVersion(
	ctx context.Context, scope db.Comment, claim CommentResponse,
) (db.Comment, bool) {
	if claim.IssueID != uuidToString(scope.IssueID) || claim.Revision < 1 {
		return db.Comment{}, false
	}
	id, err := util.ParseUUID(claim.ID)
	if err != nil {
		return db.Comment{}, false
	}
	current, err := h.Queries.GetCommentInWorkspace(ctx, db.GetCommentInWorkspaceParams{ID: id, WorkspaceID: scope.WorkspaceID})
	if err != nil || current.IssueID != scope.IssueID || current.Revision < claim.Revision ||
		current.AuthorType != claim.AuthorType || uuidToString(current.AuthorID) != claim.AuthorID || current.Type != claim.Type {
		return db.Comment{}, false
	}
	resolved := claim.ResolvedAt != nil
	resolvedAt, timeOK := commentResolutionTimestamp(claim.ResolvedAt)
	if !timeOK {
		return db.Comment{}, false
	}
	var resolverID pgtype.UUID
	var resolverType pgtype.Text
	if resolved {
		if claim.ResolvedByID == nil || claim.ResolvedByType == nil || *claim.ResolvedByType == "" {
			return db.Comment{}, false
		}
		resolverID, err = util.ParseUUID(*claim.ResolvedByID)
		if err != nil {
			return db.Comment{}, false
		}
		resolverType = pgtype.Text{String: *claim.ResolvedByType, Valid: true}
	} else if claim.ResolvedByID != nil || claim.ResolvedByType != nil {
		return db.Comment{}, false
	}
	if current.Revision == claim.Revision {
		return current, current.ResolvedAt.Valid == resolved && current.ResolvedByID == resolverID &&
			current.ResolvedByType == resolverType && (!resolved || current.ResolvedAt.Time.Equal(resolvedAt.Time))
	}

	// A later edit or reopen can land before this earlier committed response
	// reaches Go. Preserve the earlier version in both the response and event.
	updatedAt, updateOK := commentResolutionTimestamp(&claim.UpdatedAt)
	deletedAt, deleteOK := commentResolutionTimestamp(claim.DeletedAt)
	if !updateOK || !deleteOK {
		return db.Comment{}, false
	}
	current.Revision = claim.Revision
	current.Content = claim.Content
	current.UpdatedAt = updatedAt
	current.DeletedAt = deletedAt
	current.ResolvedAt = resolvedAt
	current.ResolvedByID = resolverID
	current.ResolvedByType = resolverType
	current.SourceTaskID = pgtype.UUID{}
	if claim.SourceTaskID != nil {
		current.SourceTaskID, err = util.ParseUUID(*claim.SourceTaskID)
		if err != nil {
			return db.Comment{}, false
		}
	}
	return current, true
}

func (h *Handler) finishCommentResolution(
	w http.ResponseWriter,
	r *http.Request,
	workspaceID, actorType, actorID string,
	wasResolvedBefore, resolve bool,
	cleared []db.Comment,
	updated db.Comment,
) {
	for _, c := range cleared {
		clearedID := uuidToString(c.ID)
		clearedReactions := h.groupReactions(r, []pgtype.UUID{c.ID})
		clearedAtt := h.groupAttachments(r, []pgtype.UUID{c.ID})
		clearedResp := commentToResponse(c, clearedReactions[clearedID], clearedAtt[clearedID])
		slog.Info("comment unresolved (replaced)", append(logger.RequestAttrs(r), "comment_id", clearedID)...)
		h.publish(protocol.EventCommentUnresolved, workspaceID, actorType, actorID, map[string]any{"comment": clearedResp})
	}

	grouped := h.groupReactions(r, []pgtype.UUID{updated.ID})
	groupedAtt := h.groupAttachments(r, []pgtype.UUID{updated.ID})
	cid := uuidToString(updated.ID)
	resp := commentToResponse(updated, grouped[cid], groupedAtt[cid])

	if resolve {
		if !wasResolvedBefore {
			slog.Info("comment resolved", append(logger.RequestAttrs(r), "comment_id", cid)...)
			h.publish(protocol.EventCommentResolved, workspaceID, actorType, actorID, map[string]any{"comment": resp})
		}
	} else if wasResolvedBefore {
		slog.Info("comment unresolved", append(logger.RequestAttrs(r), "comment_id", cid)...)
		h.publish(protocol.EventCommentUnresolved, workspaceID, actorType, actorID, map[string]any{"comment": resp})
	}
	writeJSON(w, http.StatusOK, resp)
}

func parseRustRouteChanged(body []byte) (bool, bool) {
	var conflict struct {
		RouteChanged bool `json:"route_changed"`
	}
	if json.Unmarshal(body, &conflict) != nil {
		return false, false
	}
	return conflict.RouteChanged, true
}

func writeRawRustUpstreamResponse(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
