package workmethod

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func change(r *Run, action, evidence string) Change {
	base := r.Revision
	return Change{Action: action, Evidence: evidence, BaseRevision: &base}
}

func apply(t *testing.T, r *Run, c Change, methods []Template) {
	t.Helper()
	if err := r.Apply(c, methods, "member/tester", "2026-11-19T07:00:00Z"); err != nil {
		t.Fatal(err)
	}
}

func rejected(t *testing.T, r *Run, c Change) {
	t.Helper()
	before, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Apply(c, Builtins(), "member/tester", "2026-11-19T07:00:00Z"); err == nil {
		t.Fatalf("%s accepted in %s", c.Action, r.State)
	}
	after, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("rejected change mutated run: before %s, after %s", before, after)
	}
}

func TestBuiltinMethodsRequireEvidenceReviewAndAllowRework(t *testing.T) {
	methods := Builtins()
	for _, m := range methods {
		t.Run(m.ID, func(t *testing.T) {
			if err := Validate(m); err != nil {
				t.Fatal(err)
			}
			r := Recommend(methods, m.Keywords[0], "", "")
			if r.Method == nil || r.Method.ID != m.ID || r.State != "ready" {
				t.Fatalf("missing recommendation: %+v", r)
			}
			rejected(t, &r, change(&r, "approve", "premature"))
			rejected(t, &r, change(&r, "advance", " "))
			for i := range m.Steps {
				if r.Step != i {
					t.Fatalf("wrong step %d", r.Step)
				}
				apply(t, &r, change(&r, "advance", "first-round artifact"), methods)
				if r.History[i].Step != i || r.History[i].Actor != "member/tester" || r.History[i].At == "" {
					t.Fatalf("incorrect event %+v", r.History[i])
				}
			}
			if r.State != "review" {
				t.Fatal(r.State)
			}
			rejected(t, &r, change(&r, "advance", "skip review"))
			rejected(t, &r, change(&r, "approve", ""))
			rejected(t, &r, change(&r, "rework", ""))
			invalid := change(&r, "rework", "refine")
			invalid.TargetStep = new(int)
			*invalid.TargetStep = len(m.Steps)
			rejected(t, &r, invalid)
			apply(t, &r, change(&r, "rework", "insufficient evidence"), methods)
			if r.State != "working" || r.Step != m.ReworkStep {
				t.Fatal(r)
			}
			rejected(t, &r, change(&r, "approve", "shortcut"))
			for i := m.ReworkStep; i < len(m.Steps); i++ {
				apply(t, &r, change(&r, "advance", "revised artifact"), methods)
			}
			apply(t, &r, change(&r, "approve", "reviewed revised artifact"), methods)
			if r.State != "closed" || r.Revision != len(r.History) || r.History[len(m.Steps)].Action != "rework" {
				t.Fatal(r)
			}
			rejected(t, &r, change(&r, "rework", "late"))
		})
	}
}

func TestSelectionOverridesBeforeWorkAndFreezesTemplate(t *testing.T) {
	methods := Builtins()
	r := Recommend(methods, "miscellaneous", "", "")
	if r.Method != nil {
		t.Fatal(r.Method)
	}
	rejected(t, &r, change(&r, "advance", "artifact"))
	c := change(&r, "select", "")
	c.MethodID = "okr"
	apply(t, &r, c, methods)
	c = change(&r, "select", "")
	c.MethodID = "seven-step"
	apply(t, &r, c, methods)
	if r.Method.ID != "seven-step" || r.State != "working" || !strings.Contains(r.Reason, "手动选择") {
		t.Fatal(r)
	}
	methods[1].Steps[0].Name = "replaced step"
	methods[1].Keywords[0] = "replaced keyword"
	if r.Method.Steps[0].Name == methods[1].Steps[0].Name || r.Method.Keywords[0] == methods[1].Keywords[0] {
		t.Fatal("snapshot referenced live catalog slices")
	}
	apply(t, &r, change(&r, "advance", "research question"), nil)
	c = change(&r, "select", "")
	c.MethodID = "okr"
	rejected(t, &r, c)
	stale := change(&r, "advance", "overwrite")
	base := 0
	stale.BaseRevision = &base
	if err := r.Apply(stale, methods, "member/tester", "now"); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	rejected(t, &r, Change{Action: "advance", Evidence: "missing base"})
}

func TestCustomMethodPublishValidation(t *testing.T) {
	m := Template{ID: "ba5901a4-2ed7-4248-af1e-f93db90378b0", Name: "访谈手册", Kind: "research", Keywords: []string{}, Steps: []Step{{Name: "访谈", Output: "登记原始访谈记录"}}, Review: "确认依据与结论一致"}
	if err := Validate(m); err != nil {
		t.Fatal(err)
	}
	r := Recommend([]Template{m}, "访谈手册", "", "")
	if r.Method != nil {
		t.Fatal("manual-only method matched by name")
	}
	for _, invalidate := range []func(*Template){
		func(t *Template) { t.ID = "*" }, func(t *Template) { t.Kind = "bogus" }, func(t *Template) { t.Name = "" }, func(t *Template) { t.Steps = []Step{} }, func(t *Template) { t.Steps = []Step{{Name: "s"}} }, func(t *Template) { t.Review = "" }, func(t *Template) { t.ReworkStep = 1 }, func(t *Template) { t.Keywords = []string{" "} },
	} {
		bad := m
		invalidate(&bad)
		if err := Validate(bad); err == nil {
			t.Fatal(bad)
		}
	}
}

func TestMixedStepReviewResubmission(t *testing.T) {
	m := Builtins()[0]
	owner := &Actor{Kind: "creator"}
	reviewer := &Actor{Kind: "project_lead"}
	m.Steps[1].Owner, m.Steps[1].Reviewer = owner, reviewer
	m.Steps[2].Owner, m.Steps[2].Reviewer = owner, reviewer
	methods := []Template{m}
	r := Recommend(methods, "开发产品面板", "", "")
	owner.Kind = "invalid"
	m.Steps[1].Checks[0] = "changed"
	if r.Method.Steps[1].Owner.Kind != "creator" || r.Method.Steps[1].Checks[0] == "changed" {
		t.Fatal("mutable run snapshot")
	}
	transition := func(action string, step int, state string) {
		t.Helper()
		c := change(&r, action, "artifact or decision")
		if action == "approve" && r.Method.Steps[r.Step].Reviewer != nil {
			c.CheckedItems = []int{0, 1}
		}
		apply(t, &r, c, methods)
		if r.Step != step || r.State != state {
			t.Fatalf("%s reached %d/%s, want %d/%s", action, r.Step, r.State, step, state)
		}
	}
	transition("advance", 1, "working")
	transition("advance", 1, "review")
	c := change(&r, "rework", "premature jump")
	ii := 2
	c.TargetStep = &ii
	rejected(t, &r, c)
	transition("rework", 1, "working")
	transition("advance", 1, "review")
	transition("approve", 2, "working")
	transition("advance", 2, "review")
	c = change(&r, "rework", "repeat initial deliverable")
	ii = 0
	c.TargetStep = &ii
	apply(t, &r, c, methods)
	last := r.History[len(r.History)-1]
	if r.Step != 0 || last.Step != 0 || last.FromStep == nil || *last.FromStep != 2 {
		t.Fatal(r)
	}
	transition("advance", 1, "working")
	transition("advance", 1, "review")
	transition("approve", 2, "working")
	transition("advance", 2, "review")
	transition("approve", 3, "working")
	transition("advance", 3, "review")
	transition("approve", 3, "closed")
}

func TestStepContracts(t *testing.T) {
	for _, m := range Builtins() {
		for _, s := range m.Steps {
			if s.Instructions == "" || s.Inputs == "" || len(s.Checks) == 0 {
				t.Fatalf("missing guidance in %s/%s", m.ID, s.Name)
			}
		}
	}
	for _, tc := range []struct {
		owner, reviewer *Actor
		valid           bool
	}{
		{nil, nil, true}, {&Actor{Kind: "creator"}, nil, false},
		{nil, &Actor{Kind: "creator"}, false}, {&Actor{Kind: "creator", ID: "fake"}, &Actor{Kind: "creator"}, false},
		{&Actor{Kind: "squad", ID: "wrong"}, &Actor{Kind: "creator"}, false},
		{&Actor{Kind: "project_lead"}, &Actor{Kind: "invalid"}, false},
		{&Actor{Kind: "member", ID: "74a3bae4-e7c5-4e07-8d0f-845605e77df7"}, &Actor{Kind: "creator"}, true},
	} {
		m := Builtins()[0]
		m.Steps[0].Owner, m.Steps[0].Reviewer = tc.owner, tc.reviewer
		if err := Validate(m); (err == nil) != tc.valid {
			t.Fatalf("%+v %+v: %v", tc.owner, tc.reviewer, err)
		}
	}
	legacy := Template{ID: "legacy"}
	c := Catalog([]Template{legacy})
	if c[3].ID != "legacy" || c[3].Revision != 1 {
		t.Fatal(c)
	}
}

func TestStartAndStepReviewEvidence(t *testing.T) {
	methods := Builtins()
	step := &methods[1].Steps[0]
	step.Owner, step.Reviewer = &Actor{Kind: "creator"}, &Actor{Kind: "project_lead"}
	r := Recommend(methods, "调研用户行为", "", "")
	initial := r
	c := change(&r, "start", "")
	c.MethodID = "agile"
	if err := r.Apply(c, methods, "member/test", "now"); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	c.MethodID = "seven-step"
	apply(t, &r, c, methods)
	if r.State != "working" || r.Reason != initial.Reason || r.Source != "title" || r.Step != 0 {
		t.Fatal(r)
	}
	c = change(&r, "select", "")
	c.MethodID = "agile"
	rejected(t, &r, c)
	apply(t, &r, change(&r, "advance", "采访计划"), methods)
	for _, invalid := range [][]int{nil, {0}, {0, 0}, {-1, 1}, {0, len(step.Checks)}} {
		c = change(&r, "approve", "同意")
		c.CheckedItems = invalid
		rejected(t, &r, c)
	}
	c = change(&r, "rework", "需要补充成功条件")
	c.CheckedItems = []int{1}
	apply(t, &r, c, methods)
	c.CheckedItems[0] = 0
	last := r.History[len(r.History)-1]
	if len(last.CheckedItems) != 1 || last.CheckedItems[0] != 1 {
		t.Fatal(last)
	}
	apply(t, &r, change(&r, "advance", "修订版"), methods)
	c = change(&r, "approve", "逐项核对")
	for i := len(step.Checks) - 1; i >= 0; i-- {
		c.CheckedItems = append(c.CheckedItems, i)
	}
	apply(t, &r, c, methods)
	if r.State != "working" || r.Step != 1 || len(r.History[len(r.History)-1].CheckedItems) != len(step.Checks) {
		t.Fatal(r)
	}
}

func TestAgileUpgradePreservesRoleOverridesAndFrozenProcedures(t *testing.T) {
	old := legacyAgile()
	old.Revision = 7
	old.Steps[0].Owner = &Actor{Kind: "creator"}
	old.Steps[0].Reviewer = &Actor{Kind: "project_lead"}
	upgraded := Catalog([]Template{old})[0]
	if upgraded.Revision != 8 || upgraded.Steps[0].Gate != "criteria" || upgraded.Steps[0].Reviewer.Kind != "project_lead" || old.Steps[0].Gate != "" {
		t.Fatal(upgraded)
	}
	old.Steps[0].Checks = []string{"custom check"}
	if custom := Catalog([]Template{old})[0]; custom.Revision != 7 || custom.Steps[0].Gate != "" {
		t.Fatal(custom)
	}
}
