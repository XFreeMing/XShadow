package goalproblem

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
)

// CommandAction enumerates all supported domain commands.
type CommandAction string

const (
	ActionDefineProblem          CommandAction = "define_problem"
	ActionConfirmProblem         CommandAction = "confirm_problem"
	ActionReviseProblem          CommandAction = "revise_problem"
	ActionCreateBranch           CommandAction = "create_branch"
	ActionReviseBranch           CommandAction = "revise_branch"
	ActionReassessBranch         CommandAction = "reassess_branch"
	ActionCreateHypothesis       CommandAction = "create_hypothesis"
	ActionReviseHypothesis       CommandAction = "revise_hypothesis"
	ActionRecordPrioritization   CommandAction = "record_prioritization"
	ActionRecordProtocol         CommandAction = "record_protocol"
	ActionApproveProtocol        CommandAction = "approve_protocol"
	ActionReviseProtocol         CommandAction = "revise_protocol"
	ActionStartValidationBatch   CommandAction = "start_validation_batch"
	ActionRecordValidationResult CommandAction = "record_validation_result"
	ActionReviewValidationResult CommandAction = "review_validation_result"
	ActionRecordSynthesis        CommandAction = "record_synthesis"
	ActionReviewSynthesis        CommandAction = "review_synthesis"
	ActionReopenSynthesis        CommandAction = "reopen_synthesis"
	ActionRecordDecision         CommandAction = "record_decision"
	ActionProposeCharterRevision CommandAction = "propose_charter_revision"
	ActionApplyCharterRevision   CommandAction = "apply_charter_revision"
)

// Command is a strictly typed discriminated union for API orchestration.
// Exactly one payload pointer corresponding to Action must be non-nil.
type Command struct {
	Action                 CommandAction                `json:"action"`
	DefineProblem          *DefineProblemInput          `json:"define_problem,omitempty"`
	ConfirmProblem         *ConfirmProblemInput         `json:"confirm_problem,omitempty"`
	ReviseProblem          *ReviseProblemInput          `json:"revise_problem,omitempty"`
	CreateBranch           *CreateBranchInput           `json:"create_branch,omitempty"`
	ReviseBranch           *ReviseBranchInput           `json:"revise_branch,omitempty"`
	ReassessBranch         *ReassessBranchInput         `json:"reassess_branch,omitempty"`
	CreateHypothesis       *CreateHypothesisInput       `json:"create_hypothesis,omitempty"`
	ReviseHypothesis       *ReviseHypothesisInput       `json:"revise_hypothesis,omitempty"`
	RecordPrioritization   *RecordPrioritizationInput   `json:"record_prioritization,omitempty"`
	RecordProtocol         *RecordProtocolInput         `json:"record_protocol,omitempty"`
	ApproveProtocol        *ApproveProtocolInput        `json:"approve_protocol,omitempty"`
	ReviseProtocol         *ReviseProtocolInput         `json:"revise_protocol,omitempty"`
	StartValidationBatch   *StartValidationBatchInput   `json:"start_validation_batch,omitempty"`
	RecordValidationResult *RecordValidationResultInput `json:"record_validation_result,omitempty"`
	ReviewValidationResult *ReviewValidationResultInput `json:"review_validation_result,omitempty"`
	RecordSynthesis        *RecordSynthesisInput        `json:"record_synthesis,omitempty"`
	ReviewSynthesis        *ReviewSynthesisInput        `json:"review_synthesis,omitempty"`
	ReopenSynthesis        *ReopenSynthesisInput        `json:"reopen_synthesis,omitempty"`
	RecordDecision         *RecordDecisionInput         `json:"record_decision,omitempty"`
	ProposeCharterRevision *ProposeCharterRevisionInput `json:"propose_charter_revision,omitempty"`
	ApplyCharterRevision   *ApplyCharterRevisionInput   `json:"apply_charter_revision,omitempty"`
}

// DecodeCommand decodes a JSON payload into a typed Command while rejecting
// unknown fields, malformed JSON, trailing tokens, and invalid union shapes.
func DecodeCommand(data []byte) (Command, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()

	var cmd Command
	if err := dec.Decode(&cmd); err != nil {
		return Command{}, errInvalid("decode command: %v", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return Command{}, errInvalid("unexpected trailing JSON after command")
	}
	if err := cmd.validateUnion(); err != nil {
		return Command{}, err
	}
	return cmd, nil
}

// IssueRefs returns all unique non-empty issue UUID references carried by the
// command's typed payload, in deterministic order, so application adapters can
// authorize linked issues without traversing raw JSON.
func (c Command) IssueRefs() []string {
	var refs []string
	seen := make(map[string]struct{})
	add := func(raw string) {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			return
		}
		if _, ok := seen[trimmed]; ok {
			return
		}
		seen[trimmed] = struct{}{}
		refs = append(refs, trimmed)
	}

	if c.StartValidationBatch != nil {
		add(c.StartValidationBatch.Task.IssueID)
	}
	if c.RecordValidationResult != nil {
		for _, ev := range c.RecordValidationResult.Evidence {
			add(ev.IssueID)
		}
	}
	return refs
}

// Apply validates and applies command atomically. Any rejected command leaves
// s unchanged.
func (s *State) Apply(command Command, actor Actor) error {
	if s == nil || s.Current().Number == 0 {
		return errConflict(ErrUninitialized, "goal state is uninitialized")
	}
	if err := command.validateUnion(); err != nil {
		return err
	}
	return s.transact(actor, func(next *State, actorID string) error {
		switch command.Action {
		case ActionDefineProblem:
			return next.defineProblem(*command.DefineProblem, actorID)
		case ActionConfirmProblem:
			return next.confirmProblem(*command.ConfirmProblem, actorID)
		case ActionReviseProblem:
			return next.reviseProblem(*command.ReviseProblem, actorID)
		case ActionCreateBranch:
			return next.createBranch(*command.CreateBranch, actorID)
		case ActionReviseBranch:
			return next.reviseBranch(*command.ReviseBranch, actorID)
		case ActionReassessBranch:
			return next.reassessBranch(*command.ReassessBranch, actorID)
		case ActionCreateHypothesis:
			return next.createHypothesis(*command.CreateHypothesis, actorID)
		case ActionReviseHypothesis:
			return next.reviseHypothesis(*command.ReviseHypothesis, actorID)
		case ActionRecordPrioritization:
			return next.recordPrioritization(*command.RecordPrioritization, actorID)
		case ActionRecordProtocol:
			return next.recordProtocol(*command.RecordProtocol, actorID)
		case ActionApproveProtocol:
			return next.approveProtocol(*command.ApproveProtocol, actorID)
		case ActionReviseProtocol:
			return next.reviseProtocol(*command.ReviseProtocol, actorID)
		case ActionStartValidationBatch:
			return next.startValidationBatch(*command.StartValidationBatch, actorID)
		case ActionRecordValidationResult:
			return next.recordValidationResult(*command.RecordValidationResult, actorID)
		case ActionReviewValidationResult:
			return next.reviewValidationResult(*command.ReviewValidationResult, actorID)
		case ActionRecordSynthesis:
			return next.recordSynthesis(*command.RecordSynthesis, actorID)
		case ActionReviewSynthesis:
			return next.reviewSynthesis(*command.ReviewSynthesis, actorID)
		case ActionReopenSynthesis:
			return next.reopenSynthesis(*command.ReopenSynthesis, actorID)
		case ActionRecordDecision:
			return next.recordDecision(*command.RecordDecision, actor, actorID)
		case ActionProposeCharterRevision:
			return next.proposeCharterRevision(*command.ProposeCharterRevision, actorID)
		case ActionApplyCharterRevision:
			return next.applyCharterRevision(*command.ApplyCharterRevision, actor, actorID)
		default:
			return errInvalid("unsupported command action %q", command.Action)
		}
	})
}

func (c Command) validateUnion() error {
	var populated []CommandAction
	check := func(action CommandAction, present bool) {
		if present {
			populated = append(populated, action)
		}
	}
	check(ActionDefineProblem, c.DefineProblem != nil)
	check(ActionConfirmProblem, c.ConfirmProblem != nil)
	check(ActionReviseProblem, c.ReviseProblem != nil)
	check(ActionCreateBranch, c.CreateBranch != nil)
	check(ActionReviseBranch, c.ReviseBranch != nil)
	check(ActionReassessBranch, c.ReassessBranch != nil)
	check(ActionCreateHypothesis, c.CreateHypothesis != nil)
	check(ActionReviseHypothesis, c.ReviseHypothesis != nil)
	check(ActionRecordPrioritization, c.RecordPrioritization != nil)
	check(ActionRecordProtocol, c.RecordProtocol != nil)
	check(ActionApproveProtocol, c.ApproveProtocol != nil)
	check(ActionReviseProtocol, c.ReviseProtocol != nil)
	check(ActionStartValidationBatch, c.StartValidationBatch != nil)
	check(ActionRecordValidationResult, c.RecordValidationResult != nil)
	check(ActionReviewValidationResult, c.ReviewValidationResult != nil)
	check(ActionRecordSynthesis, c.RecordSynthesis != nil)
	check(ActionReviewSynthesis, c.ReviewSynthesis != nil)
	check(ActionReopenSynthesis, c.ReopenSynthesis != nil)
	check(ActionRecordDecision, c.RecordDecision != nil)
	check(ActionProposeCharterRevision, c.ProposeCharterRevision != nil)
	check(ActionApplyCharterRevision, c.ApplyCharterRevision != nil)

	if len(populated) != 1 {
		return errInvalid("command must populate exactly one typed payload, got %d", len(populated))
	}
	if c.Action == "" {
		return errInvalid("command action is required")
	}
	if populated[0] != c.Action {
		return errInvalid("command action %q does not match populated payload %q", c.Action, populated[0])
	}
	return nil
}
