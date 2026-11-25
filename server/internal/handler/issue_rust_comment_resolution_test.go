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

func TestCommentResolutionUsesRustRequiresProjectlessMember(t *testing.T) {
	t.Parallel()
	h := &Handler{IssueReadBridge: &IssueReadBridge{CommentResolutionPilotScopes: []string{"*"}}}
	req := httptest.NewRequest(http.MethodPost, "/api/comments/1/resolve", nil)
	issue := db.Issue{Status: issuestatus.Todo}

	ok, err := h.commentResolutionUsesRust(req, issue, "member")
	if err != nil || !ok {
		t.Fatalf("projectless member uses rust = %v err=%v, want true", ok, err)
	}

	project := util.MustParseUUID("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	withProject := db.Issue{Status: issuestatus.Todo, ProjectID: project}
	ok, err = h.commentResolutionUsesRust(req, withProject, "member")
	if err != nil || ok {
		t.Fatalf("project issue uses rust = %v err=%v, want false", ok, err)
	}
}

func TestIssueReadBridgeCommentResolutionPilotEnv(t *testing.T) {
	t.Setenv("XSHADOW_ISSUES_RUST_COMMENT_RESOLUTION_PILOT", "resolution-pilot")
	t.Setenv("XSHADOW_ISSUES_UPSTREAM", "http://127.0.0.1:7")
	t.Setenv("XSHADOW_ISSUES_INTERNAL_TOKEN", "secret")
	bridge := NewIssueReadBridgeFromEnv(nil)
	if !reflect.DeepEqual(bridge.CommentResolutionPilotScopes, []string{"resolution-pilot"}) {
		t.Fatalf("CommentResolutionPilotScopes = %v, want [resolution-pilot]", bridge.CommentResolutionPilotScopes)
	}
}
