package handler

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/issuestatus"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestIssueReadBridgeBatchAssignPilotEnv(t *testing.T) {
	t.Setenv("XSHADOW_ISSUES_RUST_BATCH_ASSIGN_PILOT", "batch-assign-pilot")
	t.Setenv("XSHADOW_ISSUES_RUST_ASSIGN_PILOT", "assign-pilot")
	t.Setenv("XSHADOW_TABLES_UPSTREAM", "http://127.0.0.1:18080")
	t.Setenv("XSHADOW_TABLES_INTERNAL_TOKEN", "shared-secret")

	bridge := NewIssueReadBridgeFromEnv(nil)
	if !bridge.Enabled() {
		t.Fatal("expected bridge enabled with loopback upstream")
	}
	if !reflect.DeepEqual(bridge.BatchAssignPilotScopes, []string{"batch-assign-pilot"}) {
		t.Fatalf("BatchAssignPilotScopes = %v, want [batch-assign-pilot]", bridge.BatchAssignPilotScopes)
	}
}

func TestBatchOwnedDirectAgentAssignPilotActiveRequiresBothPilots(t *testing.T) {
	h := &Handler{IssueReadBridge: &IssueReadBridge{AssignPilotScopes: []string{"*"}}}
	ws := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	r := newRequest(http.MethodPost, "/api/issues/batch-update", map[string]any{
		"issue_ids": []string{"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"},
		"updates": map[string]any{
			"assignee_type": "agent",
			"assignee_id":   "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		},
	})
	raw := map[string]json.RawMessage{
		"assignee_type": json.RawMessage(`"agent"`),
		"assignee_id":   json.RawMessage(`"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"`),
	}
	agentType := "agent"
	agentID := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	useRust, err := h.batchOwnedDirectAgentAssignPilotActive(r, ws, raw, UpdateIssueRequest{
		AssigneeType: &agentType,
		AssigneeID:   &agentID,
	})
	if err != nil || useRust {
		t.Fatalf("useRust=%v err=%v, want false without batch-assign pilot", useRust, err)
	}

	h.IssueReadBridge.BatchAssignPilotScopes = []string{"*"}
	useRust, err = h.batchOwnedDirectAgentAssignPilotActive(r, ws, raw, UpdateIssueRequest{
		AssigneeType: &agentType,
		AssigneeID:   &agentID,
	})
	if err != nil || !useRust {
		t.Fatalf("useRust=%v err=%v, want true with both pilots", useRust, err)
	}
}

func TestBatchOwnedDirectAgentAssignPilotActiveRejectsMixedBatchFields(t *testing.T) {
	h := &Handler{IssueReadBridge: &IssueReadBridge{AssignPilotScopes: []string{"*"}, BatchAssignPilotScopes: []string{"*"}}}
	ws := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	agentType := "agent"
	agentID := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	raw := map[string]json.RawMessage{
		"assignee_type": json.RawMessage(`"agent"`),
		"assignee_id":   json.RawMessage(`"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"`),
		"priority":      json.RawMessage(`"high"`),
	}
	r := newRequest(http.MethodPost, "/api/issues/batch-update", map[string]any{
		"issue_ids": []string{"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"},
		"updates":   map[string]any{"assignee_type": "agent", "assignee_id": agentID, "priority": "high"},
	})
	priority := "high"
	useRust, err := h.batchOwnedDirectAgentAssignPilotActive(r, ws, raw, UpdateIssueRequest{
		AssigneeType: &agentType,
		AssigneeID:   &agentID,
		Priority:     &priority,
	})
	if err != nil || useRust {
		t.Fatalf("useRust=%v err=%v, want false for mixed batch", useRust, err)
	}
}

func TestBatchOwnedDirectAgentAssignPilotActiveRejectsScalarBatchPilotOnly(t *testing.T) {
	h := &Handler{IssueReadBridge: &IssueReadBridge{BatchPilotScopes: []string{"*"}, AssignPilotScopes: []string{"*"}}}
	ws := pgtype.UUID{Bytes: [16]byte{3}, Valid: true}
	agentType := "agent"
	agentID := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	raw := map[string]json.RawMessage{
		"assignee_type": json.RawMessage(`"agent"`),
		"assignee_id":   json.RawMessage(`"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"`),
	}
	r := newRequest(http.MethodPost, "/api/issues/batch-update", nil)
	useRust, err := h.batchOwnedDirectAgentAssignPilotActive(r, ws, raw, UpdateIssueRequest{AssigneeType: &agentType, AssigneeID: &agentID})
	if err != nil || useRust {
		t.Fatalf("useRust=%v err=%v, want false without batch-assign pilot", useRust, err)
	}
}

func TestBatchOwnedDirectAgentAssignPilotActiveRejectsSquadTargets(t *testing.T) {
	h := &Handler{IssueReadBridge: &IssueReadBridge{AssignPilotScopes: []string{"*"}, BatchAssignPilotScopes: []string{"*"}}}
	ws := pgtype.UUID{Bytes: [16]byte{4}, Valid: true}
	squadType := "squad"
	squadID := "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	raw := map[string]json.RawMessage{
		"assignee_type": json.RawMessage(`"squad"`),
		"assignee_id":   json.RawMessage(`"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"`),
	}
	r := newRequest(http.MethodPost, "/api/issues/batch-update", nil)
	useRust, err := h.batchOwnedDirectAgentAssignPilotActive(r, ws, raw, UpdateIssueRequest{AssigneeType: &squadType, AssigneeID: &squadID})
	if err != nil || useRust {
		t.Fatalf("useRust=%v err=%v, want false for squad target", useRust, err)
	}
}

func TestBatchAssignRowEligibleForRustRejectsTriageAndWorkflowSources(t *testing.T) {
	agent := pgtype.UUID{Bytes: [16]byte{5}, Valid: true}
	params := pgtype.Text{String: "agent", Valid: true}
	if batchAssignRowEligibleForRust(db.Issue{Status: issuestatus.Todo, TriageState: pgtype.Text{String: "open", Valid: true}}, params, agent) {
		t.Fatal("expected triage issue to be ineligible")
	}
	if !batchAssignRowEligibleForRust(db.Issue{Status: issuestatus.Todo}, params, agent) {
		t.Fatal("expected ordinary todo issue to be eligible")
	}
}
