package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/issueworkflow"
	"github.com/multica-ai/multica/server/internal/projectaccess"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/pkg/acceptance"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
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
	case step.Owner != nil && step.Owner.Kind != "squad" &&
		len(run.History) > 0 && run.History[len(run.History)-1].Action == "rework":
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
	if run.GoalExecution && run.State == "working" && step.Gate == "candidate" &&
		recipient.Kind == "squad" && len(run.History) > 0 && run.History[len(run.History)-1].Action == "rework" {
		squad, err := h.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{
			ID: parseUUIDOrInvalid(recipient.ID), WorkspaceID: issue.WorkspaceID,
		})
		if err != nil {
			return nil, err
		}
		if squad.ArchivedAt.Valid {
			catalog, err := h.workMethodCatalog(ctx, issue.WorkspaceID)
			if err != nil {
				return nil, err
			}
			for _, method := range catalog {
				if method.ID != run.Method.ID || run.Step >= len(method.Steps) {
					continue
				}
				next := method.Steps[run.Step]
				if next.Name == step.Name && next.Gate == step.Gate && next.Owner != nil && next.Owner.Kind == "agent" &&
					next.Reviewer != nil && step.Reviewer != nil && *next.Reviewer == *step.Reviewer {
					recipient = next.Owner
				}
				break
			}
		}
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
			if revision := methodBoundRevision(&task); revision > 0 && (issue.ID != task.IssueID || !run.AcceptsTaskRevision(revision)) {
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

func (h *Handler) writeIssueMethodView(w http.ResponseWriter, r *http.Request, issue db.Issue, run workmethod.Run, delivery *issuePlanContract) {
	actor, err := h.methodRecipient(r.Context(), issue, run)
	run.CurrentActor = actor
	if run.State == "closed" && run.Method != nil && run.Method.Kind == "development" && len(run.History) > 0 {
		last := &run.History[len(run.History)-1]
		if last.Action == "approve" && last.Closeout == nil {
			approvedAt, timeErr := time.Parse(time.RFC3339Nano, last.At)
			trace, traceErr := acceptance.Decode(issue.AcceptanceCriteria)
			if timeErr == nil && traceErr == nil && len(trace.Criteria) > 0 {
				snapshot := trace.Closeout(&approvedAt)
				last.Closeout = &snapshot
			}
		}
	}
	writeJSON(w, 200, struct {
		workmethod.Run
		CanAct bool `json:"can_act"`
		*issuePlanContract
	}{run, err == nil && h.canActOnMethod(r, issue, run, actor), delivery})
}

func (h *Handler) persistMethodTransition(w http.ResponseWriter, r *http.Request, issue db.Issue, run *workmethod.Run, trace []byte, lesson *verifiedProjectLesson) (db.Issue, bool) {
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
	actorKind, actorID := h.resolveActor(r, requestUserID(r), ws)
	source, hasSource := h.taskFromRequestHeader(r)
	scoped := hasSource && service.ReadGoalExecutionTask(source).ID.Valid
	if scoped && (r.Header.Get("X-Actor-Source") != "task_token" || source.Status != "running" ||
		source.AgentID != parseUUIDOrInvalid(actorID) || source.IssueID != issue.ID) {
		writeError(w, http.StatusForbidden, "goal execution requires its current authenticated run")
		return db.Issue{}, false
	}
	if scoped && source.SquadID.Valid && !source.IsLeaderTask {
		writeError(w, http.StatusForbidden, "only the squad leader can transition the candidate step")
		return db.Issue{}, false
	}
	if run.GoalExecution && !scoped && actorKind != "member" {
		writeError(w, http.StatusForbidden, "goal execution requires its current authenticated run")
		return db.Issue{}, false
	}
	if run.GoalExecution && !scoped && isMachineCredentialActor(r) {
		writeError(w, http.StatusForbidden, "manual takeover requires a human")
		return db.Issue{}, false
	}
	// An explicit human transition uses the ordinary private agent policy.
	run.GoalExecution = scoped
	if actor != nil {
		kind, id = pgtype.Text{String: actor.Kind, Valid: true}, parseUUIDOrInvalid(actor.ID)
		if !scoped || kind.String == "member" {
			if status, msg := h.validateAssigneePair(r.Context(), r, ws, kind, id); status != 0 {
				writeError(w, status, msg)
				return db.Issue{}, false
			}
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
			if err != nil || squad.ArchivedAt.Valid || !squad.LeaderID.Valid {
				writeError(w, 409, "无法确定军团负责人")
				return db.Issue{}, false
			}
			agentID, squadID = squad.LeaderID, id
		}
	}
	var human pgtype.UUID
	var parent *db.AgentTaskQueue
	if agentID.Valid {
		if actorKind == "member" {
			human = parseUUIDOrInvalid(actorID)
		}
		if actorKind == "agent" {
			if !hasSource || source.AgentID != parseUUIDOrInvalid(actorID) || isTerminalTaskStatus(source.Status) {
				writeError(w, 403, "智能体派工需要有效的来源运行")
				return db.Issue{}, false
			}
			parent = &source
		}
	}
	cycle, repeated := run.RepeatedRework()
	repeated = repeated && h.ReworkAnalysisSquadID.Valid && issue.ProjectID.Valid
	var analysisPolicy service.IssueCountPolicy
	if repeated {
		analysisPolicy = service.ResolveIssueCountPolicy(r.Context(), h.Entitlements, issue.WorkspaceID)
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
	if repeated {
		if _, err := q.LockWorkspaceForChatSessionCreate(ctx, issue.WorkspaceID); err != nil {
			writeError(w, 500, "failed to prepare rework analysis")
			return db.Issue{}, false
		}
	}
	if lesson != nil {
		if _, err := q.LockProjectForDelete(ctx, db.LockProjectForDeleteParams{WorkspaceID: issue.WorkspaceID, ID: issue.ProjectID}); err != nil {
			writeError(w, 409, "project changed; reload the issue")
			return db.Issue{}, false
		}
	}
	var executionID pgtype.UUID
	analysisRequester := parseUUIDOrInvalid(requestUserID(r))
	if scoped {
		execution, err := service.CheckGoalExecutionTask(ctx, q, issue, source, true)
		if err == nil && service.ReadGoalExecutionTask(source).Revision == 0 {
			if run.Method == nil || run.Method.ID != execution.Progress.MethodID || run.State != "working" ||
				len(run.History) == 0 || run.History[len(run.History)-1].Action != "start" {
				err = service.ErrWakeupForbidden
			}
		}
		if err == nil && run.State == "review" {
			// An agent may hold several squad roles. Compare the resolved
			// recipient with the actual submitter, not their role labels.
			event := run.History[len(run.History)-1]
			if actor == nil || (event.Action == "advance" &&
				(event.Actor == "agent/"+uuidToString(agentID) || event.Actor == "member/"+uuidToString(id))) {
				writeError(w, http.StatusConflict, "此步骤需要不同于提交人的独立审核人")
				return db.Issue{}, false
			}
			if event.Action == "advance" && len(issue.WorkMethodRun) > 0 {
				if err := checkGoalCandidateReview(ctx, q, issue, execution.ID, agentID, event.CandidateFailure); err != nil {
					writeError(w, http.StatusConflict, err.Error())
					return db.Issue{}, false
				}
			}
		}
		if err == nil && agentID.Valid {
			err = service.CheckGoalExecutionRecipient(ctx, q, issue, agentID, execution.By)
		}
		if err != nil {
			if errors.Is(err, service.ErrWakeupForbidden) {
				writeErrorCode(w, http.StatusForbidden, "goal_execution_forbidden", "目标授权已失效、当前步骤已变更，或接手角色无权被调用")
			} else {
				writeError(w, http.StatusInternalServerError, "failed to verify goal execution")
			}
			return db.Issue{}, false
		}
		executionID = execution.ID
		analysisRequester = execution.By
	} else if actorKind == "agent" && source.AccountableUserID.Valid {
		analysisRequester = source.AccountableUserID
	}
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
	var receivingTaskID pgtype.UUID
	var squadHandoff *workmethod.SquadHandoff
	if squadID.Valid && len(issue.WorkMethodRun) > 0 {
		var before workmethod.Run
		if err := json.Unmarshal(issue.WorkMethodRun, &before); err != nil {
			writeError(w, 500, "failed to read previous method phase")
			return db.Issue{}, false
		}
		taskID := dbid.NewV7()
		if squadHandoff = run.RecordSquadHandoff(before, uuidToString(taskID)); squadHandoff != nil {
			ledger := trace
			if ledger == nil {
				ledger = issue.AcceptanceCriteria
			}
			acceptanceTrace, err := acceptance.Decode(ledger)
			if err != nil {
				writeError(w, http.StatusConflict, "验收记录无法读取，请修复后重新交接")
				return db.Issue{}, false
			}
			squadHandoff.ContextBrief = run.SquadContextBrief(issue.Title, acceptanceTrace)
			receivingTaskID = taskID
		}
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
	if lesson != nil {
		if err := q.ProposeProjectSkill(ctx, db.ProposeProjectSkillParams{
			WorkspaceID: updated.WorkspaceID, ProjectID: updated.ProjectID, IssueID: updated.ID,
			MethodRevision: int32(run.Revision), TraceRevision: lesson.traceRevision,
			CandidateCommit: lesson.commit, Lesson: lesson.raw,
		}); err != nil {
			writeError(w, 500, "failed to save project skill proposal")
			return db.Issue{}, false
		}
	}
	var task db.AgentTaskQueue
	if agentID.Valid {
		note := fmt.Sprintf("Work method %s, step %d (%s), revision %d. Read the current method before acting.", run.Method.Name, run.Step+1, run.State, run.Revision)
		if failure := run.CandidateFailureReview(); failure != nil {
			note += "\n" + failure.ReviewBrief()
		}
		if squadHandoff != nil {
			note += "\n" + squadHandoff.Brief()
		}
		task, err = h.TaskService.CreateWorkMethodHandoff(ctx, q, updated, agentID, squadID, run.Revision, note, human, parent, executionID, receivingTaskID)
		if err != nil {
			slog.Warn("work method dispatch failed", "issue_id", uuidToString(issue.ID), "error", err)
			writeError(w, 409, "工作方法派工未成功，请检查接收人的运行环境与访问权限")
			return db.Issue{}, false
		}
	}
	var analysisIssue db.Issue
	var analysisTask db.AgentTaskQueue
	if repeated {
		var analysisSource *db.AgentTaskQueue
		if actorKind == "agent" && hasSource && source.IssueID == updated.ID && source.AgentID == parseUUIDOrInvalid(actorID) && !isTerminalTaskStatus(source.Status) {
			analysisSource = &source
		}
		analysis, err := tx.Begin(ctx)
		if err == nil {
			defer analysis.Rollback(ctx)
			analysisIssue, analysisTask, err = h.createReworkAnalysis(ctx, analysis, updated, *run, cycle, analysisPolicy, actorKind, parseUUIDOrInvalid(actorID), analysisRequester, analysisSource)
			if err == nil {
				err = analysis.Commit(ctx)
			} else {
				_ = analysis.Rollback(ctx)
			}
		}
		if err != nil {
			slog.Warn("could not create rework analysis", "issue_id", uuidToString(updated.ID), "error", err)
			analysisIssue, analysisTask = db.Issue{}, db.AgentTaskQueue{}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		writeError(w, 500, "failed to commit method transition")
		return db.Issue{}, false
	}
	if analysisIssue.ID.Valid {
		h.publish(protocol.EventIssueCreated, ws, actorKind, actorID, map[string]any{
			"issue": issueToResponse(analysisIssue, h.getIssuePrefix(ctx, issue.WorkspaceID)),
		})
		h.publishGoalChanged(ws, actorKind, actorID)
	}
	for _, handoff := range []db.AgentTaskQueue{task, analysisTask} {
		if handoff.ID.Valid {
			if err := h.TaskService.PublishWorkMethodHandoff(ctx, handoff); err != nil {
				slog.Warn("work method task awaiting runtime poll", "task_id", uuidToString(handoff.ID), "error", err)
			}
		}
	}
	return updated, true
}
