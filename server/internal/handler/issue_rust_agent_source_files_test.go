package handler

import (
	"encoding/json"
	"testing"

	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestIssueReadBridgeAgentSourceFilesLifecyclePilotEnv(t *testing.T) {
	t.Setenv("XSHADOW_ISSUES_RUST_AGENT_SOURCE_FILES_LIFECYCLE_PILOT", "lifecycle")
	t.Setenv("XSHADOW_ISSUES_UPSTREAM", "http://127.0.0.1:7")
	t.Setenv("XSHADOW_ISSUES_INTERNAL_TOKEN", "secret")
	bridge := NewIssueReadBridgeFromEnv(nil)
	if len(bridge.AgentSourceFilesLifecyclePilotScopes) != 1 || bridge.AgentSourceFilesLifecyclePilotScopes[0] != "lifecycle" {
		t.Fatalf("AgentSourceFilesLifecyclePilotScopes = %v", bridge.AgentSourceFilesLifecyclePilotScopes)
	}
}

func TestBuildAgentSourceContextFilePlanEmpty(t *testing.T) {
	t.Parallel()
	h := &Handler{}
	ws := util.MustParseUUID("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	ctx := util.MustParseUUID("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
	owner := util.MustParseUUID("cccccccc-cccc-4ccc-8ccc-cccccccccccc")
	snap, _ := json.Marshal(service.SourceContextSnapshot{
		SourceIssue: service.SourceContextIssueSnapshot{ID: "dddddddd-dddd-4ddd-8ddd-dddddddddddd"},
	})
	plan, ok := h.buildAgentSourceContextFilePlan(ws, ctx, owner, snap, nil)
	if !ok || plan.HasFiles || len(plan.Clones) != 0 {
		t.Fatalf("empty plan = %+v ok=%v, want file-free", plan, ok)
	}
}

func TestBuildAgentSourceContextFilePlanRejectsRowWithoutFrozen(t *testing.T) {
	t.Parallel()
	h := &Handler{}
	ws := util.MustParseUUID("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	ctx := util.MustParseUUID("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
	owner := util.MustParseUUID("cccccccc-cccc-4ccc-8ccc-cccccccccccc")
	snap, _ := json.Marshal(service.SourceContextSnapshot{
		SourceIssue: service.SourceContextIssueSnapshot{ID: "dddddddd-dddd-4ddd-8ddd-dddddddddddd"},
	})
	rows := []db.Attachment{{
		ID:              util.MustParseUUID("eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"),
		SourceContextID: ctx,
		UploaderType:    "member",
		UploaderID:      owner,
		Filename:        "x.txt",
	}}
	_, ok := h.buildAgentSourceContextFilePlan(ws, ctx, owner, snap, rows)
	if ok {
		t.Fatal("rows without frozen snapshot attachments should not qualify")
	}
}
