package handler

import (
	"bytes"
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

type rustIssueCommentCreate struct {
	ID         string `json:"id"`
	Content    string `json:"content"`
	Type       string `json:"type"`
	AuthorType string `json:"author_type"`
	AuthorID   string `json:"author_id"`
}

type rustIssueCommentPayload struct {
	Created           *bool            `json:"created"`
	Comment           *CommentResponse `json:"comment"`
	IssueRevision     *int64           `json:"issue_revision"`
	IssueTitle        *string          `json:"issue_title"`
	IssueStatus       *string          `json:"issue_status"`
	IssueAssigneeType *string          `json:"issue_assignee_type"`
	IssueAssigneeID   *string          `json:"issue_assignee_id"`
}

func commentCreateRouteAllowed(issue db.Issue) bool {
	if !issue.AssigneeType.Valid {
		return true
	}
	switch issue.AssigneeType.String {
	case "member":
		return true
	default:
		return false
	}
}

// commentCreateUsesRust selects plain member-authored top-level comments with no
// parsed mentions on unassigned or member-assigned tasks in the comment pilot.
func (h *Handler) commentCreateUsesRust(
	r *http.Request,
	issue db.Issue,
	req CreateCommentRequest,
	authorType string,
) (bool, error) {
	if h.IssueReadBridge == nil || len(h.IssueReadBridge.CommentPilotScopes) == 0 {
		return false, nil
	}
	if authorType != "member" {
		return false, nil
	}
	if req.ParentID != nil {
		return false, nil
	}
	if len(req.AttachmentIDs) > 0 || len(req.SuppressAgentIDs) > 0 || len(req.SteerTaskIDs) > 0 || req.Steer {
		return false, nil
	}
	if isNoteComment(req.Content) {
		return false, nil
	}
	if req.Type != "comment" {
		return false, nil
	}
	if len(util.ParseMentions(req.Content)) > 0 {
		return false, nil
	}
	if !commentCreateRouteAllowed(issue) {
		return false, nil
	}
	return matchesIssuePilot(r.Context(), h.Queries, h.IssueReadBridge.CommentPilotScopes, uuidToString(issue.WorkspaceID), issue.WorkspaceID)
}

func (h *Handler) createCommentViaRust(
	w http.ResponseWriter,
	r *http.Request,
	issue db.Issue,
	userID, authorType, authorID string,
	commentID pgtype.UUID,
	req CreateCommentRequest,
) {
	request := rustIssueCommentCreate{
		ID:         uuidToString(commentID),
		Content:    req.Content,
		Type:       req.Type,
		AuthorType: authorType,
		AuthorID:   authorID,
	}
	payload, err := json.Marshal(request)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode comment write")
		return
	}
	path := "/internal/issues/" + uuidToString(issue.ID) + "/comments"
	resp, ok := h.requestRustIssueComment(w, r, path, uuidToString(issue.WorkspaceID), userID, payload)
	if !ok {
		return
	}
	defer resp.Body.Close()
	limit := int64(maxRustIssueReadBody) + int64(len(req.Content))*2
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || int64(len(body)) > limit {
		if !writeQueryCancellation(w, r, err) {
			writeError(w, http.StatusBadGateway, "failed to read rust comment write response")
		}
		return
	}
	if resp.StatusCode == http.StatusConflict {
		var conflict struct {
			RouteChanged bool `json:"route_changed"`
		}
		if json.Unmarshal(body, &conflict) == nil && conflict.RouteChanged {
			writeErrorCode(w, http.StatusConflict, "comment_route_changed", "task comment requirements changed; reload before commenting")
			return
		}
		writeRustIssueResponse(w, resp, bytes.NewReader(body))
		return
	}
	if resp.StatusCode == http.StatusOK {
		var receipt rustIssueCommentPayload
		if json.Unmarshal(body, &receipt) != nil || receipt.Created == nil || *receipt.Created || receipt.Comment != nil {
			writeError(w, http.StatusBadGateway, "invalid rust comment write response")
			return
		}
		if !h.replayRequestedComment(w, r, commentID, issue, authorType, authorID) {
			writeError(w, http.StatusBadGateway, "invalid rust comment write response")
		}
		return
	}
	if resp.StatusCode != http.StatusCreated {
		writeRustIssueResponse(w, resp, bytes.NewReader(body))
		return
	}
	var receipt rustIssueCommentPayload
	if json.Unmarshal(body, &receipt) != nil || !rustCommentCreateReceiptValid(issue, authorType, authorID, commentID, req, receipt) {
		writeError(w, http.StatusBadGateway, "invalid rust comment write response")
		return
	}
	issueForEvent := issueFromCommentReceipt(issue, receipt)
	groupedAtt := h.groupAttachments(r, []pgtype.UUID{commentID})
	respComment := *receipt.Comment
	respComment.Reactions = []ReactionResponse{}
	if atts, ok := groupedAtt[uuidToString(commentID)]; ok {
		respComment.Attachments = atts
	} else {
		respComment.Attachments = []AttachmentResponse{}
	}
	respComment.IssueRevision = *receipt.IssueRevision
	slog.Info("comment created", append(logger.RequestAttrs(r), "comment_id", uuidToString(commentID), "issue_id", uuidToString(issue.ID))...)
	h.publishCommentCreatedEvent(issueForEvent, respComment, *receipt.IssueRevision, authorType, authorID)
	writeJSON(w, http.StatusCreated, respComment)
}

func (h *Handler) requestRustIssueComment(w http.ResponseWriter, r *http.Request, path, workspaceID, userID string, payload []byte) (*http.Response, bool) {
	unavailable := "rust issue comment write service unavailable"
	if !h.IssueReadBridge.Enabled() {
		writeError(w, http.StatusServiceUnavailable, unavailable)
		return nil, false
	}
	return h.requestRustIssueWithUnavailable(w, r, http.MethodPost, path, workspaceID, userID, payload, unavailable)
}

func issueFromCommentReceipt(issue db.Issue, receipt rustIssueCommentPayload) db.Issue {
	out := issue
	if receipt.IssueRevision != nil {
		out.Revision = *receipt.IssueRevision
	}
	if receipt.IssueTitle != nil {
		out.Title = *receipt.IssueTitle
	}
	if receipt.IssueStatus != nil {
		out.Status = *receipt.IssueStatus
	}
	if receipt.IssueAssigneeType != nil {
		out.AssigneeType = pgtype.Text{String: *receipt.IssueAssigneeType, Valid: true}
	} else {
		out.AssigneeType = pgtype.Text{}
	}
	if receipt.IssueAssigneeID != nil {
		out.AssigneeID = parseUUID(*receipt.IssueAssigneeID)
	} else {
		out.AssigneeID = pgtype.UUID{}
	}
	return out
}

func rustCommentCreateReceiptValid(
	issue db.Issue,
	authorType, authorID string,
	commentID pgtype.UUID,
	req CreateCommentRequest,
	receipt rustIssueCommentPayload,
) bool {
	if receipt.Created == nil || !*receipt.Created || receipt.Comment == nil {
		return false
	}
	comment := *receipt.Comment
	if comment.ID != uuidToString(commentID) {
		return false
	}
	if comment.IssueID != uuidToString(issue.ID) {
		return false
	}
	if comment.AuthorType != authorType || comment.AuthorID != authorID {
		return false
	}
	if comment.Content != req.Content || comment.Type != req.Type {
		return false
	}
	if comment.ParentID != nil {
		return false
	}
	if receipt.IssueRevision == nil || *receipt.IssueRevision <= issue.Revision {
		return false
	}
	if receipt.IssueTitle == nil || receipt.IssueStatus == nil || *receipt.IssueStatus == "" {
		return false
	}
	return true
}

func (h *Handler) publishCommentCreatedEvent(issue db.Issue, resp CommentResponse, issueRevision int64, authorType, authorID string) {
	h.publish(protocol.EventCommentCreated, uuidToString(issue.WorkspaceID), authorType, authorID, map[string]any{
		"comment":             resp,
		"issue_title":         issue.Title,
		"issue_assignee_type": textToPtr(issue.AssigneeType),
		"issue_assignee_id":   uuidToPtr(issue.AssigneeID),
		"issue_status":        issue.Status,
		"issue_revision":      issueRevision,
	})
}
