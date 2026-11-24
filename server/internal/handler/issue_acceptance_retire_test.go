package handler

import (
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/pkg/acceptance"
)

// Retiring a case removes it from the required set: a criterion whose only
// outstanding work is retired must clear both the candidate and delivery gates,
// while an active unrun case still blocks them.
func TestDevelopmentReviewIgnoresRetiredCases(t *testing.T) {
	commit := strings.Repeat("d", 40)
	build := func(cases []acceptance.TestCase) acceptance.Trace {
		return acceptance.Trace{
			Revision: 7,
			Criteria: []acceptance.Criterion{{
				ID:       "C1",
				Versions: []string{"检索命中"},
				Cases:    cases,
				Implementations: []acceptance.Implementation{
					{Version: 1, Repository: "org/repo", Branch: "dev", Commit: commit},
				},
			}},
		}
	}

	activeUnrun := build([]acceptance.TestCase{
		{ID: "C1-T1", Specification: "当前用例", Runs: []acceptance.Run{}},
		{ID: "C1-T2", Specification: "旧用例", Runs: []acceptance.Run{}, Retired: &acceptance.Retirement{Revision: 6, Reason: "被 T1 替代"}},
	})
	for _, gate := range []string{"candidate", "delivery"} {
		if err := developmentReview(activeUnrun, gate); err == nil {
			t.Fatalf("%s gate accepted an unrun active case", gate)
		}
	}

	onlyRetiredOutstanding := build([]acceptance.TestCase{
		{
			ID:            "C1-T1",
			Specification: "当前用例",
			Runs:          []acceptance.Run{{Version: 1, Result: "pass", Candidate: commit, Evidence: "run-1"}},
		},
		{ID: "C1-T2", Specification: "旧用例", Runs: []acceptance.Run{}, Retired: &acceptance.Retirement{Revision: 6, Reason: "被 T1 替代"}},
		{ID: "C1-T3", Specification: "旧用例三", Runs: []acceptance.Run{}, Retired: &acceptance.Retirement{Revision: 7, Reason: "不再执行"}},
	})
	for _, gate := range []string{"candidate", "delivery"} {
		if err := developmentReview(onlyRetiredOutstanding, gate); err != nil {
			t.Fatalf("%s gate blocked on a retired case: %v", gate, err)
		}
	}
}
