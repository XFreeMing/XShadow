package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
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

func (h *Handler) issueWorkMethod(ctx context.Context, issue db.Issue, methods []workmethod.Template) (workmethod.Run, *issuePlanContract, error) {
	goal, goalErr := h.Queries.GetIssueMethodGoal(ctx, db.GetIssueMethodGoalParams{WorkspaceID: issue.WorkspaceID, IssueID: issue.ID})
	if goalErr != nil && !errors.Is(goalErr, pgx.ErrNoRows) {
		return workmethod.Run{}, nil, goalErr
	}
	var delivery *issuePlanContract
	var goalText string
	if goalErr == nil {
		var err error
		delivery, err = h.issuePlanDelivery(ctx, issue, uuidToString(goal.ID))
		if err != nil {
			return workmethod.Run{}, nil, err
		}
		goalText = service.WorkMethodGoalText(goal.Title, goal.Intent, goal.AcceptanceCriteria)
	}

	if len(issue.WorkMethodRun) > 0 {
		var run workmethod.Run
		if err := json.Unmarshal(issue.WorkMethodRun, &run); err != nil {
			return run, nil, err
		}
		if run.State != "ready" {
			if run.Method != nil && run.Method.Revision == 0 {
				run.Method.Revision = 1
			}
			return run, delivery, nil
		}
	}

	if methods == nil {
		var err error
		methods, err = h.workMethodCatalog(ctx, issue.WorkspaceID)
		if err != nil {
			return workmethod.Run{}, nil, err
		}
	}
	if delivery != nil {
		for _, method := range methods {
			if method.ID == delivery.Delivery.MethodID {
				return workmethod.Run{
					State: "ready", Source: "plan", Method: &method, History: []workmethod.Event{},
					Reason: "目标计划建议采用「" + method.Name + "」。",
				}, delivery, nil
			}
		}
	}
	return workmethod.Recommend(methods, issue.Title, issue.Description.String, goalText), delivery, nil
}

func (h *Handler) resolveIssueWorkMethod(ctx context.Context, issue db.Issue) (workmethod.Run, error) {
	run, _, err := h.issueWorkMethod(ctx, issue, nil)
	return run, err
}

func (h *Handler) GetIssueWorkMethod(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	run, delivery, err := h.issueWorkMethod(r.Context(), issue, nil)
	if err != nil {
		writeError(w, 500, "failed to read issue work method")
		return
	}
	h.writeIssueMethodView(w, r, issue, run, delivery)
}

func developmentReview(raw []byte, gate string) error {
	trace, err := acceptance.Decode(raw)
	if err != nil {
		return err
	}
	if len(trace.Criteria) == 0 {
		return errors.New("请先登记研发验收标准与用例")
	}
	for _, criterion := range trace.Criteria {
		if gate == "criteria" {
			continue
		}
		if len(criterion.Cases) == 0 {
			return errors.New(criterion.ID + " 缺少关联测试用例")
		}
		if gate == "coverage" {
			continue
		}
		i := criterion.Impact()
		if i.Uncovered || len(i.Retest)+len(i.Unrun)+len(i.Failed) > 0 || !i.CurrentImplementation {
			return errors.New(criterion.ID + " 的每条用例均须在当前标准版本的候选提交上通过，请核对实现分支、提交和实测证据")
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
	if change.Action == workmethod.AdoptRevision {
		h.adoptIssueMethod(w, r, issue, userID, change)
		return
	}
	methods, err := h.workMethodCatalog(r.Context(), issue.WorkspaceID)
	if err != nil {
		writeError(w, 500, "failed to read work methods")
		return
	}
	run, delivery, err := h.issueWorkMethod(r.Context(), issue, methods)
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
	if change.BaseRevision != nil && *change.BaseRevision != run.Revision {
		writeError(w, 409, workmethod.ErrConflict.Error())
		return
	}
	if !(run.State == "ready" && (change.Action == "select" || change.Action == "start")) {
		recipient, err := h.methodRecipient(r.Context(), issue, run)
		if err != nil {
			writeError(w, 409, err.Error())
			return
		}
		if run.State != "closed" && !h.canActOnMethod(r, issue, run, recipient) {
			writeError(w, 403, "只有本步骤的当前负责人可以执行此操作")
			return
		}
	}
	var trace []byte
	if delivery != nil && run.State == "ready" && (change.Action == "start" || change.Action == "select" || change.Action == "advance") {
		trace, err = plannedAcceptanceTrace(issue, delivery.Delivery)
		if err != nil {
			writeError(w, 500, "failed to initialize planned acceptance criteria")
			return
		}
	}
	if change.Action == "advance" || change.Action == "approve" {
		gate := run.EvidenceGate()
		if change.Action == "approve" && run.State == "review" && run.Method != nil && run.Method.Kind == "development" && run.Step == len(run.Method.Steps)-1 {
			gate = "delivery"
		}
		if gate != "" {
			criteria := issue.AcceptanceCriteria
			if trace != nil {
				criteria = trace
			}
			if err := developmentReview(criteria, gate); err != nil {
				writeError(w, 409, err.Error())
				return
			}
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
	updated, ok := h.persistMethodTransition(w, r, issue, &run, trace)
	if !ok {
		return
	}
	resp := issueToResponse(updated, h.getIssuePrefix(r.Context(), issue.WorkspaceID))
	h.publish(protocol.EventIssueUpdated, ws, actorType, actorID, map[string]any{"issue": resp,
		"status_changed": issue.Status != updated.Status, "prev_status": issue.Status,
		"assignee_changed":   issue.AssigneeType != updated.AssigneeType || issue.AssigneeID != updated.AssigneeID,
		"prev_assignee_type": textToPtr(issue.AssigneeType), "prev_assignee_id": uuidToPtr(issue.AssigneeID)})
	h.writeIssueMethodView(w, r, updated, run, delivery)
}
