package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func (h *Handler) batchParentIssueEditUsesRust(
	r *http.Request,
	workspaceID pgtype.UUID,
	rawUpdates map[string]json.RawMessage,
) (bool, error) {
	if h.IssueReadBridge == nil || len(h.IssueReadBridge.BatchPilotScopes) == 0 {
		return false, nil
	}
	actorType, _ := h.resolveActor(r, requestUserID(r), uuidToString(workspaceID))
	if actorType != "member" {
		return false, nil
	}
	_, touchedParent := rawUpdates["parent_issue_id"]
	if !touchedParent {
		return false, nil
	}
	for key := range rawUpdates {
		switch key {
		case "parent_issue_id":
		default:
			return false, nil
		}
	}
	return matchesIssuePilot(r.Context(), h.Queries, h.IssueReadBridge.BatchPilotScopes, uuidToString(workspaceID), workspaceID)
}

type batchParentRustOutcome int

const (
	batchParentRustDone batchParentRustOutcome = iota
	batchParentRustAbort
	batchParentRustSkipRow
)

func (h *Handler) tryBatchUpdateIssueParentViaRust(
	w http.ResponseWriter,
	r *http.Request,
	userID string,
	prev db.Issue,
	params db.UpdateIssueParams,
	rawUpdates map[string]json.RawMessage,
) (batchParentRustOutcome, rustParentWriteResult) {
	edit, editOK := h.buildIssueParentEdit(r, &prev.Revision, params, rawUpdates, true, prev.ProjectID)
	if !editOK {
		return batchParentRustSkipRow, rustParentWriteResult{}
	}
	payload, err := json.Marshal(edit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode issue parent edit")
		return batchParentRustAbort, rustParentWriteResult{}
	}
	path := "/internal/issues/" + uuidToString(prev.ID) + "/parent"
	resp, requested := h.requestRustIssue(w, r, http.MethodPut, path, uuidToString(prev.WorkspaceID), userID, payload)
	if !requested {
		return batchParentRustAbort, rustParentWriteResult{}
	}
	limit := int64(maxRustIssueReadBody) * 2
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	_ = resp.Body.Close()
	if err != nil || int64(len(body)) > limit {
		if writeQueryCancellation(w, r, err) {
			return batchParentRustAbort, rustParentWriteResult{}
		}
		writeError(w, http.StatusBadGateway, "failed to read rust issue parent response")
		return batchParentRustAbort, rustParentWriteResult{}
	}
	switch resp.StatusCode {
	case http.StatusNotFound, http.StatusForbidden, http.StatusBadRequest:
		return batchParentRustSkipRow, rustParentWriteResult{}
	case http.StatusConflict:
		var conflict struct {
			ActualRevision int64 `json:"actual_revision"`
		}
		if json.Unmarshal(body, &conflict) == nil && conflict.ActualRevision > 0 {
			return batchParentRustSkipRow, rustParentWriteResult{}
		}
		writeError(w, http.StatusBadGateway, "invalid rust issue parent revision conflict")
		return batchParentRustAbort, rustParentWriteResult{}
	case http.StatusOK:
		receipt, receiptErr := h.validateRustParentWriteReceipt(prev, &prev.Revision, params.ParentIssueID, body)
		if receiptErr != nil {
			writeError(w, http.StatusBadGateway, receiptErr.Error())
			return batchParentRustAbort, rustParentWriteResult{}
		}
		if !receipt.changed {
			return batchParentRustSkipRow, rustParentWriteResult{}
		}
		return batchParentRustDone, receipt
	default:
		writeRustIssueResponse(w, resp, bytes.NewReader(body))
		return batchParentRustAbort, rustParentWriteResult{}
	}
}
