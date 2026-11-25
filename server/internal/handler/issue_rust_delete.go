package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/multica-ai/multica/server/internal/logger"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type rustIssueDeleteWrite struct {
	SourceTaskID      *string                             `json:"source_task_id,omitempty"`
	AttachmentCleanup []rustIssueDeleteAttachmentPlanItem `json:"attachment_cleanup,omitempty"`
}

type rustIssueDeleteClearedDuplicate struct {
	Issue           string `json:"issue"`
	OriginalIssueID string `json:"original_issue_id"`
}

type rustIssueDeleteResult struct {
	Deleted                    bool                              `json:"deleted"`
	IssueID                    string                            `json:"issue_id"`
	DetachedChildren           []string                          `json:"detached_children"`
	ClearedDuplicates          []rustIssueDeleteClearedDuplicate `json:"cleared_duplicates"`
	GoalLinksChanged           bool                              `json:"goal_links_changed"`
	ObjectCleanupAttachmentIDs []string                          `json:"object_cleanup_attachment_ids,omitempty"`
}

// issueOrdinaryDeleteEligible reports whether a task matches the supported
// delete shape without queue, storage, or source-context side effects.
func (h *Handler) issueOrdinaryDeleteEligible(ctx context.Context, issue db.Issue) (bool, error) {
	if issue.CreatorType != "member" {
		return false, nil
	}
	if issue.OriginType.Valid {
		return false, nil
	}
	if issue.AssigneeType.Valid && issue.AssigneeType.String != "member" {
		return false, nil
	}
	urls, err := h.Queries.ListAttachmentURLsByIssueOrComments(ctx, issue.ID)
	if err != nil {
		return false, err
	}
	if len(urls) > 0 {
		return false, nil
	}
	if _, err := h.Queries.GetIssueSourceContextByIssue(ctx, db.GetIssueSourceContextByIssueParams{
		WorkspaceID: issue.WorkspaceID,
		IssueID:     issue.ID,
	}); err == nil {
		return false, nil
	} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	hasTask, err := h.Queries.HasTaskForIssue(ctx, issue.ID)
	if err != nil {
		return false, err
	}
	if hasTask {
		return false, nil
	}
	if _, err := h.Queries.GetAutopilotRunByIssue(ctx, issue.ID); err == nil {
		return false, nil
	} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	return true, nil
}

type rustIssueDeleteSelection struct {
	UseRust        bool
	AttachmentPlan []rustIssueDeleteAttachmentPlanItem
}

// selectRustIssueDelete selects member-authenticated deletes of member-created
// tasks that do not need runtime queue cancellation, autopilot failure, or
// source-context object intents. When direct uploads are included, the returned
// attachment plan is fixed for the trusted Rust payload. Eligibility is
// rechecked under the Rust lock.
func (h *Handler) selectRustIssueDelete(r *http.Request, issue db.Issue) (rustIssueDeleteSelection, error) {
	var none rustIssueDeleteSelection
	if h.IssueReadBridge == nil || len(h.IssueReadBridge.DeletePilotScopes) == 0 {
		return none, nil
	}
	ctx := r.Context()
	inPilot, err := matchesIssuePilot(ctx, h.Queries, h.IssueReadBridge.DeletePilotScopes, uuidToString(issue.WorkspaceID), issue.WorkspaceID)
	if err != nil || !inPilot {
		return none, err
	}
	actorType, _ := h.resolveActor(r, requestUserID(r), uuidToString(issue.WorkspaceID))
	if actorType != "member" {
		return none, nil
	}
	if ok, err := h.issueOrdinaryDeleteEligible(ctx, issue); err != nil {
		return none, err
	} else if ok {
		return rustIssueDeleteSelection{UseRust: true}, nil
	}
	if h.IssueReadBridge == nil || len(h.IssueReadBridge.DeleteAttachPilotScopes) == 0 {
		return none, nil
	}
	inAttachPilot, err := matchesIssuePilot(ctx, h.Queries, h.IssueReadBridge.DeleteAttachPilotScopes, uuidToString(issue.WorkspaceID), issue.WorkspaceID)
	if err != nil || !inAttachPilot {
		return none, err
	}
	plan, withAttachments, err := h.buildRustIssueDeleteAttachmentPlan(ctx, issue)
	if err != nil {
		return none, err
	}
	if !withAttachments {
		return none, nil
	}
	return rustIssueDeleteSelection{UseRust: true, AttachmentPlan: plan}, nil
}

func (h *Handler) deleteIssueViaRust(w http.ResponseWriter, r *http.Request, issue db.Issue, userID string, attachmentPlan []rustIssueDeleteAttachmentPlanItem) {
	request := rustIssueDeleteWrite{SourceTaskID: uuidToPtr(h.wakeupSourceTaskID(r))}
	withAttachments := len(attachmentPlan) > 0
	if withAttachments {
		request.AttachmentCleanup = attachmentPlan
	}
	payload, err := json.Marshal(request)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode issue delete")
		return
	}
	path := "/internal/issues/" + uuidToString(issue.ID)
	resp, routed := h.requestRustIssueWithUnavailable(w, r, http.MethodDelete, path, uuidToString(issue.WorkspaceID), userID, payload, "rust issue delete service unavailable")
	if !routed {
		return
	}
	defer resp.Body.Close()
	limit := int64(maxRustIssueReadBody)
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || int64(len(body)) > limit {
		if !writeQueryCancellation(w, r, err) {
			writeError(w, http.StatusBadGateway, "failed to read rust issue delete response")
		}
		return
	}
	if resp.StatusCode == http.StatusConflict {
		var conflict struct {
			DeleteSensitive bool `json:"delete_sensitive"`
		}
		if json.Unmarshal(body, &conflict) == nil && conflict.DeleteSensitive {
			writeErrorCode(w, http.StatusConflict, "delete_route_changed", "task deletion requirements changed; reload before deleting")
			return
		}
		writeError(w, http.StatusBadGateway, "invalid rust issue delete conflict response")
		return
	}
	if resp.StatusCode != http.StatusOK {
		writeRustIssueResponse(w, resp, bytes.NewReader(body))
		return
	}
	var reply rustIssueDeleteResult
	if json.Unmarshal(body, &reply) != nil || !reply.Deleted || reply.IssueID != uuidToString(issue.ID) {
		writeError(w, http.StatusBadGateway, "invalid rust issue delete response")
		return
	}
	if withAttachments {
		if !rustIssueDeleteAttachmentIDsMatchPlan(attachmentPlan, reply.ObjectCleanupAttachmentIDs) {
			writeError(w, http.StatusBadGateway, "invalid rust issue delete object cleanup receipt")
			return
		}
		if h.TaskService != nil {
			_, _ = h.TaskService.TryDeliverIssueAttachmentObjectDeletes(r.Context(), planAttachmentIDs(attachmentPlan))
		}
	} else if len(reply.ObjectCleanupAttachmentIDs) > 0 {
		writeError(w, http.StatusBadGateway, "invalid rust issue delete response")
		return
	}
	detached, cleared, ok := rustIssueDeleteSnapshots(issue, reply)
	if !ok {
		writeError(w, http.StatusBadGateway, "invalid rust issue delete snapshots")
		return
	}
	actorType, actorID := h.resolveActor(r, userID, uuidToString(issue.WorkspaceID))
	resolvedID := uuidToString(issue.ID)
	h.publish(protocol.EventIssueDeleted, uuidToString(issue.WorkspaceID), actorType, actorID, map[string]any{"issue_id": resolvedID})
	h.publishIssueSnapshots(r.Context(), detached, actorType, actorID)
	h.publishClearedDuplicates(r.Context(), cleared, actorType, actorID)
	if reply.GoalLinksChanged {
		h.publishGoalChanged(uuidToString(issue.WorkspaceID), actorType, actorID)
	}
	slog.Info("issue deleted", append(logger.RequestAttrs(r), "issue_id", resolvedID, "workspace_id", uuidToString(issue.WorkspaceID), "backend", "rust")...)
	w.WriteHeader(http.StatusNoContent)
}

func rustIssueDeleteSnapshots(original db.Issue, reply rustIssueDeleteResult) ([]db.Issue, []clearedDuplicate, bool) {
	resolvedID := uuidToString(original.ID)
	detached := make([]db.Issue, 0, len(reply.DetachedChildren))
	for _, raw := range reply.DetachedChildren {
		var child ordinaryIssueSnapshot
		if json.Unmarshal([]byte(raw), &child) != nil || !child.ID.Valid || child.WorkspaceID != original.WorkspaceID {
			return nil, nil, false
		}
		detached = append(detached, child.issue())
	}
	cleared := make([]clearedDuplicate, 0, len(reply.ClearedDuplicates))
	for _, duplicate := range reply.ClearedDuplicates {
		if duplicate.OriginalIssueID != resolvedID || duplicate.Issue == "" {
			return nil, nil, false
		}
		var snapshot ordinaryIssueSnapshot
		if json.Unmarshal([]byte(duplicate.Issue), &snapshot) != nil || !snapshot.ID.Valid || snapshot.WorkspaceID != original.WorkspaceID {
			return nil, nil, false
		}
		cleared = append(cleared, clearedDuplicate{Issue: snapshot.issue(), Original: original})
	}
	return detached, cleared, true
}
