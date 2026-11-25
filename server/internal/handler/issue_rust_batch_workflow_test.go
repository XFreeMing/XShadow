package handler

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/issueworkflow"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestIssueReadBridgeBatchWorkflowPilotEnv(t *testing.T) {
	t.Setenv("XSHADOW_ISSUES_RUST_BATCH_WORKFLOW_PILOT", "batch-workflow-pilot")
	t.Setenv("XSHADOW_ISSUES_RUST_WORKFLOW_PILOT", "workflow-pilot")
	t.Setenv("XSHADOW_ISSUES_RUST_STATUS_PILOT", "status-pilot")
	t.Setenv("XSHADOW_TABLES_UPSTREAM", "http://127.0.0.1:18080")
	t.Setenv("XSHADOW_TABLES_INTERNAL_TOKEN", "shared-secret")

	bridge := NewIssueReadBridgeFromEnv(nil)
	if !bridge.Enabled() {
		t.Fatal("expected bridge enabled with loopback upstream")
	}
	if !reflect.DeepEqual(bridge.BatchWorkflowPilotScopes, []string{"batch-workflow-pilot"}) {
		t.Fatalf("BatchWorkflowPilotScopes = %v, want [batch-workflow-pilot]", bridge.BatchWorkflowPilotScopes)
	}
}

func TestBatchWorkflowSquadHandoffPilotActiveRequiresAllPilots(t *testing.T) {
	h := &Handler{
		IssueReadBridge: &IssueReadBridge{
			StatusPilotScopes:        []string{"*"},
			WorkflowPilotScopes:      []string{"*"},
			BatchWorkflowPilotScopes: []string{"*"},
		},
	}
	ws := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	status := "in_progress"
	r := newRequest(http.MethodPost, "/api/issues/batch-update", map[string]any{
		"issue_ids": []string{"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"},
		"updates":   map[string]any{"status": status},
	})
	raw := map[string]json.RawMessage{"status": json.RawMessage(`"in_progress"`)}
	useRust, err := h.batchWorkflowSquadHandoffPilotActive(r, ws, raw, UpdateIssueRequest{Status: &status})
	if err != nil || !useRust {
		t.Fatalf("useRust=%v err=%v, want true with all pilots", useRust, err)
	}

	h.IssueReadBridge.BatchWorkflowPilotScopes = nil
	useRust, err = h.batchWorkflowSquadHandoffPilotActive(r, ws, raw, UpdateIssueRequest{Status: &status})
	if err != nil || useRust {
		t.Fatalf("useRust=%v err=%v, want false without batch-workflow pilot", useRust, err)
	}
}

func TestBatchWorkflowSquadHandoffPilotActiveRejectsStopPreviousRuns(t *testing.T) {
	h := &Handler{
		IssueReadBridge: &IssueReadBridge{
			StatusPilotScopes:        []string{"*"},
			WorkflowPilotScopes:      []string{"*"},
			BatchWorkflowPilotScopes: []string{"*"},
		},
	}
	ws := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	status := "in_progress"
	raw := map[string]json.RawMessage{
		"status":                      json.RawMessage(`"in_progress"`),
		"stop_previous_assignee_runs": json.RawMessage(`true`),
	}
	r := newRequest(http.MethodPost, "/api/issues/batch-update", nil)
	useRust, err := h.batchWorkflowSquadHandoffPilotActive(
		r, ws, raw, UpdateIssueRequest{Status: &status, StopPreviousAssigneeRuns: true},
	)
	if err != nil || useRust {
		t.Fatalf("useRust=%v err=%v, want false for stop_previous_assignee_runs", useRust, err)
	}
}

func TestBatchWorkflowSquadHandoffIssueUsesRustRejectsDefaultWorkflow(t *testing.T) {
	handoff := &workflowHandoff{
		def: issueworkflow.Definition{IsDefault: true},
		step: issueworkflow.Step{
			Handler: issueworkflow.Handler{Type: issueworkflow.HandlerSquad},
		},
	}
	ws := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	prev := db.Issue{Status: issuestatus.Todo, WorkspaceID: ws, ProjectID: pgtype.UUID{Bytes: [16]byte{2}, Valid: true}}
	if batchWorkflowSquadHandoffIssueEligible(prev, issuestatus.InProgress, handoff, false, nil) {
		t.Fatal("expected Default workflow handoff to stay on Go in batch")
	}
}

func TestParseRustWorkflowSquadConflictRecognizesBatchSignals(t *testing.T) {
	_, ok := parseRustWorkflowSquadConflict([]byte(`{"batch_rollback":true}`))
	if !ok {
		t.Fatal("expected batch_rollback conflict")
	}
	_, ok = parseRustWorkflowSquadConflict([]byte(`{"dispatch_sensitive":true}`))
	if !ok {
		t.Fatal("expected dispatch_sensitive conflict")
	}
}

func TestParseRustWorkflowSquadConflictRejectsMixedMarkers(t *testing.T) {
	if _, ok := parseRustWorkflowSquadConflict([]byte(`{"batch_rollback":true,"actual_revision":2}`)); ok {
		t.Fatal("expected mixed batch_rollback and revision to be unrecognized")
	}
	if _, ok := parseRustWorkflowSquadConflict([]byte(`{"batch_rollback":"true"}`)); ok {
		t.Fatal("expected non-boolean batch_rollback to be unrecognized")
	}
}

func TestBatchWorkflowSquadHandoffResponseAbortUnmarked500(t *testing.T) {
	if classifyBatchWorkflowSquadHandoffHTTP(http.StatusInternalServerError, []byte(`{"error":"injected"}`)) != batchWorkflowRustAbort {
		t.Fatal("expected unmarked 500 to abort the batch")
	}
	if classifyBatchWorkflowSquadHandoffHTTP(http.StatusConflict, []byte(`{"detail":"garbage"}`)) != batchWorkflowRustAbort {
		t.Fatal("expected unrecognized conflict to abort the batch")
	}
	if classifyBatchWorkflowSquadHandoffHTTP(http.StatusConflict, []byte(`{"batch_rollback":true}`)) != batchWorkflowRustSkipRow {
		t.Fatal("expected typed batch_rollback to skip the row only")
	}
}
