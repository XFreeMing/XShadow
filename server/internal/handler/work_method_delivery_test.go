package handler

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/acceptance"
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
