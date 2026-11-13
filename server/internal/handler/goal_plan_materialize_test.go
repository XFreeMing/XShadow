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
