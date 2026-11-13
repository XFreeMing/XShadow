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
	"strconv"
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

// IssueDependencyBlocker is one strong prerequisite that is not currently
// satisfied. It carries the concrete facts a read model needs to explain the
// wait: the plan item, the issue bound to it (empty when nothing is bound), its
// status when readable, and the stable reason/detail pair.
type IssueDependencyBlocker struct {
	ItemID  string
	IssueID string
	Status  string
	Reason  string
	Detail  string
}

// DependencyWaitPrerequisite is the persisted, structured form of one unmet
// strong prerequisite. A client localizes the prompt from Reason plus the
// item/issue/status facts; Detail stays diagnostic English.
type DependencyWaitPrerequisite struct {
	ItemID  string `json:"item_id,omitempty"`
	IssueID string `json:"issue_id,omitempty"`
	Status  string `json:"status,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// DependencyWaitPrerequisites narrows blockers to the structured facts stored
// with a parked task.
func DependencyWaitPrerequisites(blockers []IssueDependencyBlocker) []DependencyWaitPrerequisite {
	out := make([]DependencyWaitPrerequisite, 0, len(blockers))
	for _, blocker := range blockers {
		out = append(out, DependencyWaitPrerequisite{
			ItemID:  blocker.ItemID,
			IssueID: blocker.IssueID,
			Status:  blocker.Status,
			Reason:  blocker.Reason,
		})
	}
	return out
}

// IssueDependenciesReady reports whether every strong prerequisite of this
// issue is successfully accepted. An issue with no goal link, no saved plan or
// no plan item is not under a declared dependency and is released unchanged.
func IssueDependenciesReady(ctx context.Context, q *db.Queries, issue db.Issue) (IssueDependencyState, error) {
	blockers, err := IssueDependencyBlockers(ctx, q, issue)
	if err != nil {
		return IssueDependencyState{}, err
	}
	if len(blockers) == 0 {
		return IssueDependencyState{Ready: true}, nil
	}
	return IssueDependencyState{Reason: blockers[0].Reason, Detail: blockers[0].Detail}, nil
}

// IssueDependencyBlockers returns every unmet strong prerequisite for the
// issue, in plan edge order and deduplicated by prerequisite issue. It is the
// same check the claim path and the release event use; callers only read it.
func IssueDependencyBlockers(ctx context.Context, q *db.Queries, issue db.Issue) ([]IssueDependencyBlocker, error) {
	link, err := q.GetGoalIssueLink(ctx, db.GetGoalIssueLinkParams{
		WorkspaceID: issue.WorkspaceID,
		IssueID:     issue.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	plan, err := q.GetGoalPlan(ctx, db.GetGoalPlanParams{
		WorkspaceID: issue.WorkspaceID,
		GoalID:      link.GoalID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var content goalPlanDependencyContent
	if err := json.Unmarshal(plan.Content, &content); err != nil {
		state := dependencyNotReady(DependencyReasonUnreadable, "the saved plan could not be read: %v", err)
		return []IssueDependencyBlocker{{Reason: state.Reason, Detail: state.Detail}}, nil
	}
	itemIssue, err := goalPlanItemIssues(ctx, q, issue.WorkspaceID, link.GoalID, content)
	if err != nil {
		return nil, err
	}
	issueID := util.UUIDToString(issue.ID)
	issueItem := ""
	for _, item := range content.Items {
		if item.ID != "" && itemIssue[item.ID] == issueID {
			issueItem = item.ID
			break
		}
	}
	if issueItem == "" {
		return nil, nil
	}

	blockers := make([]IssueDependencyBlocker, 0, len(content.Edges))
	checked := make(map[string]bool, len(content.Edges))
	for _, edge := range content.Edges {
		if edge.DependentItemID != issueItem {
			continue
		}
		prerequisite, ok := itemIssue[edge.PrerequisiteItemID]
		if !ok {
			state := dependencyNotReady(DependencyReasonUnknown,
				"prerequisite plan item %q has no linked issue, so its success cannot be verified", edge.PrerequisiteItemID)
			blockers = append(blockers, IssueDependencyBlocker{
				ItemID: edge.PrerequisiteItemID,
				Reason: state.Reason,
				Detail: state.Detail,
			})
			continue
		}
		if checked[prerequisite] {
			continue
		}
		checked[prerequisite] = true
		verdict, err := acceptedPrerequisite(ctx, q, issue.WorkspaceID, prerequisite)
		if err != nil {
			return nil, err
		}
		if !verdict.State.Ready {
			blockers = append(blockers, IssueDependencyBlocker{
				ItemID:  edge.PrerequisiteItemID,
				IssueID: prerequisite,
				Status:  verdict.Status,
				Reason:  verdict.State.Reason,
				Detail:  verdict.State.Detail,
			})
		}
	}
	return blockers, nil
}

type prerequisiteVerdict struct {
	State  IssueDependencyState
	Status string
}

// acceptedPrerequisite verifies one prerequisite issue against both halves of
// the contract: success terminal status and a valid persisted acceptance.
func acceptedPrerequisite(ctx context.Context, q *db.Queries, workspaceID pgtype.UUID, issueIDText string) (prerequisiteVerdict, error) {
	issueID, err := util.ParseUUID(issueIDText)
	if err != nil {
		return prerequisiteVerdict{State: dependencyNotReady(DependencyReasonUnknown, "prerequisite issue id %q is invalid", issueIDText)}, nil
	}
	prerequisite, err := q.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{
		ID:          issueID,
		WorkspaceID: workspaceID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return prerequisiteVerdict{State: dependencyNotReady(DependencyReasonUnknown, "prerequisite issue %s is not readable in this workspace", issueIDText)}, nil
	}
	if err != nil {
		return prerequisiteVerdict{}, err
	}
	status := prerequisite.Status
	category, err := issuestatus.CategoryWithError(ctx, q, workspaceID, status)
	if err != nil {
		return prerequisiteVerdict{}, err
	}
	if category != issuestatus.CategoryDone {
		return prerequisiteVerdict{
			State: dependencyNotReady(DependencyReasonNotDone,
				"prerequisite %s is %q (%s); only success releases a dependent",
				issueIDText, status, cancellationNote(category)),
			Status: status,
		}, nil
	}
	// Acceptance is evidence for the delivery and scope it was recorded
	// against. Re-read the facts that can move without a new review — the head
	// SHA registered on the linked PR and the goal plan revision — so a new
	// commit or a plan edit invalidates the old acceptance immediately instead
	// of waiting for the next review to supersede it.
	params := db.GetValidAcceptedClosureReviewParams{
		WorkspaceID: workspaceID,
		SubjectID:   issueID,
	}
	if delivery, known, err := IssueRegisteredDeliveryVersion(ctx, q, issueID); err != nil {
		return prerequisiteVerdict{}, err
	} else if known {
		params.DeliveryVersion = pgtype.Text{String: delivery, Valid: true}
	}
	if criteria, known, err := IssueCriteriaRevision(ctx, q, workspaceID, issueID); err != nil {
		return prerequisiteVerdict{}, err
	} else if known {
		params.CriteriaRevision = pgtype.Text{String: criteria, Valid: true}
	}
	if _, err := q.GetValidAcceptedClosureReview(ctx, params); errors.Is(err, pgx.ErrNoRows) {
		return prerequisiteVerdict{
			State: dependencyNotReady(DependencyReasonNotAccepted,
				"prerequisite %s has no currently valid acceptance for its delivery", issueIDText),
			Status: status,
		}, nil
	} else if err != nil {
		return prerequisiteVerdict{}, err
	}
	return prerequisiteVerdict{State: IssueDependencyState{Ready: true}, Status: status}, nil
}

func cancellationNote(category string) string {
	if category == issuestatus.CategoryClosed {
		return "cancellation is not success"
	}
	return "not a success terminal"
}

// PlanCriteriaRevision is the opaque closure criteria revision derived from a
// saved goal plan revision. It is derived rather than supplied by a caller so
// the review, the grant and the downstream readiness check all compare the
// same value: saving the plan changes it and invalidates earlier evidence.
func PlanCriteriaRevision(goalID pgtype.UUID, revision int32) string {
	return "plan:" + util.UUIDToString(goalID) + ":r" + strconv.FormatInt(int64(revision), 10)
}

// IssueCriteriaRevision returns the plan revision the issue is currently bound
// to. known is false when the issue has no goal link or the goal has no saved
// plan; an unreadable row is an error and callers fail closed.
func IssueCriteriaRevision(ctx context.Context, q *db.Queries, workspaceID, issueID pgtype.UUID) (string, bool, error) {
	link, err := q.GetGoalIssueLink(ctx, db.GetGoalIssueLinkParams{
		WorkspaceID: workspaceID,
		IssueID:     issueID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return GoalCriteriaRevision(ctx, q, workspaceID, link.GoalID)
}

// GoalCriteriaRevision returns the goal's saved plan revision as a closure
// criteria revision. known is false when the goal has no saved plan.
func GoalCriteriaRevision(ctx context.Context, q *db.Queries, workspaceID, goalID pgtype.UUID) (string, bool, error) {
	plan, err := q.GetGoalPlan(ctx, db.GetGoalPlanParams{
		WorkspaceID: workspaceID,
		GoalID:      goalID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return PlanCriteriaRevision(goalID, plan.Revision), true, nil
}

// IssueRegisteredDeliveryVersion returns the delivery version the closure
// contract treats as authoritative for an issue: the head SHA recorded on its
// linked pull request(s). known is false when no PR has been registered, in
// which case no registered fact can invalidate an acceptance yet.
func IssueRegisteredDeliveryVersion(ctx context.Context, q *db.Queries, issueID pgtype.UUID) (string, bool, error) {
	sha, err := q.GetIssueReviewHeadSha(ctx, issueID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	sha = strings.TrimSpace(sha)
	if sha == "" {
		return "", false, nil
	}
	return sha, true, nil
}

// goalPlanItemIssues resolves every plan item to the issue it is bound to,
// combining explicit `binding.type = "existing"` links with the persisted
// materialization rows that turn a `binding.type = "new"` intent into an
// issue. Content bindings win: an explicit link is the user's own statement.
func goalPlanItemIssues(ctx context.Context, q *db.Queries, workspaceID, goalID pgtype.UUID, content goalPlanDependencyContent) (map[string]string, error) {
	itemIssue := make(map[string]string, len(content.Items))
	for _, item := range content.Items {
		if item.ID == "" {
			continue
		}
		if bound := strings.TrimSpace(item.Binding.IssueID); bound != "" {
			itemIssue[item.ID] = bound
		}
	}
	rows, err := q.ListGoalPlanItemIssues(ctx, db.ListGoalPlanItemIssuesParams{
		WorkspaceID: workspaceID,
		GoalID:      goalID,
	})
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if _, exists := itemIssue[row.ItemID]; exists {
			continue
		}
		itemIssue[row.ItemID] = util.UUIDToString(row.IssueID)
	}
	return itemIssue, nil
}

// DependentIssueIDsForPrerequisite returns the issues whose plan item declares
// this issue's item as a strong prerequisite. It is the event lookup used to
// release parked dependents after a real dependency change; a plan that cannot
// be read yields no release (callers treat that as "nothing to release", and
// the claim path still refuses an unready issue).
func DependentIssueIDsForPrerequisite(ctx context.Context, q *db.Queries, prerequisite db.Issue) ([]pgtype.UUID, error) {
	link, err := q.GetGoalIssueLink(ctx, db.GetGoalIssueLinkParams{
		WorkspaceID: prerequisite.WorkspaceID,
		IssueID:     prerequisite.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	plan, err := q.GetGoalPlan(ctx, db.GetGoalPlanParams{
		WorkspaceID: prerequisite.WorkspaceID,
		GoalID:      link.GoalID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var content goalPlanDependencyContent
	if err := json.Unmarshal(plan.Content, &content); err != nil {
		return nil, nil
	}
	itemIssue, err := goalPlanItemIssues(ctx, q, prerequisite.WorkspaceID, link.GoalID, content)
	if err != nil {
		return nil, err
	}
	prerequisiteID := util.UUIDToString(prerequisite.ID)
	prerequisiteItems := make(map[string]bool, 1)
	for _, item := range content.Items {
		if item.ID != "" && itemIssue[item.ID] == prerequisiteID {
			prerequisiteItems[item.ID] = true
		}
	}
	if len(prerequisiteItems) == 0 {
		return nil, nil
	}
	seen := make(map[string]bool)
	var out []pgtype.UUID
	for _, edge := range content.Edges {
		if !prerequisiteItems[edge.PrerequisiteItemID] {
			continue
		}
		bound := itemIssue[edge.DependentItemID]
		if bound == "" || seen[bound] {
			continue
		}
		id, err := util.ParseUUID(bound)
		if err != nil {
			continue
		}
		seen[bound] = true
		out = append(out, id)
	}
	return out, nil
}

// GoalBoundIssueIDs returns every issue currently bound to a plan item of the
// goal, whether by an explicit binding or by a persisted materialization.
func GoalBoundIssueIDs(ctx context.Context, q *db.Queries, workspaceID, goalID pgtype.UUID) ([]pgtype.UUID, error) {
	plan, err := q.GetGoalPlan(ctx, db.GetGoalPlanParams{
		WorkspaceID: workspaceID,
		GoalID:      goalID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var content goalPlanDependencyContent
	if err := json.Unmarshal(plan.Content, &content); err != nil {
		return nil, nil
	}
	itemIssue, err := goalPlanItemIssues(ctx, q, workspaceID, goalID, content)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(itemIssue))
	var out []pgtype.UUID
	for _, bound := range itemIssue {
		if seen[bound] {
			continue
		}
		id, err := util.ParseUUID(bound)
		if err != nil {
			continue
		}
		seen[bound] = true
		out = append(out, id)
	}
	return out, nil
}
