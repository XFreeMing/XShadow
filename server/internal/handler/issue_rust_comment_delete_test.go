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

func TestCommentDeleteUsesRustRejectsReply(t *testing.T) {
	t.Parallel()
	h := &Handler{}
	req := httptest.NewRequest(http.MethodDelete, "/api/comments/1", nil)
	issue := db.Issue{Status: issuestatus.Todo}
	parent := util.MustParseUUID("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	comment := db.Comment{
		ID:         util.MustParseUUID("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"),
		AuthorType: "member",
		AuthorID:   util.MustParseUUID("cccccccc-cccc-4ccc-8ccc-cccccccccccc"),
		Type:       "comment",
		ParentID:   parent,
		IssueID:    util.MustParseUUID("dddddddd-dddd-4ddd-8ddd-dddddddddddd"),
	}
	ok, err := h.commentDeleteUsesRust(req, issue, comment, "member", "cccccccc-cccc-4ccc-8ccc-cccccccccccc", false)
	if err != nil || ok {
		t.Fatalf("reply delete uses rust = %v err=%v, want false", ok, err)
	}
}

func TestCommentDeleteUsesRustTombstoneNeedsReplies(t *testing.T) {
	t.Parallel()
	h := &Handler{}
	req := httptest.NewRequest(http.MethodDelete, "/api/comments/1", nil)
	issue := db.Issue{Status: issuestatus.Todo}
	comment := db.Comment{
		ID:         util.MustParseUUID("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"),
		AuthorType: "member",
		AuthorID:   util.MustParseUUID("cccccccc-cccc-4ccc-8ccc-cccccccccccc"),
		Type:       "comment",
		IssueID:    util.MustParseUUID("dddddddd-dddd-4ddd-8ddd-dddddddddddd"),
	}
	ok, err := h.commentDeleteUsesRustTombstone(req, issue, comment, "member", "cccccccc-cccc-4ccc-8ccc-cccccccccccc", false)
	if err != nil || ok {
		t.Fatalf("leaf delete uses tombstone rust = %v err=%v, want false", ok, err)
	}
}

func TestIssueReadBridgeCommentDeletePilotEnv(t *testing.T) {
	t.Setenv("XSHADOW_ISSUES_RUST_COMMENT_DELETE_PILOT", "comment-delete-pilot")
	t.Setenv("XSHADOW_ISSUES_RUST_COMMENT_DELETE_TOMBSTONE_PILOT", "comment-delete-tombstone-pilot")
	t.Setenv("XSHADOW_ISSUES_RUST_COMMENT_DELETE_REPLY_PILOT", "comment-delete-reply-pilot")
	t.Setenv("XSHADOW_ISSUES_UPSTREAM", "http://127.0.0.1:7")
	t.Setenv("XSHADOW_ISSUES_INTERNAL_TOKEN", "secret")
	bridge := NewIssueReadBridgeFromEnv(nil)
	if !reflect.DeepEqual(bridge.CommentDeletePilotScopes, []string{"comment-delete-pilot"}) {
		t.Fatalf("CommentDeletePilotScopes = %v, want [comment-delete-pilot]", bridge.CommentDeletePilotScopes)
	}
	if !reflect.DeepEqual(bridge.CommentDeleteTombstonePilotScopes, []string{"comment-delete-tombstone-pilot"}) {
		t.Fatalf("CommentDeleteTombstonePilotScopes = %v, want [comment-delete-tombstone-pilot]", bridge.CommentDeleteTombstonePilotScopes)
	}
	if !reflect.DeepEqual(bridge.CommentDeleteReplyPilotScopes, []string{"comment-delete-reply-pilot"}) {
		t.Fatalf("CommentDeleteReplyPilotScopes = %v, want [comment-delete-reply-pilot]", bridge.CommentDeleteReplyPilotScopes)
	}
}

func TestIssueReadBridgeCommentDeleteUploadsPilotEnv(t *testing.T) {
	t.Setenv("XSHADOW_ISSUES_RUST_COMMENT_DELETE_UPLOADS_PILOT", "comment-delete-uploads-pilot")
	t.Setenv("XSHADOW_ISSUES_UPSTREAM", "http://127.0.0.1:7")
	t.Setenv("XSHADOW_ISSUES_INTERNAL_TOKEN", "secret")
	bridge := NewIssueReadBridgeFromEnv(nil)
	if !reflect.DeepEqual(bridge.CommentDeleteUploadsPilotScopes, []string{"comment-delete-uploads-pilot"}) {
		t.Fatalf("CommentDeleteUploadsPilotScopes = %v, want [comment-delete-uploads-pilot]", bridge.CommentDeleteUploadsPilotScopes)
	}
}

func TestReplyRemovedIDsMatchPath(t *testing.T) {
	t.Parallel()
	path := []string{"leaf", "child", "root"}
	if !replyRemovedIDsMatchPath([]string{"leaf", "child", "root"}, path) {
		t.Fatal("expected full path to match")
	}
	if !replyRemovedIDsMatchPath([]string{"leaf", "child"}, path) {
		t.Fatal("expected leaf+child prefix to match")
	}
	if !replyRemovedIDsMatchPath([]string{"leaf"}, path) {
		t.Fatal("expected leaf-only prefix to match")
	}
	if replyRemovedIDsMatchPath([]string{"leaf", "forged"}, path) {
		t.Fatal("expected foreign id to fail")
	}
	if replyRemovedIDsMatchPath(nil, path) {
		t.Fatal("expected empty removed ids to fail")
	}
}
