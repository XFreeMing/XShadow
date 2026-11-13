package handler

// Execution of a saved goal plan (DEV-7). A plan is a draft: `binding.type =
// "new"` only states intent. Materialization turns each intent into exactly one
// issue, keyed by the stable plan item id, and persists the binding in
// goal_plan_item_issue so a retried or concurrent trigger resolves the same
// issue instead of creating a second one.
//
// What this endpoint does NOT do: it does not rewrite the saved plan (the item
// intent stays readable), it does not guess an assignee from proposed_owner
// (that field is a suggestion, not ownership), and it does not add a scheduler.
// An assigned item starts through the ordinary enqueue path — immediately when
// its strong prerequisites are accepted, otherwise it is parked and released by
// the real dependency-change event.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

// MaterializeGoalPlanItemRequest binds one planned item to an optional
// assignee. An absent assignee creates the issue unassigned; ownership stays a
// human decision.
type MaterializeGoalPlanItemRequest struct {
	ItemID       string `json:"item_id"`
	AssigneeType string `json:"assignee_type,omitempty"`
	AssigneeID   string `json:"assignee_id,omitempty"`
}

// MaterializeGoalPlanRequest selects which new items to materialize. An empty
// Items list materializes every `binding.type = "new"` item that has no
// persisted materialization yet.
type MaterializeGoalPlanRequest struct {
	Items []MaterializeGoalPlanItemRequest `json:"items"`
}

// PlanDependencyState is the release verdict for one materialized item.
type PlanDependencyState struct {
	Ready  bool   `json:"ready"`
	Reason string `json:"reason,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// MaterializedGoalPlanItem is the stable per-item result. Repeating the same
// request returns the same issue_id with created=false.
type MaterializedGoalPlanItem struct {
	ItemID         string              `json:"item_id"`
	IssueID        string              `json:"issue_id"`
	Created        bool                `json:"created"`
	BindingType    string              `json:"binding_type"`
	AssigneeType   string              `json:"assignee_type,omitempty"`
	AssigneeID     *string             `json:"assignee_id,omitempty"`
	Dependency     PlanDependencyState `json:"dependency"`
	TaskEnqueued   bool                `json:"task_enqueued"`
	TaskEnqueuedID *string             `json:"task_id,omitempty"`
}

type MaterializeGoalPlanResponse struct {
	GoalID       string                     `json:"goal_id"`
	PlanRevision int32                      `json:"plan_revision"`
	Items        []MaterializedGoalPlanItem `json:"items"`
}

// MaterializeGoalPlan converts the requested `binding.type = "new"` plan items
// into issues. It is idempotent per (goal, item id).
func (h *Handler) MaterializeGoalPlan(w http.ResponseWriter, r *http.Request) {
	goal, ok := h.loadVisibleGoal(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var req MaterializeGoalPlanRequest
	if r.ContentLength != 0 {
		if !h.mustDecodeJSON(w, r, &req) {
			return
		}
	}
	plan, err := h.Queries.GetGoalPlan(r.Context(), db.GetGoalPlanParams{
		WorkspaceID: goal.WorkspaceID,
		GoalID:      goal.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeErrorCode(w, http.StatusConflict, "plan_required",
			"save a plan before materializing its items")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load plan")
		return
	}
	var content GoalPlanContent
	if err := json.Unmarshal(plan.Content, &content); err != nil {
		writeError(w, http.StatusInternalServerError, "the saved plan could not be read")
		return
	}

	// Resolve the selection: explicit item ids must exist in the saved plan.
	selected := make(map[string]MaterializeGoalPlanItemRequest, len(req.Items))
	for _, item := range req.Items {
		itemID := strings.TrimSpace(item.ItemID)
		if itemID == "" {
			writeError(w, http.StatusBadRequest, "items[].item_id is required")
			return
		}
		if _, dup := selected[itemID]; dup {
			continue
		}
		selected[itemID] = item
	}
	if len(selected) > 0 {
		known := make(map[string]bool, len(content.Items))
		for _, item := range content.Items {
			known[item.ID] = true
		}
		for itemID := range selected {
			if !known[itemID] {
				writeError(w, http.StatusBadRequest, "plan item "+itemID+" does not exist in the saved plan")
				return
			}
		}
	}

	resp := MaterializeGoalPlanResponse{
		GoalID:       uuidToString(goal.ID),
		PlanRevision: plan.Revision,
		Items:        make([]MaterializedGoalPlanItem, 0, len(content.Items)),
	}
	for _, item := range content.Items {
		request, requested := selected[item.ID]
		if len(selected) > 0 && !requested {
			continue
		}
		if bound := strings.TrimSpace(derefString(item.Binding.IssueID)); bound != "" {
			// Already bound by the user: report it, never materialize it again.
			if requested {
				if id, err := util.ParseUUID(bound); err == nil {
					resp.Items = append(resp.Items, h.finishMaterializedItem(r.Context(), id, false, item.ID, "existing", false, pgtype.UUID{}))
				}
			}
			continue
		}
		if item.Binding.Type != "new" {
			continue
		}
		result, ok := h.materializePlanItem(r.Context(), w, r, goal, plan, item, request, userID)
		if !ok {
			return
		}
		resp.Items = append(resp.Items, result)
	}

	// Bindings and readiness may have changed: give every already-bound item
	// one release check through the ordinary event path.
	h.releaseGoalPlanIssues(r.Context(), goal.WorkspaceID, goal.ID)

	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) materializePlanItem(ctx context.Context, w http.ResponseWriter, r *http.Request, goal db.Goal, plan db.GoalPlan, item GoalPlanItem, request MaterializeGoalPlanItemRequest, userID string) (MaterializedGoalPlanItem, bool) {
	result := MaterializedGoalPlanItem{ItemID: item.ID, BindingType: "new"}

	// A persisted materialization wins: this is the idempotent replay path.
	if mapping, err := h.Queries.GetGoalPlanItemIssue(ctx, db.GetGoalPlanItemIssueParams{
		GoalID: goal.ID, ItemID: item.ID,
	}); err == nil {
		return h.finishMaterializedItem(ctx, mapping.IssueID, false, item.ID, "new", false, pgtype.UUID{}), true
	} else if !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "failed to read plan item binding")
		return result, false
	}

	// Deterministic id: a crash between issue insert and binding insert, or two
	// concurrent triggers, converge on the same row instead of forking.
	issueID := dbid.FromName("goal-plan-item", uuidToString(goal.WorkspaceID), uuidToString(goal.ID), item.ID)

	created := false
	if _, err := h.Queries.GetIssue(ctx, issueID); errors.Is(err, pgx.ErrNoRows) {
		assigneeType, assigneeID, ok := h.planItemAssignee(w, r, goal, request)
		if !ok {
			return result, false
		}
		creatorID, _ := util.ParseUUID(userID)
		_, createErr := h.IssueService.Create(ctx, service.IssueCreateParams{
			ID:             issueID,
			WorkspaceID:    goal.WorkspaceID,
			Title:          item.Title,
			Description:    planItemDescription(item),
			Status:         "todo",
			Priority:       "none",
			AssigneeType:   assigneeType,
			AssigneeID:     assigneeID,
			CreatorType:    "member",
			CreatorID:      creatorID,
			ProjectID:      goal.ProjectID,
			AllowDuplicate: true,
		}, service.IssueCreateOpts{
			ActorID:  userID,
			Platform: "web",
			// The goal link and item binding below are what make the dependency
			// gate aware of this issue. Create must not enqueue before they are
			// committed; the enqueue happens after both writes.
			SuppressAssignedRunEnqueue: true,
		})
		if createErr != nil {
			// A concurrent materialize can win the primary key. If the row it
			// committed is readable, converge on it instead of failing.
			if _, readErr := h.Queries.GetIssue(ctx, issueID); readErr != nil {
				slog.Error("plan materialize: issue create failed", "item_id", item.ID, "error", createErr)
				writeError(w, http.StatusInternalServerError, "failed to create the issue for plan item "+item.ID)
				return result, false
			}
		} else {
			created = true
		}
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to resolve the issue for plan item "+item.ID)
		return result, false
	}

	// The goal link is part of the binding: without it the issue is not under
	// the closure contract and its dependents cannot resolve the item.
	if err := h.ensureGoalIssueLinked(ctx, goal, issueID); err != nil {
		slog.Error("plan materialize: goal link failed",
			"goal_id", uuidToString(goal.ID),
			"item_id", item.ID,
			"issue_id", uuidToString(issueID),
			"error", err,
		)
		writeError(w, http.StatusInternalServerError, "failed to link the issue into the goal")
		return result, false
	}
	mapping, err := h.Queries.CreateGoalPlanItemIssue(ctx, db.CreateGoalPlanItemIssueParams{
		WorkspaceID:  goal.WorkspaceID,
		GoalID:       goal.ID,
		ItemID:       item.ID,
		IssueID:      issueID,
		PlanRevision: plan.Revision,
	})
	// boundHere is true only when this call inserted the (goal_id, item_id)
	// binding. A concurrent materialize that already bound the item owns its own
	// enqueue; this call must not add a second one.
	boundHere := err == nil
	if errors.Is(err, pgx.ErrNoRows) {
		// Lost the (goal_id, item_id) race: the winner's binding stands.
		mapping, err = h.Queries.GetGoalPlanItemIssue(ctx, db.GetGoalPlanItemIssueParams{
			GoalID: goal.ID, ItemID: item.ID,
		})
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to persist the plan item binding")
		return result, false
	}
	// The goal link and binding above are committed, so the dependency gate now
	// resolves this item's prerequisites. Only here may an assigned item enter
	// the queue: enqueueing during Create let a dependent be claimed while it
	// still looked unconstrained. WillEnqueueRun is the same predicate every
	// issue write uses, and a rejected dependent stays taskless until the real
	// dependency event releases it.
	taskEnqueued := false
	var taskID pgtype.UUID
	if created || boundHere {
		issue, loadErr := h.Queries.GetIssue(ctx, issueID)
		if loadErr != nil {
			slog.Error("plan materialize: bound issue unreadable",
				"goal_id", uuidToString(goal.ID),
				"item_id", item.ID,
				"issue_id", uuidToString(issueID),
				"error", loadErr,
			)
		} else if trigger, willRun := h.IssueService.WillEnqueueRun(ctx, service.IssueTriggerInput{
			Issue:    issue,
			IsCreate: true,
		}, service.IssueTriggerProbe{}); willRun {
			taskID, taskEnqueued = h.dispatchIssueRun(ctx, issue, trigger, "member", userID, "")
		}
	}
	return h.finishMaterializedItem(ctx, mapping.IssueID, created, item.ID, "new", taskEnqueued, taskID), true
}

// finishMaterializedItem reports the binding and the current dependency
// verdict. It never waits for a run: a dependent item that is not ready yet
// stays parked until its prerequisite changes.
func (h *Handler) finishMaterializedItem(ctx context.Context, issueID pgtype.UUID, created bool, itemID, bindingType string, taskEnqueued bool, taskID pgtype.UUID) MaterializedGoalPlanItem {
	result := MaterializedGoalPlanItem{
		ItemID:       itemID,
		IssueID:      uuidToString(issueID),
		Created:      created,
		BindingType:  bindingType,
		TaskEnqueued: taskEnqueued,
	}
	if taskID.Valid {
		id := uuidToString(taskID)
		result.TaskEnqueuedID = &id
	}
	issue, err := h.Queries.GetIssue(ctx, issueID)
	if err != nil {
		result.Dependency = PlanDependencyState{Reason: service.DependencyReasonUnreadable, Detail: "the bound issue is not readable"}
		return result
	}
	state, stateErr := service.IssueDependenciesReady(ctx, h.Queries, issue)
	if stateErr != nil {
		result.Dependency = PlanDependencyState{Reason: service.DependencyReasonUnreadable, Detail: stateErr.Error()}
	} else {
		result.Dependency = PlanDependencyState{Ready: state.Ready, Reason: state.Reason, Detail: state.Detail}
	}
	if issue.AssigneeType.Valid {
		result.AssigneeType = issue.AssigneeType.String
	}
	if issue.AssigneeID.Valid {
		id := uuidToString(issue.AssigneeID)
		result.AssigneeID = &id
	}
	return result
}

func (h *Handler) planItemAssignee(w http.ResponseWriter, r *http.Request, goal db.Goal, request MaterializeGoalPlanItemRequest) (pgtype.Text, pgtype.UUID, bool) {
	assigneeType := strings.TrimSpace(request.AssigneeType)
	assigneeIDText := strings.TrimSpace(request.AssigneeID)
	if assigneeType == "" && assigneeIDText == "" {
		return pgtype.Text{}, pgtype.UUID{}, true
	}
	if assigneeType != "agent" && assigneeType != "squad" && assigneeType != "member" {
		writeError(w, http.StatusBadRequest, "assignee_type must be agent, squad or member")
		return pgtype.Text{}, pgtype.UUID{}, false
	}
	assigneeID, err := util.ParseUUID(assigneeIDText)
	if err != nil {
		writeError(w, http.StatusBadRequest, "assignee_id is invalid")
		return pgtype.Text{}, pgtype.UUID{}, false
	}
	status, message := h.validateAssigneePair(r.Context(), r, uuidToString(goal.WorkspaceID), pgtype.Text{String: assigneeType, Valid: true}, assigneeID)
	if status != 0 {
		writeError(w, status, message)
		return pgtype.Text{}, pgtype.UUID{}, false
	}
	return pgtype.Text{String: assigneeType, Valid: true}, assigneeID, true
}

func (h *Handler) ensureGoalIssueLinked(ctx context.Context, goal db.Goal, issueID pgtype.UUID) error {
	if _, err := h.Queries.LinkGoalIssue(ctx, db.LinkGoalIssueParams{
		WorkspaceID: goal.WorkspaceID,
		GoalID:      goal.ID,
		IssueID:     issueID,
	}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	link, err := h.Queries.GetGoalIssueLink(ctx, db.GetGoalIssueLinkParams{
		WorkspaceID: goal.WorkspaceID,
		IssueID:     issueID,
	})
	if err != nil {
		return err
	}
	if uuidToString(link.GoalID) != uuidToString(goal.ID) {
		return fmt.Errorf("issue already belongs to another goal")
	}
	return nil
}

func planItemDescription(item GoalPlanItem) pgtype.Text {
	parts := make([]string, 0, 3)
	if outcome := strings.TrimSpace(item.Outcome); outcome != "" {
		parts = append(parts, outcome)
	}
	if criteria := strings.TrimSpace(item.AcceptanceCriteria); criteria != "" {
		parts = append(parts, "Acceptance criteria:\n"+criteria)
	}
	if item.Coordination != nil {
		if scope := strings.TrimSpace(strings.Join(item.Coordination.WriteScope, ", ")); scope != "" {
			parts = append(parts, "Write scope: "+scope)
		}
	}
	if len(parts) == 0 {
		return pgtype.Text{}
	}
	return pgtype.Text{String: strings.Join(parts, "\n\n"), Valid: true}
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
