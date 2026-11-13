// Package issueclosure owns the DEV-7 closure contract: when a delivered
// subject (an issue under a goal, or the goal itself) may be closed by an agent
// instead of waiting for a human.
//
// The contract has three parts and they must never be conflated:
//   - a persisted, scoped authorization from the user or a coordinator acting
//     on the user's explicit decision;
//   - an independent acceptance record (a pass verdict from a reviewer that is
//     not the author, produced against the same delivery version);
//   - every required deliverable being satisfied and no human review step left
//     open.
//
// Evaluate is a pure function so the same verdict drives the runtime brief,
// the status-write gate and the eventual acceptance write. A non-success
// (pending review, failed run, cancelled issue) never counts as success and
// never releases a downstream dependency.
package issueclosure

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Action is the terminal move an authorization can permit. Merge, deploy and
// release are deliberately absent: they are separate actions and never implied
// by a closure authorization.
type Action string

const (
	ActionIssueClose Action = "issue_close"
	ActionGoalAccept Action = "goal_accept"
)

// SubjectKind distinguishes the two objects the contract covers.
type SubjectKind string

const (
	SubjectIssue SubjectKind = "issue"
	SubjectGoal  SubjectKind = "goal"
)

// Outcome is the execution decision. It is not an issue status: the issue
// statuses stay the workflow's own keys.
type Outcome string

const (
	// CanClose: authorization, independent pass, deliverables and the human
	// boundary all allow the closer to perform the completion action now.
	CanClose Outcome = "can_close"
	// WaitReview: delivery is not independently accepted yet.
	WaitReview Outcome = "wait_review"
	// WaitHuman: the technical evidence may be fine, but a human or human
	// step still owns the decision.
	WaitHuman Outcome = "wait_human"
	// NeedsChanges: an independent review returned fail or needs_changes.
	NeedsChanges Outcome = "needs_changes"
	// Blocked: something outside the closure decision prevents progress
	// (cancelled subject, missing deliverable, missing delivery version).
	Blocked Outcome = "blocked"
)

// ReasonCode is a stable, machine-readable reason for the outcome. Clients
// mirror it; free text stays in Detail.
type ReasonCode string

const (
	ReasonNotManaged             ReasonCode = "not_managed"
	ReasonSubjectCancelled       ReasonCode = "subject_cancelled"
	ReasonMissingDeliveryVersion ReasonCode = "missing_delivery_version"
	ReasonHumanStepPending       ReasonCode = "human_step_pending"
	ReasonNoAuthorization        ReasonCode = "no_authorization"
	ReasonAuthorizationRevoked   ReasonCode = "authorization_revoked"
	ReasonAuthorizationExpired   ReasonCode = "authorization_expired"
	ReasonAuthorizationScope     ReasonCode = "authorization_out_of_scope"
	ReasonAuthorizationAction    ReasonCode = "authorization_missing_action"
	ReasonAuthorizationStale     ReasonCode = "authorization_stale_criteria"
	ReasonNoIndependentReview    ReasonCode = "no_independent_review"
	ReasonReviewNotIndependent   ReasonCode = "review_not_independent"
	ReasonReviewStale            ReasonCode = "review_stale_version"
	ReasonReviewSuperseded       ReasonCode = "review_superseded"
	ReasonReviewFailed           ReasonCode = "review_failed"
	ReasonReviewNeedsChanges     ReasonCode = "review_needs_changes"
	ReasonMissingDeliverable     ReasonCode = "missing_deliverable"
	ReasonCallerNotCloser        ReasonCode = "caller_not_closer"
)

// ReviewVerdict is the reviewer's conclusion. An empty verdict is a request
// that has not produced an accepted conclusion yet.
type ReviewVerdict string

const (
	VerdictPending      ReviewVerdict = "pending"
	VerdictPass         ReviewVerdict = "pass"
	VerdictFail         ReviewVerdict = "fail"
	VerdictNeedsChanges ReviewVerdict = "needs_changes"
)

// DeliveryVersion pins the exact delivery a decision is about. For code both
// the commit and its tree are recorded; a new SHA requires the review to name
// the new SHA (a matching tree alone is not enough).
type DeliveryVersion struct {
	// Version is a full commit SHA for code, or a fixed artifact version.
	Version string
	// Tree is the git tree hash for code deliveries; empty otherwise.
	Tree string
}

// Valid reports whether the version names something reviewable.
func (d DeliveryVersion) Valid() bool {
	return strings.TrimSpace(d.Version) != ""
}

// Key is the identity used to match reviews and persisted acceptances.
func (d DeliveryVersion) Key() string {
	return strings.TrimSpace(d.Version) + "+" + strings.TrimSpace(d.Tree)
}

// Principal identifies an actor and, for agent runs, the run and model family
// that produced the work. Identity and run source are always server-derived.
type Principal struct {
	Type  string // member | agent | squad
	ID    string
	RunID string
	// Model is the model family recorded by the server (for agents). Two
	// agents of the same model family are the same reviewer even when they use
	// a different CLI or role name.
	Model string
}

// Valid reports whether the principal can own a decision.
func (p Principal) Valid() bool {
	return p.Type != "" && p.ID != ""
}

// Authorization is the persisted, read-back fact that a scoped actor may
// perform Actions over Scope. It is never inferred from workflow_id=null, a
// project match, a parent relationship or a role name.
type Authorization struct {
	ID             string
	WorkspaceID    string
	ProjectID      string
	SubjectKind    SubjectKind
	SubjectID      string
	Actions        []Action
	CoversChildren bool
	// CriteriaRevision pins the acceptance criteria/plan revision the grant
	// was made against. Empty means the grant did not pin a revision.
	CriteriaRevision string
	State            string // active | revoked
	Revision         int
	ExpiresAt        *time.Time
}

// Allows reports whether this authorization covers the input's action and
// subject. It returns the reason code and a short detail when it does not.
func (a *Authorization) Allows(in Input) (bool, ReasonCode, string) {
	if a == nil {
		return false, ReasonNoAuthorization, "no authorization covers this subject"
	}
	switch a.State {
	case "active":
	case "revoked":
		return false, ReasonAuthorizationRevoked, "the authorization was revoked"
	default:
		return false, ReasonAuthorizationRevoked, "the authorization is not active"
	}
	if a.ExpiresAt != nil && !in.Now.Before(*a.ExpiresAt) {
		return false, ReasonAuthorizationExpired, "the authorization expired at " + a.ExpiresAt.UTC().Format(time.RFC3339)
	}
	if !containsAction(a.Actions, in.Action) {
		return false, ReasonAuthorizationAction, "the authorization does not grant " + string(in.Action)
	}
	if a.CriteriaRevision != "" && in.CriteriaRevision != "" && a.CriteriaRevision != in.CriteriaRevision {
		return false, ReasonAuthorizationStale, "the authorization matches an earlier acceptance scope"
	}
	if a.SubjectKind == in.SubjectKind && a.SubjectID == in.SubjectID {
		return true, "", ""
	}
	// A goal-scoped grant may cover the issues explicitly linked into that
	// goal when it says so. Nothing else inherits: not the same project, not a
	// parent relation, not a workflow default.
	if in.SubjectKind == SubjectIssue && a.SubjectKind == SubjectGoal &&
		a.CoversChildren && in.LinkedGoalID != "" && in.LinkedGoalID == a.SubjectID {
		return true, "", ""
	}
	return false, ReasonAuthorizationScope, "the authorization covers a different subject"
}

func containsAction(actions []Action, want Action) bool {
	for _, a := range actions {
		if a == want {
			return true
		}
	}
	return false
}

// CriterionResult is one acceptance criterion and the evidence that settled it.
type CriterionResult struct {
	Criterion string `json:"criterion"`
	Result    string `json:"result"` // pass | fail
	Evidence  string `json:"evidence,omitempty"`
}

// Review is one independent verdict about one delivery version.
type Review struct {
	ID          string
	SubjectKind SubjectKind
	SubjectID   string
	Action      Action
	Delivery    DeliveryVersion
	// CriteriaRevision is the acceptance scope the reviewer verified.
	CriteriaRevision string
	Author           Principal
	Reviewer         Principal
	Verdict          ReviewVerdict
	Criteria         []CriterionResult
	Platforms        []string
	// Missing lists required checks the reviewer could not perform.
	Missing    []string
	Reason     string
	RecordedAt time.Time
	// SupersededBy is set when a newer delivery or scope made this verdict
	// irrelevant. A superseded review never satisfies the contract.
	SupersededBy string
}

// Acceptance is the persisted completion action. Downstream work is released
// only when this record exists for the current delivery.
type Acceptance struct {
	ReviewID        string
	AuthorizationID string
	Delivery        DeliveryVersion
	AcceptedBy      Principal
	AcceptedAt      time.Time
}

// WorkflowFacts are the parts of the resolved workflow that bound the
// decision. A human step is not "none" and cannot be guessed from a column
// name.
type WorkflowFacts struct {
	// HumanStepPending is true when the current or a remaining step hands off
	// to a member (including a project lead/creator that resolves to one) and
	// no human decision has been recorded yet.
	HumanStepPending bool
	// HumanActor names who owns the pending human decision.
	HumanActor string
	// RequiresHumanReview is true when the flow explicitly requires a human
	// review even without a handler.
	RequiresHumanReview bool
}

// RequiredDeliverable is one condition that must be satisfied before the
// subject can be treated as successfully delivered (for example a pull
// request, an artifact or an environment verification).
type RequiredDeliverable struct {
	Kind      string
	Ref       string
	Satisfied bool
}

// Input is everything Evaluate may consider. Any fact not present here is not
// evidence: in particular, an author's successful exit, self-test, in_review
// status or a merged PR is never represented as an independent review.
type Input struct {
	Now              time.Time
	SubjectKind      SubjectKind
	SubjectID        string
	Action           Action
	Status           string
	Managed          bool
	Delivery         DeliveryVersion
	CriteriaRevision string
	LinkedGoalID     string
	Author           Principal
	Caller           Principal
	// CallerCanClose is the workflow/permission check for the acting closer.
	CallerCanClose bool
	Workflow       WorkflowFacts
	Authorization  *Authorization
	Acceptance     *Acceptance
	Reviews        []Review
	Required       []RequiredDeliverable
}

// Decision is the single verdict consumed by the prompt, the status-write gate
// and the acceptance write. CanClose is a preview until the acceptance record
// is persisted; callers must not release downstream work on the preview.
type Decision struct {
	Outcome         Outcome
	Reason          ReasonCode
	Detail          string
	AlreadyAccepted bool
	AuthorizationID string
	ReviewID        string
	Delivery        DeliveryVersion
	Closer          Principal
	// NextActor names who must act next when the outcome is not CanClose.
	NextActor string
}

// Evaluate applies the closure contract in the one order the design fixes:
// scope/permission, workflow human boundary, persisted authorization, then
// independent evidence.
func Evaluate(in Input) Decision {
	if in.Now.IsZero() {
		in.Now = time.Now()
	}
	dec := Decision{Outcome: WaitReview, Delivery: in.Delivery, Closer: in.Caller}

	// A cancelled subject is never a success and never restarts.
	if in.Status == "cancelled" {
		dec.Outcome = Blocked
		dec.Reason = ReasonSubjectCancelled
		dec.Detail = "the subject is cancelled; cancellation is not success"
		return dec
	}
	if !in.Managed {
		dec.Outcome = WaitHuman
		dec.Reason = ReasonNotManaged
		dec.Detail = "the subject is not under a closure contract; an authorized decision is required"
		return dec
	}
	// Idempotent replay: the completion action already happened for exactly
	// this delivery.
	if in.Acceptance != nil && in.Acceptance.Delivery.Key() == in.Delivery.Key() && in.Delivery.Valid() {
		dec.Outcome = CanClose
		dec.AlreadyAccepted = true
		dec.AuthorizationID = in.Acceptance.AuthorizationID
		dec.ReviewID = in.Acceptance.ReviewID
		dec.Closer = in.Acceptance.AcceptedBy
		return dec
	}
	if !in.Delivery.Valid() {
		dec.Outcome = Blocked
		dec.Reason = ReasonMissingDeliveryVersion
		dec.Detail = "the delivery has no fixed version to accept"
		return dec
	}
	// A real human step outranks an automatic closure grant.
	if in.Workflow.HumanStepPending || in.Workflow.RequiresHumanReview {
		dec.Outcome = WaitHuman
		dec.Reason = ReasonHumanStepPending
		dec.Detail = "a human review step is still open"
		dec.NextActor = in.Workflow.HumanActor
		return dec
	}

	pass, passReason, passDetail := validIndependentPass(in)
	authorized, authReason, authDetail := in.Authorization.Allows(in)
	if authorized {
		dec.AuthorizationID = in.Authorization.ID
	}

	// Independently rejected work goes back for rework regardless of grants.
	if pass.Verdict == VerdictFail || pass.Verdict == VerdictNeedsChanges {
		dec.Outcome = NeedsChanges
		dec.ReviewID = pass.ID
		if pass.Verdict == VerdictFail {
			dec.Reason = ReasonReviewFailed
		} else {
			dec.Reason = ReasonReviewNeedsChanges
		}
		dec.Detail = firstNonEmpty(pass.Reason, "the independent review did not pass")
		return dec
	}

	if !authorized {
		// The design fixes the order: authorization before evidence. Report the
		// missing grant first and keep the evidence state in the detail.
		dec.Reason = authReason
		dec.Detail = authDetail
		if pass.Verdict == VerdictPass && pass.ID != "" && passReason == "" {
			dec.Outcome = WaitHuman
			dec.Detail = authDetail + "; technical acceptance is complete, a human must accept"
			dec.ReviewID = pass.ID
			return dec
		}
		dec.Outcome = WaitReview
		if passReason != "" {
			dec.Detail = authDetail + "; " + passDetail
		}
		return dec
	}

	if passReason != "" || pass.Verdict != VerdictPass || pass.ID == "" {
		dec.Outcome = WaitReview
		dec.Reason = passReason
		dec.Detail = passDetail
		return dec
	}
	dec.ReviewID = pass.ID

	for _, d := range in.Required {
		if !d.Satisfied {
			dec.Outcome = Blocked
			dec.Reason = ReasonMissingDeliverable
			dec.Detail = "required delivery condition is not satisfied: " + strings.TrimSpace(d.Kind+" "+d.Ref)
			return dec
		}
	}
	if !in.CallerCanClose || !in.Caller.Valid() {
		dec.Outcome = WaitHuman
		dec.Reason = ReasonCallerNotCloser
		dec.Detail = "the caller is not the authorized closer"
		return dec
	}
	dec.Outcome = CanClose
	dec.Reason = ""
	dec.Detail = "authorization, independent acceptance and delivery conditions are satisfied"
	return dec
}

// validIndependentPass returns the review that decides the delivery. It prefers
// the latest independent review for the exact delivery and, when there is none,
// returns the latest review so the caller can report why it does not count.
func validIndependentPass(in Input) (Review, ReasonCode, string) {
	var independent, any []Review
	for _, r := range in.Reviews {
		if r.SupersededBy != "" {
			continue
		}
		if r.SubjectKind != in.SubjectKind || r.SubjectID != in.SubjectID || r.Action != in.Action {
			continue
		}
		any = append(any, r)
		if r.Delivery.Key() != in.Delivery.Key() {
			continue
		}
		if r.CriteriaRevision != "" && in.CriteriaRevision != "" && r.CriteriaRevision != in.CriteriaRevision {
			continue
		}
		if IsIndependent(r.Author, r.Reviewer) {
			independent = append(independent, r)
		}
	}
	sortNewestFirst(independent)
	sortNewestFirst(any)
	if len(independent) > 0 {
		return independent[0], "", ""
	}
	if len(any) > 0 {
		latest := any[0]
		if latest.Delivery.Key() != in.Delivery.Key() {
			return latest, ReasonReviewStale, "the review targets a different delivery version"
		}
		if latest.Verdict == VerdictPass {
			return latest, ReasonReviewNotIndependent, "the only passing review is not independent of the author"
		}
		return latest, ReasonNoIndependentReview, "no independent review accepted this delivery"
	}
	return Review{}, ReasonNoIndependentReview, "no independent review accepted this delivery"
}

// IsIndependent reports whether reviewer can count as the author's independent
// reviewer. Same identity, same run or the same model family in a different CLI
// is not independent.
func IsIndependent(author, reviewer Principal) bool {
	if !author.Valid() || !reviewer.Valid() {
		return false
	}
	if author.ID == reviewer.ID {
		return false
	}
	if author.RunID != "" && reviewer.RunID != "" && author.RunID == reviewer.RunID {
		return false
	}
	if author.Type == "agent" && reviewer.Type == "agent" {
		if author.Model == "" || reviewer.Model == "" || author.Model == reviewer.Model {
			return false
		}
	}
	return true
}

func sortNewestFirst(reviews []Review) {
	sort.SliceStable(reviews, func(i, j int) bool {
		if reviews[i].RecordedAt.Equal(reviews[j].RecordedAt) {
			return reviews[i].ID > reviews[j].ID
		}
		return reviews[i].RecordedAt.After(reviews[j].RecordedAt)
	})
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// ValidateAuthorization checks the persisted shape before a grant is stored.
// It deliberately rejects an empty subject or action list instead of treating
// "no constraint" as "everything".
func ValidateAuthorization(a Authorization) error {
	if a.WorkspaceID == "" {
		return fmt.Errorf("workspace_id is required")
	}
	if a.SubjectKind != SubjectIssue && a.SubjectKind != SubjectGoal {
		return fmt.Errorf("subject_kind must be issue or goal")
	}
	if a.SubjectID == "" {
		return fmt.Errorf("subject_id is required")
	}
	if len(a.Actions) == 0 {
		return fmt.Errorf("actions must not be empty")
	}
	for _, action := range a.Actions {
		switch action {
		case ActionIssueClose, ActionGoalAccept:
		default:
			return fmt.Errorf("unknown action %q", action)
		}
	}
	if a.SubjectKind == SubjectGoal && containsAction(a.Actions, ActionGoalAccept) && !containsAction(a.Actions, ActionIssueClose) {
		// Valid: a goal may be authorized for acceptance without granting
		// issue closure, but not the other way around (task authorization does
		// not propagate to goal acceptance).
	}
	return nil
}
