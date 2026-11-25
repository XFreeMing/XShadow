package handler

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestCommentCreateUsesRustRejectsOutOfScopePayloads(t *testing.T) {
	t.Parallel()
	h := &Handler{IssueReadBridge: &IssueReadBridge{CommentPilotScopes: []string{"*"}}}
	req := httptest.NewRequest(http.MethodPost, "/api/issues/1/comments", nil)
	issue := db.Issue{Status: issuestatus.Todo}

	ok, err := h.commentCreateUsesRust(req, issue, CreateCommentRequest{
		Content: "hello",
		Type:    "comment",
	}, "member")
	if err != nil || !ok {
		t.Fatalf("plain member comment uses rust = %v err=%v, want true", ok, err)
	}

	ok, err = h.commentCreateUsesRust(req, issue, CreateCommentRequest{
		Content: "[@Agent](mention://agent/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa)",
		Type:    "comment",
	}, "member")
	if err != nil || ok {
		t.Fatalf("mention comment uses rust = %v err=%v, want false", ok, err)
	}

	ok, err = h.commentCreateUsesRust(req, issue, CreateCommentRequest{
		Content:  "reply",
		Type:     "comment",
		ParentID: strPtr("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"),
	}, "member")
	if err != nil || ok {
		t.Fatalf("reply uses rust = %v err=%v, want false", ok, err)
	}

	agent := util.MustParseUUID("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
	assigned := db.Issue{
		Status:       issuestatus.Todo,
		AssigneeType: pgtype.Text{String: "agent", Valid: true},
		AssigneeID:   agent,
	}
	ok, err = h.commentCreateUsesRust(req, assigned, CreateCommentRequest{
		Content: "hi",
		Type:    "comment",
	}, "member")
	if err != nil || ok {
		t.Fatalf("agent-assigned issue uses rust = %v err=%v, want false", ok, err)
	}
}

func TestRustCommentCreateReceiptValidRequiresCreateFields(t *testing.T) {
	t.Parallel()
	issueID := util.MustParseUUID("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	commentID := util.MustParseUUID("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
	issue := db.Issue{ID: issueID, Revision: 3, Title: "Title", Status: issuestatus.Todo}
	req := CreateCommentRequest{Content: "stored body", Type: "comment"}
	rev := int64(4)
	title := "Title"
	status := issuestatus.Todo
	comment := CommentResponse{
		ID:         commentID.String(),
		IssueID:    issueID.String(),
		Content:    "stored body",
		Type:       "comment",
		AuthorType: "member",
		AuthorID:   testUserID,
	}
	isCreated := true
	created := rustIssueCommentPayload{
		Created:       &isCreated,
		Comment:       &comment,
		IssueRevision: &rev,
		IssueTitle:    &title,
		IssueStatus:   &status,
	}
	if !rustCommentCreateReceiptValid(issue, "member", testUserID, commentID, req, created) {
		t.Fatal("expected valid created receipt")
	}
}

func TestIssueReadBridgeCommentPilotEnv(t *testing.T) {
	t.Setenv("XSHADOW_ISSUES_RUST_COMMENT_PILOT", "comment-pilot")
	t.Setenv("XSHADOW_ISSUES_UPSTREAM", "http://127.0.0.1:7")
	t.Setenv("XSHADOW_ISSUES_INTERNAL_TOKEN", "secret")
	bridge := NewIssueReadBridgeFromEnv(nil)
	if !reflect.DeepEqual(bridge.CommentPilotScopes, []string{"comment-pilot"}) {
		t.Fatalf("CommentPilotScopes = %v, want [comment-pilot]", bridge.CommentPilotScopes)
	}
}
