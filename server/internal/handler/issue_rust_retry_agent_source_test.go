package handler

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestIssueReadBridgeRetryAgentSourcePilotEnv(t *testing.T) {
	t.Setenv("XSHADOW_ISSUES_RUST_RETRY_AGENT_SOURCE_PILOT", "agent-retry")
	t.Setenv("XSHADOW_ISSUES_UPSTREAM", "http://127.0.0.1:7")
	t.Setenv("XSHADOW_ISSUES_INTERNAL_TOKEN", "secret")
	bridge := NewIssueReadBridgeFromEnv(nil)
	if !reflect.DeepEqual(bridge.RetryAgentSourcePilotScopes, []string{"agent-retry"}) {
		t.Fatalf("RetryAgentSourcePilotScopes = %v, want [agent-retry]", bridge.RetryAgentSourcePilotScopes)
	}
}

func TestPrepareRustRetryAgentSourceQuickCreateSkipsDisabledPilot(t *testing.T) {
	t.Parallel()
	h := &Handler{IssueReadBridge: &IssueReadBridge{}}
	ctx := httptest.NewRequest(http.MethodPost, "/", nil).Context()
	ws := util.MustParseUUID("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	user := util.MustParseUUID("dddddddd-dddd-4ddd-8ddd-dddddddddddd")
	taskID := util.MustParseUUID("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
	useRust, _, err := h.prepareRustRetryAgentSourceQuickCreate(ctx, ws, user, taskID)
	if err != nil || useRust {
		t.Fatalf("missing pilot uses rust = %v err=%v, want false", useRust, err)
	}
}

func TestQuickCreateContextFromTaskRejectsLinkedRows(t *testing.T) {
	t.Parallel()
	issueID := util.MustParseUUID("cccccccc-cccc-4ccc-8ccc-cccccccccccc")
	if _, ok := quickCreateContextFromTask(db.AgentTaskQueue{IssueID: issueID}); ok {
		t.Fatal("linked issue task parsed as quick-create")
	}
	if _, ok := quickCreateContextFromTask(db.AgentTaskQueue{Context: []byte(`{"type":"quick_create"}`)}); !ok {
		t.Fatal("valid quick-create JSON not parsed")
	}
}
