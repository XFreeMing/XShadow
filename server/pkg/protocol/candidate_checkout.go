package protocol

// CandidateCheckout pins a local worktree to the implementation recorded for
// the authorized rework. Its branch is a hint for finding owned prior work.
type CandidateCheckout struct {
	Repository string `json:"repository"`
	Branch     string `json:"branch"`
	Commit     string `json:"commit"`
}
