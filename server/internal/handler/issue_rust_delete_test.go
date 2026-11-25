package handler

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestIssueReadBridgeDeletePilotEnv(t *testing.T) {
	t.Setenv("XSHADOW_ISSUES_RUST_DELETE_PILOT", "del-pilot")
	t.Setenv("XSHADOW_ISSUES_RUST_WRITE_PILOT", "")
	t.Setenv("XSHADOW_TABLES_UPSTREAM", "http://127.0.0.1:18080")
	t.Setenv("XSHADOW_TABLES_INTERNAL_TOKEN", "shared-secret")

	bridge := NewIssueReadBridgeFromEnv(nil)
	if !bridge.Enabled() {
		t.Fatal("expected bridge enabled with loopback upstream")
	}
	if !reflect.DeepEqual(bridge.DeletePilotScopes, []string{"del-pilot"}) {
		t.Fatalf("DeletePilotScopes = %v, want [del-pilot]", bridge.DeletePilotScopes)
	}
}

func TestIssueReadBridgeDeleteSourceContextPilotEnv(t *testing.T) {
	t.Setenv("XSHADOW_ISSUES_RUST_DELETE_SOURCE_CONTEXT_PILOT", "del-src-pilot")
	t.Setenv("XSHADOW_TABLES_UPSTREAM", "http://127.0.0.1:18080")
	t.Setenv("XSHADOW_TABLES_INTERNAL_TOKEN", "shared-secret")
	bridge := NewIssueReadBridgeFromEnv(nil)
	if !reflect.DeepEqual(bridge.DeleteSourceContextPilotScopes, []string{"del-src-pilot"}) {
		t.Fatalf("DeleteSourceContextPilotScopes = %v, want [del-src-pilot]", bridge.DeleteSourceContextPilotScopes)
	}
}

func TestIssueReadBridgeDeleteSourceContextFilesPilotEnv(t *testing.T) {
	t.Setenv("XSHADOW_ISSUES_RUST_DELETE_SOURCE_CONTEXT_FILES_PILOT", "del-src-files-pilot")
	t.Setenv("XSHADOW_TABLES_UPSTREAM", "http://127.0.0.1:18080")
	t.Setenv("XSHADOW_TABLES_INTERNAL_TOKEN", "shared-secret")
	bridge := NewIssueReadBridgeFromEnv(nil)
	if !reflect.DeepEqual(bridge.DeleteSourceContextFilesPilotScopes, []string{"del-src-files-pilot"}) {
		t.Fatalf("DeleteSourceContextFilesPilotScopes = %v, want [del-src-files-pilot]", bridge.DeleteSourceContextFilesPilotScopes)
	}
}

func TestIssueReadBridgeDeleteSourceProjectPilotEnv(t *testing.T) {
	t.Setenv("XSHADOW_ISSUES_RUST_DELETE_SOURCE_PROJECT_PILOT", "del-src-proj-pilot")
	t.Setenv("XSHADOW_TABLES_UPSTREAM", "http://127.0.0.1:18080")
	t.Setenv("XSHADOW_TABLES_INTERNAL_TOKEN", "shared-secret")
	bridge := NewIssueReadBridgeFromEnv(nil)
	if !reflect.DeepEqual(bridge.DeleteSourceProjectPilotScopes, []string{"del-src-proj-pilot"}) {
		t.Fatalf("DeleteSourceProjectPilotScopes = %v, want [del-src-proj-pilot]", bridge.DeleteSourceProjectPilotScopes)
	}
}

func TestIssueReadBridgeDeleteAttachmentsPilotEnv(t *testing.T) {
	t.Setenv("XSHADOW_ISSUES_RUST_DELETE_ATTACHMENTS_PILOT", "del-att-pilot")
	t.Setenv("XSHADOW_TABLES_UPSTREAM", "http://127.0.0.1:18080")
	t.Setenv("XSHADOW_TABLES_INTERNAL_TOKEN", "shared-secret")
	bridge := NewIssueReadBridgeFromEnv(nil)
	if !reflect.DeepEqual(bridge.DeleteAttachPilotScopes, []string{"del-att-pilot"}) {
		t.Fatalf("DeleteAttachPilotScopes = %v, want [del-att-pilot]", bridge.DeleteAttachPilotScopes)
	}
}

func TestDeleteIssueUsesRustRejectsAgentCreatedIssues(t *testing.T) {
	h := &Handler{IssueReadBridge: &IssueReadBridge{DeletePilotScopes: []string{"*"}}}
	issue := db.Issue{
		WorkspaceID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
		CreatorType: "agent",
	}
	r := newRequest(http.MethodDelete, "/api/issues/x", nil)
	selection, err := h.selectRustIssueDelete(r, issue)
	if err != nil || selection.UseRust {
		t.Fatalf("useRust=%v err=%v, want false", selection.UseRust, err)
	}
}

func TestRustIssueDeleteSnapshotsDecodeSnapshots(t *testing.T) {
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
	reply := rustIssueDeleteResult{
		Deleted:          true,
		IssueID:          uuidToString(original.ID),
		DetachedChildren: []string{string(childRaw)},
		ClearedDuplicates: []rustIssueDeleteClearedDuplicate{{
			Issue: string(dupRaw), OriginalIssueID: uuidToString(original.ID),
		}},
	}
	detached, cleared, ok := rustIssueDeleteSnapshots(original, reply)
	if !ok || len(detached) != 1 || len(cleared) != 1 {
		t.Fatalf("ok=%v detached=%d cleared=%d", ok, len(detached), len(cleared))
	}
}
