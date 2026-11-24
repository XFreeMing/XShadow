package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/attribution"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/projectaccess"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/workmethod"
)

type GoalExecution struct {
	ReviewHold bool
	ID         pgtype.UUID
	By         pgtype.UUID
	Progress   GoalWorkProgress
}

func CanManageGoalExecution(member db.Member, goal db.Goal) bool {
	return member.Role == "owner" || member.Role == "admin" ||
		(goal.CreatorType == "member" && goal.CreatorID == member.UserID)
}

// GoalExecutionScope binds approval to the goal charter and the definitions of
// the methods selected by this saved plan. The plan's revision binds its items.
func GoalExecutionScope(ctx context.Context, q *db.Queries, goal db.Goal, plan db.GoalPlan) (string, error) {
	var content struct {
		Items []goalWorkItem `json:"items"`
	}
	if err := json.Unmarshal(plan.Content, &content); err != nil {
		return "", err
	}
	methods, err := WorkMethodCatalog(ctx, q, goal.WorkspaceID)
	if err != nil {
		return "", err
	}
	selected := make(map[string]workmethod.Template)
	for _, item := range content.Items {
		id := item.Delivery.MethodID
		for _, method := range methods {
			if method.ID == id {
				selected[id] = method
				break
			}
		}
	}
	raw, err := json.Marshal([]any{goal.ProjectID, goal.Title, goal.Intent, goal.Scope, goal.AcceptanceCriteria, selected})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(raw)), nil
}

func ReadGoalExecution(ctx context.Context, q *db.Queries, goal db.Goal, plan db.GoalPlan) (GoalExecution, error) {
	if !plan.ExecutionID.Valid || goal.Status != "in_progress" || goal.ProjectID != plan.ProjectID ||
		!plan.ExecutionRevision.Valid || plan.ExecutionRevision.Int32 != plan.Revision {
		return GoalExecution{}, nil
	}
	member, err := q.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
		UserID: plan.ExecutionBy, WorkspaceID: goal.WorkspaceID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return GoalExecution{}, nil
	}
	if err != nil {
		return GoalExecution{}, err
	}
	if !CanManageGoalExecution(member, goal) {
		return GoalExecution{}, nil
	}
	access, err := projectaccess.ForPrincipals(ctx, q, goal.WorkspaceID, member.UserID)
	if err != nil {
		return GoalExecution{}, err
	}
	if !access.CanSeeProject(goal.ProjectID) {
		return GoalExecution{}, nil
	}
	scope, err := GoalExecutionScope(ctx, q, goal, plan)
	if err != nil || scope != plan.ExecutionScope.String {
		return GoalExecution{}, err
	}
	return GoalExecution{ID: plan.ExecutionID, By: plan.ExecutionBy}, nil
}

func goalExecutionForIssue(ctx context.Context, q *db.Queries, issue db.Issue, lock bool) (GoalExecution, error) {
	link, err := q.GetGoalIssueLink(ctx, db.GetGoalIssueLinkParams{WorkspaceID: issue.WorkspaceID, IssueID: issue.ID})
	if errors.Is(err, pgx.ErrNoRows) {
		return GoalExecution{}, nil
	}
	if err != nil {
		return GoalExecution{}, err
	}
	var goal db.Goal
	if lock {
		goal, err = q.LockGoalForUpdate(ctx, db.LockGoalForUpdateParams{ID: link.GoalID, WorkspaceID: issue.WorkspaceID})
	} else {
		goal, err = q.GetGoalInWorkspace(ctx, db.GetGoalInWorkspaceParams{ID: link.GoalID, WorkspaceID: issue.WorkspaceID})
	}
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (!issue.ProjectID.Valid || issue.ProjectID != goal.ProjectID)) {
		return GoalExecution{}, nil
	}
	if err != nil {
		return GoalExecution{}, err
	}
	plan, err := q.GetGoalPlan(ctx, db.GetGoalPlanParams{WorkspaceID: issue.WorkspaceID, GoalID: goal.ID})
	if errors.Is(err, pgx.ErrNoRows) {
		return GoalExecution{}, nil
	}
	if err != nil {
		return GoalExecution{}, err
	}
	if goal.Status == "in_progress" && (issuestatus.Effective(ctx, q, issue.WorkspaceID, issue.Status) == issuestatus.Todo ||
		issuestatus.Effective(ctx, q, issue.WorkspaceID, issue.Status) == issuestatus.Backlog) {
		hold, err := goalReviewHoldsNewWork(ctx, q, plan)
		if err != nil || hold {
			return GoalExecution{ReviewHold: hold}, err
		}
	}
	execution, err := ReadGoalExecution(ctx, q, goal, plan)
	if err != nil || !execution.ID.Valid {
		return execution, err
	}
	issueID := util.UUIDToString(issue.ID)
	progress, err := readGoalWorkProgress(ctx, q, goal, plan, issueID)
	if err != nil || progress.IssueID != issueID || progress.MethodID == "" ||
		(progress.Phase != "planning" && progress.Phase != "delivering") {
		return GoalExecution{}, err
	}
	execution.Progress = progress
	return execution, nil
}

func goalReviewHoldsNewWork(ctx context.Context, q *db.Queries, plan db.GoalPlan) (bool, error) {
	review, err := q.GetLatestGoalPlanReview(ctx, db.GetLatestGoalPlanReviewParams{WorkspaceID: plan.WorkspaceID, GoalID: plan.GoalID})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if review.BaseRevision != plan.Revision || review.State != "succeeded" {
		return true, nil
	}
	var candidate struct {
		Review struct {
			Decision string `json:"decision"`
		} `json:"review"`
	}
	if err := json.Unmarshal(review.Candidate, &candidate); err != nil {
		return false, err
	}
	if candidate.Review.Decision == "execution_issue" {
		var input struct {
			Review struct {
				Phase string `json:"phase"`
			} `json:"goal_review"`
		}
		if err := json.Unmarshal(review.InputContext, &input); err != nil {
			return false, err
		}
		return input.Review.Phase != "execution", nil
	}
	return candidate.Review.Decision != "covered", nil
}

type GoalExecutionTask struct {
	ID             pgtype.UUID `json:"goal_execution_id"`
	Revision       int         `json:"work_method_revision"`
	Role           string      `json:"continuation_role"`
	CandidateOffer bool        `json:"goal_candidate_offer"`
}

func ReadGoalExecutionTask(task db.AgentTaskQueue) GoalExecutionTask {
	var binding GoalExecutionTask
	if json.Unmarshal(task.Context, &binding) != nil {
		return GoalExecutionTask{}
	}
	return binding
}

func CheckGoalExecutionRecipient(ctx context.Context, q *db.Queries, issue db.Issue, id, user pgtype.UUID) error {
	agent, err := q.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{ID: id, WorkspaceID: issue.WorkspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrWakeupForbidden
	}
	if err != nil {
		return err
	}
	scoped := &TaskService{Queries: q}
	if err := (&IssueWakeupService{Tasks: scoped}).authorize(ctx, q, issue.WorkspaceID, user, agent); err != nil {
		return err
	}
	if err := scoped.guardRunProjectAccess(ctx, issue, agent, user, user); err != nil {
		if errors.Is(err, ErrProjectAccessDenied) {
			return ErrWakeupForbidden
		}
		return err
	}
	return nil
}

// CheckGoalExecutionTask authorizes only the exact issue and current method
// slot of a server-created task. Mutations hold the goal lock through commit.
func CheckGoalExecutionTask(ctx context.Context, q *db.Queries, issue db.Issue, task db.AgentTaskQueue, lock bool) (GoalExecution, error) {
	binding := ReadGoalExecutionTask(task)
	if !binding.ID.Valid || issue.ID != task.IssueID || issue.TriageState.Valid {
		return GoalExecution{}, ErrWakeupForbidden
	}
	execution, err := goalExecutionForIssue(ctx, q, issue, lock)
	if err != nil {
		return GoalExecution{}, err
	}
	if execution.ID != binding.ID {
		return GoalExecution{}, ErrWakeupForbidden
	}
	if lock {
		current, err := q.LockWakeupIssue(ctx, issue.ID)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && current.Revision != issue.Revision) {
			return GoalExecution{}, ErrWakeupForbidden
		}
		if err != nil {
			return GoalExecution{}, err
		}
	}
	var run workmethod.Run
	if len(issue.WorkMethodRun) > 0 {
		if err := json.Unmarshal(issue.WorkMethodRun, &run); err != nil {
			return GoalExecution{}, err
		}
	}
	switch {
	case binding.Revision == 0:
		if task.TriggerEvidenceKind.String != "issue_continuation" || binding.Role != "coordinator" ||
			execution.Progress.Phase != "planning" || (run.State != "" && run.State != "ready") {
			return GoalExecution{}, ErrWakeupForbidden
		}
	default:
		if !run.GoalExecution || run.Method == nil || run.Method.ID != execution.Progress.MethodID ||
			!run.AcceptsTaskRevision(binding.Revision) || (run.State != "working" && run.State != "review") {
			return GoalExecution{}, ErrWakeupForbidden
		}
		recipient, err := continuationOwner(ctx, q, issue, run)
		if err != nil {
			return GoalExecution{}, err
		}
		if recipient.SquadID != task.SquadID {
			return GoalExecution{}, ErrWakeupForbidden
		}
		if task.SquadID.Valid && !task.IsLeaderTask {
			if !run.CanDelegateCandidate() || task.AgentID == recipient.Agent.ID {
				return GoalExecution{}, ErrWakeupForbidden
			}
			if err := checkGoalCandidateWorker(ctx, q, task.SquadID, task.AgentID); err != nil {
				return GoalExecution{}, err
			}
			if IsGoalCandidateOffer(task) && !run.UsesCandidateSelfClaim() {
				return GoalExecution{}, ErrWakeupForbidden
			}
		} else if recipient.Agent.ID != task.AgentID {
			return GoalExecution{}, ErrWakeupForbidden
		}
	}
	if err := CheckGoalExecutionRecipient(ctx, q, issue, task.AgentID, execution.By); err != nil {
		return GoalExecution{}, err
	}
	return execution, nil
}

// Older servers may have queued a generic recovery for a formal goal step.
func rejectLegacyGoalRecovery(ctx context.Context, q *db.Queries, task db.AgentTaskQueue) error {
	if task.TriggerEvidenceKind.String != string(attribution.EvidenceDelegatedFailure) || !task.TriggerEvidenceRefID.Valid {
		return nil
	}
	failed, err := q.GetAgentTask(ctx, task.TriggerEvidenceRefID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if ReadGoalExecutionTask(failed).ID.Valid {
		return ErrWakeupForbidden
	}
	return nil
}

// Recheck between claim preparation and daemon start. A queued run carries no
// lasting right to launch after approval is revoked.
func verifyGoalExecutionStart(ctx context.Context, q *db.Queries, taskID pgtype.UUID) error {
	task, err := q.GetAgentTask(ctx, taskID)
	if err != nil || task.Status == "running" {
		return err
	}
	if err := rejectLegacyGoalRecovery(ctx, q, task); err != nil {
		return err
	}
	if !ReadGoalExecutionTask(task).ID.Valid {
		return nil
	}
	issue, err := q.GetIssue(ctx, task.IssueID)
	if err != nil {
		return err
	}
	_, err = CheckGoalExecutionTask(ctx, q, issue, task, true)
	return err
}
