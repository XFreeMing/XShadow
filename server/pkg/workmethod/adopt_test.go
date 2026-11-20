package workmethod

import (
	"errors"
	"reflect"
	"testing"
)

func TestAdoptRevisionPreservesReviewedContractAndHandoff(t *testing.T) {
	old := agileTemplate()
	next := snapshot(old)
	next.Revision++
	next.Steps[1].Checks[0] = "updated check"
	next.Steps[2].Instructions = "read only the failing case"
	origEvent := Event{Step: 1, Action: "approve", CheckedItems: []int{0, 1, 2}, Actor: "agent/reviewer", Evidence: "signed the old checks"}
	actor := &Actor{Kind: "agent", ID: "original-assignee"}
	run := Run{State: "working", Step: 2, Method: &old, Revision: 8, CurrentActor: actor, History: []Event{origEvent}}
	base := run.Revision
	c := Change{Action: AdoptRevision, MethodID: old.ID, MethodRevision: &next.Revision, BaseRevision: &base, Evidence: "Use new handoff guidance from implementation"}
	apply := func() {
		t.Helper()
		if err := run.Adopt(c, []Template{*next}, "member/requester", "now"); err != nil {
			t.Fatal(err)
		}
	}
	apply()
	apply()
	if run.State != "working" || run.CurrentActor != actor || run.Step != 2 || run.Revision != 9 || len(run.History) != 2 || !reflect.DeepEqual(run.History[0], origEvent) || !reflect.DeepEqual(run.Method, next) {
		t.Fatalf("adopt changed ownership, position, history or method: %+v", run)
	}
	record := run.History[1]
	if !reflect.DeepEqual(record.PreviousMethod, &old) || record.MethodRevision != next.Revision || record.Step != 2 {
		t.Fatal(record)
	}
	if !run.AcceptsTaskRevision(8) || run.AcceptsTaskRevision(7) || run.AcceptsTaskRevision(10) {
		t.Fatal("wrong active handoff binding")
	}
	base = run.Revision
	apply()
	if run.Revision != 9 {
		t.Fatal("duplicate adoption")
	}
	if err := run.Apply(Change{Action: "advance", BaseRevision: &base, Evidence: "candidate"}, nil, "agent/executor", "later"); err != nil {
		t.Fatal(err)
	}
	if run.AcceptsTaskRevision(8) || run.AcceptsTaskRevision(9) {
		t.Fatal("old handoff remained valid after advance")
	}
	if err := run.Adopt(c, []Template{*next}, "member/test", "later"); !errors.Is(err, ErrConflict) {
		t.Fatal("adopted during review", err)
	}
}

func TestAdoptRevisionRefusesIncompatibleCatalogOrConcurrentEdit(t *testing.T) {
	old := agileTemplate()
	for _, tc := range []struct {
		name   string
		change func(*Template)
	}{
		{"different method", func(m *Template) { m.ID = "another" }},
		{"newer catalog", func(m *Template) { m.Revision++ }},
		{"gate", func(m *Template) { m.Steps[1].Gate = "delivery" }},
		{"step order", func(m *Template) { m.Steps[0], m.Steps[1] = m.Steps[1], m.Steps[0] }},
		{"reviewer", func(m *Template) {
			m.Steps[1].Owner = &Actor{Kind: "creator"}
			m.Steps[1].Reviewer = &Actor{Kind: "project_lead"}
		}},
		{"check count", func(m *Template) { m.Steps[0].Checks = append(m.Steps[0].Checks, "new mandatory signoff") }},
		{"rework", func(m *Template) { m.ReworkStep = 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := snapshot(old)
			m.Revision++
			target := m.Revision
			tc.change(m)
			run := Run{State: "working", Step: 2, Method: &old, Revision: 8}
			c := Change{Action: AdoptRevision, MethodID: old.ID, MethodRevision: &target, BaseRevision: &run.Revision, Evidence: "reason"}
			if err := run.Adopt(c, []Template{*m}, "member/test", "now"); !errors.Is(err, ErrConflict) || run.Revision != 8 || len(run.History) != 0 {
				t.Fatal(err, run)
			}
		})
	}
}
