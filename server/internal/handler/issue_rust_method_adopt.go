package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"reflect"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/workmethod"
)

type rustIssueMethodAdoptWrite struct {
	IssueRevision      int64           `json:"issue_revision"`
	WorkspaceTemplates json.RawMessage `json:"workspace_templates"`
	BeforeRun          json.RawMessage `json:"before_run"`
	AfterRun           json.RawMessage `json:"after_run"`
	WriteRun           bool            `json:"write_run"`
	ExpectedProjectID  *string         `json:"expected_project_id,omitempty"`
}

func rustMethodAdoptResponseLimit(current db.Issue, afterRun []byte, status int) int64 {
	limit := int64(maxRustIssueReadBody)
	if status == http.StatusOK {
		limit += int64(len(current.WorkMethodRun)+len(afterRun)+len(current.AcceptanceCriteria)) * 2
	}
	return limit
}

// runSupportsRustMethodAdopt reports whether the stored run shape can be adopted
// without legacy actor resolution or non-ordinary delivery bindings.
func runSupportsRustMethodAdopt(run workmethod.Run, hasStoredRun bool) bool {
	if !hasStoredRun || run.State != "working" {
		return false
	}
	if run.CurrentActor == nil {
		return false
	}
	if run.ProjectDelivery != nil || run.GoalExecution {
		return false
	}
	if run.Method != nil && run.Method.GitHubDelivery != nil {
		return false
	}
	for _, event := range run.History {
		if event.DeliveryReceipt != nil || event.CandidateFailure != nil || len(event.CoveragePreflight) > 0 {
			return false
		}
	}
	return true
}

func (h *Handler) ordinaryMethodAdoptUsesRust(
	r *http.Request,
	issue db.Issue,
	run workmethod.Run,
	delivery *issuePlanContract,
	hasStoredRun bool,
) (bool, error) {
	if h.IssueReadBridge == nil || len(h.IssueReadBridge.MethodAdoptPilotScopes) == 0 {
		return false, nil
	}
	if delivery != nil || !runSupportsRustMethodAdopt(run, hasStoredRun) {
		return false, nil
	}
	return matchesIssuePilot(r.Context(), h.Queries, h.IssueReadBridge.MethodAdoptPilotScopes, uuidToString(issue.WorkspaceID), issue.WorkspaceID)
}

func (h *Handler) persistMethodAdoptViaRust(
	w http.ResponseWriter,
	r *http.Request,
	issue db.Issue,
	userID string,
	workspaceTemplates, beforeRun, afterRun []byte,
	writeRun bool,
) (updated db.Issue, ok bool) {
	request := rustIssueMethodAdoptWrite{
		IssueRevision:      issue.Revision,
		WorkspaceTemplates: normalizeWorkspaceTemplatesWire(workspaceTemplates),
		BeforeRun:          beforeRun,
		AfterRun:           afterRun,
		WriteRun:           writeRun,
		ExpectedProjectID:  uuidToPtr(issue.ProjectID),
	}
	payload, err := json.Marshal(request)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode method adoption")
		return updated, false
	}
	path := "/internal/issues/" + uuidToString(issue.ID) + "/work-method-adopt"
	resp, routed := h.requestRustIssue(w, r, http.MethodPut, path, uuidToString(issue.WorkspaceID), userID, payload)
	if !routed {
		return updated, false
	}
	defer resp.Body.Close()
	limit := rustMethodAdoptResponseLimit(issue, afterRun, resp.StatusCode)
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || int64(len(body)) > limit {
		if !writeQueryCancellation(w, r, err) {
			writeError(w, http.StatusBadGateway, "failed to read rust method adoption response")
		}
		return updated, false
	}
	if resp.StatusCode != http.StatusOK {
		writeRustIssueResponse(w, resp, bytes.NewReader(body))
		return updated, false
	}
	var reply struct {
		Before ordinaryIssueSnapshot `json:"before"`
		After  ordinaryIssueSnapshot `json:"after"`
	}
	if json.Unmarshal(body, &reply) != nil || !rustMethodAdoptReceiptValid(issue, writeRun, afterRun, reply.Before, reply.After) {
		writeError(w, http.StatusBadGateway, "invalid rust method adoption response")
		return updated, false
	}
	return reply.After.issue(), true
}

func normalizeWorkspaceTemplatesWire(raw []byte) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("null")
	}
	return json.RawMessage(raw)
}

func rustMethodAdoptReceiptValid(
	issue db.Issue,
	writeRun bool,
	expectedRun []byte,
	before, after ordinaryIssueSnapshot,
) bool {
	if before.ID != issue.ID || before.WorkspaceID != issue.WorkspaceID || before.ProjectID != issue.ProjectID ||
		after.ID != issue.ID || after.WorkspaceID != issue.WorkspaceID || after.ProjectID != issue.ProjectID {
		return false
	}
	if before.Revision != issue.Revision {
		return false
	}
	if writeRun {
		if after.Revision != issue.Revision+1 || !after.UpdatedAt.Valid || !after.LastActivityAt.Valid {
			return false
		}
	} else if after.Revision != issue.Revision {
		return false
	}
	b := before.issue()
	a := after.issue()
	if b.ProjectID != issue.ProjectID || a.ProjectID != issue.ProjectID {
		return false
	}
	if b.Status != a.Status || b.AssigneeType != a.AssigneeType || b.AssigneeID != a.AssigneeID ||
		b.Position != a.Position || !bytes.Equal(b.AcceptanceCriteria, a.AcceptanceCriteria) {
		return false
	}
	if len(a.WorkMethodRun) == 0 {
		return false
	}
	var saved, expected workmethod.Run
	if json.Unmarshal(a.WorkMethodRun, &saved) != nil || json.Unmarshal(expectedRun, &expected) != nil {
		return false
	}
	return reflect.DeepEqual(saved, expected)
}
