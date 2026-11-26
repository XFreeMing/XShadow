package goalproblem

// ProblemStatus tracks the lifecycle of a ProblemRevision.
type ProblemStatus string

const (
	ProblemStatusDraft      ProblemStatus = "draft"
	ProblemStatusConfirmed  ProblemStatus = "confirmed"
	ProblemStatusSuperseded ProblemStatus = "superseded"
)

// ProblemRevision is an immutable snapshot of a problem definition revision.
type ProblemRevision struct {
	Revision         int           `json:"revision"`
	Statement        string        `json:"statement"`
	Baseline         string        `json:"baseline"`
	AffectedUsers    []string      `json:"affected_users"`
	Constraints      []string      `json:"constraints"`
	SuccessCriterion string        `json:"success_criterion"`
	Status           ProblemStatus `json:"status"`
	AuthoredBy       string        `json:"authored_by"`
	ConfirmedBy      string        `json:"confirmed_by,omitempty"`
	ReopenReason     string        `json:"reopen_reason,omitempty"`
}

// Problem represents a defined problem with immutable revisions.
type Problem struct {
	ID              string            `json:"id"`
	CurrentRevision int               `json:"current_revision"`
	Revisions       []ProblemRevision `json:"revisions"`
}

func (p *Problem) currentRev() *ProblemRevision {
	if len(p.Revisions) == 0 {
		return nil
	}
	return &p.Revisions[len(p.Revisions)-1]
}

// DefineProblemInput defines a new problem (revision 1, draft).
type DefineProblemInput struct {
	ProblemID        string   `json:"problem_id"`
	Statement        string   `json:"statement"`
	Baseline         string   `json:"baseline"`
	AffectedUsers    []string `json:"affected_users"`
	Constraints      []string `json:"constraints"`
	SuccessCriterion string   `json:"success_criterion"`
}

// ConfirmProblemInput confirms the exact draft revision of a problem.
type ConfirmProblemInput struct {
	ProblemRef RevisionRef `json:"problem_ref"`
}

// ReviseProblemInput creates a subsequent draft revision of an existing problem.
type ReviseProblemInput struct {
	ProblemID        string   `json:"problem_id"`
	Statement        string   `json:"statement"`
	Baseline         string   `json:"baseline"`
	AffectedUsers    []string `json:"affected_users"`
	Constraints      []string `json:"constraints"`
	SuccessCriterion string   `json:"success_criterion"`
	ReopenReason     string   `json:"reopen_reason"`
}

// DefineProblem creates a new problem at revision 1 in draft status.
func (s *State) DefineProblem(in DefineProblemInput, actor Actor) error {
	return s.transact(actor, func(next *State, actorID string) error {
		return next.defineProblem(in, actorID)
	})
}

func (s *State) defineProblem(in DefineProblemInput, actorID string) error {
	id, err := requireNonEmpty("problem_id", in.ProblemID)
	if err != nil {
		return err
	}
	if s.findProblem(id) != nil {
		return errConflict(ErrDuplicateID, "problem %q already exists", id)
	}
	rev, err := buildProblemRevision(1, in.Statement, in.Baseline, in.AffectedUsers, in.Constraints, in.SuccessCriterion, actorID, "")
	if err != nil {
		return err
	}
	s.Problems = append(s.Problems, Problem{
		ID:              id,
		CurrentRevision: 1,
		Revisions:       []ProblemRevision{rev},
	})
	return nil
}

// ConfirmProblem confirms the exact current draft revision of a problem.
func (s *State) ConfirmProblem(in ConfirmProblemInput, actor Actor) error {
	return s.transact(actor, func(next *State, actorID string) error {
		return next.confirmProblem(in, actorID)
	})
}

func (s *State) confirmProblem(in ConfirmProblemInput, actorID string) error {
	ref, err := validateRevisionRef("problem_ref", in.ProblemRef)
	if err != nil {
		return err
	}
	prob := s.findProblem(ref.ID)
	if prob == nil {
		return errNotFound("problem %q not found", ref.ID)
	}
	if prob.CurrentRevision != ref.Revision {
		return errConflict(ErrStaleRevision, "problem %q current revision is %d, got %d", ref.ID, prob.CurrentRevision, ref.Revision)
	}
	rev := prob.currentRev()
	if rev.Status != ProblemStatusDraft {
		return errConflict(ErrInvalidTransition, "problem %q revision %d is %s, expected draft", ref.ID, ref.Revision, rev.Status)
	}
	rev.Status = ProblemStatusConfirmed
	rev.ConfirmedBy = actorID
	return nil
}

// ReviseProblem supersedes the current revision and appends a new draft revision.
func (s *State) ReviseProblem(in ReviseProblemInput, actor Actor) error {
	return s.transact(actor, func(next *State, actorID string) error {
		return next.reviseProblem(in, actorID)
	})
}

func (s *State) reviseProblem(in ReviseProblemInput, actorID string) error {
	id, err := requireNonEmpty("problem_id", in.ProblemID)
	if err != nil {
		return err
	}
	reason, err := requireNonEmpty("reopen_reason", in.ReopenReason)
	if err != nil {
		return err
	}
	prob := s.findProblem(id)
	if prob == nil {
		return errNotFound("problem %q not found", id)
	}
	nextRevNum := prob.CurrentRevision + 1
	rev, err := buildProblemRevision(nextRevNum, in.Statement, in.Baseline, in.AffectedUsers, in.Constraints, in.SuccessCriterion, actorID, reason)
	if err != nil {
		return err
	}
	if prev := prob.currentRev(); prev != nil {
		prev.Status = ProblemStatusSuperseded
	}
	prob.Revisions = append(prob.Revisions, rev)
	prob.CurrentRevision = nextRevNum
	return nil
}

func buildProblemRevision(
	revision int,
	statement, baseline string,
	affectedUsers, constraints []string,
	successCriterion, actorID, reopenReason string,
) (ProblemRevision, error) {
	stmt, err := requireNonEmpty("statement", statement)
	if err != nil {
		return ProblemRevision{}, err
	}
	base, err := requireNonEmpty("baseline", baseline)
	if err != nil {
		return ProblemRevision{}, err
	}
	users, err := requireNonEmptyList("affected_users", affectedUsers)
	if err != nil {
		return ProblemRevision{}, err
	}
	cons, err := requireNonEmptyList("constraints", constraints)
	if err != nil {
		return ProblemRevision{}, err
	}
	crit, err := requireNonEmpty("success_criterion", successCriterion)
	if err != nil {
		return ProblemRevision{}, err
	}
	return ProblemRevision{
		Revision:         revision,
		Statement:        stmt,
		Baseline:         base,
		AffectedUsers:    users,
		Constraints:      cons,
		SuccessCriterion: crit,
		Status:           ProblemStatusDraft,
		AuthoredBy:       actorID,
		ReopenReason:     reopenReason,
	}, nil
}

// requireConfirmedProblemRef validates that ref points to the current confirmed revision of an existing problem.
func (s *State) requireConfirmedProblemRef(ref RevisionRef) (RevisionRef, error) {
	cleanRef, err := validateRevisionRef("problem_ref", ref)
	if err != nil {
		return RevisionRef{}, err
	}
	prob := s.findProblem(cleanRef.ID)
	if prob == nil {
		return RevisionRef{}, errNotFound("problem %q not found", cleanRef.ID)
	}
	if prob.CurrentRevision != cleanRef.Revision {
		return RevisionRef{}, errConflict(ErrStaleRevision, "problem %q current revision is %d, got %d", cleanRef.ID, prob.CurrentRevision, cleanRef.Revision)
	}
	rev := prob.currentRev()
	if rev.Status != ProblemStatusConfirmed {
		return RevisionRef{}, errConflict(ErrInvalidTransition, "problem %q revision %d must be confirmed before branching (status=%s)", cleanRef.ID, cleanRef.Revision, rev.Status)
	}
	return cleanRef, nil
}
