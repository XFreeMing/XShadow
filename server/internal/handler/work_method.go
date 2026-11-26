package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
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
		if id := delivery.Delivery.TaskTemplateID; id != "" && id != uuidToString(issue.IssueTypeID) {
			return workmethod.Run{State: "ready", Reason: "当前任务的工作方法与目标计划不一致。", History: []workmethod.Event{}}, delivery, nil
		}
		if method, ok := workmethod.ResolvePlanMethod(methods, delivery.Delivery.TaskTemplateID, delivery.Delivery.MethodID); ok {
			return workmethod.Run{
				State: "ready", Source: "plan", Method: &method, History: []workmethod.Event{},
				Reason: "目标计划建议采用「" + method.Name + "」。",
			}, delivery, nil
		}
		if delivery.Delivery.TaskTemplateID != "" {
			return workmethod.Run{State: "ready", Reason: "目标计划绑定的工作方法不可用。", History: []workmethod.Event{}}, delivery, nil
		}
	}
	return workmethod.RecommendForIssueType(methods, uuidToString(issue.IssueTypeID), issue.Title, issue.Description.String, goalText), delivery, nil
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

func developmentReview(trace acceptance.Trace, gate string) error {
	if len(trace.Criteria) == 0 {
		return errors.New("请先登记研发验收标准与用例")
	}
	for _, criterion := range trace.Criteria {
		if gate == "criteria" {
			continue
		}
		if !slices.ContainsFunc(criterion.Cases, func(tc acceptance.TestCase) bool { return tc.Retired == nil }) {
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

func writeBacklogPlanningTransitionForbidden(w http.ResponseWriter) {
	writeErrorCode(w, http.StatusForbidden, "goal_execution_forbidden", "待规划运行可以准备方案和验收草案，正式派工或修改目标执行计划需要另行授权")
}

func (h *Handler) rejectBacklogPlanningTransition(w http.ResponseWriter, r *http.Request) bool {
	task, _ := h.taskFromRequestHeader(r)
	if !service.IsBacklogPlanningTask(task) {
		return false
	}
	writeBacklogPlanningTransitionForbidden(w)
	return true
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
	if change.ProjectLesson != nil && change.Action != "approve" {
		writeError(w, http.StatusBadRequest, "project_lesson is only valid for final approval")
		return
	}
	task, hasSource := h.taskFromRequestHeader(r)
	if service.IsBacklogPlanningTask(task) {
		writeBacklogPlanningTransitionForbidden(w)
		return
	}
	binding := service.ReadGoalExecutionTask(task)
	if binding.ID.Valid && (task.IssueID != issue.ID || (binding.Revision == 0 && change.Action != "start")) {
		writeErrorCode(w, http.StatusForbidden, "goal_execution_forbidden", "目标授权仅用于本事项的当前工作方法步骤")
		return
	}
	if service.IsGoalCandidateOffer(task) && change.Action != workmethod.ClaimCandidate {
		writeError(w, http.StatusForbidden, "candidate assessments can only accept their own offer")
		return
	}
	if change.Action == workmethod.Delegate || change.Action == workmethod.OfferCandidate || change.Action == workmethod.ClaimCandidate {
		h.dispatchGoalCandidate(w, r, issue, task, change)
		return
	}
	if change.DelegateAgentID != "" {
		writeError(w, http.StatusBadRequest, "delegate_agent_id is only valid for candidate delegation")
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
	if delivery != nil && run.State == "ready" && hasSource && task.TriggerEvidenceKind.String == "issue_continuation" && !binding.ID.Valid {
		writeErrorCode(w, http.StatusForbidden, "goal_execution_forbidden", "目标计划需要有效授权才能自动启动工作方法")
		return
	}
	if run.Revision == 0 && issuestatus.Effective(r.Context(), h.Queries, issue.WorkspaceID, issue.Status) == "done" {
		writeError(w, 409, "reopen this issue before starting a work method")
		return
	}
	if change.Action == "select" && (run.Source == "issue_type" || (delivery != nil && delivery.Delivery.TaskTemplateID != "")) {
		writeError(w, http.StatusConflict, "工作方法由任务模板决定，请核对当前任务的模板。")
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
	var closeout *acceptance.Closeout
	var candidateFailure *workmethod.CandidateFailure
	if change.Action == "advance" || change.Action == "approve" {
		if !h.requireWorkMethodTableGate(w, r, issue, run, userID) {
			return
		}
		gate := run.EvidenceGate()
		closing := change.Action == "approve" && run.State == "review" && run.Method != nil && run.Method.Kind == "development" && run.Step == len(run.Method.Steps)-1
		if closing {
			gate = "delivery"
		}
		if gate != "" {
			criteria := issue.AcceptanceCriteria
			if trace != nil {
				criteria = trace
			}
			acceptanceTrace, err := acceptance.Decode(criteria)
			if err == nil {
				err = developmentReview(acceptanceTrace, gate)
				if err == nil && run.RequiresCoveragePreflight() {
					err = run.CheckCoveragePreflight(acceptanceTrace, change.CoveragePreflight)
				}
				if err != nil && gate == "candidate" && change.Action == "advance" && binding.ID.Valid &&
					run.CanDelegateCandidate() && run.Method.Steps[run.Step].Reviewer != nil {
					candidateFailure = workmethod.FailedCandidate(acceptanceTrace)
				}
			}
			if err != nil && candidateFailure == nil {
				writeError(w, 409, err.Error())
				return
			}
			if closing {
				snapshot := acceptanceTrace.Closeout(nil)
				if run.Method.GitHubDelivery != nil {
					if len(snapshot.Criteria) == 0 || !gitHubCommitSHA.MatchString(snapshot.Criteria[0].Candidate) {
						writeError(w, http.StatusConflict, "须提供已复验的完整 GitHub 候选提交")
						return
					}
					head := snapshot.Criteria[0].Candidate
					for _, criterion := range snapshot.Criteria {
						if criterion.Candidate != head {
							writeError(w, http.StatusConflict, "所有交付验收标准须关联同一个完整 GitHub 候选提交")
							return
						}
					}
				}
				closeout = &snapshot
			}
		}
	}
	ws := uuidToString(issue.WorkspaceID)
	actorType, actorID := h.resolveActor(r, userID, ws)
	var lesson *verifiedProjectLesson
	if change.ProjectLesson != nil {
		lesson, err = verifyProjectLesson(issue, run, change.ProjectLesson, actorType+"/"+actorID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if len(change.Findings) > 0 {
		if err := validateReviewFindingReferences(issue.AcceptanceCriteria, change.Findings); err != nil {
			writeError(w, 400, err.Error())
			return
		}
	}
	if err := run.Apply(change, methods, actorType+"/"+actorID, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, workmethod.ErrConflict) || errors.Is(err, workmethod.ErrCandidateFailureApproval) {
			status = http.StatusConflict
		}
		writeError(w, status, err.Error())
		return
	}
	if change.Action == "select" || run.Revision == 1 {
		if err := h.bindProjectDelivery(r.Context(), issue, &run); err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
	}
	if closeout != nil {
		run.History[len(run.History)-1].Closeout = closeout
		if run.ProjectDelivery != nil {
			if _, err := projectDeliveryJob(issue, run); err != nil {
				writeError(w, http.StatusConflict, err.Error())
				return
			}
		}
	}
	if candidateFailure != nil {
		event := &run.History[len(run.History)-1]
		event.CandidateFailure = candidateFailure
		event.Evidence = candidateFailure.Summary() + "交独立评审判定应修订用例还是实现。\n" + event.Evidence
	}
	updated, ok := h.persistMethodTransition(w, r, issue, &run, trace, lesson)
	if !ok {
		return
	}
	resp := issueToResponse(updated, h.getIssuePrefix(r.Context(), issue.WorkspaceID))
	h.publish(protocol.EventIssueUpdated, ws, actorType, actorID, map[string]any{"issue": resp,
		"status_changed": issue.Status != updated.Status, "prev_status": issue.Status,
		"assignee_changed":   issue.AssigneeType != updated.AssigneeType || issue.AssigneeID != updated.AssigneeID,
		"prev_assignee_type": textToPtr(issue.AssigneeType), "prev_assignee_id": uuidToPtr(issue.AssigneeID)})
	if run.State == "closed" && run.Method != nil && run.Method.GitHubDelivery != nil {
		h.maybeAutoCompleteIssue(r.Context(), updated.WorkspaceID, updated.ID, nil)
	}
	h.writeIssueMethodView(w, r, updated, run, delivery)
}

func validateReviewFindingReferences(raw []byte, findings []workmethod.ReviewFinding) error {
	trace, err := acceptance.Decode(raw)
	if err != nil {
		return err
	}
	for _, finding := range findings {
		if finding.CriterionID == "" && finding.CaseID == "" {
			continue
		}
		valid := slices.ContainsFunc(trace.Criteria, func(criterion acceptance.Criterion) bool {
			return criterion.ID == finding.CriterionID && (finding.CaseID == "" ||
				slices.ContainsFunc(criterion.Cases, func(tc acceptance.TestCase) bool {
					return tc.ID == finding.CaseID && tc.Retired == nil
				}))
		})
		if !valid {
			return errors.New("评审发现须引用本事项的现行标准和用例")
		}
	}
	return nil
}
