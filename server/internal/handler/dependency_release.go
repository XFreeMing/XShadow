package handler

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Dependency release is event-driven, not poll-driven.
//
// An issue whose strong prerequisite is not yet accepted must not sit in the
// claim queue: every poll would claim it and reject it again. The claim path
// parks such a task in 'deferred' with no fire_at (see
// DeferAgentTaskForUnmetDependencies), and the only thing that pulls it forward
// is a real change to the prerequisite. These helpers are those events. They do
// not add a scheduler: a released task goes back through the existing deferred
// promotion and claim path, which re-checks the persisted acceptance evidence.

// releaseDependentsOfIssue re-arms work that depends on this issue. Call it
// after a write that may have changed whether this prerequisite is accepted
// (a status write) or after the plan graph itself changed. It is safe to call
// repeatedly: a parked row is armed at most once and the enqueue path coalesces
// on the pending-task index.
func (h *Handler) releaseDependentsOfIssue(ctx context.Context, issue db.Issue) {
	dependents, err := service.DependentIssueIDsForPrerequisite(ctx, h.Queries, issue)
	if err != nil {
		slog.Warn("dependency release: dependents unreadable",
			"issue_id", util.UUIDToString(issue.ID),
			"error", err,
		)
		return
	}
	for _, dependentID := range dependents {
		h.releaseDependentIssue(ctx, dependentID)
	}
}

// releaseDependentIssue releases one dependent issue if it is ready now.
func (h *Handler) releaseDependentIssue(ctx context.Context, issueID pgtype.UUID) {
	issue, err := h.Queries.GetIssue(ctx, issueID)
	if err != nil {
		return
	}
	state, err := service.IssueDependenciesReady(ctx, h.Queries, issue)
	if err != nil || !state.Ready {
		return
	}
	parked, err := h.Queries.ReleaseDeferredDependentTasksForIssue(ctx, issueID)
	if err != nil {
		slog.Warn("dependency release: parked tasks unreadable",
			"issue_id", util.UUIDToString(issueID),
			"error", err,
		)
		return
	}
	if len(parked) > 0 {
		// A task already exists for this issue; promotion is the only step
		// left, and it owns the single-pending fence. Promote immediately so
		// the daemon does not wait for the next poll. An offline runtime keeps
		// the row deferred with a due fire_at and the claim path promotes it
		// when the runtime returns.
		for _, task := range parked {
			if err := h.TaskService.PromoteDueDeferredTasksForRuntime(ctx, task.RuntimeID); err != nil {
				slog.Warn("dependency release: promote parked task failed",
					"task_id", util.UUIDToString(task.ID),
					"issue_id", util.UUIDToString(issueID),
					"error", err,
				)
			}
		}
		return
	}
	// The enqueue predicate dropped this dependent when it was assigned, so no
	// task exists yet. Re-run the same predicate the write path uses; the
	// enqueue itself coalesces on the pending-task index, so a duplicate event
	// cannot dispatch twice.
	trigger, willRun := h.IssueService.WillEnqueueRun(ctx, service.IssueTriggerInput{
		Issue:           issue,
		IsCreate:        false,
		AssigneeChanged: true,
	}, service.IssueTriggerProbe{})
	if !willRun {
		return
	}
	h.dispatchIssueRun(ctx, issue, trigger, "system", "", "")
}

// releaseGoalPlanIssues re-checks every issue bound to a goal plan after the
// graph or its bindings changed (a plan save or a materialize). Each issue is
// only enqueued or promoted when its own prerequisites are accepted now.
func (h *Handler) releaseGoalPlanIssues(ctx context.Context, workspaceID, goalID pgtype.UUID) {
	issueIDs, err := service.GoalBoundIssueIDs(ctx, h.Queries, workspaceID, goalID)
	if err != nil {
		slog.Warn("dependency release: goal bindings unreadable",
			"goal_id", util.UUIDToString(goalID),
			"error", err,
		)
		return
	}
	for _, issueID := range issueIDs {
		h.releaseDependentIssue(ctx, issueID)
	}
}
