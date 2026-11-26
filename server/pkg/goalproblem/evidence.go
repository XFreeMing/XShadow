package goalproblem

import "strings"

// EvidenceKind classifies a stable evidence reference.
type EvidenceKind string

const (
	EvidenceKindIssue    EvidenceKind = "issue"
	EvidenceKindArtifact EvidenceKind = "artifact"
	EvidenceKindDataset  EvidenceKind = "dataset"
	EvidenceKindDocument EvidenceKind = "document"
)

// EvidenceRef is a stable reference to supporting evidence for a validation result.
type EvidenceRef struct {
	ID      string       `json:"id"`
	Kind    EvidenceKind `json:"kind"`
	IssueID string       `json:"issue_id,omitempty"`
	Locator string       `json:"locator,omitempty"`
	Summary string       `json:"summary"`
}

// ReviewVerdict is the independent reviewer's determination on a validation result.
type ReviewVerdict string

const (
	VerdictSupported    ReviewVerdict = "supported"
	VerdictRefuted      ReviewVerdict = "refuted"
	VerdictInsufficient ReviewVerdict = "insufficient"
)

// ResultReview captures an independent review of a ValidationResult.
type ResultReview struct {
	Verdict                 ReviewVerdict `json:"verdict"`
	ReviewerID              string        `json:"reviewer_id"`
	Rationale               string        `json:"rationale"`
	LateForCurrentRevisions bool          `json:"late_for_current_revisions,omitempty"`
}

// ValidationResult stores observations, evidence references, and optional review for a batch.
type ValidationResult struct {
	ID            string        `json:"id"`
	BatchID       string        `json:"batch_id"`
	HypothesisRef RevisionRef   `json:"hypothesis_ref"`
	ProtocolRef   RevisionRef   `json:"protocol_ref"`
	Observations  []string      `json:"observations"`
	Evidence      []EvidenceRef `json:"evidence"`
	RecordedBy    string        `json:"recorded_by"`
	Review        *ResultReview `json:"review,omitempty"`
}

// RecordValidationResultInput records observations and stable evidence for a validation batch.
type RecordValidationResultInput struct {
	ResultID     string        `json:"result_id"`
	BatchID      string        `json:"batch_id"`
	Observations []string      `json:"observations"`
	Evidence     []EvidenceRef `json:"evidence"`
}

// ReviewValidationResultInput records an independent review verdict on a result.
type ReviewValidationResultInput struct {
	ResultID  string        `json:"result_id"`
	Verdict   ReviewVerdict `json:"verdict"`
	Rationale string        `json:"rationale"`
}

// RecordValidationResult records observations and stable evidence references for a batch.
func (s *State) RecordValidationResult(in RecordValidationResultInput, actor Actor) error {
	return s.transact(actor, func(next *State, actorID string) error {
		return next.recordValidationResult(in, actorID)
	})
}

func (s *State) recordValidationResult(in RecordValidationResultInput, actorID string) error {
	resultID, err := requireNonEmpty("result_id", in.ResultID)
	if err != nil {
		return err
	}
	if s.findValidationResult(resultID) != nil {
		return errConflict(ErrDuplicateID, "validation result %q already exists", resultID)
	}
	batchID, err := requireNonEmpty("batch_id", in.BatchID)
	if err != nil {
		return err
	}
	batch := s.findValidationBatch(batchID)
	if batch == nil {
		return errNotFound("validation batch %q not found", batchID)
	}
	if batch.Status != BatchStatusRunning || s.findValidationResultByBatch(batchID) != nil {
		return errConflict(ErrInvalidTransition, "validation batch %q already has a recorded result", batchID)
	}
	obs, err := requireNonEmptyList("observations", in.Observations)
	if err != nil {
		return err
	}
	evidence, err := validateEvidenceRefs(in.Evidence)
	if err != nil {
		return err
	}

	s.ValidationResults = append(s.ValidationResults, ValidationResult{
		ID:            resultID,
		BatchID:       batch.ID,
		HypothesisRef: batch.HypothesisRef,
		ProtocolRef:   batch.ProtocolRef,
		Observations:  obs,
		Evidence:      evidence,
		RecordedBy:    actorID,
	})
	batch.Status = BatchStatusCompleted
	return nil
}

// ReviewValidationResult records an independent reviewer's verdict on a recorded result.
func (s *State) ReviewValidationResult(in ReviewValidationResultInput, actor Actor) error {
	return s.transact(actor, func(next *State, actorID string) error {
		return next.reviewValidationResult(in, actorID)
	})
}

func (s *State) reviewValidationResult(in ReviewValidationResultInput, actorID string) error {
	resultID, err := requireNonEmpty("result_id", in.ResultID)
	if err != nil {
		return err
	}
	rationale, err := requireNonEmpty("rationale", in.Rationale)
	if err != nil {
		return err
	}
	switch in.Verdict {
	case VerdictSupported, VerdictRefuted, VerdictInsufficient:
	default:
		return errInvalid("verdict must be one of supported, refuted, insufficient")
	}

	res := s.findValidationResult(resultID)
	if res == nil {
		return errNotFound("validation result %q not found", resultID)
	}
	if res.Review != nil {
		return errConflict(ErrInvalidTransition, "validation result %q has already been reviewed", resultID)
	}
	batch := s.findValidationBatch(res.BatchID)
	if batch == nil {
		return errNotFound("validation batch %q not found", res.BatchID)
	}

	// Enforce independent reviewer: distinct from recorder and known task submitter.
	if actorID == res.RecordedBy {
		return errForbidden(ErrIndependentReviewRequired, "recorder %q cannot review their own validation result %q", actorID, resultID)
	}
	if batch.Task.SubmitterID != "" && actorID == batch.Task.SubmitterID {
		return errForbidden(ErrIndependentReviewRequired, "task submitter %q cannot review validation result %q", actorID, resultID)
	}

	hyp := s.findHypothesis(res.HypothesisRef.ID)
	proto := s.findProtocol(res.ProtocolRef.ID)
	late := hyp == nil || proto == nil ||
		hyp.CurrentRevision != res.HypothesisRef.Revision ||
		proto.CurrentRevision != res.ProtocolRef.Revision

	res.Review = &ResultReview{
		Verdict:                 in.Verdict,
		ReviewerID:              actorID,
		Rationale:               rationale,
		LateForCurrentRevisions: late,
	}
	batch.Status = BatchStatusReviewed

	if hyp != nil {
		s.onResultReviewed(*res, *hyp)
	}
	return nil
}

func validateEvidenceRefs(refs []EvidenceRef) ([]EvidenceRef, error) {
	if len(refs) == 0 {
		return nil, errInvalid("evidence must contain at least one stable reference")
	}
	seen := make(map[string]struct{}, len(refs))
	out := make([]EvidenceRef, len(refs))
	for i, ref := range refs {
		id, err := requireNonEmpty("evidence.id", ref.ID)
		if err != nil {
			return nil, err
		}
		if _, dup := seen[id]; dup {
			return nil, errInvalid("duplicate evidence id %q", id)
		}
		seen[id] = struct{}{}

		summary, err := requireNonEmpty("evidence.summary", ref.Summary)
		if err != nil {
			return nil, err
		}
		locator := strings.TrimSpace(ref.Locator)
		issueID := strings.TrimSpace(ref.IssueID)

		switch ref.Kind {
		case EvidenceKindIssue:
			issueID, err = parseUUIDField("evidence.issue_id", issueID)
			if err != nil {
				return nil, err
			}
		case EvidenceKindArtifact, EvidenceKindDataset, EvidenceKindDocument:
			if locator == "" {
				return nil, errInvalid("evidence[%d].locator is required for kind %q", i, ref.Kind)
			}
			if issueID != "" {
				issueID, err = parseUUIDField("evidence.issue_id", issueID)
				if err != nil {
					return nil, err
				}
			}
		default:
			return nil, errInvalid("evidence[%d].kind must be one of issue, artifact, dataset, document", i)
		}

		out[i] = EvidenceRef{
			ID:      id,
			Kind:    ref.Kind,
			IssueID: issueID,
			Locator: locator,
			Summary: summary,
		}
	}
	return out, nil
}
