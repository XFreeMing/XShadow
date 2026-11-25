package handler

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestManualSourceContextCreateRustRejectsOutOfScopeShapes(t *testing.T) {
	t.Parallel()
	h := &Handler{IssueReadBridge: &IssueReadBridge{
		CreatePilotScopes:              []string{"*"},
		CreateSourceContextPilotScopes: []string{"*"},
	}}
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	ws := util.MustParseUUID("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	sourceIssue := util.MustParseUUID("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
	capture := service.SourceContextCapture{
		SourceIssueID: sourceIssue,
		Snapshot: service.SourceContextSnapshot{
			SourceIssue: service.SourceContextIssueSnapshot{ID: util.UUIDToString(sourceIssue)},
		},
	}
	assignee := "member"
	plan, err := h.manualSourceContextCreateRustEligible(
		req, ws,
		CreateIssueRequest{Title: "child", Status: issuestatus.Todo, AssigneeType: &assignee},
		capture,
		service.SourceContextBuild{},
		issuestatus.Todo,
		false,
	)
	if err != nil || plan.useRust {
		t.Fatalf("assignee uses rust = %v err=%v, want false", plan.useRust, err)
	}

	plan, err = h.manualSourceContextCreateRustEligible(
		req, ws,
		CreateIssueRequest{Title: "child", Status: issuestatus.Todo},
		capture,
		service.SourceContextBuild{Rows: []db.Attachment{{}}},
		issuestatus.Todo,
		false,
	)
	if err != nil || plan.useRust {
		t.Fatalf("attachment rows uses rust = %v err=%v, want false", plan.useRust, err)
	}

	hMissing := &Handler{IssueReadBridge: &IssueReadBridge{CreatePilotScopes: []string{"*"}}}
	plan, err = hMissing.manualSourceContextCreateRustEligible(
		req, ws,
		CreateIssueRequest{Title: "child", Status: issuestatus.Todo},
		capture,
		service.SourceContextBuild{},
		issuestatus.Todo,
		false,
	)
	if err != nil || plan.useRust {
		t.Fatalf("missing source-context pilot uses rust = %v err=%v, want false", plan.useRust, err)
	}
}

func TestIssueReadBridgeCreateSourceContextPilotEnv(t *testing.T) {
	t.Setenv("XSHADOW_ISSUES_RUST_CREATE_PILOT", "create-pilot")
	t.Setenv("XSHADOW_ISSUES_RUST_CREATE_SOURCE_CONTEXT_PILOT", "create-source-context")
	t.Setenv("XSHADOW_ISSUES_UPSTREAM", "http://127.0.0.1:7")
	t.Setenv("XSHADOW_ISSUES_INTERNAL_TOKEN", "secret")
	bridge := NewIssueReadBridgeFromEnv(nil)
	if !reflect.DeepEqual(bridge.CreateSourceContextPilotScopes, []string{"create-source-context"}) {
		t.Fatalf("CreateSourceContextPilotScopes = %v, want [create-source-context]", bridge.CreateSourceContextPilotScopes)
	}
}
