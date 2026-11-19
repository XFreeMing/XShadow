package handler

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/issueworkflow"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/workmethod"
)

func (h *Handler) CreateWorkMethod(w http.ResponseWriter, r *http.Request) {
	h.saveWorkMethodTemplate(w, r, "")
}

func (h *Handler) UpdateWorkMethod(w http.ResponseWriter, r *http.Request) {
	h.saveWorkMethodTemplate(w, r, chi.URLParam(r, "id"))
}

func (h *Handler) saveWorkMethodTemplate(w http.ResponseWriter, r *http.Request, methodID string) {
	ws := h.resolveWorkspaceID(r)
	id, ok := parseUUIDOrBadRequest(w, ws, "workspace id")
	if !ok {
		return
	}
	if _, ok := h.requireWorkspaceMember(w, r, ws, "workspace not found"); !ok {
		return
	}
	var req struct {
		workmethod.Template
		BaseRevision *int `json:"base_revision"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, 400, "invalid work method definition")
		return
	}

	m := req.Template
	m.Name = strings.TrimSpace(m.Name)
	if m.Keywords == nil {
		m.Keywords = []string{}
	}
	if methodID == "" {
		m.ID = uuid.NewString()
	} else {
		if m.ID != "" && m.ID != methodID {
			writeError(w, 400, "method id must match request path")
			return
		}
		if req.BaseRevision == nil || *req.BaseRevision < 1 {
			writeError(w, 400, "positive base_revision is required")
			return
		}
		m.ID = methodID
	}
	if err := workmethod.Validate(m); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	for _, step := range m.Steps {
		if step.Owner == nil {
			continue
		}
		for _, actor := range []*workmethod.Actor{step.Owner, step.Reviewer} {
			s := issueworkflow.Step{Handler: issueworkflow.Handler{Type: actor.Kind, ID: actor.ID}}
			if status, msg := h.validateHandoffHandler(r.Context(), r, ws, s); status != 0 {
				writeError(w, status, msg)
				return
			}
		}
	}
	ctx := r.Context()
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		writeError(w, 500, "failed to save work method")
		return
	}
	defer tx.Rollback(ctx)
	q := h.Queries.WithTx(tx)
	raw, err := q.LockWorkMethodTemplates(ctx, id)
	if err != nil {
		writeError(w, 500, "failed to read work methods")
		return
	}
	var stored []workmethod.Template
	if err := json.Unmarshal(raw, &stored); err != nil {
		writeError(w, 500, "invalid stored work methods")
		return
	}
	methods := workmethod.Catalog(stored)
	m.Revision = 1
	status := http.StatusCreated
	if methodID != "" {
		i := slices.IndexFunc(methods, func(t workmethod.Template) bool { return t.ID == methodID })
		if i < 0 {
			writeError(w, 404, "work method not found")
			return
		}
		if methods[i].Revision != *req.BaseRevision {
			writeError(w, 409, "work method changed; reload before saving")
			return
		}
		m.Revision = methods[i].Revision + 1
		status = http.StatusOK
	}
	for _, t := range methods {
		if t.ID != m.ID && strings.EqualFold(m.Name, t.Name) {
			writeError(w, 409, "method name already exists")
			return
		}
	}
	if i := slices.IndexFunc(stored, func(t workmethod.Template) bool { return t.ID == m.ID }); i >= 0 {
		stored[i] = m
	} else {
		stored = append(stored, m)
	}
	encoded, err := json.Marshal(stored)
	if err == nil {
		err = q.SaveWorkMethodTemplates(ctx, db.SaveWorkMethodTemplatesParams{WorkspaceID: id, Templates: encoded})
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		writeError(w, 500, "failed to save work method")
		return
	}
	writeJSON(w, status, m)
}
