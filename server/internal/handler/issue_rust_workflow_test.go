package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/issueworkflow"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestStatusWorkflowSquadHandoffUsesRustRequiresWorkflowPilot(t *testing.T) {
	t.Parallel()
	h := &Handler{
		IssueReadBridge: &IssueReadBridge{
			StatusPilotScopes:   []string{"*"},
			WorkflowPilotScopes: []string{"*"},
		},
	}
	rev := int64(2)
	issue := db.Issue{Status: issuestatus.Todo}
	raw := map[string]json.RawMessage{
		"status":            mustRawJSON(t, issuestatus.InProgress),
		"expected_revision": mustRawJSON(t, rev),
	}
	req := httptest.NewRequest(http.MethodPut, "/api/issues/1", nil)
	handoff := &workflowHandoff{
		def: issueworkflow.Definition{Name: "Flow"},
		step: issueworkflow.Step{
			StatusKey: issuestatus.InProgress,
			Handler:   issueworkflow.Handler{Type: issueworkflow.HandlerSquad, ID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"},
		},
	}
	ok, err := h.statusWorkflowSquadHandoffUsesRust(
		req, issue.WorkspaceID, issue, raw,
		UpdateIssueRequest{ExpectedRevision: &rev, Status: strPtr(issuestatus.InProgress)},
		handoff, issuestatus.InProgress, nil,
	)
	if err != nil || !ok {
		t.Fatalf("squad workflow handoff uses rust = %v err=%v, want true", ok, err)
	}

	withStop := mustRawJSON(t, true)
	mixed := map[string]json.RawMessage{
		"status":                      mustRawJSON(t, issuestatus.InProgress),
		"expected_revision":           mustRawJSON(t, rev),
		"stop_previous_assignee_runs": withStop,
	}
	ok, err = h.statusWorkflowSquadHandoffUsesRust(
		req, issue.WorkspaceID, issue, mixed,
		UpdateIssueRequest{ExpectedRevision: &rev, Status: strPtr(issuestatus.InProgress), StopPreviousAssigneeRuns: true},
		handoff, issuestatus.InProgress, nil,
	)
	if err != nil || ok {
		t.Fatalf("mixed stop_previous_assignee_runs uses rust = %v err=%v, want false", ok, err)
	}

	agentHandoff := &workflowHandoff{
		step: issueworkflow.Step{Handler: issueworkflow.Handler{Type: issueworkflow.HandlerAgent}},
	}
	ok, err = h.statusWorkflowSquadHandoffUsesRust(
		req, issue.WorkspaceID, issue, raw,
		UpdateIssueRequest{ExpectedRevision: &rev, Status: strPtr(issuestatus.InProgress)},
		agentHandoff, issuestatus.InProgress, nil,
	)
	if err != nil || ok {
		t.Fatalf("agent workflow handoff uses rust = %v err=%v, want false", ok, err)
	}
}

func mustRawJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
