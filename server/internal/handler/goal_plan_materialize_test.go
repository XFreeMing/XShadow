package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// Execution of a saved plan: `binding.type = "new"` items become one issue
// each, keyed by the stable plan item id. These tests exercise the real
// endpoint and database constraints so a replayed trigger is shown to converge
// on the persisted binding instead of forking work.

func materializePlanRaw(t *testing.T, goalID string, body any) *testutil.Response {
	t.Helper()
	req := newRequest("POST", "/api/goals/"+goalID+"/plan/materialize", body)
	return testutil.Call(t, testHandler.MaterializeGoalPlan, withURLParam(req, "id", goalID))
}

func materializePlan(t *testing.T, goalID string, body any) MaterializeGoalPlanResponse {
	t.Helper()
	var resp MaterializeGoalPlanResponse
	testutil.Call(t, testHandler.MaterializeGoalPlan,
		withURLParam(newRequest("POST", "/api/goals/"+goalID+"/plan/materialize", body), "id", goalID)).
		Want(http.StatusOK).JSON(&resp)
	return resp
}

func TestMaterializeGoalPlanIsIdempotentPerItem(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	projectID := createProjectForPlan(t, "Materialize project "+t.Name())
	goal := planTestGoal(t, projectID)
	agentID := closureTestAgent(t, "materialize agent", "deepseek-v4.1-flash")

	itemA := "018f3a9e-1111-7c2b-9f1e-0000000000a1"
	itemB := "018f3a9e-1111-7c2b-9f1e-0000000000a2"
	code, saved := putPlan(t, goal.ID, map[string]any{
		"expected_revision": 0,
		"items": []map[string]any{
			{"id": itemA, "title": "deliver the first node", "outcome": "A exists", "acceptance_criteria": "A is usable", "binding": map[string]any{"type": "new"}},
			{"id": itemB, "title": "deliver the second node", "outcome": "B builds on A", "acceptance_criteria": "B is usable", "binding": map[string]any{"type": "new"}},
		},
		"edges": []map[string]any{{
			"dependent_item_id": itemB, "prerequisite_item_id": itemA, "reason": "B needs A's interface",
		}},
	})
	if code != http.StatusOK {
		t.Fatalf("save plan = %d, want 200", code)
	}
	if saved.Revision != 1 {
		t.Fatalf("plan revision = %d, want 1", saved.Revision)
	}

	body := map[string]any{"items": []map[string]any{
		{"item_id": itemA, "assignee_type": "agent", "assignee_id": agentID},
		{"item_id": itemB, "assignee_type": "agent", "assignee_id": agentID},
	}}
	first := materializePlan(t, goal.ID, body)
	if first.GoalID != goal.ID || first.PlanRevision != 1 || len(first.Items) != 2 {
		t.Fatalf("first materialize = %+v", first)
	}
	issues := map[string]string{}
	for _, item := range first.Items {
		if !item.Created || item.IssueID == "" || item.BindingType != "new" {
			t.Fatalf("first materialize item = %+v, want a newly created issue", item)
		}
		issues[item.ItemID] = item.IssueID
	}
	if issues[itemA] == issues[itemB] {
		t.Fatal("two plan items materialized into one issue")
	}
	// A has no prerequisite; B waits for A and must be reported as not ready.
	for _, item := range first.Items {
		if item.ItemID == itemA && !item.Dependency.Ready {
			t.Fatalf("independent item A reported not ready: %+v", item.Dependency)
		}
		if item.ItemID == itemB {
			if item.Dependency.Ready {
				t.Fatal("dependent item B reported ready while A is still todo")
			}
			if item.Dependency.Reason != "dependency_not_done" {
				t.Fatalf("B dependency reason = %q, want dependency_not_done", item.Dependency.Reason)
			}
		}
	}

	var tasksAfterFirst int
	dbfx.QueryRow(t, `SELECT count(*) FROM agent_task_queue WHERE issue_id = ANY($1::uuid[])`,
		[]string{issues[itemA], issues[itemB]}).Scan(&tasksAfterFirst)

	// The same trigger again: same issues, created=false, no extra binding or
	// task. This is the retry a scheduler or a duplicated event produces.
	second := materializePlan(t, goal.ID, body)
	for _, item := range second.Items {
		if item.Created {
			t.Fatalf("replayed materialize created a second issue for %s: %+v", item.ItemID, item)
		}
		if item.IssueID != issues[item.ItemID] {
			t.Fatalf("replayed materialize for %s returned issue %s, want %s", item.ItemID, item.IssueID, issues[item.ItemID])
		}
	}
	var bindings, boundIssues int
	dbfx.QueryRow(t, `SELECT count(*) FROM goal_plan_item_issue WHERE goal_id = $1`, goal.ID).Scan(&bindings)
	dbfx.QueryRow(t, `SELECT count(DISTINCT issue_id) FROM goal_plan_item_issue WHERE goal_id = $1`, goal.ID).Scan(&boundIssues)
	if bindings != 2 || boundIssues != 2 {
		t.Fatalf("bindings = %d (distinct issues %d), want 2/2", bindings, boundIssues)
	}
	var tasksAfterSecond int
	dbfx.QueryRow(t, `SELECT count(*) FROM agent_task_queue WHERE issue_id = ANY($1::uuid[])`,
		[]string{issues[itemA], issues[itemB]}).Scan(&tasksAfterSecond)
	if tasksAfterSecond != tasksAfterFirst {
		t.Fatalf("replayed materialize changed task count %d -> %d", tasksAfterFirst, tasksAfterSecond)
	}

	// Every materialized issue is linked to the goal, which is what makes the
	// dependency graph and closure contract apply to it.
	var linked int
	dbfx.QueryRow(t, `SELECT count(*) FROM goal_issue WHERE goal_id = $1 AND issue_id = ANY($2::uuid[])`,
		goal.ID, []string{issues[itemA], issues[itemB]}).Scan(&linked)
	if linked != 2 {
		t.Fatalf("goal links = %d, want 2", linked)
	}

	t.Cleanup(func() {
		ctx := context.Background()
		testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE issue_id = ANY($1::uuid[])`, []string{issues[itemA], issues[itemB]})
		testPool.Exec(ctx, `DELETE FROM goal_plan_item_issue WHERE goal_id = $1`, goal.ID)
		testPool.Exec(ctx, `DELETE FROM goal_issue WHERE goal_id = $1`, goal.ID)
		testPool.Exec(ctx, `DELETE FROM issue WHERE id = ANY($1::uuid[])`, []string{issues[itemA], issues[itemB]})
	})
}

// A refreshed page reads the durable execution model: the persisted item→issue
// binding, the current dependency verdict, and the real event that releases a
// waiting item. Nothing here depends on the materialize response being held in
// memory.
func TestGoalPlanReadModelRestoresBindingsAndDependencyWaits(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	projectID := createProjectForPlan(t, "Read model project "+t.Name())
	goal := planTestGoal(t, projectID)
	agentID := closureTestAgent(t, "read model agent", "deepseek-v4.1-flash")

	itemA := "018f3a9e-2222-7c2b-9f1e-0000000000a1"
	itemB := "018f3a9e-2222-7c2b-9f1e-0000000000a2"
	code, _ := putPlan(t, goal.ID, map[string]any{
		"expected_revision": 0,
		"items": []map[string]any{
			{"id": itemA, "title": "prerequisite node", "binding": map[string]any{"type": "new"}},
			{"id": itemB, "title": "dependent node", "binding": map[string]any{"type": "new"}},
		},
		"edges": []map[string]any{{
			"dependent_item_id": itemB, "prerequisite_item_id": itemA, "reason": "B needs A",
		}},
	})
	if code != http.StatusOK {
		t.Fatalf("save plan = %d, want 200", code)
	}
	materialized := materializePlan(t, goal.ID, map[string]any{"items": []map[string]any{
		{"item_id": itemA, "assignee_type": "agent", "assignee_id": agentID},
		{"item_id": itemB, "assignee_type": "agent", "assignee_id": agentID},
	}})
	issues := map[string]string{}
	for _, item := range materialized.Items {
		issues[item.ItemID] = item.IssueID
	}
	if issues[itemA] == "" || issues[itemB] == "" {
		t.Fatalf("materialize did not bind both items: %+v", materialized)
	}
	authorization := dbfx.Insert(t, "closure_authorization", testutil.Cols{
		"workspace_id":    testWorkspaceID,
		"project_id":      projectID,
		"subject_kind":    "issue",
		"subject_id":      issues[itemA],
		"actions":         []string{"issue_close"},
		"covers_children": false,
		"state":           "active",
		"revision":        1,
		"granted_by_type": "member",
		"granted_by_id":   testUserID,
	})
	dbfx.Insert(t, "closure_review", testutil.Cols{
		"workspace_id":      testWorkspaceID,
		"project_id":        projectID,
		"subject_kind":      "issue",
		"subject_id":        issues[itemA],
		"action":            "issue_close",
		"delivery_version":  closureTestDelivery,
		"criteria_revision": "plan:" + goal.ID + ":r1",
		"author_type":       "agent",
		"author_id":         agentID,
		"reviewer_type":     "member",
		"reviewer_id":       testUserID,
		"verdict":           "pass",
		"criteria":          testutil.Raw("'[]'::jsonb"),
		"platforms":         testutil.Raw("'[]'::jsonb"),
		"missing":           testutil.Raw("'[]'::jsonb"),
		"authorization_id":  authorization,
	})
	t.Cleanup(func() {
		ctx := context.Background()
		testPool.Exec(ctx, `DELETE FROM closure_review WHERE subject_id = $1`, issues[itemA])
		testPool.Exec(ctx, `DELETE FROM closure_authorization WHERE subject_id = $1`, issues[itemA])
		testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE issue_id = ANY($1::uuid[])`, []string{issues[itemA], issues[itemB]})
		testPool.Exec(ctx, `DELETE FROM goal_plan_item_issue WHERE goal_id = $1`, goal.ID)
		testPool.Exec(ctx, `DELETE FROM goal_issue WHERE goal_id = $1`, goal.ID)
		testPool.Exec(ctx, `DELETE FROM issue WHERE id = ANY($1::uuid[])`, []string{issues[itemA], issues[itemB]})
	})

	read := func() GoalPlanResponse {
		t.Helper()
		code, plan := getPlan(t, goal.ID)
		if code != http.StatusOK {
			t.Fatalf("get plan = %d, want 200", code)
		}
		return plan
	}

	// Refresh before A is accepted: both bindings survive, B reports why it is
	// waiting and names the prerequisite facts plus the real release event.
	plan := read()
	if plan.Execution == nil || plan.Execution.PlanRevision != 1 || len(plan.Execution.Items) != 2 {
		t.Fatalf("execution = %+v, want revision 1 with two items", plan.Execution)
	}
	byID := map[string]GoalPlanExecutionItem{}
	for _, item := range plan.Execution.Items {
		byID[item.ItemID] = item
	}
	a, b := byID[itemA], byID[itemB]
	if a.IssueID != issues[itemA] || !a.Materialized || a.Dependency == nil || !a.Dependency.Ready || a.Wait != nil {
		t.Fatalf("restored A = %+v", a)
	}
	if b.IssueID != issues[itemB] || !b.Materialized || b.Dependency == nil || b.Dependency.Ready {
		t.Fatalf("restored B = %+v", b)
	}
	if b.Dependency.Reason != "dependency_not_done" {
		t.Fatalf("B dependency reason = %q, want dependency_not_done", b.Dependency.Reason)
	}
	if b.Wait == nil || b.Wait.Event != "dependency_changed" || len(b.Wait.Prerequisites) != 1 {
		t.Fatalf("B wait = %+v, want one dependency_changed prerequisite", b.Wait)
	}
	prerequisite := b.Wait.Prerequisites[0]
	if prerequisite.ItemID != itemA || prerequisite.IssueID != issues[itemA] || prerequisite.Status != "todo" {
		t.Fatalf("waiting prerequisite = %+v", prerequisite)
	}
	if prerequisite.Reason != "dependency_not_done" {
		t.Fatalf("waiting prerequisite reason = %q", prerequisite.Reason)
	}

	// Persisted acceptance is the real recovery event. The same read now
	// reports B released, with no timer ever involved.
	dbfx.Exec(t, `UPDATE issue SET status = 'done' WHERE id = $1`, issues[itemA])
	dbfx.Exec(t, `UPDATE closure_review SET accepted_at = now(), accepted_by_type = 'member', accepted_by_id = $2
		WHERE subject_id = $1`, issues[itemA], testUserID)

	plan = read()
	byID = map[string]GoalPlanExecutionItem{}
	for _, item := range plan.Execution.Items {
		byID[item.ItemID] = item
	}
	b = byID[itemB]
	if b.Dependency == nil || !b.Dependency.Ready || b.Wait != nil {
		t.Fatalf("B after acceptance = %+v, want ready and no wait", b)
	}
	if byID[itemA].IssueStatus != "done" {
		t.Fatalf("A status = %q, want done", byID[itemA].IssueStatus)
	}
}

func TestMaterializeGoalPlanRejectsUnknownItem(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	projectID := createProjectForPlan(t, "Materialize project "+t.Name())
	goal := planTestGoal(t, projectID)
	item := "018f3a9e-1111-7c2b-9f1e-0000000000b1"
	if code, _ := putPlan(t, goal.ID, map[string]any{
		"expected_revision": 0,
		"items": []map[string]any{{
			"id": item, "title": "node", "binding": map[string]any{"type": "new"},
		}},
	}); code != http.StatusOK {
		t.Fatalf("save plan = %d, want 200", code)
	}

	body := map[string]any{"items": []map[string]any{{"item_id": "018f3a9e-1111-7c2b-9f1e-00000000dead"}}}
	res := testutil.Call(t, testHandler.MaterializeGoalPlan,
		withURLParam(newRequest("POST", "/api/goals/"+goal.ID+"/plan/materialize", body), "id", goal.ID)).
		Want(http.StatusBadRequest)
	if res.Map()["error"] == nil {
		t.Fatalf("unknown item response = %v, want a 400 with an explanation", res.Map())
	}
}
