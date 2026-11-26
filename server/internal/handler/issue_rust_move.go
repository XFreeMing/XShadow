package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/projectaccess"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type rustIssueRelativeMove struct {
	ExpectedRevision *int64  `json:"expected_revision,omitempty"`
	BeforeID         *string `json:"before_id"`
	AfterID          *string `json:"after_id"`
	SourceTaskID     *string `json:"source_task_id,omitempty"`
	ParentTouched    bool    `json:"parent_touched,omitempty"`
	ParentClear      bool    `json:"parent_clear,omitempty"`
	ParentIssueID    *string `json:"parent_issue_id,omitempty"`
	ProjectTouched   bool    `json:"project_touched,omitempty"`
	ProjectClear     bool    `json:"project_clear,omitempty"`
	ProjectID        *string `json:"project_id,omitempty"`
}

type rustIssueRelativeMoveWrite struct {
	Before  ordinaryIssueSnapshot `json:"before"`
	After   ordinaryIssueSnapshot `json:"after"`
	Changed bool                  `json:"changed"`
}

// issueMoveRustEligible selects position and optional parent_issue_id or
// project_id moves on the write pilot, but not both parent and project in one
// request until combined boundaries are verified (see rust/MAP.md).
func issueMoveRustEligible(fields map[string]json.RawMessage) bool {
	_, hasParent := fields["parent_issue_id"]
	_, hasProject := fields["project_id"]
	if hasParent && hasProject {
		return false
	}
	for key := range fields {
		switch key {
		case "before_id", "after_id", "expected_revision", "parent_issue_id", "project_id":
		default:
			return false
		}
	}
	return true
}

type issueMoveParentIntent struct {
	touched bool
	clear   bool
	parent  pgtype.UUID
}

type issueMoveProjectIntent struct {
	touched bool
	clear   bool
	project pgtype.UUID
}

func decodeIssueMoveProjectIntent(w http.ResponseWriter, fields map[string]json.RawMessage) (issueMoveProjectIntent, bool) {
	raw, ok := fields["project_id"]
	if !ok {
		return issueMoveProjectIntent{}, true
	}
	intent := issueMoveProjectIntent{touched: true}
	if rawJSONNull(raw) {
		intent.clear = true
		return intent, true
	}
	projectID, ok := decodeIssueMoveAnchor(w, raw, "project_id")
	if !ok {
		return issueMoveProjectIntent{}, false
	}
	if projectID == nil {
		intent.clear = true
		return intent, true
	}
	intent.project = *projectID
	return intent, true
}

// issueMoveProjectRustAllowed reports whether a project_id intent may use the
// Rust move writer. Clearing the project or leaving it unchanged is allowed;
// setting a project whose row carries a named workflow (non-null workflow_id)
// must stay on Go so status can be remapped like applyIssueWorkflow.
func issueMoveProjectRustAllowed(intent issueMoveProjectIntent, targetNamedWorkflow bool) bool {
	if !intent.touched || intent.clear {
		return true
	}
	return !targetNamedWorkflow
}

func (h *Handler) issueMoveTargetProjectNamedWorkflow(ctx context.Context, workspaceID, projectID pgtype.UUID) (bool, error) {
	if !projectID.Valid {
		return false, nil
	}
	var named bool
	err := h.DB.QueryRow(ctx, `
		SELECT workflow_id IS NOT NULL
		FROM project
		WHERE workspace_id = $1 AND id = $2
	`, workspaceID, projectID).Scan(&named)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return named, err
}

func effectiveMoveProjectID(intent issueMoveProjectIntent, current pgtype.UUID) pgtype.UUID {
	if !intent.touched {
		return current
	}
	if intent.clear {
		return pgtype.UUID{Valid: false}
	}
	return intent.project
}

func decodeIssueMoveParentIntent(w http.ResponseWriter, fields map[string]json.RawMessage) (issueMoveParentIntent, bool) {
	raw, ok := fields["parent_issue_id"]
	if !ok {
		return issueMoveParentIntent{}, true
	}
	intent := issueMoveParentIntent{touched: true}
	if rawJSONNull(raw) {
		intent.clear = true
		return intent, true
	}
	parentID, ok := decodeIssueMoveAnchor(w, raw, "parent_issue_id")
	if !ok {
		return issueMoveParentIntent{}, false
	}
	if parentID == nil {
		intent.clear = true
		return intent, true
	}
	intent.parent = *parentID
	return intent, true
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

func (h *Handler) buildRustIssueRelativeMove(
	r *http.Request,
	parentIntent issueMoveParentIntent,
	projectIntent issueMoveProjectIntent,
	beforeID, afterID *pgtype.UUID,
	expectedRevision *int64,
) rustIssueRelativeMove {
	request := rustIssueRelativeMove{
		SourceTaskID:     uuidToPtr(h.wakeupSourceTaskID(r)),
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
	if parentIntent.touched {
		request.ParentTouched = true
		if parentIntent.clear {
			request.ParentClear = true
		} else {
			id := uuidToString(parentIntent.parent)
			request.ParentIssueID = &id
		}
	}
	if projectIntent.touched {
		request.ProjectTouched = true
		if projectIntent.clear {
			request.ProjectClear = true
		} else {
			id := uuidToString(projectIntent.project)
			request.ProjectID = &id
		}
	}
	return request
}

func moveProjectReceiptMatches(intent issueMoveProjectIntent, before, after db.Issue) bool {
	if !intent.touched {
		return before.ProjectID == after.ProjectID
	}
	if intent.clear {
		return !after.ProjectID.Valid
	}
	return after.ProjectID == intent.project
}

func moveParentReceiptMatches(intent issueMoveParentIntent, before, after db.Issue) bool {
	if !intent.touched {
		return before.ParentIssueID == after.ParentIssueID
	}
	if intent.clear {
		return !after.ParentIssueID.Valid
	}
	return after.ParentIssueID == intent.parent
}

func rustIssueRelativeMoveWriteSnapshotsValid(
	expectedRevision *int64,
	parentIntent issueMoveParentIntent,
	projectIntent issueMoveProjectIntent,
	issueID, workspaceID pgtype.UUID,
	write rustIssueRelativeMoveWrite,
) bool {
	if write.Before.ID != issueID || write.Before.WorkspaceID != workspaceID ||
		write.After.ID != issueID || write.After.WorkspaceID != workspaceID {
		return false
	}
	before := write.Before.issue()
	after := write.After.issue()
	if before.Revision < 1 {
		return false
	}
	if expectedRevision != nil && before.Revision != *expectedRevision {
		return false
	}
	positionChanged := before.Position != after.Position
	parentChanged := before.ParentIssueID != after.ParentIssueID
	projectChanged := before.ProjectID != after.ProjectID
	actualChange := positionChanged || parentChanged || projectChanged
	if write.Changed != actualChange {
		return false
	}
	if write.Changed {
		if after.Revision != before.Revision+1 {
			return false
		}
		if parentChanged && !after.LastActivityAt.Valid {
			return false
		}
	} else if after.Revision != before.Revision {
		return false
	}
	// Guard against corrupt replays: only position, parent and project may differ.
	if before.Title != after.Title || before.Status != after.Status ||
		before.AssigneeType != after.AssigneeType || before.AssigneeID != after.AssigneeID ||
		before.Stage != after.Stage || before.Priority != after.Priority {
		return false
	}
	if !moveParentReceiptMatches(parentIntent, before, after) {
		return false
	}
	if !moveProjectReceiptMatches(projectIntent, before, after) {
		return false
	}
	return true
}

func (h *Handler) moveIssueViaRust(
	w http.ResponseWriter,
	r *http.Request,
	userID string,
	current db.Issue,
	parentIntent issueMoveParentIntent,
	projectIntent issueMoveProjectIntent,
	beforeID, afterID *pgtype.UUID,
	expectedRevision *int64,
) bool {
	if projectIntent.touched {
		effective := effectiveMoveProjectID(projectIntent, current.ProjectID)
		if !h.requireAssigneeCanAccessProject(w, r, current.WorkspaceID, current.AssigneeType, current.AssigneeID, effective) {
			return false
		}
	}
	request := h.buildRustIssueRelativeMove(r, parentIntent, projectIntent, beforeID, afterID, expectedRevision)
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
	if json.Unmarshal(body, &write) != nil ||
		!rustIssueRelativeMoveWriteSnapshotsValid(expectedRevision, parentIntent, projectIntent, current.ID, current.WorkspaceID, write) {
		writeError(w, http.StatusBadGateway, "invalid rust issue move response")
		return false
	}
	h.writeIssueRelativeMove(w, r, userID, projectIntent, write)
	return true
}

func (h *Handler) writeIssueRelativeMove(w http.ResponseWriter, r *http.Request, userID string, projectIntent issueMoveProjectIntent, write rustIssueRelativeMoveWrite) {
	before := write.Before.issue()
	after := write.After.issue()
	workspaceID := uuidToString(after.WorkspaceID)
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	prefix := h.getIssuePrefix(r.Context(), after.WorkspaceID)
	resp := issueToResponse(after, prefix)
	h.fillStatusCategory(r.Context(), after.WorkspaceID, projectaccess.Hidden{}, &resp)
	parentChanged := before.ParentIssueID != after.ParentIssueID
	projectChanged := projectIntent.touched && before.ProjectID != after.ProjectID
	if write.Changed {
		updatedPayload := map[string]any{
			"issue":                      resp,
			"assignee_changed":           false,
			"status_changed":             false,
			"priority_changed":           false,
			"project_changed":            projectChanged,
			"start_date_changed":         false,
			"due_date_changed":           false,
			"description_changed":        false,
			"title_changed":              false,
			"parent_changed":             parentChanged,
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
	if parentChanged && write.Changed {
		h.processChildEvents(r.Context(), after.ParentIssueID, before.ParentIssueID)
	}
	if resp.DuplicateOf != nil {
		resp.DuplicateOf = nil
		if hidden, err := h.hiddenProjects(r, workspaceID); err == nil {
			h.fillStatusCategory(r.Context(), after.WorkspaceID, hidden, &resp)
		}
	}
	writeJSON(w, http.StatusOK, resp)
}
