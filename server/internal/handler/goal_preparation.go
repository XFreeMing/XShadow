package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/issueposition"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/issueworkflow"
	"github.com/multica-ai/multica/server/internal/projectaccess"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/multica-ai/multica/server/pkg/workmethod"
)

func bindGoalPreparationIssues(items []GoalPlanItem, bindings map[string]string) {
	for i := range items {
		item := &items[i]
		if id, ok := bindings[item.ID]; ok && item.Binding.Type == "new" {
			item.Binding = GoalPlanItemBinding{Type: "existing", IssueID: &id}
		}
	}
}

// The issue link is execution state. An unrelated plan edit must not persist
// its projected binding as part of the reviewed plan revision.
func removeProjectedGoalBindings(content *GoalPlanContent, goalID pgtype.UUID) {
	for i := range content.Items {
		item := &content.Items[i]
		if item.Binding.IssueID != nil && *item.Binding.IssueID == uuidToString(service.GoalPreparationIssueID(goalID, item.ID)) {
			item.Binding = GoalPlanItemBinding{Type: "new"}
		}
	}
}

func (h *Handler) materializeGoalPreparation(ctx context.Context, goal db.Goal, plan db.GoalPlan, review db.GoalPlanGeneration, owner pgtype.UUID, progress service.GoalWorkProgress) error {
	ready := make(map[string]bool)
	for _, node := range progress.ReadyToPrepare {
		if node.IssueID == "" {
			ready[node.ItemID] = true
		}
	}
	if len(ready) == 0 {
		return nil
	}
	var content GoalPlanContent
	if err := json.Unmarshal(plan.Content, &content); err != nil {
		return err
	}
	var failures []error
	for _, item := range content.Items {
		if !ready[item.ID] || item.Binding.Type != "new" || item.Delivery == nil || len(item.Delivery.Criteria) == 0 {
			continue
		}
		issue, err := h.createGoalPreparationIssue(ctx, goal, review, owner, item)
		if err != nil {
			failures = append(failures, fmt.Errorf("goal preparation %s/%s: %w", uuidToString(goal.ID), item.ID, err))
			continue
		}
		if issue.ID.Valid {
			workspaceID := uuidToString(goal.WorkspaceID)
			h.publish(protocol.EventIssueCreated, workspaceID, "", "", map[string]any{
				"issue": issueToResponse(issue, h.getIssuePrefix(ctx, goal.WorkspaceID)),
			})
			h.publishGoalChanged(workspaceID, "", "")
		}
	}
	return errors.Join(failures...)
}

func (h *Handler) createGoalPreparationIssue(ctx context.Context, goal db.Goal, review db.GoalPlanGeneration, owner pgtype.UUID, item GoalPlanItem) (db.Issue, error) {
	var empty db.Issue
	id := service.GoalPreparationIssueID(goal.ID, item.ID)
	// An explicitly unlinked, moved, or archived existing card is never
	// replaced or silently reclaimed by a later sweep.
	if _, err := h.Queries.GetIssue(ctx, id); !errors.Is(err, pgx.ErrNoRows) {
		return empty, err
	}
	policy := service.ResolveIssueCountPolicy(ctx, h.Entitlements, goal.WorkspaceID)
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback(ctx)
	q := h.Queries.WithTx(tx)
	if _, err := q.LockWorkspaceForChatSessionCreate(ctx, goal.WorkspaceID); err != nil {
		return empty, err
	}
	if _, err := q.LockProjectForChatSessionCreate(ctx, db.LockProjectForChatSessionCreateParams{ID: goal.ProjectID, WorkspaceID: goal.WorkspaceID}); err != nil {
		return empty, err
	}
	locked, err := q.LockGoalForUpdate(ctx, db.LockGoalForUpdateParams{ID: goal.ID, WorkspaceID: goal.WorkspaceID})
	if err != nil || locked.ProjectID != goal.ProjectID || locked.Status != "in_progress" {
		return empty, err
	}
	latest, err := q.GetLatestGoalPlanReview(ctx, db.GetLatestGoalPlanReviewParams{WorkspaceID: goal.WorkspaceID, GoalID: goal.ID})
	if err != nil || latest.ID != review.ID || latest.State != "succeeded" {
		return empty, err
	}
	plan, err := q.GetGoalPlan(ctx, db.GetGoalPlanParams{WorkspaceID: goal.WorkspaceID, GoalID: goal.ID})
	if err != nil || plan.Revision != review.BaseRevision {
		return empty, err
	}
	member, err := q.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{UserID: owner, WorkspaceID: goal.WorkspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		return empty, nil
	}
	if err != nil || !service.CanManageGoalExecution(member, locked) {
		return empty, err
	}
	access, err := projectaccess.ForPrincipals(ctx, q, goal.WorkspaceID, owner)
	if err != nil || !access.CanSeeProject(goal.ProjectID) {
		return empty, err
	}
	progress, err := service.ReadGoalWorkProgress(ctx, q, locked, plan)
	if err != nil {
		return empty, err
	}
	eligible := false
	for _, node := range progress.ReadyToPrepare {
		if node.ItemID == item.ID && node.IssueID == "" {
			eligible = true
			break
		}
	}
	if !eligible {
		return empty, nil
	}
	if _, err := q.GetIssue(ctx, id); !errors.Is(err, pgx.ErrNoRows) {
		return empty, err
	}
	workflow, _, err := issueworkflow.ForProject(ctx, q, goal.WorkspaceID, goal.ProjectID)
	if err != nil {
		return empty, err
	}
	if err := issueworkflow.CheckStatus(workflow, issuestatus.Backlog); err != nil {
		return empty, err
	}
	number, err := service.AllocateIssueNumber(ctx, q, goal.WorkspaceID, policy)
	if err != nil {
		return empty, err
	}
	position, err := issueposition.NextTopPosition(ctx, tx, goal.WorkspaceID, issuestatus.Backlog)
	if err != nil {
		return empty, err
	}
	description := fmt.Sprintf("## 目标\n%s\n\n## 用户成果\n%s\n\n## 验收条件\n%s", locked.Title, item.Outcome, item.AcceptanceCriteria)
	methods, err := service.WorkMethodCatalog(ctx, q, goal.WorkspaceID)
	if err != nil {
		return empty, err
	}
	var issueTypeID pgtype.UUID
	var properties []byte
	method, found := workmethod.ResolvePlanMethod(methods, item.Delivery.TaskTemplateID, item.Delivery.MethodID)
	if !found && item.Delivery.TaskTemplateID != "" {
		return empty, fmt.Errorf("task template %s has no active work method", item.Delivery.TaskTemplateID)
	}
	if found && method.PlanTask != nil {
		issueTypeID, properties, err = tableTaskProperties(ctx, tx, q, goal.WorkspaceID, *method.PlanTask, item.Outcome)
		if err != nil {
			return empty, err
		}
	}
	issue, err := q.CreateIssue(ctx, db.CreateIssueParams{
		ID: id, WorkspaceID: goal.WorkspaceID, ProjectID: goal.ProjectID, Number: number,
		Title: item.Title, Description: pgtype.Text{String: description, Valid: true},
		Status: issuestatus.Backlog, Priority: "none", Position: position,
		CreatorType: "member", CreatorID: owner,
		IssueTypeID: issueTypeID, Properties: properties,
	})
	if err != nil {
		return empty, err
	}
	if _, err = q.LinkGoalIssue(ctx, db.LinkGoalIssueParams{WorkspaceID: goal.WorkspaceID, GoalID: goal.ID, IssueID: issue.ID}); err != nil {
		return empty, err
	}
	if err := tx.Commit(ctx); err != nil {
		return empty, err
	}
	return issue, nil
}
