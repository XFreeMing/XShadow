package goalproblem

import "strings"

// ProposalStatus tracks the lifecycle of a charter RevisionProposal.
type ProposalStatus string

const (
	ProposalStatusOpen       ProposalStatus = "open"
	ProposalStatusApplied    ProposalStatus = "applied"
	ProposalStatusSuperseded ProposalStatus = "superseded"
)

// GoalDecision records an authorized retain/revise/pause/stop decision on the goal.
type GoalDecision struct {
	ID                string       `json:"id"`
	Kind              DecisionKind `json:"kind"`
	GoalVersion       int          `json:"goal_version"`
	SynthesisRef      RevisionRef  `json:"synthesis_ref"`
	Reason            string       `json:"reason"`
	EvidenceResultIDs []string     `json:"evidence_result_ids"`
	ReviewTrigger     string       `json:"review_trigger"`
	DecidedBy         string       `json:"decided_by"`
}

// RevisionProposal proposes a meaningful charter change from a specific goal version and synthesis.
type RevisionProposal struct {
	ID                   string         `json:"id"`
	BaseGoalVersion      int            `json:"base_goal_version"`
	SynthesisRef         RevisionRef    `json:"synthesis_ref"`
	DecisionID           string         `json:"decision_id,omitempty"`
	ProposedCharter      Charter        `json:"proposed_charter"`
	ChangeSummary        string         `json:"change_summary"`
	ProposedBy           string         `json:"proposed_by"`
	Status               ProposalStatus `json:"status"`
	AppliedBy            string         `json:"applied_by,omitempty"`
	ResultingGoalVersion int            `json:"resulting_goal_version,omitempty"`
}

// RecordDecisionInput records a retain, revise, pause, or stop decision.
type RecordDecisionInput struct {
	DecisionID        string       `json:"decision_id"`
	Kind              DecisionKind `json:"kind"`
	SynthesisRef      RevisionRef  `json:"synthesis_ref"`
	Reason            string       `json:"reason"`
	EvidenceResultIDs []string     `json:"evidence_result_ids"`
	ReviewTrigger     string       `json:"review_trigger"`
}

// ProposeCharterRevisionInput proposes a new charter version grounded in current goal version and synthesis.
type ProposeCharterRevisionInput struct {
	ProposalID      string      `json:"proposal_id"`
	BaseGoalVersion int         `json:"base_goal_version"`
	SynthesisRef    RevisionRef `json:"synthesis_ref"`
	DecisionID      string      `json:"decision_id,omitempty"`
	ProposedCharter Charter     `json:"proposed_charter"`
	ChangeSummary   string      `json:"change_summary"`
}

// ApplyCharterRevisionInput applies an open RevisionProposal as the next GoalVersion.
type ApplyCharterRevisionInput struct {
	ProposalID string `json:"proposal_id"`
}

// RecordDecision records an authorized retain, revise, pause, or stop decision
// without altering the live goal charter.
func (s *State) RecordDecision(in RecordDecisionInput, actor Actor) error {
	return s.transact(actor, func(next *State, actorID string) error {
		return next.recordDecision(in, actor, actorID)
	})
}

func (s *State) recordDecision(in RecordDecisionInput, actor Actor, actorID string) error {
	if !actor.CanDecide {
		return errForbidden(nil, "actor %q is not authorized to record goal decisions", actorID)
	}
	id, err := requireNonEmpty("decision_id", in.DecisionID)
	if err != nil {
		return err
	}
	if s.findDecision(id) != nil {
		return errConflict(ErrDuplicateID, "decision %q already exists", id)
	}
	if err := validateDecisionKind("kind", in.Kind); err != nil {
		return err
	}
	reason, err := requireNonEmpty("reason", in.Reason)
	if err != nil {
		return err
	}
	trigger, err := requireNonEmpty("review_trigger", in.ReviewTrigger)
	if err != nil {
		return err
	}
	synRef, synRev, err := s.requirePublishedCurrentSynthesis(in.SynthesisRef, s.Current().Number)
	if err != nil {
		return err
	}
	if synRev.Recommendation != in.Kind {
		return errConflict(ErrRevisionMismatch, "synthesis %s@%d recommends %s, not %s", synRef.ID, synRef.Revision, synRev.Recommendation, in.Kind)
	}
	if len(in.EvidenceResultIDs) == 0 {
		return errInvalid("evidence_result_ids must contain at least one reviewed result")
	}

	seen := make(map[string]struct{}, len(in.EvidenceResultIDs))
	evidenceIDs := make([]string, len(in.EvidenceResultIDs))
	hasDecisive := false
	for i, rawID := range in.EvidenceResultIDs {
		resID, err := requireNonEmpty("evidence_result_ids", rawID)
		if err != nil {
			return err
		}
		if _, dup := seen[resID]; dup {
			return errInvalid("duplicate evidence result id %q", resID)
		}
		seen[resID] = struct{}{}
		res := s.findValidationResult(resID)
		if res == nil {
			return errNotFound("validation result %q not found", resID)
		}
		if res.Review == nil {
			return errConflict(ErrInvalidTransition, "validation result %q is not reviewed", resID)
		}
		if res.Review.Verdict == VerdictSupported || res.Review.Verdict == VerdictRefuted {
			hasDecisive = true
		}
		evidenceIDs[i] = resID
	}
	if in.Kind == DecisionRevise && !hasDecisive {
		return errConflict(ErrInsufficientEvidence, "insufficient evidence alone cannot justify a revise decision")
	}

	dec := GoalDecision{
		ID:                id,
		Kind:              in.Kind,
		GoalVersion:       s.Current().Number,
		SynthesisRef:      synRef,
		Reason:            reason,
		EvidenceResultIDs: evidenceIDs,
		ReviewTrigger:     trigger,
		DecidedBy:         actorID,
	}
	s.Decisions = append(s.Decisions, dec)
	s.onDecisionRecorded(dec)
	return nil
}

// ProposeCharterRevision creates an open proposal for a meaningful charter change
// grounded in the current goal version and current published revise synthesis.
func (s *State) ProposeCharterRevision(in ProposeCharterRevisionInput, actor Actor) error {
	return s.transact(actor, func(next *State, actorID string) error {
		return next.proposeCharterRevision(in, actorID)
	})
}

func (s *State) proposeCharterRevision(in ProposeCharterRevisionInput, actorID string) error {
	id, err := requireNonEmpty("proposal_id", in.ProposalID)
	if err != nil {
		return err
	}
	if s.findProposal(id) != nil {
		return errConflict(ErrDuplicateID, "proposal %q already exists", id)
	}
	if in.BaseGoalVersion != s.Current().Number {
		return errConflict(ErrStaleRevision, "base_goal_version %d does not match current goal version %d", in.BaseGoalVersion, s.Current().Number)
	}
	changeSummary, err := requireNonEmpty("change_summary", in.ChangeSummary)
	if err != nil {
		return err
	}
	proposedCharter, err := normalizeCharter(in.ProposedCharter)
	if err != nil {
		return err
	}
	if chartersEqual(proposedCharter, s.Current().Charter) {
		return errInvalid("proposed_charter must represent a meaningful change from current charter")
	}

	synRef, synRev, err := s.requirePublishedCurrentSynthesis(in.SynthesisRef, in.BaseGoalVersion)
	if err != nil {
		return err
	}
	if synRev.Recommendation != DecisionRevise {
		return errConflict(ErrInvalidTransition, "synthesis %s@%d recommendation is %s, expected revise", synRef.ID, synRef.Revision, synRev.Recommendation)
	}
	if !synthesisHasDecisiveEvidence(*synRev) {
		return errConflict(ErrInsufficientEvidence, "synthesis %s@%d lacks decisive evidence for charter revision", synRef.ID, synRef.Revision)
	}

	decisionID := strings.TrimSpace(in.DecisionID)
	if decisionID != "" {
		dec := s.findDecision(decisionID)
		if dec == nil {
			return errNotFound("decision %q not found", decisionID)
		}
		if dec.Kind != DecisionRevise || dec.GoalVersion != in.BaseGoalVersion || dec.SynthesisRef != synRef {
			return errConflict(ErrRevisionMismatch, "decision %q does not match revise proposal base version and synthesis", decisionID)
		}
	}

	s.Proposals = append(s.Proposals, RevisionProposal{
		ID:              id,
		BaseGoalVersion: in.BaseGoalVersion,
		SynthesisRef:    synRef,
		DecisionID:      decisionID,
		ProposedCharter: proposedCharter,
		ChangeSummary:   changeSummary,
		ProposedBy:      actorID,
		Status:          ProposalStatusOpen,
	})
	return nil
}

// ApplyCharterRevision applies an open, non-stale RevisionProposal as the next
// GoalVersion when invoked by an authorized decision-maker.
func (s *State) ApplyCharterRevision(in ApplyCharterRevisionInput, actor Actor) error {
	return s.transact(actor, func(next *State, actorID string) error {
		return next.applyCharterRevision(in, actor, actorID)
	})
}

func (s *State) applyCharterRevision(in ApplyCharterRevisionInput, actor Actor, actorID string) error {
	if !actor.CanDecide {
		return errForbidden(nil, "actor %q is not authorized to apply charter revisions", actorID)
	}
	id, err := requireNonEmpty("proposal_id", in.ProposalID)
	if err != nil {
		return err
	}
	prop := s.findProposal(id)
	if prop == nil {
		return errNotFound("proposal %q not found", id)
	}
	switch prop.Status {
	case ProposalStatusApplied:
		return errConflict(ErrInvalidTransition, "proposal %q has already been applied", id)
	case ProposalStatusSuperseded:
		return errConflict(ErrStaleProposal, "proposal %q was superseded by a newer goal version", id)
	case ProposalStatusOpen:
	default:
		return errConflict(ErrInvalidTransition, "proposal %q has invalid status %q", id, prop.Status)
	}

	if s.GoalStatus == GoalStatusStopped {
		return errConflict(ErrInvalidTransition, "cannot apply charter revision to a stopped goal")
	}
	if prop.BaseGoalVersion != s.Current().Number {
		return errConflict(ErrStaleProposal, "proposal %q targets goal version %d, current is %d", id, prop.BaseGoalVersion, s.Current().Number)
	}

	syn := s.findSynthesis(prop.SynthesisRef.ID)
	if syn == nil || syn.CurrentRevision != prop.SynthesisRef.Revision {
		return errConflict(ErrStaleProposal, "proposal %q synthesis %s@%d has been superseded", id, prop.SynthesisRef.ID, prop.SynthesisRef.Revision)
	}
	synRev := syn.currentRev()
	if synRev.Status != SynthesisStatusPublished {
		return errConflict(ErrStaleProposal, "proposal %q synthesis %s@%d is %s, not published", id, prop.SynthesisRef.ID, prop.SynthesisRef.Revision, synRev.Status)
	}
	if err := s.verifySynthesisDependenciesCurrent(*synRev); err != nil {
		return errConflict(ErrStaleProposal, "proposal %q synthesis dependencies are stale: %v", id, err)
	}

	nextVersion := s.Current().Number + 1
	s.GoalVersions = append(s.GoalVersions, GoalVersion{
		Number:     nextVersion,
		Charter:    prop.ProposedCharter.Clone(),
		ProposalID: prop.ID,
		AppliedBy:  actorID,
	})
	prop.Status = ProposalStatusApplied
	prop.AppliedBy = actorID
	prop.ResultingGoalVersion = nextVersion

	s.onCharterRevisionApplied(prop.ID, prop.BaseGoalVersion)
	return nil
}

func (s *State) requirePublishedCurrentSynthesis(ref RevisionRef, expectedGoalVersion int) (RevisionRef, *SynthesisRevision, error) {
	synRef, err := validateRevisionRef("synthesis_ref", ref)
	if err != nil {
		return RevisionRef{}, nil, err
	}
	syn := s.findSynthesis(synRef.ID)
	if syn == nil {
		return RevisionRef{}, nil, errNotFound("synthesis %q not found", synRef.ID)
	}
	if syn.CurrentRevision != synRef.Revision {
		return RevisionRef{}, nil, errConflict(ErrStaleRevision, "synthesis %q current revision is %d, got %d", synRef.ID, syn.CurrentRevision, synRef.Revision)
	}
	rev := syn.currentRev()
	if rev.Status != SynthesisStatusPublished {
		return RevisionRef{}, nil, errConflict(ErrInvalidTransition, "synthesis %s@%d must be published (status=%s)", synRef.ID, synRef.Revision, rev.Status)
	}
	if rev.BaseGoalVersion != expectedGoalVersion {
		return RevisionRef{}, nil, errConflict(ErrStaleRevision, "synthesis %s@%d targets goal version %d, expected %d", synRef.ID, synRef.Revision, rev.BaseGoalVersion, expectedGoalVersion)
	}
	if err := s.verifySynthesisDependenciesCurrent(*rev); err != nil {
		return RevisionRef{}, nil, err
	}
	return synRef, rev, nil
}
