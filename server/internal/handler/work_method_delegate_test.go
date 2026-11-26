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
	"github.com/multica-ai/multica/server/pkg/taskfailure"
	"github.com/multica-ai/multica/server/pkg/workmethod"
)

func TestGoalCandidateDelegationKeepsPrivateAuthorityWithinTheStep(t *testing.T) {
	testGoalCandidateHandoff(t, candidateHandoffScenario{})
}

func TestGoalCandidateSelfClaimRequiresTheCandidatesOwnDecision(t *testing.T) {
	testGoalCandidateHandoff(t, candidateHandoffScenario{selfClaim: true})
}

func TestGoalCandidateRejectsChangedSquadHandoffBeforeClaimAndStart(t *testing.T) {
	testGoalCandidateHandoff(t, candidateHandoffScenario{selfClaim: true, briefFence: true})
}

func TestGoalCandidateFailureReachesIndependentReviewerAfterResumption(t *testing.T) {
	testGoalCandidateHandoff(t, candidateHandoffScenario{selfClaim: true, candidateFails: true})
}

func TestGoalCandidateCheckoutFailureContinuesToSquadLeaderOnce(t *testing.T) {
	testGoalCandidateHandoff(t, candidateHandoffScenario{selfClaim: true, checkoutFailure: true})
}

func TestReworkUsesCurrentImplementerAfterSquadRetirement(t *testing.T) {
	ctx := context.Background()
	lead := dbfx.Agent(t, "Retired squad leader", testRuntimeID)
	worker := dbfx.Agent(t, "Replacement implementer", testRuntimeID)
	reviewer := dbfx.Agent(t, "Unchanged independent reviewer", testRuntimeID)
	squad := dbfx.Squad(t, "Retired squad", lead)
	issue := db.Issue{WorkspaceID: util.MustParseUUID(testWorkspaceID)}
	step := workmethod.Step{Gate: "candidate", Name: "Implement", Output: "Tested candidate",
		Owner: &workmethod.Actor{Kind: "squad", ID: squad}, Reviewer: &workmethod.Actor{Kind: "agent", ID: reviewer}}
	var original workmethod.Template
	testutil.Call(t, testHandler.CreateWorkMethod, newRequest("POST", "/api/work-methods?workspace_id="+testWorkspaceID, workmethod.Template{
		Name: "Retire a candidate squad " + workflowTestSuffix(), Kind: "development", Review: "Independent review", Steps: []workmethod.Step{step},
	})).Want(http.StatusCreated).JSON(&original)
	run := workmethod.Run{State: "working", GoalExecution: true, Method: &original, History: []workmethod.Event{{Action: "rework"}}}
	next := original
	next.Steps = []workmethod.Step{step}
	next.Steps[0].Owner = &workmethod.Actor{Kind: "agent", ID: worker}
	update := struct {
		workmethod.Template
		BaseRevision int `json:"base_revision"`
	}{next, original.Revision}
	testutil.Call(t, testHandler.UpdateWorkMethod, withURLParam(newRequest("PUT", "/api/work-methods/"+original.ID+"?workspace_id="+testWorkspaceID, update), "id", original.ID)).Want(http.StatusOK)
	if actor, err := testHandler.methodRecipient(ctx, issue, run); err != nil || actor == nil || actor.ID != squad {
		t.Fatalf("live bound squad: actor=%+v err=%v", actor, err)
	}
	dbfx.Exec(t, "UPDATE squad SET archived_at=now() WHERE id=$1", squad)
	if actor, err := testHandler.methodRecipient(ctx, issue, run); err != nil || actor == nil || actor.Kind != "agent" || actor.ID != worker {
		t.Fatalf("archived squad must use the already reauthorized catalogue implementer: actor=%+v err=%v", actor, err)
	}
	if original.Steps[0].Owner.ID != squad {
		t.Fatal("historical method snapshot was overwritten")
	}
}

type candidateHandoffScenario struct {
	selfClaim       bool
	candidateFails  bool
	checkoutFailure bool
	briefFence      bool
}

func testGoalCandidateHandoff(t *testing.T, scenario candidateHandoffScenario) {
	t.Helper()
	selfClaim, candidateFails := scenario.selfClaim, scenario.candidateFails
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
		workmethod.Template{Name: "Build and review candidate " + workflowTestSuffix(), Kind: "development", Review: "Check the working candidate",
			Steps: []workmethod.Step{{Name: "Implement candidate", Gate: "candidate", SelfClaim: selfClaim, Output: "Verified candidate",
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
	dispatch := func(action, card, token, target string, revision, want int) string {
		t.Helper()
		var result map[string]string
		var out any
		if want == http.StatusOK {
			out = &result
		}
		post(card, "work-method", token, map[string]any{"action": action, "base_revision": revision,
			"delegate_agent_id": target, "evidence": "Implement and test the approved report"}, want, out)
		return result["task_id"]
	}
	offerMode := workmethod.Delegate
	if selfClaim {
		offerMode = "offer_candidate"
	}
	delegate := func(card, token, target string, revision, want int) string {
		t.Helper()
		return dispatch(offerMode, card, token, target, revision, want)
	}

	tick()
	coordinator := start(latest(manager))
	post(issue, "work-method", tokenFor(coordinator), map[string]any{
		"action": "start", "base_revision": 0, "method_id": template.ID,
	}, http.StatusOK, nil)
	finish(coordinator)
	var originalLedger, editedLedger []byte
	var brief *workmethod.SquadContextBrief
	if scenario.briefFence {
		card, err := testHandler.Queries.GetIssue(ctx, util.MustParseUUID(issue))
		if err != nil {
			t.Fatal(err)
		}
		trace, err := acceptance.Decode(card.AcceptanceCriteria)
		if err != nil {
			t.Fatal(err)
		}
		trace.Criteria[0].Cases = []acceptance.TestCase{{
			ID: "T1", Specification: "Open the fixed report", ArtifactSHA256: strings.Repeat("a", 64),
		}}
		trace.Revision++
		originalLedger, err = json.Marshal(trace)
		if err != nil {
			t.Fatal(err)
		}
		run := readDeliveryMethod(t, issue)
		brief = run.SquadContextBrief(card.Title, trace)
		run.History[len(run.History)-1].SquadHandoff = &workmethod.SquadHandoff{
			TaskID: uuidToString(latest(lead).ID), ToSquadID: squad, ToStep: run.Step, RunRevision: run.Revision, ContextBrief: brief,
		}
		rawRun, err := json.Marshal(run)
		if err != nil {
			t.Fatal(err)
		}
		dbfx.Exec(t, "UPDATE issue SET work_method_run=$2, acceptance_criteria=$3 WHERE id=$1", issue, rawRun, originalLedger)
		trace.Criteria[0].Cases[0].ArtifactSHA256 = strings.Repeat("b", 64)
		trace.Revision++
		editedLedger, err = json.Marshal(trace)
		if err != nil {
			t.Fatal(err)
		}
	}
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
	var parentID = leader.ID
	priorWorkerTasks := 0
	if selfClaim {
		dispatch("delegate", issue, leaderToken, worker, 1, http.StatusForbidden)
		delegate(issue, leaderToken, alternate, 1, http.StatusOK)
		first, second := start(latest(worker)), start(latest(alternate))
		if !service.IsGoalCandidateOffer(first) || !service.IsGoalCandidateOffer(second) ||
			taskCountFor(t, issue, worker) != 1 || taskCountFor(t, issue, alternate) != 1 {
			t.Fatal("inviting agents should only create assessment runs")
		}
		if scenario.briefFence {
			if !strings.Contains(first.HandoffNote.String, brief.ContractDigest) {
				t.Fatalf("assessment did not receive the original brief: %q", first.HandoffNote.String)
			}
			// Keep the method slot fixed to exercise the defensive data check
			// even if a contract edit bypasses the normal return to review.
			dbfx.Exec(t, "UPDATE issue SET acceptance_criteria=$2, revision=revision+1 WHERE id=$1", issue, editedLedger)
			var rejected map[string]string
			post(issue, "work-method", tokenFor(first), map[string]any{
				"action": "claim_candidate", "base_revision": 1, "evidence": "I read the old brief",
			}, http.StatusConflict, &rejected)
			if rejected["code"] != "squad_context_changed" || taskCountFor(t, issue, worker) != 1 {
				t.Fatalf("stale assessment must not create a worker: %v", rejected)
			}
			dbfx.Exec(t, "UPDATE issue SET acceptance_criteria=$2, revision=revision+1 WHERE id=$1", issue, originalLedger)
		}
		dispatch("claim_candidate", issue, leaderToken, "", 1, http.StatusForbidden)
		dispatch("claim_candidate", issue, tokenFor(first), alternate, 1, http.StatusBadRequest)
		dispatch("claim_candidate", otherIssue, tokenFor(first), "", 1, http.StatusForbidden)
		dispatch("claim_candidate", issue, tokenFor(first), "", 2, http.StatusConflict)
		post(issue, "comments", tokenFor(first), map[string]string{"content": "Do work now"}, http.StatusForbidden, nil)
		claims := make([]*http.Request, 0, 2)
		for _, assessment := range []db.AgentTaskQueue{first, second} {
			request := httptest.NewRequest(http.MethodPost, "/api/issues/"+issue+"/work-method",
				strings.NewReader(`{"action":"claim_candidate","base_revision":1,"evidence":"I read the accepted cases and can implement them"}`))
			request.Header.Set("Authorization", "Bearer "+tokenFor(assessment))
			request.Header.Set("X-Workspace-ID", testWorkspaceID)
			claims = append(claims, request)
		}
		startRace := make(chan struct{})
		outcomes := make(chan int, len(claims))
		for _, request := range claims {
			go func() {
				<-startRace
				response := httptest.NewRecorder()
				router.ServeHTTP(response, request)
				outcomes <- response.Code
			}()
		}
		close(startRace)
		a, b := <-outcomes, <-outcomes
		if !(a == http.StatusOK && b == http.StatusConflict || a == http.StatusConflict && b == http.StatusOK) {
			t.Fatalf("competing claims got %d and %d, want one success and one conflict", a, b)
		}
		if taskCountFor(t, issue, alternate) == 2 {
			worker, alternate = alternate, worker
			first, second = second, first
		}
		parentID, priorWorkerTasks = first.ID, 1
		claimID := dispatch("claim_candidate", issue, tokenFor(first), "", 1, http.StatusOK)
		if implementation := latest(worker); claimID != uuidToString(implementation.ID) || implementation.Status != "deferred" {
			t.Fatalf("retry must return the same implementation, held until assessment stops: %s %s", claimID, implementation.Status)
		}
		finish(first)
		finish(second)
		if err := testHandler.TaskService.PromoteDueDeferredTasksForRuntime(ctx, first.RuntimeID); err != nil {
			t.Fatal(err)
		}
		if got := latest(worker).Status; got != "queued" {
			t.Fatalf("finishing assessment should release the implementation for claim, got %s", got)
		}
	} else {
		delegate(issue, leaderToken, alternate, 1, http.StatusConflict)
	}
	implementer := latest(worker)
	if taskCountFor(t, issue, worker) != priorWorkerTasks+1 || implementer.IsLeaderTask || implementer.OriginatorUserID.Valid ||
		implementer.DelegatedFromTaskID != parentID || service.ReadGoalExecutionTask(implementer).Revision != 1 {
		t.Fatalf("unexpected worker binding: %s", uuidToString(implementer.ID))
	}
	if scenario.briefFence {
		if !strings.Contains(implementer.HandoffNote.String, brief.ContractDigest) {
			t.Fatalf("implementation did not receive the original brief: %q", implementer.HandoffNote.String)
		}
		dbfx.Exec(t, "UPDATE issue SET acceptance_criteria=$2, revision=revision+1 WHERE id=$1", issue, editedLedger)
		check := service.IssueWakeupService{Tasks: testHandler.TaskService}
		if err := check.CheckClaim(ctx, implementer); !errors.Is(err, service.ErrSquadContextChanged) {
			t.Fatalf("the queued implementation accepted stale context: %v", err)
		}
		dbfx.Exec(t, "UPDATE agent_task_queue SET status='dispatched',dispatched_at=now() WHERE id=$1", implementer.ID)
		if _, err := testHandler.TaskService.StartTask(ctx, implementer.ID); !errors.Is(err, service.ErrSquadContextChanged) {
			t.Fatalf("the prepared implementation launched with stale context: %v", err)
		}
		return
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
	result := "pass"
	if candidateFails {
		result = "fail"
	}
	record(map[string]any{"action": "run", "case_id": "T1", "result": result, "candidate": sha, "evidence": "Recorded the report behavior on the candidate"})
	advance := map[string]any{"action": "advance", "base_revision": 1, "evidence": "Verified the same candidate"}
	post(issue, "work-method", workerToken, advance, http.StatusForbidden, nil)
	finish(implementer)
	if candidateFails {
		authorize(false)
		tick()
		if taskCountFor(t, issue, lead) != 1 {
			t.Fatal("a revoked goal must leave the failed candidate paused")
		}
		authorize(true)
	}
	tick()
	resumed := start(latest(lead))
	if resumed.ID == leader.ID || (!candidateFails && !strings.Contains(resumed.HandoffNote.String, uuidToString(implementer.ID))) {
		t.Fatalf("resume should cite the finished worker: %q", resumed.HandoffNote.String)
	}
	var opened workmethod.Run
	post(issue, "work-method", tokenFor(resumed), advance, http.StatusOK, &opened)
	if candidateFails {
		if !strings.Contains(resumed.HandoffNote.String, sha) || !strings.Contains(resumed.HandoffNote.String, "action=advance") {
			t.Fatalf("resumption must point to the fixed failed candidate: %q", resumed.HandoffNote.String)
		}
		if opened.State != "review" || opened.Revision != 2 || taskCountFor(t, issue, reviewer) != 1 {
			t.Fatalf("a reported failure must dispatch exactly one review: %+v", opened)
		}
		post(issue, "work-method", tokenFor(resumed), advance, http.StatusConflict, nil)
	}
	finish(resumed)
	independent := start(latest(reviewer))
	if candidateFails {
		if !strings.Contains(independent.HandoffNote.String, sha) || !strings.Contains(independent.HandoffNote.String, "T1") {
			t.Fatalf("the reviewer needs the failed SHA and case: %q", independent.HandoffNote.String)
		}
		post(issue, "work-method", tokenFor(independent), map[string]any{
			"action": "approve", "base_revision": 2, "evidence": "Try to accept the failed candidate",
		}, http.StatusConflict, nil)
	}
	post(issue, "work-method", tokenFor(independent), map[string]any{
		"action": "rework", "base_revision": 2, "evidence": "Recheck the candidate report",
	}, http.StatusOK, nil)
	finish(independent)
	if candidateFails {
		rework := start(latest(lead))
		post(issue, "work-method", tokenFor(rework), map[string]any{
			"action": "advance", "base_revision": 3, "evidence": "Try to resubmit the previous slot's failed candidate",
		}, http.StatusConflict, nil)
		if service.ReadGoalExecutionTask(rework).Revision != 3 || taskCountFor(t, issue, reviewer) != 1 {
			t.Fatal("the independent decision must reassign the working step without repeating failed review")
		}
		return
	}
	rework := start(latest(lead))
	firstReworkID := delegate(issue, tokenFor(rework), worker, 3, http.StatusOK)
	if selfClaim {
		declined := start(latest(worker))
		finish(declined)
		if replay := delegate(issue, tokenFor(rework), worker, 3, http.StatusOK); replay != firstReworkID {
			t.Fatalf("the original leader run must replay the same invitation: %s", replay)
		}
		finish(rework)
		tick()
		retry := start(latest(lead))
		if replay := delegate(issue, tokenFor(retry), worker, 3, http.StatusOK); replay != firstReworkID {
			t.Fatalf("a scheduler retry alone must not issue another invitation: %s", replay)
		}
		finish(retry)
		for _, status := range []string{"blocked", "in_progress"} {
			testutil.Call(t, testHandler.UpdateIssue, goalRequest(http.MethodPut, "/api/issues/"+issue, testUserID,
				map[string]any{"status": status}, map[string]string{"id": issue})).Want(http.StatusOK)
		}
		// The server's activity listener is outside the handler test harness.
		dbfx.Exec(t, `INSERT INTO activity_log (workspace_id, issue_id, actor_type, actor_id, action, details)
			VALUES ($1, $2, 'member', $3, 'status_changed', '{"from":"blocked","to":"in_progress"}')`,
			testWorkspaceID, issue, testUserID)
		tick()
		resumed := latest(lead)
		if resumed.ID == retry.ID {
			t.Fatal("reopening the card must schedule a new leader run")
		}
		reopened := start(resumed)
		fresh := delegate(issue, tokenFor(reopened), worker, 3, http.StatusOK)
		if fresh == firstReworkID || fresh == "" {
			t.Fatalf("the resumed leader must create a new invitation after an unclaimed assessment: %s", fresh)
		}
		if replay := delegate(issue, tokenFor(reopened), worker, 3, http.StatusOK); replay != fresh {
			t.Fatalf("retrying the new invitation must reuse its active run: %s", replay)
		}
		dispatch("claim_candidate", issue, tokenFor(declined), "", 3, http.StatusForbidden)
		assessment := start(latest(worker))
		dispatch("claim_candidate", issue, tokenFor(assessment), "", 3, http.StatusOK)
		finish(assessment)
		finish(reopened)
	}
	next := latest(worker)
	if next.ID == implementer.ID {
		t.Fatal("an independent rework must have its own candidate run")
	}
	checkout := service.ReadGoalExecutionTask(next).CandidateCheckout
	if checkout == nil || checkout.Commit != sha || checkout.Branch != "dev" || checkout.Repository != "org/reports" {
		t.Fatalf("rework must keep the candidate that was reviewed: %+v", checkout)
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
	if scenario.checkoutFailure {
		dbfx.Exec(t, "UPDATE agent_task_queue SET status='dispatched',dispatched_at=now() WHERE id=$1", next.ID)
		reason := taskfailure.ReasonCandidateCheckoutUnavailable.String()
		failed, err := testHandler.TaskService.FailTask(ctx, next.ID, "candidate missing in the local repository", "", "", "", reason, false, "", "")
		if err != nil || failed.Status != "failed" || failed.FailureReason.String != reason || failed.StartedAt.Valid {
			t.Fatalf("checkout failure must be recorded before launching the implementer: %+v, %v", failed, err)
		}
		dbfx.Exec(t, "UPDATE agent_task_queue SET completed_at=now()-interval '2 minutes' WHERE id=$1", next.ID)
		before := taskCountFor(t, issue, lead)
		tick()
		coordinator := start(latest(lead))
		if !strings.Contains(coordinator.HandoffNote.String, sha) || !strings.Contains(coordinator.HandoffNote.String, "候选工作树准备失败") {
			t.Fatalf("the formal successor must receive the actual failure and candidate: %q", coordinator.HandoffNote.String)
		}
		finish(coordinator)
		tick()
		if taskCountFor(t, issue, lead) != before+1 || taskCountFor(t, issue, reviewer) != 1 {
			t.Fatal("checkout failure redelivered the escalation or started a code review")
		}
		return
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
