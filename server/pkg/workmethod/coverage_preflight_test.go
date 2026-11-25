package workmethod

import (
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/pkg/acceptance"
)

func TestCoveragePreflightReusesOnlyCurrentCaseAndCriterion(t *testing.T) {
	trace := acceptance.Trace{Criteria: []acceptance.Criterion{{
		ID: "C1", Versions: []string{"v1"}, Cases: []acceptance.TestCase{
			{ID: "legacy", Specification: "Preserve empty offset"},
			{ID: "new", Specification: "Update the complete excerpt"},
			{ID: "old", Retired: &acceptance.Retirement{}},
		},
	}}}
	baseline := CoveragePreflight{
		CriterionID: "C1", CriterionVersion: 1, CaseID: "legacy", CaseVersion: 1, Command: "./test legacy",
		Baseline: CoverageRun{Commit: strings.Repeat("a", 40), Result: "pass", Evidence: "legacy receipt"},
	}
	run := Run{History: []Event{{CoveragePreflight: []CoveragePreflight{baseline}}}}
	comparison := CoveragePreflight{
		CriterionID: "C1", CriterionVersion: 1, CaseID: "new", CaseVersion: 1, Command: "./test new",
		Baseline: CoverageRun{Commit: strings.Repeat("a", 40), Result: "fail", Evidence: "missing update"},
	}
	check := func(proof CoveragePreflight, wantErr bool) {
		t.Helper()
		if err := run.CheckCoveragePreflight(trace, []CoveragePreflight{proof}); (err != nil) != wantErr {
			t.Fatalf("want error=%v, got %v", wantErr, err)
		}
	}
	check(comparison, true)
	comparison.Reference = &CoverageRun{Commit: strings.Repeat("b", 40), Result: "pass", Evidence: "updated body"}
	check(comparison, false)
	trace.Criteria[0].Cases[1].PreviousSpecifications = []acceptance.CaseSpecification{{Text: "Select excerpt only"}}
	check(comparison, true)
	comparison.CaseVersion = 2
	check(comparison, false)
	trace.Criteria[0].Versions = append(trace.Criteria[0].Versions, "v2")
	comparison.CriterionVersion = 2
	check(comparison, true)
}
