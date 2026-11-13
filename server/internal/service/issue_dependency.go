package service

// Downstream release of a managed issue. A goal plan declares strong
// prerequisites between its items (GoalPlanEdge); an item bound to an existing
// issue therefore makes that issue depend on the prerequisite item's issue.
//
// Release is not a status check. The prerequisite must be in the success
// terminal category AND carry a currently valid persisted closure acceptance.
// That is what keeps a human or an existing automation (for example a merged PR
// writing done) from releasing a dependent without the acceptance record the
// closure contract requires. Unknown evidence - an unreadable plan, an
// unbound prerequisite, a catalog read failure - never counts as satisfied.
//
// The check is deliberately read-only and shared by the enqueue predicate and
// the claim path; it starts no run and owns no queue.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Stable reasons a dependent issue is not released.
const (
	DependencyReasonUnknown     = "dependency_unknown"
	DependencyReasonNotDone     = "dependency_not_done"
	DependencyReasonNotAccepted = "dependency_not_accepted"
	DependencyReasonUnreadable  = "dependency_unreadable"
)

// IssueDependencyState is the release verdict for one managed issue.
type IssueDependencyState struct {
	Ready  bool
	Reason string
	Detail string
}

func dependencyNotReady(reason, format string, args ...any) IssueDependencyState {
	return IssueDependencyState{Reason: reason, Detail: fmt.Sprintf(format, args...)}
}

// goalPlanDependencyContent mirrors only the fields release needs from the
// saved plan. The full plan types live in the handler; decoding the same JSON
// here keeps this package independent of the transport layer.
type goalPlanDependencyContent struct {
	Items []struct {
		ID      string `json:"id"`
		Binding struct {
			Type    string `json:"type"`
			IssueID string `json:"issue_id"`
		} `json:"binding"`
	} `json:"items"`
	Edges []struct {
		DependentItemID    string `json:"dependent_item_id"`
		PrerequisiteItemID string `json:"prerequisite_item_id"`
	} `json:"edges"`
}

// IssueDependenciesReady reports whether every strong prerequisite of this
// issue is successfully accepted. An issue with no goal link, no saved plan or
// no plan item is not under a declared dependency and is released unchanged.
func IssueDependenciesReady(ctx context.Context, q *db.Queries, issue db.Issue) (IssueDependencyState, error) {
	link, err := q.GetGoalIssueLink(ctx, db.GetGoalIssueLinkParams{
		WorkspaceID: issue.WorkspaceID,
		IssueID:     issue.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return IssueDependencyState{Ready: true}, nil
	}
	if err != nil {
		return IssueDependencyState{}, err
	}
	plan, err := q.GetGoalPlan(ctx, db.GetGoalPlanParams{
		WorkspaceID: issue.WorkspaceID,
		GoalID:      link.GoalID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return IssueDependencyState{Ready: true}, nil
	}
	if err != nil {
		return IssueDependencyState{}, err
	}
	var content goalPlanDependencyContent
	if err := json.Unmarshal(plan.Content, &content); err != nil {
		return dependencyNotReady(DependencyReasonUnreadable, "the saved plan could not be read: %v", err), nil
	}
	issueID := util.UUIDToString(issue.ID)
	itemIssue := make(map[string]string, len(content.Items))
	issueItem := ""
	for _, item := range content.Items {
		if item.ID == "" {
			continue
		}
		bound := strings.TrimSpace(item.Binding.IssueID)
		if bound == "" {
			continue
		}
		itemIssue[item.ID] = bound
		if bound == issueID {
			issueItem = item.ID
		}
	}
	if issueItem == "" {
		return IssueDependencyState{Ready: true}, nil
	}

	checked := make(map[string]bool, len(content.Edges))
	for _, edge := range content.Edges {
		if edge.DependentItemID != issueItem {
			continue
		}
		prerequisite, ok := itemIssue[edge.PrerequisiteItemID]
		if !ok {
			return dependencyNotReady(DependencyReasonUnknown,
				"prerequisite plan item %q has no linked issue, so its success cannot be verified", edge.PrerequisiteItemID), nil
		}
		if checked[prerequisite] {
			continue
		}
		checked[prerequisite] = true
		state, err := acceptedPrerequisite(ctx, q, issue.WorkspaceID, prerequisite)
		if err != nil {
			return IssueDependencyState{}, err
		}
		if !state.Ready {
			return state, nil
		}
	}
	return IssueDependencyState{Ready: true}, nil
}

// acceptedPrerequisite verifies one prerequisite issue against both halves of
// the contract: success terminal status and a valid persisted acceptance.
func acceptedPrerequisite(ctx context.Context, q *db.Queries, workspaceID pgtype.UUID, issueIDText string) (IssueDependencyState, error) {
	issueID, err := util.ParseUUID(issueIDText)
	if err != nil {
		return dependencyNotReady(DependencyReasonUnknown, "prerequisite issue id %q is invalid", issueIDText), nil
	}
	prerequisite, err := q.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{
		ID:          issueID,
		WorkspaceID: workspaceID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return dependencyNotReady(DependencyReasonUnknown, "prerequisite issue %s is not readable in this workspace", issueIDText), nil
	}
	if err != nil {
		return IssueDependencyState{}, err
	}
	category, err := issuestatus.CategoryWithError(ctx, q, workspaceID, prerequisite.Status)
	if err != nil {
		return IssueDependencyState{}, err
	}
	if category != issuestatus.CategoryDone {
		return dependencyNotReady(DependencyReasonNotDone,
			"prerequisite %s is %q (%s); only success releases a dependent",
			issueIDText, prerequisite.Status, cancellationNote(category)), nil
	}
	if _, err := q.GetValidAcceptedClosureReview(ctx, db.GetValidAcceptedClosureReviewParams{
		WorkspaceID: workspaceID,
		SubjectID:   issueID,
	}); errors.Is(err, pgx.ErrNoRows) {
		return dependencyNotReady(DependencyReasonNotAccepted,
			"prerequisite %s has no currently valid acceptance for its delivery", issueIDText), nil
	} else if err != nil {
		return IssueDependencyState{}, err
	}
	return IssueDependencyState{Ready: true}, nil
}

func cancellationNote(category string) string {
	if category == issuestatus.CategoryClosed {
		return "cancellation is not success"
	}
	return "not a success terminal"
}
