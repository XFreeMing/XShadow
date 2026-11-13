package issueclosure

import (
	"testing"
	"time"
)

var (
	now      = time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	delivery = DeliveryVersion{Version: "e7206f4d5ca0967b4868c70fae3f82e99f16ccf1", Tree: "50c145a22f6e099fc68f9be21933a89fb2d7c790"}
)

func author() Principal {
	return Principal{Type: "agent", ID: "author-agent", RunID: "author-run", Model: "deepseek-v4.1-flash"}
}

func reviewer() Principal {
	return Principal{Type: "agent", ID: "review-agent", RunID: "review-run", Model: "gpt-5-codex"}
}

func human() Principal {
	return Principal{Type: "member", ID: "member-1"}
}

func baseInput() Input {
	return Input{
		Now:               now,
		SubjectKind:       SubjectIssue,
		SubjectID:         "issue-1",
		Action:            ActionIssueClose,
		Status:            "in_review",
		Managed:           true,
		Delivery:          delivery,
		CriteriaRevision:  "plan-rev-3",
		LinkedGoalID:      "goal-1",
		Author:            author(),
		Caller:            author(),
		CallerCanClose:    true,
		RequirementsKnown: true,
	}
}

func issueAuthorization(actions ...Action) *Authorization {
	if len(actions) == 0 {
		actions = []Action{ActionIssueClose}
	}
	return &Authorization{
		ID:               "auth-1",
		WorkspaceID:      "ws-1",
		ProjectID:        "project-1",
		SubjectKind:      SubjectGoal,
		SubjectID:        "goal-1",
		Actions:          actions,
		CoversChildren:   true,
		CriteriaRevision: "plan-rev-3",
		State:            "active",
		Revision:         1,
	}
}

func goalAuthorization() *Authorization {
	a := issueAuthorization(ActionGoalAccept)
	a.SubjectKind = SubjectGoal
	a.SubjectID = "goal-1"
	a.CoversChildren = false
	a.CriteriaRevision = "goal-criteria-rev-1"
	return a
}

func passReview(opts ...func(*Review)) Review {
	r := Review{
		ID:               "review-1",
		SubjectKind:      SubjectIssue,
		SubjectID:        "issue-1",
		Action:           ActionIssueClose,
		Delivery:         delivery,
		CriteriaRevision: "plan-rev-3",
		Author:           author(),
		Reviewer:         reviewer(),
		Verdict:          VerdictPass,
		Criteria:         []CriterionResult{{Criterion: "downstream is gated", Result: "pass"}},
		Platforms:        []string{"linux"},
		RecordedAt:       now.Add(-time.Hour),
	}
	for _, opt := range opts {
		opt(&r)
	}
	return r
}

func want(t *testing.T, got Decision, outcome Outcome, reason ReasonCode) {
	t.Helper()
	if got.Outcome != outcome || got.Reason != reason {
		t.Fatalf("decision = (%s, %s, %q), want (%s, %s)", got.Outcome, got.Reason, got.Detail, outcome, reason)
	}
}

// Case 1: no authorization, no independent review. Delivery waits; null/absent
// workflow facts never become a grant.
func TestNoAuthorizationWaitsForReview(t *testing.T) {
	in := baseInput()
	in.Authorization = nil
	want(t, Evaluate(in), WaitReview, ReasonNoAuthorization)

	in.Status = "cancelled"
	want(t, Evaluate(in), Blocked, ReasonSubjectCancelled)
}

// Case 1/6: an issue outside a goal is not silently managed.
func TestUnmanagedSubjectWaitsForHuman(t *testing.T) {
	in := baseInput()
	in.Managed = false
	in.Authorization = issueAuthorization()
	in.Reviews = []Review{passReview()}
	want(t, Evaluate(in), WaitHuman, ReasonNotManaged)
}

// Case 2: author self-test, a successful run exit, in_review and a same-model
// reviewer are not independent acceptance.
func TestAuthorEvidenceIsNotIndependentAcceptance(t *testing.T) {
	// No review at all.
	in := baseInput()
	in.Authorization = issueAuthorization()
	want(t, Evaluate(in), WaitReview, ReasonNoIndependentReview)

	// Same agent reviewing its own work.
	in.Reviews = []Review{passReview(func(r *Review) { r.Reviewer = author() })}
	want(t, Evaluate(in), WaitReview, ReasonReviewNotIndependent)

	// A different agent CLI on the same model family.
	in.Reviews = []Review{passReview(func(r *Review) {
		r.Reviewer = Principal{Type: "agent", ID: "another-agent", RunID: "another-run", Model: "deepseek-v4.1-flash"}
	})}
	want(t, Evaluate(in), WaitReview, ReasonReviewNotIndependent)

	// Same run, different recorded identity.
	in.Reviews = []Review{passReview(func(r *Review) {
		r.Reviewer = Principal{Type: "agent", ID: "other", RunID: author().RunID, Model: "gpt-5-codex"}
	})}
	want(t, Evaluate(in), WaitReview, ReasonReviewNotIndependent)
}

// Case 3: authorization + same-version independent pass + deliverables is the
// only automatic close, and replaying it is idempotent.
func TestAuthorizedIndependentPassCanClose(t *testing.T) {
	in := baseInput()
	in.Authorization = issueAuthorization()
	in.Reviews = []Review{passReview()}
	dec := Evaluate(in)
	want(t, dec, CanClose, "")
	if dec.AuthorizationID != "auth-1" || dec.ReviewID != "review-1" {
		t.Fatalf("decision lost its evidence refs: %+v", dec)
	}

	// Replaying the same completion returns the original result.
	in.Acceptance = &Acceptance{
		ReviewID:        "review-1",
		AuthorizationID: "auth-1",
		Delivery:        delivery,
		AcceptedBy:      author(),
		AcceptedAt:      now.Add(-time.Minute),
	}
	dec = Evaluate(in)
	want(t, dec, CanClose, "")
	if !dec.AlreadyAccepted {
		t.Fatal("replayed acceptance was not reported as already accepted")
	}
}

// Case 4: an independent pass without a matching grant waits for a human. A
// task-level grant never becomes goal acceptance.
func TestPassWithoutAuthorizationWaitsForHuman(t *testing.T) {
	in := baseInput()
	in.Authorization = nil
	in.Reviews = []Review{passReview()}
	want(t, Evaluate(in), WaitHuman, ReasonNoAuthorization)

	// Grant for another project's goal.
	in.Authorization = issueAuthorization()
	in.Authorization.SubjectID = "goal-other"
	want(t, Evaluate(in), WaitHuman, ReasonAuthorizationScope)

	// Grant for issue closure only, used to accept the goal.
	goal := baseInput()
	goal.SubjectKind = SubjectGoal
	goal.SubjectID = "goal-1"
	goal.Action = ActionGoalAccept
	goal.LinkedGoalID = ""
	goal.Authorization = issueAuthorization(ActionIssueClose)
	goal.Reviews = []Review{passReview(func(r *Review) {
		r.SubjectKind = SubjectGoal
		r.SubjectID = "goal-1"
		r.Action = ActionGoalAccept
	})}
	want(t, Evaluate(goal), WaitHuman, ReasonAuthorizationAction)
}

// Case 5: a real human step keeps its terminal position even with a grant.
func TestHumanStepOutranksAutomaticClose(t *testing.T) {
	in := baseInput()
	in.Authorization = issueAuthorization()
	in.Reviews = []Review{passReview()}
	in.Workflow = WorkflowFacts{HumanStepPending: true, HumanActor: "member-1"}
	want(t, Evaluate(in), WaitHuman, ReasonHumanStepPending)
	if got := Evaluate(in).NextActor; got != "member-1" {
		t.Fatalf("next actor = %q, want member-1", got)
	}

	in.Workflow = WorkflowFacts{RequiresHumanReview: true}
	want(t, Evaluate(in), WaitHuman, ReasonHumanStepPending)
}

// Case 6: short flows and agent handlers cannot skip acceptance.
func TestNoEvidenceCannotUseShortFlowToFinish(t *testing.T) {
	in := baseInput()
	in.Status = "todo"
	in.Authorization = issueAuthorization()
	want(t, Evaluate(in), WaitReview, ReasonNoIndependentReview)
}

// Case 7: fail/needs_changes returns work; a review for another version does
// not decide the current delivery.
func TestReviewFailuresReturnWork(t *testing.T) {
	in := baseInput()
	in.Authorization = issueAuthorization()

	in.Reviews = []Review{passReview(func(r *Review) { r.Verdict = VerdictFail })}
	want(t, Evaluate(in), NeedsChanges, ReasonReviewFailed)

	in.Reviews = []Review{passReview(func(r *Review) { r.Verdict = VerdictNeedsChanges })}
	want(t, Evaluate(in), NeedsChanges, ReasonReviewNeedsChanges)

	in.Reviews = []Review{passReview(func(r *Review) {
		r.Delivery = DeliveryVersion{Version: "old-sha", Tree: "old-tree"}
	})}
	want(t, Evaluate(in), WaitReview, ReasonReviewStale)
}

// Case 8: a revoked/expired grant, superseded evidence or a passed criteria
// revision is not an active acceptance.
func TestStaleGrantsAndEvidenceAreRefused(t *testing.T) {
	in := baseInput()
	in.Reviews = []Review{passReview()}

	revoked := issueAuthorization()
	revoked.State = "revoked"
	in.Authorization = revoked
	want(t, Evaluate(in), WaitHuman, ReasonAuthorizationRevoked)

	expired := issueAuthorization()
	expired.ExpiresAt = &now
	in.Authorization = expired
	want(t, Evaluate(in), WaitHuman, ReasonAuthorizationExpired)

	stale := issueAuthorization()
	stale.CriteriaRevision = "plan-rev-2"
	in.Authorization = stale
	want(t, Evaluate(in), WaitHuman, ReasonAuthorizationStale)

	in.Authorization = issueAuthorization()
	in.Reviews = []Review{passReview(func(r *Review) { r.SupersededBy = "review-2" })}
	want(t, Evaluate(in), WaitReview, ReasonNoIndependentReview)

	// A new delivery version invalidates the old acceptance.
	in.Reviews = []Review{passReview()}
	in.Acceptance = &Acceptance{ReviewID: "review-1", AuthorizationID: "auth-1", Delivery: DeliveryVersion{Version: "old-sha"}}
	dec := Evaluate(in)
	want(t, dec, CanClose, "")
	if dec.AlreadyAccepted {
		t.Fatal("an acceptance for an older delivery must not be replayed")
	}
}

// Case 11: goal acceptance needs a goal-level grant and an independent goal
// review; linked issue success alone does not complete the goal.
func TestGoalAcceptanceIsIndependent(t *testing.T) {
	in := baseInput()
	in.SubjectKind = SubjectGoal
	in.SubjectID = "goal-1"
	in.Action = ActionGoalAccept
	in.CriteriaRevision = "goal-criteria-rev-1"
	in.LinkedGoalID = ""
	in.Authorization = goalAuthorization()
	in.Reviews = []Review{passReview(func(r *Review) {
		r.SubjectKind = SubjectGoal
		r.SubjectID = "goal-1"
		r.Action = ActionGoalAccept
		r.CriteriaRevision = "goal-criteria-rev-1"
	})}
	want(t, Evaluate(in), CanClose, "")

	in.Authorization = nil
	want(t, Evaluate(in), WaitHuman, ReasonNoAuthorization)
}

// Case 12: technical pass with a missing required delivery condition is not a
// success.
func TestMissingDeliverableBlocksClosure(t *testing.T) {
	in := baseInput()
	in.Authorization = issueAuthorization()
	in.Reviews = []Review{passReview()}
	in.Required = []RequiredDeliverable{{Kind: "pull_request", Ref: "PR #1", Satisfied: false}}
	dec := Evaluate(in)
	want(t, dec, Blocked, ReasonMissingDeliverable)
	if dec.Detail == "" {
		t.Fatal("blocked decision must name the missing condition")
	}

	in.Required[0].Satisfied = true
	want(t, Evaluate(in), CanClose, "")
}

// A caller that could not read the delivery conditions must not fall through
// to the "no required deliverables" branch: unknown is not empty.
func TestUnknownDeliverablesBlockClosure(t *testing.T) {
	in := baseInput()
	in.Authorization = issueAuthorization()
	in.Reviews = []Review{passReview()}
	in.RequirementsKnown = false

	dec := Evaluate(in)
	want(t, dec, Blocked, ReasonDeliverablesUnknown)
	if dec.Detail == "" {
		t.Fatal("blocked decision must say the conditions could not be read")
	}

	// The same input with a readable, empty list is a legitimate "nothing
	// extra required" answer.
	in.RequirementsKnown = true
	want(t, Evaluate(in), CanClose, "")
}

// A pending human step still reports the human endpoint even when the
// delivery-condition source is unreadable; neither path may auto-close.
func TestUnknownDeliverablesNeverOverrideAHumanStep(t *testing.T) {
	in := baseInput()
	in.Authorization = issueAuthorization()
	in.Reviews = []Review{passReview()}
	in.RequirementsKnown = false
	in.Workflow.HumanStepPending = true
	in.Workflow.HumanActor = "member-1"
	want(t, Evaluate(in), WaitHuman, ReasonHumanStepPending)
}

// Case 13: an observer or any caller without closer rights cannot run the
// completion action.
func TestCallerMustBeTheCloser(t *testing.T) {
	in := baseInput()
	in.Authorization = issueAuthorization()
	in.Reviews = []Review{passReview()}
	in.Caller = Principal{Type: "agent", ID: "observer", Model: "gpt-5-codex"}
	in.CallerCanClose = false
	want(t, Evaluate(in), WaitHuman, ReasonCallerNotCloser)
}

// A delivery without a fixed version is never acceptable.
func TestDeliveryVersionIsRequired(t *testing.T) {
	in := baseInput()
	in.Authorization = issueAuthorization()
	in.Reviews = []Review{passReview()}
	in.Delivery = DeliveryVersion{}
	want(t, Evaluate(in), Blocked, ReasonMissingDeliveryVersion)
}

// A member reviewer is independent when it is not the author; model family is
// only comparable between agents.
func TestMemberReviewerIndependence(t *testing.T) {
	in := baseInput()
	in.Authorization = issueAuthorization()
	in.Reviews = []Review{passReview(func(r *Review) {
		r.Reviewer = human()
	})}
	want(t, Evaluate(in), CanClose, "")

	in.Reviews = []Review{passReview(func(r *Review) { r.Reviewer = in.Author })}
	want(t, Evaluate(in), WaitReview, ReasonReviewNotIndependent)
}

// A direct issue grant is usable without inheriting through the goal.
func TestDirectIssueGrant(t *testing.T) {
	in := baseInput()
	in.LinkedGoalID = ""
	auth := issueAuthorization()
	auth.SubjectKind = SubjectIssue
	auth.SubjectID = "issue-1"
	auth.CoversChildren = false
	in.Authorization = auth
	in.Reviews = []Review{passReview()}
	want(t, Evaluate(in), CanClose, "")
}

// A goal grant without CoversChildren does not cover a linked issue.
func TestGoalGrantDoesNotCoverChildrenUnlessDeclared(t *testing.T) {
	in := baseInput()
	auth := issueAuthorization()
	auth.CoversChildren = false
	in.Authorization = auth
	in.Reviews = []Review{passReview()}
	want(t, Evaluate(in), WaitHuman, ReasonAuthorizationScope)
}

func TestValidateAuthorization(t *testing.T) {
	valid := Authorization{
		WorkspaceID: "ws-1",
		SubjectKind: SubjectGoal,
		SubjectID:   "goal-1",
		Actions:     []Action{ActionIssueClose, ActionGoalAccept},
	}
	if err := ValidateAuthorization(valid); err != nil {
		t.Fatalf("valid authorization rejected: %v", err)
	}
	noActions := valid
	noActions.Actions = nil
	if err := ValidateAuthorization(noActions); err == nil {
		t.Fatal("authorization without actions was accepted")
	}
	badKind := valid
	badKind.SubjectKind = "project"
	if err := ValidateAuthorization(badKind); err == nil {
		t.Fatal("authorization with unknown subject kind was accepted")
	}
}
