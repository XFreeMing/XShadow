package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/workmethod"
)

type GoalWorkProgress struct {
	Phase       string   `json:"phase"`
	Total       int      `json:"total"`
	Completed   int      `json:"completed"`
	MethodID    string   `json:"-"`
	ItemID      string   `json:"item_id,omitempty"`
	IssueID     string   `json:"issue_id,omitempty"`
	IssueStatus string   `json:"issue_status,omitempty"`
	MethodState string   `json:"method_state,omitempty"`
	MethodStep  string   `json:"method_step,omitempty"`
	WaitingFor  []string `json:"waiting_for,omitempty"`
}

type goalWorkItem struct {
	Delivery struct {
		MethodID string `json:"method_id"`
	} `json:"delivery"`
	ID      string `json:"id"`
	Stage   *int   `json:"stage"`
	Binding struct {
		IssueID string `json:"issue_id"`
	} `json:"binding"`
}

// ReadGoalWorkProgress projects the saved delivery sequence onto linked issue
// state. It does not advance work or interpret a plan as implementation approval.
func ReadGoalWorkProgress(ctx context.Context, q *db.Queries, goal db.Goal, plan db.GoalPlan) (GoalWorkProgress, error) {
	var content struct {
		Items []goalWorkItem `json:"items"`
		Edges []struct {
			Dependent    string `json:"dependent_item_id"`
			Prerequisite string `json:"prerequisite_item_id"`
			Kind         string `json:"kind"`
		} `json:"edges"`
	}
	if err := json.Unmarshal(plan.Content, &content); err != nil {
		return GoalWorkProgress{}, err
	}
	progress := GoalWorkProgress{Total: len(content.Items), Phase: "planning"}
	if progress.Total == 0 {
		return progress, nil
	}
	states := make(map[string]string, progress.Total)
	linked := make(map[string]db.Issue, progress.Total)
	for offset := int32(0); ; offset += 100 {
		rows, err := q.ListGoalIssues(ctx, db.ListGoalIssuesParams{
			WorkspaceID: goal.WorkspaceID, GoalID: goal.ID, Offset: offset, Limit: 100,
		})
		if err != nil {
			return GoalWorkProgress{}, err
		}
		for _, issue := range rows {
			id := util.UUIDToString(issue.ID)
			linked[id] = issue
			state := issuestatus.Effective(ctx, q, goal.WorkspaceID, issue.Status)
			if state == issuestatus.Done && len(issue.WorkMethodRun) > 0 {
				var run workmethod.Run
				if err := json.Unmarshal(issue.WorkMethodRun, &run); err != nil {
					return GoalWorkProgress{}, err
				}
				if run.GoalExecution && run.State != "closed" {
					state = issuestatus.Blocked
				}
			}
			states[id] = state
		}
		if len(rows) < 100 {
			break
		}
	}

	sort.SliceStable(content.Items, func(a, b int) bool {
		left, right := content.Items[a].Stage, content.Items[b].Stage
		return left != nil && (right == nil || *left < *right)
	})
	byID := make(map[string]goalWorkItem, progress.Total)
	var activeItem goalWorkItem
	for _, item := range content.Items {
		byID[item.ID] = item
		switch states[item.Binding.IssueID] {
		case issuestatus.Done:
			progress.Completed++
			continue
		case issuestatus.InProgress, issuestatus.InReview, issuestatus.Blocked:
			if activeItem.ID == "" {
				activeItem = item
			}
		}
		if progress.ItemID == "" {
			progress.ItemID = item.ID
			progress.IssueID = item.Binding.IssueID
		}
	}
	// A reordered draft must still follow the story that is in delivery.
	if activeItem.ID != "" {
		progress.ItemID = activeItem.ID
		progress.IssueID = activeItem.Binding.IssueID
	}
	if progress.ItemID == "" {
		progress.Phase = "awaiting_goal_review"
		if goal.Status == "completed" {
			progress.Phase = "goal_completed"
		}
		return progress, nil
	}
	current, ok := linked[progress.IssueID]
	if !ok {
		progress.Phase = "waiting_for_binding"
		return progress, nil
	}
	progress.IssueStatus = current.Status
	progress.MethodID = byID[progress.ItemID].Delivery.MethodID
	for _, edge := range content.Edges {
		if edge.Dependent == progress.ItemID && (edge.Kind == "" || edge.Kind == "required") &&
			states[byID[edge.Prerequisite].Binding.IssueID] != issuestatus.Done {
			progress.WaitingFor = append(progress.WaitingFor, edge.Prerequisite)
		}
	}
	switch {
	case goal.Status != "in_progress":
		progress.Phase = "goal_inactive"
	case len(progress.WaitingFor) > 0:
		progress.Phase = "waiting_for_prerequisite"
	case states[progress.IssueID] == issuestatus.Cancelled:
		progress.Phase = "needs_replan"
	case states[progress.IssueID] == issuestatus.Blocked:
		progress.Phase = "blocked"
	case states[progress.IssueID] == issuestatus.Backlog || states[progress.IssueID] == issuestatus.Todo:
		progress.Phase = "planning"
	default:
		progress.Phase = "delivering"
	}
	if len(current.WorkMethodRun) > 0 {
		var run workmethod.Run
		if err := json.Unmarshal(current.WorkMethodRun, &run); err != nil {
			return GoalWorkProgress{}, err
		}
		progress.MethodState = run.State
		if run.Method != nil && run.Step >= 0 && run.Step < len(run.Method.Steps) {
			progress.MethodStep = run.Method.Steps[run.Step].Name
		}
	}
	return progress, nil
}

type goalBacklogGate struct {
	managed bool
	ready   bool
	key     string
	note    string
}

func goalBacklogPosition(ctx context.Context, q *db.Queries, issue db.Issue) (goalBacklogGate, error) {
	link, err := q.GetGoalIssueLink(ctx, db.GetGoalIssueLinkParams{WorkspaceID: issue.WorkspaceID, IssueID: issue.ID})
	if errors.Is(err, pgx.ErrNoRows) {
		return goalBacklogGate{}, nil
	}
	if err != nil {
		return goalBacklogGate{}, err
	}
	plan, err := q.GetGoalPlan(ctx, db.GetGoalPlanParams{WorkspaceID: issue.WorkspaceID, GoalID: link.GoalID})
	if errors.Is(err, pgx.ErrNoRows) {
		return goalBacklogGate{}, nil
	}
	if err != nil {
		return goalBacklogGate{}, err
	}
	goal, err := q.GetGoalInWorkspace(ctx, db.GetGoalInWorkspaceParams{WorkspaceID: issue.WorkspaceID, ID: link.GoalID})
	if err != nil {
		return goalBacklogGate{}, err
	}
	progress, err := ReadGoalWorkProgress(ctx, q, goal, plan)
	if err != nil {
		return goalBacklogGate{}, err
	}
	gate := goalBacklogGate{managed: progress.Total > 0}
	if progress.Phase != "planning" || progress.IssueID != util.UUIDToString(issue.ID) {
		return gate, nil
	}
	// Deduplicate by delivery facts, excluding incidental context snapshot
	// refreshes and revision increments that do not change the plan.
	facts, err := json.Marshal([]any{goal.ID, goal.Intent, goal.Scope, goal.AcceptanceCriteria, json.RawMessage(plan.Content), progress})
	if err != nil {
		return gate, err
	}
	gate.ready = true
	gate.key = fmt.Sprintf("goal/%x", sha256.Sum256(facts))
	gate.note = fmt.Sprintf(
		"目标 %s 的当前计划第 %d 版已验收 %d/%d 项。本卡是当前顺序中的下一项。请按现行目标、原事项和必需前置核对验收范围及接手人。只记录新增的可验收结果、阻塞和接续条件；计划调整提出带依据的候选，保留已验收证据；实施须有有效授权并沿原工作方法独立验收。",
		util.UUIDToString(goal.ID), plan.Revision, progress.Completed, progress.Total,
	)
	return gate, nil
}
