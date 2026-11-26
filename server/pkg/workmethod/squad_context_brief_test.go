package workmethod

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/pkg/acceptance"
)

func TestSquadContextBriefPinsActiveCaseAndPreflight(t *testing.T) {
	sample := strings.Repeat("a", 64)
	baseline := strings.Repeat("b", 40)
	trace := acceptance.Trace{Revision: 4, Criteria: []acceptance.Criterion{{
		ID: "AC1", Versions: []string{"Open the exact source paragraph"}, Cases: []acceptance.TestCase{
			{ID: "B01", Specification: "Open the same paragraph in edition two", ArtifactSHA256: sample,
				PreviousSpecifications: []acceptance.CaseSpecification{{Text: "Open edition one"}}},
			{ID: "old", Specification: "Old path", Retired: &acceptance.Retirement{Revision: 3}},
		},
	}}}
	run := Run{Method: &Template{Revision: 3}, History: []Event{
		{Action: "advance", CoveragePreflight: []CoveragePreflight{
			{CriterionID: "AC1", CriterionVersion: 1, CaseID: "B01", CaseVersion: 2,
				Command: "./verify B01", Baseline: CoverageRun{Commit: baseline}},
			{CriterionID: "AC1", CriterionVersion: 1, CaseID: "B01", CaseVersion: 1, Command: "./obsolete"},
		}},
		{Action: "approve"},
	}}
	brief := run.SquadContextBrief("Read the saved citation", trace)
	if len(brief.Criteria) != 1 || len(brief.Criteria[0].Cases) != 1 {
		t.Fatalf("want only active case references: %+v", brief.Criteria)
	}
	want := SquadBriefCase{ID: "B01", Version: 2, ArtifactSHA256: sample, Command: "./verify B01", BaselineCommit: baseline}
	if got := brief.Criteria[0].Cases[0]; got != want {
		t.Fatalf("brief case %+v, want %+v", got, want)
	}
	ledgerMatches := func() bool {
		t.Helper()
		raw, err := json.Marshal(trace)
		if err != nil {
			t.Fatal(err)
		}
		return brief.MatchesAcceptance(raw)
	}
	trace.Criteria[0].Cases[0].Runs = []acceptance.Run{{CaseVersion: 2, Result: "pass"}}
	trace.Revision++
	if !ledgerMatches() {
		t.Fatal("adding behavior evidence changed the input contract")
	}
	trace.Criteria[0].Cases[0].ArtifactSHA256 = strings.Repeat("c", 64)
	if ledgerMatches() || brief.Criteria[0].Cases[0].ArtifactSHA256 != sample {
		t.Fatal("the changed sample still matched or overwrote the historical brief")
	}
	trace.Criteria[0].Cases[0].ArtifactSHA256 = sample
	trace.Criteria[0].Cases[0].PreviousSpecifications = append(trace.Criteria[0].Cases[0].PreviousSpecifications,
		acceptance.CaseSpecification{Text: trace.Criteria[0].Cases[0].Specification, ArtifactSHA256: sample})
	if ledgerMatches() {
		t.Fatal("the changed case revision still matched")
	}
}
