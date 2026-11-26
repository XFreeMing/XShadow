package handler

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/issueworkflow"
	"github.com/multica-ai/multica/server/internal/service"
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
	for _, task := range []*workmethod.TableTask{m.PlanTask, m.ReworkTask} {
		if task == nil {
			continue
		}
		typeID, seedID := parseUUIDOrInvalid(task.IssueTypeID), parseUUIDOrInvalid(task.SeedPropertyID)
		issueType, err := h.Queries.GetIssueType(r.Context(), db.GetIssueTypeParams{WorkspaceID: id, ID: typeID})
		seed, seedErr := h.Queries.GetIssueProperty(r.Context(), db.GetIssuePropertyParams{WorkspaceID: id, ID: seedID})
		if err != nil || seedErr != nil || issueType.ArchivedAt.Valid || seed.ArchivedAt.Valid || seed.Type != "text" || !issueTypeHasProperty(issueType, seedID) {
			writeError(w, 400, "任务模板须引用当前工作区活动任务类型中的文本属性")
			return
		}
		task.IssueTypeID, task.SeedPropertyID = uuidToString(typeID), uuidToString(seedID)
	}
	for _, step := range m.Steps {
		if gate := step.TableGate; gate != nil {
			typeID, propertyID := parseUUIDOrInvalid(gate.IssueTypeID), parseUUIDOrInvalid(gate.PropertyID)
			issueType, err := h.Queries.GetIssueType(r.Context(), db.GetIssueTypeParams{WorkspaceID: id, ID: typeID})
			if err != nil || issueType.ArchivedAt.Valid || !issueTypeHasProperty(issueType, propertyID) ||
				service.ValidateTaskReadinessFormula(r.Context(), h.Queries, id, propertyID) != nil {
				writeError(w, 400, "流程门槛须引用当前工作区活动任务类型中的同记录公式")
				return
			}
			gate.IssueTypeID, gate.PropertyID = uuidToString(typeID), uuidToString(propertyID)
		}
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
		if t.ID != m.ID {
			if m.PlanTask != nil && t.UsesIssueType(m.PlanTask.IssueTypeID) {
				writeError(w, http.StatusConflict, "该任务类型已绑定另一工作方法")
				return
			}
			for _, step := range m.Steps {
				if step.TableGate != nil && t.UsesIssueType(step.TableGate.IssueTypeID) {
					writeError(w, http.StatusConflict, "该任务类型已绑定另一工作方法")
					return
				}
			}
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
