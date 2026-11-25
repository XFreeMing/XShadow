package handler

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestIssueReadBridgeBatchDeletePilotEnv(t *testing.T) {
	t.Setenv("XSHADOW_ISSUES_RUST_BATCH_DELETE_PILOT", "batch-del-pilot")
	t.Setenv("XSHADOW_ISSUES_RUST_DELETE_PILOT", "del-pilot")
	t.Setenv("XSHADOW_TABLES_UPSTREAM", "http://127.0.0.1:18080")
	t.Setenv("XSHADOW_TABLES_INTERNAL_TOKEN", "shared-secret")

	bridge := NewIssueReadBridgeFromEnv(nil)
	if !bridge.Enabled() {
		t.Fatal("expected bridge enabled with loopback upstream")
	}
	if !reflect.DeepEqual(bridge.BatchDeletePilotScopes, []string{"batch-del-pilot"}) {
		t.Fatalf("BatchDeletePilotScopes = %v, want [batch-del-pilot]", bridge.BatchDeletePilotScopes)
	}
	if !reflect.DeepEqual(bridge.DeletePilotScopes, []string{"del-pilot"}) {
		t.Fatalf("DeletePilotScopes = %v, want [del-pilot]", bridge.DeletePilotScopes)
	}
}

func TestBatchDeleteIssuesUsesRustRejectsDeletePilotOnly(t *testing.T) {
	h := &Handler{IssueReadBridge: &IssueReadBridge{DeletePilotScopes: []string{"*"}}}
	ws := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	issues := []db.Issue{{WorkspaceID: ws, CreatorType: "member"}}
	r := newRequest(http.MethodPost, "/api/issues/batch-delete", map[string]any{
		"issue_ids": []string{"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"},
	})
	useRust, err := h.batchDeleteIssuesUsesRust(r, issues)
	if err != nil || useRust {
		t.Fatalf("useRust=%v err=%v, want false without batch delete pilot", useRust, err)
	}
}

func TestBatchDeleteIssuesUsesRustRejectsMixedShape(t *testing.T) {
	h := *testHandler
	h.IssueReadBridge = &IssueReadBridge{BatchDeletePilotScopes: []string{"*"}}
	ws := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	issues := []db.Issue{
		{WorkspaceID: ws, CreatorType: "member"},
		{WorkspaceID: ws, CreatorType: "agent"},
	}
	r := newRequest(http.MethodPost, "/api/issues/batch-delete", map[string]any{
		"issue_ids": []string{
			"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
			"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
		},
	})
	useRust, err := h.batchDeleteIssuesUsesRust(r, issues)
	if err != nil || useRust {
		t.Fatalf("useRust=%v err=%v, want false for mixed batch", useRust, err)
	}
}

func TestRustIssueBatchDeleteSnapshotsDecode(t *testing.T) {
	original := db.Issue{
		ID:          pgtype.UUID{Bytes: [16]byte{9}, Valid: true},
		WorkspaceID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
	}
	childRaw, err := json.Marshal(map[string]any{
		"id": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "workspace_id": uuidToString(original.WorkspaceID),
		"number": 1, "title": "child", "status": "todo", "priority": "none",
		"creator_type": "member", "creator_id": "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
		"revision": 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	dupRaw, err := json.Marshal(map[string]any{
		"id": "dddddddd-dddd-4ddd-8ddd-dddddddddddd", "workspace_id": uuidToString(original.WorkspaceID),
		"number": 2, "title": "dup", "status": "cancelled", "priority": "none",
		"creator_type": "member", "creator_id": "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
		"revision": 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	reply := rustIssueBatchDeleteResult{
		Deleted:          1,
		DeletedIssueIDs:  []string{uuidToString(original.ID)},
		DetachedChildren: []string{string(childRaw)},
		ClearedDuplicates: []rustIssueDeleteClearedDuplicate{{
			Issue: string(dupRaw), OriginalIssueID: uuidToString(original.ID),
		}},
	}
	detached, cleared, ok := rustIssueBatchDeleteSnapshots([]db.Issue{original}, reply)
	if !ok || len(detached) != 1 || len(cleared) != 1 {
		t.Fatalf("ok=%v detached=%d cleared=%d", ok, len(detached), len(cleared))
	}
}
