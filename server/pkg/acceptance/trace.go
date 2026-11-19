// Package acceptance records the versioned acceptance contract of a single issue.
package acceptance

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
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
type Impact struct {
	CriterionID           string   `json:"criterion_id"`
	Version               int      `json:"version"`
	Uncovered             bool     `json:"uncovered"`
	Unrun                 []string `json:"unrun_cases"`
	Retest                []string `json:"retest_cases"`
	Failed                []string `json:"failed_cases"`
	CurrentImplementation bool     `json:"has_current_implementation"`
}

func (c Criterion) Impact() Impact {
	result := Impact{CriterionID: c.ID, Version: len(c.Versions), Uncovered: len(c.Cases) == 0, Unrun: []string{}, Retest: []string{}, Failed: []string{}}
	var candidate string
	for _, link := range c.Implementations {
		if link.Version == result.Version {
			candidate = link.Commit
		}
	}
	result.CurrentImplementation = candidate != ""
	for _, tc := range c.Cases {
		if len(tc.Runs) == 0 {
			result.Unrun = append(result.Unrun, tc.ID)
			continue
		}
		latest := tc.Runs[len(tc.Runs)-1]
		if latest.Version != result.Version || (candidate != "" && latest.Candidate != candidate) {
			result.Retest = append(result.Retest, tc.ID)
		} else if latest.Result == "fail" {
			result.Failed = append(result.Failed, tc.ID)
		}
	}
	return result
}

type TestCase struct {
	ID            string `json:"id"`
	Specification string `json:"specification"`
	Runs          []Run  `json:"runs"`
}

type Run struct {
	Version   int    `json:"version"`
	Result    string `json:"result"`
	Candidate string `json:"candidate"`
	Evidence  string `json:"evidence"`
}

type Implementation struct {
	Version    int    `json:"version"`
	Repository string `json:"repository"`
	Branch     string `json:"branch"`
	Commit     string `json:"commit"`
}

type Change struct {
	BaseRevision *int   `json:"base_revision"`
	Action       string `json:"action"`
	CriterionID  string `json:"criterion_id"`
	CaseID       string `json:"case_id"`
	Text         string `json:"text"`
	Repository   string `json:"repository"`
	Branch       string `json:"branch"`
	Commit       string `json:"commit"`
	Result       string `json:"result"`
	Candidate    string `json:"candidate"`
	Evidence     string `json:"evidence"`
}

var (
	ErrConflict = errors.New("acceptance records changed; reload before saving")
	key         = regexp.MustCompile("^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,79}$")
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
		criterion.Cases = append(criterion.Cases, TestCase{ID: op.CaseID, Specification: op.Text, Runs: []Run{}})
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
		if op.Result != "pass" && op.Result != "fail" {
			return errors.New("result must be pass or fail")
		}
		if !sha.MatchString(op.Candidate) || strings.TrimSpace(op.Evidence) == "" {
			return errors.New("candidate commit SHA and evidence are required")
		}
		test.Runs = append(test.Runs, Run{len(criterion.Versions), op.Result, op.Candidate, op.Evidence})
	default:
		return fmt.Errorf("unknown acceptance action %q", op.Action)
	}
	t.Revision++
	return nil
}
