package workmethod

import (
	"slices"
	"strings"

	"github.com/multica-ai/multica/server/pkg/acceptance"
)

// ProjectDelivery snapshots the executing machine and checkout selected by
// the project. GitHubDelivery on the method snapshot is the delivery target.
type ProjectDelivery struct {
	ResourceID string `json:"resource_id"`
	DaemonID   string `json:"daemon_id"`
	Directory  string `json:"directory"`
}

type DeliveryJob struct {
	IssueID    string          `json:"issue_id"`
	Identifier string          `json:"identifier"`
	Revision   int             `json:"revision"`
	Target     GitHubDelivery  `json:"target"`
	Directory  string          `json:"directory"`
	Head       string          `json:"head"`
	Branch     string          `json:"branch"`
	Receipt    DeliveryReceipt `json:"receipt"`
}

type DeliveryReceipt struct {
	State       string `json:"state"`
	PullRequest int    `json:"pull_request,omitempty"`
	Base        string `json:"base,omitempty"`
	Merge       string `json:"merge,omitempty"`
}

const (
	DeliveryWaiting = "waiting_actions"
	DeliveryBlocked = "merge_blocked"
	DeliverySetup   = "setup_required"
	DeliveryCleanup = "cleanup_pending"
	DeliveryDone    = "reclaimed"
)

func (d DeliveryReceipt) Valid() bool {
	validSHA := func(s string) bool { return len(s) == 40 && coverageCommit.MatchString(s) }
	switch d.State {
	case DeliverySetup:
		return d.PullRequest == 0 && d.Base == "" && d.Merge == ""
	case DeliveryWaiting, DeliveryBlocked:
		return d.PullRequest > 0 && validSHA(d.Base) && d.Merge == ""
	case DeliveryCleanup, DeliveryDone:
		return d.PullRequest > 0 && validSHA(d.Merge) && (d.Base == "" || validSHA(d.Base))
	default:
		return false
	}
}

func (r Run) DeliveryApproval() *Event {
	if r.State != "closed" || r.Method == nil || r.Method.GitHubDelivery == nil || r.ProjectDelivery == nil || len(r.History) == 0 {
		return nil
	}
	last := &r.History[len(r.History)-1]
	if last.Action != "approve" || last.Closeout == nil || len(last.Closeout.Criteria) == 0 {
		return nil
	}
	head := last.Closeout.Criteria[0].Candidate
	if len(head) != 40 || !coverageCommit.MatchString(head) ||
		slices.ContainsFunc(last.Closeout.Criteria, func(c acceptance.ClosedCriterion) bool { return !c.Passed || c.Candidate != head }) {
		return nil
	}
	return last
}

// An approved candidate may be recorded using either the GitHub slug or the
// exact local repository. The execution machine checks the configured origin.
func (j DeliveryJob) MatchesRepository(repository string) bool {
	return strings.EqualFold(repository, j.Target.Repository) || repository == j.Directory
}
