package handler

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestIssueReadBridgeBatchPilotEnv(t *testing.T) {
	t.Setenv("XSHADOW_ISSUES_RUST_BATCH_PILOT", "batch-pilot")
	t.Setenv("XSHADOW_ISSUES_RUST_WRITE_PILOT", "")
	t.Setenv("XSHADOW_TABLES_UPSTREAM", "http://127.0.0.1:18080")
	t.Setenv("XSHADOW_TABLES_INTERNAL_TOKEN", "shared-secret")

	bridge := NewIssueReadBridgeFromEnv(nil)
	if !bridge.Enabled() {
		t.Fatal("expected bridge enabled with loopback upstream")
	}
	if !reflect.DeepEqual(bridge.BatchPilotScopes, []string{"batch-pilot"}) {
		t.Fatalf("BatchPilotScopes = %v, want [batch-pilot]", bridge.BatchPilotScopes)
	}
	if len(bridge.WritePilotScopes) != 0 {
		t.Fatalf("WritePilotScopes = %v, want empty", bridge.WritePilotScopes)
	}
}

func TestBatchOrdinaryIssueEditUsesRustRejectsWritePilotOnly(t *testing.T) {
	h := &Handler{IssueReadBridge: &IssueReadBridge{WritePilotScopes: []string{"*"}}}
	ws := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	r := newRequest(http.MethodPost, "/api/issues/batch-update", map[string]any{
		"issue_ids": []string{"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"},
		"updates":   map[string]any{"priority": "high"},
	})
	useRust, err := h.batchOrdinaryIssueEditUsesRust(r, ws, map[string]json.RawMessage{
		"priority": json.RawMessage(`"high"`),
	})
	if err != nil || useRust {
		t.Fatalf("useRust=%v err=%v, want false without batch pilot", useRust, err)
	}
}

func TestBatchOrdinaryIssueEditUsesRustRejectsNonScalarFields(t *testing.T) {
	h := &Handler{IssueReadBridge: &IssueReadBridge{BatchPilotScopes: []string{"*"}}}
	ws := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	r := newRequest(http.MethodPost, "/api/issues/batch-update", map[string]any{
		"issue_ids": []string{"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"},
		"updates":   map[string]any{"priority": "high", "status": "done"},
	})
	useRust, err := h.batchOrdinaryIssueEditUsesRust(r, ws, map[string]json.RawMessage{
		"priority": json.RawMessage(`"high"`),
		"status":   json.RawMessage(`"done"`),
	})
	if err != nil || useRust {
		t.Fatalf("useRust=%v err=%v, want false", useRust, err)
	}
}

func TestBatchOrdinaryIssueEditUsesRustAcceptsScalarBatch(t *testing.T) {
	h := &Handler{IssueReadBridge: &IssueReadBridge{BatchPilotScopes: []string{"*"}}}
	ws := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	r := newRequest(http.MethodPost, "/api/issues/batch-update", map[string]any{
		"issue_ids": []string{"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"},
		"updates":   map[string]any{"priority": "high", "position": 1.5},
	})
	raw := map[string]json.RawMessage{
		"priority": json.RawMessage(`"high"`),
		"position": json.RawMessage(`1.5`),
	}
	useRust, err := h.batchOrdinaryIssueEditUsesRust(r, ws, raw)
	if err != nil || !useRust {
		t.Fatalf("useRust=%v err=%v, want true", useRust, err)
	}
}

func TestBuildBatchOrdinaryIssueEditRejectsInvalidCalendarDate(t *testing.T) {
	raw := map[string]json.RawMessage{"due_date": json.RawMessage(`"not-a-date"`)}
	badDay := "not-a-date"
	_, ok := testHandler.buildBatchOrdinaryIssueEdit(UpdateIssueRequest{DueDate: &badDay}, raw, pgtype.UUID{})
	if ok {
		t.Fatal("expected invalid calendar date to be rejected")
	}
}

func TestBuildBatchOrdinaryIssueEditClearsDates(t *testing.T) {
	raw := map[string]json.RawMessage{
		"start_date": json.RawMessage(`null`),
		"due_date":   json.RawMessage(`null`),
	}
	edit, ok := testHandler.buildBatchOrdinaryIssueEdit(UpdateIssueRequest{}, raw, pgtype.UUID{})
	if !ok || edit.StartDate == nil || edit.DueDate == nil {
		t.Fatalf("edit=%+v ok=%v, want cleared dates", edit, ok)
	}
}

func TestParseOrdinaryIssueRustSnapshotsRejectsWrongTenant(t *testing.T) {
	issueID := pgtype.UUID{Bytes: [16]byte{3}, Valid: true}
	workspaceID := pgtype.UUID{Bytes: [16]byte{4}, Valid: true}
	otherWS := pgtype.UUID{Bytes: [16]byte{5}, Valid: true}
	body, err := json.Marshal(map[string]any{
		"before": map[string]any{"id": uuidToString(issueID), "workspace_id": uuidToString(otherWS)},
		"after":  map[string]any{"id": uuidToString(issueID), "workspace_id": uuidToString(workspaceID)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := decodeOrdinaryIssueRustSnapshots(issueID, workspaceID, body); ok {
		t.Fatal("expected mismatched workspace to be rejected")
	}
}
