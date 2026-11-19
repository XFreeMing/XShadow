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
	m := Template{ID: "ba5901a4-2ed7-4248-af1e-f93db90378b0", Name: "访谈手册", Kind: "research", Keywords: []string{}, Steps: []Step{{"访谈", "登记原始访谈记录"}}, Review: "确认依据与结论一致"}
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
