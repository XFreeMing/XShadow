package handler

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/imagemetadata"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type goalFrame struct {
	Caption string `json:"caption"`
	Src     string `json:"src"`
}

func (h *Handler) GetGoalOutcomeImages(w http.ResponseWriter, r *http.Request) {
	goal, ok := h.loadVisibleGoal(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var frames json.RawMessage
	err := h.DB.QueryRow(r.Context(), `SELECT coalesce(gallery.images,'[]'::jsonb) FROM goal g LEFT JOIN goal_outcome_gallery gallery ON gallery.goal_id=g.id WHERE g.id=$1 AND g.workspace_id=$2 AND g.project_id IS NOT DISTINCT FROM $3`, goal.ID, goal.WorkspaceID, goal.ProjectID).Scan(&frames)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 404, "goal not found")
		return
	}
	if err != nil {
		writeError(w, 500, "failed to load goal outcome images")
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, 200, frames)
}

func (h *Handler) PutGoalOutcomeImages(w http.ResponseWriter, r *http.Request) {
	goal, ok := h.loadVisibleGoal(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	user, ok := requireUserID(w, r)
	if !ok {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 10<<20))
	if err != nil {
		writeError(w, 413, "image gallery too large")
		return
	}
	var frames []goalFrame
	if json.Unmarshal(body, &frames) != nil || frames == nil || len(frames) > 12 {
		writeError(w, 400, "expected up to twelve captioned images")
		return
	}
	for i := range frames {
		f := &frames[i]
		f.Caption = strings.TrimSpace(f.Caption)
		header, encoded, ok := strings.Cut(f.Src, ",")
		if !ok || (header != "data:image/jpeg;base64" && header != "data:image/png;base64") || f.Caption == "" || len(f.Caption) > 800 {
			writeError(w, 400, "expected a caption and a PNG or JPEG image")
			return
		}
		raw, err := base64.StdEncoding.DecodeString(encoded)
		size, valid := imagemetadata.Dimensions(raw)
		if err != nil || !valid || int64(size.Width)*int64(size.Height) > 16000000 || header != "data:"+http.DetectContentType(raw)+";base64" {
			writeError(w, 400, "invalid image")
			return
		}
	}
	ctx := r.Context()
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		writeError(w, 500, "failed to start gallery update")
		return
	}
	defer tx.Rollback(ctx)
	locked, err := h.Queries.WithTx(tx).LockGoalForUpdate(ctx, db.LockGoalForUpdateParams{ID: goal.ID, WorkspaceID: goal.WorkspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 404, "goal not found")
		return
	}
	if err != nil {
		writeError(w, 500, "failed to lock goal")
		return
	}
	ws := uuidToString(locked.WorkspaceID)
	if !h.requireProjectVisible(w, r, ws, locked.ProjectID, http.StatusNotFound, "goal not found") {
		return
	}
	data, err := json.Marshal(frames)
	if err != nil {
		writeError(w, 400, "invalid gallery")
		return
	}
	_, err = tx.Exec(ctx, `INSERT INTO goal_outcome_gallery(goal_id,images) VALUES ($1,$2::jsonb) ON CONFLICT (goal_id) DO UPDATE SET images=excluded.images`, goal.ID, data)
	if err != nil || tx.Commit(ctx) != nil {
		writeError(w, 500, "failed to save gallery")
		return
	}
	actor, id := h.resolveActor(r, user, ws)
	h.publishGoalChanged(ws, actor, id)
	w.WriteHeader(http.StatusNoContent)
}
