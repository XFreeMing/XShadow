package acceptance

import (
	"errors"
	"strings"
	"testing"
)

func TestCriterionChangePreservesEvidenceAndScopesImpact(t *testing.T) {
	trace, err := Decode([]byte("[]"))
	if err != nil {
		t.Fatal(err)
	}
	apply := func(op Change) {
		t.Helper()
		revision := trace.Revision
		op.BaseRevision = &revision
		if err := trace.Apply(op); err != nil {
			t.Fatal(err)
		}
	}
	commit := strings.Repeat("a", 40)
	for _, id := range []string{"A", "B"} {
		apply(Change{Action: "criterion", CriterionID: id, Text: id})
		apply(Change{Action: "implementation", CriterionID: id, Repository: "org/repo", Branch: "dev", Commit: commit})
		for _, suffix := range []string{"1", "2"} {
			apply(Change{Action: "case", CriterionID: id, CaseID: id + suffix, Text: "Check output"})
			apply(Change{Action: "run", CriterionID: id, CaseID: id + suffix, Candidate: commit, Evidence: "log", Result: "pass"})
		}
	}
	apply(Change{Action: "criterion", CriterionID: "A", Text: "Revised"})
	a, b := trace.Criteria[0], trace.Criteria[1]
	if len(a.Impact().Retest) != 2 || a.Impact().CurrentImplementation || len(b.Impact().Retest) != 0 || !b.Impact().CurrentImplementation {
		t.Fatalf("incorrect impact: %+v %+v", a.Impact(), b.Impact())
	}
	if a.Versions[0] != "A" || a.Implementations[0].Version != 1 || a.Cases[0].Runs[0].Version != 1 {
		t.Fatal("lost historical links")
	}
	apply(Change{Action: "run", CriterionID: "A", CaseID: "A1", Candidate: commit, Evidence: "log2", Result: "fail"})
	impact := trace.Criteria[0].Impact()
	if len(impact.Failed) != 1 || impact.Failed[0] != "A1" || len(impact.Retest) != 1 || impact.Retest[0] != "A2" {
		t.Fatal(impact)
	}
	zero := 0
	if err := trace.Apply(Change{BaseRevision: &zero, Action: "criterion", CriterionID: "A", Text: "stale"}); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if err := trace.Apply(Change{BaseRevision: &trace.Revision, Action: "case", CriterionID: "B", CaseID: "A1", Text: "reuse"}); err == nil {
		t.Fatal("case reused across criteria")
	}
}
