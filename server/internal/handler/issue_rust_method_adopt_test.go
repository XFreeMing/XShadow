package handler

import (
	"encoding/json"
	"reflect"
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/workmethod"
)

func TestRustMethodAdoptPilotScopesIndependent(t *testing.T) {
	t.Setenv("XSHADOW_ISSUES_RUST_METHOD_ADOPT_PILOT", "method-adopt-pilot")
	t.Setenv("XSHADOW_TABLES_UPSTREAM", "http://127.0.0.1:18080")
	t.Setenv("XSHADOW_TABLES_INTERNAL_TOKEN", "shared-secret")

	bridge := NewIssueReadBridgeFromEnv(nil)
	if !bridge.Enabled() {
		t.Fatal("expected bridge enabled with loopback upstream")
	}
	if !reflect.DeepEqual(bridge.MethodAdoptPilotScopes, []string{"method-adopt-pilot"}) {
		t.Fatalf("MethodAdoptPilotScopes = %v, want [method-adopt-pilot]", bridge.MethodAdoptPilotScopes)
	}
}

func TestRunSupportsRustMethodAdoptRejectsLegacyShape(t *testing.T) {
	run := workmethod.Run{State: "working", Method: &workmethod.Template{ID: "m"}, Revision: 1}
	if runSupportsRustMethodAdopt(run, true) {
		t.Fatal("missing current_actor must stay on Go")
	}
	run.CurrentActor = &workmethod.Actor{Kind: "agent", ID: "a"}
	if !runSupportsRustMethodAdopt(run, true) {
		t.Fatal("self-contained working run should qualify")
	}
}

func TestRustMethodAdoptReceiptValidRejectsPartialSnapshot(t *testing.T) {
	issueID := parseUUID("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	wsID := parseUUID("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
	issue := db.Issue{ID: issueID, WorkspaceID: wsID, Revision: 4}
	run, err := json.Marshal(workmethod.Run{Revision: 2, State: "working", Method: &workmethod.Template{ID: "m", Revision: 2}})
	if err != nil {
		t.Fatal(err)
	}
	var partial ordinaryIssueSnapshot
	if err := json.Unmarshal([]byte(`{
		"id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		"workspace_id":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	}`), &partial); err != nil {
		t.Fatal(err)
	}
	if rustMethodAdoptReceiptValid(issue, true, run, partial, partial) {
		t.Fatal("partial upstream snapshot must not validate")
	}
}
