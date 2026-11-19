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

func TestCandidateReplacementAndRollbackRequireMatchingCaseEvidence(t *testing.T) {
	trace := Trace{}
	apply := func(op Change) {
		t.Helper()
		op.BaseRevision = &trace.Revision
		op.CriterionID = "AC1"
		if err := trace.Apply(op); err != nil {
			t.Fatal(err)
		}
	}
	apply(Change{Action: "criterion", Text: "可复验"})
	apply(Change{Action: "case", CaseID: "T1", Text: "验证交付"})
	a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
	link := Change{Action: "implementation", Repository: "org/project", Branch: "dev", Commit: a}
	apply(link)
	apply(Change{Action: "run", CaseID: "T1", Candidate: a, Result: "pass", Evidence: "CI/1"})
	link.Commit = b
	apply(link)
	if len(trace.Criteria[0].Impact().Retest) != 1 {
		t.Fatal("new candidate inherited passing evidence")
	}
	link.Commit = a
	apply(link)
	revision := trace.Revision
	apply(link)
	if len(trace.Criteria[0].Impact().Retest) != 0 || trace.Revision != revision || len(trace.Criteria[0].Implementations) != 3 {
		t.Fatal(trace)
	}
}

func TestRetiredCaseKeepsHistoryAndLeavesRequiredSet(t *testing.T) {
	trace := Trace{}
	apply := func(op Change) {
		t.Helper()
		op.BaseRevision = &trace.Revision
		op.CriterionID = "C1"
		if err := trace.Apply(op); err != nil {
			t.Fatal(err)
		}
	}
	commit := strings.Repeat("c", 40)
	apply(Change{Action: "criterion", Text: "检索命中"})
	apply(Change{Action: "implementation", Repository: "org/repo", Branch: "dev", Commit: commit})
	apply(Change{Action: "case", CaseID: "C1-T1", Text: "原文一"})
	apply(Change{Action: "case", CaseID: "C1-T2", Text: "原文二"})
	apply(Change{Action: "case", CaseID: "C1-T3", Text: "原文三"})
	apply(Change{Action: "run", CaseID: "C1-T2", Candidate: commit, Result: "fail", Evidence: "run-1"})

	apply(Change{Action: "case_retire", CaseID: "C1-T2", Reason: "被 C1-T1 更正替代", SupersededBy: "C1-T1"})
	apply(Change{Action: "case_retire", CaseID: "C1-T3", Reason: "不再执行"})

	impact := trace.Criteria[0].Impact()
	if len(impact.Unrun) != 1 || impact.Unrun[0] != "C1-T1" {
		t.Fatalf("retired case still required: %+v", impact)
	}
	if len(impact.Retest) != 0 || len(impact.Failed) != 0 {
		t.Fatalf("retired case leaked into gates: %+v", impact)
	}
	if len(impact.Retired) != 2 || impact.Retired[0] != "C1-T2" || impact.Retired[1] != "C1-T3" {
		t.Fatalf("retired cases not reported: %+v", impact)
	}
	retired := trace.Criteria[0].Cases[1]
	if retired.Specification != "原文二" || len(retired.Runs) != 1 || retired.Runs[0].Evidence != "run-1" {
		t.Fatalf("retirement dropped history: %+v", retired)
	}
	if retired.Retired == nil || retired.Retired.SupersededBy != "C1-T1" || retired.Retired.Reason == "" || retired.Retired.Revision != trace.Revision-1 {
		t.Fatalf("retirement metadata incorrect: %+v", retired.Retired)
	}
	for _, c := range trace.Criteria[0].Cases {
		for _, run := range c.Runs {
			if run.Result == "pass" {
				t.Fatalf("retirement fabricated a pass on %s", c.ID)
			}
		}
	}

	revision := trace.Revision
	if err := trace.Apply(Change{BaseRevision: &revision, Action: "case_retire", CriterionID: "C1", CaseID: "C1-T2", Reason: "again"}); err == nil {
		t.Fatal("duplicate retirement accepted")
	}
	revision = trace.Revision
	if err := trace.Apply(Change{BaseRevision: &revision, Action: "case_retire", CriterionID: "C1", CaseID: "C1-T9", Reason: "unknown"}); err == nil {
		t.Fatal("unknown case accepted")
	}
	revision = trace.Revision
	if err := trace.Apply(Change{BaseRevision: &revision, Action: "case_retire", CriterionID: "C1", CaseID: "C1-T1", Reason: ""}); err == nil {
		t.Fatal("retirement without reason accepted")
	}
	applyOther := func(op Change) {
		t.Helper()
		op.BaseRevision = &trace.Revision
		op.CriterionID = "C2"
		if err := trace.Apply(op); err != nil {
			t.Fatal(err)
		}
	}
	applyOther(Change{Action: "criterion", Text: "另一个标准"})
	applyOther(Change{Action: "case", CaseID: "C2-T1", Text: "其他用例"})
	revision = trace.Revision
	if err := trace.Apply(Change{BaseRevision: &revision, Action: "case_retire", CriterionID: "C1", CaseID: "C1-T1", Reason: "替换", SupersededBy: "C2-T1"}); err == nil {
		t.Fatal("cross-criterion superseded_by accepted")
	}
	revision = trace.Revision
	if err := trace.Apply(Change{BaseRevision: &revision, Action: "case_retire", CriterionID: "C1", CaseID: "C1-T1", Reason: "替换", SupersededBy: "C1-T9"}); err == nil {
		t.Fatal("unknown superseded_by accepted")
	}
	revision = trace.Revision
	if err := trace.Apply(Change{BaseRevision: &revision, Action: "case_retire", CriterionID: "C1", CaseID: "C1-T1", Reason: "自己", SupersededBy: "C1-T1"}); err == nil {
		t.Fatal("self superseded_by accepted")
	}
}

func TestLegacyLedgerDecodesWithoutRetirement(t *testing.T) {
	legacy := []byte(`{"revision":43,"criteria":[{"id":"C1","versions":["原文标准"],"cases":[{"id":"C1-T1","specification":"原文一","runs":[{"version":1,"result":"pass","candidate":"` + strings.Repeat("a", 40) + `","evidence":"log"}]}],"implementations":[{"version":1,"repository":"org/repo","branch":"dev","commit":"` + strings.Repeat("a", 40) + `"}]}]}`)
	trace, err := Decode(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if trace.Revision != 43 || len(trace.Criteria) != 1 || trace.Criteria[0].Cases[0].Retired != nil {
		t.Fatalf("legacy ledger changed shape: %+v", trace)
	}
	impact := trace.Criteria[0].Impact()
	if len(impact.Retired) != 0 || len(impact.Unrun) != 0 || len(impact.Failed) != 0 {
		t.Fatalf("legacy impact incorrect: %+v", impact)
	}
}
