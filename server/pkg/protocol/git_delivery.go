package protocol

// MergedPullRequestHead identifies the source revision GitHub accepted,
// including for squash merges that do not preserve the head as an ancestor.
type MergedPullRequestHead struct {
	Repository string `json:"repository"`
	Branch     string `json:"branch"`
	SHA        string `json:"sha"`
}
