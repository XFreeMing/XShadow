package handler

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/util"
)

func TestIssueReadBridgeAttachAgentSourcePilotEnv(t *testing.T) {
	t.Setenv("XSHADOW_ISSUES_RUST_ATTACH_AGENT_SOURCE_PILOT", "agent-attach")
	t.Setenv("XSHADOW_ISSUES_UPSTREAM", "http://127.0.0.1:7")
	t.Setenv("XSHADOW_ISSUES_INTERNAL_TOKEN", "secret")
	bridge := NewIssueReadBridgeFromEnv(nil)
	if !reflect.DeepEqual(bridge.AttachAgentSourcePilotScopes, []string{"agent-attach"}) {
		t.Fatalf("AttachAgentSourcePilotScopes = %v, want [agent-attach]", bridge.AttachAgentSourcePilotScopes)
	}
}

func TestPrepareAgentQuickCreateAttachRustSkipsWithoutPilots(t *testing.T) {
	t.Parallel()
	h := &Handler{IssueReadBridge: &IssueReadBridge{}}
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	ws := util.MustParseUUID("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	origin := util.MustParseUUID("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
	parent := util.MustParseUUID("cccccccc-cccc-4ccc-8ccc-cccccccccccc")
	use, _, err := h.prepareAgentQuickCreateAttachRust(
		r, ws, "agent", "dddddddd-dddd-4ddd-8ddd-dddddddddddd",
		pgtype.Text{String: "quick_create", Valid: true}, origin,
		CreateIssueRequest{Title: "child"},
		parent, pgtype.UUID{}, pgtype.UUID{}, pgtype.Text{}, nil, nil, "todo", false,
	)
	if err != nil || use {
		t.Fatalf("missing pilots useRust = %v err=%v, want false", use, err)
	}
}

func TestTaskHeaderMatchesOrigin(t *testing.T) {
	t.Parallel()
	origin := util.MustParseUUID("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Header.Set("X-Task-ID", "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
	if !taskHeaderMatchesOrigin(r, origin) {
		t.Fatal("expected matching X-Task-ID")
	}
	r.Header.Set("X-Task-ID", "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	if taskHeaderMatchesOrigin(r, origin) {
		t.Fatal("expected mismatch")
	}
}

func TestAgentQuickCreateAttachShapeRejectsAssignment(t *testing.T) {
	t.Parallel()
	parent := util.MustParseUUID("cccccccc-cccc-4ccc-8ccc-cccccccccccc")
	if agentQuickCreateAttachShape(
		CreateIssueRequest{Title: "x"},
		pgtype.Text{String: "agent", Valid: true},
		parent, pgtype.UUID{}, pgtype.UUID{}, 0, 0, "todo",
	) {
		t.Fatal("assignee should reject attach shape")
	}
}
