package handler

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestIssueReactionUsesRustRequiresProjectlessMember(t *testing.T) {
	t.Parallel()
	h := &Handler{IssueReadBridge: &IssueReadBridge{ReactionPilotScopes: []string{"*"}}}
	req := httptest.NewRequest(http.MethodPost, "/api/issues/1/reactions", nil)
	issue := db.Issue{Status: issuestatus.Todo}

	ok, err := h.issueReactionUsesRust(req, issue, "member")
	if err != nil || !ok {
		t.Fatalf("projectless member reaction uses rust = %v err=%v, want true", ok, err)
	}

	project := util.MustParseUUID("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	withProject := db.Issue{
		Status:    issuestatus.Todo,
		ProjectID: project,
	}
	ok, err = h.issueReactionUsesRust(req, withProject, "member")
	if err != nil || ok {
		t.Fatalf("project issue uses rust = %v err=%v, want false", ok, err)
	}

	ok, err = h.issueReactionUsesRust(req, issue, "agent")
	if err != nil || ok {
		t.Fatalf("agent actor uses rust = %v err=%v, want false", ok, err)
	}
}

func TestIssueReactionReceiptRevisionValid(t *testing.T) {
	t.Parallel()
	changed, noop := true, false
	if issueReactionReceiptRevisionValid(2, 3, nil) || issueReactionReceiptRevisionValid(2, 2, &changed) {
		t.Fatal("expected missing change marker and non-advancing mutation to fail")
	}
	if !issueReactionReceiptRevisionValid(2, 3, &changed) || !issueReactionReceiptRevisionValid(2, 2, &noop) {
		t.Fatal("expected committed mutation and same-revision no-op to pass")
	}
}

func TestIssueReadBridgeReactionPilotEnv(t *testing.T) {
	t.Setenv("XSHADOW_ISSUES_RUST_REACTION_PILOT", "reaction-pilot")
	t.Setenv("XSHADOW_ISSUES_UPSTREAM", "http://127.0.0.1:7")
	t.Setenv("XSHADOW_ISSUES_INTERNAL_TOKEN", "secret")
	bridge := NewIssueReadBridgeFromEnv(nil)
	if !reflect.DeepEqual(bridge.ReactionPilotScopes, []string{"reaction-pilot"}) {
		t.Fatalf("ReactionPilotScopes = %v, want [reaction-pilot]", bridge.ReactionPilotScopes)
	}
}
