package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type rustIssueLabelWrite struct {
	SourceTaskID *string `json:"source_task_id,omitempty"`
	Detach       bool    `json:"detach"`
}

func (h *Handler) issueLabelMutationViaRust(
	w http.ResponseWriter,
	r *http.Request,
	issue db.Issue,
	labelID pgtype.UUID,
	userID string,
	detach bool,
) (db.AttachLabelToIssueRow, bool) {
	var zero db.AttachLabelToIssueRow
	request := rustIssueLabelWrite{SourceTaskID: uuidToPtr(h.wakeupSourceTaskID(r)), Detach: detach}
	payload, err := json.Marshal(request)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode issue label write")
		return zero, false
	}
	path := "/internal/issues/" + uuidToString(issue.ID) + "/labels/" + uuidToString(labelID)
	response, ok := h.requestRustIssue(w, r, http.MethodPut, path, uuidToString(issue.WorkspaceID), userID, payload)
	if !ok {
		return zero, false
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxRustIssueReadBody+1))
	if err != nil || len(body) > maxRustIssueReadBody {
		if !writeQueryCancellation(w, r, err) {
			writeError(w, http.StatusBadGateway, "failed to read rust issue label response")
		}
		return zero, false
	}
	if response.StatusCode != http.StatusOK {
		writeRustIssueResponse(w, response, bytes.NewReader(body))
		return zero, false
	}
	var reply struct {
		Changed       *bool  `json:"changed"`
		IssueRevision *int64 `json:"issue_revision"`
	}
	if json.Unmarshal(body, &reply) != nil || reply.Changed == nil || reply.IssueRevision == nil {
		writeError(w, http.StatusBadGateway, "invalid rust issue label response")
		return zero, false
	}
	changed, revision := *reply.Changed, *reply.IssueRevision
	if (changed && revision < 1) || (!changed && revision != 0) {
		writeError(w, http.StatusBadGateway, "invalid rust issue label response")
		return zero, false
	}
	return db.AttachLabelToIssueRow{Changed: changed, IssueRevision: revision}, true
}

func (h *Handler) attachIssueLabelViaRust(w http.ResponseWriter, r *http.Request, issue db.Issue, labelID pgtype.UUID, userID string) (db.AttachLabelToIssueRow, bool) {
	return h.issueLabelMutationViaRust(w, r, issue, labelID, userID, false)
}

func (h *Handler) detachIssueLabelViaRust(w http.ResponseWriter, r *http.Request, issue db.Issue, labelID pgtype.UUID, userID string) (db.AttachLabelToIssueRow, bool) {
	return h.issueLabelMutationViaRust(w, r, issue, labelID, userID, true)
}
