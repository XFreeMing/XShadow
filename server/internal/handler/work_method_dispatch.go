package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/issueworkflow"
	"github.com/multica-ai/multica/server/internal/projectaccess"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/workmethod"
)

// methodRecipient resolves a fresh destination once per transition. A running
// phase keeps the identity it was dispatched to even if the project lead changes.
func (h *Handler) methodRecipient(ctx context.Context, issue db.Issue, run workmethod.Run) (*workmethod.Actor, error) {
	if run.State == "closed" || run.Method == nil {
		return nil, nil
	}
	if run.CurrentActor != nil {
		return run.CurrentActor, nil
	}
	if run.Step < 0 || run.Step >= len(run.Method.Steps) {
		return nil, errors.New("工作方法步骤无效")
	}
	step := run.Method.Steps[run.Step]
	recipient := step.Owner
	switch {
	case run.State == "review":
		recipient = step.Reviewer
	case step.Owner != nil && len(run.History) > 0 && run.History[len(run.History)-1].Action == "rework":
		for i := len(run.History) - 2; i >= 0; i-- {
			e := run.History[i]
			if e.Action == "advance" && e.Step == run.Step {
				kind, id, ok := strings.Cut(e.Actor, "/")
				if ok && (kind == "member" || kind == "agent") && parseUUIDOrInvalid(id).Valid {
					return &workmethod.Actor{Kind: kind, ID: id}, nil
				}
				break
			}
		}
	}
	if recipient == nil {
		return nil, nil
	}
	var project db.Project
	if recipient.Kind == "project_lead" && issue.ProjectID.Valid {
		var err error
		project, err = h.Queries.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{ID: issue.ProjectID, WorkspaceID: issue.WorkspaceID})
		if err != nil {
			return nil, err
		}
	}
	kind, id, ok := issueworkflow.ResolveHandler(issueworkflow.Step{Handler: issueworkflow.Handler{Type: recipient.Kind, ID: recipient.ID}}, project, issue.CreatorType, issue.CreatorID)
	if !ok {
		return nil, fmt.Errorf("无法为本步骤解析负责人 %s", recipient.Kind)
	}
	return &workmethod.Actor{Kind: kind, ID: uuidToString(id)}, nil
}

func methodBoundRevision(task *db.AgentTaskQueue) int {
	if task == nil {
		return 0
	}
	var binding struct {
		Revision int `json:"work_method_revision"`
	}
	_ = json.Unmarshal(task.Context, &binding)
	return binding.Revision
}

func (h *Handler) canActOnMethod(r *http.Request, issue db.Issue, run workmethod.Run, actor *workmethod.Actor) bool {
	if run.State == "closed" {
		return false
	}
	kind, id := h.resolveActor(r, requestUserID(r), uuidToString(issue.WorkspaceID))
	if kind == "agent" {
		if taskID := parseUUIDOrInvalid(r.Header.Get("X-Task-ID")); taskID.Valid {
			task, err := h.Queries.GetAgentTask(r.Context(), taskID)
			if err != nil || task.AgentID != parseUUIDOrInvalid(id) || isTerminalTaskStatus(task.Status) {
				return false
			}
			if revision := methodBoundRevision(&task); revision > 0 && (issue.ID != task.IssueID || run.Revision != revision) {
				return false
			}
		}
	}
	if actor == nil {
		return true
	}
	if actor.Kind == kind && actor.ID == id {
		return true
	}
	if actor.Kind != "squad" || kind != "agent" {
		return false
	}
	squad, err := h.Queries.GetSquadInWorkspace(r.Context(), db.GetSquadInWorkspaceParams{ID: parseUUIDOrInvalid(actor.ID), WorkspaceID: issue.WorkspaceID})
	return err == nil && !squad.ArchivedAt.Valid && uuidToString(squad.LeaderID) == id
}

func (h *Handler) writeIssueMethodView(w http.ResponseWriter, r *http.Request, issue db.Issue, run workmethod.Run, delivery *GoalPlanDelivery) {
	actor, err := h.methodRecipient(r.Context(), issue, run)
	run.CurrentActor = actor
	writeJSON(w, 200, struct {
		workmethod.Run
		CanAct   bool              `json:"can_act"`
		Delivery *GoalPlanDelivery `json:"planned_delivery,omitempty"`
	}{run, err == nil && h.canActOnMethod(r, issue, run, actor), delivery})
}

func (h *Handler) persistMethodTransition(w http.ResponseWriter, r *http.Request, issue db.Issue, run *workmethod.Run, trace []byte) (db.Issue, bool) {
	run.CurrentActor = nil
	actor, err := h.methodRecipient(r.Context(), issue, *run)
	if err != nil {
		writeError(w, 409, err.Error())
		return db.Issue{}, false
	}
	run.CurrentActor = actor
	var kind pgtype.Text
	var id, agentID, squadID pgtype.UUID
	ws := uuidToString(issue.WorkspaceID)
	if actor != nil {
		kind, id = pgtype.Text{String: actor.Kind, Valid: true}, parseUUIDOrInvalid(actor.ID)
		if status, msg := h.validateAssigneePair(r.Context(), r, ws, kind, id); status != 0 {
			writeError(w, status, msg)
			return db.Issue{}, false
		}
		if !h.requireAssigneeCanAccessProject(w, r, issue.WorkspaceID, kind, id, issue.ProjectID) {
			return db.Issue{}, false
		}
		switch kind.String {
		case "member":
			if issue.ProjectID.Valid {
				hidden, err := projectaccess.ForPrincipals(r.Context(), h.Queries, issue.WorkspaceID, id)
				if err != nil {
					writeError(w, 500, "failed to resolve recipient project access")
					return db.Issue{}, false
				}
				if !hidden.CanSeeProject(issue.ProjectID) {
					writeError(w, 409, "当前负责人无权访问该项目")
					return db.Issue{}, false
				}
			}
		case "agent":
			agentID = id
		case "squad":
			squad, err := h.Queries.GetSquadInWorkspace(r.Context(), db.GetSquadInWorkspaceParams{ID: id, WorkspaceID: issue.WorkspaceID})
			if err != nil {
				writeError(w, 409, "无法确定军团负责人")
				return db.Issue{}, false
			}
			agentID, squadID = squad.LeaderID, id
		}
	}
	var human pgtype.UUID
	var parent *db.AgentTaskQueue
	if agentID.Valid {
		actorKind, actorID := h.resolveActor(r, requestUserID(r), ws)
		if actorKind == "member" {
			human = parseUUIDOrInvalid(actorID)
		}
		if actorKind == "agent" {
			source, ok := h.taskFromRequestHeader(r)
			if !ok || source.AgentID != parseUUIDOrInvalid(actorID) || isTerminalTaskStatus(source.Status) {
				writeError(w, 403, "智能体派工需要有效的来源运行")
				return db.Issue{}, false
			}
			parent = &source
		}
	}
	ctx := h.withWakeupActor(r).Context()
	if agentID.Valid {
		ctx = withWakeupHandedTo(ctx, uuidToString(agentID))
	}
	tx, err := h.beginWakeupWrite(ctx)
	if err != nil {
		writeError(w, 500, "failed to start method transition")
		return db.Issue{}, false
	}
	defer tx.Rollback(ctx)
	q := h.Queries.WithTx(tx)
	var boardStatus pgtype.Text
	status := methodBoardStatus(*run)
	if status != "" && status != issue.Status {
		category, err := issuestatus.CategoryWithError(ctx, q, issue.WorkspaceID, issue.Status)
		if err != nil {
			writeError(w, 500, "failed to read issue status")
			return db.Issue{}, false
		}
		if category == issuestatus.CategoryClosed || (category == issuestatus.CategoryDone && trace == nil) || issue.Status == issuestatus.Blocked {
			writeError(w, 409, "请先恢复事项再推进敏捷方法")
			return db.Issue{}, false
		}
		def, _, err := issueworkflow.ForProject(ctx, q, issue.WorkspaceID, issue.ProjectID)
		if err == nil && !issue.ProjectID.Valid {
			def, err = issueworkflow.OutsideProject(ctx, q, issue.WorkspaceID)
		}
		if err != nil {
			writeError(w, 500, "failed to read project workflow")
			return db.Issue{}, false
		}
		if err := issueworkflow.CheckStatus(def, status); err != nil {
			writeIssueWorkflowError(w, err)
			return db.Issue{}, false
		}
		boardStatus = pgtype.Text{String: status, Valid: true}
	}
	raw, err := json.Marshal(run)
	if err != nil {
		writeError(w, 500, "failed to encode method run")
		return db.Issue{}, false
	}
	updated, err := q.SaveIssueWorkMethodRun(ctx, db.SaveIssueWorkMethodRunParams{ID: issue.ID, WorkspaceID: issue.WorkspaceID, IssueRevision: issue.Revision, Run: raw, Trace: trace, BoardStatus: boardStatus, AssigneeType: kind, AssigneeID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 409, "issue changed; refresh before advancing the method")
		return db.Issue{}, false
	}
	if err != nil {
		writeError(w, 500, "failed to update method run")
		return db.Issue{}, false
	}
	var task db.AgentTaskQueue
	if agentID.Valid {
		note := fmt.Sprintf("Work method %s, step %d (%s), revision %d. Read the current method before acting.", run.Method.Name, run.Step+1, run.State, run.Revision)
		task, err = h.TaskService.CreateWorkMethodHandoff(ctx, q, updated, agentID, squadID, run.Revision, note, human, parent)
		if err != nil {
			slog.Warn("work method dispatch failed", "issue_id", uuidToString(issue.ID), "error", err)
			writeError(w, 409, "工作方法派工未成功，请检查接收人的运行环境与访问权限")
			return db.Issue{}, false
		}
	}
	if err := tx.Commit(ctx); err != nil {
		writeError(w, 500, "failed to commit method transition")
		return db.Issue{}, false
	}
	if task.ID.Valid {
		if err := h.TaskService.PublishWorkMethodHandoff(ctx, task); err != nil {
			slog.Warn("work method task awaiting runtime poll", "task_id", uuidToString(task.ID), "error", err)
		}
	}
	return updated, true
}
