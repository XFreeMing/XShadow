package goalproblem

import "strings"

// SynthesisStatus tracks the publication and challenge status of a SynthesisRevision.
type SynthesisStatus string

const (
	SynthesisStatusDraft      SynthesisStatus = "draft"
	SynthesisStatusPublished  SynthesisStatus = "published"
	SynthesisStatusChallenged SynthesisStatus = "challenged"
	SynthesisStatusSuperseded SynthesisStatus = "superseded"
)

// SynthesisClaim links an analytical claim to actual reviewed results of this goal.
type SynthesisClaim struct {
	ID        string   `json:"id"`
	Statement string   `json:"statement"`
	ResultIDs []string `json:"result_ids"`
}

// ResultDependencySnapshot records the exact upstream revisions a synthesis depended on.
type ResultDependencySnapshot struct {
	ResultID      string        `json:"result_id"`
	BranchID      string        `json:"branch_id"`
	HypothesisRef RevisionRef   `json:"hypothesis_ref"`
	ProtocolRef   RevisionRef   `json:"protocol_ref"`
	Verdict       ReviewVerdict `json:"verdict"`
}

// SynthesisChallenge records a refutation that challenged a synthesis dependency.
type SynthesisChallenge struct {
	ResultID      string      `json:"result_id"`
	BranchID      string      `json:"branch_id"`
	HypothesisRef RevisionRef `json:"hypothesis_ref"`
	Reason        string      `json:"reason"`
}

// SynthesisRevision is an immutable versioned synthesis of reviewed goal evidence.
type SynthesisRevision struct {
	Revision            int                        `json:"revision"`
	BaseGoalVersion     int                        `json:"base_goal_version"`
	Claims              []SynthesisClaim           `json:"claims"`
	DependencySnapshots []ResultDependencySnapshot `json:"dependency_snapshots"`
	Tradeoffs           []string                   `json:"tradeoffs"`
	Limitations         []string                   `json:"limitations"`
	Recommendation      DecisionKind               `json:"recommendation"`
	Rationale           string                     `json:"rationale"`
	Status              SynthesisStatus            `json:"status"`
	AuthoredBy          string                     `json:"authored_by"`
	ReviewedBy          string                     `json:"reviewed_by,omitempty"`
	ReviewNotes         string                     `json:"review_notes,omitempty"`
	ChallengedBy        []SynthesisChallenge       `json:"challenged_by,omitempty"`
	ReopenReason        string                     `json:"reopen_reason,omitempty"`
}

// Synthesis tracks versioned synthesis revisions under a stable synthesis ID.
type Synthesis struct {
	ID              string              `json:"id"`
	CurrentRevision int                 `json:"current_revision"`
	Revisions       []SynthesisRevision `json:"revisions"`
}

func (syn *Synthesis) currentRev() *SynthesisRevision {
	if len(syn.Revisions) == 0 {
		return nil
	}
	return &syn.Revisions[len(syn.Revisions)-1]
}

// RecordSynthesisInput creates revision 1 (draft) of a synthesis.
type RecordSynthesisInput struct {
	SynthesisID     string           `json:"synthesis_id"`
	BaseGoalVersion int              `json:"base_goal_version"`
	Claims          []SynthesisClaim `json:"claims"`
	Tradeoffs       []string         `json:"tradeoffs"`
	Limitations     []string         `json:"limitations"`
	Recommendation  DecisionKind     `json:"recommendation"`
	Rationale       string           `json:"rationale"`
}

// ReviewSynthesisInput independently reviews and publishes a draft synthesis revision.
type ReviewSynthesisInput struct {
	SynthesisRef RevisionRef `json:"synthesis_ref"`
	ReviewNotes  string      `json:"review_notes,omitempty"`
}

// ReopenSynthesisInput creates a subsequent draft revision of a synthesis after material changes.
type ReopenSynthesisInput struct {
	SynthesisID     string           `json:"synthesis_id"`
	BaseGoalVersion int              `json:"base_goal_version"`
	ReopenReason    string           `json:"reopen_reason"`
	Claims          []SynthesisClaim `json:"claims"`
	Tradeoffs       []string         `json:"tradeoffs"`
	Limitations     []string         `json:"limitations"`
	Recommendation  DecisionKind     `json:"recommendation"`
	Rationale       string           `json:"rationale"`
}

// RecordSynthesis drafts revision 1 of a synthesis grounded in reviewed results of this goal.
func (s *State) RecordSynthesis(in RecordSynthesisInput, actor Actor) error {
	return s.transact(actor, func(next *State, actorID string) error {
		return next.recordSynthesis(in, actorID)
	})
}

func (s *State) recordSynthesis(in RecordSynthesisInput, actorID string) error {
	id, err := requireNonEmpty("synthesis_id", in.SynthesisID)
	if err != nil {
		return err
	}
	if s.findSynthesis(id) != nil {
		return errConflict(ErrDuplicateID, "synthesis %q already exists", id)
	}
	rev, err := s.buildSynthesisRevision(1, in.BaseGoalVersion, in.Claims, in.Tradeoffs, in.Limitations, in.Recommendation, in.Rationale, actorID, "")
	if err != nil {
		return err
	}
	s.Syntheses = append(s.Syntheses, Synthesis{
		ID:              id,
		CurrentRevision: 1,
		Revisions:       []SynthesisRevision{rev},
	})
	return nil
}

// ReviewSynthesis independently reviews and publishes the current draft synthesis revision.
func (s *State) ReviewSynthesis(in ReviewSynthesisInput, actor Actor) error {
	return s.transact(actor, func(next *State, actorID string) error {
		return next.reviewSynthesis(in, actorID)
	})
}

func (s *State) reviewSynthesis(in ReviewSynthesisInput, actorID string) error {
	synRef, err := validateRevisionRef("synthesis_ref", in.SynthesisRef)
	if err != nil {
		return err
	}
	syn := s.findSynthesis(synRef.ID)
	if syn == nil {
		return errNotFound("synthesis %q not found", synRef.ID)
	}
	if syn.CurrentRevision != synRef.Revision {
		return errConflict(ErrStaleRevision, "synthesis %q current revision is %d, got %d", synRef.ID, syn.CurrentRevision, synRef.Revision)
	}
	rev := syn.currentRev()
	if rev.Status != SynthesisStatusDraft {
		return errConflict(ErrInvalidTransition, "synthesis %q revision %d is %s, expected draft", synRef.ID, synRef.Revision, rev.Status)
	}
	if rev.BaseGoalVersion != s.Current().Number {
		return errConflict(ErrStaleRevision, "synthesis %q targets goal version %d, current is %d", synRef.ID, rev.BaseGoalVersion, s.Current().Number)
	}
	if err := s.verifySynthesisDependenciesCurrent(*rev); err != nil {
		return err
	}
	if rev.AuthoredBy == actorID {
		return errForbidden(ErrIndependentReviewRequired, "synthesis author %q cannot self-review synthesis %q", actorID, synRef.ID)
	}
	rev.Status = SynthesisStatusPublished
	rev.ReviewedBy = actorID
	rev.ReviewNotes = strings.TrimSpace(in.ReviewNotes)
	return nil
}

// ReopenSynthesis appends a new draft revision when dependencies materially change or are challenged.
func (s *State) ReopenSynthesis(in ReopenSynthesisInput, actor Actor) error {
	return s.transact(actor, func(next *State, actorID string) error {
		return next.reopenSynthesis(in, actorID)
	})
}

func (s *State) reopenSynthesis(in ReopenSynthesisInput, actorID string) error {
	id, err := requireNonEmpty("synthesis_id", in.SynthesisID)
	if err != nil {
		return err
	}
	reopenReason, err := requireNonEmpty("reopen_reason", in.ReopenReason)
	if err != nil {
		return err
	}
	syn := s.findSynthesis(id)
	if syn == nil {
		return errNotFound("synthesis %q not found", id)
	}
	nextRevNum := syn.CurrentRevision + 1
	rev, err := s.buildSynthesisRevision(nextRevNum, in.BaseGoalVersion, in.Claims, in.Tradeoffs, in.Limitations, in.Recommendation, in.Rationale, actorID, reopenReason)
	if err != nil {
		return err
	}
	if prev := syn.currentRev(); prev != nil && prev.Status != SynthesisStatusChallenged {
		prev.Status = SynthesisStatusSuperseded
	}
	syn.Revisions = append(syn.Revisions, rev)
	syn.CurrentRevision = nextRevNum
	return nil
}

func (s *State) buildSynthesisRevision(
	revision int,
	baseGoalVersion int,
	claimsIn []SynthesisClaim,
	tradeoffsIn, limitationsIn []string,
	recommendation DecisionKind,
	rationaleIn, actorID, reopenReason string,
) (SynthesisRevision, error) {
	if baseGoalVersion != s.Current().Number {
		return SynthesisRevision{}, errConflict(ErrStaleRevision, "base_goal_version %d does not match current goal version %d", baseGoalVersion, s.Current().Number)
	}
	if err := validateDecisionKind("recommendation", recommendation); err != nil {
		return SynthesisRevision{}, err
	}
	rationale, err := requireNonEmpty("rationale", rationaleIn)
	if err != nil {
		return SynthesisRevision{}, err
	}
	tradeoffs, err := requireNonEmptyList("tradeoffs", tradeoffsIn)
	if err != nil {
		return SynthesisRevision{}, err
	}
	limitations, err := requireNonEmptyList("limitations", limitationsIn)
	if err != nil {
		return SynthesisRevision{}, err
	}
	if len(claimsIn) == 0 {
		return SynthesisRevision{}, errInvalid("claims must contain at least one claim")
	}

	seenClaims := make(map[string]struct{}, len(claimsIn))
	seenDeps := make(map[string]struct{})
	claims := make([]SynthesisClaim, len(claimsIn))
	var deps []ResultDependencySnapshot
	hasDecisiveEvidence := false

	for i, c := range claimsIn {
		cid, err := requireNonEmpty("claims.id", c.ID)
		if err != nil {
			return SynthesisRevision{}, err
		}
		if _, dup := seenClaims[cid]; dup {
			return SynthesisRevision{}, errInvalid("duplicate claim id %q", cid)
		}
		seenClaims[cid] = struct{}{}

		stmt, err := requireNonEmpty("claims.statement", c.Statement)
		if err != nil {
			return SynthesisRevision{}, err
		}
		if len(c.ResultIDs) == 0 {
			return SynthesisRevision{}, errInvalid("claim %q must reference at least one reviewed result", cid)
		}
		seenClaimRes := make(map[string]struct{}, len(c.ResultIDs))
		resIDs := make([]string, len(c.ResultIDs))
		for j, rawResID := range c.ResultIDs {
			resID, err := requireNonEmpty("claims.result_ids", rawResID)
			if err != nil {
				return SynthesisRevision{}, err
			}
			if _, dup := seenClaimRes[resID]; dup {
				return SynthesisRevision{}, errInvalid("duplicate result_id %q in claim %q", resID, cid)
			}
			seenClaimRes[resID] = struct{}{}

			res := s.findValidationResult(resID)
			if res == nil {
				return SynthesisRevision{}, errNotFound("validation result %q not found in this goal", resID)
			}
			if res.Review == nil {
				return SynthesisRevision{}, errConflict(ErrInvalidTransition, "validation result %q has not been independently reviewed", resID)
			}
			if res.Review.LateForCurrentRevisions || !s.resultMatchesCurrentRevisions(*res) {
				return SynthesisRevision{}, errConflict(ErrStaleRevision, "validation result %q does not match current hypothesis/protocol revisions and cannot validate altered revisions", resID)
			}
			if res.Review.Verdict == VerdictSupported || res.Review.Verdict == VerdictRefuted {
				hasDecisiveEvidence = true
			}
			if _, ok := seenDeps[resID]; !ok {
				seenDeps[resID] = struct{}{}
				hyp := s.findHypothesis(res.HypothesisRef.ID)
				branchID := ""
				if hyp != nil {
					branchID = hyp.BranchID
				}
				deps = append(deps, ResultDependencySnapshot{
					ResultID:      res.ID,
					BranchID:      branchID,
					HypothesisRef: res.HypothesisRef,
					ProtocolRef:   res.ProtocolRef,
					Verdict:       res.Review.Verdict,
				})
			}
			resIDs[j] = resID
		}
		claims[i] = SynthesisClaim{
			ID:        cid,
			Statement: stmt,
			ResultIDs: resIDs,
		}
	}

	if recommendation == DecisionRevise && !hasDecisiveEvidence {
		return SynthesisRevision{}, errConflict(ErrInsufficientEvidence, "insufficient evidence alone cannot justify recommending a charter revision")
	}

	return SynthesisRevision{
		Revision:            revision,
		BaseGoalVersion:     baseGoalVersion,
		Claims:              claims,
		DependencySnapshots: deps,
		Tradeoffs:           tradeoffs,
		Limitations:         limitations,
		Recommendation:      recommendation,
		Rationale:           rationale,
		Status:              SynthesisStatusDraft,
		AuthoredBy:          actorID,
		ReopenReason:        reopenReason,
	}, nil
}

func (s *State) verifySynthesisDependenciesCurrent(rev SynthesisRevision) error {
	for _, dep := range rev.DependencySnapshots {
		res := s.findValidationResult(dep.ResultID)
		if res == nil || res.Review == nil || res.Review.LateForCurrentRevisions || !s.resultMatchesCurrentRevisions(*res) {
			return errConflict(ErrStaleRevision, "synthesis dependency result %q is no longer current for its hypothesis/protocol revisions", dep.ResultID)
		}
	}
	return nil
}

func synthesisHasDecisiveEvidence(rev SynthesisRevision) bool {
	for _, dep := range rev.DependencySnapshots {
		if dep.Verdict == VerdictSupported || dep.Verdict == VerdictRefuted {
			return true
		}
	}
	return false
}
