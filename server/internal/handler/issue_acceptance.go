package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/pkg/acceptance"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func (h *Handler) ChangeIssueAcceptance(w http.ResponseWriter, r *http.Request) {
	r = h.withWakeupActor(r)
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var change acceptance.Change
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&change); err != nil {
		writeError(w, 400, "invalid acceptance change")
		return
	}
	trace, err := acceptance.Decode(issue.AcceptanceCriteria)
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	before := trace.Revision
	if err := trace.Apply(change); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, acceptance.ErrConflict) {
			status = http.StatusConflict
		}
		writeError(w, status, err.Error())
		return
	}
	if before == trace.Revision {
		writeJSON(w, 200, trace)
		return
	}
	raw, err := json.Marshal(trace)
	if err != nil {
		writeError(w, 500, "failed to encode acceptance records")
		return
	}
	updated, err := wakeupWrite(h, r, func(q *db.Queries) (db.Issue, error) {
		return q.UpdateIssueAcceptanceTrace(r.Context(), db.UpdateIssueAcceptanceTraceParams{ID: issue.ID, WorkspaceID: issue.WorkspaceID, Trace: raw, Previous: issue.AcceptanceCriteria})
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 409, acceptance.ErrConflict.Error())
		return
	}
	if err != nil {
		writeError(w, 500, "failed to save acceptance change")
		return
	}
	ws := uuidToString(issue.WorkspaceID)
	actorType, actorID := h.resolveActor(r, userID, ws)
	resp := issueToResponse(updated, h.getIssuePrefix(r.Context(), issue.WorkspaceID))
	resp.AcceptanceTrace = &trace
	h.publish(protocol.EventIssueUpdated, ws, actorType, actorID, map[string]any{"issue": resp})
	writeJSON(w, http.StatusOK, trace)
}
