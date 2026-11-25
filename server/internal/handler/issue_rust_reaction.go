package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type rustIssueReactionWrite struct {
	Emoji string `json:"emoji"`
}

type rustIssueReactionAddReceipt struct {
	Changed       *bool                 `json:"changed"`
	Reaction      IssueReactionResponse `json:"reaction"`
	IssueRevision int64                 `json:"issue_revision"`
}

type rustIssueReactionRemoveReceipt struct {
	Changed       *bool `json:"changed"`
	IssueRevision int64 `json:"issue_revision"`
}

func (h *Handler) issueReactionUsesRust(r *http.Request, issue db.Issue, actorType string) (bool, error) {
	if h.IssueReadBridge == nil || len(h.IssueReadBridge.ReactionPilotScopes) == 0 {
		return false, nil
	}
	if actorType != "member" {
		return false, nil
	}
	if issue.ProjectID.Valid {
		return false, nil
	}
	return matchesIssuePilot(r.Context(), h.Queries, h.IssueReadBridge.ReactionPilotScopes, uuidToString(issue.WorkspaceID), issue.WorkspaceID)
}

func (h *Handler) addIssueReactionViaRust(
	w http.ResponseWriter,
	r *http.Request,
	issue db.Issue,
	userID, actorType, actorID, emoji string,
) {
	body, ok := h.readRustIssueReactionResponse(w, r, http.MethodPost, issue, userID, emoji, http.StatusCreated)
	if !ok {
		return
	}
	var receipt rustIssueReactionAddReceipt
	if json.Unmarshal(body, &receipt) != nil {
		writeError(w, http.StatusBadGateway, "invalid rust issue reaction response")
		return
	}
	if !h.confirmIssueReactionAddReceipt(r.Context(), issue, actorType, actorID, emoji, receipt) {
		writeError(w, http.StatusBadGateway, "invalid rust issue reaction response")
		return
	}
	response := receipt.Reaction
	createdAt, _ := time.Parse(time.RFC3339, response.CreatedAt)
	response.CreatedAt = createdAt.Local().Format(time.RFC3339)
	response.IssueRevision = nil
	if *receipt.Changed {
		response.IssueRevision = &receipt.IssueRevision
		h.publishIssueReactionAddedEvent(issue, uuidToString(issue.WorkspaceID), actorType, actorID, response, receipt.IssueRevision)
	}
	writeJSON(w, http.StatusCreated, response)
}

func (h *Handler) removeIssueReactionViaRust(
	w http.ResponseWriter,
	r *http.Request,
	issue db.Issue,
	userID, actorType, actorID, emoji string,
) {
	body, ok := h.readRustIssueReactionResponse(w, r, http.MethodDelete, issue, userID, emoji, http.StatusOK)
	if !ok {
		return
	}
	var receipt rustIssueReactionRemoveReceipt
	if json.Unmarshal(body, &receipt) != nil ||
		!issueReactionReceiptRevisionValid(issue.Revision, receipt.IssueRevision, receipt.Changed) {
		writeError(w, http.StatusBadGateway, "invalid rust issue reaction response")
		return
	}
	current, ok := h.currentIssueRevision(r.Context(), issue)
	if !ok || current < receipt.IssueRevision {
		writeError(w, http.StatusBadGateway, "invalid rust issue reaction response")
		return
	}
	if *receipt.Changed {
		h.publishIssueReactionRemovedEvent(issue, uuidToString(issue.WorkspaceID), actorType, actorID, emoji, receipt.IssueRevision)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) readRustIssueReactionResponse(
	w http.ResponseWriter,
	r *http.Request,
	method string,
	issue db.Issue,
	userID, emoji string,
	wantStatus int,
) ([]byte, bool) {
	payload, err := json.Marshal(rustIssueReactionWrite{Emoji: emoji})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode issue reaction write")
		return nil, false
	}
	const unavailable = "rust issue reaction write service unavailable"
	if !h.IssueReadBridge.Enabled() {
		writeError(w, http.StatusServiceUnavailable, unavailable)
		return nil, false
	}
	path := "/internal/issues/" + uuidToString(issue.ID) + "/reactions"
	resp, routed := h.requestRustIssueWithUnavailable(w, r, method, path, uuidToString(issue.WorkspaceID), userID, payload, unavailable)
	if !routed {
		return nil, false
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxRustIssueReadBody)+1))
	if err != nil || len(body) > maxRustIssueReadBody {
		if !writeQueryCancellation(w, r, err) {
			writeError(w, http.StatusBadGateway, "failed to read rust issue reaction response")
		}
		return nil, false
	}
	if resp.StatusCode == http.StatusConflict {
		var conflict struct {
			RouteChanged bool `json:"route_changed"`
		}
		if json.Unmarshal(body, &conflict) == nil && conflict.RouteChanged {
			writeErrorCode(w, http.StatusConflict, "issue_reaction_route_changed", "task reaction requirements changed; reload before reacting")
			return nil, false
		}
	}
	if resp.StatusCode != wantStatus {
		writeRustIssueResponse(w, resp, bytes.NewReader(body))
		return nil, false
	}
	return body, true
}

func issueReactionReceiptRevisionValid(previous, observed int64, changed *bool) bool {
	if changed == nil || observed < 1 {
		return false
	}
	if *changed {
		return observed > previous
	}
	return observed >= previous
}

func (h *Handler) currentIssueRevision(ctx context.Context, issue db.Issue) (int64, bool) {
	row, err := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{
		ID: issue.ID, WorkspaceID: issue.WorkspaceID,
	})
	return row.Revision, err == nil
}

func (h *Handler) confirmIssueReactionAddReceipt(
	ctx context.Context,
	issue db.Issue,
	actorType, actorID, emoji string,
	receipt rustIssueReactionAddReceipt,
) bool {
	rx := receipt.Reaction
	if !issueReactionReceiptRevisionValid(issue.Revision, receipt.IssueRevision, receipt.Changed) ||
		rx.IssueID != uuidToString(issue.ID) || rx.ActorType != actorType || rx.ActorID != actorID || rx.Emoji != emoji {
		return false
	}
	id, err := util.ParseUUID(rx.ID)
	if err != nil {
		return false
	}
	createdAt, err := time.Parse(time.RFC3339, rx.CreatedAt)
	if err != nil {
		return false
	}
	rows, err := h.Queries.ListIssueReactions(ctx, issue.ID)
	if err != nil {
		return false
	}
	matched := false
	for _, row := range rows {
		if row.ID == id {
			matched = row.ActorType == actorType && uuidToString(row.ActorID) == actorID && row.Emoji == emoji &&
				row.CreatedAt.Time.Truncate(time.Second).Equal(createdAt)
			if !matched {
				return false
			}
			break
		}
	}
	// Read the task revision after the reaction lookup so a concurrent removal
	// can establish that the response describes an earlier committed state.
	current, ok := h.currentIssueRevision(ctx, issue)
	return ok && current >= receipt.IssueRevision && (matched || current > receipt.IssueRevision)
}

func (h *Handler) publishIssueReactionAddedEvent(
	issue db.Issue,
	workspaceID, actorType, actorID string,
	respReaction IssueReactionResponse,
	issueRevision int64,
) {
	h.publish(protocol.EventIssueReactionAdded, workspaceID, actorType, actorID, map[string]any{
		"reaction":       respReaction,
		"issue_id":       uuidToString(issue.ID),
		"issue_title":    issue.Title,
		"issue_status":   issue.Status,
		"creator_type":   issue.CreatorType,
		"creator_id":     uuidToString(issue.CreatorID),
		"issue_revision": issueRevision,
	})
}

func (h *Handler) publishIssueReactionRemovedEvent(
	issue db.Issue,
	workspaceID, actorType, actorID, emoji string,
	issueRevision int64,
) {
	h.publish(protocol.EventIssueReactionRemoved, workspaceID, actorType, actorID, map[string]any{
		"issue_id":       uuidToString(issue.ID),
		"emoji":          emoji,
		"actor_type":     actorType,
		"actor_id":       actorID,
		"issue_revision": issueRevision,
	})
}
