package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/projectaccess"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type rustIssueRelativeMove struct {
	ExpectedRevision *int64  `json:"expected_revision,omitempty"`
	BeforeID         *string `json:"before_id"`
	AfterID          *string `json:"after_id"`
	SourceTaskID     *string `json:"source_task_id,omitempty"`
}

type rustIssueRelativeMoveWrite struct {
	Before  ordinaryIssueSnapshot `json:"before"`
	After   ordinaryIssueSnapshot `json:"after"`
	Changed bool                  `json:"changed"`
}

func issueMovePositionOnly(fields map[string]json.RawMessage) bool {
	for key := range fields {
		switch key {
		case "before_id", "after_id", "expected_revision":
		default:
			return false
		}
	}
	return true
}

func (h *Handler) issueMoveUsesRust(r *http.Request, workspaceID pgtype.UUID) (bool, error) {
	if h.IssueReadBridge == nil || len(h.IssueReadBridge.WritePilotScopes) == 0 {
		return false, nil
	}
	return matchesIssuePilot(r.Context(), h.Queries, h.IssueReadBridge.WritePilotScopes, uuidToString(workspaceID), workspaceID)
}

func parseMoveExpectedRevision(w http.ResponseWriter, fields map[string]json.RawMessage) (*int64, bool) {
	raw, ok := fields["expected_revision"]
	if !ok {
		return nil, true
	}
	if rawJSONNull(raw) {
		return nil, true
	}
	var revision int64
	if err := json.Unmarshal(raw, &revision); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return nil, false
	}
	if revision < 1 {
		writeError(w, http.StatusBadRequest, "expected_revision must be a positive integer")
		return nil, false
	}
	return &revision, true
}

func rustIssueRelativeMoveWriteValid(issueID, workspaceID pgtype.UUID, write rustIssueRelativeMoveWrite) bool {
	if write.Before.ID != issueID || write.Before.WorkspaceID != workspaceID ||
		write.After.ID != issueID || write.After.WorkspaceID != workspaceID {
		return false
	}
	revisionDelta := write.After.Revision - write.Before.Revision
	switch {
	case write.Changed:
		return revisionDelta == 1
	case !write.Changed:
		return revisionDelta == 0
	default:
		return false
	}
}

func (h *Handler) moveIssuePositionOnlyViaRust(
	w http.ResponseWriter,
	r *http.Request,
	userID string,
	current db.Issue,
	beforeID, afterID *pgtype.UUID,
	expectedRevision *int64,
) bool {
	request := rustIssueRelativeMove{
		SourceTaskID: uuidToPtr(h.wakeupSourceTaskID(r)),
		ExpectedRevision: expectedRevision,
	}
	if beforeID != nil {
		id := uuidToString(*beforeID)
		request.BeforeID = &id
	}
	if afterID != nil {
		id := uuidToString(*afterID)
		request.AfterID = &id
	}
	if expectedRevision != nil && current.Revision != *expectedRevision {
		writeRevisionConflict(w, "issue", current.ID, *expectedRevision, current.Revision)
		return false
	}
	payload, err := json.Marshal(request)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode issue move")
		return false
	}
	resp, requested := h.requestRustIssue(w, r, http.MethodPost, "/internal/issues/"+uuidToString(current.ID)+"/move", uuidToString(current.WorkspaceID), userID, payload)
	if !requested {
		return false
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxRustIssueReadBody)+1))
	if err != nil || len(body) > maxRustIssueReadBody {
		if !writeQueryCancellation(w, r, err) {
			writeError(w, http.StatusBadGateway, "failed to read rust issue move response")
		}
		return false
	}
	if resp.StatusCode == http.StatusConflict {
		var conflict struct {
			ActualRevision int64 `json:"actual_revision"`
		}
		if json.Unmarshal(body, &conflict) == nil && conflict.ActualRevision > 0 && expectedRevision != nil {
			writeRevisionConflict(w, "issue", current.ID, *expectedRevision, conflict.ActualRevision)
			return false
		}
		writeRustIssueResponse(w, resp, bytes.NewReader(body))
		return false
	}
	if resp.StatusCode != http.StatusOK {
		writeRustIssueResponse(w, resp, bytes.NewReader(body))
		return false
	}
	var write rustIssueRelativeMoveWrite
	if json.Unmarshal(body, &write) != nil || !rustIssueRelativeMoveWriteValid(current.ID, current.WorkspaceID, write) {
		writeError(w, http.StatusBadGateway, "invalid rust issue move response")
		return false
	}
	h.writeIssueRelativeMove(w, r, userID, write)
	return true
}

func (h *Handler) writeIssueRelativeMove(w http.ResponseWriter, r *http.Request, userID string, write rustIssueRelativeMoveWrite) {
	before := write.Before.issue()
	after := write.After.issue()
	workspaceID := uuidToString(after.WorkspaceID)
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	prefix := h.getIssuePrefix(r.Context(), after.WorkspaceID)
	resp := issueToResponse(after, prefix)
	h.fillStatusCategory(r.Context(), after.WorkspaceID, projectaccess.Hidden{}, &resp)
	if write.Changed {
		updatedPayload := map[string]any{
			"issue":                      resp,
			"assignee_changed":           false,
			"status_changed":             false,
			"priority_changed":           false,
			"project_changed":            false,
			"start_date_changed":         false,
			"due_date_changed":           false,
			"description_changed":        false,
			"title_changed":              false,
			"parent_changed":             before.ParentIssueID != after.ParentIssueID,
			"prev_parent_issue_id":       uuidToPtr(before.ParentIssueID),
			"prev_title":                 before.Title,
			"prev_assignee_type":         textToPtr(before.AssigneeType),
			"prev_assignee_id":           uuidToPtr(before.AssigneeID),
			"prev_status":                before.Status,
			"prev_priority":              before.Priority,
			"prev_start_date":            dateToPtr(before.StartDate),
			"prev_due_date":              dateToPtr(before.DueDate),
			"prev_description":           textToPtr(before.Description),
			"creator_type":               before.CreatorType,
			"creator_id":                 uuidToString(before.CreatorID),
			"duplicate_of_issue_id":      liveDuplicateMark(after.Status, after.DuplicateOfIssueID),
			"prev_duplicate_of_issue_id": liveDuplicateMark(before.Status, before.DuplicateOfIssueID),
		}
		h.publishIssueUpdated(workspaceID, actorType, actorID, updatedPayload, "")
	}
	if resp.DuplicateOf != nil {
		resp.DuplicateOf = nil
		if hidden, err := h.hiddenProjects(r, workspaceID); err == nil {
			h.fillStatusCategory(r.Context(), after.WorkspaceID, hidden, &resp)
		}
	}
	writeJSON(w, http.StatusOK, resp)
}