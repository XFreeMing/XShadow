package goalproblem

import "strings"

// PrioritizationDisposition indicates whether a branch is prioritized or deferred.
type PrioritizationDisposition string

const (
	DispositionPrioritize PrioritizationDisposition = "prioritize"
	DispositionDefer      PrioritizationDisposition = "defer"
)

// PrioritizationRecord is an immutable prioritization or deferral judgment on a branch.
type PrioritizationRecord struct {
	ID                          string                    `json:"id"`
	Sequence                    int                       `json:"sequence"`
	BranchRef                   RevisionRef               `json:"branch_ref"`
	Disposition                 PrioritizationDisposition `json:"disposition"`
	Rank                        int                       `json:"rank,omitempty"`
	Impact                      Level                     `json:"impact"`
	Controllability             Level                     `json:"controllability"`
	Cost                        Level                     `json:"cost"`
	ReopeningCondition          string                    `json:"reopening_condition,omitempty"`
	Rationale                   string                    `json:"rationale"`
	ReconsidersPrioritizationID string                    `json:"reconsiders_prioritization_id,omitempty"`
	RecordedBy                  string                    `json:"recorded_by"`
}

// RecordPrioritizationInput prioritizes, defers, or reconsiders a branch.
type RecordPrioritizationInput struct {
	PrioritizationID            string                    `json:"prioritization_id"`
	BranchRef                   RevisionRef               `json:"branch_ref"`
	Disposition                 PrioritizationDisposition `json:"disposition"`
	Rank                        int                       `json:"rank,omitempty"`
	Impact                      Level                     `json:"impact"`
	Controllability             Level                     `json:"controllability"`
	Cost                        Level                     `json:"cost"`
	ReopeningCondition          string                    `json:"reopening_condition,omitempty"`
	Rationale                   string                    `json:"rationale"`
	ReconsidersPrioritizationID string                    `json:"reconsiders_prioritization_id,omitempty"`
}

// RecordPrioritization records a new immutable prioritization or deferral judgment
// on a branch without modifying the live goal charter.
func (s *State) RecordPrioritization(in RecordPrioritizationInput, actor Actor) error {
	return s.transact(actor, func(next *State, actorID string) error {
		return next.recordPrioritization(in, actorID)
	})
}

func (s *State) recordPrioritization(in RecordPrioritizationInput, actorID string) error {
	id, err := requireNonEmpty("prioritization_id", in.PrioritizationID)
	if err != nil {
		return err
	}
	if s.findPrioritization(id) != nil {
		return errConflict(ErrDuplicateID, "prioritization %q already exists", id)
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
	switch br.Status {
	case BranchStatusRetired:
		return errConflict(ErrInvalidTransition, "retired branch %q cannot be prioritized", brRef.ID)
	case BranchStatusNeedsReassessment:
		return errConflict(ErrReassessmentRequired, "branch %q requires reassessment before reprioritization", brRef.ID)
	}

	if err := validateLevel("impact", in.Impact); err != nil {
		return err
	}
	if err := validateLevel("controllability", in.Controllability); err != nil {
		return err
	}
	if err := validateLevel("cost", in.Cost); err != nil {
		return err
	}
	rationale, err := requireNonEmpty("rationale", in.Rationale)
	if err != nil {
		return err
	}

	reopenCond := strings.TrimSpace(in.ReopeningCondition)
	switch in.Disposition {
	case DispositionPrioritize:
		if in.Rank < 1 {
			return errInvalid("rank must be >= 1 when prioritizing a branch")
		}
	case DispositionDefer:
		if in.Rank != 0 {
			return errInvalid("rank must be 0 when deferring a branch")
		}
		if reopenCond == "" {
			return errInvalid("reopening_condition is required when deferring a branch")
		}
	default:
		return errInvalid("disposition must be prioritize or defer")
	}

	reconsidersID := strings.TrimSpace(in.ReconsidersPrioritizationID)
	if reconsidersID != "" {
		prior := s.findPrioritization(reconsidersID)
		if prior == nil {
			return errNotFound("prioritization %q not found", reconsidersID)
		}
		if prior.BranchRef.ID != br.ID {
			return errConflict(ErrRevisionMismatch, "prioritization %q belongs to branch %q, got %q", reconsidersID, prior.BranchRef.ID, br.ID)
		}
		if prior.Disposition != DispositionDefer {
			return errConflict(ErrInvalidTransition, "prioritization %q is not a deferral record", reconsidersID)
		}
	} else if br.Status == BranchStatusDeferred && in.Disposition == DispositionPrioritize {
		// Automatically link to the most recent deferral for this branch if omitted.
		for i := len(s.Prioritizations) - 1; i >= 0; i-- {
			if s.Prioritizations[i].BranchRef.ID == br.ID && s.Prioritizations[i].Disposition == DispositionDefer {
				reconsidersID = s.Prioritizations[i].ID
				break
			}
		}
	}

	rec := PrioritizationRecord{
		ID:                          id,
		Sequence:                    len(s.Prioritizations) + 1,
		BranchRef:                   brRef,
		Disposition:                 in.Disposition,
		Rank:                        in.Rank,
		Impact:                      in.Impact,
		Controllability:             in.Controllability,
		Cost:                        in.Cost,
		ReopeningCondition:          reopenCond,
		Rationale:                   rationale,
		ReconsidersPrioritizationID: reconsidersID,
		RecordedBy:                  actorID,
	}
	s.Prioritizations = append(s.Prioritizations, rec)
	s.onPrioritizationRecorded(rec)
	return nil
}
