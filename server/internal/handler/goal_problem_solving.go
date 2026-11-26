package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/issueposition"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/issueworkflow"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/goalproblem"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type goalProblemResponse struct {
	GoalID           string            `json:"goal_id"`
	Revision         int32             `json:"revision"`
	ActorID          string            `json:"actor_id"`
	CanDecide        bool              `json:"can_decide"`
	CurrentResultIDs []string          `json:"current_result_ids"`
	State            goalproblem.State `json:"state"`
}

type goalProblemCommand struct {
	ExpectedRevision *int32              `json:"expected_revision"`
	Command          goalproblem.Command `json:"command"`
	NewTaskTitle     string              `json:"new_task_title,omitempty"`
}

func goalProblemCharter(goal db.Goal) goalproblem.Charter {
	var criteria []string
	if goal.AcceptanceCriteria != "" {
		criteria = []string{goal.AcceptanceCriteria}
	}
	return goalproblem.Charter{
		Title: goal.Title, Intent: goal.Intent, Scope: goal.Scope, AcceptanceCriteria: criteria,
	}
}

func readGoalProblem(ctx context.Context, q *db.Queries, goal db.Goal) (goalProblemResponse, error) {
	view := goalProblemResponse{GoalID: uuidToString(goal.ID)}
	row, err := q.GetGoalProblemSolving(ctx, db.GetGoalProblemSolvingParams{GoalID: goal.ID, WorkspaceID: goal.WorkspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		view.State = goalproblem.New(goalProblemCharter(goal))
	} else if err != nil {
		return view, err
	} else {
		view.Revision = row.Revision
		if err := json.Unmarshal(row.State, &view.State); err != nil {
			return view, err
		}
	}
	switch goal.Status {
	case "paused":
		view.State.GoalStatus = goalproblem.GoalStatusPaused
	case "cancelled":
		view.State.GoalStatus = goalproblem.GoalStatusStopped
	default:
		view.State.GoalStatus = goalproblem.GoalStatusActive
	}
	return view, nil
}

func (h *Handler) goalProblemActor(r *http.Request, member db.Member, goal db.Goal) goalproblem.Actor {
	kind, id := h.resolveActor(r, requestUserID(r), uuidToString(goal.WorkspaceID))
	return goalproblem.Actor{
		ID: kind + "/" + id,
		CanDecide: kind == "member" && !isMachineCredentialActor(r) &&
			service.CanManageGoalExecution(member, goal),
	}
}

func (view *goalProblemResponse) setActorAndCurrentResults(actor goalproblem.Actor) {
	view.ActorID, view.CanDecide = actor.ID, actor.CanDecide
	view.CurrentResultIDs = []string{}
	for _, result := range view.State.ValidationResults {
		if view.State.IsResultCurrent(result.ID) {
			view.CurrentResultIDs = append(view.CurrentResultIDs, result.ID)
		}
	}
}

func (h *Handler) GetGoalProblemSolving(w http.ResponseWriter, r *http.Request) {
	goal, ok := h.loadVisibleGoal(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	member, ok := h.workspaceMember(w, r, uuidToString(goal.WorkspaceID))
	if !ok {
		return
	}
	view, err := readGoalProblem(r.Context(), h.Queries, goal)
	if err != nil {
		h.writeGoalWriteError(w, r, err, "read problem solving")
		return
	}
	view.setActorAndCurrentResults(h.goalProblemActor(r, member, goal))
	writeJSON(w, http.StatusOK, view)
}

func (h *Handler) ChangeGoalProblemSolving(w http.ResponseWriter, r *http.Request) {
	goal, ok := h.loadVisibleGoal(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	workspace := uuidToString(goal.WorkspaceID)
	member, ok := h.workspaceMember(w, r, workspace)
	if !ok {
		return
	}
	var request goalProblemCommand
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&request); err != nil || request.ExpectedRevision == nil ||
		*request.ExpectedRevision < 0 || dec.Decode(new(json.RawMessage)) != io.EOF {
		writeError(w, http.StatusBadRequest, "a problem-solving command and expected_revision are required")
		return
	}
	taskTitle := strings.TrimSpace(request.NewTaskTitle)
	batch := request.Command.StartValidationBatch
	if taskTitle != "" && (request.Command.Action != goalproblem.ActionStartValidationBatch || batch == nil ||
		batch.Task.IssueID != "" || utf8.RuneCountInString(taskTitle) > goalPlanMaxTitleRunes) {
		writeError(w, http.StatusBadRequest, "provide a task title or link an existing task when starting a validation batch")
		return
	}
	ctx := r.Context()
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		h.writeGoalWriteError(w, r, err, "start problem-solving update")
		return
	}
	defer tx.Rollback(ctx)
	q := h.Queries.WithTx(tx)
	var newIssueID pgtype.UUID
	if taskTitle != "" {
		if _, err := q.LockWorkspaceForChatSessionCreate(ctx, goal.WorkspaceID); err != nil {
			h.writeGoalWriteError(w, r, err, "lock verification task workspace")
			return
		}
		newIssueID = dbid.NewV7()
	}
	goal, err = q.LockGoalForUpdate(ctx, db.LockGoalForUpdateParams{ID: goal.ID, WorkspaceID: goal.WorkspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "goal not found")
		return
	}
	if err != nil {
		h.writeGoalWriteError(w, r, err, "lock problem-solving goal")
		return
	}
	if !h.requireProjectVisible(w, r, workspace, goal.ProjectID, http.StatusNotFound, "goal not found") {
		return
	}
	view, err := readGoalProblem(ctx, q, goal)
	if err != nil {
		h.writeGoalWriteError(w, r, err, "read problem solving")
		return
	}
	if view.Revision != *request.ExpectedRevision {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "the goal's problem-solving record has changed", "code": "goal_problem_revision_conflict",
			"current_revision": view.Revision,
		})
		return
	}
	actor := h.goalProblemActor(r, member, goal)
	kind, id := h.resolveActor(r, requestUserID(r), workspace)
	before, beforeStatus := view.State.Current(), view.State.GoalStatus
	if newIssueID.Valid {
		batch.Task = goalproblem.TaskRef{IssueID: uuidToString(newIssueID)}
	} else if !h.checkGoalProblemTasks(w, r, q, goal, view.State, &request.Command) {
		return
	}
	if err := view.State.Apply(request.Command, actor); err != nil {
		writeGoalProblemDomainError(w, err)
		return
	}
	var newIssue db.Issue
	if newIssueID.Valid {
		newIssue, err = h.createGoalValidationIssue(ctx, tx, q, goal, view.State, *batch, newIssueID, taskTitle, kind, id)
		if err != nil {
			writeGoalProblemDomainError(w, err)
			return
		}
	}
	after := view.State.Current()
	if err := applyGoalProblemDecision(ctx, tx, q, goal, after.Charter, before.Number != after.Number, beforeStatus, view.State.GoalStatus); err != nil {
		h.writeGoalWriteError(w, r, err, "apply goal decision")
		return
	}
	view.Revision++
	data, err := json.Marshal(view.State)
	if err == nil {
		err = q.SaveGoalProblemSolving(ctx, db.SaveGoalProblemSolvingParams{
			WorkspaceID: goal.WorkspaceID, GoalID: goal.ID, Revision: view.Revision, State: data,
		})
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		h.writeGoalWriteError(w, r, err, "save problem solving")
		return
	}
	view.setActorAndCurrentResults(actor)
	if newIssue.ID.Valid {
		h.publish(protocol.EventIssueCreated, workspace, kind, id, map[string]any{
			"issue": issueToResponse(newIssue, h.getIssuePrefix(ctx, goal.WorkspaceID)),
		})
	}
	h.publishGoalChanged(workspace, kind, id)
	writeJSON(w, http.StatusOK, view)
}

func (h *Handler) createGoalValidationIssue(ctx context.Context, tx pgx.Tx, q *db.Queries, goal db.Goal,
	state goalproblem.State, batch goalproblem.StartValidationBatchInput, issueID pgtype.UUID, title, actorKind, actorID string) (db.Issue, error) {
	var empty db.Issue
	hypothesis, _ := state.Hypothesis(batch.HypothesisRef.ID)
	protocol, _ := state.Protocol(batch.ProtocolRef.ID)
	target := hypothesis.Revisions[batch.HypothesisRef.Revision-1]
	method := protocol.Revisions[batch.ProtocolRef.Revision-1]
	workspace, err := q.GetWorkspace(ctx, goal.WorkspaceID)
	if err != nil {
		return empty, err
	}
	workflow, _, err := issueworkflow.ForProject(ctx, q, goal.WorkspaceID, goal.ProjectID)
	if err != nil {
		return empty, err
	}
	if err := issueworkflow.CheckStatus(workflow, issuestatus.Backlog); err != nil {
		return empty, fmt.Errorf("%w: %s", goalproblem.ErrConflict, err)
	}
	policy := service.ResolveIssueCountPolicy(ctx, h.Entitlements, goal.WorkspaceID)
	number, err := service.AllocateIssueNumber(ctx, q, goal.WorkspaceID, policy)
	if err != nil {
		return empty, err
	}
	position, err := issueposition.NextTopPosition(ctx, tx, goal.WorkspaceID, issuestatus.Backlog)
	if err != nil {
		return empty, err
	}
	description := fmt.Sprintf(`## 假设验证

%s

- [目标与验证记录](/%s/goals/%s#goal-problem-solving)
- 验证批次：%s
- 假设版本：%s v%d
- 验证方案：%s v%d
- 证伪条件：%s
- 预期观察：%s
- 固定样本与来源：%s
- 对照：%s
- 判定阈值：%s
- 停止条件：%s

把实际观察和可复核证据回传到目标的「证据分析」。CLI 可用 multica goal solve %s 读取本批次。`,
		target.Statement, url.PathEscape(workspace.Slug), uuidToString(goal.ID), batch.BatchID,
		hypothesis.ID, target.Revision, protocol.ID, method.Revision, target.FalsificationCriterion,
		method.PredictedResult, method.SampleSource, method.ControlOrComparison, method.DecisionThreshold, method.StopCondition,
		uuidToString(goal.ID))
	created, err := q.CreateIssue(ctx, db.CreateIssueParams{
		ID: issueID, WorkspaceID: goal.WorkspaceID, ProjectID: goal.ProjectID, Number: number,
		Title: title, Description: pgtype.Text{String: description, Valid: true},
		Status: issuestatus.Backlog, Priority: "none", Position: position,
		CreatorType: actorKind, CreatorID: parseUUIDOrInvalid(actorID),
	})
	if err != nil {
		return empty, err
	}
	_, err = q.LinkGoalIssue(ctx, db.LinkGoalIssueParams{WorkspaceID: goal.WorkspaceID, GoalID: goal.ID, IssueID: issueID})
	return created, err
}

func applyGoalProblemDecision(ctx context.Context, tx pgx.Tx, q *db.Queries, goal db.Goal, next goalproblem.Charter, charterChanged bool, before, after goalproblem.GoalStatus) error {
	if !charterChanged && before == after {
		return nil
	}
	text := func(value string) pgtype.Text { return pgtype.Text{String: value, Valid: true} }
	params := db.UpdateGoalParams{ID: goal.ID, WorkspaceID: goal.WorkspaceID}
	if charterChanged {
		params.Title = text(next.Title)
		params.IntentSet, params.Intent = true, text(next.Intent)
		params.ScopeSet, params.Scope = true, text(next.Scope)
		params.AcceptanceCriteriaSet, params.AcceptanceCriteria = true, text(strings.Join(next.AcceptanceCriteria, "\n"))
		if goal.Status == "completed" {
			params.Status = text("in_progress")
		}
	}
	if before != after {
		switch after {
		case goalproblem.GoalStatusPaused:
			params.Status = text("paused")
		case goalproblem.GoalStatusStopped:
			params.Status = text("cancelled")
		case goalproblem.GoalStatusActive:
			params.Status = text("in_progress")
		}
	}
	updated, err := q.UpdateGoal(ctx, params)
	if err != nil {
		return err
	}
	if !charterChanged {
		return q.InvalidateGoalPlanExecution(ctx, db.InvalidateGoalPlanExecutionParams{WorkspaceID: goal.WorkspaceID, GoalID: goal.ID})
	}
	if err := q.AdvanceGoalPlanForCharter(ctx, db.AdvanceGoalPlanForCharterParams{WorkspaceID: goal.WorkspaceID, GoalID: goal.ID}); err != nil {
		return err
	}
	return service.RefreshGoalWorkMethods(ctx, tx, q, updated, service.WorkMethodGoalText(updated.Title, updated.Intent, updated.AcceptanceCriteria))
}

func writeGoalProblemDomainError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, goalproblem.ErrForbidden):
		writeErrorCode(w, http.StatusForbidden, "goal_problem_forbidden", err.Error())
	case errors.Is(err, goalproblem.ErrNotFound):
		writeErrorCode(w, http.StatusNotFound, "goal_problem_reference_not_found", err.Error())
	case errors.Is(err, goalproblem.ErrConflict):
		writeErrorCode(w, http.StatusConflict, "goal_problem_conflict", err.Error())
	case errors.Is(err, goalproblem.ErrInvalidInput):
		writeErrorCode(w, http.StatusBadRequest, "goal_problem_invalid", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "failed to apply problem-solving command")
	}
}

func (h *Handler) checkGoalProblemTasks(w http.ResponseWriter, r *http.Request, q *db.Queries, goal db.Goal, state goalproblem.State, command *goalproblem.Command) bool {
	refs := command.IssueRefs()
	if result := command.RecordValidationResult; result != nil {
		if batch, ok := state.ValidationBatch(result.BatchID); ok {
			refs = append(refs, batch.Task.IssueID)
		}
	}
	slices.Sort(refs)
	for _, raw := range slices.Compact(refs) {
		id, ok := parseUUIDOrBadRequest(w, raw, "issue_id")
		if !ok {
			return false
		}
		ctx := r.Context()
		issue, err := q.LockIssueForGoalLink(ctx, db.LockIssueForGoalLinkParams{WorkspaceID: goal.WorkspaceID, ID: id})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			h.writeGoalWriteError(w, r, err, "read verification task")
			return false
		}
		if errors.Is(err, pgx.ErrNoRows) || issue.ProjectID != goal.ProjectID {
			writeError(w, http.StatusNotFound, "verification task not found")
			return false
		}
		link, err := q.GetGoalIssueLink(ctx, db.GetGoalIssueLinkParams{WorkspaceID: goal.WorkspaceID, IssueID: id})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			h.writeGoalWriteError(w, r, err, "read task goal")
			return false
		}
		if errors.Is(err, pgx.ErrNoRows) || link.GoalID != goal.ID {
			writeErrorCode(w, http.StatusConflict, "goal_problem_task_not_linked", "the verification task must be linked to this goal")
			return false
		}
		if batch := command.StartValidationBatch; batch != nil && batch.Task.IssueID == raw {
			current, err := q.GetIssue(ctx, id)
			if err != nil {
				h.writeGoalWriteError(w, r, err, "read verification task owner")
				return false
			}
			batch.Task.IssueID = uuidToString(id)
			batch.Task.SubmitterID = ""
			if current.AssigneeID.Valid {
				batch.Task.SubmitterID = current.AssigneeType.String + "/" + uuidToString(current.AssigneeID)
			}
		}
	}
	return true
}
