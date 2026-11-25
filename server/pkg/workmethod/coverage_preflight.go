package workmethod

import (
	"fmt"
	"regexp"

	"github.com/multica-ai/multica/server/pkg/acceptance"
)

// CoveragePreflight points to actual runs of a specific test revision. The
// baseline can be a known-good legacy behavior or a red/green comparison.
type CoveragePreflight struct {
	CriterionID      string       `json:"criterion_id"`
	CriterionVersion int          `json:"criterion_version"`
	CaseID           string       `json:"case_id"`
	CaseVersion      int          `json:"case_version"`
	Command          string       `json:"command"`
	Baseline         CoverageRun  `json:"baseline"`
	Reference        *CoverageRun `json:"reference,omitempty"`
}

type CoverageRun struct {
	Commit   string `json:"commit"`
	Result   string `json:"result"`
	Evidence string `json:"evidence"`
}

func (r Run) RequiresCoveragePreflight() bool {
	return r.EvidenceGate() == "coverage" && r.Method.Steps[r.Step].RequirePreflight
}

// CheckCoveragePreflight checks the active case revisions. A previous receipt is
// eligible only for the identical criterion and case revisions. This lets a
// scoped re-review retain evidence for unchanged cases.
func (r Run) CheckCoveragePreflight(trace acceptance.Trace, submitted []CoveragePreflight) error {
	type revision struct {
		criterion string
		version   int
		caseID    string
		caseVer   int
	}
	needed := make(map[revision]bool)
	active := make([]revision, 0)
	for _, criterion := range trace.Criteria {
		for _, test := range criterion.Cases {
			if test.Retired == nil {
				id := revision{criterion.ID, len(criterion.Versions), test.ID, test.Version()}
				needed[id] = false
				active = append(active, id)
			}
		}
	}
	for _, proof := range submitted {
		id := revision{proof.CriterionID, proof.CriterionVersion, proof.CaseID, proof.CaseVersion}
		if covered, exists := needed[id]; covered || !exists {
			return fmt.Errorf("覆盖预检须引用唯一的现行标准和用例修订：%s / %s", proof.CriterionID, proof.CaseID)
		}
		if err := proof.validate(); err != nil {
			return err
		}
		needed[id] = true
	}
	for _, event := range r.History {
		for _, proof := range event.CoveragePreflight {
			id := revision{proof.CriterionID, proof.CriterionVersion, proof.CaseID, proof.CaseVersion}
			if covered, exists := needed[id]; exists && !covered {
				needed[id] = true
			}
		}
	}
	for _, id := range active {
		if !needed[id] {
			return fmt.Errorf("%s v%d / %s v%d 缺少可复跑的覆盖预检回执", id.criterion, id.version, id.caseID, id.caseVer)
		}
	}
	return nil
}

func (p CoveragePreflight) validate() error {
	baseline := p.Baseline
	if !validText(p.Command, 1500) || !baseline.valid() ||
		(baseline.Result == "fail" && p.Reference == nil) ||
		(p.Reference != nil && (!p.Reference.valid() || p.Reference.Result == baseline.Result)) {
		return fmt.Errorf("%s 的预检须记录复跑命令、完整 SHA 和实际结果。红灯基线须配通过的参照，已有行为至少记录通过的旧版基线", p.CaseID)
	}
	return nil
}

var coverageCommit = regexp.MustCompile(`^(?:[a-f0-9]{40}|[a-f0-9]{64})$`)

func (r CoverageRun) valid() bool {
	return coverageCommit.MatchString(r.Commit) && (r.Result == "pass" || r.Result == "fail") && validText(r.Evidence, 1500)
}
