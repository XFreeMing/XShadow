package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type ordinaryBatchParentTrust struct {
	ExpectedProjectID *string `json:"expected_project_id,omitempty"`
}

type issueParentEdit struct {
	ExpectedRevision *int64                    `json:"expected_revision,omitempty"`
	ParentClear      bool                      `json:"parent_clear"`
	ParentIssueID    *string                   `json:"parent_issue_id,omitempty"`
	SourceTaskID     *string                   `json:"source_task_id,omitempty"`
	OrdinaryBatch    *ordinaryBatchParentTrust `json:"ordinary_batch,omitempty"`
}

type rustParentWriteResult struct {
	before, after db.Issue
	changed       bool
}

// parentIssueEditUsesRust selects parent-only grid edits on a write-pilot
// workspace. Mixed payloads stay on Go.
func (h *Handler) parentIssueEditUsesRust(
	r *http.Request,
	workspaceID pgtype.UUID,
	rawFields map[string]json.RawMessage,
	handoff *workflowHandoff,
) (bool, error) {
	if h.IssueReadBridge == nil || len(h.IssueReadBridge.WritePilotScopes) == 0 {
		return false, nil
	}
	if handoff != nil {
		return false, nil
	}
	_, touchedParent := rawFields["parent_issue_id"]
	if !touchedParent {
		return false, nil
	}
	for key := range rawFields {
		switch key {
		case "parent_issue_id", "expected_revision", "suppress_run":
		default:
			return false, nil
		}
	}
	return matchesIssuePilot(r.Context(), h.Queries, h.IssueReadBridge.WritePilotScopes, uuidToString(workspaceID), workspaceID)
}

func (h *Handler) buildIssueParentEdit(
	r *http.Request,
	expectedRevision *int64,
	params db.UpdateIssueParams,
	rawFields map[string]json.RawMessage,
	batch bool,
	expectedProjectID pgtype.UUID,
) (issueParentEdit, bool) {
	edit := issueParentEdit{
		ExpectedRevision: expectedRevision,
		SourceTaskID:     uuidToPtr(h.wakeupSourceTaskID(r)),
	}
	if batch {
		edit.OrdinaryBatch = &ordinaryBatchParentTrust{
			ExpectedProjectID: uuidToPtr(expectedProjectID),
		}
	}
	if rawParent, ok := rawFields["parent_issue_id"]; ok && rawJSONNull(rawParent) {
		edit.ParentClear = true
		return edit, true
	}
	if params.ParentIssueID.Valid {
		id := uuidToString(params.ParentIssueID)
		edit.ParentIssueID = &id
		return edit, true
	}
	return edit, false
}

func rustParentWriteSnapshotsValid(
	expectedRevision *int64,
	targetParent pgtype.UUID,
	before, after ordinaryIssueSnapshot,
) bool {
	b := before.issue()
	a := after.issue()
	if b.Revision < 1 || (expectedRevision != nil && b.Revision != *expectedRevision) {
		return false
	}
	parentChanged := b.ParentIssueID != a.ParentIssueID
	if parentChanged {
		if a.Revision != b.Revision+1 {
			return false
		}
		if !a.LastActivityAt.Valid {
			return false
		}
	} else if a.Revision != b.Revision {
		return false
	}
	if a.ParentIssueID != targetParent {
		return false
	}
	if b.Title != a.Title || b.Status != a.Status || b.Position != a.Position ||
		b.AssigneeType != a.AssigneeType || b.AssigneeID != a.AssigneeID ||
		b.ProjectID != a.ProjectID || b.Stage != a.Stage || b.Priority != a.Priority {
		return false
	}
	return true
}

func (h *Handler) validateRustParentWriteReceipt(
	current db.Issue,
	expectedRevision *int64,
	targetParent pgtype.UUID,
	body []byte,
) (rustParentWriteResult, error) {
	var snapshots struct {
		Before  ordinaryIssueSnapshot `json:"before"`
		After   ordinaryIssueSnapshot `json:"after"`
		Changed bool                  `json:"changed"`
	}
	if json.Unmarshal(body, &snapshots) != nil ||
		snapshots.Before.ID != current.ID || snapshots.Before.WorkspaceID != current.WorkspaceID ||
		snapshots.After.ID != current.ID || snapshots.After.WorkspaceID != current.WorkspaceID ||
		!rustParentWriteSnapshotsValid(expectedRevision, targetParent, snapshots.Before, snapshots.After) {
		return rustParentWriteResult{}, errors.New("invalid rust issue parent snapshots")
	}
	beforeIssue := snapshots.Before.issue()
	afterIssue := snapshots.After.issue()
	parentChanged := beforeIssue.ParentIssueID != afterIssue.ParentIssueID
	if snapshots.Changed != parentChanged {
		return rustParentWriteResult{}, errors.New("invalid rust issue parent snapshots")
	}
	return rustParentWriteResult{
		before:  beforeIssue,
		after:   afterIssue,
		changed: snapshots.Changed,
	}, nil
}

func (h *Handler) updateIssueParentViaRust(
	w http.ResponseWriter,
	r *http.Request,
	userID string,
	current db.Issue,
	params db.UpdateIssueParams,
	req UpdateIssueRequest,
	rawFields map[string]json.RawMessage,
) (after, before db.Issue, ok bool) {
	edit, editOK := h.buildIssueParentEdit(r, req.ExpectedRevision, params, rawFields, false, pgtype.UUID{})
	if !editOK {
		writeError(w, http.StatusBadRequest, "invalid parent issue edit")
		return after, before, false
	}
	payload, err := json.Marshal(edit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode issue parent edit")
		return after, before, false
	}
	path := "/internal/issues/" + uuidToString(current.ID) + "/parent"
	resp, requested := h.requestRustIssue(w, r, http.MethodPut, path, uuidToString(current.WorkspaceID), userID, payload)
	if !requested {
		return after, before, false
	}
	defer resp.Body.Close()
	limit := int64(maxRustIssueReadBody) * 2
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || int64(len(body)) > limit {
		if !writeQueryCancellation(w, r, err) {
			if err != nil {
				writeError(w, http.StatusServiceUnavailable, "rust issue write service unavailable")
			} else {
				writeError(w, http.StatusBadGateway, "failed to read rust issue parent response")
			}
		}
		return after, before, false
	}
	if resp.StatusCode == http.StatusConflict {
		var conflict struct {
			ActualRevision int64 `json:"actual_revision"`
		}
		if req.ExpectedRevision != nil && json.Unmarshal(body, &conflict) == nil && conflict.ActualRevision > 0 {
			writeRevisionConflict(w, "issue", current.ID, *req.ExpectedRevision, conflict.ActualRevision)
			return after, before, false
		}
		writeError(w, http.StatusBadGateway, "invalid rust issue parent revision conflict")
		return after, before, false
	}
	if resp.StatusCode != http.StatusOK {
		writeRustIssueResponse(w, resp, bytes.NewReader(body))
		return after, before, false
	}
	result, receiptErr := h.validateRustParentWriteReceipt(current, req.ExpectedRevision, params.ParentIssueID, body)
	if receiptErr != nil {
		writeError(w, http.StatusBadGateway, receiptErr.Error())
		return after, before, false
	}
	return result.after, result.before, true
}
