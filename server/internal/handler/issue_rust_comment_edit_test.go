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

func TestCommentEditUsesRustRequiresStrictMemberAuthor(t *testing.T) {
	t.Parallel()
	h := &Handler{}
	req := httptest.NewRequest(http.MethodPut, "/api/comments/1", nil)
	issue := db.Issue{Status: issuestatus.Todo}
	comment := db.Comment{
		ID:         util.MustParseUUID("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"),
		AuthorType: "member",
		AuthorID:   util.MustParseUUID("cccccccc-cccc-4ccc-8ccc-cccccccccccc"),
		Type:       "comment",
		IssueID:    util.MustParseUUID("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"),
	}
	plan := commentEditRequest{Content: "hello", ExpectedRevision: 1}

	legacy := commentEditRequest{Content: plan.Content}
	ok, err := h.commentEditUsesRust(req, issue, comment, "member", "cccccccc-cccc-4ccc-8ccc-cccccccccccc", legacy)
	if err != nil || ok {
		t.Fatalf("legacy request uses rust = %v err=%v, want false", ok, err)
	}

	ok, err = h.commentEditUsesRust(req, issue, comment, "member", "dddddddd-dddd-4ddd-8ddd-dddddddddddd", plan)
	if err != nil || ok {
		t.Fatalf("non-author uses rust = %v err=%v, want false", ok, err)
	}

	reply := comment
	reply.ParentID = util.MustParseUUID("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	ok, err = h.commentEditUsesRust(req, issue, reply, "member", "cccccccc-cccc-4ccc-8ccc-cccccccccccc", plan)
	if err != nil || ok {
		t.Fatalf("reply uses rust = %v err=%v, want false", ok, err)
	}

	withMention := commentEditRequest{Content: "ping [@member](mention://member/dddddddd-dddd-4ddd-8ddd-dddddddddddd)", ExpectedRevision: 1}
	ok, err = h.commentEditUsesRust(req, issue, comment, "member", "cccccccc-cccc-4ccc-8ccc-cccccccccccc", withMention)
	if err != nil || ok {
		t.Fatalf("mention edit uses rust = %v err=%v, want false", ok, err)
	}

	project := util.MustParseUUID("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	ok, err = h.commentEditUsesRust(req, db.Issue{Status: issuestatus.Todo, ProjectID: project}, comment, "member", "cccccccc-cccc-4ccc-8ccc-cccccccccccc", plan)
	if err != nil || ok {
		t.Fatalf("project issue uses rust = %v err=%v, want false", ok, err)
	}

	withSource := comment
	withSource.SourceTaskID = util.MustParseUUID("eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee")
	ok, err = h.commentEditUsesRust(req, issue, withSource, "member", "cccccccc-cccc-4ccc-8ccc-cccccccccccc", plan)
	if err != nil || ok {
		t.Fatalf("source lineage uses rust = %v err=%v, want false", ok, err)
	}
}

func TestIssueReadBridgeCommentEditPilotEnv(t *testing.T) {
	t.Setenv("XSHADOW_ISSUES_RUST_COMMENT_EDIT_PILOT", "comment-edit-pilot")
	t.Setenv("XSHADOW_ISSUES_UPSTREAM", "http://127.0.0.1:7")
	t.Setenv("XSHADOW_ISSUES_INTERNAL_TOKEN", "secret")
	bridge := NewIssueReadBridgeFromEnv(nil)
	if !reflect.DeepEqual(bridge.CommentEditPilotScopes, []string{"comment-edit-pilot"}) {
		t.Fatalf("CommentEditPilotScopes = %v, want [comment-edit-pilot]", bridge.CommentEditPilotScopes)
	}
}
