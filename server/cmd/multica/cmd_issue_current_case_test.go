package main

import (
	"encoding/json"
	"github.com/multica-ai/multica/server/pkg/acceptance"
	"strings"
	"testing"
)

func TestCurrentCasePreservesCandidateAndRuleRevisions(t *testing.T) {
	trace := acceptance.Trace{Revision: 18, Criteria: []acceptance.Criterion{{
		ID: "C4", Versions: []string{"superseded rule", "current rule"},
		Implementations: []acceptance.Implementation{{Version: 2, Commit: "current"}, {Version: 1, Commit: "old"}},
		Cases: []acceptance.TestCase{
			{ID: "left", Specification: "open the left occurrence", Runs: []acceptance.Run{
				{Version: 1, Candidate: "old", Result: "fail", Evidence: "old receipt"}, {Version: 2, Candidate: "other", Result: "pass", Evidence: "last receipt"},
			}},
			{ID: "right", Specification: "neighboring case"},
			{ID: "retired", Retired: &acceptance.Retirement{Revision: 11, SupersededBy: "left"}},
		},
	}}}
	view, err := selectCurrentCase(&trace, "left")
	if err != nil {
		t.Fatal(err)
	}
	if view.Revision != 18 || view.Version != 2 || view.Rule != "current rule" || view.Implementation.Commit != "current" || view.LatestRun.Evidence != "last receipt" || len(view.Impact.Retest) != 1 || view.Impact.Retest[0] != "left" {
		t.Fatalf("incorrect current case: %+v", view)
	}
	buf, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"old receipt", "neighboring case", "superseded rule"} {
		if strings.Contains(string(buf), s) {
			t.Errorf("unrequested history: %s", s)
		}
	}
	pending, err := selectCurrentCase(&trace, "right")
	if err != nil || pending.LatestRun != nil || len(pending.Impact.Unrun) != 1 {
		t.Fatalf("unrun case: %+v %v", pending, err)
	}
	old, err := selectCurrentCase(&trace, "retired")
	if err != nil || old.Retired == nil || old.Retired.SupersededBy != "left" || len(old.Impact.Retired) != 1 {
		t.Fatalf("retired case: %+v %v", old, err)
	}
	if _, err := selectCurrentCase(&trace, "missing"); err == nil {
		t.Fatal("missing case was reported as found")
	}
	if len(trace.Criteria[0].Cases) != 3 || len(trace.Criteria[0].Cases[0].Runs) != 2 {
		t.Fatal("reading one case modified the full trace")
	}
}
