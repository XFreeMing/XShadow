package protocol

// PreparedSkill identifies a workspace skill actually prepared for a run.
type PreparedSkill struct {
	ID          string `json:"id"`
	ContentHash string `json:"content_hash"`
}
