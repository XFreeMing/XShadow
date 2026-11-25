package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/logger"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type rustIssuePropertyWrite struct {
	SourceTaskID *string `json:"source_task_id,omitempty"`
	Operation    any     `json:"operation"`
}

type rustIssuePropertySet struct {
	Set struct {
		Type   string          `json:"property_type"`
		Config json.RawMessage `json:"config"`
		Value  json.RawMessage `json:"value"`
	} `json:"set"`
}

func (h *Handler) rustIssuePropertyPilot(w http.ResponseWriter, r *http.Request, workspaceID pgtype.UUID) (bool, bool) {
	if h.IssueReadBridge == nil || len(h.IssueReadBridge.WritePilotScopes) == 0 {
		return false, true
	}
	selected, err := matchesIssuePilot(r.Context(), h.Queries, h.IssueReadBridge.WritePilotScopes, uuidToString(workspaceID), workspaceID)
	if err != nil {
		if !writeQueryCancellation(w, r, err) {
			slog.Warn("resolve rust issue property pilot failed", append(logger.RequestAttrs(r), "error", err)...)
			writeError(w, http.StatusInternalServerError, "failed to resolve issue write route")
		}
		return false, false
	}
	return selected, true
}

// retry means Rust explicitly reported that the validated config changed
// before the write. A lost or invalid reply never permits another write here.
func (h *Handler) writeIssuePropertyViaRust(w http.ResponseWriter, r *http.Request, issue db.Issue, propertyID pgtype.UUID, userID string, operation any) (updated db.Issue, retry, ok bool) {
	request := rustIssuePropertyWrite{SourceTaskID: uuidToPtr(h.wakeupSourceTaskID(r)), Operation: operation}
	payload, err := json.Marshal(request)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode issue property write")
		return updated, false, false
	}
	path := "/internal/issues/" + uuidToString(issue.ID) + "/properties/" + uuidToString(propertyID)
	response, ok := h.requestRustIssue(w, r, http.MethodPut, path, uuidToString(issue.WorkspaceID), userID, payload)
	if !ok {
		return updated, false, false
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxRustIssueReadBody+1))
	if err != nil || len(body) > maxRustIssueReadBody {
		if !writeQueryCancellation(w, r, err) {
			writeError(w, http.StatusBadGateway, "failed to read rust issue property response")
		}
		return updated, false, false
	}

	var reply struct {
		Code       string          `json:"code"`
		Name       string          `json:"name"`
		Properties json.RawMessage `json:"properties"`
		Revision   int64           `json:"issue_revision"`
	}
	decodeErr := json.Unmarshal(body, &reply)
	switch response.StatusCode {
	case http.StatusOK:
		if decodeErr != nil || len(reply.Properties) == 0 || reply.Revision < 1 {
			writeError(w, http.StatusBadGateway, "invalid rust issue property response")
			return updated, false, false
		}
		issue.Properties, issue.Revision = reply.Properties, reply.Revision
		return issue, false, true
	case http.StatusConflict:
		if decodeErr == nil && reply.Code == "definition_changed" {
			return updated, true, false
		}
	case http.StatusBadRequest:
		if decodeErr == nil {
			switch reply.Code {
			case "archived":
				writeError(w, http.StatusBadRequest, fmt.Sprintf("property %q is archived and cannot receive new values", reply.Name))
				return updated, false, false
			case "required":
				writeError(w, http.StatusBadRequest, fmt.Sprintf("cannot clear required property for issue type %q", reply.Name))
				return updated, false, false
			}
		}
	}
	writeRustIssueResponse(w, response, bytes.NewReader(body))
	return updated, false, false
}
