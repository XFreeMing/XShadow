package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestConfirmRustCommentDeleteTombstoneReceiptWithAttachmentPlan(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	h := &Handler{Queries: testHandler.Queries}
	scope := db.Comment{
		ID:          util.MustParseUUID("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"),
		IssueID:     util.MustParseUUID("dddddddd-dddd-4ddd-8ddd-dddddddddddd"),
		WorkspaceID: util.MustParseUUID(testWorkspaceID),
		AuthorType:  "member",
		AuthorID:    util.MustParseUUID(testUserID),
		Type:        "comment",
		Revision:    1,
	}
	plan := []rustIssueDeleteAttachmentPlanItem{
		{AttachmentID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"},
	}
	receipt := rustCommentDeleteReceipt{
		CommentID:                  scope.ID.String(),
		IssueID:                    scope.IssueID.String(),
		IssueRevision:              3,
		ObjectCleanupAttachmentIDs: []string{plan[0].AttachmentID},
		Tombstone:                  &CommentResponse{ID: scope.ID.String()},
	}
	if _, ok := h.confirmRustCommentDeleteTombstoneReceipt(context.Background(), scope, 2, receipt, nil); ok {
		t.Fatal("unexpected accept without attachment plan when cleanup ids present")
	}
	receipt.ObjectCleanupAttachmentIDs = nil
	if _, ok := h.confirmRustCommentDeleteTombstoneReceipt(context.Background(), scope, 2, receipt, plan); ok {
		t.Fatal("expected reject when cleanup ids missing from receipt")
	}
	if rustIssueDeleteAttachmentIDsMatchPlan(plan, []string{"ffffffff-ffff-4fff-8fff-ffffffffffff"}) {
		t.Fatal("expected foreign cleanup id mismatch")
	}
}

func TestConfirmRustCommentDeleteReplyReceiptWithAttachmentPlan(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	h := &Handler{Queries: testHandler.Queries}
	scope := db.Comment{
		ID:          util.MustParseUUID("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"),
		IssueID:     util.MustParseUUID("dddddddd-dddd-4ddd-8ddd-dddddddddddd"),
		WorkspaceID: util.MustParseUUID(testWorkspaceID),
	}
	plan := []rustIssueDeleteAttachmentPlanItem{
		{AttachmentID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"},
	}
	receipt := rustCommentDeleteReceipt{
		CommentID:                  scope.ID.String(),
		IssueID:                    scope.IssueID.String(),
		IssueRevision:              3,
		RemovedIDs:                 []string{scope.ID.String()},
		ObjectCleanupAttachmentIDs: []string{plan[0].AttachmentID},
	}
	branch := commentDeleteReplyBranchScope{PathLeafToRoot: []string{scope.ID.String()}}
	if h.confirmRustCommentDeleteReplyReceipt(context.Background(), scope, 2, receipt, branch, nil) {
		t.Fatal("unexpected accept without attachment plan")
	}
	receipt.ObjectCleanupAttachmentIDs = nil
	if h.confirmRustCommentDeleteReplyReceipt(context.Background(), scope, 2, receipt, branch, plan) {
		t.Fatal("expected reject when cleanup ids missing from receipt")
	}
}

func TestPlanRustTombstoneCommentDeleteUploadsPilotOffLeavesFallback(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	issueID := dbfx.Issue(t, "tombstone upload rust plan off")
	root := dbfx.Comment(t, issueID, "root")
	dbfx.Comment(t, issueID, "child", testutil.Cols{"parent_id": root})
	dbfx.Insert(t, "attachment", testutil.Cols{
		"workspace_id": testWorkspaceID, "issue_id": issueID, "comment_id": root,
		"uploader_type": "member", "uploader_id": testUserID,
		"filename": "root.png", "url": "https://example.test/root.png", "content_type": "image/png", "size_bytes": 1,
	})
	issue, err := testHandler.Queries.GetIssue(context.Background(), util.MustParseUUID(issueID))
	if err != nil {
		t.Fatalf("load issue: %v", err)
	}
	comment, err := testHandler.Queries.GetComment(context.Background(), util.MustParseUUID(root))
	if err != nil {
		t.Fatalf("load comment: %v", err)
	}
	h := *testHandler
	h.IssueReadBridge = &IssueReadBridge{
		CommentDeleteUploadsPilotScopes: nil,
	}
	req := httptest.NewRequest(http.MethodDelete, "/api/comments/"+root, nil)
	plan, err := h.planRustTombstoneCommentDelete(req, issue, comment, "member", testUserID, true)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.UseRust {
		t.Fatalf("plan = %+v, want Go fallback when uploads pilot off", plan)
	}
}

func TestPlanRustReplyCommentDeleteUploadsPilotOffLeavesFallback(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	issueID := dbfx.Issue(t, "reply upload rust plan off")
	root := dbfx.Comment(t, issueID, "root")
	reply := dbfx.Comment(t, issueID, "reply", testutil.Cols{"parent_id": root})
	dbfx.Insert(t, "attachment", testutil.Cols{
		"workspace_id": testWorkspaceID, "issue_id": issueID, "comment_id": reply,
		"uploader_type": "member", "uploader_id": testUserID,
		"filename": "reply.png", "url": "https://example.test/reply.png", "content_type": "image/png", "size_bytes": 1,
	})
	issue, err := testHandler.Queries.GetIssue(context.Background(), util.MustParseUUID(issueID))
	if err != nil {
		t.Fatalf("load issue: %v", err)
	}
	comment, err := testHandler.Queries.GetComment(context.Background(), util.MustParseUUID(reply))
	if err != nil {
		t.Fatalf("load comment: %v", err)
	}
	h := *testHandler
	h.IssueReadBridge = &IssueReadBridge{CommentDeleteUploadsPilotScopes: nil}
	req := httptest.NewRequest(http.MethodDelete, "/api/comments/"+reply, nil)
	plan, err := h.planRustReplyCommentDelete(req, issue, comment, "member", testUserID, false)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.UseRust {
		t.Fatalf("plan = %+v, want Go fallback when uploads pilot off", plan)
	}
}
