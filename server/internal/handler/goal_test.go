package handler

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Goal API coverage for DEV-5. The tests exercise the HTTP handlers against
// the real database so the relation cleanup lives where it actually runs:
// shared UpdateIssue, issue delete, and project delete.

func goalRequest(method, path, userID string, body any, params map[string]string) *http.Request {
	req := asUser(newRequest(method, path, body), userID)
	rctx := chi.NewRouteContext()
	for key, value := range params {
		rctx.URLParams.Add(key, value)
	}
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

func createGoalViaAPI(t *testing.T, userID string, body map[string]any) GoalResponse {
	t.Helper()
	var resp GoalResponse
	testutil.Call(t, testHandler.CreateGoal,
		goalRequest(http.MethodPost, "/api/goals", userID, body, nil)).
		Want(http.StatusCreated).JSON(&resp)
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM goal_issue WHERE workspace_id = $1 AND goal_id = $2`, testWorkspaceID, resp.ID)
		testPool.Exec(context.Background(), `DELETE FROM goal WHERE id = $1`, resp.ID)
	})
	return resp
}

func getGoalViaAPI(t *testing.T, userID, goalID string) (int, GoalResponse) {
	t.Helper()
	rec := testutil.Call(t, testHandler.GetGoal,
		goalRequest(http.MethodGet, "/api/goals/"+goalID, userID, nil, map[string]string{"id": goalID}))
	var resp GoalResponse
	if rec.Code == http.StatusOK {
		rec.JSON(&resp)
	}
	return rec.Code, resp
}

func updateGoalViaAPI(t *testing.T, userID, goalID string, body map[string]any) (int, GoalResponse) {
	t.Helper()
	rec := testutil.Call(t, testHandler.UpdateGoal,
		goalRequest(http.MethodPut, "/api/goals/"+goalID, userID, body, map[string]string{"id": goalID}))
	var resp GoalResponse
	if rec.Code == http.StatusOK {
		rec.JSON(&resp)
	}
	return rec.Code, resp
}

func linkGoalIssueViaAPI(t *testing.T, userID, goalID, issueID string) int {
	t.Helper()
	return testutil.Call(t, testHandler.LinkGoalIssue, goalRequest(http.MethodPut,
		"/api/goals/"+goalID+"/issues/"+issueID, userID, nil,
		map[string]string{"id": goalID, "issueId": issueID})).Code
}

func unlinkGoalIssueViaAPI(t *testing.T, userID, goalID, issueID string) int {
	t.Helper()
	return testutil.Call(t, testHandler.UnlinkGoalIssue, goalRequest(http.MethodDelete,
		"/api/goals/"+goalID+"/issues/"+issueID, userID, nil,
		map[string]string{"id": goalID, "issueId": issueID})).Code
}

func listGoalsViaAPI(t *testing.T, userID, query string) (int, []GoalResponse, int) {
	t.Helper()
	rec := testutil.Call(t, testHandler.ListGoals,
		asUser(newRequest(http.MethodGet, "/api/goals?"+query, nil), userID))
	var resp struct {
		Goals []GoalResponse `json:"goals"`
		Total int            `json:"total"`
	}
	rec.JSON(&resp)
	return rec.Code, resp.Goals, resp.Total
}

func listGoalIssuesViaAPI(t *testing.T, userID, goalID string) (int, []IssueResponse, int) {
	t.Helper()
	rec := testutil.Call(t, testHandler.ListGoalIssues,
		goalRequest(http.MethodGet, "/api/goals/"+goalID+"/issues", userID, nil, map[string]string{"id": goalID}))
	var resp struct {
		Issues []IssueResponse `json:"issues"`
		Total  int             `json:"total"`
	}
	rec.JSON(&resp)
	return rec.Code, resp.Issues, resp.Total
}

func goalLinkRows(t *testing.T, goalID string) int {
	t.Helper()
	var n int
	dbfx.QueryRow(t, `SELECT count(*) FROM goal_issue WHERE workspace_id = $1 AND goal_id = $2`, testWorkspaceID, goalID).Scan(&n)
	return n
}

func goalRows(t *testing.T, goalID string) int {
	t.Helper()
	var n int
	dbfx.QueryRow(t, `SELECT count(*) FROM goal WHERE id = $1 AND workspace_id = $2`, goalID, testWorkspaceID).Scan(&n)
	return n
}

// goalChangedCounter counts empty-payload goal:changed broadcasts.
func goalChangedCounter(t *testing.T) func() int {
	t.Helper()
	var mu sync.Mutex
	count := 0
	testHandler.Bus.Subscribe(protocol.EventGoalChanged, func(e events.Event) {
		if e.Payload == nil {
			t.Errorf("goal:changed payload = %#v, want empty object", e.Payload)
		}
		if payload, ok := e.Payload.(map[string]any); !ok || len(payload) != 0 {
			t.Errorf("goal:changed payload = %#v, want an empty object", e.Payload)
		}
		mu.Lock()
		count++
		mu.Unlock()
	})
	return func() int {
		mu.Lock()
		defer mu.Unlock()
		return count
	}
}

func TestGoalLifecycleSurvivesIssueWrites(t *testing.T) {
	countGoalChanged := goalChangedCounter(t)
	goal := createGoalViaAPI(t, testUserID, map[string]any{
		"title":               "Ship goal management",
		"intent":              "let a user define a deliverable",
		"acceptance_criteria": "goal survives task transitions",
	})
	if goal.Status != "planned" {
		t.Fatalf("new goal status = %q, want planned", goal.Status)
	}
	issueID := dbfx.Issue(t, "goal lifecycle task", testutil.Cols{"status": "todo"})

	if code := linkGoalIssueViaAPI(t, testUserID, goal.ID, issueID); code != http.StatusNoContent {
		t.Fatalf("link issue = %d, want 204", code)
	}
	if n := goalLinkRows(t, goal.ID); n != 1 {
		t.Fatalf("link rows = %d, want 1", n)
	}
	_, issues, total := listGoalIssuesViaAPI(t, testUserID, goal.ID)
	if total != 1 || len(issues) != 1 || issues[0].ID != issueID {
		t.Fatalf("goal issues = %+v total=%d, want one linked issue", issues, total)
	}

	// Task transitions never touch the goal's own lifecycle.
	for _, status := range []string{"done", "cancelled"} {
		testutil.Call(t, testHandler.UpdateIssue,
			goalRequest(http.MethodPatch, "/api/issues/"+issueID, testUserID,
				map[string]any{"status": status}, map[string]string{"id": issueID})).
			Want(http.StatusOK)
		if code, got := getGoalViaAPI(t, testUserID, goal.ID); code != http.StatusOK || got.Status != "planned" {
			t.Fatalf("after task %s: GET goal = %d status=%q, want 200 planned", status, code, got.Status)
		}
	}

	// Deleting the task clears the link and leaves the goal intact.
	testutil.Call(t, testHandler.DeleteIssue,
		goalRequest(http.MethodDelete, "/api/issues/"+issueID, testUserID, nil, map[string]string{"id": issueID})).
		Want(http.StatusNoContent)
	if n := goalLinkRows(t, goal.ID); n != 0 {
		t.Fatalf("link rows after issue delete = %d, want 0", n)
	}
	if n := goalRows(t, goal.ID); n != 1 {
		t.Fatalf("goal rows after issue delete = %d, want 1", n)
	}
	if code, got := getGoalViaAPI(t, testUserID, goal.ID); code != http.StatusOK || got.Status != "planned" {
		t.Fatalf("goal after issue delete = %d status=%q, want 200 planned", code, got.Status)
	}
	if countGoalChanged() < 2 {
		t.Fatalf("goal:changed broadcasts = %d, want create + link at least", countGoalChanged())
	}
}

func TestGoalProjectMoveInsideIssueUpdateUnlinks(t *testing.T) {
	p1 := dbfx.Project(t, "goal move source", testutil.Cols{"visibility": "workspace"})
	p2 := dbfx.Project(t, "goal move target", testutil.Cols{"visibility": "workspace"})
	issueID := dbfx.Issue(t, "goal move task", testutil.Cols{"project_id": p1})
	goal := createGoalViaAPI(t, testUserID, map[string]any{"title": "move with task", "project_id": p1})
	if code := linkGoalIssueViaAPI(t, testUserID, goal.ID, issueID); code != http.StatusNoContent {
		t.Fatalf("link issue = %d, want 204", code)
	}

	// Single update: the shared UpdateIssue query must remove the link when the
	// task's project actually changes, without touching the goal.
	testutil.Call(t, testHandler.UpdateIssue,
		goalRequest(http.MethodPatch, "/api/issues/"+issueID, testUserID,
			map[string]any{"project_id": p2}, map[string]string{"id": issueID})).
		Want(http.StatusOK)
	if n := goalLinkRows(t, goal.ID); n != 0 {
		t.Fatalf("link rows after project move = %d, want 0", n)
	}
	if n := goalRows(t, goal.ID); n != 1 {
		t.Fatalf("goal rows after project move = %d, want 1", n)
	}

	// A link into a project the caller cannot see answers 404.
	if code := linkGoalIssueViaAPI(t, testUserID, goal.ID, issueID); code != http.StatusConflict {
		t.Fatalf("relink after move = %d, want 409 (goal and issue now differ)", code)
	}
}

func TestGoalBatchUpdateAndMoveUnlink(t *testing.T) {
	p1 := dbfx.Project(t, "goal batch source", testutil.Cols{"visibility": "workspace"})
	p2 := dbfx.Project(t, "goal batch target", testutil.Cols{"visibility": "workspace"})
	goal := createGoalViaAPI(t, testUserID, map[string]any{"title": "batch unlink", "project_id": p1})
	batchIssue := dbfx.Issue(t, "goal batch task", testutil.Cols{"project_id": p1})
	if code := linkGoalIssueViaAPI(t, testUserID, goal.ID, batchIssue); code != http.StatusNoContent {
		t.Fatalf("link issue = %d, want 204", code)
	}
	testutil.Call(t, testHandler.BatchUpdateIssues,
		newRequest(http.MethodPost, "/api/issues/batch", map[string]any{
			"issue_ids": []string{batchIssue},
			"updates":   map[string]any{"project_id": p2},
		})).Want(http.StatusOK)
	if n := goalLinkRows(t, goal.ID); n != 0 {
		t.Fatalf("link rows after batch project change = %d, want 0", n)
	}

	// Drag/move goes through the same persistence boundary.
	moveIssue := dbfx.Issue(t, "goal move handler task", testutil.Cols{"project_id": p1})
	newGoal := createGoalViaAPI(t, testUserID, map[string]any{"title": "move unlink", "project_id": p1})
	if code := linkGoalIssueViaAPI(t, testUserID, newGoal.ID, moveIssue); code != http.StatusNoContent {
		t.Fatalf("link move issue = %d, want 204", code)
	}
	testutil.Call(t, testHandler.MoveIssue,
		goalRequest(http.MethodPost, "/api/issues/"+moveIssue+"/move", testUserID, map[string]any{
			"project_id": p2,
			"before_id":  nil,
			"after_id":   nil,
		}, map[string]string{"id": moveIssue})).Want(http.StatusOK)
	if n := goalLinkRows(t, newGoal.ID); n != 0 {
		t.Fatalf("link rows after move = %d, want 0", n)
	}
}

func TestGoalLinkIdempotencyAndConflicts(t *testing.T) {
	goalA := createGoalViaAPI(t, testUserID, map[string]any{"title": "link idempotent"})
	goalB := createGoalViaAPI(t, testUserID, map[string]any{"title": "link conflict"})
	issueID := dbfx.Issue(t, "goal idempotent task")

	if code := linkGoalIssueViaAPI(t, testUserID, goalA.ID, issueID); code != http.StatusNoContent {
		t.Fatalf("first link = %d, want 204", code)
	}
	if code := linkGoalIssueViaAPI(t, testUserID, goalA.ID, issueID); code != http.StatusNoContent {
		t.Fatalf("repeat link = %d, want 204", code)
	}
	if n := goalLinkRows(t, goalA.ID); n != 1 {
		t.Fatalf("link rows after repeat = %d, want 1", n)
	}
	if code := linkGoalIssueViaAPI(t, testUserID, goalB.ID, issueID); code != http.StatusConflict {
		t.Fatalf("link to second goal = %d, want 409", code)
	}
	if n := goalLinkRows(t, goalB.ID); n != 0 {
		t.Fatalf("second goal link rows = %d, want 0", n)
	}

	// Unlink is idempotent too, and releases the task for another goal.
	if code := unlinkGoalIssueViaAPI(t, testUserID, goalA.ID, issueID); code != http.StatusNoContent {
		t.Fatalf("unlink = %d, want 204", code)
	}
	if code := unlinkGoalIssueViaAPI(t, testUserID, goalA.ID, issueID); code != http.StatusNoContent {
		t.Fatalf("repeat unlink = %d, want 204", code)
	}
	if code := linkGoalIssueViaAPI(t, testUserID, goalB.ID, issueID); code != http.StatusNoContent {
		t.Fatalf("link after unlink = %d, want 204", code)
	}

	// Same-workspace but different project is a conflict, not an implicit move.
	p := dbfx.Project(t, "goal mismatch project", testutil.Cols{"visibility": "workspace"})
	projectIssue := dbfx.Issue(t, "goal mismatch task", testutil.Cols{"project_id": p})
	if code := linkGoalIssueViaAPI(t, testUserID, goalA.ID, projectIssue); code != http.StatusConflict {
		t.Fatalf("cross-project link = %d, want 409", code)
	}
	if n := goalLinkRows(t, goalA.ID); n != 0 {
		t.Fatalf("cross-project link rows = %d, want 0", n)
	}
}

func TestGoalConcurrentLinkRace(t *testing.T) {
	goalA := createGoalViaAPI(t, testUserID, map[string]any{"title": "race goal a"})
	goalB := createGoalViaAPI(t, testUserID, map[string]any{"title": "race goal b"})
	issueID := dbfx.Issue(t, "goal race task")

	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for _, goalID := range []string{goalA.ID, goalB.ID} {
		wg.Add(1)
		go func(goalID string) {
			defer wg.Done()
			req := goalRequest(http.MethodPut,
				"/api/goals/"+goalID+"/issues/"+issueID, testUserID, nil,
				map[string]string{"id": goalID, "issueId": issueID})
			rec := testutil.Call(t, testHandler.LinkGoalIssue, req)
			codes <- rec.Code
		}(goalID)
	}
	wg.Wait()
	close(codes)
	got := map[int]int{}
	for code := range codes {
		got[code]++
	}
	if got[http.StatusNoContent] != 1 || got[http.StatusConflict] != 1 {
		t.Fatalf("concurrent link codes = %v, want one 204 and one 409", got)
	}
	var links int
	dbfx.QueryRow(t, `SELECT count(*) FROM goal_issue WHERE workspace_id = $1 AND issue_id = $2`, testWorkspaceID, issueID).Scan(&links)
	if links != 1 {
		t.Fatalf("goal_issue rows for the raced task = %d, want 1", links)
	}
}

func TestGoalProjectChangeRequiresUnlink(t *testing.T) {
	p1 := dbfx.Project(t, "goal guard source", testutil.Cols{"visibility": "workspace"})
	p2 := dbfx.Project(t, "goal guard target", testutil.Cols{"visibility": "workspace"})
	issueID := dbfx.Issue(t, "goal guard task", testutil.Cols{"project_id": p1})
	goal := createGoalViaAPI(t, testUserID, map[string]any{"title": "guarded move", "project_id": p1})
	if code := linkGoalIssueViaAPI(t, testUserID, goal.ID, issueID); code != http.StatusNoContent {
		t.Fatalf("link = %d, want 204", code)
	}

	if code, _ := updateGoalViaAPI(t, testUserID, goal.ID, map[string]any{"project_id": p2}); code != http.StatusConflict {
		t.Fatalf("move linked goal = %d, want 409", code)
	}
	// The rejected write rolled back: still in p1, and the link is intact.
	code, got := getGoalViaAPI(t, testUserID, goal.ID)
	if code != http.StatusOK || got.ProjectID == nil || *got.ProjectID != p1 {
		t.Fatalf("goal after rejected move = %d project=%v, want p1", code, got.ProjectID)
	}
	if n := goalLinkRows(t, goal.ID); n != 1 {
		t.Fatalf("link rows after rejected move = %d, want 1", n)
	}

	if code := unlinkGoalIssueViaAPI(t, testUserID, goal.ID, issueID); code != http.StatusNoContent {
		t.Fatalf("unlink = %d, want 204", code)
	}
	if code, got := updateGoalViaAPI(t, testUserID, goal.ID, map[string]any{"project_id": p2}); code != http.StatusOK {
		t.Fatalf("move unlinked goal = %d, want 200", code)
	} else if got.ProjectID == nil || *got.ProjectID != p2 {
		t.Fatalf("goal project = %v, want p2", got.ProjectID)
	}

	// Explicit null clears the project; an omitted key keeps it.
	if code, got := updateGoalViaAPI(t, testUserID, goal.ID, map[string]any{"project_id": nil}); code != http.StatusOK || got.ProjectID != nil {
		t.Fatalf("clear project = %d project=%v, want 200 null", code, got.ProjectID)
	}
}

func TestGoalOwnerIsAssignmentOnly(t *testing.T) {
	agentID := createHandlerTestAgent(t, "goal owner agent "+t.Name(), nil)
	var before int
	dbfx.QueryRow(t, `SELECT count(*) FROM agent_task_queue q JOIN agent a ON a.id = q.agent_id WHERE a.workspace_id = $1`, testWorkspaceID).Scan(&before)

	goal := createGoalViaAPI(t, testUserID, map[string]any{
		"title":      "agent-owned goal",
		"owner_type": "agent",
		"owner_id":   agentID,
	})
	if goal.OwnerType == nil || *goal.OwnerType != "agent" || goal.OwnerID == nil || *goal.OwnerID != agentID {
		t.Fatalf("owner = %v/%v, want agent/%s", goal.OwnerType, goal.OwnerID, agentID)
	}
	var afterCreate int
	dbfx.QueryRow(t, `SELECT count(*) FROM agent_task_queue q JOIN agent a ON a.id = q.agent_id WHERE a.workspace_id = $1`, testWorkspaceID).Scan(&afterCreate)
	if afterCreate != before {
		t.Fatalf("agent_task_queue rows after owner create = %d, want %d (no run)", afterCreate, before)
	}

	if code, got := updateGoalViaAPI(t, testUserID, goal.ID, map[string]any{"owner_type": "member", "owner_id": testUserID}); code != http.StatusOK {
		t.Fatalf("reassign owner = %d, want 200", code)
	} else if got.OwnerType == nil || *got.OwnerType != "member" {
		t.Fatalf("owner_type = %v, want member", got.OwnerType)
	}
	var afterPatch int
	dbfx.QueryRow(t, `SELECT count(*) FROM agent_task_queue q JOIN agent a ON a.id = q.agent_id WHERE a.workspace_id = $1`, testWorkspaceID).Scan(&afterPatch)
	if afterPatch != before {
		t.Fatalf("agent_task_queue rows after owner update = %d, want %d (no run)", afterPatch, before)
	}

	// Half an owner pair is rejected, as is an archived or foreign target.
	if code, _ := updateGoalViaAPI(t, testUserID, goal.ID, map[string]any{"owner_type": "agent"}); code != http.StatusBadRequest {
		t.Fatalf("half owner pair = %d, want 400", code)
	}
	if code, _ := updateGoalViaAPI(t, testUserID, goal.ID, map[string]any{"owner_type": "member", "owner_id": ""}); code != http.StatusBadRequest {
		t.Fatalf("empty owner id = %d, want 400", code)
	}
}

func TestGoalPrivateProjectVisibility(t *testing.T) {
	f := newPrivateProjectFixture(t)
	goal := createGoalViaAPI(t, f.insider, map[string]any{
		"title":      "private project goal",
		"project_id": f.projectID,
	})

	// Insider and the workspace owner see it; an outsider cannot.
	if code, _ := getGoalViaAPI(t, f.insider, goal.ID); code != http.StatusOK {
		t.Fatalf("insider GET = %d, want 200", code)
	}
	if code, _ := getGoalViaAPI(t, testUserID, goal.ID); code != http.StatusOK {
		t.Fatalf("owner GET = %d, want 200", code)
	}
	if code, _ := getGoalViaAPI(t, f.outsider, goal.ID); code != http.StatusNotFound {
		t.Fatalf("outsider GET = %d, want 404", code)
	}
	if code, _, _ := listGoalsViaAPI(t, f.outsider, "limit=100"); code != http.StatusOK {
		t.Fatalf("outsider list = %d, want 200", code)
	} else if _, goals, total := listGoalsViaAPI(t, f.outsider, "limit=100"); total != len(goals) {
		t.Fatalf("outsider total = %d, page = %d", total, len(goals))
	} else {
		for _, g := range goals {
			if g.ID == goal.ID {
				t.Fatal("outsider sees a goal from a private project")
			}
		}
	}

	// A private project the caller cannot see answers 404 on create, as does
	// linking a task from it.
	if code := testutil.Call(t, testHandler.CreateGoal,
		goalRequest(http.MethodPost, "/api/goals", f.outsider, map[string]any{
			"title":      "outsider goal",
			"project_id": f.projectID,
		}, nil)).Code; code != http.StatusNotFound {
		t.Fatalf("outsider create in private project = %d, want 404", code)
	}
	visibleGoal := createGoalViaAPI(t, testUserID, map[string]any{"title": "visible goal"})
	if code := linkGoalIssueViaAPI(t, f.outsider, visibleGoal.ID, f.secretIssue); code != http.StatusNotFound {
		t.Fatalf("outsider link secret issue = %d, want 404", code)
	}
	// Insider can link within the private project.
	if code := linkGoalIssueViaAPI(t, f.insider, goal.ID, f.secretIssue); code != http.StatusNoContent {
		t.Fatalf("insider link = %d, want 204", code)
	}
	if code, issues, total := listGoalIssuesViaAPI(t, f.insider, goal.ID); code != http.StatusOK || total != 1 || len(issues) != 1 {
		t.Fatalf("insider goal issues = %d total=%d len=%d, want one", code, total, len(issues))
	}
}

func TestGoalDeletionPermissionsAndProjectCascade(t *testing.T) {
	creator := createPlainMember(t, fmt.Sprintf("goal-creator-%d@multica.test", time.Now().UnixNano()))
	other := createPlainMember(t, fmt.Sprintf("goal-other-%d@multica.test", time.Now().UnixNano()))

	goal := createGoalViaAPI(t, creator, map[string]any{"title": "creator owned goal"})
	if code := testutil.Call(t, testHandler.DeleteGoal,
		goalRequest(http.MethodDelete, "/api/goals/"+goal.ID, other, nil, map[string]string{"id": goal.ID})).Code; code != http.StatusForbidden {
		t.Fatalf("non-creator delete = %d, want 403", code)
	}
	if n := goalRows(t, goal.ID); n != 1 {
		t.Fatalf("goal rows after refused delete = %d, want 1", n)
	}
	if code := testutil.Call(t, testHandler.DeleteGoal,
		goalRequest(http.MethodDelete, "/api/goals/"+goal.ID, creator, nil, map[string]string{"id": goal.ID})).Code; code != http.StatusNoContent {
		t.Fatalf("creator delete = %d, want 204", code)
	}
	if n := goalRows(t, goal.ID); n != 0 {
		t.Fatalf("goal rows after delete = %d, want 0", n)
	}

	// Project deletion removes the project's goals and their links in one
	// transaction, leaving the linked task intact.
	p := dbfx.Project(t, "goal cascade project", testutil.Cols{"visibility": "workspace"})
	cascadeIssue := dbfx.Issue(t, "goal cascade task", testutil.Cols{"project_id": p})
	cascadeGoal := createGoalViaAPI(t, testUserID, map[string]any{"title": "cascade goal", "project_id": p})
	if code := linkGoalIssueViaAPI(t, testUserID, cascadeGoal.ID, cascadeIssue); code != http.StatusNoContent {
		t.Fatalf("link cascade issue = %d, want 204", code)
	}
	testutil.Call(t, testHandler.DeleteProject,
		goalRequest(http.MethodDelete, "/api/projects/"+p, testUserID, nil, map[string]string{"id": p})).
		Want(http.StatusNoContent)
	if n := goalRows(t, cascadeGoal.ID); n != 0 {
		t.Fatalf("goal rows after project delete = %d, want 0", n)
	}
	if n := goalLinkRows(t, cascadeGoal.ID); n != 0 {
		t.Fatalf("goal link rows after project delete = %d, want 0", n)
	}
	var issueExists bool
	dbfx.QueryRow(t, `SELECT EXISTS(SELECT 1 FROM issue WHERE id = $1)`, cascadeIssue).Scan(&issueExists)
	if !issueExists {
		t.Fatal("project delete removed a linked task")
	}
}

func TestGoalRevisionConflictDoesNotUnlink(t *testing.T) {
	p1 := dbfx.Project(t, "goal revision source", testutil.Cols{"visibility": "workspace"})
	p2 := dbfx.Project(t, "goal revision target", testutil.Cols{"visibility": "workspace"})
	issueID := dbfx.Issue(t, "goal revision task", testutil.Cols{"project_id": p1})
	goal := createGoalViaAPI(t, testUserID, map[string]any{"title": "revision guard", "project_id": p1})
	if code := linkGoalIssueViaAPI(t, testUserID, goal.ID, issueID); code != http.StatusNoContent {
		t.Fatalf("link = %d, want 204", code)
	}

	var revision int64
	dbfx.QueryRow(t, `SELECT revision FROM issue WHERE id = $1`, issueID).Scan(&revision)
	// A stale revision is refused; the statement must not leave the goal link
	// deleted behind the 409.
	testutil.Call(t, testHandler.UpdateIssue,
		goalRequest(http.MethodPatch, "/api/issues/"+issueID, testUserID, map[string]any{
			"expected_revision": revision + 1,
			"project_id":        p2,
		}, map[string]string{"id": issueID})).Want(http.StatusConflict)
	if n := goalLinkRows(t, goal.ID); n != 1 {
		t.Fatalf("link rows after revision conflict = %d, want 1", n)
	}
	var projectID string
	dbfx.QueryRow(t, `SELECT project_id::text FROM issue WHERE id = $1`, issueID).Scan(&projectID)
	if projectID != p1 {
		t.Fatalf("issue project after revision conflict = %s, want %s", projectID, p1)
	}

	testutil.Call(t, testHandler.UpdateIssue,
		goalRequest(http.MethodPatch, "/api/issues/"+issueID, testUserID, map[string]any{
			"expected_revision": revision,
			"project_id":        p2,
		}, map[string]string{"id": issueID})).Want(http.StatusOK)
	if n := goalLinkRows(t, goal.ID); n != 0 {
		t.Fatalf("link rows after committed move = %d, want 0", n)
	}
}

func TestGoalProjectClearUnlinks(t *testing.T) {
	p := dbfx.Project(t, "goal clear project", testutil.Cols{"visibility": "workspace"})
	issueID := dbfx.Issue(t, "goal clear task", testutil.Cols{"project_id": p})
	goal := createGoalViaAPI(t, testUserID, map[string]any{"title": "clear project link", "project_id": p})
	if code := linkGoalIssueViaAPI(t, testUserID, goal.ID, issueID); code != http.StatusNoContent {
		t.Fatalf("link = %d, want 204", code)
	}

	// Clearing project_id sends a null pointer; the shared statement compares
	// the locked old/new values, so the link still goes.
	testutil.Call(t, testHandler.UpdateIssue,
		goalRequest(http.MethodPatch, "/api/issues/"+issueID, testUserID,
			map[string]any{"project_id": nil}, map[string]string{"id": issueID})).Want(http.StatusOK)
	if n := goalLinkRows(t, goal.ID); n != 0 {
		t.Fatalf("link rows after clearing the issue project = %d, want 0", n)
	}
	if n := goalRows(t, goal.ID); n != 1 {
		t.Fatalf("goal rows after clearing the issue project = %d, want 1", n)
	}
	// The goal stays in p, so the now-projectless task is in another scope.
	if code := linkGoalIssueViaAPI(t, testUserID, goal.ID, issueID); code != http.StatusConflict {
		t.Fatalf("relink after clear = %d, want 409", code)
	}
}

func TestGoalListPaginationAndFilters(t *testing.T) {
	p := dbfx.Project(t, "goal paging project", testutil.Cols{"visibility": "workspace"})
	ids := []string{
		createGoalViaAPI(t, testUserID, map[string]any{"title": "paging one"}).ID,
		createGoalViaAPI(t, testUserID, map[string]any{"title": "paging two"}).ID,
		createGoalViaAPI(t, testUserID, map[string]any{"title": "paging three", "project_id": p}).ID,
	}
	if _, goals, _ := listGoalsViaAPI(t, testUserID, "limit=100"); len(goals) < 3 {
		t.Fatalf("list goals = %d, want at least the three created", len(goals))
	}
	code, standalone, total := listGoalsViaAPI(t, testUserID, "project_id=none&limit=100")
	if code != http.StatusOK {
		t.Fatalf("standalone list = %d, want 200", code)
	}
	if total != len(standalone) {
		t.Fatalf("standalone total = %d, page = %d", total, len(standalone))
	}
	seen := map[string]bool{}
	for _, g := range standalone {
		if g.ProjectID != nil {
			t.Fatalf("standalone list returned a project goal: %v", g.ProjectID)
		}
		seen[g.ID] = true
	}
	if !seen[ids[0]] || !seen[ids[1]] || seen[ids[2]] {
		t.Fatalf("standalone list membership = %v, want the two projectless goals", seen)
	}

	page, _, pageTotal := listGoalsViaAPI(t, testUserID, "project_id="+p+"&limit=1")
	if page != http.StatusOK || pageTotal < 1 {
		t.Fatalf("project filter list = %d total=%d, want at least one", page, pageTotal)
	}
	if _, goals, _ := listGoalsViaAPI(t, testUserID, "project_id="+p+"&limit=1"); len(goals) != 1 || goals[0].ID != ids[2] {
		t.Fatalf("project filter page = %+v, want %s", goals, ids[2])
	}

	if code, _, _ := listGoalsViaAPI(t, testUserID, "status=bogus"); code != http.StatusBadRequest {
		t.Fatalf("invalid status = %d, want 400", code)
	}
	if code, _, _ := listGoalsViaAPI(t, testUserID, "status=completed"); code != http.StatusOK {
		t.Fatalf("valid status filter = %d, want 200", code)
	}
}
