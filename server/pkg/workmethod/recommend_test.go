package workmethod

import (
	"slices"
	"strings"
	"testing"
)

func TestRecommendationsExplainTaskAndGoalSignals(t *testing.T) {
	methods := Builtins()
	methods[1].ID = "research"
	m := methods[1]
	m.ID = "interviews"
	m.Name = "访谈"
	m.Keywords = []string{"定性访谈"}
	methods = append(methods, m)
	tests := []struct{ title, body, goal, method, reason string }{
		{"研发增量：改进搜索", "", "提升留存率", "agile", "事项标题"},
		{"调研研发流程的返工原因", "", "提升转化率", "research", "调研"},
		{"第一轮", "请调研研发流程", "提高留存率", "research", "事项交付物"},
		{"第一轮", "交付物：竞品分析报告", "提高留存率", "research", "事项交付物"},
		{"第一轮", "背景：曾经研究过开发工具", "提高转化率", "okr", "关联目标"},
		{"第一轮", "参考：测试、Git、代码示例", "", "", "手动选择"},
		{"定义下一步", "", "定性访谈新顾客", "interviews", "定性访谈"},
		{"reimplementation tutorial", "", "", "", "手动选择"},
		{"Implement recommendations", "", "Investigate root cause", "agile", "implement"},
		{"研发和调研", "", "提升转化率", "", "同时匹配"},
	}
	for _, tc := range tests {
		result := Recommend(methods, tc.title, tc.body, tc.goal)
		id := ""
		if result.Method != nil {
			id = result.Method.ID
		}
		if tc.method != id || !strings.Contains(strings.ToLower(result.Reason), tc.reason) {
			t.Errorf("%q %q %q => %q (%s), want %q (%s)", tc.title, tc.body, tc.goal, id, result.Reason, tc.method, tc.reason)
		}
	}
}

func TestEquallyMatchingCustomMethodsRemainUnselected(t *testing.T) {
	a := Builtins()[1]
	a.ID = "research"
	a.Keywords = []string{"访谈"}
	b := a
	b.ID = "interview"
	b.Name = "访谈复核"
	methods := []Template{a, b}
	first := Recommend(methods, "访谈样本", "", "")
	slices.Reverse(methods)
	second := Recommend(methods, "访谈样本", "", "")
	if first.Method != nil || second.Method != nil || first.Reason != second.Reason {
		t.Fatalf("%+v vs %+v", first, second)
	}
}

func TestRetiredSevenStepIsAvailableOnlyToExistingReferences(t *testing.T) {
	methods := Catalog(nil)
	old, exists := ResolvePlanMethod(methods, "", "seven-step")
	if !exists || len(old.Steps) != 7 {
		t.Fatal("existing plan reference no longer resolves")
	}
	if run := Recommend(methods, "七步问题解决法", "", ""); run.Method != nil {
		t.Fatalf("retired method was recommended: %s", run.Method.ID)
	}
	if slices.ContainsFunc(ActiveCatalog(methods), func(method Template) bool { return method.ID == old.ID }) {
		t.Fatal("retired method appeared in the selectable catalog")
	}
	run := Run{State: "ready"}
	revision := 0
	selectOld := Change{Action: "select", MethodID: old.ID, BaseRevision: &revision}
	if err := run.Apply(selectOld, methods, "member/test", "now"); err == nil {
		t.Fatal("retired method was selected for a new task")
	}
	run = Run{State: "working", Method: &old}
	if err := run.Apply(Change{Action: "advance", Evidence: "已确认原范围", BaseRevision: &revision}, methods, "member/test", "now"); err != nil || run.Step != 1 {
		t.Fatalf("frozen run could not continue: %+v, %v", run, err)
	}
}
