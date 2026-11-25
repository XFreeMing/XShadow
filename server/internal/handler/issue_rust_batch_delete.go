package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"github.com/multica-ai/multica/server/internal/logger"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type rustIssueBatchDeleteWrite struct {
	IssueIDs     []string `json:"issue_ids"`
	SourceTaskID *string  `json:"source_task_id,omitempty"`
}

type rustIssueBatchDeleteResult struct {
	Deleted           int                               `json:"deleted"`
	DeletedIssueIDs   []string                          `json:"deleted_issue_ids"`
	DetachedChildren  []string                          `json:"detached_children"`
	ClearedDuplicates []rustIssueDeleteClearedDuplicate `json:"cleared_duplicates"`
	GoalLinksChanged  bool                              `json:"goal_links_changed"`
}

// batchDeleteIssuesUsesRust selects member-authenticated batch deletes where
// every visible selected task matches the ordinary delete shape. Mixed or
// unsupported batches stay on Go.
func (h *Handler) batchDeleteIssuesUsesRust(r *http.Request, issues []db.Issue) (bool, error) {
	if h.IssueReadBridge == nil || len(h.IssueReadBridge.BatchDeletePilotScopes) == 0 || len(issues) == 0 {
		return false, nil
	}
	workspaceID := issues[0].WorkspaceID
	ctx := r.Context()
	inPilot, err := matchesIssuePilot(ctx, h.Queries, h.IssueReadBridge.BatchDeletePilotScopes, uuidToString(workspaceID), workspaceID)
	if err != nil || !inPilot {
		return false, err
	}
	actorType, _ := h.resolveActor(r, requestUserID(r), uuidToString(workspaceID))
	if actorType != "member" {
		return false, nil
	}
	for _, issue := range issues {
		if issue.WorkspaceID != workspaceID {
			return false, nil
		}
		ok, err := h.issueOrdinaryDeleteEligible(ctx, issue)
		if err != nil || !ok {
			return false, err
		}
	}
	return true, nil
}

func (h *Handler) batchDeleteIssuesViaRust(
	w http.ResponseWriter,
	r *http.Request,
	issues []db.Issue,
	userID, workspaceID string,
) {
	issueIDs := make([]string, len(issues))
	byID := make(map[string]db.Issue, len(issues))
	for i, issue := range issues {
		id := uuidToString(issue.ID)
		issueIDs[i] = id
		byID[id] = issue
	}
	request := rustIssueBatchDeleteWrite{
		IssueIDs:     issueIDs,
		SourceTaskID: uuidToPtr(h.wakeupSourceTaskID(r)),
	}
	payload, err := json.Marshal(request)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode batch delete")
		return
	}
	resp, routed := h.requestRustIssueWithUnavailable(
		w, r, http.MethodPost, "/internal/issues/batch-delete", workspaceID, userID, payload,
		"rust issue batch delete service unavailable",
	)
	if !routed {
		return
	}
	defer resp.Body.Close()
	limit := int64(maxRustIssueReadBody)
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || int64(len(body)) > limit {
		if !writeQueryCancellation(w, r, err) {
			writeError(w, http.StatusBadGateway, "failed to read rust issue batch delete response")
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
		writeError(w, http.StatusBadGateway, "invalid rust issue batch delete conflict response")
		return
	}
	if resp.StatusCode != http.StatusOK {
		writeRustIssueResponse(w, resp, bytes.NewReader(body))
		return
	}
	var reply rustIssueBatchDeleteResult
	if json.Unmarshal(body, &reply) != nil {
		writeError(w, http.StatusBadGateway, "invalid rust issue batch delete response")
		return
	}
	if reply.Deleted != len(issues) || len(reply.DeletedIssueIDs) != len(issues) {
		writeError(w, http.StatusBadGateway, "invalid rust issue batch delete response")
		return
	}
	deleted := make(map[string]struct{}, len(reply.DeletedIssueIDs))
	for _, id := range reply.DeletedIssueIDs {
		deleted[id] = struct{}{}
	}
	for _, issue := range issues {
		if _, ok := deleted[uuidToString(issue.ID)]; !ok {
			writeError(w, http.StatusBadGateway, "invalid rust issue batch delete response")
			return
		}
	}
	detached, cleared, ok := rustIssueBatchDeleteSnapshots(issues, reply)
	if !ok {
		writeError(w, http.StatusBadGateway, "invalid rust issue batch delete snapshots")
		return
	}
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	for _, issue := range issues {
		h.publish(protocol.EventIssueDeleted, workspaceID, actorType, actorID, map[string]any{"issue_id": uuidToString(issue.ID)})
	}
	h.publishIssueSnapshots(r.Context(), detached, actorType, actorID)
	h.publishClearedDuplicates(r.Context(), cleared, actorType, actorID)
	if reply.GoalLinksChanged {
		h.publishGoalChanged(workspaceID, actorType, actorID)
	}
	slog.Info("batch delete issues", append(logger.RequestAttrs(r), "count", reply.Deleted, "workspace_id", workspaceID, "backend", "rust")...)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": reply.Deleted})
}

func rustIssueBatchDeleteSnapshots(issues []db.Issue, reply rustIssueBatchDeleteResult) ([]db.Issue, []clearedDuplicate, bool) {
	if len(issues) == 0 {
		return nil, nil, false
	}
	workspaceID := issues[0].WorkspaceID
	originals := make(map[string]db.Issue, len(issues))
	for _, issue := range issues {
		originals[uuidToString(issue.ID)] = issue
	}
	detached := make([]db.Issue, 0, len(reply.DetachedChildren))
	for _, raw := range reply.DetachedChildren {
		var child ordinaryIssueSnapshot
		if json.Unmarshal([]byte(raw), &child) != nil || !child.ID.Valid || child.WorkspaceID != workspaceID {
			return nil, nil, false
		}
		detached = append(detached, child.issue())
	}
	cleared := make([]clearedDuplicate, 0, len(reply.ClearedDuplicates))
	for _, duplicate := range reply.ClearedDuplicates {
		original, ok := originals[duplicate.OriginalIssueID]
		if !ok || duplicate.Issue == "" {
			return nil, nil, false
		}
		var snapshot ordinaryIssueSnapshot
		if json.Unmarshal([]byte(duplicate.Issue), &snapshot) != nil || !snapshot.ID.Valid || snapshot.WorkspaceID != workspaceID {
			return nil, nil, false
		}
		cleared = append(cleared, clearedDuplicate{Issue: snapshot.issue(), Original: original})
	}
	return detached, cleared, true
}
