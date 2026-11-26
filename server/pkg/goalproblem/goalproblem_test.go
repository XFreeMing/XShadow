package goalproblem_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/multica-ai/multica/server/pkg/goalproblem"
)

const (
	issueTask1 = "11111111-1111-4111-8111-111111111111"
	issueTask2 = "22222222-2222-4222-8222-222222222222"
	issueTask3 = "33333333-3333-4333-8333-333333333333"
	issueTask4 = "44444444-4444-4444-8444-444444444444"
	issueEvid1 = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	issueEvid2 = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	issueEvid3 = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
)

var (
	actorAuthor    = goalproblem.Actor{ID: "user-author", CanDecide: false}
	actorSubmitter = goalproblem.Actor{ID: "agent-worker", CanDecide: false}
	actorRecorder  = goalproblem.Actor{ID: "agent-recorder", CanDecide: false}
	actorReviewer  = goalproblem.Actor{ID: "user-reviewer", CanDecide: false}
	actorOwner     = goalproblem.Actor{ID: "user-owner", CanDecide: true}
)

func baseCharter() goalproblem.Charter {
	return goalproblem.Charter{
		Title:  "Reduce checkout latency",
		Intent: "Keep checkout p95 latency under 400ms during peak load",
		Scope:  "Web and mobile checkout submission pipeline",
		AcceptanceCriteria: []string{
			"p95 checkout submit latency <= 400ms over 7 days",
			"error rate remains <= 0.1%",
		},
	}
}

func revisedCharter() goalproblem.Charter {
	return goalproblem.Charter{
		Title:  "Reduce checkout latency with async ledger settlement",
		Intent: "Keep checkout p95 latency under 300ms by decoupling ledger settlement",
		Scope:  "Checkout submission pipeline and async settlement worker",
		AcceptanceCriteria: []string{
			"p95 checkout submit latency <= 300ms over 7 days",
			"settlement lag <= 30s at p99",
		},
	}
}

func snapshotJSON(t *testing.T, s goalproblem.State) []byte {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	return b
}

func mustApply(t *testing.T, s *goalproblem.State, cmd goalproblem.Command, actor goalproblem.Actor) {
	t.Helper()
	if err := s.Apply(cmd, actor); err != nil {
		t.Fatalf("Apply(%s) unexpected error: %v", cmd.Action, err)
	}
}

func mustRejectUnchanged(t *testing.T, s *goalproblem.State, cmd goalproblem.Command, actor goalproblem.Actor, wantErrs ...error) {
	t.Helper()
	before := snapshotJSON(t, *s)
	err := s.Apply(cmd, actor)
	if err == nil {
		t.Fatalf("Apply(%s) succeeded, expected error matching %v", cmd.Action, wantErrs)
	}
	for _, want := range wantErrs {
		if !errors.Is(err, want) {
			t.Fatalf("Apply(%s) error = %v, want errors.Is(_, %v)", cmd.Action, err, want)
		}
	}
	after := snapshotJSON(t, *s)
	if !bytes.Equal(before, after) {
		t.Fatalf("rejected Apply(%s) mutated state:\nbefore=%s\nafter=%s", cmd.Action, before, after)
	}
}

// seedActiveBranch creates a confirmed problem, one active branch, and one falsifiable hypothesis.
func seedActiveBranch(t *testing.T) goalproblem.State {
	t.Helper()
	s := goalproblem.New(baseCharter())
	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionDefineProblem,
		DefineProblem: &goalproblem.DefineProblemInput{
			ProblemID:        "prob-1",
			Statement:        "Checkout p95 exceeds 850ms at peak traffic",
			Baseline:         "p95=870ms across 120k daily checkouts",
			AffectedUsers:    []string{"returning shoppers", "mobile web buyers"},
			Constraints:      []string{"no payment provider change", "zero double charges"},
			SuccessCriterion: "p95 <= 400ms for 7 consecutive days",
		},
	}, actorAuthor)
	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionConfirmProblem,
		ConfirmProblem: &goalproblem.ConfirmProblemInput{
			ProblemRef: goalproblem.RevisionRef{ID: "prob-1", Revision: 1},
		},
	}, actorOwner)
	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionCreateBranch,
		CreateBranch: &goalproblem.CreateBranchInput{
			BranchID:   "br-db",
			ProblemRef: goalproblem.RevisionRef{ID: "prob-1", Revision: 1},
			Title:      "Synchronous ledger write contention",
			Scope:      "Order write transaction path",
			Boundary:   "Excludes client bundle size and CDN routing",
			Rationale:  "DB lock wait accounts for 60% of trace time",
		},
	}, actorAuthor)
	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionCreateHypothesis,
		CreateHypothesis: &goalproblem.CreateHypothesisInput{
			HypothesisID:           "hyp-db",
			BranchRef:              goalproblem.RevisionRef{ID: "br-db", Revision: 1},
			Statement:              "Decoupling ledger write cuts checkout p95 by >400ms",
			FalsificationCriterion: "p95 reduction < 150ms under 2x replay load",
			Assumptions:            []string{"idempotency keys prevent double settlement"},
		},
	}, actorAuthor)
	return s
}

func seedApprovedProtocol(t *testing.T, s *goalproblem.State, protoID string, hypRef goalproblem.RevisionRef) {
	t.Helper()
	mustApply(t, s, goalproblem.Command{
		Action: goalproblem.ActionRecordProtocol,
		RecordProtocol: &goalproblem.RecordProtocolInput{
			ProtocolID:          protoID,
			HypothesisRef:       hypRef,
			PredictedResult:     "p95 drops from 870ms to <= 350ms",
			SampleSource:        "Production shadow replay, 10,000 peak orders",
			ControlOrComparison: "Synchronous write path on identical cluster",
			DecisionThreshold:   "p95 <= 400ms and zero ledger drift",
			StopCondition:       "Error rate > 0.5% or duplicate debit detected",
		},
	}, actorAuthor)
	mustApply(t, s, goalproblem.Command{
		Action: goalproblem.ActionApproveProtocol,
		ApproveProtocol: &goalproblem.ApproveProtocolInput{
			ProtocolRef: goalproblem.RevisionRef{ID: protoID, Revision: 1},
		},
	}, actorReviewer)
}

func TestZeroStateAndInitialCharterV1(t *testing.T) {
	var zero goalproblem.State
	if got := zero.Current(); got.Number != 0 {
		t.Fatalf("zero.Current().Number = %d, want 0", got.Number)
	}

	// Zero state rejects commands and stays zero.
	mustRejectUnchanged(t, &zero, goalproblem.Command{
		Action: goalproblem.ActionDefineProblem,
		DefineProblem: &goalproblem.DefineProblemInput{
			ProblemID:        "prob-1",
			Statement:        "Latency spike",
			Baseline:         "870ms",
			AffectedUsers:    []string{"shoppers"},
			Constraints:      []string{"zero downtime"},
			SuccessCriterion: "<=400ms",
		},
	}, actorAuthor, goalproblem.ErrConflict, goalproblem.ErrUninitialized)

	initial := baseCharter()
	s := goalproblem.New(initial)
	// Mutating caller's input slice must not mutate seeded state.
	initial.AcceptanceCriteria[0] = "tampered"

	cur := s.Current()
	if cur.Number != 1 {
		t.Fatalf("Current().Number = %d, want 1", cur.Number)
	}
	if cur.Charter.Title != "Reduce checkout latency" || cur.Charter.AcceptanceCriteria[0] != "p95 checkout submit latency <= 400ms over 7 days" {
		t.Fatalf("unexpected seeded charter: %+v", cur.Charter)
	}

	// Mutating returned Current() slice must not corrupt state.
	cur.Charter.AcceptanceCriteria[0] = "tampered-again"
	if s.Current().Charter.AcceptanceCriteria[0] != "p95 checkout submit latency <= 400ms over 7 days" {
		t.Fatalf("Current() returned aliased AcceptanceCriteria slice")
	}
}

func TestCommandDecodingAndIssueRefExtraction(t *testing.T) {
	// Unknown field must be rejected by strict REST decoder.
	rawUnknown := []byte(`{"action":"define_problem","unknown_field":123,"define_problem":{"problem_id":"p1"}}`)
	if _, err := goalproblem.DecodeCommand(rawUnknown); !errors.Is(err, goalproblem.ErrInvalidInput) {
		t.Fatalf("DecodeCommand unknown field err = %v, want ErrInvalidInput", err)
	}

	// Trailing garbage JSON rejected.
	rawTrailing := []byte(`{"action":"apply_charter_revision","apply_charter_revision":{"proposal_id":"prop-1"}}{"extra":1}`)
	if _, err := goalproblem.DecodeCommand(rawTrailing); !errors.Is(err, goalproblem.ErrInvalidInput) {
		t.Fatalf("DecodeCommand trailing data err = %v, want ErrInvalidInput", err)
	}

	// Valid StartValidationBatch exposes linked task issue UUID via IssueRefs without touching raw JSON.
	rawBatch := []byte(`{
		"action": "start_validation_batch",
		"start_validation_batch": {
			"batch_id": "batch-1",
			"hypothesis_ref": {"id": "hyp-1", "revision": 1},
			"protocol_ref": {"id": "proto-1", "revision": 1},
			"task": {"issue_id": "` + issueTask1 + `", "submitter_id": "agent-worker"}
		}
	}`)
	cmdBatch, err := goalproblem.DecodeCommand(rawBatch)
	if err != nil {
		t.Fatalf("DecodeCommand valid batch: %v", err)
	}
	if got := cmdBatch.IssueRefs(); !reflect.DeepEqual(got, []string{issueTask1}) {
		t.Fatalf("cmdBatch.IssueRefs() = %v, want [%s]", got, issueTask1)
	}

	// RecordValidationResult exposes deduplicated issue UUIDs from evidence refs.
	cmdResult := goalproblem.Command{
		Action: goalproblem.ActionRecordValidationResult,
		RecordValidationResult: &goalproblem.RecordValidationResultInput{
			ResultID:     "res-1",
			BatchID:      "batch-1",
			Observations: []string{"observed p95=320ms"},
			Evidence: []goalproblem.EvidenceRef{
				{ID: "ev-1", Kind: goalproblem.EvidenceKindIssue, IssueID: issueEvid1, Summary: "Replay task report"},
				{ID: "ev-2", Kind: goalproblem.EvidenceKindArtifact, Locator: "s3://bucket/trace.json", Summary: "Trace dump"},
				{ID: "ev-3", Kind: goalproblem.EvidenceKindIssue, IssueID: issueEvid2, Summary: "Audit issue"},
				{ID: "ev-4", Kind: goalproblem.EvidenceKindDocument, IssueID: issueEvid1, Locator: "doc://notes", Summary: "Cross-linked note"},
			},
		},
	}
	if got := cmdResult.IssueRefs(); !reflect.DeepEqual(got, []string{issueEvid1, issueEvid2}) {
		t.Fatalf("cmdResult.IssueRefs() = %v, want [%s %s]", got, issueEvid1, issueEvid2)
	}

	// Apply rejects empty payload, mismatched payload, and multiple payloads.
	s := goalproblem.New(baseCharter())
	mustRejectUnchanged(t, &s, goalproblem.Command{
		Action: goalproblem.ActionDefineProblem,
	}, actorAuthor, goalproblem.ErrInvalidInput)

	mustRejectUnchanged(t, &s, goalproblem.Command{
		Action: goalproblem.ActionDefineProblem,
		ConfirmProblem: &goalproblem.ConfirmProblemInput{
			ProblemRef: goalproblem.RevisionRef{ID: "p1", Revision: 1},
		},
	}, actorAuthor, goalproblem.ErrInvalidInput)

	mustRejectUnchanged(t, &s, goalproblem.Command{
		Action: goalproblem.ActionDefineProblem,
		DefineProblem: &goalproblem.DefineProblemInput{
			ProblemID:        "p1",
			Statement:        "s",
			Baseline:         "b",
			AffectedUsers:    []string{"u"},
			Constraints:      []string{"c"},
			SuccessCriterion: "sc",
		},
		ConfirmProblem: &goalproblem.ConfirmProblemInput{
			ProblemRef: goalproblem.RevisionRef{ID: "p1", Revision: 1},
		},
	}, actorAuthor, goalproblem.ErrInvalidInput)

	// Missing actor ID is rejected.
	mustRejectUnchanged(t, &s, goalproblem.Command{
		Action: goalproblem.ActionDefineProblem,
		DefineProblem: &goalproblem.DefineProblemInput{
			ProblemID:        "p1",
			Statement:        "s",
			Baseline:         "b",
			AffectedUsers:    []string{"u"},
			Constraints:      []string{"c"},
			SuccessCriterion: "sc",
		},
	}, goalproblem.Actor{}, goalproblem.ErrInvalidInput)
}

func TestProblemLogicTreeForksAndRevisions(t *testing.T) {
	s := goalproblem.New(baseCharter())

	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionDefineProblem,
		DefineProblem: &goalproblem.DefineProblemInput{
			ProblemID:        "prob-1",
			Statement:        "Checkout p95 exceeds 850ms",
			Baseline:         "870ms",
			AffectedUsers:    []string{"shoppers"},
			Constraints:      []string{"no payment vendor swap"},
			SuccessCriterion: "p95 <= 400ms",
		},
	}, actorAuthor)

	// Cannot create a branch before the problem is confirmed.
	mustRejectUnchanged(t, &s, goalproblem.Command{
		Action: goalproblem.ActionCreateBranch,
		CreateBranch: &goalproblem.CreateBranchInput{
			BranchID:   "br-1",
			ProblemRef: goalproblem.RevisionRef{ID: "prob-1", Revision: 1},
			Title:      "DB contention",
			Scope:      "Write path",
			Boundary:   "Server only",
			Rationale:  "High lock wait",
		},
	}, actorAuthor, goalproblem.ErrConflict)

	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionConfirmProblem,
		ConfirmProblem: &goalproblem.ConfirmProblemInput{
			ProblemRef: goalproblem.RevisionRef{ID: "prob-1", Revision: 1},
		},
	}, actorOwner)

	// Duplicate problem ID rejected.
	mustRejectUnchanged(t, &s, goalproblem.Command{
		Action: goalproblem.ActionDefineProblem,
		DefineProblem: &goalproblem.DefineProblemInput{
			ProblemID:        "prob-1",
			Statement:        "Duplicate",
			Baseline:         "b",
			AffectedUsers:    []string{"u"},
			Constraints:      []string{"c"},
			SuccessCriterion: "sc",
		},
	}, actorAuthor, goalproblem.ErrConflict, goalproblem.ErrDuplicateID)

	// Create root branch and forked child branch; reject foreign parent branch.
	mustRejectUnchanged(t, &s, goalproblem.Command{
		Action: goalproblem.ActionCreateBranch,
		CreateBranch: &goalproblem.CreateBranchInput{
			BranchID:       "br-orphan",
			ProblemRef:     goalproblem.RevisionRef{ID: "prob-1", Revision: 1},
			ParentBranchID: "br-foreign",
			Title:          "Orphan",
			Scope:          "Scope",
			Boundary:       "Boundary",
			Rationale:      "Rationale",
		},
	}, actorAuthor, goalproblem.ErrNotFound)

	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionCreateBranch,
		CreateBranch: &goalproblem.CreateBranchInput{
			BranchID:   "br-root",
			ProblemRef: goalproblem.RevisionRef{ID: "prob-1", Revision: 1},
			Title:      "Server write latency",
			Scope:      "Checkout backend",
			Boundary:   "Excludes frontend",
			Rationale:  "Traces point to backend",
		},
	}, actorAuthor)

	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionCreateBranch,
		CreateBranch: &goalproblem.CreateBranchInput{
			BranchID:       "br-child",
			ProblemRef:     goalproblem.RevisionRef{ID: "prob-1", Revision: 1},
			ParentBranchID: "br-root",
			Title:          "Ledger lock contention",
			Scope:          "Ledger commit step",
			Boundary:       "Excludes inventory reservation",
			Rationale:      "Lock wait concentrated on ledger table",
		},
	}, actorAuthor)

	// Revise branch preserves identity and immutable rev 1.
	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionReviseBranch,
		ReviseBranch: &goalproblem.ReviseBranchInput{
			BranchID:     "br-child",
			ProblemRef:   goalproblem.RevisionRef{ID: "prob-1", Revision: 1},
			Title:        "Ledger lock contention during peak bursts",
			Scope:        "Ledger commit and outbox write",
			Boundary:     "Excludes inventory reservation",
			Rationale:    "Outbox row shares lock group",
			ChangeReason: "Profiler revealed outbox contention",
		},
	}, actorAuthor)

	br, ok := s.Branch("br-child")
	if !ok || br.CurrentRevision != 2 || len(br.Revisions) != 2 {
		t.Fatalf("unexpected branch state: ok=%v br=%+v", ok, br)
	}
	if br.Revisions[0].Title != "Ledger lock contention" || br.Revisions[1].Title != "Ledger lock contention during peak bursts" {
		t.Fatalf("branch revisions not preserved immutably: %+v", br.Revisions)
	}

	// Hypothesis must reference current branch revision (reject stale rev 1).
	mustRejectUnchanged(t, &s, goalproblem.Command{
		Action: goalproblem.ActionCreateHypothesis,
		CreateHypothesis: &goalproblem.CreateHypothesisInput{
			HypothesisID:           "hyp-1",
			BranchRef:              goalproblem.RevisionRef{ID: "br-child", Revision: 1},
			Statement:              "Async ledger cuts latency",
			FalsificationCriterion: "p95 drop < 150ms",
		},
	}, actorAuthor, goalproblem.ErrConflict, goalproblem.ErrStaleRevision)

	// Hypothesis requires falsification criterion.
	mustRejectUnchanged(t, &s, goalproblem.Command{
		Action: goalproblem.ActionCreateHypothesis,
		CreateHypothesis: &goalproblem.CreateHypothesisInput{
			HypothesisID:           "hyp-1",
			BranchRef:              goalproblem.RevisionRef{ID: "br-child", Revision: 2},
			Statement:              "Async ledger cuts latency",
			FalsificationCriterion: "   ",
		},
	}, actorAuthor, goalproblem.ErrInvalidInput)

	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionCreateHypothesis,
		CreateHypothesis: &goalproblem.CreateHypothesisInput{
			HypothesisID:           "hyp-1",
			BranchRef:              goalproblem.RevisionRef{ID: "br-child", Revision: 2},
			Statement:              "Async ledger cuts latency",
			FalsificationCriterion: "p95 drop < 150ms",
		},
	}, actorAuthor)

	// Revising problem creates rev 2 while preserving rev 1.
	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionReviseProblem,
		ReviseProblem: &goalproblem.ReviseProblemInput{
			ProblemID:        "prob-1",
			Statement:        "Checkout p95 exceeds 850ms on web and mobile",
			Baseline:         "870ms web / 920ms mobile",
			AffectedUsers:    []string{"web shoppers", "mobile shoppers"},
			Constraints:      []string{"no payment vendor swap"},
			SuccessCriterion: "p95 <= 400ms on both surfaces",
			ReopenReason:     "Mobile telemetry added",
		},
	}, actorAuthor)

	prob, ok := s.Problem("prob-1")
	if !ok || prob.CurrentRevision != 2 || len(prob.Revisions) != 2 {
		t.Fatalf("unexpected problem state: ok=%v prob=%+v", ok, prob)
	}
	if prob.Revisions[0].Status != goalproblem.ProblemStatusSuperseded || prob.Revisions[1].Status != goalproblem.ProblemStatusDraft {
		t.Fatalf("unexpected problem revision statuses: %+v", prob.Revisions)
	}

	// Charter remains intact at v1.
	if s.Current().Number != 1 {
		t.Fatalf("preparatory work changed goal version to %d", s.Current().Number)
	}
}

func TestPrioritizationDeferralAndReconsideration(t *testing.T) {
	s := seedActiveBranch(t)
	seedApprovedProtocol(t, &s, "proto-1", goalproblem.RevisionRef{ID: "hyp-db", Revision: 1})

	// Deferring without a reopening condition is rejected.
	mustRejectUnchanged(t, &s, goalproblem.Command{
		Action: goalproblem.ActionRecordPrioritization,
		RecordPrioritization: &goalproblem.RecordPrioritizationInput{
			PrioritizationID: "prio-bad",
			BranchRef:        goalproblem.RevisionRef{ID: "br-db", Revision: 1},
			Disposition:      goalproblem.DispositionDefer,
			Impact:           goalproblem.LevelHigh,
			Controllability:  goalproblem.LevelLow,
			Cost:             goalproblem.LevelHigh,
			Rationale:        "Needs schema freeze window",
		},
	}, actorAuthor, goalproblem.ErrInvalidInput)

	// Defer branch with reopening condition.
	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionRecordPrioritization,
		RecordPrioritization: &goalproblem.RecordPrioritizationInput{
			PrioritizationID:   "prio-1",
			BranchRef:          goalproblem.RevisionRef{ID: "br-db", Revision: 1},
			Disposition:        goalproblem.DispositionDefer,
			Impact:             goalproblem.LevelHigh,
			Controllability:    goalproblem.LevelLow,
			Cost:               goalproblem.LevelHigh,
			ReopeningCondition: "Reopen after Q4 freeze lifts or if p95 exceeds 1000ms",
			Rationale:          "Too risky during holiday freeze",
		},
	}, actorAuthor)

	br, _ := s.Branch("br-db")
	if br.Status != goalproblem.BranchStatusDeferred {
		t.Fatalf("branch status = %q, want deferred", br.Status)
	}

	// Deferred branch cannot start validation batches.
	mustRejectUnchanged(t, &s, goalproblem.Command{
		Action: goalproblem.ActionStartValidationBatch,
		StartValidationBatch: &goalproblem.StartValidationBatchInput{
			BatchID:       "batch-blocked",
			HypothesisRef: goalproblem.RevisionRef{ID: "hyp-db", Revision: 1},
			ProtocolRef:   goalproblem.RevisionRef{ID: "proto-1", Revision: 1},
			Task:          goalproblem.TaskRef{IssueID: issueTask1, SubmitterID: actorSubmitter.ID},
		},
	}, actorAuthor, goalproblem.ErrConflict)

	// Reconsider the deferred branch via a new prioritization record.
	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionRecordPrioritization,
		RecordPrioritization: &goalproblem.RecordPrioritizationInput{
			PrioritizationID:            "prio-2",
			BranchRef:                   goalproblem.RevisionRef{ID: "br-db", Revision: 1},
			Disposition:                 goalproblem.DispositionPrioritize,
			Rank:                        1,
			Impact:                      goalproblem.LevelHigh,
			Controllability:             goalproblem.LevelHigh,
			Cost:                        goalproblem.LevelMedium,
			Rationale:                   "Freeze ended and p95 hit 1050ms",
			ReconsidersPrioritizationID: "prio-1",
		},
	}, actorAuthor)

	br, _ = s.Branch("br-db")
	if br.Status != goalproblem.BranchStatusActive {
		t.Fatalf("reconsidered branch status = %q, want active", br.Status)
	}
	if len(s.Prioritizations) != 2 || s.Prioritizations[1].ReconsidersPrioritizationID != "prio-1" {
		t.Fatalf("prioritization history not preserved: %+v", s.Prioritizations)
	}

	// Reconsidered branch can now start a validation batch.
	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionStartValidationBatch,
		StartValidationBatch: &goalproblem.StartValidationBatchInput{
			BatchID:       "batch-1",
			HypothesisRef: goalproblem.RevisionRef{ID: "hyp-db", Revision: 1},
			ProtocolRef:   goalproblem.RevisionRef{ID: "proto-1", Revision: 1},
			Task:          goalproblem.TaskRef{IssueID: issueTask1, SubmitterID: actorSubmitter.ID},
		},
	}, actorAuthor)

	// Priority changes NEVER alter the live charter.
	if s.Current().Number != 1 || s.Current().Charter.Title != baseCharter().Title {
		t.Fatalf("prioritization unexpectedly mutated charter: %+v", s.Current())
	}
}

func TestValidationProtocolVersionedApprovalsAndRollback(t *testing.T) {
	s := seedActiveBranch(t)

	// Draft protocol rev 1.
	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionRecordProtocol,
		RecordProtocol: &goalproblem.RecordProtocolInput{
			ProtocolID:          "proto-1",
			HypothesisRef:       goalproblem.RevisionRef{ID: "hyp-db", Revision: 1},
			PredictedResult:     "p95 <= 350ms",
			SampleSource:        "10k replay orders",
			ControlOrComparison: "sync baseline",
			DecisionThreshold:   "p95 <= 400ms",
			StopCondition:       "error rate > 0.5%",
		},
	}, actorAuthor)

	// Cannot start batch before protocol approval.
	mustRejectUnchanged(t, &s, goalproblem.Command{
		Action: goalproblem.ActionStartValidationBatch,
		StartValidationBatch: &goalproblem.StartValidationBatchInput{
			BatchID:       "batch-1",
			HypothesisRef: goalproblem.RevisionRef{ID: "hyp-db", Revision: 1},
			ProtocolRef:   goalproblem.RevisionRef{ID: "proto-1", Revision: 1},
			Task:          goalproblem.TaskRef{IssueID: issueTask1},
		},
	}, actorAuthor, goalproblem.ErrConflict)

	// Author cannot self-approve protocol.
	mustRejectUnchanged(t, &s, goalproblem.Command{
		Action: goalproblem.ActionApproveProtocol,
		ApproveProtocol: &goalproblem.ApproveProtocolInput{
			ProtocolRef: goalproblem.RevisionRef{ID: "proto-1", Revision: 1},
		},
	}, actorAuthor, goalproblem.ErrForbidden, goalproblem.ErrIndependentReviewRequired)

	// Independent reviewer approves rev 1.
	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionApproveProtocol,
		ApproveProtocol: &goalproblem.ApproveProtocolInput{
			ProtocolRef: goalproblem.RevisionRef{ID: "proto-1", Revision: 1},
		},
	}, actorReviewer)

	// Invalid task issue UUID is rejected with rollback.
	mustRejectUnchanged(t, &s, goalproblem.Command{
		Action: goalproblem.ActionStartValidationBatch,
		StartValidationBatch: &goalproblem.StartValidationBatchInput{
			BatchID:       "batch-bad-uuid",
			HypothesisRef: goalproblem.RevisionRef{ID: "hyp-db", Revision: 1},
			ProtocolRef:   goalproblem.RevisionRef{ID: "proto-1", Revision: 1},
			Task:          goalproblem.TaskRef{IssueID: "not-a-uuid"},
		},
	}, actorAuthor, goalproblem.ErrInvalidInput)

	// Start batch 1 against (hyp-db rev 1, proto-1 rev 1).
	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionStartValidationBatch,
		StartValidationBatch: &goalproblem.StartValidationBatchInput{
			BatchID:       "batch-1",
			HypothesisRef: goalproblem.RevisionRef{ID: "hyp-db", Revision: 1},
			ProtocolRef:   goalproblem.RevisionRef{ID: "proto-1", Revision: 1},
			Task:          goalproblem.TaskRef{IssueID: issueTask1, SubmitterID: actorSubmitter.ID},
		},
	}, actorAuthor)

	// Revise protocol to rev 2; in-flight batch-1 must keep its rev 1 pin untouched.
	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionReviseProtocol,
		ReviseProtocol: &goalproblem.ReviseProtocolInput{
			ProtocolID:          "proto-1",
			HypothesisRef:       goalproblem.RevisionRef{ID: "hyp-db", Revision: 1},
			PredictedResult:     "p95 <= 300ms",
			SampleSource:        "50k replay orders",
			ControlOrComparison: "sync baseline with cold cache",
			DecisionThreshold:   "p95 <= 350ms",
			StopCondition:       "error rate > 0.2%",
			ChangeReason:        "Increase sample size for burst tail",
		},
	}, actorAuthor)

	batch1, ok := s.ValidationBatch("batch-1")
	if !ok || batch1.ProtocolRef.Revision != 1 {
		t.Fatalf("in-flight batch-1 protocol ref altered: ok=%v batch=%+v", ok, batch1)
	}
	proto, _ := s.Protocol("proto-1")
	if proto.CurrentRevision != 2 || len(proto.Revisions) != 2 || !proto.Revisions[0].Approved || proto.Revisions[0].Status != goalproblem.ProtocolStatusSuperseded {
		t.Fatalf("protocol history mismatch: %+v", proto)
	}

	// Cannot start a new batch on superseded rev 1 or unapproved rev 2.
	mustRejectUnchanged(t, &s, goalproblem.Command{
		Action: goalproblem.ActionStartValidationBatch,
		StartValidationBatch: &goalproblem.StartValidationBatchInput{
			BatchID:       "batch-old",
			HypothesisRef: goalproblem.RevisionRef{ID: "hyp-db", Revision: 1},
			ProtocolRef:   goalproblem.RevisionRef{ID: "proto-1", Revision: 1},
			Task:          goalproblem.TaskRef{IssueID: issueTask2},
		},
	}, actorAuthor, goalproblem.ErrConflict)

	mustRejectUnchanged(t, &s, goalproblem.Command{
		Action: goalproblem.ActionStartValidationBatch,
		StartValidationBatch: &goalproblem.StartValidationBatchInput{
			BatchID:       "batch-unapproved",
			HypothesisRef: goalproblem.RevisionRef{ID: "hyp-db", Revision: 1},
			ProtocolRef:   goalproblem.RevisionRef{ID: "proto-1", Revision: 2},
			Task:          goalproblem.TaskRef{IssueID: issueTask2},
		},
	}, actorAuthor, goalproblem.ErrConflict)

	// Approve rev 2 and start batch-2.
	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionApproveProtocol,
		ApproveProtocol: &goalproblem.ApproveProtocolInput{
			ProtocolRef: goalproblem.RevisionRef{ID: "proto-1", Revision: 2},
		},
	}, actorReviewer)

	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionStartValidationBatch,
		StartValidationBatch: &goalproblem.StartValidationBatchInput{
			BatchID:       "batch-2",
			HypothesisRef: goalproblem.RevisionRef{ID: "hyp-db", Revision: 1},
			ProtocolRef:   goalproblem.RevisionRef{ID: "proto-1", Revision: 2},
			Task:          goalproblem.TaskRef{IssueID: issueTask2, SubmitterID: actorSubmitter.ID},
		},
	}, actorAuthor)
}

func TestThreeVerdictsUnrelatedBranchesAndLateReview(t *testing.T) {
	s := seedActiveBranch(t)

	// Add a second unrelated branch (br-cache) and hypothesis (hyp-cache).
	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionCreateBranch,
		CreateBranch: &goalproblem.CreateBranchInput{
			BranchID:   "br-cache",
			ProblemRef: goalproblem.RevisionRef{ID: "prob-1", Revision: 1},
			Title:      "Pricing catalog cache misses",
			Scope:      "Read-only price lookup",
			Boundary:   "Excludes ledger write path",
			Rationale:  "Secondary contributor to checkout latency",
		},
	}, actorAuthor)
	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionCreateHypothesis,
		CreateHypothesis: &goalproblem.CreateHypothesisInput{
			HypothesisID:           "hyp-cache",
			BranchRef:              goalproblem.RevisionRef{ID: "br-cache", Revision: 1},
			Statement:              "Warming pricing cache trims 120ms from p95",
			FalsificationCriterion: "p95 delta < 40ms under peak replay",
		},
	}, actorAuthor)

	seedApprovedProtocol(t, &s, "proto-db", goalproblem.RevisionRef{ID: "hyp-db", Revision: 1})
	seedApprovedProtocol(t, &s, "proto-cache", goalproblem.RevisionRef{ID: "hyp-cache", Revision: 1})

	// 1. INSUFFICIENT verdict branch: start batch-insuff on hyp-db, record result, reject non-independent reviewers, review as insufficient.
	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionStartValidationBatch,
		StartValidationBatch: &goalproblem.StartValidationBatchInput{
			BatchID:       "batch-insuff",
			HypothesisRef: goalproblem.RevisionRef{ID: "hyp-db", Revision: 1},
			ProtocolRef:   goalproblem.RevisionRef{ID: "proto-db", Revision: 1},
			Task:          goalproblem.TaskRef{IssueID: issueTask1, SubmitterID: actorSubmitter.ID},
		},
	}, actorAuthor)
	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionRecordValidationResult,
		RecordValidationResult: &goalproblem.RecordValidationResultInput{
			ResultID:     "res-insuff",
			BatchID:      "batch-insuff",
			Observations: []string{"Traffic replay terminated at 800 orders due to staging restart"},
			Evidence: []goalproblem.EvidenceRef{
				{ID: "ev-1", Kind: goalproblem.EvidenceKindIssue, IssueID: issueEvid1, Summary: "Partial replay log"},
			},
		},
	}, actorRecorder)

	// Recorder cannot review their own result.
	mustRejectUnchanged(t, &s, goalproblem.Command{
		Action: goalproblem.ActionReviewValidationResult,
		ReviewValidationResult: &goalproblem.ReviewValidationResultInput{
			ResultID:  "res-insuff",
			Verdict:   goalproblem.VerdictInsufficient,
			Rationale: "Sample too small",
		},
	}, actorRecorder, goalproblem.ErrForbidden, goalproblem.ErrIndependentReviewRequired)

	// Task submitter cannot review either.
	mustRejectUnchanged(t, &s, goalproblem.Command{
		Action: goalproblem.ActionReviewValidationResult,
		ReviewValidationResult: &goalproblem.ReviewValidationResultInput{
			ResultID:  "res-insuff",
			Verdict:   goalproblem.VerdictInsufficient,
			Rationale: "Sample too small",
		},
	}, actorSubmitter, goalproblem.ErrForbidden, goalproblem.ErrIndependentReviewRequired)

	// Independent reviewer assigns insufficient.
	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionReviewValidationResult,
		ReviewValidationResult: &goalproblem.ReviewValidationResultInput{
			ResultID:  "res-insuff",
			Verdict:   goalproblem.VerdictInsufficient,
			Rationale: "Only 8% of required sample completed",
		},
	}, actorReviewer)

	// Insufficient result alone CANNOT justify recommending a charter revision.
	mustRejectUnchanged(t, &s, goalproblem.Command{
		Action: goalproblem.ActionRecordSynthesis,
		RecordSynthesis: &goalproblem.RecordSynthesisInput{
			SynthesisID:     "syn-insuff",
			BaseGoalVersion: 1,
			Claims: []goalproblem.SynthesisClaim{
				{ID: "c1", Statement: "Change charter based on partial run", ResultIDs: []string{"res-insuff"}},
			},
			Tradeoffs:      []string{"None"},
			Limitations:    []string{"Small sample"},
			Recommendation: goalproblem.DecisionRevise,
			Rationale:      "Premature change",
		},
	}, actorAuthor, goalproblem.ErrConflict, goalproblem.ErrInsufficientEvidence)

	// Insufficient allows starting a follow-up batch referencing the prior batch.
	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionStartValidationBatch,
		StartValidationBatch: &goalproblem.StartValidationBatchInput{
			BatchID:       "batch-refute",
			HypothesisRef: goalproblem.RevisionRef{ID: "hyp-db", Revision: 1},
			ProtocolRef:   goalproblem.RevisionRef{ID: "proto-db", Revision: 1},
			Task:          goalproblem.TaskRef{IssueID: issueTask2, SubmitterID: actorSubmitter.ID},
			PriorBatchID:  "batch-insuff",
		},
	}, actorAuthor)

	// 2. REFUTED verdict branch: refutes hyp-db -> br-db needs reassessment, br-cache stays active, charter stays v1!
	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionRecordValidationResult,
		RecordValidationResult: &goalproblem.RecordValidationResultInput{
			ResultID:     "res-refute",
			BatchID:      "batch-refute",
			Observations: []string{"Full 10k replay reduced p95 by only 45ms"},
			Evidence: []goalproblem.EvidenceRef{
				{ID: "ev-2", Kind: goalproblem.EvidenceKindArtifact, Locator: "artifact://replay-2", Summary: "Full latency histogram"},
			},
		},
	}, actorRecorder)
	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionReviewValidationResult,
		ReviewValidationResult: &goalproblem.ReviewValidationResultInput{
			ResultID:  "res-refute",
			Verdict:   goalproblem.VerdictRefuted,
			Rationale: "45ms reduction is well below the 150ms falsification threshold",
		},
	}, actorReviewer)

	brDB, _ := s.Branch("br-db")
	brCache, _ := s.Branch("br-cache")
	if brDB.Status != goalproblem.BranchStatusNeedsReassessment {
		t.Fatalf("br-db status = %q, want needs_reassessment", brDB.Status)
	}
	if brCache.Status != goalproblem.BranchStatusActive {
		t.Fatalf("unrelated br-cache status = %q, want active", brCache.Status)
	}
	if s.Current().Number != 1 {
		t.Fatalf("refutation mutated live goal version to %d", s.Current().Number)
	}

	// Cannot start new validation batch on br-db until reassessed.
	mustRejectUnchanged(t, &s, goalproblem.Command{
		Action: goalproblem.ActionStartValidationBatch,
		StartValidationBatch: &goalproblem.StartValidationBatchInput{
			BatchID:       "batch-blocked-reassess",
			HypothesisRef: goalproblem.RevisionRef{ID: "hyp-db", Revision: 1},
			ProtocolRef:   goalproblem.RevisionRef{ID: "proto-db", Revision: 1},
			Task:          goalproblem.TaskRef{IssueID: issueTask3},
		},
	}, actorAuthor, goalproblem.ErrConflict, goalproblem.ErrReassessmentRequired)

	// Reassess br-db (revise_scope -> creates br-db rev 2 and restores active status).
	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionReassessBranch,
		ReassessBranch: &goalproblem.ReassessBranchInput{
			BranchRef:            goalproblem.RevisionRef{ID: "br-db", Revision: 1},
			ChallengedByResultID: "res-refute",
			Outcome:              goalproblem.ReassessmentReviseScope,
			Rationale:            "Contention was actually on payment token lock, not ledger insert",
			NewTitle:             "Payment token lock contention",
			NewScope:             "Token verification write path",
			NewBoundary:          "Excludes ledger table",
		},
	}, actorAuthor)

	brDB, _ = s.Branch("br-db")
	if brDB.Status != goalproblem.BranchStatusActive || brDB.CurrentRevision != 2 || len(brDB.Reassessments) != 1 || !brDB.Reassessments[0].Resolved {
		t.Fatalf("unexpected reassessed br-db: %+v", brDB)
	}
	mustRejectUnchanged(t, &s, goalproblem.Command{
		Action: goalproblem.ActionStartValidationBatch,
		StartValidationBatch: &goalproblem.StartValidationBatchInput{
			BatchID: "batch-on-old-scope", HypothesisRef: goalproblem.RevisionRef{ID: "hyp-db", Revision: 1},
			ProtocolRef: goalproblem.RevisionRef{ID: "proto-db", Revision: 1}, Task: goalproblem.TaskRef{IssueID: issueTask3},
		},
	}, actorAuthor, goalproblem.ErrConflict, goalproblem.ErrStaleRevision)
	if s.IsResultCurrent("res-refute") {
		t.Fatal("the refutation of the original scope was promoted to the revised scope")
	}

	// 3. SUPPORTED verdict + LATE REVIEW check on unrelated br-cache:
	// Start two batches on hyp-cache rev 1: batch-late (left unreviewed until hyp-cache changes) and batch-support.
	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionStartValidationBatch,
		StartValidationBatch: &goalproblem.StartValidationBatchInput{
			BatchID:       "batch-late",
			HypothesisRef: goalproblem.RevisionRef{ID: "hyp-cache", Revision: 1},
			ProtocolRef:   goalproblem.RevisionRef{ID: "proto-cache", Revision: 1},
			Task:          goalproblem.TaskRef{IssueID: issueTask3, SubmitterID: actorSubmitter.ID},
		},
	}, actorAuthor)
	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionRecordValidationResult,
		RecordValidationResult: &goalproblem.RecordValidationResultInput{
			ResultID:     "res-late",
			BatchID:      "batch-late",
			Observations: []string{"p95 trimmed by 140ms under rev 1"},
			Evidence: []goalproblem.EvidenceRef{
				{ID: "ev-late", Kind: goalproblem.EvidenceKindDataset, Locator: "dataset://cache-run-1", Summary: "Run 1"},
			},
		},
	}, actorRecorder)

	// Revise hyp-cache to rev 2 BEFORE res-late is reviewed.
	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionReviseHypothesis,
		ReviseHypothesis: &goalproblem.ReviseHypothesisInput{
			HypothesisID:           "hyp-cache",
			BranchRef:              goalproblem.RevisionRef{ID: "br-cache", Revision: 1},
			Statement:              "Warming pricing cache + negative caching trims 220ms from p95",
			FalsificationCriterion: "p95 delta < 100ms under peak replay",
			ChangeReason:           "Expanded scope to negative lookups",
		},
	}, actorAuthor)

	// Late review of res-late is recorded into history with LateForCurrentRevisions=true.
	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionReviewValidationResult,
		ReviewValidationResult: &goalproblem.ReviewValidationResultInput{
			ResultID:  "res-late",
			Verdict:   goalproblem.VerdictSupported,
			Rationale: "Valid for rev 1, though hypothesis has moved to rev 2",
		},
	}, actorReviewer)

	resLate, _ := s.ValidationResult("res-late")
	if resLate.Review == nil || !resLate.Review.LateForCurrentRevisions {
		t.Fatalf("expected late review flag on res-late: %+v", resLate.Review)
	}
	if s.IsResultCurrent("res-late") {
		t.Fatalf("late result must not be reported as current for altered hypothesis")
	}

	// Late review CANNOT validate altered hyp-cache rev 2 inside a synthesis claim.
	mustRejectUnchanged(t, &s, goalproblem.Command{
		Action: goalproblem.ActionRecordSynthesis,
		RecordSynthesis: &goalproblem.RecordSynthesisInput{
			SynthesisID:     "syn-stale",
			BaseGoalVersion: 1,
			Claims: []goalproblem.SynthesisClaim{
				{ID: "c1", Statement: "Hyp-cache rev 2 is validated", ResultIDs: []string{"res-late"}},
			},
			Tradeoffs:      []string{"RAM cost"},
			Limitations:    []string{"Cold start"},
			Recommendation: goalproblem.DecisionRetain,
			Rationale:      "Attempt to reuse late review",
		},
	}, actorAuthor, goalproblem.ErrConflict, goalproblem.ErrStaleRevision)

	// Now revise proto-cache for hyp-cache rev 2, approve, run batch-support, and get a current supported result.
	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionReviseProtocol,
		ReviseProtocol: &goalproblem.ReviseProtocolInput{
			ProtocolID:          "proto-cache",
			HypothesisRef:       goalproblem.RevisionRef{ID: "hyp-cache", Revision: 2},
			PredictedResult:     "p95 drops by >= 220ms",
			SampleSource:        "10k peak replay with cold misses",
			ControlOrComparison: "unwarmed cache",
			DecisionThreshold:   "p95 delta >= 150ms",
			StopCondition:       "stale price rate > 0.01%",
			ChangeReason:        "Aligned protocol to hyp-cache rev 2",
		},
	}, actorAuthor)
	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionApproveProtocol,
		ApproveProtocol: &goalproblem.ApproveProtocolInput{
			ProtocolRef: goalproblem.RevisionRef{ID: "proto-cache", Revision: 2},
		},
	}, actorReviewer)
	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionStartValidationBatch,
		StartValidationBatch: &goalproblem.StartValidationBatchInput{
			BatchID:       "batch-support",
			HypothesisRef: goalproblem.RevisionRef{ID: "hyp-cache", Revision: 2},
			ProtocolRef:   goalproblem.RevisionRef{ID: "proto-cache", Revision: 2},
			Task:          goalproblem.TaskRef{IssueID: issueTask4, SubmitterID: actorSubmitter.ID},
		},
	}, actorAuthor)
	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionRecordValidationResult,
		RecordValidationResult: &goalproblem.RecordValidationResultInput{
			ResultID:     "res-support",
			BatchID:      "batch-support",
			Observations: []string{"p95 dropped by 245ms with zero stale prices"},
			Evidence: []goalproblem.EvidenceRef{
				{ID: "ev-sup", Kind: goalproblem.EvidenceKindIssue, IssueID: issueEvid2, Summary: "Replay report"},
			},
		},
	}, actorRecorder)
	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionReviewValidationResult,
		ReviewValidationResult: &goalproblem.ReviewValidationResultInput{
			ResultID:  "res-support",
			Verdict:   goalproblem.VerdictSupported,
			Rationale: "Exceeds 150ms threshold cleanly",
		},
	}, actorReviewer)

	if !s.IsResultCurrent("res-support") {
		t.Fatalf("expected res-support to be current")
	}

	// Supported result leads to retaining the charter via synthesis + authorized decision.
	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionRecordSynthesis,
		RecordSynthesis: &goalproblem.RecordSynthesisInput{
			SynthesisID:     "syn-retain",
			BaseGoalVersion: 1,
			Claims: []goalproblem.SynthesisClaim{
				{ID: "c1", Statement: "Cache warming meets v1 charter without architecture rewrite", ResultIDs: []string{"res-support"}},
			},
			Tradeoffs:      []string{"+128MB cache memory per node"},
			Limitations:    []string{"Requires invalidation hook on catalog updates"},
			Recommendation: goalproblem.DecisionRetain,
			Rationale:      "Current charter scope and targets remain valid",
		},
	}, actorAuthor)
	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionReviewSynthesis,
		ReviewSynthesis: &goalproblem.ReviewSynthesisInput{
			SynthesisRef: goalproblem.RevisionRef{ID: "syn-retain", Revision: 1},
			ReviewNotes:  "Evidence and tradeoffs verified",
		},
	}, actorReviewer)
	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionRecordDecision,
		RecordDecision: &goalproblem.RecordDecisionInput{
			DecisionID:        "dec-retain-1",
			Kind:              goalproblem.DecisionRetain,
			SynthesisRef:      goalproblem.RevisionRef{ID: "syn-retain", Revision: 1},
			Reason:            "Supported evidence confirms charter v1 is achievable",
			EvidenceResultIDs: []string{"res-support"},
			ReviewTrigger:     "Revisit if checkout p95 exceeds 400ms for 2 consecutive days",
		},
	}, actorOwner)

	if s.Current().Number != 1 || s.GoalStatus != goalproblem.GoalStatusActive {
		t.Fatalf("retain decision altered charter or status: v=%d status=%q", s.Current().Number, s.GoalStatus)
	}
}

func TestSynthesisChallengeReopenAndCharterRevisionProposals(t *testing.T) {
	s := seedActiveBranch(t)
	seedApprovedProtocol(t, &s, "proto-db", goalproblem.RevisionRef{ID: "hyp-db", Revision: 1})

	// Add unrelated branch + supported result + published synthesis to ensure refutation challenges ONLY its precise dependency.
	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionCreateBranch,
		CreateBranch: &goalproblem.CreateBranchInput{
			BranchID:   "br-other",
			ProblemRef: goalproblem.RevisionRef{ID: "prob-1", Revision: 1},
			Title:      "CDN compression",
			Scope:      "Edge responses",
			Boundary:   "Excludes DB",
			Rationale:  "Independent track",
		},
	}, actorAuthor)
	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionCreateHypothesis,
		CreateHypothesis: &goalproblem.CreateHypothesisInput{
			HypothesisID:           "hyp-other",
			BranchRef:              goalproblem.RevisionRef{ID: "br-other", Revision: 1},
			Statement:              "Brotli reduces payload transfer by 80ms",
			FalsificationCriterion: "Delta < 20ms",
		},
	}, actorAuthor)
	seedApprovedProtocol(t, &s, "proto-other", goalproblem.RevisionRef{ID: "hyp-other", Revision: 1})

	// Run supported batch on hyp-db and hyp-other.
	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionStartValidationBatch,
		StartValidationBatch: &goalproblem.StartValidationBatchInput{
			BatchID:       "batch-db-1",
			HypothesisRef: goalproblem.RevisionRef{ID: "hyp-db", Revision: 1},
			ProtocolRef:   goalproblem.RevisionRef{ID: "proto-db", Revision: 1},
			Task:          goalproblem.TaskRef{IssueID: issueTask1, SubmitterID: actorSubmitter.ID},
		},
	}, actorAuthor)
	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionRecordValidationResult,
		RecordValidationResult: &goalproblem.RecordValidationResultInput{
			ResultID:     "res-db-1",
			BatchID:      "batch-db-1",
			Observations: []string{"Async ledger hits 280ms p95"},
			Evidence:     []goalproblem.EvidenceRef{{ID: "ev-db-1", Kind: goalproblem.EvidenceKindIssue, IssueID: issueEvid1, Summary: "Replay 1"}},
		},
	}, actorRecorder)
	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionReviewValidationResult,
		ReviewValidationResult: &goalproblem.ReviewValidationResultInput{
			ResultID:  "res-db-1",
			Verdict:   goalproblem.VerdictSupported,
			Rationale: "Meets decision threshold",
		},
	}, actorReviewer)

	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionStartValidationBatch,
		StartValidationBatch: &goalproblem.StartValidationBatchInput{
			BatchID:       "batch-other-1",
			HypothesisRef: goalproblem.RevisionRef{ID: "hyp-other", Revision: 1},
			ProtocolRef:   goalproblem.RevisionRef{ID: "proto-other", Revision: 1},
			Task:          goalproblem.TaskRef{IssueID: issueTask2, SubmitterID: actorSubmitter.ID},
		},
	}, actorAuthor)
	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionRecordValidationResult,
		RecordValidationResult: &goalproblem.RecordValidationResultInput{
			ResultID:     "res-other-1",
			BatchID:      "batch-other-1",
			Observations: []string{"Brotli trims 90ms"},
			Evidence:     []goalproblem.EvidenceRef{{ID: "ev-o-1", Kind: goalproblem.EvidenceKindArtifact, Locator: "art://edge", Summary: "Edge trace"}},
		},
	}, actorRecorder)
	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionReviewValidationResult,
		ReviewValidationResult: &goalproblem.ReviewValidationResultInput{
			ResultID:  "res-other-1",
			Verdict:   goalproblem.VerdictSupported,
			Rationale: "Confirmed on edge",
		},
	}, actorReviewer)

	// Reject synthesis referencing foreign/missing result ID.
	mustRejectUnchanged(t, &s, goalproblem.Command{
		Action: goalproblem.ActionRecordSynthesis,
		RecordSynthesis: &goalproblem.RecordSynthesisInput{
			SynthesisID:     "syn-bad",
			BaseGoalVersion: 1,
			Claims:          []goalproblem.SynthesisClaim{{ID: "c1", Statement: "Foreign", ResultIDs: []string{"res-does-not-exist"}}},
			Tradeoffs:       []string{"t"},
			Limitations:     []string{"l"},
			Recommendation:  goalproblem.DecisionRevise,
			Rationale:       "r",
		},
	}, actorAuthor, goalproblem.ErrNotFound)

	// Publish synthesis on hyp-db (syn-db) and unrelated synthesis on hyp-other (syn-other).
	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionRecordSynthesis,
		RecordSynthesis: &goalproblem.RecordSynthesisInput{
			SynthesisID:     "syn-db",
			BaseGoalVersion: 1,
			Claims:          []goalproblem.SynthesisClaim{{ID: "c1", Statement: "Async settlement justifies charter expansion", ResultIDs: []string{"res-db-1"}}},
			Tradeoffs:       []string{"Eventual settlement lag up to 30s"},
			Limitations:     []string{"Single region replay only"},
			Recommendation:  goalproblem.DecisionRevise,
			Rationale:       "Charter must include async settlement scope",
		},
	}, actorAuthor)
	// Author cannot self-review synthesis.
	mustRejectUnchanged(t, &s, goalproblem.Command{
		Action: goalproblem.ActionReviewSynthesis,
		ReviewSynthesis: &goalproblem.ReviewSynthesisInput{
			SynthesisRef: goalproblem.RevisionRef{ID: "syn-db", Revision: 1},
		},
	}, actorAuthor, goalproblem.ErrForbidden, goalproblem.ErrIndependentReviewRequired)

	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionReviewSynthesis,
		ReviewSynthesis: &goalproblem.ReviewSynthesisInput{
			SynthesisRef: goalproblem.RevisionRef{ID: "syn-db", Revision: 1},
		},
	}, actorReviewer)

	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionRecordSynthesis,
		RecordSynthesis: &goalproblem.RecordSynthesisInput{
			SynthesisID:     "syn-other",
			BaseGoalVersion: 1,
			Claims:          []goalproblem.SynthesisClaim{{ID: "c1", Statement: "Edge compression helps", ResultIDs: []string{"res-other-1"}}},
			Tradeoffs:       []string{"CPU overhead"},
			Limitations:     []string{"Static assets only"},
			Recommendation:  goalproblem.DecisionRetain,
			Rationale:       "Keep current charter",
		},
	}, actorAuthor)
	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionReviewSynthesis,
		ReviewSynthesis: &goalproblem.ReviewSynthesisInput{
			SynthesisRef: goalproblem.RevisionRef{ID: "syn-other", Revision: 1},
		},
	}, actorReviewer)

	// Open a charter revision proposal grounded in syn-db rev 1.
	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionProposeCharterRevision,
		ProposeCharterRevision: &goalproblem.ProposeCharterRevisionInput{
			ProposalID:      "prop-challenged",
			BaseGoalVersion: 1,
			SynthesisRef:    goalproblem.RevisionRef{ID: "syn-db", Revision: 1},
			ProposedCharter: revisedCharter(),
			ChangeSummary:   "Include async settlement scope",
		},
	}, actorAuthor)

	// Now a follow-up batch on hyp-db comes back REFUTED -> challenges syn-db (precise dependency), NOT syn-other!
	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionStartValidationBatch,
		StartValidationBatch: &goalproblem.StartValidationBatchInput{
			BatchID:       "batch-db-2",
			HypothesisRef: goalproblem.RevisionRef{ID: "hyp-db", Revision: 1},
			ProtocolRef:   goalproblem.RevisionRef{ID: "proto-db", Revision: 1},
			Task:          goalproblem.TaskRef{IssueID: issueTask3, SubmitterID: actorSubmitter.ID},
		},
	}, actorAuthor)
	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionRecordValidationResult,
		RecordValidationResult: &goalproblem.RecordValidationResultInput{
			ResultID:     "res-db-2",
			BatchID:      "batch-db-2",
			Observations: []string{"Multi-region replay caused duplicate settlement under failover"},
			Evidence:     []goalproblem.EvidenceRef{{ID: "ev-db-2", Kind: goalproblem.EvidenceKindIssue, IssueID: issueEvid2, Summary: "Failover incident"}},
		},
	}, actorRecorder)
	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionReviewValidationResult,
		ReviewValidationResult: &goalproblem.ReviewValidationResultInput{
			ResultID:  "res-db-2",
			Verdict:   goalproblem.VerdictRefuted,
			Rationale: "Violated zero-duplicate stop condition",
		},
	}, actorReviewer)

	synDB, _ := s.Synthesis("syn-db")
	synOther, _ := s.Synthesis("syn-other")
	if synDB.Revisions[0].Status != goalproblem.SynthesisStatusChallenged || len(synDB.Revisions[0].ChallengedBy) != 1 {
		t.Fatalf("expected syn-db rev 1 to be challenged: %+v", synDB.Revisions[0])
	}
	if synOther.Revisions[0].Status != goalproblem.SynthesisStatusPublished {
		t.Fatalf("unrelated syn-other should remain published: %+v", synOther.Revisions[0])
	}

	// Proposal based on challenged synthesis is now stale and rejected even for decision-maker.
	mustRejectUnchanged(t, &s, goalproblem.Command{
		Action: goalproblem.ActionApplyCharterRevision,
		ApplyCharterRevision: &goalproblem.ApplyCharterRevisionInput{
			ProposalID: "prop-challenged",
		},
	}, actorOwner, goalproblem.ErrConflict, goalproblem.ErrStaleProposal)

	// Reopen syn-db -> rev 2 addressing counterevidence, publish rev 2.
	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionReopenSynthesis,
		ReopenSynthesis: &goalproblem.ReopenSynthesisInput{
			SynthesisID:     "syn-db",
			BaseGoalVersion: 1,
			ReopenReason:    "Incorporate failover refutation and revise charter with strict idempotency scope",
			Claims: []goalproblem.SynthesisClaim{
				{ID: "c1", Statement: "Async path delivers latency target", ResultIDs: []string{"res-db-1"}},
				{ID: "c2", Statement: "Failover refutation requires explicit idempotency acceptance criteria", ResultIDs: []string{"res-db-2"}},
			},
			Tradeoffs:      []string{"Requires distributed idempotency ledger"},
			Limitations:    []string{"Counterevidence res-db-2 failed under region failover"},
			Recommendation: goalproblem.DecisionRevise,
			Rationale:      "Charter must explicitly scope idempotent async settlement",
		},
	}, actorAuthor)
	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionReviewSynthesis,
		ReviewSynthesis: &goalproblem.ReviewSynthesisInput{
			SynthesisRef: goalproblem.RevisionRef{ID: "syn-db", Revision: 2},
			ReviewNotes:  "Counterevidence properly addressed",
		},
	}, actorReviewer)

	// Unauthorized actor cannot record goal decision.
	mustRejectUnchanged(t, &s, goalproblem.Command{
		Action: goalproblem.ActionRecordDecision,
		RecordDecision: &goalproblem.RecordDecisionInput{
			DecisionID:        "dec-unauth",
			Kind:              goalproblem.DecisionRevise,
			SynthesisRef:      goalproblem.RevisionRef{ID: "syn-db", Revision: 2},
			Reason:            "Revise charter",
			EvidenceResultIDs: []string{"res-db-1", "res-db-2"},
			ReviewTrigger:     "After 14 days in production",
		},
	}, actorAuthor, goalproblem.ErrForbidden)

	// Authorized owner records revise decision.
	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionRecordDecision,
		RecordDecision: &goalproblem.RecordDecisionInput{
			DecisionID:        "dec-revise-1",
			Kind:              goalproblem.DecisionRevise,
			SynthesisRef:      goalproblem.RevisionRef{ID: "syn-db", Revision: 2},
			Reason:            "Adopt async settlement with strict failover idempotency",
			EvidenceResultIDs: []string{"res-db-1", "res-db-2"},
			ReviewTrigger:     "Revisit after 14 days post-rollout or any settlement lag > 30s",
		},
	}, actorOwner)

	// No-op charter proposal (identical to current charter) is rejected.
	mustRejectUnchanged(t, &s, goalproblem.Command{
		Action: goalproblem.ActionProposeCharterRevision,
		ProposeCharterRevision: &goalproblem.ProposeCharterRevisionInput{
			ProposalID:      "prop-noop",
			BaseGoalVersion: 1,
			SynthesisRef:    goalproblem.RevisionRef{ID: "syn-db", Revision: 2},
			DecisionID:      "dec-revise-1",
			ProposedCharter: baseCharter(),
			ChangeSummary:   "No real change",
		},
	}, actorAuthor, goalproblem.ErrInvalidInput)

	// Propose two competing proposals P1 and P2 against v1.
	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionProposeCharterRevision,
		ProposeCharterRevision: &goalproblem.ProposeCharterRevisionInput{
			ProposalID:      "prop-1",
			BaseGoalVersion: 1,
			SynthesisRef:    goalproblem.RevisionRef{ID: "syn-db", Revision: 2},
			DecisionID:      "dec-revise-1",
			ProposedCharter: revisedCharter(),
			ChangeSummary:   "Adopt async ledger settlement and 300ms target",
		},
	}, actorAuthor)
	altCharter := revisedCharter()
	altCharter.Title = "Alternative async settlement charter"
	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionProposeCharterRevision,
		ProposeCharterRevision: &goalproblem.ProposeCharterRevisionInput{
			ProposalID:      "prop-2",
			BaseGoalVersion: 1,
			SynthesisRef:    goalproblem.RevisionRef{ID: "syn-db", Revision: 2},
			DecisionID:      "dec-revise-1",
			ProposedCharter: altCharter,
			ChangeSummary:   "Alternative phrasing",
		},
	}, actorAuthor)

	// Unauthorized actor cannot apply revision proposal; charter remains v1.
	mustRejectUnchanged(t, &s, goalproblem.Command{
		Action: goalproblem.ActionApplyCharterRevision,
		ApplyCharterRevision: &goalproblem.ApplyCharterRevisionInput{
			ProposalID: "prop-1",
		},
	}, actorAuthor, goalproblem.ErrForbidden)

	// Authorized owner applies prop-1 -> creates exactly one new version (v2), preserves v1.
	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionApplyCharterRevision,
		ApplyCharterRevision: &goalproblem.ApplyCharterRevisionInput{
			ProposalID: "prop-1",
		},
	}, actorOwner)

	if s.Current().Number != 2 || len(s.GoalVersions) != 2 {
		t.Fatalf("expected 2 goal versions, got current=%d len=%d", s.Current().Number, len(s.GoalVersions))
	}
	v1, ok := s.GoalVersionAt(1)
	if !ok || v1.Charter.Title != baseCharter().Title {
		t.Fatalf("v1 snapshot not preserved: ok=%v v1=%+v", ok, v1)
	}
	if s.Current().Charter.Title != revisedCharter().Title {
		t.Fatalf("v2 charter mismatch: %+v", s.Current().Charter)
	}

	// Re-applying prop-1 is rejected.
	mustRejectUnchanged(t, &s, goalproblem.Command{
		Action: goalproblem.ActionApplyCharterRevision,
		ApplyCharterRevision: &goalproblem.ApplyCharterRevisionInput{
			ProposalID: "prop-1",
		},
	}, actorOwner, goalproblem.ErrConflict)

	// Applying stale prop-2 (based on v1 after v2 is live) is rejected.
	mustRejectUnchanged(t, &s, goalproblem.Command{
		Action: goalproblem.ActionApplyCharterRevision,
		ApplyCharterRevision: &goalproblem.ApplyCharterRevisionInput{
			ProposalID: "prop-2",
		},
	}, actorOwner, goalproblem.ErrConflict, goalproblem.ErrStaleProposal)

	// Pause and stop decisions are supported cleanly on v2.
	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionRecordSynthesis,
		RecordSynthesis: &goalproblem.RecordSynthesisInput{
			SynthesisID:     "syn-pause",
			BaseGoalVersion: 2,
			Claims:          []goalproblem.SynthesisClaim{{ID: "c1", Statement: "Pause pending region capacity", ResultIDs: []string{"res-other-1"}}},
			Tradeoffs:       []string{"Delays rollout by 2 weeks"},
			Limitations:     []string{"Region quota pending"},
			Recommendation:  goalproblem.DecisionPause,
			Rationale:       "Hold until cluster quota is provisioned",
		},
	}, actorAuthor)
	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionReviewSynthesis,
		ReviewSynthesis: &goalproblem.ReviewSynthesisInput{
			SynthesisRef: goalproblem.RevisionRef{ID: "syn-pause", Revision: 1},
		},
	}, actorReviewer)
	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionRecordDecision,
		RecordDecision: &goalproblem.RecordDecisionInput{
			DecisionID:        "dec-pause-1",
			Kind:              goalproblem.DecisionPause,
			SynthesisRef:      goalproblem.RevisionRef{ID: "syn-pause", Revision: 1},
			Reason:            "Waiting on multi-region quota",
			EvidenceResultIDs: []string{"res-other-1"},
			ReviewTrigger:     "Resume once quota ticket closes",
		},
	}, actorOwner)

	if s.GoalStatus != goalproblem.GoalStatusPaused || s.Current().Number != 2 {
		t.Fatalf("pause decision mismatch: status=%q version=%d", s.GoalStatus, s.Current().Number)
	}

	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionRecordSynthesis,
		RecordSynthesis: &goalproblem.RecordSynthesisInput{
			SynthesisID:     "syn-stop",
			BaseGoalVersion: 2,
			Claims:          []goalproblem.SynthesisClaim{{ID: "c1", Statement: "Stop goal due to product sunset", ResultIDs: []string{"res-other-1"}}},
			Tradeoffs:       []string{"No further latency spend"},
			Limitations:     []string{"Legacy checkout retiring"},
			Recommendation:  goalproblem.DecisionStop,
			Rationale:       "Legacy checkout is being replaced",
		},
	}, actorAuthor)
	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionReviewSynthesis,
		ReviewSynthesis: &goalproblem.ReviewSynthesisInput{
			SynthesisRef: goalproblem.RevisionRef{ID: "syn-stop", Revision: 1},
		},
	}, actorReviewer)
	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionRecordDecision,
		RecordDecision: &goalproblem.RecordDecisionInput{
			DecisionID:        "dec-stop-1",
			Kind:              goalproblem.DecisionStop,
			SynthesisRef:      goalproblem.RevisionRef{ID: "syn-stop", Revision: 1},
			Reason:            "Legacy checkout deprecated",
			EvidenceResultIDs: []string{"res-other-1"},
			ReviewTrigger:     "Reopen only if deprecation is cancelled",
		},
	}, actorOwner)

	if s.GoalStatus != goalproblem.GoalStatusStopped || s.Current().Number != 2 {
		t.Fatalf("stop decision mismatch: status=%q version=%d", s.GoalStatus, s.Current().Number)
	}
}

func TestJSONRoundTripAndContinuedExecution(t *testing.T) {
	s := seedActiveBranch(t)
	seedApprovedProtocol(t, &s, "proto-db", goalproblem.RevisionRef{ID: "hyp-db", Revision: 1})

	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionRecordPrioritization,
		RecordPrioritization: &goalproblem.RecordPrioritizationInput{
			PrioritizationID: "prio-1",
			BranchRef:        goalproblem.RevisionRef{ID: "br-db", Revision: 1},
			Disposition:      goalproblem.DispositionPrioritize,
			Rank:             1,
			Impact:           goalproblem.LevelHigh,
			Controllability:  goalproblem.LevelHigh,
			Cost:             goalproblem.LevelMedium,
			Rationale:        "Top impact branch",
		},
	}, actorAuthor)

	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionStartValidationBatch,
		StartValidationBatch: &goalproblem.StartValidationBatchInput{
			BatchID:       "batch-1",
			HypothesisRef: goalproblem.RevisionRef{ID: "hyp-db", Revision: 1},
			ProtocolRef:   goalproblem.RevisionRef{ID: "proto-db", Revision: 1},
			Task:          goalproblem.TaskRef{IssueID: issueTask1, SubmitterID: actorSubmitter.ID},
		},
	}, actorAuthor)
	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionRecordValidationResult,
		RecordValidationResult: &goalproblem.RecordValidationResultInput{
			ResultID:     "res-1",
			BatchID:      "batch-1",
			Observations: []string{"p95 dropped to 290ms"},
			Evidence: []goalproblem.EvidenceRef{
				{ID: "ev-1", Kind: goalproblem.EvidenceKindIssue, IssueID: issueEvid1, Summary: "Replay task"},
				{ID: "ev-2", Kind: goalproblem.EvidenceKindArtifact, Locator: "s3://traces/run-1", Summary: "Trace"},
			},
		},
	}, actorRecorder)
	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionReviewValidationResult,
		ReviewValidationResult: &goalproblem.ReviewValidationResultInput{
			ResultID:  "res-1",
			Verdict:   goalproblem.VerdictSupported,
			Rationale: "Clear latency win",
		},
	}, actorReviewer)
	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionRecordSynthesis,
		RecordSynthesis: &goalproblem.RecordSynthesisInput{
			SynthesisID:     "syn-1",
			BaseGoalVersion: 1,
			Claims:          []goalproblem.SynthesisClaim{{ID: "c1", Statement: "Adopt async settlement", ResultIDs: []string{"res-1"}}},
			Tradeoffs:       []string{"Async reconciliation"},
			Limitations:     []string{"Single region"},
			Recommendation:  goalproblem.DecisionRevise,
			Rationale:       "Material scope expansion",
		},
	}, actorAuthor)
	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionReviewSynthesis,
		ReviewSynthesis: &goalproblem.ReviewSynthesisInput{
			SynthesisRef: goalproblem.RevisionRef{ID: "syn-1", Revision: 1},
		},
	}, actorReviewer)
	mustApply(t, &s, goalproblem.Command{
		Action: goalproblem.ActionProposeCharterRevision,
		ProposeCharterRevision: &goalproblem.ProposeCharterRevisionInput{
			ProposalID:      "prop-1",
			BaseGoalVersion: 1,
			SynthesisRef:    goalproblem.RevisionRef{ID: "syn-1", Revision: 1},
			ProposedCharter: revisedCharter(),
			ChangeSummary:   "Expand scope to async settlement",
		},
	}, actorAuthor)

	// Marshal & restore into a brand new State.
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var restored goalproblem.State
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(s, restored) {
		t.Fatalf("JSON round-trip mismatch:\norig=%+v\nrestored=%+v", s, restored)
	}

	// Continue applying commands on restored state: apply prop-1 -> v2.
	mustApply(t, &restored, goalproblem.Command{
		Action: goalproblem.ActionApplyCharterRevision,
		ApplyCharterRevision: &goalproblem.ApplyCharterRevisionInput{
			ProposalID: "prop-1",
		},
	}, actorOwner)

	if restored.Current().Number != 2 {
		t.Fatalf("restored.Current().Number = %d, want 2", restored.Current().Number)
	}
	v1, ok := restored.GoalVersionAt(1)
	if !ok || v1.Charter.Title != baseCharter().Title {
		t.Fatalf("restored v1 snapshot lost: ok=%v v1=%+v", ok, v1)
	}

	// Zero state also round-trips cleanly with Current().Number == 0.
	var empty goalproblem.State
	emptyRaw, err := json.Marshal(empty)
	if err != nil {
		t.Fatalf("marshal empty: %v", err)
	}
	var emptyRestored goalproblem.State
	if err := json.Unmarshal(emptyRaw, &emptyRestored); err != nil {
		t.Fatalf("unmarshal empty: %v", err)
	}
	if emptyRestored.Current().Number != 0 {
		t.Fatalf("emptyRestored.Current().Number = %d, want 0", emptyRestored.Current().Number)
	}
}
