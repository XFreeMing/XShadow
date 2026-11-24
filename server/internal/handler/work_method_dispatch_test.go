package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/multica-ai/multica/server/pkg/workmethod"
)

func methodStageTask(t *testing.T, issueID string, revision int) db.AgentTaskQueue {
	t.Helper()
	var id string
	if err := testPool.QueryRow(context.Background(), "SELECT id FROM agent_task_queue WHERE issue_id=$1 AND (context->>'work_method_revision')::int=$2", issueID, revision).Scan(&id); err != nil {
		t.Fatal(err)
	}
	task, err := testHandler.Queries.GetAgentTask(context.Background(), util.MustParseUUID(id))
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func TestMethodHandsOffToReviewAndReturnsToOriginalExecutor(t *testing.T) {
	seedTestCatalog(t)
	agent := seededReadyAgentID(t)
	squad := dbfx.Squad(t, "Method review", agent)
	suffix := workflowTestSuffix()
	member := dbfx.User(t, "Second executor", "method-"+suffix+"@example.com")
	dbfx.Member(t, testWorkspaceID, member, "member")
	project := deliveryProject(t, "Research handoff", agent)
	dbfx.Exec(t, "UPDATE project SET lead_type='member',lead_id=$1 WHERE id=$2", testUserID, project)
	custom := workmethod.Template{Name: "Evidence review " + suffix, Kind: "research", Keywords: []string{"evidence" + suffix}, Review: "Final review", Steps: []workmethod.Step{
		{Name: "Analysis", Inputs: "Dataset", Instructions: "Run comparisons", Output: "Analysis report", Checks: []string{"Check data source", "Check sample"}, Owner: &workmethod.Actor{Kind: "project_lead"}, Reviewer: &workmethod.Actor{Kind: "squad", ID: squad}},
		{Name: "Deliver", Output: "Decision record", Owner: &workmethod.Actor{Kind: "member", ID: member}, Reviewer: &workmethod.Actor{Kind: "member", ID: testUserID}},
	}}
	var saved workmethod.Template
	testutil.Call(t, testHandler.CreateWorkMethod, newRequest("POST", "/api/work-methods?workspace_id="+testWorkspaceID, custom)).Want(201).JSON(&saved)
	id := dbfx.Issue(t, "evidence"+suffix, testutil.Cols{"project_id": project, "status": "todo"})
	method := func(user, agentID, taskID string, body map[string]any, want int) workmethod.Run {
		t.Helper()
		req := goalRequest("POST", "/api/issues/"+id+"/work-method", user, body, map[string]string{"id": id})
		req.Header.Set("X-Agent-ID", agentID)
		req.Header.Set("X-Task-ID", taskID)
		rsp := testutil.Call(t, testHandler.ChangeIssueWorkMethod, req).Want(want)
		var got workmethod.Run
		if want == 200 {
			rsp.JSON(&got)
		}
		return got
	}
	checkOwner := func(kind, idWant string) {
		t.Helper()
		_, gotKind, gotID := issueStatusAndAssignee(t, id)
		if gotKind != kind || gotID != idWant {
			t.Fatalf("assignee %s/%s, want %s/%s", gotKind, gotID, kind, idWant)
		}
		run := readDeliveryMethod(t, id)
		if run.CurrentActor == nil || run.CurrentActor.Kind != kind || run.CurrentActor.ID != idWant {
			t.Fatalf("current actor %+v", run.CurrentActor)
		}
	}
	first := map[string]any{"action": "start", "base_revision": 0, "method_id": saved.ID}
	method(member, "", "", first, 200)
	checkOwner("member", testUserID)
	method(member, "", "", first, 409)
	method(member, "", "", map[string]any{"action": "select", "method_id": "okr", "base_revision": 1}, 403)
	updateIssueForTest(t, id, map[string]any{"status": "in_progress"}).Want(http.StatusOK)
	checkOwner("member", testUserID)
	dbfx.Exec(t, "UPDATE project SET lead_id=$1 WHERE id=$2", member, project)
	checkOwner("member", testUserID)
	advance := func(rev int) map[string]any {
		return map[string]any{"action": "advance", "base_revision": rev, "evidence": "Verified dataset and filed a report"}
	}
	method(member, "", "", advance(1), 403)
	method(testUserID, "", "", advance(1), 200)
	checkOwner("squad", squad)
	review := methodStageTask(t, id, 2)
	if !review.IsLeaderTask || uuidToString(review.SquadID) != squad || uuidToString(review.AgentID) != agent {
		t.Fatal(review)
	}
	brief := claimBriefForTest(t, id, review)
	for _, want := range []string{"Run comparisons", "Analysis report", "Check sample", `"checked_items":[0,1]`, "$MULTICA_CLI_PATH"} {
		if !strings.Contains(brief, want) {
			t.Fatal(brief)
		}
	}
	rework := map[string]any{"action": "rework", "base_revision": 2, "evidence": "Attach original source"}
	method(testUserID, "", "", rework, 403)
	method(testUserID, agent, uuidToString(review.ID), rework, 200)
	checkOwner("member", testUserID)
	method(testUserID, "", "", advance(3), 200)
	second := methodStageTask(t, id, 4)
	approve := map[string]any{"action": "approve", "base_revision": 4, "evidence": "Checked both sources", "checked_items": []int{0, 1}}
	method(testUserID, agent, uuidToString(review.ID), approve, 403)
	method(testUserID, agent, uuidToString(second.ID), approve, 200)
	checkOwner("member", member)
	method(member, "", "", advance(5), 200)
	checkOwner("member", testUserID)
	method(testUserID, "", "", map[string]any{"action": "approve", "base_revision": 6, "evidence": "Decision approved"}, 200)
	if run := readDeliveryMethod(t, id); run.State != "closed" || run.CurrentActor != nil {
		t.Fatal(run)
	}
	method(testUserID, "", "", advance(1), 409)
	if n := taskCountFor(t, id, agent); n != 2 {
		t.Fatalf("created %d tasks", n)
	}
}

func TestMethodDispatchRollsBackWhenAgentHasNoRuntime(t *testing.T) {
	suffix := workflowTestSuffix()
	agent := dbfx.Agent(t, "No runtime "+suffix, "")
	custom := workmethod.Template{Name: "No runtime", Kind: "research", Review: "Review", Steps: []workmethod.Step{{Name: "Study", Output: "Findings", Owner: &workmethod.Actor{Kind: "agent", ID: agent}, Reviewer: &workmethod.Actor{Kind: "creator"}}}}
	var saved workmethod.Template
	testutil.Call(t, testHandler.CreateWorkMethod, newRequest("POST", "/api/work-methods?workspace_id="+testWorkspaceID, custom)).Want(201).JSON(&saved)
	id := dbfx.Issue(t, "Unassigned "+suffix)
	change := map[string]any{"action": "select", "base_revision": 0, "method_id": saved.ID}
	req := withURLParam(newRequest("POST", "/api/issues/"+id+"/work-method", change), "id", id)
	testutil.Call(t, testHandler.ChangeIssueWorkMethod, req).Want(409)
	if readDeliveryMethod(t, id).Revision != 0 || taskCountFor(t, id, agent) != 0 {
		t.Fatal("failed dispatch advanced method or left a task")
	}
	_, _, assigned := issueStatusAndAssignee(t, id)
	if assigned != "" {
		t.Fatalf("failed dispatch left assignment %s", assigned)
	}
}

func TestConcurrentMethodStartAndAgentHandoffShareOneTaskPerRevision(t *testing.T) {
	rt := dbfx.Runtime(t, "Handoff runtime")
	agent := dbfx.Agent(t, "Method executor", rt)
	reviewer := dbfx.Agent(t, "Method reviewer", rt)
	custom := workmethod.Template{Name: "Peer review", Kind: "research", Review: "Check sources", Steps: []workmethod.Step{{Name: "Study", Output: "Report", Owner: &workmethod.Actor{Kind: "agent", ID: agent}, Reviewer: &workmethod.Actor{Kind: "agent", ID: reviewer}}}}
	var saved workmethod.Template
	testutil.Call(t, testHandler.CreateWorkMethod, newRequest("POST", "/api/work-methods?workspace_id="+testWorkspaceID, custom)).Want(201).JSON(&saved)
	id := dbfx.Issue(t, "Research study", testutil.Cols{"status": "todo"})
	request := func(body map[string]any) *http.Request {
		return withURLParam(newRequest("POST", "/api/issues/"+id+"/work-method", body), "id", id)
	}
	gates := make(chan struct{})
	codes := make(chan int, 2)
	for i := 0; i < 2; i++ {
		req := request(map[string]any{"action": "select", "method_id": saved.ID, "base_revision": 0})
		go func() {
			<-gates
			rec := httptest.NewRecorder()
			testHandler.ChangeIssueWorkMethod(rec, req)
			codes <- rec.Code
		}()
	}
	close(gates)
	first, second := <-codes, <-codes
	if !(first == 200 && second == 409 || first == 409 && second == 200) {
		t.Fatalf("concurrent start: %d %d", first, second)
	}
	task := methodStageTask(t, id, 1)
	if uuidToString(task.AgentID) != agent || uuidToString(task.OriginatorUserID) != testUserID || taskCountFor(t, id, agent) != 1 {
		t.Fatalf("wrong first task binding %s", uuidToString(task.ID))
	}
	submit := request(map[string]any{"action": "advance", "base_revision": 1, "evidence": "Report checked in"})
	submit.Header.Set("X-Agent-ID", agent)
	submit.Header.Set("X-Task-ID", uuidToString(task.ID))
	testutil.Call(t, testHandler.ChangeIssueWorkMethod, submit).Want(200)
	next := methodStageTask(t, id, 2)
	if uuidToString(next.AgentID) != reviewer || next.DelegatedFromTaskID != task.ID || next.OriginatorUserID != task.OriginatorUserID || next.OriginatorSource.String != "delegation" {
		t.Fatalf("reviewer task origin=%s parent=%s", next.OriginatorSource.String, uuidToString(next.DelegatedFromTaskID))
	}
	if brief := claimBriefForTest(t, id, task); !strings.Contains(brief, "End this run") || strings.Contains(brief, "--change-file") {
		t.Fatal(brief)
	}
}

func TestAgileDurableHandoffAfterLostNotification(t *testing.T) {
	ctx := context.Background()
	rt := dbfx.Runtime(t, "Agile restart")
	agent := dbfx.Agent(t, "Agile restart owner", rt)
	id := dbfx.Issue(t, "恢复敏捷步骤")
	issue, err := testHandler.Queries.GetIssue(ctx, util.MustParseUUID(id))
	if err != nil {
		t.Fatal(err)
	}
	tx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	task, err := testHandler.TaskService.CreateWorkMethodHandoff(ctx, db.New(tx), issue, util.MustParseUUID(agent), pgtype.UUID{}, 6, "Recover revision 6", util.MustParseUUID(testUserID), nil, pgtype.UUID{})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	// Simulate losing the process between commit and the post-commit wakeup.
	bus := events.New()
	queuedEvents := 0
	bus.Subscribe(protocol.EventTaskQueued, func(e events.Event) { queuedEvents++ })
	fresh := service.NewTaskService(db.New(testPool), testPool, nil, bus)
	for i := 0; i < 2; i++ {
		got, err := fresh.ClaimTasksForRuntimes(ctx, []pgtype.UUID{util.MustParseUUID(rt)}, 8)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 && (len(got) != 1 || got[0].ID != task.ID || methodBoundRevision(&got[0]) != 6) {
			t.Fatal(got)
		}
		if i == 1 && len(got) != 0 {
			t.Fatal("redelivered claimed task", got)
		}
	}

	// A delayed or repeated post-commit notification must not announce a
	// claimed task as queued again.
	for range 2 {
		if err := fresh.PublishWorkMethodHandoff(ctx, task); err != nil {
			t.Fatal(err)
		}
	}
	if queuedEvents != 1 {
		t.Fatalf("published %d queued events for one task", queuedEvents)
	}
}

func TestBoundMethodStepContextKeepsCurrentReviewEvidence(t *testing.T) {
	agent := seededReadyAgentID(t)
	custom := workmethod.Template{Name: "Scoped review " + workflowTestSuffix(), Kind: "development", Review: "Independent verdict", Steps: []workmethod.Step{{Name: "Inspect", Gate: "criteria", Output: "Findings", Checks: []string{"Check original"}, Owner: &workmethod.Actor{Kind: "member", ID: testUserID}, Reviewer: &workmethod.Actor{Kind: "agent", ID: agent}}}}
	var saved workmethod.Template
	testutil.Call(t, testHandler.CreateWorkMethod, newRequest("POST", "/api/work-methods?workspace_id="+testWorkspaceID, custom)).Want(201).JSON(&saved)
	id := dbfx.Issue(t, "Current step", testutil.Cols{"status": "todo"})
	change := func(body map[string]any) {
		req := withURLParam(newRequest("POST", "/api/issues/"+id+"/work-method", body), "id", id)
		testutil.Call(t, testHandler.ChangeIssueWorkMethod, req).Want(200)
	}
	change(map[string]any{"action": "select", "base_revision": 0, "method_id": saved.ID})
	dbfx.Exec(t, `UPDATE issue SET acceptance_criteria=$2 WHERE id=$1`, id, `{"revision":4,"criteria":[{"id":"C1","versions":["obsolete criterion","live criterion"]}]}`)
	change(map[string]any{"action": "advance", "base_revision": 1, "evidence": "original sample"})
	task := methodStageTask(t, id, 2)
	issue, err := testHandler.Queries.GetIssue(context.Background(), util.MustParseUUID(id))
	if err != nil {
		t.Fatal(err)
	}
	claim := func() *TaskProjectWorkflowData {
		return testHandler.claimProjectWorkflow(context.Background(), issue, &task, false)
	}
	if claim().StepContext {
		t.Fatal("unconfigured issue opted in")
	}
	var goalRun workmethod.Run
	if err := json.Unmarshal(issue.WorkMethodRun, &goalRun); err != nil {
		t.Fatal(err)
	}
	goalRun.GoalExecution = true
	if !usesCurrentStepContext(issue, goalRun, &task) {
		t.Fatal("goal development handoff did not default to the current-step brief")
	}
	issue.Metadata = []byte(`{"work_method_context":"full"}`)
	if usesCurrentStepContext(issue, goalRun, &task) {
		t.Fatal("explicit full context was ignored")
	}
	issue.Metadata = []byte(`{"work_method_context":"current_step"}`)
	scoped := claim()
	if !scoped.StepContext {
		t.Fatal(scoped)
	}
	for _, want := range []string{"live criterion", "original sample", "Check original", "checked_items", "rework", "Acceptance revision 4", "--current-case <ID>"} {
		if !strings.Contains(scoped.Brief, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(scoped.Brief, "obsolete criterion") {
		t.Fatal("included old version")
	}
	dbfx.Exec(t, `UPDATE issue SET metadata=$2 WHERE id=$1`, id, string(issue.Metadata))
	dbfx.Exec(t, `INSERT INTO agent_task_queue(agent_id,runtime_id,issue_id,status,started_at,completed_at,session_id,work_dir)
		VALUES ($1,$2,$3,'completed',now(),now(),'previous-review','/tmp/review-source')`, task.AgentID, task.RuntimeID, id)
	runtime, err := testHandler.Queries.GetAgentRuntime(context.Background(), task.RuntimeID)
	if err != nil {
		t.Fatal(err)
	}
	req := newOwnerDaemonRequest("POST", "/claim", nil)
	resp, _, _, _, _, failure := testHandler.buildClaimedTaskResponse(req, &task, runtime, uuidToString(task.RuntimeID), testWorkspaceID)
	if failure != nil || resp.ProjectWorkflow == nil || !resp.ProjectWorkflow.StepContext || resp.PriorSessionID != "" || resp.PriorWorkDir != "/tmp/review-source" {
		t.Fatalf("scoped claim failed: failure=%v session=%q dir=%q", failure, resp.PriorSessionID, resp.PriorWorkDir)
	}
	task.Context = nil
	if claim().StepContext {
		t.Fatal("unrelated run lost continuity")
	}
	task.TriggerCommentID = util.MustParseUUID(id)
	if !claim().StepContext {
		t.Fatal("step feedback lost current context")
	}
	task.DelegatedFromTaskID = util.MustParseUUID(id)
	dbfx.Cleanup(t, "DELETE FROM issue_wakeup WHERE issue_id=$1", id)
	rule := createRule(t, id, agent, pgtype.UUID{}, service.WakeupInput{Instruction: "Resume review on child completion", Kind: "event", EventTypes: []string{"task.completed"}})
	task.OriginatorUserID = util.MustParseUUID(testUserID)
	task.Context = []byte(`{"wakeup_id":"` + rule + `","wakeup_revision":1,"wakeup_evidence":{"facts":[{"event_type":"task.completed","payload":{"issue_id":"` + id + `","task_id":"child"}}]}}`)
	resp, _, _, _, _, failure = testHandler.buildClaimedTaskResponse(req, &task, runtime, uuidToString(task.RuntimeID), testWorkspaceID)
	if failure != nil || resp.ProjectWorkflow == nil || !resp.ProjectWorkflow.StepContext || resp.PriorSessionID != "" || resp.PriorWorkDir != "/tmp/review-source" {
		t.Fatalf("completion callback lost the current step or inherited the old session: %+v %v", resp.ProjectWorkflow, failure)
	}
	completionContext := string(task.Context)
	task.Context = []byte(strings.Replace(completionContext, `"issue_id":"`+id+`"`, `"issue_id":"other"`, 1))
	if claim().StepContext {
		t.Fatal("a different issue's completion unexpectedly discarded the session")
	}
	task.Context = []byte(strings.Replace(completionContext, "task.completed", "comment.created", 1))
	if claim().StepContext {
		t.Fatal("comment wakeup unexpectedly discarded its session")
	}
	task.Context = []byte(`{"wakeup_id":"timer"}`)
	if claim().StepContext {
		t.Fatal("timer unexpectedly discarded its session")
	}
	task.Context = nil
	task.DelegatedFromTaskID = pgtype.UUID{}
	task.RerunOfTaskID = util.MustParseUUID(id)
	if claim().StepContext {
		t.Fatal("manual retry lost continuity")
	}
	task.Context = []byte(`{"fresh_rerun":true}`)
	fresh := claim()
	if !fresh.StepContext || !strings.Contains(fresh.Brief, "Acceptance revision 4") {
		t.Fatal("fresh rerun of step feedback is missing the current acceptance index")
	}
	task.RetryOfTaskID = util.MustParseUUID(id)
	if claim().StepContext {
		t.Fatal("automatic retry lost continuity")
	}
}
