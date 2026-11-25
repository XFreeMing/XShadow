package handler

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestIssueReadBridgeBatchStatusPilotEnv(t *testing.T) {
	t.Setenv("XSHADOW_ISSUES_RUST_BATCH_STATUS_PILOT", "batch-status-pilot")
	t.Setenv("XSHADOW_ISSUES_RUST_STATUS_PILOT", "status-pilot")
	t.Setenv("XSHADOW_TABLES_UPSTREAM", "http://127.0.0.1:18080")
	t.Setenv("XSHADOW_TABLES_INTERNAL_TOKEN", "shared-secret")

	bridge := NewIssueReadBridgeFromEnv(nil)
	if !bridge.Enabled() {
		t.Fatal("expected bridge enabled with loopback upstream")
	}
	if !reflect.DeepEqual(bridge.BatchStatusPilotScopes, []string{"batch-status-pilot"}) {
		t.Fatalf("BatchStatusPilotScopes = %v, want [batch-status-pilot]", bridge.BatchStatusPilotScopes)
	}
}

func TestBatchOrdinaryActiveStatusPilotActiveRequiresBothPilots(t *testing.T) {
	h := &Handler{IssueReadBridge: &IssueReadBridge{StatusPilotScopes: []string{"*"}}}
	ws := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	status := "in_review"
	r := newRequest(http.MethodPost, "/api/issues/batch-update", map[string]any{
		"issue_ids": []string{"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"},
		"updates":   map[string]any{"status": status},
	})
	raw := map[string]json.RawMessage{"status": json.RawMessage(`"in_review"`)}
	useRust, err := h.batchOrdinaryActiveStatusPilotActive(r, ws, raw, UpdateIssueRequest{Status: &status}, status)
	if err != nil || useRust {
		t.Fatalf("useRust=%v err=%v, want false without batch-status pilot", useRust, err)
	}

	h.IssueReadBridge.BatchStatusPilotScopes = []string{"*"}
	useRust, err = h.batchOrdinaryActiveStatusPilotActive(r, ws, raw, UpdateIssueRequest{Status: &status}, status)
	if err != nil || !useRust {
		t.Fatalf("useRust=%v err=%v, want true with both pilots", useRust, err)
	}
}

func TestBatchOrdinaryActiveStatusPilotActiveRejectsMixedBatchFields(t *testing.T) {
	h := &Handler{IssueReadBridge: &IssueReadBridge{StatusPilotScopes: []string{"*"}, BatchStatusPilotScopes: []string{"*"}}}
	ws := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	status := "todo"
	priority := "high"
	raw := map[string]json.RawMessage{
		"status":   json.RawMessage(`"todo"`),
		"priority": json.RawMessage(`"high"`),
	}
	r := newRequest(http.MethodPost, "/api/issues/batch-update", nil)
	useRust, err := h.batchOrdinaryActiveStatusPilotActive(r, ws, raw, UpdateIssueRequest{Status: &status, Priority: &priority}, status)
	if err != nil || useRust {
		t.Fatalf("useRust=%v err=%v, want false for mixed batch", useRust, err)
	}
}

func TestParseRustIssueStatusConflictRecognizesBatchSignals(t *testing.T) {
	_, ok := parseRustIssueStatusConflict([]byte(`{"batch_rollback":true}`))
	if !ok {
		t.Fatal("expected batch_rollback conflict")
	}
	_, ok = parseRustIssueStatusConflict([]byte(`{"actual_revision":3}`))
	if !ok {
		t.Fatal("expected revision conflict")
	}
	if _, ok := parseRustIssueStatusConflict([]byte(`{"detail":"garbage"}`)); ok {
		t.Fatal("expected malformed conflict to be unrecognized")
	}
}

func TestRustIssueStatusReceiptHasBatchScopeExclusives(t *testing.T) {
	issueID, err := util.ParseUUID("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	wsID, err := util.ParseUUID("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
	if err != nil {
		t.Fatal(err)
	}
	receipt, ok := decodeRustIssueStatusWriteReceipt([]byte(`{
		"before":{"id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","workspace_id":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","status":"todo","revision":1,"position":0,"priority":"none","title":"t"},
		"after":{"id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","workspace_id":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","status":"in_review","revision":2,"position":0,"priority":"none","title":"t"},
		"coalesced_queued_task_id":"cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	}`), issueID, wsID)
	if !ok {
		t.Fatal("decode failed")
	}
	if !receipt.hasQueue() {
		t.Fatal("expected queue receipt to be rejected in batch scope")
	}
}

func TestRustIssueStatusTrustedProjectMatch(t *testing.T) {
	project := pgtype.UUID{Bytes: [16]byte{0x01}, Valid: true}
	before := ordinaryIssueSnapshot{Issue: db.Issue{ProjectID: project}}
	after := ordinaryIssueSnapshot{Issue: db.Issue{ProjectID: project}}
	if !rustIssueStatusTrustedProjectMatch(before, after, project) {
		t.Fatal("expected matching projects")
	}
	after.ProjectID = pgtype.UUID{Bytes: [16]byte{0x02}, Valid: true}
	if rustIssueStatusTrustedProjectMatch(before, after, project) {
		t.Fatal("expected after project mismatch")
	}
}

func TestBatchOrdinaryActiveStatusPilotActiveRejectsTerminalTargets(t *testing.T) {
	h := &Handler{IssueReadBridge: &IssueReadBridge{StatusPilotScopes: []string{"*"}, BatchStatusPilotScopes: []string{"*"}}}
	ws := pgtype.UUID{Bytes: [16]byte{3}, Valid: true}
	done := issuestatus.Done
	raw := map[string]json.RawMessage{"status": json.RawMessage(`"done"`)}
	r := newRequest(http.MethodPost, "/api/issues/batch-update", nil)
	useRust, err := h.batchOrdinaryActiveStatusPilotActive(r, ws, raw, UpdateIssueRequest{Status: &done}, done)
	if err != nil || useRust {
		t.Fatalf("useRust=%v err=%v, want false for terminal target", useRust, err)
	}
}

func TestBatchStatusRowEligibleForRustRejectsTriageAndWorkMethod(t *testing.T) {
	if batchStatusRowEligibleForRust(db.Issue{
		Status:      issuestatus.Todo,
		TriageState: pgtype.Text{String: "open", Valid: true},
	}) {
		t.Fatal("expected triage issue to be ineligible")
	}
	if batchStatusRowEligibleForRust(db.Issue{
		Status:        issuestatus.Todo,
		WorkMethodRun: []byte(`{"state":"ready"}`),
	}) {
		t.Fatal("expected work-method issue to be ineligible")
	}
	if !batchStatusRowEligibleForRust(db.Issue{Status: issuestatus.Todo}) {
		t.Fatal("expected ordinary todo issue to be eligible")
	}
}

func TestBatchStatusIssueUsesRustRejectsDispatchSensitivePromotion(t *testing.T) {
	h := &Handler{}
	agent := pgtype.UUID{Bytes: [16]byte{9}, Valid: true}
	prev := db.Issue{
		Status:       issuestatus.Backlog,
		AssigneeType: pgtype.Text{String: "agent", Valid: true},
		AssigneeID:   agent,
	}
	r := newRequest(http.MethodPost, "/api/issues/batch-update", nil)
	if h.batchStatusIssueUsesRust(r.Context(), prev, issuestatus.Todo, nil, false) {
		t.Fatal("expected dispatch-sensitive backlog promotion without suppress_run to stay on Go")
	}
	if !h.batchStatusIssueUsesRust(r.Context(), prev, issuestatus.Todo, nil, true) {
		t.Fatal("expected suppressed backlog promotion to use Rust")
	}
}
