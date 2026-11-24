package acceptance

import (
	"strings"
	"testing"
	"time"
)

func TestCloseoutUsesCurrentBehaviorRunAndAttachmentReferences(t *testing.T) {
	candidate := strings.Repeat("a", 40)
	id := "01a20c18-444a-7a66-9b41-6daec377bf47"
	earlier := time.Unix(1, 0)
	approval := time.Unix(2, 0)
	later := time.Unix(3, 0)
	accepted := Run{
		Version: 1, CaseVersion: 2, Result: "pass", Candidate: candidate,
		RunOrigin: RunOrigin{RecordedAt: &earlier, TaskID: "independent-run"},
		Evidence:  "Report /api/attachments/" + id + "/download and /api/attachments/" + id + "/download",
	}
	trace := Trace{Revision: 24, Criteria: []Criterion{{
		ID: "US21-02", Versions: []string{"Keep the original\n\nVerification steps"},
		Implementations: []Implementation{{Version: 1, Commit: candidate}},
		Cases: []TestCase{{
			ID: "B01", PreviousSpecifications: []CaseSpecification{{Text: "v1"}},
			Runs: []Run{
				{Version: 1, Result: "pass", Candidate: candidate},
				accepted,
				{Version: 1, CaseVersion: 2, Kind: Counterexample, Result: "fail", Candidate: candidate, RunOrigin: RunOrigin{RecordedAt: &earlier}},
			},
		}},
	}}}
	saved := trace.Closeout(nil)
	entry := saved.Criteria[0].Evidence[0]
	if saved.TraceRevision != 24 || saved.Reconstructed || saved.Criteria[0].Summary != "Keep the original" ||
		!saved.Criteria[0].Passed || entry.RunNumber != 2 || entry.TaskID != "independent-run" ||
		len(entry.AttachmentIDs) != 1 || entry.AttachmentIDs[0] != id {
		t.Fatalf("wrong closeout: %+v", saved)
	}
	test := &trace.Criteria[0].Cases[0]
	laterFailure := accepted
	laterFailure.RecordedAt, laterFailure.Result = &later, "fail"
	test.Runs = append(test.Runs, laterFailure)
	if current := trace.Closeout(nil); current.Criteria[0].Passed {
		t.Fatal("the current case must not be reported as passing")
	}
	old := trace.Closeout(&approval)
	if !old.Reconstructed || !old.Criteria[0].Passed || old.Criteria[0].Evidence[0].RunNumber != 2 {
		t.Fatalf("a later run changed the historical decision: %+v", old)
	}
}
