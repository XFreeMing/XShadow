package goalproblem

import (
	"slices"
	"strings"

	"github.com/google/uuid"
)

// Actor identifies the authenticated caller executing a command. It is supplied
// by authenticated application code and never decoded from command payloads.
type Actor struct {
	ID        string
	CanDecide bool
}

// Charter is the live goal charter snapshot captured in each GoalVersion.
type Charter struct {
	Title              string   `json:"title"`
	Intent             string   `json:"intent"`
	Scope              string   `json:"scope"`
	AcceptanceCriteria []string `json:"acceptance_criteria"`
}

// Clone returns a defensive copy of Charter.
func (c Charter) Clone() Charter {
	return Charter{
		Title:              c.Title,
		Intent:             c.Intent,
		Scope:              c.Scope,
		AcceptanceCriteria: slices.Clone(c.AcceptanceCriteria),
	}
}

// Validate checks that Charter has all required fields populated.
func (c Charter) Validate() error {
	_, err := normalizeCharter(c)
	return err
}

func normalizeCharter(c Charter) (Charter, error) {
	title, err := requireNonEmpty("charter.title", c.Title)
	if err != nil {
		return Charter{}, err
	}
	intent, err := requireNonEmpty("charter.intent", c.Intent)
	if err != nil {
		return Charter{}, err
	}
	scope, err := requireNonEmpty("charter.scope", c.Scope)
	if err != nil {
		return Charter{}, err
	}
	criteria, err := requireNonEmptyList("charter.acceptance_criteria", c.AcceptanceCriteria)
	if err != nil {
		return Charter{}, err
	}
	return Charter{
		Title:              title,
		Intent:             intent,
		Scope:              scope,
		AcceptanceCriteria: criteria,
	}, nil
}

func chartersEqual(a, b Charter) bool {
	return strings.TrimSpace(a.Title) == strings.TrimSpace(b.Title) &&
		strings.TrimSpace(a.Intent) == strings.TrimSpace(b.Intent) &&
		strings.TrimSpace(a.Scope) == strings.TrimSpace(b.Scope) &&
		slices.Equal(trimListCopy(a.AcceptanceCriteria), trimListCopy(b.AcceptanceCriteria))
}

func trimListCopy(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = strings.TrimSpace(s)
	}
	return out
}

// GoalVersion is an immutable applied version of the goal's charter.
type GoalVersion struct {
	Number     int     `json:"number"`
	Charter    Charter `json:"charter"`
	ProposalID string  `json:"proposal_id,omitempty"`
	AppliedBy  string  `json:"applied_by,omitempty"`
}

// Clone returns a defensive copy of GoalVersion.
func (v GoalVersion) Clone() GoalVersion {
	return GoalVersion{
		Number:     v.Number,
		Charter:    v.Charter.Clone(),
		ProposalID: v.ProposalID,
		AppliedBy:  v.AppliedBy,
	}
}

// GoalStatus tracks whether the goal is active, paused, or stopped.
type GoalStatus string

const (
	GoalStatusActive  GoalStatus = "active"
	GoalStatusPaused  GoalStatus = "paused"
	GoalStatusStopped GoalStatus = "stopped"
)

// RevisionRef pins an entity by stable ID and immutable 1-based revision number.
type RevisionRef struct {
	ID       string `json:"id"`
	Revision int    `json:"revision"`
}

// Level represents qualitative rating values for prioritization dimensions.
type Level string

const (
	LevelLow    Level = "low"
	LevelMedium Level = "medium"
	LevelHigh   Level = "high"
)

func validateLevel(field string, l Level) error {
	switch l {
	case LevelLow, LevelMedium, LevelHigh:
		return nil
	default:
		return errInvalid("%s must be one of low, medium, high", field)
	}
}

// DecisionKind represents the four goal-level decision outcomes.
type DecisionKind string

const (
	DecisionRetain DecisionKind = "retain"
	DecisionRevise DecisionKind = "revise"
	DecisionPause  DecisionKind = "pause"
	DecisionStop   DecisionKind = "stop"
)

func validateDecisionKind(field string, k DecisionKind) error {
	switch k {
	case DecisionRetain, DecisionRevise, DecisionPause, DecisionStop:
		return nil
	default:
		return errInvalid("%s must be one of retain, revise, pause, stop", field)
	}
}

// State is the serializable domain state of one XShadow goal's seven-step
// problem-solving lifecycle. The zero value represents an uninitialized goal
// (Current().Number == 0).
type State struct {
	GoalVersions      []GoalVersion          `json:"goal_versions,omitempty"`
	GoalStatus        GoalStatus             `json:"goal_status,omitempty"`
	Problems          []Problem              `json:"problems,omitempty"`
	Branches          []Branch               `json:"branches,omitempty"`
	Hypotheses        []Hypothesis           `json:"hypotheses,omitempty"`
	Prioritizations   []PrioritizationRecord `json:"prioritizations,omitempty"`
	Protocols         []ValidationProtocol   `json:"protocols,omitempty"`
	ValidationBatches []ValidationBatch      `json:"validation_batches,omitempty"`
	ValidationResults []ValidationResult     `json:"validation_results,omitempty"`
	Syntheses         []Synthesis            `json:"syntheses,omitempty"`
	Decisions         []GoalDecision         `json:"decisions,omitempty"`
	Proposals         []RevisionProposal     `json:"proposals,omitempty"`
}

// New creates an initialized goal State seeded with initial as goal version 1.
func New(initial Charter) State {
	return State{
		GoalVersions: []GoalVersion{
			{
				Number:  1,
				Charter: initial.Clone(),
			},
		},
		GoalStatus: GoalStatusActive,
	}
}

// Current returns the latest applied GoalVersion, or a zero GoalVersion
// (Number == 0) if the state is uninitialized.
func (s State) Current() GoalVersion {
	if len(s.GoalVersions) == 0 {
		return GoalVersion{}
	}
	return s.GoalVersions[len(s.GoalVersions)-1].Clone()
}

// GoalVersionAt returns the immutable GoalVersion snapshot for number.
func (s State) GoalVersionAt(number int) (GoalVersion, bool) {
	for _, gv := range s.GoalVersions {
		if gv.Number == number {
			return gv.Clone(), true
		}
	}
	return GoalVersion{}, false
}

// Problem returns a defensive copy of the Problem with the given ID.
func (s State) Problem(id string) (Problem, bool) {
	for _, p := range s.Problems {
		if p.ID == id {
			return cloneProblem(p), true
		}
	}
	return Problem{}, false
}

// Branch returns a defensive copy of the Branch with the given ID.
func (s State) Branch(id string) (Branch, bool) {
	for _, b := range s.Branches {
		if b.ID == id {
			return cloneBranch(b), true
		}
	}
	return Branch{}, false
}

// Hypothesis returns a defensive copy of the Hypothesis with the given ID.
func (s State) Hypothesis(id string) (Hypothesis, bool) {
	for _, h := range s.Hypotheses {
		if h.ID == id {
			return cloneHypothesis(h), true
		}
	}
	return Hypothesis{}, false
}

// Protocol returns a defensive copy of the ValidationProtocol with the given ID.
func (s State) Protocol(id string) (ValidationProtocol, bool) {
	for _, p := range s.Protocols {
		if p.ID == id {
			return cloneProtocol(p), true
		}
	}
	return ValidationProtocol{}, false
}

// ValidationBatch returns a copy of the ValidationBatch with the given ID.
func (s State) ValidationBatch(id string) (ValidationBatch, bool) {
	for _, b := range s.ValidationBatches {
		if b.ID == id {
			return b, true
		}
	}
	return ValidationBatch{}, false
}

// ValidationResult returns a defensive copy of the ValidationResult with the given ID.
func (s State) ValidationResult(id string) (ValidationResult, bool) {
	for _, r := range s.ValidationResults {
		if r.ID == id {
			return cloneValidationResult(r), true
		}
	}
	return ValidationResult{}, false
}

// Synthesis returns a defensive copy of the Synthesis with the given ID.
func (s State) Synthesis(id string) (Synthesis, bool) {
	for _, syn := range s.Syntheses {
		if syn.ID == id {
			return cloneSynthesis(syn), true
		}
	}
	return Synthesis{}, false
}

// Decision returns a defensive copy of the GoalDecision with the given ID.
func (s State) Decision(id string) (GoalDecision, bool) {
	for _, d := range s.Decisions {
		if d.ID == id {
			return cloneDecision(d), true
		}
	}
	return GoalDecision{}, false
}

// Proposal returns a defensive copy of the RevisionProposal with the given ID.
func (s State) Proposal(id string) (RevisionProposal, bool) {
	for _, p := range s.Proposals {
		if p.ID == id {
			return cloneProposal(p), true
		}
	}
	return RevisionProposal{}, false
}

// IsResultCurrent reports whether resultID is a reviewed validation result
// whose pinned hypothesis and protocol revisions still match their current
// revisions.
func (s State) IsResultCurrent(resultID string) bool {
	res := s.findValidationResult(resultID)
	if res == nil || res.Review == nil || res.Review.LateForCurrentRevisions {
		return false
	}
	return s.resultMatchesCurrentRevisions(*res)
}

func (s State) resultMatchesCurrentRevisions(res ValidationResult) bool {
	hyp := s.findHypothesis(res.HypothesisRef.ID)
	if hyp == nil || hyp.CurrentRevision != res.HypothesisRef.Revision || !s.hypothesisStructureCurrent(hyp) {
		return false
	}
	proto := s.findProtocol(res.ProtocolRef.ID)
	if proto == nil || proto.CurrentRevision != res.ProtocolRef.Revision {
		return false
	}
	return true
}

// Clone returns a complete deep copy of State so mutations can be executed
// speculatively and discarded cleanly on error.
func (s State) Clone() State {
	out := State{
		GoalStatus: s.GoalStatus,
	}
	if len(s.GoalVersions) > 0 {
		out.GoalVersions = make([]GoalVersion, len(s.GoalVersions))
		for i, gv := range s.GoalVersions {
			out.GoalVersions[i] = gv.Clone()
		}
	}
	if len(s.Problems) > 0 {
		out.Problems = make([]Problem, len(s.Problems))
		for i, p := range s.Problems {
			out.Problems[i] = cloneProblem(p)
		}
	}
	if len(s.Branches) > 0 {
		out.Branches = make([]Branch, len(s.Branches))
		for i, b := range s.Branches {
			out.Branches[i] = cloneBranch(b)
		}
	}
	if len(s.Hypotheses) > 0 {
		out.Hypotheses = make([]Hypothesis, len(s.Hypotheses))
		for i, h := range s.Hypotheses {
			out.Hypotheses[i] = cloneHypothesis(h)
		}
	}
	if len(s.Prioritizations) > 0 {
		out.Prioritizations = slices.Clone(s.Prioritizations)
	}
	if len(s.Protocols) > 0 {
		out.Protocols = make([]ValidationProtocol, len(s.Protocols))
		for i, p := range s.Protocols {
			out.Protocols[i] = cloneProtocol(p)
		}
	}
	if len(s.ValidationBatches) > 0 {
		out.ValidationBatches = slices.Clone(s.ValidationBatches)
	}
	if len(s.ValidationResults) > 0 {
		out.ValidationResults = make([]ValidationResult, len(s.ValidationResults))
		for i, r := range s.ValidationResults {
			out.ValidationResults[i] = cloneValidationResult(r)
		}
	}
	if len(s.Syntheses) > 0 {
		out.Syntheses = make([]Synthesis, len(s.Syntheses))
		for i, syn := range s.Syntheses {
			out.Syntheses[i] = cloneSynthesis(syn)
		}
	}
	if len(s.Decisions) > 0 {
		out.Decisions = make([]GoalDecision, len(s.Decisions))
		for i, d := range s.Decisions {
			out.Decisions[i] = cloneDecision(d)
		}
	}
	if len(s.Proposals) > 0 {
		out.Proposals = make([]RevisionProposal, len(s.Proposals))
		for i, p := range s.Proposals {
			out.Proposals[i] = cloneProposal(p)
		}
	}
	return out
}

func cloneProblem(p Problem) Problem {
	revs := make([]ProblemRevision, len(p.Revisions))
	for i, r := range p.Revisions {
		r.AffectedUsers = slices.Clone(r.AffectedUsers)
		r.Constraints = slices.Clone(r.Constraints)
		revs[i] = r
	}
	p.Revisions = revs
	return p
}

func cloneBranch(b Branch) Branch {
	b.Revisions = slices.Clone(b.Revisions)
	b.Reassessments = slices.Clone(b.Reassessments)
	return b
}

func cloneHypothesis(h Hypothesis) Hypothesis {
	revs := make([]HypothesisRevision, len(h.Revisions))
	for i, r := range h.Revisions {
		r.Assumptions = slices.Clone(r.Assumptions)
		revs[i] = r
	}
	h.Revisions = revs
	return h
}

func cloneProtocol(p ValidationProtocol) ValidationProtocol {
	p.Revisions = slices.Clone(p.Revisions)
	return p
}

func cloneValidationResult(r ValidationResult) ValidationResult {
	r.Observations = slices.Clone(r.Observations)
	r.Evidence = slices.Clone(r.Evidence)
	if r.Review != nil {
		cp := *r.Review
		r.Review = &cp
	}
	return r
}

func cloneSynthesis(syn Synthesis) Synthesis {
	revs := make([]SynthesisRevision, len(syn.Revisions))
	for i, r := range syn.Revisions {
		claims := make([]SynthesisClaim, len(r.Claims))
		for j, c := range r.Claims {
			c.ResultIDs = slices.Clone(c.ResultIDs)
			claims[j] = c
		}
		r.Claims = claims
		r.DependencySnapshots = slices.Clone(r.DependencySnapshots)
		r.Tradeoffs = slices.Clone(r.Tradeoffs)
		r.Limitations = slices.Clone(r.Limitations)
		r.ChallengedBy = slices.Clone(r.ChallengedBy)
		revs[i] = r
	}
	syn.Revisions = revs
	return syn
}

func cloneDecision(d GoalDecision) GoalDecision {
	d.EvidenceResultIDs = slices.Clone(d.EvidenceResultIDs)
	return d
}

func cloneProposal(p RevisionProposal) RevisionProposal {
	p.ProposedCharter = p.ProposedCharter.Clone()
	return p
}

// transact executes fn against a cloned copy of State and commits atomically
// only if fn returns nil, guaranteeing rollback on every error.
func (s *State) transact(actor Actor, fn func(next *State, actorID string) error) error {
	if s == nil || s.Current().Number == 0 {
		return errConflict(ErrUninitialized, "goal state is uninitialized")
	}
	actorID, err := requireNonEmpty("actor.id", actor.ID)
	if err != nil {
		return err
	}
	next := s.Clone()
	if err := fn(&next, actorID); err != nil {
		return err
	}
	*s = next
	return nil
}

// Internal mutable lookup helpers (only called on speculative clones).
func (s *State) findProblem(id string) *Problem {
	for i := range s.Problems {
		if s.Problems[i].ID == id {
			return &s.Problems[i]
		}
	}
	return nil
}

func (s *State) findBranch(id string) *Branch {
	for i := range s.Branches {
		if s.Branches[i].ID == id {
			return &s.Branches[i]
		}
	}
	return nil
}

func (s *State) findHypothesis(id string) *Hypothesis {
	for i := range s.Hypotheses {
		if s.Hypotheses[i].ID == id {
			return &s.Hypotheses[i]
		}
	}
	return nil
}

func (s *State) findPrioritization(id string) *PrioritizationRecord {
	for i := range s.Prioritizations {
		if s.Prioritizations[i].ID == id {
			return &s.Prioritizations[i]
		}
	}
	return nil
}

func (s *State) findProtocol(id string) *ValidationProtocol {
	for i := range s.Protocols {
		if s.Protocols[i].ID == id {
			return &s.Protocols[i]
		}
	}
	return nil
}

func (s *State) findValidationBatch(id string) *ValidationBatch {
	for i := range s.ValidationBatches {
		if s.ValidationBatches[i].ID == id {
			return &s.ValidationBatches[i]
		}
	}
	return nil
}

func (s *State) findValidationResult(id string) *ValidationResult {
	for i := range s.ValidationResults {
		if s.ValidationResults[i].ID == id {
			return &s.ValidationResults[i]
		}
	}
	return nil
}

func (s *State) findValidationResultByBatch(batchID string) *ValidationResult {
	for i := range s.ValidationResults {
		if s.ValidationResults[i].BatchID == batchID {
			return &s.ValidationResults[i]
		}
	}
	return nil
}

func (s *State) findSynthesis(id string) *Synthesis {
	for i := range s.Syntheses {
		if s.Syntheses[i].ID == id {
			return &s.Syntheses[i]
		}
	}
	return nil
}

func (s *State) findDecision(id string) *GoalDecision {
	for i := range s.Decisions {
		if s.Decisions[i].ID == id {
			return &s.Decisions[i]
		}
	}
	return nil
}

func (s *State) findProposal(id string) *RevisionProposal {
	for i := range s.Proposals {
		if s.Proposals[i].ID == id {
			return &s.Proposals[i]
		}
	}
	return nil
}

// Validation helpers.
func requireNonEmpty(field, value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", errInvalid("%s is required", field)
	}
	return trimmed, nil
}

func requireNonEmptyList(field string, values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, errInvalid("%s must contain at least one entry", field)
	}
	out := make([]string, len(values))
	for i, v := range values {
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return nil, errInvalid("%s[%d] must not be blank", field, i)
		}
		out[i] = trimmed
	}
	return out, nil
}

func cleanOptionalList(field string, values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	out := make([]string, len(values))
	for i, v := range values {
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return nil, errInvalid("%s[%d] must not be blank", field, i)
		}
		out[i] = trimmed
	}
	return out, nil
}

func validateRevisionRef(field string, ref RevisionRef) (RevisionRef, error) {
	id, err := requireNonEmpty(field+".id", ref.ID)
	if err != nil {
		return RevisionRef{}, err
	}
	if ref.Revision < 1 {
		return RevisionRef{}, errInvalid("%s.revision must be >= 1", field)
	}
	return RevisionRef{ID: id, Revision: ref.Revision}, nil
}

func parseUUIDField(field, raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", errInvalid("%s is required", field)
	}
	parsed, err := uuid.Parse(trimmed)
	if err != nil {
		return "", errInvalid("%s must be a valid UUID", field)
	}
	return parsed.String(), nil
}
