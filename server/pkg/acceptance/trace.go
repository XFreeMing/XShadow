// Package acceptance records the versioned acceptance contract of a single issue.
package acceptance

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

type Trace struct {
	Revision int         `json:"revision"`
	Criteria []Criterion `json:"criteria"`
}

type Criterion struct {
	ID              string           `json:"id"`
	Versions        []string         `json:"versions"`
	Cases           []TestCase       `json:"cases"`
	Implementations []Implementation `json:"implementations"`
}

// Impact reports direct dependencies. Old run evidence remains on its original version.
// Retired cases keep their text and runs but are no longer required by any gate.
type Impact struct {
	CriterionID           string   `json:"criterion_id"`
	Version               int      `json:"version"`
	Uncovered             bool     `json:"uncovered"`
	Unrun                 []string `json:"unrun_cases"`
	Retest                []string `json:"retest_cases"`
	Failed                []string `json:"failed_cases"`
	Retired               []string `json:"retired_cases"`
	CurrentImplementation bool     `json:"has_current_implementation"`
}

func (c Criterion) Impact() Impact {
	result := Impact{CriterionID: c.ID, Version: len(c.Versions), Uncovered: len(c.Cases) == 0, Unrun: []string{}, Retest: []string{}, Failed: []string{}, Retired: []string{}}
	var candidate string
	for _, link := range c.Implementations {
		if link.Version == result.Version {
			candidate = link.Commit
		}
	}
	result.CurrentImplementation = candidate != ""
	for _, tc := range c.Cases {
		if tc.Retired != nil {
			result.Retired = append(result.Retired, tc.ID)
			continue
		}
		latest := tc.LatestBehaviorRun()
		if latest == nil {
			result.Unrun = append(result.Unrun, tc.ID)
			continue
		}
		if latest.Version != result.Version || latest.TestCaseVersion() != tc.Version() || (candidate != "" && latest.Candidate != candidate) {
			result.Retest = append(result.Retest, tc.ID)
		} else if latest.Result == "fail" {
			result.Failed = append(result.Failed, tc.ID)
		}
	}
	return result
}

type TestCase struct {
	ID                     string              `json:"id"`
	Specification          string              `json:"specification"`
	ArtifactSHA256         string              `json:"artifact_sha256,omitempty"`
	PreviousSpecifications []CaseSpecification `json:"previous_specifications,omitempty"`
	Runs                   []Run               `json:"runs"`
	Retired                *Retirement         `json:"retired,omitempty"`
}

type CaseSpecification struct {
	Text           string `json:"text"`
	ArtifactSHA256 string `json:"artifact_sha256,omitempty"`
}

func (tc TestCase) Version() int {
	return len(tc.PreviousSpecifications) + 1
}

func (tc TestCase) LatestBehaviorRun() *Run {
	for i := len(tc.Runs) - 1; i >= 0; i-- {
		if tc.Runs[i].Kind == "" {
			return &tc.Runs[i]
		}
	}
	return nil
}

// Retirement marks a replaced case. The original specification and runs stay
// on the case; the milestone only records when and why it stopped being required.
type Retirement struct {
	Revision     int    `json:"revision"`
	Reason       string `json:"reason"`
	SupersededBy string `json:"superseded_by,omitempty"`
}

type RunKind string

const (
	Counterexample RunKind = "counterexample"
	BeforeChange   RunKind = "red_before_change"
)

type Run struct {
	Version        int     `json:"version"`
	CaseVersion    int     `json:"case_version,omitempty"`
	Kind           RunKind `json:"run_kind,omitempty"`
	ExpectedResult string  `json:"expected_result,omitempty"`
	Result         string  `json:"result"`
	Candidate      string  `json:"candidate"`
	Evidence       string  `json:"evidence"`
	RunOrigin
}

type RunOrigin struct {
	Actor      string     `json:"actor,omitempty"`
	TaskID     string     `json:"task_id,omitempty"`
	RecordedAt *time.Time `json:"recorded_at,omitempty"`
}

func (r Run) TestCaseVersion() int {
	if r.CaseVersion == 0 {
		return 1
	}
	return r.CaseVersion
}

type Implementation struct {
	Version    int    `json:"version"`
	Repository string `json:"repository"`
	Branch     string `json:"branch"`
	Commit     string `json:"commit"`
}

type Change struct {
	BaseRevision   *int    `json:"base_revision"`
	Action         string  `json:"action"`
	CriterionID    string  `json:"criterion_id"`
	CaseID         string  `json:"case_id"`
	Text           string  `json:"text"`
	Repository     string  `json:"repository"`
	Branch         string  `json:"branch"`
	Commit         string  `json:"commit"`
	Result         string  `json:"result"`
	Candidate      string  `json:"candidate"`
	Evidence       string  `json:"evidence"`
	CaseVersion    *int    `json:"case_version,omitempty"`
	ArtifactSHA256 string  `json:"artifact_sha256,omitempty"`
	Kind           RunKind `json:"run_kind,omitempty"`
	ExpectedResult string  `json:"expected_result,omitempty"`
	Reason         string  `json:"reason"`
	SupersededBy   string  `json:"superseded_by"`
}

var (
	ErrConflict = errors.New("acceptance records changed; reload before saving")
	key         = regexp.MustCompile("^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,79}$")
	sha256      = regexp.MustCompile(`^[a-f0-9]{64}$`)
	sha         = regexp.MustCompile("^(?:[a-f0-9]{40}|[a-f0-9]{64})$")
)

func Decode(raw []byte) (Trace, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("[]")) {
		return Trace{Criteria: []Criterion{}}, nil
	}
	var trace Trace
	if err := json.Unmarshal(raw, &trace); err != nil || trace.Criteria == nil {
		return Trace{}, errors.New("unrecognized stored acceptance criteria")
	}
	for _, c := range trace.Criteria {
		if len(c.Versions) == 0 {
			return Trace{}, errors.New("missing criterion version")
		}
	}
	return trace, nil
}

func (t *Trace) Apply(op Change) error {
	return t.ApplyWithOrigin(op, RunOrigin{})
}

func (t *Trace) ApplyWithOrigin(op Change, origin RunOrigin) error {
	if op.BaseRevision == nil {
		return errors.New("base_revision is required")
	}
	if *op.BaseRevision != t.Revision {
		return ErrConflict
	}
	if !key.MatchString(op.CriterionID) {
		return errors.New("invalid criterion_id")
	}
	var criterion *Criterion
	var test *TestCase
	for i := range t.Criteria {
		c := &t.Criteria[i]
		if c.ID == op.CriterionID {
			criterion = c
		}
		for j := range c.Cases {
			if c.Cases[j].ID == op.CaseID {
				if c.ID != op.CriterionID {
					return errors.New("case belongs to another criterion")
				}
				test = &c.Cases[j]
			}
		}
	}
	op.Text = strings.TrimSpace(op.Text)
	if op.ArtifactSHA256 != "" && !sha256.MatchString(op.ArtifactSHA256) {
		return errors.New("artifact_sha256 must be a full lowercase SHA-256")
	}
	if criterion == nil && op.Action != "criterion" {
		return errors.New("criterion does not exist")
	}
	switch op.Action {
	case "criterion":
		if op.Text == "" {
			return errors.New("criterion text is required")
		}
		if criterion == nil {
			t.Criteria = append(t.Criteria, Criterion{ID: op.CriterionID, Versions: []string{op.Text}, Cases: []TestCase{}, Implementations: []Implementation{}})
		} else {
			if criterion.Versions[len(criterion.Versions)-1] == op.Text {
				return nil
			}
			criterion.Versions = append(criterion.Versions, op.Text)
		}
	case "case":
		if !key.MatchString(op.CaseID) || op.Text == "" {
			return errors.New("case_id and test specification are required")
		}
		if test != nil {
			return errors.New("case_id already exists")
		}
		criterion.Cases = append(criterion.Cases, TestCase{ID: op.CaseID, Specification: op.Text, ArtifactSHA256: op.ArtifactSHA256, Runs: []Run{}})
	case "case_revision":
		if test == nil || test.Retired != nil || op.Text == "" {
			return errors.New("case_revision requires an active case and test specification")
		}
		if test.Specification == op.Text && test.ArtifactSHA256 == op.ArtifactSHA256 {
			return nil
		}
		test.PreviousSpecifications = append(test.PreviousSpecifications, CaseSpecification{Text: test.Specification, ArtifactSHA256: test.ArtifactSHA256})
		test.Specification, test.ArtifactSHA256 = op.Text, op.ArtifactSHA256
	case "implementation":
		if strings.TrimSpace(op.Repository) == "" || strings.TrimSpace(op.Branch) == "" || !sha.MatchString(op.Commit) {
			return errors.New("repository, branch and full lowercase commit SHA are required")
		}
		link := Implementation{len(criterion.Versions), op.Repository, op.Branch, op.Commit}
		if n := len(criterion.Implementations); n > 0 && criterion.Implementations[n-1] == link {
			return nil
		}
		criterion.Implementations = append(criterion.Implementations, link)
	case "run":
		if test == nil {
			return errors.New("case not found")
		}
		if err := validateRunChange(op, *test); err != nil {
			return err
		}
		test.Runs = append(test.Runs, Run{
			Version: len(criterion.Versions), CaseVersion: test.Version(), Kind: op.Kind,
			ExpectedResult: op.ExpectedResult, Result: op.Result,
			Candidate: op.Candidate, Evidence: op.Evidence, RunOrigin: origin,
		})
	case "case_retire":
		if test == nil {
			return errors.New("case not found")
		}
		if test.Retired != nil {
			return errors.New("case is already retired")
		}
		reason := strings.TrimSpace(op.Reason)
		if reason == "" {
			return errors.New("retirement reason is required")
		}
		supersededBy := strings.TrimSpace(op.SupersededBy)
		if supersededBy != "" {
			if supersededBy == test.ID {
				return errors.New("superseded_by must name another case")
			}
			if !key.MatchString(supersededBy) {
				return errors.New("invalid superseded_by")
			}
			superseding := false
			for _, other := range criterion.Cases {
				if other.ID == supersededBy {
					superseding = true
					break
				}
			}
			if !superseding {
				for _, other := range t.Criteria {
					for _, otherCase := range other.Cases {
						if other.ID != criterion.ID && otherCase.ID == supersededBy {
							return errors.New("superseded_by belongs to another criterion")
						}
					}
				}
				return errors.New("superseded_by case not found")
			}
		}
		test.Retired = &Retirement{Revision: t.Revision + 1, Reason: reason, SupersededBy: supersededBy}
	default:
		return fmt.Errorf("unknown acceptance action %q", op.Action)
	}
	t.Revision++
	return nil
}

func validateRunChange(op Change, test TestCase) error {
	if op.Result != "pass" && op.Result != "fail" {
		return errors.New("result must be pass or fail")
	}
	if !sha.MatchString(op.Candidate) || strings.TrimSpace(op.Evidence) == "" {
		return errors.New("candidate commit SHA and evidence are required")
	}
	if op.CaseVersion == nil && test.Version() > 1 || op.CaseVersion != nil && *op.CaseVersion != test.Version() {
		return fmt.Errorf("case_version must match current case version %d", test.Version())
	}
	if op.ArtifactSHA256 != test.ArtifactSHA256 {
		return errors.New("artifact_sha256 must match the registered test artifact")
	}
	switch op.Kind {
	case "":
		if op.ExpectedResult != "" {
			return errors.New("expected_result requires a counterexample or red_before_change run")
		}
	case Counterexample:
		if op.ExpectedResult != "pass" && op.ExpectedResult != "fail" {
			return errors.New("counterexample requires expected_result pass or fail")
		}
	case BeforeChange:
		if op.ExpectedResult != "fail" {
			return errors.New("red_before_change requires expected_result fail")
		}
	default:
		return errors.New("unknown acceptance run_kind")
	}
	return nil
}
