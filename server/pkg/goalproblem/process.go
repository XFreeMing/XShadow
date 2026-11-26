package goalproblem

// Cross-domain process manager coordinating explicit, localized side effects
// across the seven cooperating subdomains without coupling entity storage.

// onPrioritizationRecorded updates branch operational status when deferred or reconsidered.
func (s *State) onPrioritizationRecorded(rec PrioritizationRecord) {
	br := s.findBranch(rec.BranchRef.ID)
	if br == nil || br.Status == BranchStatusRetired || br.Status == BranchStatusNeedsReassessment {
		return
	}
	switch rec.Disposition {
	case DispositionDefer:
		br.Status = BranchStatusDeferred
	case DispositionPrioritize:
		if br.Status == BranchStatusDeferred {
			br.Status = BranchStatusActive
		}
	}
}

// onResultReviewed propagates refutations to ONLY the affected branch and
// any active synthesis whose dependencies precisely reference the refuted hypothesis.
// Unrelated branches, unrelated syntheses, and the live goal charter are untouched.
func (s *State) onResultReviewed(result ValidationResult, hyp Hypothesis) {
	if result.Review == nil || result.Review.Verdict != VerdictRefuted {
		return
	}

	// 1. Trigger reassessment on the affected branch if the refuted hypothesis revision is current.
	if result.HypothesisRef.Revision == hyp.CurrentRevision {
		if br := s.findBranch(hyp.BranchID); br != nil && br.Status != BranchStatusRetired {
			br.Status = BranchStatusNeedsReassessment
			br.Reassessments = append(br.Reassessments, BranchReassessment{
				ChallengedByResultID: result.ID,
				HypothesisRef:        result.HypothesisRef,
				ChallengeReason:      result.Review.Rationale,
			})
		}
	}

	// 2. Challenge any draft or published synthesis depending on this exact hypothesis.
	for i := range s.Syntheses {
		rev := s.Syntheses[i].currentRev()
		if rev == nil {
			continue
		}
		if rev.Status != SynthesisStatusDraft && rev.Status != SynthesisStatusPublished {
			continue
		}
		if synthesisDependsOnHypothesis(*rev, result.HypothesisRef.ID, result.ID) {
			rev.Status = SynthesisStatusChallenged
			rev.ChallengedBy = append(rev.ChallengedBy, SynthesisChallenge{
				ResultID:      result.ID,
				BranchID:      hyp.BranchID,
				HypothesisRef: result.HypothesisRef,
				Reason:        result.Review.Rationale,
			})
		}
	}
}

func synthesisDependsOnHypothesis(rev SynthesisRevision, hypothesisID, resultID string) bool {
	for _, dep := range rev.DependencySnapshots {
		if dep.HypothesisRef.ID == hypothesisID || dep.ResultID == resultID {
			return true
		}
	}
	return false
}

// onDecisionRecorded aligns goal lifecycle status with the authorized goal decision.
func (s *State) onDecisionRecorded(dec GoalDecision) {
	switch dec.Kind {
	case DecisionRetain, DecisionRevise:
		s.GoalStatus = GoalStatusActive
	case DecisionPause:
		s.GoalStatus = GoalStatusPaused
	case DecisionStop:
		s.GoalStatus = GoalStatusStopped
	}
}

// onCharterRevisionApplied supersedes competing open proposals for the old goal version.
func (s *State) onCharterRevisionApplied(appliedProposalID string, baseGoalVersion int) {
	s.GoalStatus = GoalStatusActive
	for i := range s.Proposals {
		if s.Proposals[i].ID == appliedProposalID {
			continue
		}
		if s.Proposals[i].Status == ProposalStatusOpen && s.Proposals[i].BaseGoalVersion == baseGoalVersion {
			s.Proposals[i].Status = ProposalStatusSuperseded
		}
	}
}
