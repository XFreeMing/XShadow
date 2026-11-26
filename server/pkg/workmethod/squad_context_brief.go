package workmethod

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/multica-ai/multica/server/pkg/acceptance"
)

type SquadContextBrief struct {
	Objective          string                `json:"objective"`
	MethodRevision     int                   `json:"method_revision"`
	AcceptanceRevision int                   `json:"acceptance_revision"`
	ContractDigest     string                `json:"contract_digest"`
	Criteria           []SquadBriefCriterion `json:"criteria"`
	CandidateCommit    string                `json:"candidate_commit,omitempty"`
	OpenQuestions      string                `json:"open_questions,omitempty"`
}

type SquadBriefCriterion struct {
	ID      string           `json:"id"`
	Version int              `json:"version"`
	Cases   []SquadBriefCase `json:"cases"`
}

type SquadBriefCase struct {
	ID              string `json:"id"`
	Version         int    `json:"version"`
	ArtifactSHA256  string `json:"artifact_sha256,omitempty"`
	Command         string `json:"command,omitempty"`
	BaselineCommit  string `json:"baseline_commit,omitempty"`
	ReferenceCommit string `json:"reference_commit,omitempty"`
}

type squadContractCriterion struct {
	ID      string
	Version int
	Text    string
	Cases   []squadContractCase
}

type squadContractCase struct {
	ID             string
	Version        int
	Specification  string
	ArtifactSHA256 string
}

func squadContract(trace acceptance.Trace) []squadContractCriterion {
	contract := make([]squadContractCriterion, 0, len(trace.Criteria))
	for _, criterion := range trace.Criteria {
		version := len(criterion.Versions)
		entry := squadContractCriterion{
			ID: criterion.ID, Version: version, Text: criterion.Versions[version-1],
			Cases: make([]squadContractCase, 0, len(criterion.Cases)),
		}
		for _, tc := range criterion.Cases {
			if tc.Retired == nil {
				entry.Cases = append(entry.Cases, squadContractCase{
					ID: tc.ID, Version: tc.Version(), Specification: tc.Specification, ArtifactSHA256: tc.ArtifactSHA256,
				})
			}
		}
		sort.Slice(entry.Cases, func(i, j int) bool { return entry.Cases[i].ID < entry.Cases[j].ID })
		contract = append(contract, entry)
	}
	sort.Slice(contract, func(i, j int) bool { return contract[i].ID < contract[j].ID })
	return contract
}

func squadContractDigest(contract []squadContractCriterion) string {
	raw, _ := json.Marshal(contract)
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

// SquadContextBrief freezes the inputs that were available when the receiving
// slot was created. Run evidence stays in the source issue.
func (r Run) SquadContextBrief(objective string, trace acceptance.Trace) *SquadContextBrief {
	contract := squadContract(trace)
	brief := &SquadContextBrief{
		Objective: objective, MethodRevision: max(r.Method.Revision, 1), AcceptanceRevision: trace.Revision,
		ContractDigest: squadContractDigest(contract), Criteria: make([]SquadBriefCriterion, 0, len(contract)),
		CandidateCommit: agreedCandidateCommit(trace),
	}
	type receiptKey struct {
		criterionID      string
		criterionVersion int
		caseID           string
		caseVersion      int
	}
	proofs := make(map[receiptKey]CoveragePreflight)
	for _, event := range r.History {
		for _, proof := range event.CoveragePreflight {
			proofs[receiptKey{proof.CriterionID, proof.CriterionVersion, proof.CaseID, proof.CaseVersion}] = proof
		}
	}
	for _, criterion := range contract {
		entry := SquadBriefCriterion{ID: criterion.ID, Version: criterion.Version, Cases: make([]SquadBriefCase, 0, len(criterion.Cases))}
		for _, tc := range criterion.Cases {
			tcBrief := SquadBriefCase{ID: tc.ID, Version: tc.Version, ArtifactSHA256: tc.ArtifactSHA256}
			if proof, ok := proofs[receiptKey{criterion.ID, criterion.Version, tc.ID, tc.Version}]; ok {
				tcBrief.Command = proof.Command
				tcBrief.BaselineCommit = proof.Baseline.Commit
				if proof.Reference != nil {
					tcBrief.ReferenceCommit = proof.Reference.Commit
				}
			}
			entry.Cases = append(entry.Cases, tcBrief)
		}
		brief.Criteria = append(brief.Criteria, entry)
	}
	event := r.History[len(r.History)-1]
	if event.Action == "rework" {
		brief.OpenQuestions = strings.TrimSpace(event.Evidence)
	}
	return brief
}

func agreedCandidateCommit(trace acceptance.Trace) string {
	var candidate string
	for _, criterion := range trace.Criteria {
		var commit string
		for i := len(criterion.Implementations) - 1; i >= 0; i-- {
			link := criterion.Implementations[i]
			if link.Version == len(criterion.Versions) {
				commit = link.Commit
				break
			}
		}
		if !coverageCommit.MatchString(commit) || (candidate != "" && candidate != commit) {
			return ""
		}
		candidate = commit
	}
	return candidate
}

// CurrentSquadHandoff follows the receiving slot across compatible guidance
// updates. Leaving the step ends the binding to this brief.
func (r Run) CurrentSquadHandoff() *SquadHandoff {
	if r.CurrentActor == nil || r.CurrentActor.Kind != "squad" {
		return nil
	}
	slot := r.SlotRevision()
	index := len(r.History) - 1 - (r.Revision - slot)
	if index < 0 {
		return nil
	}
	event := r.History[index].SquadHandoff
	if event == nil || event.RunRevision != slot || event.ToStep != r.Step || event.ToSquadID != r.CurrentActor.ID {
		return nil
	}
	return event
}

func (b SquadContextBrief) MatchesAcceptance(raw []byte) bool {
	trace, err := acceptance.Decode(raw)
	return err == nil && b.ContractDigest == squadContractDigest(squadContract(trace))
}

func (b SquadContextBrief) note() string {
	var note strings.Builder
	fmt.Fprintf(&note, "Pinned handoff brief: %s\nWork method v%d; acceptance r%d; contract SHA-256 %s.",
		b.Objective, b.MethodRevision, b.AcceptanceRevision, b.ContractDigest)
	if b.CandidateCommit != "" {
		fmt.Fprintf(&note, "\nCandidate at handoff: %s", b.CandidateCommit)
	}
	for _, criterion := range b.Criteria {
		for _, tc := range criterion.Cases {
			ref := fmt.Sprintf("\n%s v%d / %s v%d", criterion.ID, criterion.Version, tc.ID, tc.Version)
			if tc.ArtifactSHA256 != "" {
				ref += " · sample SHA-256 " + tc.ArtifactSHA256
			}
			if tc.Command != "" {
				ref += " · recorded preflight: " + tc.Command
			}
			if note.Len()+len(ref) > 3200 {
				note.WriteString("\nSee the stored handoff brief for the remaining case references.")
				return note.String()
			}
			note.WriteString(ref)
		}
	}
	if b.OpenQuestions != "" {
		note.WriteString("\nSee the stored handoff brief for the recorded rework findings.")
	}
	return note.String()
}
