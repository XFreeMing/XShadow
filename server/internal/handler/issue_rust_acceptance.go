package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"reflect"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/pkg/acceptance"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type rustIssueAcceptanceWrite struct {
	IssueRevision int64           `json:"issue_revision"`
	SourceTaskID  *string         `json:"source_task_id,omitempty"`
	Trace         json.RawMessage `json:"trace"`
}

// persistAcceptanceTraceViaRust commits the acceptance trace through the native
// pilot. The pre-mutation `issue` row remains the websocket "before" snapshot; no
// extra Go read is needed after a successful Rust commit.
func (h *Handler) persistAcceptanceTraceViaRust(
	w http.ResponseWriter,
	r *http.Request,
	issue db.Issue,
	userID string,
	trace json.RawMessage,
) (updated db.Issue, ok bool) {
	request := rustIssueAcceptanceWrite{
		IssueRevision: issue.Revision,
		SourceTaskID:  uuidToPtr(h.wakeupSourceTaskID(r)),
		Trace:         trace,
	}
	payload, err := json.Marshal(request)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode acceptance write")
		return updated, false
	}
	path := "/internal/issues/" + uuidToString(issue.ID) + "/acceptance"
	response, routed := h.requestRustIssue(w, r, http.MethodPut, path, uuidToString(issue.WorkspaceID), userID, payload)
	if !routed {
		return updated, false
	}
	defer response.Body.Close()
	// The public request limits a single acceptance change. The saved trace can
	// grow over time and is included in the successful snapshot.
	limit := int64(maxRustIssueReadBody)
	if response.StatusCode == http.StatusOK {
		limit += int64(len(trace))
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || int64(len(body)) > limit {
		if !writeQueryCancellation(w, r, err) {
			writeError(w, http.StatusBadGateway, "failed to read rust acceptance write response")
		}
		return updated, false
	}
	if response.StatusCode != http.StatusOK {
		writeRustIssueResponse(w, response, bytes.NewReader(body))
		return updated, false
	}
	var reply struct {
		After ordinaryIssueSnapshot `json:"after"`
	}
	if json.Unmarshal(body, &reply) != nil || !rustAcceptanceAfterValid(issue, trace, reply.After) {
		writeError(w, http.StatusBadGateway, "invalid rust acceptance write response")
		return updated, false
	}
	return reply.After.issue(), true
}

func rustAcceptanceAfterValid(issue db.Issue, expectedTrace json.RawMessage, after ordinaryIssueSnapshot) bool {
	if after.ID != issue.ID || after.WorkspaceID != issue.WorkspaceID {
		return false
	}
	if after.Revision != issue.Revision+1 || !after.UpdatedAt.Valid || !after.LastActivityAt.Valid {
		return false
	}
	if len(after.AcceptanceCriteria) == 0 {
		return false
	}
	expected, err := acceptance.Decode(expectedTrace)
	if err != nil {
		return false
	}
	saved, err := acceptance.Decode(after.AcceptanceCriteria)
	if err != nil {
		return false
	}
	return reflect.DeepEqual(expected, saved)
}

func (h *Handler) rustIssueAcceptancePilot(w http.ResponseWriter, r *http.Request, workspaceID pgtype.UUID) (bool, bool) {
	if h.IssueReadBridge == nil || len(h.IssueReadBridge.AcceptancePilotScopes) == 0 {
		return false, true
	}
	selected, err := matchesIssuePilot(r.Context(), h.Queries, h.IssueReadBridge.AcceptancePilotScopes, uuidToString(workspaceID), workspaceID)
	if err != nil {
		if !writeQueryCancellation(w, r, err) {
			slog.Warn("resolve rust issue acceptance pilot failed", append(logger.RequestAttrs(r), "error", err)...)
			writeError(w, http.StatusInternalServerError, "failed to resolve issue acceptance route")
		}
		return false, false
	}
	return selected, true
}
