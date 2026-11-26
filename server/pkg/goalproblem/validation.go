package goalproblem

import "strings"

// ProtocolStatus tracks the approval lifecycle of a ProtocolRevision.
type ProtocolStatus string

const (
	ProtocolStatusDraft      ProtocolStatus = "draft"
	ProtocolStatusApproved   ProtocolStatus = "approved"
	ProtocolStatusSuperseded ProtocolStatus = "superseded"
)

// ProtocolRevision is an immutable validation protocol specification.
type ProtocolRevision struct {
	Revision            int            `json:"revision"`
	HypothesisRef       RevisionRef    `json:"hypothesis_ref"`
	PredictedResult     string         `json:"predicted_result"`
	SampleSource        string         `json:"sample_source"`
	ControlOrComparison string         `json:"control_or_comparison"`
	DecisionThreshold   string         `json:"decision_threshold"`
	StopCondition       string         `json:"stop_condition"`
	Status              ProtocolStatus `json:"status"`
	Approved            bool           `json:"approved"`
	AuthoredBy          string         `json:"authored_by"`
	ApprovedBy          string         `json:"approved_by,omitempty"`
	ChangeReason        string         `json:"change_reason,omitempty"`
}

// ValidationProtocol holds the immutable revision history for a hypothesis validation plan.
type ValidationProtocol struct {
	ID              string             `json:"id"`
	HypothesisID    string             `json:"hypothesis_id"`
	CurrentRevision int                `json:"current_revision"`
	Revisions       []ProtocolRevision `json:"revisions"`
}

func (p *ValidationProtocol) currentRev() *ProtocolRevision {
	if len(p.Revisions) == 0 {
		return nil
	}
	return &p.Revisions[len(p.Revisions)-1]
}

// TaskRef binds a validation batch to a goal-linked issue/task UUID.
type TaskRef struct {
	IssueID     string `json:"issue_id"`
	SubmitterID string `json:"submitter_id,omitempty"`
}

// BatchStatus tracks the execution and review progress of a ValidationBatch.
type BatchStatus string

const (
	BatchStatusRunning   BatchStatus = "running"
	BatchStatusCompleted BatchStatus = "completed"
	BatchStatusReviewed  BatchStatus = "reviewed"
)

// ValidationBatch is a discrete execution run pinned to exact hypothesis and protocol revisions.
type ValidationBatch struct {
	ID            string      `json:"id"`
	HypothesisRef RevisionRef `json:"hypothesis_ref"`
	ProtocolRef   RevisionRef `json:"protocol_ref"`
	Task          TaskRef     `json:"task"`
	PriorBatchID  string      `json:"prior_batch_id,omitempty"`
	StartedBy     string      `json:"started_by"`
	Status        BatchStatus `json:"status"`
}

// RecordProtocolInput drafts a new validation protocol (revision 1).
type RecordProtocolInput struct {
	ProtocolID          string      `json:"protocol_id"`
	HypothesisRef       RevisionRef `json:"hypothesis_ref"`
	PredictedResult     string      `json:"predicted_result"`
	SampleSource        string      `json:"sample_source"`
	ControlOrComparison string      `json:"control_or_comparison"`
	DecisionThreshold   string      `json:"decision_threshold"`
	StopCondition       string      `json:"stop_condition"`
}

// ApproveProtocolInput independently approves an exact draft protocol revision.
type ApproveProtocolInput struct {
	ProtocolRef RevisionRef `json:"protocol_ref"`
}

// ReviseProtocolInput creates a subsequent draft revision of an existing protocol.
type ReviseProtocolInput struct {
	ProtocolID          string      `json:"protocol_id"`
	HypothesisRef       RevisionRef `json:"hypothesis_ref"`
	PredictedResult     string      `json:"predicted_result"`
	SampleSource        string      `json:"sample_source"`
	ControlOrComparison string      `json:"control_or_comparison"`
	DecisionThreshold   string      `json:"decision_threshold"`
	StopCondition       string      `json:"stop_condition"`
	ChangeReason        string      `json:"change_reason"`
}

// StartValidationBatchInput starts a validation run against exact hypothesis and approved protocol revisions.
type StartValidationBatchInput struct {
	BatchID       string      `json:"batch_id"`
	HypothesisRef RevisionRef `json:"hypothesis_ref"`
	ProtocolRef   RevisionRef `json:"protocol_ref"`
	Task          TaskRef     `json:"task"`
	PriorBatchID  string      `json:"prior_batch_id,omitempty"`
}

// RecordProtocol drafts revision 1 of a validation protocol for an active hypothesis.
func (s *State) RecordProtocol(in RecordProtocolInput, actor Actor) error {
	return s.transact(actor, func(next *State, actorID string) error {
		return next.recordProtocol(in, actorID)
	})
}

func (s *State) recordProtocol(in RecordProtocolInput, actorID string) error {
	id, err := requireNonEmpty("protocol_id", in.ProtocolID)
	if err != nil {
		return err
	}
	if s.findProtocol(id) != nil {
		return errConflict(ErrDuplicateID, "protocol %q already exists", id)
	}
	hypRef, hyp, err := s.requireActiveHypothesisRef(in.HypothesisRef)
	if err != nil {
		return err
	}
	rev, err := buildProtocolRevision(1, hypRef, in.PredictedResult, in.SampleSource, in.ControlOrComparison, in.DecisionThreshold, in.StopCondition, actorID, "")
	if err != nil {
		return err
	}
	s.Protocols = append(s.Protocols, ValidationProtocol{
		ID:              id,
		HypothesisID:    hyp.ID,
		CurrentRevision: 1,
		Revisions:       []ProtocolRevision{rev},
	})
	return nil
}

// ApproveProtocol independently approves the exact current draft revision of a protocol.
func (s *State) ApproveProtocol(in ApproveProtocolInput, actor Actor) error {
	return s.transact(actor, func(next *State, actorID string) error {
		return next.approveProtocol(in, actorID)
	})
}

func (s *State) approveProtocol(in ApproveProtocolInput, actorID string) error {
	protoRef, err := validateRevisionRef("protocol_ref", in.ProtocolRef)
	if err != nil {
		return err
	}
	proto := s.findProtocol(protoRef.ID)
	if proto == nil {
		return errNotFound("protocol %q not found", protoRef.ID)
	}
	if proto.CurrentRevision != protoRef.Revision {
		return errConflict(ErrStaleRevision, "protocol %q current revision is %d, got %d", protoRef.ID, proto.CurrentRevision, protoRef.Revision)
	}
	rev := proto.currentRev()
	if rev.Status != ProtocolStatusDraft {
		return errConflict(ErrInvalidTransition, "protocol %q revision %d is %s, expected draft", protoRef.ID, protoRef.Revision, rev.Status)
	}
	if _, _, err := s.requireActiveHypothesisRef(rev.HypothesisRef); err != nil {
		return err
	}
	if rev.AuthoredBy == actorID {
		return errForbidden(ErrIndependentReviewRequired, "protocol author %q cannot self-approve protocol %q", actorID, protoRef.ID)
	}
	rev.Status = ProtocolStatusApproved
	rev.Approved = true
	rev.ApprovedBy = actorID
	return nil
}

// ReviseProtocol creates a subsequent draft revision without rewriting in-flight batches.
func (s *State) ReviseProtocol(in ReviseProtocolInput, actor Actor) error {
	return s.transact(actor, func(next *State, actorID string) error {
		return next.reviseProtocol(in, actorID)
	})
}

func (s *State) reviseProtocol(in ReviseProtocolInput, actorID string) error {
	id, err := requireNonEmpty("protocol_id", in.ProtocolID)
	if err != nil {
		return err
	}
	reason, err := requireNonEmpty("change_reason", in.ChangeReason)
	if err != nil {
		return err
	}
	proto := s.findProtocol(id)
	if proto == nil {
		return errNotFound("protocol %q not found", id)
	}
	hypRef, _, err := s.requireActiveHypothesisRef(in.HypothesisRef)
	if err != nil {
		return err
	}
	if hypRef.ID != proto.HypothesisID {
		return errConflict(ErrRevisionMismatch, "protocol %q belongs to hypothesis %q, got %q", id, proto.HypothesisID, hypRef.ID)
	}
	nextRevNum := proto.CurrentRevision + 1
	rev, err := buildProtocolRevision(nextRevNum, hypRef, in.PredictedResult, in.SampleSource, in.ControlOrComparison, in.DecisionThreshold, in.StopCondition, actorID, reason)
	if err != nil {
		return err
	}
	if prev := proto.currentRev(); prev != nil {
		prev.Status = ProtocolStatusSuperseded
	}
	proto.Revisions = append(proto.Revisions, rev)
	proto.CurrentRevision = nextRevNum
	return nil
}

// StartValidationBatch starts a new validation run pinned to exact current hypothesis
// and current approved protocol revisions, attached to a goal-linked task issue UUID.
func (s *State) StartValidationBatch(in StartValidationBatchInput, actor Actor) error {
	return s.transact(actor, func(next *State, actorID string) error {
		return next.startValidationBatch(in, actorID)
	})
}

func (s *State) startValidationBatch(in StartValidationBatchInput, actorID string) error {
	batchID, err := requireNonEmpty("batch_id", in.BatchID)
	if err != nil {
		return err
	}
	if s.findValidationBatch(batchID) != nil {
		return errConflict(ErrDuplicateID, "validation batch %q already exists", batchID)
	}
	hypRef, hyp, err := s.requireActiveHypothesisRef(in.HypothesisRef)
	if err != nil {
		return err
	}
	protoRef, err := validateRevisionRef("protocol_ref", in.ProtocolRef)
	if err != nil {
		return err
	}
	proto := s.findProtocol(protoRef.ID)
	if proto == nil {
		return errNotFound("protocol %q not found", protoRef.ID)
	}
	if proto.HypothesisID != hyp.ID {
		return errConflict(ErrRevisionMismatch, "protocol %q belongs to hypothesis %q, not %q", proto.ID, proto.HypothesisID, hyp.ID)
	}
	if proto.CurrentRevision != protoRef.Revision {
		return errConflict(ErrStaleRevision, "protocol %q current revision is %d, got %d", protoRef.ID, proto.CurrentRevision, protoRef.Revision)
	}
	protoRev := proto.currentRev()
	if protoRev.Status != ProtocolStatusApproved {
		return errConflict(ErrInvalidTransition, "protocol %q revision %d must be approved before starting a batch (status=%s)", protoRef.ID, protoRef.Revision, protoRev.Status)
	}
	if protoRev.HypothesisRef != hypRef {
		return errConflict(ErrRevisionMismatch, "protocol %q revision %d targets hypothesis %s@%d, got %s@%d", protoRef.ID, protoRef.Revision, protoRev.HypothesisRef.ID, protoRev.HypothesisRef.Revision, hypRef.ID, hypRef.Revision)
	}

	taskIssueID, err := parseUUIDField("task.issue_id", in.Task.IssueID)
	if err != nil {
		return err
	}
	priorBatchID := strings.TrimSpace(in.PriorBatchID)
	if priorBatchID != "" {
		priorBatch := s.findValidationBatch(priorBatchID)
		if priorBatch == nil {
			return errNotFound("prior validation batch %q not found", priorBatchID)
		}
		if priorBatch.HypothesisRef.ID != hyp.ID {
			return errConflict(ErrRevisionMismatch, "prior batch %q belongs to hypothesis %q, not %q", priorBatchID, priorBatch.HypothesisRef.ID, hyp.ID)
		}
		priorRes := s.findValidationResultByBatch(priorBatchID)
		if priorRes == nil || priorRes.Review == nil || priorRes.Review.Verdict != VerdictInsufficient {
			return errConflict(ErrInvalidTransition, "prior batch %q must have an insufficient reviewed result to spawn a follow-up batch", priorBatchID)
		}
	}

	s.ValidationBatches = append(s.ValidationBatches, ValidationBatch{
		ID:            batchID,
		HypothesisRef: hypRef,
		ProtocolRef:   protoRef,
		Task: TaskRef{
			IssueID:     taskIssueID,
			SubmitterID: strings.TrimSpace(in.Task.SubmitterID),
		},
		PriorBatchID: priorBatchID,
		StartedBy:    actorID,
		Status:       BatchStatusRunning,
	})
	return nil
}

func (s *State) requireActiveHypothesisRef(ref RevisionRef) (RevisionRef, *Hypothesis, error) {
	hypRef, err := validateRevisionRef("hypothesis_ref", ref)
	if err != nil {
		return RevisionRef{}, nil, err
	}
	hyp := s.findHypothesis(hypRef.ID)
	if hyp == nil {
		return RevisionRef{}, nil, errNotFound("hypothesis %q not found", hypRef.ID)
	}
	if hyp.CurrentRevision != hypRef.Revision {
		return RevisionRef{}, nil, errConflict(ErrStaleRevision, "hypothesis %q current revision is %d, got %d", hypRef.ID, hyp.CurrentRevision, hypRef.Revision)
	}
	branch, err := s.requireActiveBranch(hyp.BranchID)
	if err != nil {
		return RevisionRef{}, nil, err
	}
	if !s.hypothesisStructureCurrent(hyp) {
		return RevisionRef{}, nil, errConflict(ErrStaleRevision, "hypothesis %q targets a superseded problem or branch revision", hyp.ID)
	}
	for _, entry := range branch.Reassessments {
		if entry.Resolved && entry.Outcome == ReassessmentReplaceHypothesis && entry.HypothesisRef == hypRef {
			return RevisionRef{}, nil, errConflict(ErrStaleRevision, "hypothesis %q revision %d was selected for replacement", hyp.ID, hypRef.Revision)
		}
	}
	return hypRef, hyp, nil
}

func buildProtocolRevision(
	revision int,
	hypRef RevisionRef,
	predicted, sampleSource, control, threshold, stopCond, actorID, changeReason string,
) (ProtocolRevision, error) {
	pred, err := requireNonEmpty("predicted_result", predicted)
	if err != nil {
		return ProtocolRevision{}, err
	}
	sample, err := requireNonEmpty("sample_source", sampleSource)
	if err != nil {
		return ProtocolRevision{}, err
	}
	ctrl, err := requireNonEmpty("control_or_comparison", control)
	if err != nil {
		return ProtocolRevision{}, err
	}
	thresh, err := requireNonEmpty("decision_threshold", threshold)
	if err != nil {
		return ProtocolRevision{}, err
	}
	stop, err := requireNonEmpty("stop_condition", stopCond)
	if err != nil {
		return ProtocolRevision{}, err
	}
	return ProtocolRevision{
		Revision:            revision,
		HypothesisRef:       hypRef,
		PredictedResult:     pred,
		SampleSource:        sample,
		ControlOrComparison: ctrl,
		DecisionThreshold:   thresh,
		StopCondition:       stop,
		Status:              ProtocolStatusDraft,
		Approved:            false,
		AuthoredBy:          actorID,
		ChangeReason:        changeReason,
	}, nil
}
