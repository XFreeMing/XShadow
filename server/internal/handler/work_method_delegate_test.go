package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/pkg/acceptance"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/workmethod"
)

func TestGoalCandidateDelegationKeepsPrivateAuthorityWithinTheStep(t *testing.T) {
	ctx := context.Background()
	seedTestCatalog(t)
	runtime := dbfx.Runtime(t, "Goal candidate runtime")
	manager := dbfx.Agent(t, "Goal coordinator", runtime)
	lead := dbfx.Agent(t, "Implementation squad lead", runtime)
	worker := dbfx.Agent(t, "Private implementer", runtime)
	alternate := dbfx.Agent(t, "Other eligible implementer", runtime)
	reviewer := dbfx.Agent(t, "Independent reviewer", runtime)
	outsider := dbfx.Agent(t, "Private agent outside squad", runtime)
	squad := dbfx.Squad(t, "Implementation squad", lead)
	for _, agent := range []string{worker, alternate, reviewer} {
		dbfx.SquadMember(t, squad, "agent", agent)
	}
	project := deliveryProject(t, "Goal candidate project", manager)
	goal := createGoalViaAPI(t, testUserID, map[string]any{"title": "Deliver a candidate", "project_id": project, "status": "in_progress"})
	t.Cleanup(func() { dbfx.Exec(t, "DELETE FROM goal_plan WHERE goal_id=$1", goal.ID) })
	issue := dbfx.Issue(t, "Implement candidate", testutil.Cols{"project_id": project, "status": "backlog"})
	otherIssue := dbfx.Issue(t, "Unrelated issue", testutil.Cols{"project_id": project, "status": "backlog"})
	if result := linkGoalIssueViaAPI(t, testUserID, goal.ID, issue); result != http.StatusNoContent {
		t.Fatalf("link goal issue: %d", result)
	}
	var template workmethod.Template
	testutil.Call(t, testHandler.CreateWorkMethod, newRequest("POST", "/api/work-methods?workspace_id="+testWorkspaceID,
		workmethod.Template{Name: "Build and review candidate", Kind: "development", Review: "Check the working candidate",
			Steps: []workmethod.Step{{Name: "Implement candidate", Gate: "candidate", Output: "Verified candidate",
				Owner: &workmethod.Actor{Kind: "squad", ID: squad}, Reviewer: &workmethod.Actor{Kind: "agent", ID: reviewer}}}},
	)).Want(http.StatusCreated).JSON(&template)
	planPath := "/api/goals/" + goal.ID + "/plan"
	testutil.Call(t, testHandler.PutGoalPlan, goalRequest("PUT", planPath, testUserID, map[string]any{
		"expected_revision": 0, "items": []map[string]any{{
			"id": uuidToString(dbid.NewV7()), "title": "Implement candidate", "stage": 1,
			"binding": map[string]any{"type": "existing", "issue_id": issue},
			"delivery": map[string]any{"type": "feature", "method_id": template.ID,
				"criteria": []map[string]any{{"id": "C1", "expected": "A working report", "verification": "Open the report"}}},
		}},
	}, map[string]string{"id": goal.ID})).Want(http.StatusOK)
	authorize := func(enabled bool) {
		t.Helper()
		testutil.Call(t, testHandler.PutGoalExecution, goalRequest("PUT", planPath+"/execution", testUserID,
			map[string]any{"enabled": enabled, "expected_revision": 1}, map[string]string{"id": goal.ID})).Want(http.StatusOK)
	}
	authorize(true)

	router := chi.NewRouter()
	router.Use(middleware.Auth(testHandler.Queries, nil, nil, nil))
	router.Use(middleware.RequireWorkspaceMember(testHandler.Queries))
	router.Post("/api/issues/{id}/work-method", testHandler.ChangeIssueWorkMethod)
	router.Post("/api/issues/{id}/comments", testHandler.CreateComment)
	router.Post("/api/issues/{id}/acceptance", testHandler.ChangeIssueAcceptance)
	post := func(card, endpoint, token string, body any, want int, output any) {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/issues/"+card+"/"+endpoint, bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Workspace-ID", testWorkspaceID)
		req.Header.Set("Content-Type", "application/json")
		resp := testutil.Call(t, router.ServeHTTP, req).Want(want)
		if output != nil {
			resp.JSON(output)
		}
	}
	tokenFor := func(task db.AgentTaskQueue) string {
		t.Helper()
		return mintGoalPlanTaskToken(t, testWorkspaceID, uuidToString(task.ID), uuidToString(task.AgentID))
	}
	latest := func(agentID string) db.AgentTaskQueue {
		t.Helper()
		var id string
		dbfx.QueryRow(t, `SELECT id FROM agent_task_queue WHERE issue_id=$1 AND agent_id=$2 ORDER BY created_at DESC, id DESC LIMIT 1`, issue, agentID).Scan(&id)
		task, err := testHandler.Queries.GetAgentTask(ctx, util.MustParseUUID(id))
		if err != nil {
			t.Fatal(err)
		}
		return task
	}
	start := func(task db.AgentTaskQueue) db.AgentTaskQueue {
		t.Helper()
		check := service.IssueWakeupService{Tasks: testHandler.TaskService}
		if err := check.CheckClaim(ctx, task); err != nil {
			t.Fatal(err)
		}
		dbfx.Exec(t, "UPDATE agent_task_queue SET status='dispatched',dispatched_at=now() WHERE id=$1", task.ID)
		running, err := testHandler.TaskService.StartTask(ctx, task.ID)
		if err != nil || running.Status != "running" {
			t.Fatalf("start goal task: %v", err)
		}
		return *running
	}
	finish := func(task db.AgentTaskQueue) {
		t.Helper()
		if _, err := testHandler.TaskService.CompleteTask(ctx, task.ID, nil, "", "", "", false, "", ""); err != nil {
			t.Fatal(err)
		}
		dbfx.Exec(t, "UPDATE agent_task_queue SET completed_at=now()-interval '2 minutes' WHERE id=$1", task.ID)
	}
	tick := func() {
		t.Helper()
		dbfx.Exec(t, "UPDATE issue SET updated_at=now()-interval '2 minutes' WHERE id=$1", issue)
		scanner := service.IssueContinuation{Tasks: testHandler.TaskService, ManagerID: util.MustParseUUID(manager)}
		if err := scanner.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	delegate := func(card, token, target string, revision, want int) string {
		t.Helper()
		var result map[string]string
		var out any
		if want == http.StatusOK {
			out = &result
		}
		post(card, "work-method", token, map[string]any{"action": "delegate", "base_revision": revision,
			"delegate_agent_id": target, "evidence": "Implement and test the approved report"}, want, out)
		return result["task_id"]
	}

	tick()
	coordinator := start(latest(manager))
	post(issue, "work-method", tokenFor(coordinator), map[string]any{
		"action": "start", "base_revision": 0, "method_id": template.ID,
	}, http.StatusOK, nil)
	finish(coordinator)
	leader := start(latest(lead))
	leaderToken := tokenFor(leader)
	var comment CommentResponse
	post(issue, "comments", leaderToken, map[string]any{"content": fmt.Sprintf("[@Engineer](mention://agent/%s) implement the report", worker)}, http.StatusCreated, &comment)
	if got := findCommentOutcome(t, comment.TriggerOutcomes, worker); got.Status != DispatchBlocked || got.ReasonCode != ReasonInvocationNotAllowed {
		t.Fatalf("a plain mention must still respect private permissions: %+v", got)
	}
	delegate(otherIssue, leaderToken, worker, 1, http.StatusForbidden)
	delegate(issue, leaderToken, outsider, 1, http.StatusForbidden)
	delegate(issue, leaderToken, reviewer, 1, http.StatusForbidden)
	otherOwner := dbfx.User(t, "Private agent owner", "delegate-"+workflowTestSuffix()+"@example.com")
	dbfx.Member(t, testWorkspaceID, otherOwner, "member")
	dbfx.Exec(t, "UPDATE agent SET owner_id=$2 WHERE id=$1", alternate, otherOwner)
	delegate(issue, leaderToken, alternate, 1, http.StatusForbidden)
	dbfx.Exec(t, "UPDATE agent SET owner_id=$2 WHERE id=$1", alternate, testUserID)
	delegate(issue, leaderToken, worker, 2, http.StatusConflict)
	if taskID := delegate(issue, leaderToken, worker, 1, http.StatusOK); taskID == "" || delegate(issue, leaderToken, worker, 1, http.StatusOK) != taskID {
		t.Fatalf("same selection must return one durable run, got %s", taskID)
	}
	delegate(issue, leaderToken, alternate, 1, http.StatusConflict)
	implementer := latest(worker)
	if taskCountFor(t, issue, worker) != 1 || implementer.IsLeaderTask || implementer.OriginatorUserID.Valid ||
		implementer.DelegatedFromTaskID != leader.ID || service.ReadGoalExecutionTask(implementer).Revision != 1 {
		t.Fatalf("unexpected worker binding: %s", uuidToString(implementer.ID))
	}
	finish(leader)
	tick()
	if taskCountFor(t, issue, lead) != 1 {
		t.Fatal("leader was woken while the implementer still had work")
	}
	implementer = start(implementer)
	workerToken := tokenFor(implementer)
	delegate(issue, workerToken, alternate, 1, http.StatusForbidden)
	card, err := testHandler.Queries.GetIssue(ctx, util.MustParseUUID(issue))
	if err != nil {
		t.Fatal(err)
	}
	trace, err := acceptance.Decode(card.AcceptanceCriteria)
	if err != nil {
		t.Fatal(err)
	}
	record := func(fields map[string]any) {
		t.Helper()
		fields["criterion_id"], fields["base_revision"] = "C1", trace.Revision
		post(issue, "acceptance", workerToken, fields, http.StatusOK, &trace)
	}
	sha := strings.Repeat("a", 40)
	record(map[string]any{"action": "case", "case_id": "T1", "text": "Open the report"})
	record(map[string]any{"action": "implementation", "repository": "org/reports", "branch": "dev", "commit": sha})
	record(map[string]any{"action": "run", "case_id": "T1", "result": "pass", "candidate": sha, "evidence": "Report opened on the candidate"})
	advance := map[string]any{"action": "advance", "base_revision": 1, "evidence": "Verified the same candidate"}
	post(issue, "work-method", workerToken, advance, http.StatusForbidden, nil)
	finish(implementer)
	tick()
	resumed := start(latest(lead))
	if resumed.ID == leader.ID || !strings.Contains(resumed.HandoffNote.String, uuidToString(implementer.ID)) {
		t.Fatalf("resume should cite the finished worker: %q", resumed.HandoffNote.String)
	}
	post(issue, "work-method", tokenFor(resumed), advance, http.StatusOK, nil)
	finish(resumed)
	independent := start(latest(reviewer))
	post(issue, "work-method", tokenFor(independent), map[string]any{
		"action": "rework", "base_revision": 2, "evidence": "Recheck the candidate report",
	}, http.StatusOK, nil)
	finish(independent)
	rework := start(latest(lead))
	delegate(issue, tokenFor(rework), worker, 3, http.StatusOK)
	next := latest(worker)
	if next.ID == implementer.ID {
		t.Fatal("an independent rework must have its own candidate run")
	}
	check := service.IssueWakeupService{Tasks: testHandler.TaskService}
	dbfx.Exec(t, "DELETE FROM squad_member WHERE squad_id=$1 AND member_type='agent' AND member_id=$2", squad, worker)
	if err := check.CheckClaim(ctx, next); !errors.Is(err, service.ErrWakeupForbidden) {
		t.Fatalf("a removed squad member must not claim the delegated candidate: %v", err)
	}
	dbfx.SquadMember(t, squad, "agent", worker)
	if err := check.CheckClaim(ctx, next); err != nil {
		t.Fatalf("the restored member must still be able to claim: %v", err)
	}
	authorize(false)
	if err := check.CheckClaim(ctx, next); !errors.Is(err, service.ErrWakeupForbidden) {
		t.Fatalf("revocation must prevent claiming the delegated candidate: %v", err)
	}
	dbfx.Exec(t, "UPDATE agent_task_queue SET status='dispatched',dispatched_at=now() WHERE id=$1", next.ID)
	if _, err := testHandler.TaskService.StartTask(ctx, next.ID); !errors.Is(err, service.ErrWakeupForbidden) {
		t.Fatalf("revocation must also prevent launching the prepared candidate: %v", err)
	}
}
