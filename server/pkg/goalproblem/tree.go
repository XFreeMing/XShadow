package goalproblem

import "strings"

// BranchStatus tracks the operational status of a logic tree branch.
type BranchStatus string

const (
	BranchStatusActive            BranchStatus = "active"
	BranchStatusDeferred          BranchStatus = "deferred"
	BranchStatusNeedsReassessment BranchStatus = "needs_reassessment"
	BranchStatusRetired           BranchStatus = "retired"
)

// ReassessmentOutcome captures how a refuted branch was reassessed.
type ReassessmentOutcome string

const (
	ReassessmentReviseScope       ReassessmentOutcome = "revise_scope"
	ReassessmentReplaceHypothesis ReassessmentOutcome = "replace_hypothesis"
	ReassessmentDeferBranch       ReassessmentOutcome = "defer_branch"
	ReassessmentRetireBranch      ReassessmentOutcome = "retire_branch"
)

// BranchRevision is an immutable snapshot of a branch's scope and boundaries.
type BranchRevision struct {
	Revision     int         `json:"revision"`
	ProblemRef   RevisionRef `json:"problem_ref"`
	Title        string      `json:"title"`
	Scope        string      `json:"scope"`
	Boundary     string      `json:"boundary"`
	Rationale    string      `json:"rationale"`
	AuthoredBy   string      `json:"authored_by"`
	ChangeReason string      `json:"change_reason,omitempty"`
}

// BranchReassessment records a refutation challenge and its resolution on a branch.
type BranchReassessment struct {
	ChallengedByResultID string              `json:"challenged_by_result_id"`
	HypothesisRef        RevisionRef         `json:"hypothesis_ref"`
	ChallengeReason      string              `json:"challenge_reason"`
	Resolved             bool                `json:"resolved"`
	Outcome              ReassessmentOutcome `json:"outcome,omitempty"`
	ResolutionRationale  string              `json:"resolution_rationale,omitempty"`
	ReopeningCondition   string              `json:"reopening_condition,omitempty"`
	ResolvedBy           string              `json:"resolved_by,omitempty"`
	ResolvedAtRevision   int                 `json:"resolved_at_revision,omitempty"`
}

// Branch is a scoped node in the problem-solving logic tree.
type Branch struct {
	ID              string               `json:"id"`
	ParentBranchID  string               `json:"parent_branch_id,omitempty"`
	CurrentRevision int                  `json:"current_revision"`
	Status          BranchStatus         `json:"status"`
	Revisions       []BranchRevision     `json:"revisions"`
	Reassessments   []BranchReassessment `json:"reassessments,omitempty"`
}

func (b *Branch) currentRev() *BranchRevision {
	if len(b.Revisions) == 0 {
		return nil
	}
	return &b.Revisions[len(b.Revisions)-1]
}

func (b *Branch) hasUnresolvedReassessment() bool {
	for _, r := range b.Reassessments {
		if !r.Resolved {
			return true
		}
	}
	return false
}

// HypothesisRevision is an immutable snapshot of a falsifiable hypothesis.
type HypothesisRevision struct {
	Revision               int         `json:"revision"`
	BranchRef              RevisionRef `json:"branch_ref"`
	Statement              string      `json:"statement"`
	FalsificationCriterion string      `json:"falsification_criterion"`
	Assumptions            []string    `json:"assumptions,omitempty"`
	AuthoredBy             string      `json:"authored_by"`
	ChangeReason           string      `json:"change_reason,omitempty"`
}

// Hypothesis is a falsifiable hypothesis attached to a logic tree branch.
type Hypothesis struct {
	ID              string               `json:"id"`
	BranchID        string               `json:"branch_id"`
	CurrentRevision int                  `json:"current_revision"`
	Revisions       []HypothesisRevision `json:"revisions"`
}

// CreateBranchInput creates a new logic tree branch (revision 1).
type CreateBranchInput struct {
	BranchID       string      `json:"branch_id"`
	ProblemRef     RevisionRef `json:"problem_ref"`
	ParentBranchID string      `json:"parent_branch_id,omitempty"`
	Title          string      `json:"title"`
	Scope          string      `json:"scope"`
	Boundary       string      `json:"boundary"`
	Rationale      string      `json:"rationale"`
}

// ReviseBranchInput creates a subsequent immutable revision of an existing branch.
type ReviseBranchInput struct {
	BranchID     string      `json:"branch_id"`
	ProblemRef   RevisionRef `json:"problem_ref"`
	Title        string      `json:"title"`
	Scope        string      `json:"scope"`
	Boundary     string      `json:"boundary"`
	Rationale    string      `json:"rationale"`
	ChangeReason string      `json:"change_reason"`
}

// ReassessBranchInput resolves a pending refutation reassessment on a branch.
type ReassessBranchInput struct {
	BranchRef            RevisionRef         `json:"branch_ref"`
	ChallengedByResultID string              `json:"challenged_by_result_id"`
	Outcome              ReassessmentOutcome `json:"outcome"`
	Rationale            string              `json:"rationale"`
	NewTitle             string              `json:"new_title,omitempty"`
	NewScope             string              `json:"new_scope,omitempty"`
	NewBoundary          string              `json:"new_boundary,omitempty"`
	ReopeningCondition   string              `json:"reopening_condition,omitempty"`
}

// CreateHypothesisInput creates a falsifiable hypothesis (revision 1) on a branch.
type CreateHypothesisInput struct {
	HypothesisID           string      `json:"hypothesis_id"`
	BranchRef              RevisionRef `json:"branch_ref"`
	Statement              string      `json:"statement"`
	FalsificationCriterion string      `json:"falsification_criterion"`
	Assumptions            []string    `json:"assumptions,omitempty"`
}

// ReviseHypothesisInput creates a subsequent immutable revision of a hypothesis.
type ReviseHypothesisInput struct {
	HypothesisID           string      `json:"hypothesis_id"`
	BranchRef              RevisionRef `json:"branch_ref"`
	Statement              string      `json:"statement"`
	FalsificationCriterion string      `json:"falsification_criterion"`
	Assumptions            []string    `json:"assumptions,omitempty"`
	ChangeReason           string      `json:"change_reason"`
}

// CreateBranch splits a confirmed problem into a scoped logic tree branch.
func (s *State) CreateBranch(in CreateBranchInput, actor Actor) error {
	return s.transact(actor, func(next *State, actorID string) error {
		return next.createBranch(in, actorID)
	})
}

func (s *State) createBranch(in CreateBranchInput, actorID string) error {
	id, err := requireNonEmpty("branch_id", in.BranchID)
	if err != nil {
		return err
	}
	if s.findBranch(id) != nil {
		return errConflict(ErrDuplicateID, "branch %q already exists", id)
	}
	probRef, err := s.requireConfirmedProblemRef(in.ProblemRef)
	if err != nil {
		return err
	}
	parentID := strings.TrimSpace(in.ParentBranchID)
	if parentID != "" {
		if parentID == id {
			return errInvalid("parent_branch_id cannot reference branch itself")
		}
		parent := s.findBranch(parentID)
		if parent == nil {
			return errNotFound("parent branch %q not found", parentID)
		}
		if parent.Status == BranchStatusRetired {
			return errConflict(ErrInvalidTransition, "parent branch %q is retired", parentID)
		}
	}
	rev, err := buildBranchRevision(1, probRef, in.Title, in.Scope, in.Boundary, in.Rationale, actorID, "")
	if err != nil {
		return err
	}
	s.Branches = append(s.Branches, Branch{
		ID:              id,
		ParentBranchID:  parentID,
		CurrentRevision: 1,
		Status:          BranchStatusActive,
		Revisions:       []BranchRevision{rev},
	})
	return nil
}

// ReviseBranch appends a new immutable revision while preserving branch identity.
func (s *State) ReviseBranch(in ReviseBranchInput, actor Actor) error {
	return s.transact(actor, func(next *State, actorID string) error {
		return next.reviseBranch(in, actorID)
	})
}

func (s *State) reviseBranch(in ReviseBranchInput, actorID string) error {
	id, err := requireNonEmpty("branch_id", in.BranchID)
	if err != nil {
		return err
	}
	reason, err := requireNonEmpty("change_reason", in.ChangeReason)
	if err != nil {
		return err
	}
	br := s.findBranch(id)
	if br == nil {
		return errNotFound("branch %q not found", id)
	}
	if br.Status == BranchStatusRetired {
		return errConflict(ErrInvalidTransition, "retired branch %q cannot be revised", id)
	}
	probRef, err := s.requireConfirmedProblemRef(in.ProblemRef)
	if err != nil {
		return err
	}
	nextRevNum := br.CurrentRevision + 1
	rev, err := buildBranchRevision(nextRevNum, probRef, in.Title, in.Scope, in.Boundary, in.Rationale, actorID, reason)
	if err != nil {
		return err
	}
	br.Revisions = append(br.Revisions, rev)
	br.CurrentRevision = nextRevNum
	return nil
}

// ReassessBranch resolves a pending refutation reassessment on the targeted branch.
func (s *State) ReassessBranch(in ReassessBranchInput, actor Actor) error {
	return s.transact(actor, func(next *State, actorID string) error {
		return next.reassessBranch(in, actorID)
	})
}

func (s *State) reassessBranch(in ReassessBranchInput, actorID string) error {
	brRef, err := validateRevisionRef("branch_ref", in.BranchRef)
	if err != nil {
		return err
	}
	resultID, err := requireNonEmpty("challenged_by_result_id", in.ChallengedByResultID)
	if err != nil {
		return err
	}
	rationale, err := requireNonEmpty("rationale", in.Rationale)
	if err != nil {
		return err
	}
	br := s.findBranch(brRef.ID)
	if br == nil {
		return errNotFound("branch %q not found", brRef.ID)
	}
	if br.CurrentRevision != brRef.Revision {
		return errConflict(ErrStaleRevision, "branch %q current revision is %d, got %d", brRef.ID, br.CurrentRevision, brRef.Revision)
	}
	if br.Status != BranchStatusNeedsReassessment {
		return errConflict(ErrInvalidTransition, "branch %q does not require reassessment (status=%s)", brRef.ID, br.Status)
	}

	reassessIdx := -1
	for i := range br.Reassessments {
		if br.Reassessments[i].ChallengedByResultID == resultID && !br.Reassessments[i].Resolved {
			reassessIdx = i
			break
		}
	}
	if reassessIdx == -1 {
		if s.findValidationResult(resultID) == nil {
			return errNotFound("validation result %q not found", resultID)
		}
		return errConflict(ErrInvalidTransition, "branch %q has no unresolved reassessment for result %q", brRef.ID, resultID)
	}

	resolvedAtRev := br.CurrentRevision
	var reopeningCondition string
	switch in.Outcome {
	case ReassessmentReviseScope:
		title, err := requireNonEmpty("new_title", in.NewTitle)
		if err != nil {
			return err
		}
		scope, err := requireNonEmpty("new_scope", in.NewScope)
		if err != nil {
			return err
		}
		boundary, err := requireNonEmpty("new_boundary", in.NewBoundary)
		if err != nil {
			return err
		}
		prev := br.currentRev()
		nextRevNum := br.CurrentRevision + 1
		br.Revisions = append(br.Revisions, BranchRevision{
			Revision:     nextRevNum,
			ProblemRef:   prev.ProblemRef,
			Title:        title,
			Scope:        scope,
			Boundary:     boundary,
			Rationale:    rationale,
			AuthoredBy:   actorID,
			ChangeReason: "Reassessment after refutation " + resultID,
		})
		br.CurrentRevision = nextRevNum
		resolvedAtRev = nextRevNum
	case ReassessmentReplaceHypothesis:
		// Branch scope remains intact; a replacement hypothesis may now be authored.
	case ReassessmentDeferBranch:
		if reopeningCondition, err = requireNonEmpty("reopening_condition", in.ReopeningCondition); err != nil {
			return err
		}
	case ReassessmentRetireBranch:
		// Branch is retired from active investigation.
	default:
		return errInvalid("outcome must be one of revise_scope, replace_hypothesis, defer_branch, retire_branch")
	}

	br.Reassessments[reassessIdx].Resolved = true
	br.Reassessments[reassessIdx].Outcome = in.Outcome
	br.Reassessments[reassessIdx].ResolutionRationale = rationale
	br.Reassessments[reassessIdx].ReopeningCondition = reopeningCondition
	br.Reassessments[reassessIdx].ResolvedBy = actorID
	br.Reassessments[reassessIdx].ResolvedAtRevision = resolvedAtRev

	switch in.Outcome {
	case ReassessmentRetireBranch:
		br.Status = BranchStatusRetired
	case ReassessmentDeferBranch:
		if !br.hasUnresolvedReassessment() {
			br.Status = BranchStatusDeferred
		}
	default:
		if !br.hasUnresolvedReassessment() {
			br.Status = BranchStatusActive
		}
	}
	return nil
}

// CreateHypothesis attaches a new falsifiable hypothesis (revision 1) to a branch.
func (s *State) CreateHypothesis(in CreateHypothesisInput, actor Actor) error {
	return s.transact(actor, func(next *State, actorID string) error {
		return next.createHypothesis(in, actorID)
	})
}

func (s *State) createHypothesis(in CreateHypothesisInput, actorID string) error {
	id, err := requireNonEmpty("hypothesis_id", in.HypothesisID)
	if err != nil {
		return err
	}
	if s.findHypothesis(id) != nil {
		return errConflict(ErrDuplicateID, "hypothesis %q already exists", id)
	}
	brRef, err := validateRevisionRef("branch_ref", in.BranchRef)
	if err != nil {
		return err
	}
	br := s.findBranch(brRef.ID)
	if br == nil {
		return errNotFound("branch %q not found", brRef.ID)
	}
	if br.CurrentRevision != brRef.Revision {
		return errConflict(ErrStaleRevision, "branch %q current revision is %d, got %d", brRef.ID, br.CurrentRevision, brRef.Revision)
	}
	if br.Status == BranchStatusRetired {
		return errConflict(ErrInvalidTransition, "cannot add hypothesis to retired branch %q", brRef.ID)
	}
	rev, err := buildHypothesisRevision(1, brRef, in.Statement, in.FalsificationCriterion, in.Assumptions, actorID, "")
	if err != nil {
		return err
	}
	s.Hypotheses = append(s.Hypotheses, Hypothesis{
		ID:              id,
		BranchID:        br.ID,
		CurrentRevision: 1,
		Revisions:       []HypothesisRevision{rev},
	})
	return nil
}

// ReviseHypothesis appends a new immutable revision of an existing hypothesis.
func (s *State) ReviseHypothesis(in ReviseHypothesisInput, actor Actor) error {
	return s.transact(actor, func(next *State, actorID string) error {
		return next.reviseHypothesis(in, actorID)
	})
}

func (s *State) reviseHypothesis(in ReviseHypothesisInput, actorID string) error {
	id, err := requireNonEmpty("hypothesis_id", in.HypothesisID)
	if err != nil {
		return err
	}
	reason, err := requireNonEmpty("change_reason", in.ChangeReason)
	if err != nil {
		return err
	}
	hyp := s.findHypothesis(id)
	if hyp == nil {
		return errNotFound("hypothesis %q not found", id)
	}
	brRef, err := validateRevisionRef("branch_ref", in.BranchRef)
	if err != nil {
		return err
	}
	if brRef.ID != hyp.BranchID {
		return errConflict(ErrRevisionMismatch, "hypothesis %q belongs to branch %q, got %q", id, hyp.BranchID, brRef.ID)
	}
	br := s.findBranch(brRef.ID)
	if br == nil {
		return errNotFound("branch %q not found", brRef.ID)
	}
	if br.CurrentRevision != brRef.Revision {
		return errConflict(ErrStaleRevision, "branch %q current revision is %d, got %d", brRef.ID, br.CurrentRevision, brRef.Revision)
	}
	if br.Status == BranchStatusRetired {
		return errConflict(ErrInvalidTransition, "cannot revise hypothesis on retired branch %q", brRef.ID)
	}
	nextRevNum := hyp.CurrentRevision + 1
	rev, err := buildHypothesisRevision(nextRevNum, brRef, in.Statement, in.FalsificationCriterion, in.Assumptions, actorID, reason)
	if err != nil {
		return err
	}
	hyp.Revisions = append(hyp.Revisions, rev)
	hyp.CurrentRevision = nextRevNum
	return nil
}

func buildBranchRevision(
	revision int,
	problemRef RevisionRef,
	title, scope, boundary, rationale, actorID, changeReason string,
) (BranchRevision, error) {
	t, err := requireNonEmpty("title", title)
	if err != nil {
		return BranchRevision{}, err
	}
	sc, err := requireNonEmpty("scope", scope)
	if err != nil {
		return BranchRevision{}, err
	}
	bd, err := requireNonEmpty("boundary", boundary)
	if err != nil {
		return BranchRevision{}, err
	}
	rat, err := requireNonEmpty("rationale", rationale)
	if err != nil {
		return BranchRevision{}, err
	}
	return BranchRevision{
		Revision:     revision,
		ProblemRef:   problemRef,
		Title:        t,
		Scope:        sc,
		Boundary:     bd,
		Rationale:    rat,
		AuthoredBy:   actorID,
		ChangeReason: changeReason,
	}, nil
}

func buildHypothesisRevision(
	revision int,
	branchRef RevisionRef,
	statement, falsification string,
	assumptions []string,
	actorID, changeReason string,
) (HypothesisRevision, error) {
	stmt, err := requireNonEmpty("statement", statement)
	if err != nil {
		return HypothesisRevision{}, err
	}
	fals, err := requireNonEmpty("falsification_criterion", falsification)
	if err != nil {
		return HypothesisRevision{}, err
	}
	cleanAssump, err := cleanOptionalList("assumptions", assumptions)
	if err != nil {
		return HypothesisRevision{}, err
	}
	return HypothesisRevision{
		Revision:               revision,
		BranchRef:              branchRef,
		Statement:              stmt,
		FalsificationCriterion: fals,
		Assumptions:            cleanAssump,
		AuthoredBy:             actorID,
		ChangeReason:           changeReason,
	}, nil
}

// requireActiveBranch checks that branchID exists and is currently active for validation work.
func (s *State) requireActiveBranch(branchID string) (*Branch, error) {
	br := s.findBranch(branchID)
	if br == nil {
		return nil, errNotFound("branch %q not found", branchID)
	}
	switch br.Status {
	case BranchStatusActive:
		return br, nil
	case BranchStatusNeedsReassessment:
		return nil, errConflict(ErrReassessmentRequired, "branch %q requires reassessment before new validation work", branchID)
	default:
		return nil, errConflict(ErrInvalidTransition, "branch %q is %s and not active for validation", branchID, br.Status)
	}
}

func (s State) hypothesisStructureCurrent(hypothesis *Hypothesis) bool {
	branchRef := hypothesis.Revisions[hypothesis.CurrentRevision-1].BranchRef
	branch := s.findBranch(branchRef.ID)
	if branch == nil || branch.CurrentRevision != branchRef.Revision {
		return false
	}
	problemRef := branch.currentRev().ProblemRef
	problem := s.findProblem(problemRef.ID)
	return problem != nil && problem.CurrentRevision == problemRef.Revision &&
		problem.currentRev().Status == ProblemStatusConfirmed
}
