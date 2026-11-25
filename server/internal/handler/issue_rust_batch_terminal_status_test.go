package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/issuestatus"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestIssueReadBridgeBatchTerminalStatusPilotEnv(t *testing.T) {
	t.Setenv("XSHADOW_ISSUES_RUST_BATCH_TERMINAL_STATUS_PILOT", "batch-terminal-pilot")
	t.Setenv("XSHADOW_ISSUES_RUST_TERMINAL_STATUS_PILOT", "terminal-pilot")
	t.Setenv("XSHADOW_ISSUES_RUST_STATUS_PILOT", "status-pilot")
	t.Setenv("XSHADOW_TABLES_UPSTREAM", "http://127.0.0.1:18080")
	t.Setenv("XSHADOW_TABLES_INTERNAL_TOKEN", "shared-secret")

	bridge := NewIssueReadBridgeFromEnv(nil)
	if !reflect.DeepEqual(bridge.BatchTerminalPilotScopes, []string{"batch-terminal-pilot"}) {
		t.Fatalf("BatchTerminalPilotScopes = %v, want [batch-terminal-pilot]", bridge.BatchTerminalPilotScopes)
	}
}

func TestBatchOrdinaryTerminalStatusPilotActiveRequiresAllPilots(t *testing.T) {
	h := &Handler{
		IssueReadBridge: &IssueReadBridge{
			StatusPilotScopes:         []string{"*"},
			TerminalStatusPilotScopes: []string{"*"},
		},
	}
	ws := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	done := issuestatus.Done
	raw := map[string]json.RawMessage{"status": json.RawMessage(`"done"`)}
	r := newRequest(http.MethodPost, "/api/issues/batch-update", nil)
	useRust, err := h.batchOrdinaryTerminalStatusPilotActive(r, ws, raw, UpdateIssueRequest{Status: &done}, done)
	if err != nil || useRust {
		t.Fatalf("useRust=%v err=%v, want false without batch-terminal pilot", useRust, err)
	}

	h.IssueReadBridge.BatchTerminalPilotScopes = []string{"*"}
	useRust, err = h.batchOrdinaryTerminalStatusPilotActive(r, ws, raw, UpdateIssueRequest{Status: &done}, done)
	if err != nil || !useRust {
		t.Fatalf("useRust=%v err=%v, want true with all pilots", useRust, err)
	}
}

func TestBatchOrdinaryTerminalStatusPilotActiveRejectsActiveTargets(t *testing.T) {
	h := &Handler{IssueReadBridge: &IssueReadBridge{
		StatusPilotScopes:         []string{"*"},
		TerminalStatusPilotScopes: []string{"*"},
		BatchTerminalPilotScopes:  []string{"*"},
	}}
	ws := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	status := issuestatus.Todo
	raw := map[string]json.RawMessage{"status": json.RawMessage(`"todo"`)}
	r := newRequest(http.MethodPost, "/api/issues/batch-update", nil)
	useRust, err := h.batchOrdinaryTerminalStatusPilotActive(r, ws, raw, UpdateIssueRequest{Status: &status}, status)
	if err != nil || useRust {
		t.Fatalf("useRust=%v err=%v, want false for active target", useRust, err)
	}
}

func TestBatchTerminalStatusRowEligibleForRust(t *testing.T) {
	if !batchTerminalStatusRowEligibleForRust(db.Issue{Status: issuestatus.Todo}, issuestatus.Done) {
		t.Fatal("expected ordinary todo row to be eligible for done")
	}
	if batchTerminalStatusRowEligibleForRust(db.Issue{
		Status:             issuestatus.Todo,
		DuplicateOfIssueID: pgtype.UUID{Valid: true},
	}, issuestatus.Done) {
		t.Fatal("expected stale duplicate marker on todo to be ineligible")
	}
	if batchTerminalStatusRowEligibleForRust(db.Issue{
		Status:             issuestatus.Cancelled,
		DuplicateOfIssueID: pgtype.UUID{Valid: true},
	}, issuestatus.Done) {
		t.Fatal("expected duplicate-marked cancelled row to be ineligible")
	}
	if batchTerminalStatusRowEligibleForRust(db.Issue{Status: "custom_done"}, issuestatus.Done) {
		t.Fatal("expected custom status row to be ineligible")
	}
}

func TestRustTerminalCancelledTaskReceiptsRequiredRejectsNull(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	h := &Handler{}
	if _, ok := h.rustIssueStatusTerminalCancelledTasks(rec, context.Background(), db.Issue{}, rustIssueStatusWriteReceipt{
		CancelledTasks: json.RawMessage(`null`),
	}); ok {
		t.Fatal("expected null cancelled_tasks to be rejected")
	}
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status=%d, want 502", rec.Code)
	}
}
