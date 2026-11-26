package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestParentIssueEditUsesRustRejectsMixedPayloads(t *testing.T) {
	t.Parallel()
	h := &Handler{IssueReadBridge: &IssueReadBridge{WritePilotScopes: []string{"*"}}}
	req := httptest.NewRequest(http.MethodPut, "/api/issues/1", nil)
	parentOnly := map[string]json.RawMessage{
		"parent_issue_id": json.RawMessage(`"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"`),
	}
	ok, err := h.parentIssueEditUsesRust(req, pgtype.UUID{}, parentOnly, nil)
	if err != nil || !ok {
		t.Fatalf("parent-only payload uses rust = %v err=%v, want true", ok, err)
	}

	mixed := map[string]json.RawMessage{
		"parent_issue_id":   json.RawMessage(`"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"`),
		"expected_revision": json.RawMessage(`2`),
		"title":             json.RawMessage(`"changed"`),
	}
	ok, err = h.parentIssueEditUsesRust(req, pgtype.UUID{}, mixed, nil)
	if err != nil || ok {
		t.Fatalf("mixed parent/title uses rust = %v err=%v, want false", ok, err)
	}
}

func TestRustParentWriteSnapshotsValidRejectsCorruptReplay(t *testing.T) {
	t.Parallel()
	issueID := util.MustParseUUID("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	ws := util.MustParseUUID("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
	parent := util.MustParseUUID("cccccccc-cccc-4ccc-8ccc-cccccccccccc")
	before := ordinaryIssueSnapshot{Issue: db.Issue{
		ID: issueID, WorkspaceID: ws, Revision: 3, ParentIssueID: pgtype.UUID{},
	}}
	after := ordinaryIssueSnapshot{Issue: db.Issue{
		ID: issueID, WorkspaceID: ws, Revision: 3,
		ParentIssueID: parent, LastActivityAt: pgtype.Timestamptz{Valid: true},
	}}
	revision := int64(3)
	if rustParentWriteSnapshotsValid(&revision, parent, before, after) {
		t.Fatal("revision bump without matching before revision should fail")
	}
	after.Issue.Revision = 4
	if !rustParentWriteSnapshotsValid(&revision, parent, before, after) {
		t.Fatal("valid parent snapshots should pass")
	}
	if !rustParentWriteSnapshotsValid(nil, parent, before, after) {
		t.Fatal("valid parent snapshots should pass without a client revision")
	}
	stale := revision - 1
	if rustParentWriteSnapshotsValid(&stale, parent, before, after) {
		t.Fatal("an explicit stale revision should fail receipt validation")
	}
	after.Issue.Title = "stale"
	if rustParentWriteSnapshotsValid(&revision, parent, before, after) {
		t.Fatal("corrupt title mutation should fail receipt validation")
	}
}

func TestBatchParentIssueEditUsesRustRequiresParentOnly(t *testing.T) {
	t.Parallel()
	h := &Handler{IssueReadBridge: &IssueReadBridge{BatchPilotScopes: []string{"*"}}}
	req := httptest.NewRequest(http.MethodPost, "/api/issues/batch-update", nil)
	parentOnly := map[string]json.RawMessage{"parent_issue_id": json.RawMessage(`"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"`)}
	ok, err := h.batchParentIssueEditUsesRust(req, pgtype.UUID{}, parentOnly)
	if err != nil || !ok {
		t.Fatalf("parent batch uses rust = %v err=%v", ok, err)
	}
	mixed := map[string]json.RawMessage{
		"parent_issue_id": json.RawMessage(`"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"`),
		"priority":        json.RawMessage(`"high"`),
	}
	ok, err = h.batchParentIssueEditUsesRust(req, pgtype.UUID{}, mixed)
	if err != nil || ok {
		t.Fatalf("mixed batch should stay on Go: ok=%v err=%v", ok, err)
	}
}
