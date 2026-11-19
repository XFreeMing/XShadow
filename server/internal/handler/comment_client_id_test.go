package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

// TestCreateComment_ClientSuppliedIDReplaysWithoutDuplicateTrigger pins the
// member-answer retry contract: the client picks the comment UUID once, and a
// double click, network retry or second window that posts it again gets the
// existing comment instead of a second answer or a second agent run.
func TestCreateComment_ClientSuppliedIDReplaysWithoutDuplicateTrigger(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	issueID := createTestIssue(t, "client comment id replay fixture", "todo", "medium")
	t.Cleanup(func() { deleteTestIssue(t, issueID) })
	otherIssueID := createTestIssue(t, "client comment id replay other issue", "todo", "medium")
	t.Cleanup(func() { deleteTestIssue(t, otherIssueID) })

	agentID := createHandlerTestAgent(t, "Client ID Replay Agent", nil)
	commentID := uuid.NewString()
	content := fmt.Sprintf("[@Replay](mention://agent/%s)\n\n成员答复", agentID)

	post := func(issue string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		r := newRequest(http.MethodPost, "/api/issues/"+issue+"/comments", map[string]any{
			"id":      commentID,
			"content": content,
		})
		r = withURLParam(r, "id", issue)
		testHandler.CreateComment(w, r)
		return w
	}

	first := post(issueID)
	if first.Code != http.StatusCreated {
		t.Fatalf("first create: expected 201, got %d: %s", first.Code, first.Body.String())
	}
	var firstBody struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(first.Body).Decode(&firstBody); err != nil {
		t.Fatalf("decode first response: %v", err)
	}
	if firstBody.ID != commentID {
		t.Fatalf("first create id = %q, want %q", firstBody.ID, commentID)
	}
	if n := countQueuedCommentTriggerTasks(t, issueID, agentID); n != 1 {
		t.Fatalf("first create queued %d runs, want 1", n)
	}

	second := post(issueID)
	if second.Code != http.StatusOK {
		t.Fatalf("replayed create: expected 200, got %d: %s", second.Code, second.Body.String())
	}
	var secondBody struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(second.Body).Decode(&secondBody); err != nil {
		t.Fatalf("decode replay response: %v", err)
	}
	if secondBody.ID != commentID {
		t.Fatalf("replayed id = %q, want %q", secondBody.ID, commentID)
	}

	var rows int
	if err := testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM comment WHERE id = $1`, commentID).Scan(&rows); err != nil {
		t.Fatalf("count comments: %v", err)
	}
	if rows != 1 {
		t.Fatalf("comment rows = %d, want exactly 1", rows)
	}
	if n := countQueuedCommentTriggerTasks(t, issueID, agentID); n != 1 {
		t.Fatalf("replay queued %d runs, want still 1", n)
	}

	// The same id on a different issue is a conflict, not a second row or a
	// silent success that hides where the answer landed.
	third := post(otherIssueID)
	if third.Code != http.StatusConflict {
		t.Fatalf("id on another issue: expected 409, got %d: %s", third.Code, third.Body.String())
	}
	if n := countQueuedCommentTriggerTasks(t, otherIssueID, agentID); n != 0 {
		t.Fatalf("conflicting id queued %d runs on the other issue, want 0", n)
	}
}
