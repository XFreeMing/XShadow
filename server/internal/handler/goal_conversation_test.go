package handler

import (
	"context"
	"net/http"
	"slices"
	"testing"

	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestGoalConversationReusesDedicatedSessionAndRecordsLinkedTasks(t *testing.T) {
	ctx := context.Background()
	chief := dbfx.Agent(t, "Test chief", testRuntimeID)
	dbfx.Squad(t, "军团统筹", chief)
	ordinary := dbfx.ChatSession(t, chief)

	open := func() ChatSessionResponse {
		req := newRequest(http.MethodPost, "/api/goals/conversation", nil)
		req = req.WithContext(middleware.SetMemberContext(req.Context(), testWorkspaceID, db.Member{Role: "owner"}))
		var session ChatSessionResponse
		testutil.Call(t, testHandler.OpenGoalConversation, req).Want(http.StatusOK).JSON(&session)
		return session
	}
	session := open()
	t.Cleanup(func() { testPool.Exec(ctx, "DELETE FROM chat_session WHERE id = $1", session.ID) })
	if !session.IsGoalConversation || session.ID == ordinary || session.AgentID != chief || open().ID != session.ID {
		t.Fatalf("goal entry did not reopen a dedicated chief session: %+v", session)
	}
	project := dbfx.Project(t, "Goal project")
	issueID := dbfx.Issue(t, "Deliver the goal", testutil.Cols{"project_id": project})
	secondIssueID := dbfx.Issue(t, "Verify the goal", testutil.Cols{"project_id": project})
	otherProject := dbfx.Project(t, "Other project")
	otherIssueID := dbfx.Issue(t, "Unrelated project task", testutil.Cols{"project_id": otherProject})
	turn := func(sessionID string, inputID string) (string, string) {
		taskID := dbfx.Task(t, chief, testutil.Cols{
			"chat_session_id": sessionID, "status": "running", "runtime_id": testRuntimeID,
			"originator_user_id": testUserID, "accountable_user_id": testUserID, "originator_source": "direct_human",
		})
		if inputID == "" {
			inputID = taskID
		}
		dbfx.Exec(t, "UPDATE agent_task_queue SET chat_input_task_id = $1 WHERE id = $2", inputID, taskID)
		return taskID, inputID
	}
	record := func(taskID string, issues []string) *testutil.Response {
		req := newRequest(http.MethodPost, "/api/goals", map[string]any{
			"title": "Ship usable goal intake", "intent": "Record a clear outcome",
			"scope": "Goals page", "acceptance_criteria": "A saved goal links its delivery task",
			"issue_ids": issues,
		})
		if taskID != "" {
			req.Header.Set("X-Actor-Source", "task_token")
			req.Header.Set("X-Task-ID", taskID)
			req.Header.Set("X-Agent-ID", chief)
		}
		return testutil.Call(t, testHandler.CreateGoal, req)
	}

	record("", nil).Want(http.StatusForbidden)
	ordinaryTask, _ := turn(ordinary, "")
	record(ordinaryTask, nil).Want(http.StatusForbidden)

	taskID, inputID := turn(session.ID, "")
	t.Cleanup(func() {
		testPool.Exec(ctx, "DELETE FROM goal_issue WHERE goal_id IN (SELECT id FROM goal WHERE source_chat_input_id = $1)", inputID)
		testPool.Exec(ctx, "DELETE FROM goal WHERE source_chat_input_id = $1", inputID)
	})
	record(taskID, []string{issueID, otherIssueID}).Want(http.StatusConflict)
	var count int
	dbfx.QueryRow(t, "SELECT count(*) FROM goal WHERE source_chat_input_id = $1", inputID).Scan(&count)
	if count != 0 {
		t.Fatal("task association failure left a goal behind")
	}
	var saved GoalResponse
	record(taskID, []string{issueID, secondIssueID, issueID}).Want(http.StatusCreated).JSON(&saved)
	if saved.ProjectID == nil || *saved.ProjectID != project || saved.Status != "planned" || saved.CreatorID != chief {
		t.Fatalf("recorded goal has wrong project, status, or creator: %+v", saved)
	}
	code, issues, total := listGoalIssuesViaAPI(t, testUserID, saved.ID)
	if code != http.StatusOK || total != 2 || len(issues) != 2 ||
		!slices.ContainsFunc(issues, func(issue IssueResponse) bool { return issue.ID == issueID }) ||
		!slices.ContainsFunc(issues, func(issue IssueResponse) bool { return issue.ID == secondIssueID }) {
		t.Fatalf("linked task response = %d (%d): %+v", code, total, issues)
	}
	retryTask, _ := turn(session.ID, inputID)
	var replay GoalResponse
	record(retryTask, []string{issueID}).Want(http.StatusOK).JSON(&replay)
	if replay.ID != saved.ID {
		t.Fatalf("retry created a second goal: %s != %s", replay.ID, saved.ID)
	}

	nextTask, nextInputID := turn(session.ID, "")
	record(nextTask, []string{issueID}).Want(http.StatusConflict)
	dbfx.QueryRow(t, "SELECT count(*) FROM goal WHERE source_chat_input_id = $1", nextInputID).Scan(&count)
	if count != 0 {
		t.Fatal("task already linked to another goal was moved or left a duplicate goal")
	}
}
