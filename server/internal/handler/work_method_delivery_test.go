package handler

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/pkg/acceptance"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/workmethod"
)

func readDeliveryMethod(t *testing.T, id string) workmethod.Run {
	t.Helper()
	var run workmethod.Run
	req := withURLParam(newRequest(http.MethodGet, "/api/issues/"+id+"/work-method", nil), "id", id)
	testutil.Call(t, testHandler.GetIssueWorkMethod, req).Want(http.StatusOK).JSON(&run)
	return run
}

func TestGoalMethodRecommendationInheritedUnlessTaskHasExplicitDeliverable(t *testing.T) {
	goal := createGoalViaAPI(t, testUserID, map[string]any{
		"title": "提升留存", "intent": "关键结果按月复核，次月活跃人数占比提升到 75%。",
	})
	var goalMethod workmethod.Run
	testutil.Call(t, testHandler.GetGoalWorkMethod, goalRequest(http.MethodGet, "/api/goals/"+goal.ID+"/work-method", testUserID, nil, map[string]string{"id": goal.ID})).Want(http.StatusOK).JSON(&goalMethod)
	if goalMethod.Method == nil || goalMethod.Method.ID != "okr" {
		t.Fatalf("goal method: %+v", goalMethod)
	}

	for _, tc := range []struct{ title, want, source string }{
		{"本月工作包", "okr", "关联目标"},
		{"调研用户回访体验", "seven-step", "事项标题"},
	} {
		id := dbfx.Issue(t, tc.title)
		if got := linkGoalIssueViaAPI(t, testUserID, goal.ID, id); got != http.StatusNoContent {
			t.Fatalf("link %s: HTTP %d", id, got)
		}
		run := readDeliveryMethod(t, id)
		if run.Method == nil || run.Method.ID != tc.want || !strings.Contains(run.Reason, tc.source) || run.State != "ready" || run.Revision != 0 {
			t.Errorf("%q method: %+v", tc.title, run)
		}
	}
}

func TestAgileApprovalReopensWhenAcceptedCriterionChanges(t *testing.T) {
	id := dbfx.Issue(t, "实现下载报表", testutil.Cols{"status": "in_progress"})
	run := readDeliveryMethod(t, id)
	if run.Method == nil || run.Method.ID != "agile" {
		t.Fatalf("wanted agile: %+v", run)
	}
	// Existing issues keep the four pre-gate steps they started with.
	for i := range run.Method.Steps {
		run.Method.Steps[i].Gate = ""
	}
	run.State = "working"
	raw, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	dbfx.Exec(t, "UPDATE issue SET work_method_run=$1 WHERE id=$2", raw, id)
	path := "/api/issues/" + id
	method := func(action, evidence string, want int) {
		t.Helper()
		req := withURLParam(newRequest(http.MethodPost, path+"/work-method", map[string]any{"action": action, "evidence": evidence, "base_revision": run.Revision}), "id", id)
		response := testutil.Call(t, testHandler.ChangeIssueWorkMethod, req).Want(want)
		if want == http.StatusOK {
			response.JSON(&run)
		}
	}
	var trace acceptance.Trace
	accept := func(action string, fields map[string]any) {
		t.Helper()
		fields["action"], fields["criterion_id"], fields["base_revision"] = action, "C1", trace.Revision
		req := withURLParam(newRequest(http.MethodPost, path+"/acceptance", fields), "id", id)
		testutil.Call(t, testHandler.ChangeIssueAcceptance, req).Want(http.StatusOK).JSON(&trace)
	}
	for _, step := range run.Method.Steps {
		method("advance", step.Name+" 的交付记录", http.StatusOK)
	}
	if run.State != "review" {
		t.Fatalf("wanted review: %+v", run)
	}
	// Passing a method review requires current test and implementation evidence.
	method("approve", "已核对交付材料", http.StatusConflict)
	accept("criterion", map[string]any{"text": "下载 JSON 格式的报表"})
	accept("case", map[string]any{"case_id": "T1", "text": "下载报表并解析 JSON"})
	sha := strings.Repeat("a", 40)
	implementation := map[string]any{"repository": "org/reports", "branch": "dev", "commit": sha}
	result := map[string]any{"case_id": "T1", "result": "pass", "candidate": sha, "evidence": "CI/report-json"}
	accept("run", result)
	method("approve", "缺少实现的审查", http.StatusConflict)
	accept("implementation", implementation)
	method("approve", "v1 对应提交与用例通过", http.StatusOK)
	closedRevision := run.Revision
	if read := readDeliveryMethod(t, id); read.State != "closed" || read.Revision != closedRevision {
		t.Fatalf("persisted method: %+v", read)
	}

	accept("criterion", map[string]any{"text": "下载包含总计行的 JSON 报表"})
	run = readDeliveryMethod(t, id)
	if run.State != "review" || run.Revision <= closedRevision || len(trace.Criteria) != 1 || len(trace.Criteria[0].Impact().Retest) != 1 {
		t.Fatalf("criterion change did not reopen with outdated evidence: %+v %+v", run, trace)
	}
	method("approve", "只引用 v1 的证据", http.StatusConflict)
	result["evidence"] = "CI/report-json-with-total"
	accept("run", result)
	method("approve", "v2 尚无实现关联", http.StatusConflict)
	accept("implementation", implementation)
	method("approve", "v2 用例和提交已复核", http.StatusOK)
	if read := readDeliveryMethod(t, id); read.State != "closed" || read.History[len(read.History)-1].Evidence != "v2 用例和提交已复核" {
		t.Fatalf("v2 final review: %+v", read)
	}
}

func TestReopeningRoutedAgileReviewDispatchesOnce(t *testing.T) {
	agent := seededReadyAgentID(t)
	m := workmethod.Builtins()[0]
	for i := range m.Steps {
		m.Steps[i].Gate = ""
	}
	last := len(m.Steps) - 1
	m.Steps[last].Owner = &workmethod.Actor{Kind: "creator"}
	m.Steps[last].Reviewer = &workmethod.Actor{Kind: "agent", ID: agent}
	run := workmethod.Run{Method: &m, State: "closed", Step: last, Revision: 9}
	raw, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	id := dbfx.Issue(t, "已批准版本追加验收范围", testutil.Cols{"status": "in_progress", "work_method_run": raw})
	body := map[string]any{"action": "criterion", "criterion_id": "C2", "text": "新增的交付条件", "base_revision": 0}
	post := func(want int) {
		t.Helper()
		req := withURLParam(newRequest(http.MethodPost, "/api/issues/"+id+"/acceptance", body), "id", id)
		testutil.Call(t, testHandler.ChangeIssueAcceptance, req).Want(want)
	}
	post(http.StatusOK)
	post(http.StatusConflict)
	review := readDeliveryMethod(t, id)
	if review.State != "review" || review.Revision != 10 || taskCountFor(t, id, agent) != 1 {
		t.Fatalf("changed criteria should reopen the final review and queue its reviewer once: %+v", review)
	}
}

func TestAgileEvidenceEventsDriveBoardAndHandoff(t *testing.T) {
	seedTestCatalog(t)
	agent := seededReadyAgentID(t)
	m := workmethod.Builtins()[0]
	for i := range m.Steps {
		m.Steps[i].Owner = &workmethod.Actor{Kind: "member", ID: testUserID}
		m.Steps[i].Reviewer = &workmethod.Actor{Kind: "agent", ID: agent}
	}
	run := workmethod.Run{Method: &m, State: "working", Revision: 1}
	raw, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	id := dbfx.Issue(t, "实现敏捷交付闭环", testutil.Cols{"status": "todo", "work_method_run": raw})
	path := "/api/issues/" + id
	trace := acceptance.Trace{}
	accept := func(action string, fields map[string]any) {
		t.Helper()
		fields["action"], fields["criterion_id"], fields["base_revision"] = action, "AC1", trace.Revision
		req := withURLParam(newRequest("POST", path+"/acceptance", fields), "id", id)
		testutil.Call(t, testHandler.ChangeIssueAcceptance, req).Want(200).JSON(&trace)
		run = readDeliveryMethod(t, id)
	}
	move := func(action string, want int) {
		t.Helper()
		body := map[string]any{"action": action, "base_revision": run.Revision, "evidence": "核对原始证据"}
		req := goalRequest("POST", path+"/work-method", testUserID, body, map[string]string{"id": id})
		if action == "approve" {
			checked := make([]int, len(run.Method.Steps[run.Step].Checks))
			for i := range checked {
				checked[i] = i
			}
			body["checked_items"] = checked
			req = goalRequest("POST", path+"/work-method", testUserID, body, map[string]string{"id": id})
			task := methodStageTask(t, id, run.Revision)
			req.Header.Set("X-Agent-ID", agent)
			req.Header.Set("X-Task-ID", uuidToString(task.ID))
		}
		rsp := testutil.Call(t, testHandler.ChangeIssueWorkMethod, req).Want(want)
		if want == 200 {
			rsp.JSON(&run)
		}
	}
	board := func(want string) {
		t.Helper()
		if got, _, _ := issueStatusAndAssignee(t, id); got != want {
			t.Fatalf("step %d/%s status %s want %s", run.Step, run.State, got, want)
		}
	}
	updateIssueForTest(t, id, map[string]any{"status": "in_review"}).Want(http.StatusConflict)
	move("advance", 409)
	accept("criterion", map[string]any{"text": "候选提交可复验"})
	move("advance", 200)
	board("in_review")
	move("approve", 200)
	board("todo")
	move("advance", 409)
	for _, key := range []string{"T1", "T2"} {
		accept("case", map[string]any{"case_id": key, "text": "运行 " + key})
	}
	move("advance", 200)
	move("approve", 200)
	board("in_progress")
	sha := strings.Repeat("b", 40)
	accept("implementation", map[string]any{"repository": "xshadow", "branch": "dev", "commit": sha})
	result := map[string]any{"case_id": "T1", "candidate": sha, "result": "pass", "evidence": "CI/T1"}
	accept("run", result)
	result["case_id"], result["candidate"] = "T2", strings.Repeat("a", 40)
	accept("run", result)
	move("advance", 409)
	result["candidate"] = sha
	accept("run", result)
	move("advance", 200)
	reviewerTask := methodStageTask(t, id, run.Revision)
	brief := claimBriefForTest(t, id, reviewerTask)
	step := run.Method.Steps[run.Step]
	for _, instruction := range append([]string{step.Instructions, step.Output}, step.Checks...) {
		if !strings.Contains(brief, instruction) {
			t.Fatalf("candidate review brief missing %q", instruction)
		}
	}
	req := goalRequest("POST", path+"/work-method", testUserID, map[string]any{
		"action": "approve", "base_revision": run.Revision, "evidence": "缺少首项验证",
		"checked_items": []int{1, 2},
	}, map[string]string{"id": id})
	req.Header.Set("X-Agent-ID", agent)
	req.Header.Set("X-Task-ID", uuidToString(reviewerTask.ID))
	testutil.Call(t, testHandler.ChangeIssueWorkMethod, req).Want(http.StatusBadRequest)
	move("approve", 200)
	move("advance", 200)
	board("in_review")
	move("approve", 200)
	board("done")
	count := taskCountFor(t, id, agent)
	if count != 4 {
		t.Fatalf("wanted one reviewer task per step, got %d", count)
	}
	accept("criterion", map[string]any{"text": "增加边界输入"})
	board("todo")
	if run.Step != 0 || run.State != "working" || run.History[len(run.History)-1].Action != "rework" || len(trace.Criteria[0].Impact().Retest) != 2 {
		t.Fatal(run, trace)
	}
	revision := run.Revision
	accept("criterion", map[string]any{"text": "增加边界输入"})
	if run.Revision != revision || taskCountFor(t, id, agent) != count {
		t.Fatal("duplicate scope event advanced or dispatched")
	}
}

func TestAgileChangedEvidenceDispatchesResponsibleOwnerOnce(t *testing.T) {
	seedTestCatalog(t)
	agent := seededReadyAgentID(t)
	sha := strings.Repeat("a", 40)
	for _, tc := range []struct {
		action string
		fields map[string]any
		step   int
		board  string
	}{
		{"criterion", map[string]any{"text": "新范围"}, 0, "todo"},
		{"case", map[string]any{"case_id": "T2", "text": "复现异常"}, 1, "todo"},
		{"implementation", map[string]any{"repository": "org/app", "branch": "dev", "commit": strings.Repeat("b", 40)}, 2, "in_progress"},
		{"run", map[string]any{"case_id": "T1", "result": "fail", "candidate": sha, "evidence": "CI/failed"}, 2, "in_progress"},
	} {
		t.Run(tc.action, func(t *testing.T) {
			m := workmethod.Builtins()[0]
			m.Steps[tc.step].Owner = &workmethod.Actor{Kind: "agent", ID: agent}
			run := workmethod.Run{Method: &m, State: "closed", Step: 3, Revision: 9}
			trace := acceptance.Trace{Revision: 4, Criteria: []acceptance.Criterion{{
				ID: "C1", Versions: []string{"原范围"},
				Cases:           []acceptance.TestCase{{ID: "T1", Specification: "验证候选", Runs: []acceptance.Run{{Version: 1, Result: "pass", Candidate: sha, Evidence: "CI/pass"}}}},
				Implementations: []acceptance.Implementation{{Version: 1, Repository: "org/app", Branch: "dev", Commit: sha}},
			}}}
			methodJSON, err := json.Marshal(run)
			if err != nil {
				t.Fatal(err)
			}
			traceJSON, err := json.Marshal(trace)
			if err != nil {
				t.Fatal(err)
			}
			id := dbfx.Issue(t, "终验后修改交付证据", testutil.Cols{"status": "done", "work_method_run": methodJSON, "acceptance_criteria": traceJSON})
			tc.fields["action"], tc.fields["criterion_id"], tc.fields["base_revision"] = tc.action, "C1", trace.Revision
			for _, want := range []int{http.StatusOK, http.StatusConflict} {
				req := withURLParam(newRequest(http.MethodPost, "/api/issues/"+id+"/acceptance", tc.fields), "id", id)
				testutil.Call(t, testHandler.ChangeIssueAcceptance, req).Want(want)
			}
			updated := readDeliveryMethod(t, id)
			status, _, assignee := issueStatusAndAssignee(t, id)
			if updated.State != "working" || updated.Step != tc.step || updated.Revision != run.Revision+1 || status != tc.board || assignee != agent || taskCountFor(t, id, agent) != 1 {
				t.Fatalf("changed %s: method=%+v, board=%s, assignee=%s", tc.action, updated, status, assignee)
			}
		})
	}
}

func TestAgileProviderFailureKeepsCurrentBoardStep(t *testing.T) {
	seedTestCatalog(t)
	agent := seededReadyAgentID(t)
	m := workmethod.Builtins()[0]
	run := workmethod.Run{State: "working", Step: 2, Revision: 5, Method: &m}
	data, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	id := dbfx.Issue(t, "候选步骤等待重试", testutil.Cols{"status": "in_progress", "work_method_run": data})
	taskID := dbfx.Task(t, agent, testutil.Cols{
		"issue_id": id, "runtime_id": handlerTestRuntimeID(t), "status": "failed", "max_attempts": 1,
	})
	failed, err := testHandler.Queries.GetAgentTask(t.Context(), util.MustParseUUID(taskID))
	if err != nil {
		t.Fatal(err)
	}
	testHandler.TaskService.HandleFailedTasks(t.Context(), []db.AgentTaskQueue{failed})
	if status, _, _ := issueStatusAndAssignee(t, id); status != "in_progress" {
		t.Fatal(status)
	}
	if current := readDeliveryMethod(t, id); current.Revision != 5 || current.Step != 2 {
		t.Fatal(current)
	}
}
