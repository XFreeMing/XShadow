package skill

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

type VersionFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// VersionHash uses the ordered supporting files in the persisted version snapshot.
func VersionHash(name, description, content string, files []VersionFile) (string, error) {
	payload, err := json.Marshal(struct {
		Name        string        `json:"name"`
		Description string        `json:"description"`
		Content     string        `json:"content"`
		Files       []VersionFile `json:"files"`
	}{name, description, content, files})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}
