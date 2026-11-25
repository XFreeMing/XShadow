package storage

import (
	"fmt"
	"path"
	"strings"

	"github.com/google/uuid"
)

// UploadKeyAdapter maps between persisted URLs and storage object keys.
type UploadKeyAdapter interface {
	KeyFromURL(rawURL string) string
	ObjectURL(key string) string
}

// ManagedWorkspaceUploadKey derives the canonical first-party object key for a
// workspace attachment when KeyFromURL(storedURL) and ObjectURL(key) round-trip
// with the configured adapter.
func ManagedWorkspaceUploadKey(store UploadKeyAdapter, workspaceID, attachmentID, storedURL string) (string, bool) {
	if store == nil || storedURL == "" || workspaceID == "" || attachmentID == "" {
		return "", false
	}
	if _, err := uuid.Parse(workspaceID); err != nil {
		return "", false
	}
	if _, err := uuid.Parse(attachmentID); err != nil {
		return "", false
	}
	derived := strings.TrimSpace(store.KeyFromURL(storedURL))
	if derived == "" || !ScopedWorkspaceAttachmentKeyValid(workspaceID, attachmentID, derived) {
		return "", false
	}
	if store.ObjectURL(derived) != storedURL {
		return "", false
	}
	return derived, true
}

// ScopedWorkspaceAttachmentKeyValid checks workspaces/<ws>/<attachment> or
// workspaces/<ws>/<attachment>.<single-ext> with no extra path segments.
func ScopedWorkspaceAttachmentKeyValid(workspaceID, attachmentID, key string) bool {
	prefix := "workspaces/" + workspaceID + "/"
	if !strings.HasPrefix(key, prefix) {
		return false
	}
	rest := strings.TrimPrefix(key, prefix)
	if rest == attachmentID {
		return true
	}
	dot := attachmentID + "."
	if !strings.HasPrefix(rest, dot) {
		return false
	}
	ext := strings.TrimPrefix(rest, dot)
	if ext == "" {
		return false
	}
	for _, c := range ext {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// ScopedSourceContextCloneKeyValid checks workspaces/<ws>/source-context/<clone>
// or workspaces/<ws>/source-context/<clone>.<single-ext> with no extra segments.
func ScopedSourceContextCloneKeyValid(workspaceID, cloneID, key string) bool {
	prefix := fmt.Sprintf("workspaces/%s/source-context/%s", workspaceID, cloneID)
	if key == prefix {
		return true
	}
	dot := prefix + "."
	if !strings.HasPrefix(key, dot) {
		return false
	}
	ext := strings.TrimPrefix(key, dot)
	if ext == "" {
		return false
	}
	for _, c := range ext {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// ManagedSourceContextCloneKey derives the canonical first-party clone object key
// when KeyFromURL(storedURL) and ObjectURL(key) round-trip with the adapter.
func ManagedSourceContextCloneKey(store UploadKeyAdapter, workspaceID, cloneID, filename, storedURL string) (string, bool) {
	if store == nil || storedURL == "" || workspaceID == "" || cloneID == "" {
		return "", false
	}
	if _, err := uuid.Parse(workspaceID); err != nil {
		return "", false
	}
	if _, err := uuid.Parse(cloneID); err != nil {
		return "", false
	}
	derived := strings.TrimSpace(store.KeyFromURL(storedURL))
	if derived == "" || !ScopedSourceContextCloneKeyValid(workspaceID, cloneID, derived) {
		return "", false
	}
	if store.ObjectURL(derived) != storedURL {
		return "", false
	}
	canonical := fmt.Sprintf("workspaces/%s/source-context/%s%s", workspaceID, cloneID, path.Ext(filename))
	if derived != canonical {
		return "", false
	}
	return derived, true
}
