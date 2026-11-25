package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/projectaccess"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// batchOrdinaryIssueEditUsesRust selects member-authored batch updates that only
// touch priority, position, start_date and due_date in a batch-pilot workspace.
// Agent tokens, status/assign/workflow fields and other shapes stay on Go.
func (h *Handler) batchOrdinaryIssueEditUsesRust(r *http.Request, workspaceID pgtype.UUID, rawUpdates map[string]json.RawMessage) (bool, error) {
	if h.IssueReadBridge == nil || len(h.IssueReadBridge.BatchPilotScopes) == 0 {
		return false, nil
	}
	if len(rawUpdates) == 0 {
		return false, nil
	}
	actorType, _ := h.resolveActor(r, requestUserID(r), uuidToString(workspaceID))
	if actorType != "member" {
		return false, nil
	}
	for key := range rawUpdates {
		switch key {
		case "priority", "position", "start_date", "due_date":
		default:
			return false, nil
		}
	}
	return matchesIssuePilot(r.Context(), h.Queries, h.IssueReadBridge.BatchPilotScopes, uuidToString(workspaceID), workspaceID)
}

type ordinaryRustBatchCall struct {
	before, after db.Issue
	outcome       ordinaryRustWriteOutcome
	responseSent  bool
	upstreamResp  *http.Response
	upstreamBody  []byte
}

func (h *Handler) buildBatchOrdinaryIssueEdit(updates UpdateIssueRequest, rawUpdates map[string]json.RawMessage, sourceTaskID pgtype.UUID) (ordinaryIssueEdit, bool) {
	edit := ordinaryIssueEdit{SourceTaskID: uuidToPtr(sourceTaskID)}
	if updates.Priority != nil {
		edit.Priority = updates.Priority
	}
	if updates.Position != nil {
		edit.Position = updates.Position
	}
	if _, touched := rawUpdates["start_date"]; touched {
		if updates.StartDate != nil && *updates.StartDate != "" {
			d, err := util.ParseCalendarDate(*updates.StartDate)
			if err != nil {
				return edit, false
			}
			edit.StartDate = ordinaryDateChange(d)
		} else {
			edit.StartDate = &ordinaryDateEdit{}
		}
	}
	if _, touched := rawUpdates["due_date"]; touched {
		if updates.DueDate != nil && *updates.DueDate != "" {
			d, err := util.ParseCalendarDate(*updates.DueDate)
			if err != nil {
				return edit, false
			}
			edit.DueDate = ordinaryDateChange(d)
		} else {
			edit.DueDate = &ordinaryDateEdit{}
		}
	}
	return edit, true
}

type ordinaryRustWriteOutcome int

const (
	ordinaryRustSkip ordinaryRustWriteOutcome = iota
	ordinaryRustUpdated
	ordinaryRustFatal
)

func (h *Handler) ordinaryIssueEditViaRustSnapshots(
	w http.ResponseWriter,
	r *http.Request,
	userID string,
	workspaceID pgtype.UUID,
	issueID pgtype.UUID,
	edit ordinaryIssueEdit,
) ordinaryRustBatchCall {
	var call ordinaryRustBatchCall
	payload, err := json.Marshal(edit)
	if err != nil {
		call.outcome = ordinaryRustFatal
		return call
	}
	path := "/internal/issues/" + uuidToString(issueID)
	resp, routed := h.requestRustIssue(
		w, r, http.MethodPut, path, uuidToString(workspaceID), userID, payload,
	)
	if !routed {
		call.outcome = ordinaryRustFatal
		call.responseSent = true
		return call
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		call.outcome = ordinaryRustFatal
		call.responseSent = writeQueryCancellation(w, r, err)
		return call
	}
	switch resp.StatusCode {
	case http.StatusNotFound, http.StatusForbidden, http.StatusConflict:
		call.outcome = ordinaryRustSkip
		return call
	case http.StatusOK:
		snapshots, valid := decodeOrdinaryIssueRustSnapshots(issueID, workspaceID, body)
		if !valid {
			call.outcome = ordinaryRustFatal
			return call
		}
		call.before, call.after = snapshots.Before.issue(), snapshots.After.issue()
		call.outcome = ordinaryRustUpdated
		return call
	case http.StatusInternalServerError:
		slog.Warn("batch ordinary issue edit failed", "issue_id", uuidToString(issueID), "status", resp.StatusCode)
		call.outcome = ordinaryRustSkip
		return call
	default:
		call.outcome = ordinaryRustFatal
		call.upstreamResp = resp
		call.upstreamBody = body
		return call
	}
}

func writeOrdinaryRustBatchFatal(w http.ResponseWriter, call ordinaryRustBatchCall) {
	if call.responseSent {
		return
	}
	if call.upstreamResp != nil {
		writeRustIssueResponse(w, call.upstreamResp, bytes.NewReader(call.upstreamBody))
		return
	}
	writeError(w, http.StatusBadGateway, "invalid rust batch issue edit response")
}

func (h *Handler) batchUpdateOrdinaryIssuesViaRust(
	w http.ResponseWriter,
	r *http.Request,
	userID, workspaceID string,
	wsUUID pgtype.UUID,
	issueIDs []string,
	updates UpdateIssueRequest,
	edit ordinaryIssueEdit,
) (updated int, ok bool) {
	fillBatch := h.newStatusCategoryFiller(r.Context(), wsUUID, projectaccess.Hidden{})
	actorType, actorID := h.resolveActor(r, userID, workspaceID)

	for _, issueID := range issueIDs {
		issueUUID, err := util.ParseUUID(issueID)
		if err != nil {
			continue
		}
		call := h.ordinaryIssueEditViaRustSnapshots(w, r, userID, wsUUID, issueUUID, edit)
		switch call.outcome {
		case ordinaryRustSkip:
			continue
		case ordinaryRustFatal:
			writeOrdinaryRustBatchFatal(w, call)
			return updated, false
		case ordinaryRustUpdated:
			prevIssue, issue := call.before, call.after
			prefix := h.getIssuePrefix(r.Context(), issue.WorkspaceID)
			resp := issueToResponse(issue, prefix)
			fillBatch(&resp)
			priorityChanged := updates.Priority != nil && prevIssue.Priority != issue.Priority
			batchPayload := map[string]any{
				"issue":                      resp,
				"assignee_changed":           false,
				"status_changed":             false,
				"priority_changed":           priorityChanged,
				"project_changed":            false,
				"title_changed":              false,
				"prev_title":                 prevIssue.Title,
				"parent_changed":             false,
				"prev_parent_issue_id":       uuidToPtr(prevIssue.ParentIssueID),
				"prev_status":                prevIssue.Status,
				"duplicate_of_issue_id":      liveDuplicateMark(issue.Status, issue.DuplicateOfIssueID),
				"prev_duplicate_of_issue_id": liveDuplicateMark(prevIssue.Status, prevIssue.DuplicateOfIssueID),
			}
			h.publish(protocol.EventIssueUpdated, workspaceID, actorType, actorID, batchPayload)
			updated++
		}
	}
	return updated, true
}
