package acceptance

import (
	"regexp"
	"slices"
	"strings"
	"time"
)

type Closeout struct {
	TraceRevision int               `json:"trace_revision"`
	Reconstructed bool              `json:"reconstructed,omitempty"`
	Criteria      []ClosedCriterion `json:"criteria"`
}

type ClosedCriterion struct {
	ID        string        `json:"id"`
	Version   int           `json:"version"`
	Summary   string        `json:"summary"`
	Candidate string        `json:"candidate,omitempty"`
	Passed    bool          `json:"passed"`
	Evidence  []AcceptedRun `json:"evidence"`
}

type AcceptedRun struct {
	CaseID        string   `json:"case_id"`
	RunNumber     int      `json:"run_number"`
	AttachmentIDs []string `json:"attachment_ids,omitempty"`
	Run
}

var attachmentInEvidence = regexp.MustCompile(`(?i)/api/attachments/([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})/download\b`)

// Closeout snapshots the behavior runs used by the delivery gate. For old
// approvals, only timestamped runs already present at approval can be inferred.
func (t Trace) Closeout(legacyApproval *time.Time) Closeout {
	result := Closeout{TraceRevision: t.Revision, Reconstructed: legacyApproval != nil, Criteria: make([]ClosedCriterion, 0, len(t.Criteria))}
	for _, criterion := range t.Criteria {
		version := len(criterion.Versions)
		if version == 0 {
			continue
		}
		summary, _, _ := strings.Cut(criterion.Versions[version-1], "\n")
		row := ClosedCriterion{ID: criterion.ID, Version: version, Summary: summary, Evidence: []AcceptedRun{}}
		for _, link := range criterion.Implementations {
			if link.Version == version {
				row.Candidate = link.Commit
			}
		}
		active := 0
		for _, test := range criterion.Cases {
			if test.Retired != nil {
				continue
			}
			active++
			for index := len(test.Runs) - 1; index >= 0; index-- {
				run := test.Runs[index]
				if run.Kind != "" || (legacyApproval != nil && run.RecordedAt != nil && run.RecordedAt.After(*legacyApproval)) {
					continue
				}
				if row.Candidate != "" && run.Candidate == row.Candidate && run.Version == version &&
					run.TestCaseVersion() == test.Version() && run.Result == "pass" &&
					(legacyApproval == nil || run.RecordedAt != nil) {
					entry := AcceptedRun{CaseID: test.ID, RunNumber: index + 1, Run: run}
					for _, match := range attachmentInEvidence.FindAllStringSubmatch(run.Evidence, -1) {
						id := strings.ToLower(match[1])
						if !slices.Contains(entry.AttachmentIDs, id) {
							entry.AttachmentIDs = append(entry.AttachmentIDs, id)
						}
					}
					row.Evidence = append(row.Evidence, entry)
				}
				break
			}
		}
		row.Passed = active > 0 && len(row.Evidence) == active
		result.Criteria = append(result.Criteria, row)
	}
	return result
}
