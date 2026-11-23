package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/workmethod"
)

func TestGoalExecutionPrivateHandoffsAndRevocation(t *testing.T) {
	ctx := context.Background()
	seedTestCatalog(t)
	runtime := dbfx.Runtime(t, "Goal execution runtime")
	manager := dbfx.Agent(t, "Goal coordinator", runtime)
	author := dbfx.Agent(t, "Private analyst", runtime)
	reviewer := dbfx.Agent(t, "Private reviewer", runtime)
	outsideOwner := dbfx.User(t, "Other agent owner", "goal-"+workflowTestSuffix()+"@example.com")
	dbfx.Member(t, testWorkspaceID, outsideOwner, "member")
	project := deliveryProject(t, "Authorized goal project", manager)
	goal := createGoalViaAPI(t, testUserID, map[string]any{"title": "Goal execution", "project_id": project, "status": "in_progress"})
	t.Cleanup(func() {
		dbfx.Exec(t, "DELETE FROM goal_plan WHERE goal_id=$1", goal.ID)
	})
	issue := func(name string) string {
		id := dbfx.Issue(t, name, testutil.Cols{"status": "backlog", "project_id": project})
		testutil.Call(t, testHandler.LinkGoalIssue, goalRequest("PUT", "/api/goals/"+goal.ID+"/issues/"+id, testUserID, nil,
			map[string]string{"id": goal.ID, "issueId": id})).Want(http.StatusNoContent)
		dbfx.Exec(t, "UPDATE issue SET updated_at=now()-interval '2 minutes' WHERE id=$1", id)
		return id
	}
	first, next, extra := issue("Prepare design"), issue("Build accepted design"), issue("Outside the saved plan")
	var template workmethod.Template
	testutil.Call(t, testHandler.CreateWorkMethod, newRequest("POST", "/api/work-methods?workspace_id="+testWorkspaceID,
		workmethod.Template{Name: "Design and independent review", Kind: "research", Review: "Check the source",
			Steps: []workmethod.Step{{Name: "Compare sources", Output: "Source comparison", Checks: []string{"Compare source evidence"},
				Owner: &workmethod.Actor{Kind: "agent", ID: author}, Reviewer: &workmethod.Actor{Kind: "agent", ID: reviewer}}}},
	)).Want(201).JSON(&template)
	firstItem, nextItem := uuidToString(dbid.NewV7()), uuidToString(dbid.NewV7())
	items := []map[string]any{
		{"id": firstItem, "title": "Prepare design", "binding": map[string]any{"type": "existing", "issue_id": first}, "stage": 1},
		{"id": nextItem, "title": "Use the design", "binding": map[string]any{"type": "existing", "issue_id": next}, "stage": 2},
	}
	for _, item := range items {
		item["delivery"] = map[string]any{"type": "research", "method_id": template.ID,
			"criteria": []map[string]any{{"id": "C1", "expected": "A comparison with sources", "verification": "Check the comparison against the cited source"}}}
	}
	save := func(revision int) {
		t.Helper()
		testutil.Call(t, testHandler.PutGoalPlan, goalRequest("PUT", "/api/goals/"+goal.ID+"/plan", testUserID,
			map[string]any{"expected_revision": revision, "items": items,
				"edges": []map[string]any{{"dependent_item_id": nextItem, "prerequisite_item_id": firstItem, "reason": "Use the accepted comparison"}}},
			map[string]string{"id": goal.ID})).Want(200)
	}
	save(0)

	svc := service.IssueContinuation{Tasks: testHandler.TaskService, ManagerID: util.MustParseUUID(manager)}
	wakeup := service.IssueWakeupService{Tasks: testHandler.TaskService}
	tick := func() {
		t.Helper()
		if err := svc.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	execution := func(enabled bool, revision, status int, actorSource string) GoalPlanResponse {
		t.Helper()
		request := goalRequest("PUT", "/api/goals/"+goal.ID+"/plan/execution", testUserID,
			map[string]any{"enabled": enabled, "expected_revision": revision}, map[string]string{"id": goal.ID})
		request.Header.Set("X-Actor-Source", actorSource)
		response := testutil.Call(t, testHandler.PutGoalExecution, request).Want(status)
		var plan GoalPlanResponse
		if status == 200 {
			response.JSON(&plan)
		}
		return plan
	}
	load := func(issueID, agentID string) db.AgentTaskQueue {
		t.Helper()
		var id string
		if err := testPool.QueryRow(ctx, `SELECT id FROM agent_task_queue WHERE issue_id=$1 AND agent_id=$2
			ORDER BY created_at DESC, id DESC LIMIT 1`, issueID, agentID).Scan(&id); err != nil {
			t.Fatal(err)
		}
		task, err := testHandler.Queries.GetAgentTask(ctx, util.MustParseUUID(id))
		if err != nil {
			t.Fatal(err)
		}
		return task
	}
	running := func(task db.AgentTaskQueue) {
		dbfx.Exec(t, "UPDATE agent_task_queue SET status='running' WHERE id=$1", task.ID)
	}
	finish := func(task db.AgentTaskQueue) {
		dbfx.Exec(t, "UPDATE agent_task_queue SET status='completed',completed_at=now()-interval '2 minutes' WHERE id=$1", task.ID)
	}
	postMethod := func(issueID string, task db.AgentTaskQueue, action, methodID string, revision, want int) {
		t.Helper()
		body := map[string]any{"action": action, "base_revision": revision, "evidence": "Checked and cited the matching source"}
		if methodID != "" {
			body["method_id"] = methodID
		}
		if action == "approve" {
			body["checked_items"] = []int{0}
		}
		r := goalRequest("POST", "/api/issues/"+issueID+"/work-method", testUserID, body, map[string]string{"id": issueID})
		r.Header.Set("X-Actor-Source", "task_token")
		r.Header.Set("X-Agent-ID", uuidToString(task.AgentID))
		r.Header.Set("X-Task-ID", uuidToString(task.ID))
		testutil.Call(t, testHandler.ChangeIssueWorkMethod, r).Want(want)
	}

	tick()
	ungranted := load(first, manager)
	running(ungranted)
	postMethod(first, ungranted, "start", template.ID, 0, 403)
	finish(ungranted)
	execution(true, 1, 403, "task_token")
	execution(true, 2, 409, "")
	if p := execution(true, 1, 200, ""); p.ExecutionState != "active" || p.Progress.IssueID != first {
		t.Fatalf("unexpected approval response: %+v", p)
	}
	tick()
	coordinator := load(first, manager)
	if err := wakeup.CheckClaim(ctx, coordinator); err != nil || !service.ReadGoalExecutionTask(coordinator).ID.Valid || coordinator.OriginatorUserID.Valid {
		t.Fatalf("coordinator grant or attribution invalid: %v", err)
	}
	running(coordinator)
	mention := goalRequest("POST", "/api/issues/"+extra+"/comments", testUserID,
		map[string]any{"content": fmt.Sprintf("[@Analyst](mention://agent/%s) please look at this", author)},
		map[string]string{"id": extra})
	mention.Header.Set("X-Actor-Source", "task_token")
	mention.Header.Set("X-Agent-ID", manager)
	mention.Header.Set("X-Task-ID", uuidToString(coordinator.ID))
	var comment CommentResponse
	testutil.Call(t, testHandler.CreateComment, mention).Want(201).JSON(&comment)
	outcome := findCommentOutcome(t, comment.TriggerOutcomes, author)
	if outcome.Status != DispatchBlocked || outcome.ReasonCode != ReasonInvocationNotAllowed {
		t.Fatalf("the ordinary mention must not inherit the goal's authority: %+v", outcome)
	}
	postMethod(next, coordinator, "start", template.ID, 0, 403)
	// An approval cannot be used on a linked card outside the saved plan.
	postMethod(extra, coordinator, "start", "", 0, 403)
	postMethod(first, coordinator, "select", template.ID, 0, 403)
	postMethod(first, coordinator, "start", template.ID, 0, 200)
	finish(coordinator)

	implementation := methodStageTask(t, first, 1)
	if implementation.AgentID != util.MustParseUUID(author) || implementation.OriginatorUserID.Valid {
		t.Fatal("the first step was not handed to its private analyst without impersonation")
	}
	if err := wakeup.CheckClaim(ctx, implementation); err != nil {
		t.Fatal(err)
	}
	running(implementation)
	dbfx.Exec(t, "UPDATE agent SET owner_id=$2 WHERE id=$1", reviewer, outsideOwner)
	postMethod(first, implementation, "advance", "", 1, 403)
	dbfx.Exec(t, "UPDATE agent SET owner_id=$2 WHERE id=$1", reviewer, testUserID)
	postMethod(first, implementation, "advance", "", 1, 200)
	finish(implementation)

	review := methodStageTask(t, first, 2)
	if review.AgentID != util.MustParseUUID(reviewer) || review.OriginatorUserID.Valid {
		t.Fatal("expected an independent private reviewer and no human impersonation")
	}
	if err := wakeup.CheckClaim(ctx, review); err != nil {
		t.Fatal(err)
	}
	if n := taskCountFor(t, next, manager); n != 0 {
		t.Fatalf("dependent has %d premature runs", n)
	}
	save(1)
	if err := wakeup.CheckClaim(ctx, review); !errors.Is(err, service.ErrWakeupForbidden) {
		t.Fatalf("old queued review should be fenced by plan revision: %v", err)
	}
	dbfx.Exec(t, "UPDATE agent_task_queue SET status='dispatched', dispatched_at=now() WHERE id=$1", review.ID)
	if _, err := testHandler.TaskService.StartTask(ctx, review.ID); !errors.Is(err, service.ErrWakeupForbidden) {
		t.Fatalf("prepared review should fail the daemon start fence: %v", err)
	}
	finish(review)
	execution(true, 2, 200, "")
	dbfx.Exec(t, "UPDATE issue SET updated_at=now()-interval '2 minutes' WHERE id=$1", first)
	tick()
	resumed := load(first, reviewer)
	if resumed.ID == review.ID {
		t.Fatal("new approval did not resume the current reviewer")
	}
	if err := wakeup.CheckClaim(ctx, resumed); err != nil {
		t.Fatal(err)
	}
	running(resumed)
	postMethod(first, implementation, "approve", "", 2, 403)
	postMethod(first, resumed, "approve", "", 2, 200)
	finish(resumed)
	tick()
	successor := load(next, manager)
	if err := wakeup.CheckClaim(ctx, successor); err != nil {
		t.Fatalf("next issue didn't receive authority after independent review: %v", err)
	}
	running(successor)
	execution(false, 2, 200, "")
	if err := wakeup.CheckClaim(ctx, successor); !errors.Is(err, service.ErrWakeupForbidden) {
		t.Fatalf("revoked run should not be claimable: %v", err)
	}
	postMethod(next, successor, "start", template.ID, 0, 403)
}
