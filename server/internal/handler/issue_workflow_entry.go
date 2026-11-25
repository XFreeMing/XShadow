package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/issueworkflow"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/pkg/acceptance"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/workmethod"
)

// Project workflows on the issue write paths (MUL-7420).
//
// Two rules apply to an issue in a project:
//   - when the project uses a named workflow, its status must be one the
//     workflow lists; a move into such a project keeps an equivalent status
//     instead of failing. Default accepts every status;
//   - entering a column the project hands off, through an ordinary status
//     change of an issue that stays in its project, assigns the issue to that
//     handler and starts its run with the step's instructions. A write that
//     sets the assignee itself keeps its explicit choice. A project move
//     enters the column it lands on only when the caller asks for it.
//
// Handoffs reuse assignment wholesale: the resolved handler goes through the
// same permission gate as a hand-picked assignee, and the run starts through
// the same WillEnqueueRun predicate, with the step's brief as its handoff note.

// workflowHandoff is a handoff an issue write triggers.
type workflowHandoff struct {
	def     issueworkflow.Definition
	step    issueworkflow.Step
	project db.Project
}

// applyIssueWorkflow enforces prev's (possibly new) project workflow on one
// prospective update and may rewrite params: the status of an issue moving
// into the project, and the assignee when the write enters a handoff step.
// enterOnMove makes a project move enter the step the issue lands on.
// run is the calling agent's run on this issue (nil for members): it may only
// move the issue from the step it works on. It returns the handoff that write
// triggers, or nil.
func (h *Handler) applyIssueWorkflow(ctx context.Context, prev db.Issue, params *db.UpdateIssueParams, statusExplicit, assigneeTouched, enterOnMove bool, run *db.AgentTaskQueue) (*workflowHandoff, error) {
	def, project, err := issueworkflow.ForProject(ctx, h.Queries, prev.WorkspaceID, params.ProjectID)
	if err != nil {
		return nil, err
	}
	// Keeping the current status is never a rejected write, even when the
	// workflow no longer lists it.
	projectMoving := params.ProjectID != prev.ProjectID
	if def != nil && projectMoving && !statusExplicit && !def.IsDefault && !def.Has(prev.Status) {
		target := issueworkflow.MoveTarget(*def, prev.Status, func(key string) string {
			return issuestatus.Category(ctx, h.Queries, prev.WorkspaceID, key)
		})
		params.Status = pgtype.Text{String: target, Valid: true}
	}
	statusChanging := params.Status.Valid && params.Status.String != prev.Status
	if statusChanging && len(prev.WorkMethodRun) > 0 {
		if bound := methodBoundRevision(run); bound > 0 {
			var method workmethod.Run
			if err := json.Unmarshal(prev.WorkMethodRun, &method); err != nil {
				return nil, err
			}
			if !method.AcceptsTaskRevision(bound) {
				return nil, errMethodStageMoved
			}
		}
		if err := h.checkMethodCompletion(ctx, prev, params.Status.String); err != nil {
			return nil, err
		}
	}
	if def == nil {
		if !statusChanging {
			return nil, nil
		}
		outside, err := issueworkflow.OutsideProject(ctx, h.Queries, prev.WorkspaceID)
		if err != nil {
			return nil, err
		}
		return nil, issueworkflow.CheckStatusOutsideProject(outside, params.Status.String)
	}
	// A move into a Default project keeps the issue's status, as it is never
	// mapped; a named workflow must list the status the issue lands on.
	if params.Status.Valid && (statusChanging || (projectMoving && !def.IsDefault)) {
		if err := issueworkflow.CheckStatus(def, params.Status.String); err != nil {
			return nil, err
		}
	}
	// Someone else moved the issue while the run worked: the run's decision was
	// made for a step the issue has left, and applying it would undo that move.
	// Default without handoffs keeps the pre-workflow behavior, so workspaces
	// that never adopt workflows see no change.
	if methodBoundRevision(run) == 0 && workflowStepMoved(def, prev.Status, run) &&
		statusExplicit && params.Status.Valid && params.Status.String != prev.Status {
		return nil, h.stepMovedError(ctx, prev, run.WorkflowStep.String)
	}
	if assigneeTouched || prev.TriageState.Valid {
		return nil, nil
	}
	entering := ""
	switch {
	case projectMoving && enterOnMove:
		entering = prev.Status
		if params.Status.Valid {
			entering = params.Status.String
		}
	// A project move is administrative unless the caller opted in, and
	// re-selecting the current status is a no-op: neither hands the issue off.
	case !projectMoving && statusExplicit && params.Status.Valid && params.Status.String != prev.Status:
		entering = params.Status.String
	default:
		return nil, nil
	}
	step, ok := def.Step(entering)
	if !ok || !step.HandsOff() {
		return nil, nil
	}
	method, err := h.resolveIssueWorkMethod(ctx, prev)
	if err != nil {
		return nil, err
	}
	if methodOwnsHandoff(method) {
		return nil, nil
	}
	assigneeType, assigneeID, ok := issueworkflow.ResolveHandler(step, project, prev.CreatorType, prev.CreatorID)
	if !ok {
		return nil, nil
	}
	params.AssigneeType = pgtype.Text{String: assigneeType, Valid: true}
	params.AssigneeID = assigneeID
	return &workflowHandoff{def: *def, step: step, project: project}, nil
}

// A method with its own dispatch, and research or outcome work, own the handoff.
func methodOwnsHandoff(run workmethod.Run) bool {
	if run.Method == nil {
		return false
	}
	if run.Method.Kind == "research" || run.Method.Kind == "outcome" {
		return true
	}
	if run.State == "closed" {
		return false
	}
	for _, step := range run.Method.Steps {
		if step.Owner != nil || step.Reviewer != nil {
			return true
		}
	}
	return false
}

// Evidence-gated methods and explicitly authorized goal runs own the board.
// Recommendations retain the project's status workflow.
func methodBoardStatus(run workmethod.Run) string {
	if run.State == "closed" && run.Method != nil && run.Method.GitHubDelivery != nil {
		return issuestatus.InReview
	}
	if (!run.GoalExecution && run.EvidenceGate() == "") || run.State == "ready" {
		return ""
	}
	switch run.State {
	case "closed":
		return issuestatus.Done
	case "review":
		return issuestatus.InReview
	case "working":
		return issuestatus.InProgress
	}
	return ""
}

var errMethodOwnsStatus = errors.New("当前看板状态由敏捷工作方法同步，请在事项的工作方法中提交或评审")
var errMethodStageMoved = errors.New("work method stage moved; end this run")

var errMethodReviewOpen = errors.New("complete the work method review before marking the issue done")
var errMethodMergePending = errors.New("等待已审核候选合入指定 GitHub 分支")

func (h *Handler) checkMethodCompletion(ctx context.Context, issue db.Issue, status string) error {
	return h.checkMethodStatus(ctx, issue, status, false)
}

func (h *Handler) checkMethodStatus(ctx context.Context, issue db.Issue, status string, requestingMerge bool) error {
	if len(issue.WorkMethodRun) == 0 {
		return nil
	}
	var run workmethod.Run
	if err := json.Unmarshal(issue.WorkMethodRun, &run); err != nil {
		return err
	}
	if expected := methodBoardStatus(run); expected != "" && run.State != "closed" && expected != status {
		switch status {
		case issuestatus.Backlog, issuestatus.Todo, issuestatus.InProgress, issuestatus.InReview:
			return errMethodOwnsStatus
		}
	}
	category, err := issuestatus.CategoryWithError(ctx, h.issueStatusCatalog(), issue.WorkspaceID, status)
	if err != nil || category != issuestatus.CategoryDone {
		return err
	}
	// Member requests use their recorded answer to release the source
	// issue's wait. A suggested method on the request itself is advisory.
	if run.State == "ready" && run.Revision == 0 && service.IsMemberTodo(issue) {
		return nil
	}
	if run.State != "closed" {
		return errMethodReviewOpen
	}
	if !requestingMerge && run.Method != nil && run.Method.GitHubDelivery != nil && h.verifyMethodGitHubDelivery(ctx, issue, run.Method.GitHubDelivery) != nil {
		return errMethodMergePending
	}
	return nil
}

// workflowStepMoved shares the step guard with the claim brief so a run is
// never told to advance a step its status changes cannot act on.
func workflowStepMoved(def *issueworkflow.Definition, status string, run *db.AgentTaskQueue) bool {
	return def != nil && run != nil && run.WorkflowStep.Valid && run.WorkflowStep.String != status &&
		(!def.IsDefault || def.HandsOffAnywhere())
}

// workflowHandoffPayload describes a handoff on the issue:updated event, so
// the activity log can record the status change and the handoff as one
// timeline entry.
func workflowHandoffPayload(issue db.Issue, hand *workflowHandoff, note string, runStarted bool) map[string]string {
	out := map[string]string{
		"workflow": hand.def.Name,
		"to_type":  issue.AssigneeType.String,
		"to_id":    uuidToString(issue.AssigneeID),
		"run":      strconv.FormatBool(runStarted),
	}
	if runStarted && note != "" {
		out["note"] = note
	}
	return out
}

// writeIssueWorkflowError answers a workflow rejection. It reports false when
// err is not one, so the caller can fall through to its generic 500.
func writeIssueWorkflowError(w http.ResponseWriter, err error) bool {
	if errors.Is(err, errMethodOwnsStatus) {
		writeJSON(w, 409, map[string]any{"error": err.Error(), "code": "work_method_owns_status"})
		return true
	}
	if errors.Is(err, errMethodStageMoved) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "code": "work_method_stage_moved"})
		return true
	}
	if errors.Is(err, errMethodReviewOpen) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "code": "work_method_review_open"})
		return true
	}
	if errors.Is(err, errMethodMergePending) {
		writeErrorCode(w, http.StatusConflict, "github_delivery_pending", err.Error())
		return true
	}
	var notIn *issueworkflow.NotInWorkflowError
	if errors.As(err, &notIn) {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error":            notIn.Error(),
			"code":             "status_not_in_workflow",
			"workflow_name":    notIn.WorkflowName,
			"allowed_statuses": notIn.Allowed,
		})
		return true
	}
	var moved *issueworkflow.StepMovedError
	if errors.As(err, &moved) {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":          moved.Error(),
			"code":           "workflow_step_moved",
			"step":           moved.Step,
			"current_status": moved.Current,
		})
		return true
	}
	return false
}

// agentRunOnIssue returns the calling agent's run when it works on this issue,
// nil for members and for runs on other issues. The task id is the
// server-trusted X-Task-ID resolveActor vouched for.
func (h *Handler) agentRunOnIssue(r *http.Request, actorType string, issue db.Issue) *db.AgentTaskQueue {
	if actorType != "agent" {
		return nil
	}
	taskID, err := util.ParseUUID(r.Header.Get("X-Task-ID"))
	if err != nil {
		return nil
	}
	task, err := h.Queries.GetAgentTask(r.Context(), taskID)
	if err != nil || task.IssueID != issue.ID {
		return nil
	}
	return &task
}

// handedToAgent names the agent a write hands the issue to: its new agent
// assignee, the leader of its new squad assignee, or a workflow step's
// handler even when that handler already owned the issue. Empty when the
// write leaves the issue with whoever had it.
func (h *Handler) handedToAgent(ctx context.Context, prev db.Issue, params db.UpdateIssueParams, handoff bool) string {
	if !params.AssigneeID.Valid {
		return ""
	}
	if !handoff && params.AssigneeType == prev.AssigneeType && params.AssigneeID == prev.AssigneeID {
		return ""
	}
	switch params.AssigneeType.String {
	case "agent":
		return uuidToString(params.AssigneeID)
	case "squad":
		squad, err := h.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{ID: params.AssigneeID, WorkspaceID: prev.WorkspaceID})
		if err == nil && squad.LeaderID.Valid {
			return uuidToString(squad.LeaderID)
		}
	}
	return ""
}

// advanceRunStep moves a run's workflow step along with a status change the
// run made itself, so its next change is judged from where it put the issue.
func (h *Handler) advanceRunStep(ctx context.Context, run *db.AgentTaskQueue, prev, issue db.Issue) {
	if run == nil || !run.WorkflowStep.Valid || prev.Status == issue.Status {
		return
	}
	if err := h.Queries.SetTaskWorkflowStep(ctx, db.SetTaskWorkflowStepParams{
		ID:           run.ID,
		WorkflowStep: pgtype.Text{String: issue.Status, Valid: true},
	}); err != nil {
		slog.Warn("workflow: advance run step failed", "task_id", uuidToString(run.ID), "error", err)
	}
}

// stepMovedError explains a refused status change: where the run's step was,
// where the issue is now, and who moved it.
func (h *Handler) stepMovedError(ctx context.Context, issue db.Issue, step string) error {
	resolver := issuestatus.NewResolver(issue.WorkspaceID)
	out := &issueworkflow.StepMovedError{
		IssueIdentifier: h.getIssuePrefix(ctx, issue.WorkspaceID) + "-" + strconv.Itoa(int(issue.Number)),
		Step:            step,
		StepName:        resolver.Name(ctx, h.Queries, step),
		Current:         issue.Status,
		CurrentName:     resolver.Name(ctx, h.Queries, issue.Status),
		MovedBy:         h.lastIssueStatusMover(ctx, issue),
	}
	return out
}

func (h *Handler) lastIssueStatusMover(ctx context.Context, issue db.Issue) string {
	if last, err := h.Queries.GetLastIssueStatusChange(ctx, issue.ID); err == nil && last.ActorID.Valid {
		return h.actorDisplayName(ctx, issue.WorkspaceID, last.ActorType.String, last.ActorID)
	}
	return ""
}

// workflowHandoffBrief renders the brief a handoff's agent or squad will
// read, for the timeline and the handoff confirmation. Members run nothing,
// so they get none. The run itself reads the brief its claim renders.
func (h *Handler) workflowHandoffBrief(ctx context.Context, issue db.Issue, hand *workflowHandoff) string {
	if hand == nil || !issue.AssigneeType.Valid {
		return ""
	}
	if t := issue.AssigneeType.String; t != "agent" && t != "squad" {
		return ""
	}
	in := h.workflowBriefInput(ctx, issue, hand.def, hand.project)
	in.StatusKey = hand.step.StatusKey
	brief := issueworkflow.Brief(in)
	if method, err := h.resolveIssueWorkMethod(ctx, issue); err == nil && method.Method != nil {
		brief += "\n" + methodClaimBrief(method, in.IssueIdentifier, true, false)
	}
	return brief
}

// workflowBriefInput describes an issue at its current status under its
// project's workflow, for the assignee's brief.
func (h *Handler) workflowBriefInput(ctx context.Context, issue db.Issue, def issueworkflow.Definition, project db.Project) issueworkflow.BriefInput {
	resolver := issuestatus.NewResolver(issue.WorkspaceID)
	return issueworkflow.BriefInput{
		Workflow:        def,
		ProjectName:     project.Title,
		StatusKey:       issue.Status,
		IssueIdentifier: h.getIssuePrefix(ctx, issue.WorkspaceID) + "-" + strconv.Itoa(int(issue.Number)),
		StatusName: func(key string) string {
			return resolver.Name(ctx, h.Queries, key)
		},
		HandlerName: func(step issueworkflow.Step) string {
			if !step.HandsOff() {
				return ""
			}
			return h.stepHandlerName(ctx, issue, project, step)
		},
	}
}

// actorDisplayName names an agent, squad or member for a brief. It falls back
// to the actor type so a lookup failure never blocks a handoff.
func (h *Handler) actorDisplayName(ctx context.Context, wsUUID pgtype.UUID, actorType string, id pgtype.UUID) string {
	switch actorType {
	case "agent":
		if agent, err := h.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{ID: id, WorkspaceID: wsUUID}); err == nil {
			return agent.Name
		}
	case "squad":
		if squad, err := h.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{ID: id, WorkspaceID: wsUUID}); err == nil {
			return squad.Name
		}
	case "member":
		if user, err := h.Queries.GetUser(ctx, id); err == nil {
			return user.Name
		}
	}
	return actorType
}

// stopPreviousAssigneeRuns cancels the active runs of whoever a handoff took
// the issue from — an agent's runs, or every run made on a squad's behalf —
// when the caller asked for it. A handoff back to the same handler keeps its
// run, and the agent making the handoff keeps its own: that run is the one
// making this write.
func (h *Handler) stopPreviousAssigneeRuns(ctx context.Context, prev, issue db.Issue, actorType, actorID string) {
	if issue.AssigneeType == prev.AssigneeType && issue.AssigneeID == prev.AssigneeID {
		return
	}
	tasks := h.assigneeActiveRuns(ctx, prev)
	if len(tasks) == 0 {
		return
	}
	actor := service.TaskCancellationActor{Type: actorType}
	if id, err := parseUUIDString(actorID); err == nil {
		actor.ID = id
		actor.Name = h.actorDisplayName(ctx, issue.WorkspaceID, actorType, id)
	}
	for _, task := range tasks {
		if actorType == "agent" && uuidToString(task.AgentID) == actorID {
			continue
		}
		if _, err := h.TaskService.CancelTaskByUser(ctx, task.ID, actor); err != nil {
			slog.Warn("workflow: cancel previous run failed",
				"issue_id", uuidToString(issue.ID), "task_id", uuidToString(task.ID), "error", err)
		}
	}
}

// assigneeActiveRuns lists the unfinished runs of an issue's assignee on it,
// including deferred work: the agent's own runs, or every run made on a
// squad's behalf. Members run nothing.
func (h *Handler) assigneeActiveRuns(ctx context.Context, issue db.Issue) []db.ListAssigneeRunsForHandoffRow {
	if !issue.AssigneeID.Valid {
		return nil
	}
	assigneeType := issue.AssigneeType.String
	if assigneeType != "agent" && assigneeType != "squad" {
		return nil
	}
	tasks, err := h.Queries.ListAssigneeRunsForHandoff(ctx, db.ListAssigneeRunsForHandoffParams{
		IssueID: issue.ID, AssigneeType: assigneeType, AssigneeID: issue.AssigneeID,
	})
	if err != nil {
		slog.Warn("workflow: list active tasks failed", "issue_id", uuidToString(issue.ID), "error", err)
		return nil
	}
	return tasks
}

// claimProjectWorkflow renders the issue's project workflow for a task claim,
// or nil when there is nothing to say beyond the runtime's own status rules:
// no project, or Default with no handoffs. The issue's assignee gets the
// step's instructions and commands; any other agent working on the issue
// gets the observer brief, so it does not move a step it does not own.
//
// legacyRuntimeRules is true for a daemon that does not advertise
// protocol.DaemonCapabilityWorkflowStatusRulesV1; see BriefInput.
func (h *Handler) claimProjectWorkflow(ctx context.Context, issue db.Issue, task *db.AgentTaskQueue, legacyRuntimeRules bool) *TaskProjectWorkflowData {
	method, contract, methodErr := h.issueWorkMethod(ctx, issue, nil)
	if methodErr != nil {
		slog.Warn("task claim: failed to read work method", "issue_id", uuidToString(issue.ID), "error", methodErr)
	}
	def, project, err := issueworkflow.ForProject(ctx, h.Queries, issue.WorkspaceID, issue.ProjectID)
	if err != nil {
		slog.Warn("task claim: failed to load project workflow for brief injection",
			"issue_id", uuidToString(issue.ID), "error", err)
		return nil
	}
	methodOwned := methodOwnsHandoff(method)
	stepMoved := !methodOwned && workflowStepMoved(def, issue.Status, task)
	bound := methodBoundRevision(task)
	if bound > 0 && !method.AcceptsTaskRevision(bound) {
		return &TaskProjectWorkflowData{Brief: "This work-method handoff has already moved. End this run. The current stage has a separate owner.", stepMoved: true}
	}
	ownsStep := taskRunsForAssignee(issue, task) && !stepMoved
	methodBrief := ""
	stepContext := false
	freshSession := false
	if method.Method != nil {
		issueRef := h.getIssuePrefix(ctx, issue.WorkspaceID) + "-" + strconv.Itoa(int(issue.Number))
		recipient, err := h.methodRecipient(ctx, issue, method)
		if err != nil {
			ownsStep = false
		}
		if recipient != nil {
			method.CurrentActor = recipient
			ownsStep = task != nil && (recipient.Kind == "agent" && uuidToString(task.AgentID) == recipient.ID || recipient.Kind == "squad" && task.IsLeaderTask && uuidToString(task.SquadID) == recipient.ID)
		}
		stepContext = usesCurrentStepContext(issue, method, task)
		freshSession = stepContext && currentStepContextMode(issue.Metadata) == "current_step"
		methodBrief = contract.claimBrief(issueRef) + methodClaimBrief(method, issueRef, ownsStep, stepContext)
		if stepContext {
			methodBrief = fmt.Sprintf("Issue %s: %q.\n", issueRef, issue.Title) + methodBrief + stepAcceptanceIndex(issue, method)
		}
		if ownsStep && issue.ProjectID.Valid && method.CanProposeProjectLesson() {
			methodBrief += "\n\n" + workmethod.ProjectLessonBrief
		}
	}
	if def == nil || (def.IsDefault && !def.HandsOffAnywhere()) || methodOwned {
		if methodBrief == "" {
			return nil
		}
		return &TaskProjectWorkflowData{Brief: methodBrief, StepContext: stepContext, stepMoved: stepMoved, freshSession: freshSession}
	}
	in := h.workflowBriefInput(ctx, issue, *def, project)
	in.LegacyRuntimeRules = legacyRuntimeRules
	if stepMoved {
		in.BoundStep = task.WorkflowStep.String
		in.MovedBy = h.lastIssueStatusMover(ctx, issue)
	} else if !taskRunsForAssignee(issue, task) {
		in.Observer = true
		if issue.AssigneeType.Valid && issue.AssigneeID.Valid {
			in.AssigneeName = h.actorDisplayName(ctx, issue.WorkspaceID, issue.AssigneeType.String, issue.AssigneeID)
		}
	}
	brief := issueworkflow.Brief(in)
	if methodBrief != "" {
		brief += "\n" + methodBrief
	}
	return &TaskProjectWorkflowData{Brief: brief, StepContext: stepContext, stepMoved: stepMoved, freshSession: freshSession}
}

// A fresh rerun of step work gets the same brief as a new handoff.
// Resumed runs and automatic retries keep their continuity contract.
func usesCurrentStepContext(issue db.Issue, run workmethod.Run, task *db.AgentTaskQueue) bool {
	if task == nil || (run.State != "working" && run.State != "review") {
		return false
	}
	if task.RetryOfTaskID.Valid || task.ChatSessionID.Valid || task.AutopilotRunID.Valid {
		return false
	}
	if task.RerunOfTaskID.Valid && !service.IsFreshRerun(*task) {
		return false
	}
	var trigger struct {
		Revision int    `json:"work_method_revision"`
		Wakeup   string `json:"wakeup_id"`
		Evidence struct {
			Facts []struct {
				Event   string `json:"event_type"`
				Payload struct {
					IssueID string `json:"issue_id"`
					TaskID  string `json:"task_id"`
				} `json:"payload"`
			} `json:"facts"`
		} `json:"wakeup_evidence"`
	}
	if len(task.Context) > 0 {
		if err := json.Unmarshal(task.Context, &trigger); err != nil {
			return false
		}
	}
	if trigger.Wakeup != "" {
		// A child completion carries the handoff instruction and its receipt.
		// Timers and unrelated wakeups still resume their original conversation.
		if !task.DelegatedFromTaskID.Valid || len(trigger.Evidence.Facts) == 0 {
			return false
		}
		for _, f := range trigger.Evidence.Facts {
			if f.Event != "task.completed" || f.Payload.IssueID != uuidToString(issue.ID) || f.Payload.TaskID == "" {
				return false
			}
		}
	}
	if trigger.Revision == 0 && !task.TriggerCommentID.Valid && !task.DelegatedFromTaskID.Valid {
		return false
	}
	mode := currentStepContextMode(issue.Metadata)
	return mode == "current_step" || (mode == "" && run.GoalExecution && run.Method != nil &&
		run.Method.Kind == "development" && trigger.Revision > 0 && run.AcceptsTaskRevision(trigger.Revision))
}

func currentStepContextMode(raw []byte) string {
	var config struct {
		Mode string `json:"work_method_context"`
	}
	_ = json.Unmarshal(raw, &config)
	return config.Mode
}

// The handoff carries the current contract and an index into the ledger. Large
// case specifications, superseded rows and per-run logs stay on demand.
func stepAcceptanceIndex(issue db.Issue, run workmethod.Run) string {
	if run.Step < 0 || run.Step >= len(run.Method.Steps) || run.Method.Steps[run.Step].Gate == "" {
		return ""
	}
	trace, err := acceptance.Decode(issue.AcceptanceCriteria)
	if err != nil {
		return "\nRead the current acceptance contract from the issue before acting."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\nAcceptance revision %d:", trace.Revision)
	if run.Method.Steps[run.Step].Gate == "criteria" && issue.Description.Valid {
		fmt.Fprintf(&b, "\nStory scope: %q", issue.Description.String)
	}
	for _, c := range trace.Criteria {
		fmt.Fprintf(&b, "\n%s v%d: %q", c.ID, len(c.Versions), c.Versions[len(c.Versions)-1])
		if run.Method.Steps[run.Step].Gate == "criteria" {
			continue
		}
		var ids []string
		for _, tc := range c.Cases {
			if tc.Retired == nil {
				ids = append(ids, fmt.Sprintf("%s v%d", tc.ID, tc.Version()))
			}
		}
		fmt.Fprintf(&b, " Active cases: %s.", strings.Join(ids, ", "))
		if gate := run.Method.Steps[run.Step].Gate; gate == "candidate" || gate == "delivery" {
			for i := len(c.Implementations) - 1; i >= 0; i-- {
				link := c.Implementations[i]
				if link.Version == len(c.Versions) {
					fmt.Fprintf(&b, " Candidate: %s@%s.", link.Repository, link.Commit)
					break
				}
			}
		}
	}
	return b.String()
}

// The method instance is read again before a write: another contributor may
// have advanced the step since this run was claimed.
func methodClaimBrief(run workmethod.Run, issueRef string, ownsStep, stepContext bool) string {
	if run.Method == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Work method for %s: %q (%s), state %s.\n", issueRef, run.Method.Name, run.Method.ID, run.State)
	if stepContext {
		fmt.Fprintf(&b, "Method revision %d. On conflict or missing history, reload with \"$MULTICA_CLI_PATH\" issue method %s.\n", run.Revision, issueRef)
	} else {
		fmt.Fprintf(&b, "Read the latest revision, delivered evidence and checklist: \"$MULTICA_CLI_PATH\" issue method %s\n", issueRef)
	}
	if n := len(run.History); n > 0 {
		last := run.History[n-1]
		fmt.Fprintf(&b, "Latest recorded action %q; evidence: %q.\n", last.Action, last.Evidence)
		if stepContext {
			writeStepReviewFindings(&b, run)
			anchor := last.At
			if n > 1 {
				anchor = run.History[n-2].At
			}
			fmt.Fprintf(&b, "Check new feedback with issue comment list %s --since %s --compact. Expand only threads that affect this step. Reuse the frozen cases and prior receipts by reference; record the current candidate SHA, new evidence, and any unresolved decision.\n", issueRef, anchor)
		}
	}
	if run.State == "closed" {
		b.WriteString("The method review has been approved.")
		return b.String()
	}
	if run.State == "ready" {
		fmt.Fprintf(&b, "Selection basis: %q.\n", run.Reason)
	}
	if run.Step < 0 || run.Step >= len(run.Method.Steps) {
		return b.String()
	}
	step := run.Method.Steps[run.Step]
	fmt.Fprintf(&b, "Step %d of %d: %q. Required output: %q.\n", run.Step+1, len(run.Method.Steps), step.Name, step.Output)
	if stepContext && step.Gate != "" {
		fmt.Fprintf(&b, "Acceptance gate %q. Use \"$MULTICA_CLI_PATH\" issue acceptance %s --current-case <ID> for one case, its current rule, candidate and last run. Read --criterion <ID> when its history is needed. ", step.Gate, issueRef)
		switch step.Gate {
		case "criteria":
			b.WriteString("Use current criterion texts and their user-facing scope.")
		case "coverage":
			b.WriteString("Check current criteria and active cases against original samples. Before submitting cases, show which plausible wrong implementation each important case rejects. Where applicable, check object identity alongside status, legacy fields alongside current fields, and actual execution evidence for each request. Retrieve retired cases only for supersession checks.")
		case "candidate", "delivery":
			b.WriteString("Use current criteria, active cases, candidate commit and matching run evidence. Verify the original entry points.")
		}
		b.WriteString(" Record evidence with --change-file.\n")
		if step.Gate == "candidate" {
			b.WriteString("When sharing evidence an already assigned worker expects, reply on your assigned thread. If the worker is queued or waiting for the directory, put the evidence at the agreed location and use issue comment add --suppress-agent <worker-UUID> for the receipt; for a running turn that accepts input, use --steer <run-ID>.\n")
		}
	} else if step.Gate != "" {
		fmt.Fprintf(&b, "The method synchronizes the board and dispatches the next actor. Read the versioned criteria, cases, candidate and impact with \"$MULTICA_CLI_PATH\" issue acceptance %s. Record actual structured evidence with --change-file; run issue acceptance --help for the JSON. Submit or review through issue method.\n", issueRef)
	}
	if step.Inputs != "" {
		fmt.Fprintf(&b, "Inputs: %s\n", step.Inputs)
	}
	if step.Instructions != "" {
		fmt.Fprintf(&b, "Instructions: %s\n", step.Instructions)
	}
	for i, check := range step.Checks {
		fmt.Fprintf(&b, "Check [%d]: %s\n", i, check)
	}
	if actor := run.CurrentActor; actor != nil {
		fmt.Fprintf(&b, "Current recipient: %s/%s.\n", actor.Kind, actor.ID)
	}
	if !ownsStep {
		b.WriteString("Provide findings to the current assignee. That assignee advances the method.")
		return b.String()
	}
	if status := methodBoardStatus(run); status != "" {
		fmt.Fprintf(&b, "If an external dependency prevents this step, record the dependency and the event that makes work possible. Register that wakeup on %s and confirm it is enabled with issue wakeup list. Mark the issue blocked with --no-start. On waking, verify the dependency, then restore status %s with --no-start before submitting the step.\n", issueRef, status)
	}
	action := "advance"
	if run.State == "review" {
		action = "approve"
		if run.Step == len(run.Method.Steps)-1 {
			fmt.Fprintf(&b, "Final review criteria: %s\n", run.Method.Review)
		}
		b.WriteString("Review the submitted work and each check against original evidence. Approve only after all checks pass. Otherwise record rework with the zero-based target_step. For each reproducible finding, submit findings with criterion_id and case_id when applicable, expected, actual, and evidence.\n")
	}
	payload := map[string]any{"base_revision": run.Revision, "action": action, "evidence": "<actual deliverable or review findings and source>"}
	if action == "approve" && step.Reviewer != nil && len(step.Checks) > 0 {
		checked := make([]int, len(step.Checks))
		for i := range checked {
			checked[i] = i
		}
		payload["checked_items"] = checked
	}
	example, _ := json.Marshal(payload)
	fmt.Fprintf(&b, "Record the result with \"$MULTICA_CLI_PATH\" issue method %s --change-file <json-file>. Use the latest revision and replace the evidence in this example: %s. After successful handoff, end this run. Complete the method review before moving the issue to a completed status.", issueRef, example)
	return b.String()
}

// taskRunsForAssignee reports whether a run works on the issue as its
// assignee: the assigned agent's own run, or a run made for the assigned
// squad.
func taskRunsForAssignee(issue db.Issue, task *db.AgentTaskQueue) bool {
	if task == nil || !issue.AssigneeID.Valid {
		return false
	}
	switch issue.AssigneeType.String {
	case "agent":
		return task.AgentID == issue.AssigneeID
	case "squad":
		return task.SquadID == issue.AssigneeID
	}
	return false
}

// stepHandlerName describes who a step hands the issue to, for briefs.
func (h *Handler) stepHandlerName(ctx context.Context, issue db.Issue, project db.Project, step issueworkflow.Step) string {
	t, id, ok := issueworkflow.ResolveHandler(step, project, issue.CreatorType, issue.CreatorID)
	if !ok {
		switch step.Handler.Type {
		case issueworkflow.HandlerProjectLead:
			return "the project lead (none set)"
		case issueworkflow.HandlerCreator:
			return "the issue creator"
		}
		return ""
	}
	name := h.actorDisplayName(ctx, issue.WorkspaceID, t, id)
	switch step.Handler.Type {
	case issueworkflow.HandlerProjectLead:
		return name + " (project lead)"
	case issueworkflow.HandlerCreator:
		return name + " (issue creator)"
	}
	return name
}

func writeStepReviewFindings(b *strings.Builder, run workmethod.Run) {
	for i := len(run.History) - 1; i >= 0; i-- {
		event := run.History[i]
		if event.Step != run.Step {
			continue
		}
		if event.Action == "approve" {
			return
		}
		if event.Action != "rework" {
			continue
		}
		for _, finding := range event.Findings {
			fmt.Fprintf(b, "Review finding %s %s. Expected: %q. Actual: %q. Reproduce: %q.\n",
				finding.CriterionID, finding.CaseID, finding.Expected, finding.Actual, finding.Evidence)
		}
		return
	}
}
