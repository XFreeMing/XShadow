package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"time"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type rustIssueMetadataWrite struct {
	SourceTaskID *string         `json:"source_task_id,omitempty"`
	Value        json.RawMessage `json:"value,omitempty"`
}

func (h *Handler) writeIssueMetadataViaRust(w http.ResponseWriter, r *http.Request, issue db.Issue, issueID, userID, key string, value json.RawMessage) {
	op := "set"
	if len(value) == 0 {
		op = "delete"
	}
	request := rustIssueMetadataWrite{SourceTaskID: uuidToPtr(h.wakeupSourceTaskID(r)), Value: value}
	payload, err := json.Marshal(request)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode issue metadata write")
		return
	}
	path := "/internal/issues/" + uuidToString(issue.ID) + "/metadata/" + key
	started := time.Now()
	response, ok := h.requestRustIssue(w, r, http.MethodPut, path, uuidToString(issue.WorkspaceID), userID, payload)
	if !ok {
		h.recordIssueMetadataMutation(r, op, "error", issueID, key, time.Since(started))
		return
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxRustIssueReadBody+1))
	duration := time.Since(started)
	if err != nil || len(body) > maxRustIssueReadBody {
		h.recordIssueMetadataMutation(r, op, "error", issueID, key, duration)
		if !writeQueryCancellation(w, r, err) {
			writeError(w, http.StatusBadGateway, "failed to read rust issue metadata response")
		}
		return
	}
	if response.StatusCode != http.StatusOK {
		result := "error"
		if response.StatusCode == http.StatusNotFound {
			result = "not_found"
		}
		h.recordIssueMetadataMutation(r, op, result, issueID, key, duration)
		writeRustIssueResponse(w, response, bytes.NewReader(body))
		return
	}
	var reply struct {
		Metadata json.RawMessage `json:"metadata"`
		Revision int64           `json:"issue_revision"`
		Changed  *bool           `json:"changed"`
	}
	if json.Unmarshal(body, &reply) != nil || len(reply.Metadata) == 0 || reply.Revision < 1 || reply.Changed == nil {
		h.recordIssueMetadataMutation(r, op, "error", issueID, key, duration)
		writeError(w, http.StatusBadGateway, "invalid rust issue metadata response")
		return
	}
	if !*reply.Changed {
		h.recordIssueMetadataMutation(r, op, "noop", issueID, key, duration)
		writeJSON(w, http.StatusOK, map[string]any{
			"metadata": parseIssueMetadata(reply.Metadata), "issue_revision": reply.Revision,
		})
		return
	}
	h.recordIssueMetadataMutation(r, op, "changed", issueID, key, duration)
	h.writeIssueMetadataChange(w, r, userID, issue.ID, issue.WorkspaceID, reply.Metadata, reply.Revision)
}
