package workmethod

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/multica-ai/multica/server/pkg/acceptance"
)

var ErrCandidateFailureApproval = errors.New("失败候选的独立裁定只能退回返工，修复后须重新送审")

type CandidateFailure struct {
	Commit        string   `json:"commit"`
	WorkerTaskID  string   `json:"worker_task_id"`
	Cases         []string `json:"cases"`
	TraceRevision int      `json:"trace_revision"`
}

// FailedCandidate uses current behavior results from one implementation and
// task. Baseline failures and stale evidence never open a failure review.
func FailedCandidate(trace acceptance.Trace) *CandidateFailure {
	var failure *CandidateFailure
	for _, criterion := range trace.Criteria {
		impact := criterion.Impact()
		if !impact.CurrentImplementation || len(impact.Failed) == 0 {
			continue
		}
		for _, test := range criterion.Cases {
			if !slices.Contains(impact.Failed, test.ID) {
				continue
			}
			result := test.LatestBehaviorRun()
			if result.Candidate == "" || result.TaskID == "" {
				return nil
			}
			if failure == nil {
				failure = &CandidateFailure{Commit: result.Candidate, WorkerTaskID: result.TaskID, TraceRevision: trace.Revision}
			}
			if failure.Commit != result.Candidate || failure.WorkerTaskID != result.TaskID {
				return nil
			}
			failure.Cases = append(failure.Cases, test.ID)
		}
	}
	return failure
}

func (f CandidateFailure) Summary() string {
	cases := strings.Join(f.Cases[:min(len(f.Cases), 10)], "、")
	if len(f.Cases) > 10 {
		cases += fmt.Sprintf("（共 %d 条）", len(f.Cases))
	}
	return fmt.Sprintf("候选 %s 的必验用例 %s 未通过（验收台账修订 %d，来源实现运行 %s）。", f.Commit, cases, f.TraceRevision, f.WorkerTaskID)
}

func (f CandidateFailure) ReviewBrief() string {
	return f.Summary() + "仅独立复跑失败项和区分用例缺口与实现缺陷的对照；引用固定证据，用 rework 指向负责的步骤。修复后须以新证据正式送审。"
}

func (r Run) CandidateFailureReview() *CandidateFailure {
	if r.State != "review" || len(r.History) == 0 {
		return nil
	}
	return r.History[len(r.History)-1].CandidateFailure
}
