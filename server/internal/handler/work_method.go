package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/pkg/acceptance"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/multica-ai/multica/server/pkg/workmethod"
)

func (h *Handler) workMethodCatalog(ctx context.Context, workspaceID pgtype.UUID) ([]workmethod.Template, error) {
	return service.WorkMethodCatalog(ctx, h.Queries, workspaceID)
}

func (h *Handler) ListWorkMethods(w http.ResponseWriter, r *http.Request) {
	ws := h.resolveWorkspaceID(r)
	id, ok := parseUUIDOrBadRequest(w, ws, "workspace id")
	if !ok {
		return
	}
	if _, ok := h.requireWorkspaceMember(w, r, ws, "workspace not found"); !ok {
		return
	}
	methods, err := h.workMethodCatalog(r.Context(), id)
	if err != nil {
		writeError(w, 500, "failed to read work methods")
		return
	}
	writeJSON(w, 200, map[string]any{"methods": methods})
}

func (h *Handler) CreateWorkMethod(w http.ResponseWriter, r *http.Request) {
	ws := h.resolveWorkspaceID(r)
	id, ok := parseUUIDOrBadRequest(w, ws, "workspace id")
	if !ok {
		return
	}
	if _, ok := h.requireWorkspaceMember(w, r, ws, "workspace not found"); !ok {
		return
	}
	var m workmethod.Template
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&m); err != nil {
		writeError(w, 400, "invalid work method definition")
		return
	}
	m.ID = uuid.NewString()
	m.Name = strings.TrimSpace(m.Name)
	if m.Keywords == nil {
		m.Keywords = []string{}
	}
	if err := workmethod.Validate(m); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	for _, builtin := range workmethod.Builtins() {
		if strings.EqualFold(builtin.Name, m.Name) {
			writeError(w, 409, "method name already exists")
			return
		}
	}
	raw, err := json.Marshal(m)
	if err != nil {
		writeError(w, 500, "failed to encode work method")
		return
	}
	count, err := h.Queries.AddWorkMethodTemplate(r.Context(), db.AddWorkMethodTemplateParams{WorkspaceID: id, Name: m.Name, Definition: raw})
	if err != nil {
		writeError(w, 500, "failed to create work method")
		return
	}
	if count == 0 {
		writeError(w, 409, "method name already exists")
		return
	}
	writeJSON(w, 201, m)
}

func (h *Handler) GetGoalWorkMethod(w http.ResponseWriter, r *http.Request) {
	g, ok := h.loadVisibleGoal(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	methods, err := h.workMethodCatalog(r.Context(), g.WorkspaceID)
	if err != nil {
		writeError(w, 500, "failed to read work methods")
		return
	}
	writeJSON(w, 200, workmethod.Recommend(methods, "", "", service.WorkMethodGoalText(g.Title, g.Intent, g.AcceptanceCriteria)))
}

func (h *Handler) issueWorkMethod(ctx context.Context, issue db.Issue, methods []workmethod.Template) (workmethod.Run, error) {
	if len(issue.WorkMethodRun) > 0 {
		var run workmethod.Run
		err := json.Unmarshal(issue.WorkMethodRun, &run)
		return run, err
	}
	goal, err := h.Queries.GetIssueMethodGoal(ctx, db.GetIssueMethodGoalParams{WorkspaceID: issue.WorkspaceID, IssueID: issue.ID})
	var goalText string
	switch {
	case err == nil:
		goalText = service.WorkMethodGoalText(goal.Title, goal.Intent, goal.AcceptanceCriteria)
	case !errors.Is(err, pgx.ErrNoRows):
		return workmethod.Run{}, err
	}
	return workmethod.Recommend(methods, issue.Title, issue.Description.String, goalText), nil
}

func (h *Handler) resolveIssueWorkMethod(ctx context.Context, issue db.Issue) (workmethod.Run, error) {
	if len(issue.WorkMethodRun) > 0 {
		return h.issueWorkMethod(ctx, issue, nil)
	}
	methods, err := h.workMethodCatalog(ctx, issue.WorkspaceID)
	if err != nil {
		return workmethod.Run{}, err
	}
	return h.issueWorkMethod(ctx, issue, methods)
}

func (h *Handler) GetIssueWorkMethod(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	run, err := h.resolveIssueWorkMethod(r.Context(), issue)
	if err != nil {
		writeError(w, 500, "failed to read issue work method")
		return
	}
	writeJSON(w, 200, run)
}

func developmentReview(raw []byte) error {
	trace, err := acceptance.Decode(raw)
	if err != nil {
		return err
	}
	if len(trace.Criteria) == 0 {
		return errors.New("请先登记研发验收标准与用例")
	}
	for _, criterion := range trace.Criteria {
		i := criterion.Impact()
		if i.Uncovered || len(i.Retest)+len(i.Unrun)+len(i.Failed) > 0 || !i.CurrentImplementation {
			return errors.New(criterion.ID + " 的当前版本需有通过的用例和对应的实现提交")
		}
	}
	return nil
}

func (h *Handler) ChangeIssueWorkMethod(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var change workmethod.Change
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&change); err != nil {
		writeError(w, 400, "invalid method change")
		return
	}
	methods, err := h.workMethodCatalog(r.Context(), issue.WorkspaceID)
	if err != nil {
		writeError(w, 500, "failed to read work methods")
		return
	}
	run, err := h.issueWorkMethod(r.Context(), issue, methods)
	if err != nil {
		writeError(w, 500, "failed to read method run")
		return
	}
	if run.Revision == 0 && issuestatus.Effective(r.Context(), h.Queries, issue.WorkspaceID, issue.Status) == "done" {
		writeError(w, 409, "reopen this issue before starting a work method")
		return
	}
	if change.Action == "advance" && run.State == "ready" && change.MethodID != "" && (run.Method == nil || change.MethodID != run.Method.ID) {
		writeError(w, 409, "method recommendation changed; refresh before advancing")
		return
	}
	if change.Action == "advance" && run.State == "ready" {
		change.MethodID = ""
	}
	if change.Action == "approve" && run.Method != nil && run.Method.Kind == "development" {
		if err := developmentReview(issue.AcceptanceCriteria); err != nil {
			writeError(w, 409, err.Error())
			return
		}
	}
	ws := uuidToString(issue.WorkspaceID)
	actorType, actorID := h.resolveActor(r, userID, ws)
	if err := run.Apply(change, methods, actorType+"/"+actorID, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, workmethod.ErrConflict) {
			status = http.StatusConflict
		}
		writeError(w, status, err.Error())
		return
	}
	raw, err := json.Marshal(run)
	if err != nil {
		writeError(w, 500, "failed to encode method run")
		return
	}
	updated, err := wakeupWrite(h, r, func(q *db.Queries) (db.Issue, error) {
		return q.SaveIssueWorkMethodRun(r.Context(), db.SaveIssueWorkMethodRunParams{ID: issue.ID, WorkspaceID: issue.WorkspaceID, IssueRevision: issue.Revision, Run: raw})
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 409, "issue changed; refresh before advancing the method")
		return
	}
	if err != nil {
		writeError(w, 500, "failed to update method run")
		return
	}
	resp := issueToResponse(updated, h.getIssuePrefix(r.Context(), issue.WorkspaceID))
	h.publish(protocol.EventIssueUpdated, ws, actorType, actorID, map[string]any{"issue": resp})
	writeJSON(w, 200, run)
}
