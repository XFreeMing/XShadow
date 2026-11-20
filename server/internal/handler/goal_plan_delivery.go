package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/pkg/acceptance"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type GoalPlanDelivery struct {
	Type     string              `json:"type"`
	MethodID string              `json:"method_id"`
	Criteria []GoalPlanCriterion `json:"criteria"`
}

type GoalPlanCriterion struct {
	ID           string `json:"id"`
	Expected     string `json:"expected"`
	Verification string `json:"verification"`
}

type issuePlanContract struct {
	Delivery      *GoalPlanDelivery  `json:"planned_delivery"`
	Prerequisites []planPrerequisite `json:"plan_prerequisites"`
}

type planPrerequisite struct {
	Title   string  `json:"title"`
	IssueID *string `json:"issue_id,omitempty"`
	Kind    string  `json:"kind"`
	Reason  string  `json:"reason"`
}

func (content GoalPlanContent) contractForIssue(issueID string) *issuePlanContract {
	byID := make(map[string]GoalPlanItem, len(content.Items))
	var bound GoalPlanItem
	for _, item := range content.Items {
		byID[item.ID] = item
		if item.Binding.IssueID != nil && *item.Binding.IssueID == issueID {
			bound = item
		}
	}
	if bound.Delivery == nil {
		return nil
	}
	contract := &issuePlanContract{Delivery: bound.Delivery, Prerequisites: []planPrerequisite{}}
	for _, edge := range content.Edges {
		if edge.DependentItemID != bound.ID {
			continue
		}
		prerequisite := byID[edge.PrerequisiteItemID]
		kind := edge.Kind
		if kind == "" {
			kind = "required"
		}
		contract.Prerequisites = append(contract.Prerequisites, planPrerequisite{prerequisite.Title, prerequisite.Binding.IssueID, kind, edge.Reason})
	}
	return contract
}

func (c *issuePlanContract) claimBrief(issueRef string) string {
	if c == nil {
		return ""
	}
	ids := make([]string, 0, len(c.Delivery.Criteria))
	for _, criterion := range c.Delivery.Criteria {
		ids = append(ids, criterion.ID)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Goal plan delivery: type %q, planned method %q, acceptance IDs %s. Read expected results, verification and prerequisite details with \"$MULTICA_CLI_PATH\" issue method %s. Follow the selected method's current owner and reviewer.\n", c.Delivery.Type, c.Delivery.MethodID, strings.Join(ids, ", "), issueRef)
	for _, p := range c.Prerequisites {
		fmt.Fprintf(&b, "Prerequisite (%s): %q — %q.\n", p.Kind, p.Title, p.Reason)
	}
	return b.String()
}

// A saved plan owns the proposed contract. The issue owns its execution and
// never replaces existing acceptance evidence when a plan is edited.
func (h *Handler) issuePlanDelivery(ctx context.Context, issue db.Issue, goalID string) (*issuePlanContract, error) {
	if goalID == "" {
		return nil, nil
	}
	plan, err := h.Queries.GetGoalPlan(ctx, db.GetGoalPlanParams{WorkspaceID: issue.WorkspaceID, GoalID: parseUUIDOrInvalid(goalID)})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var content GoalPlanContent
	if err := json.Unmarshal(plan.Content, &content); err != nil {
		return nil, err
	}
	return content.contractForIssue(uuidToString(issue.ID)), nil
}

var goalPlanDeliveryKey = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,79}$`)

func validateGoalPlanDelivery(in *GoalPlanDelivery, index int) (*GoalPlanDelivery, error) {
	if in == nil {
		return nil, nil
	}
	out := &GoalPlanDelivery{Type: strings.TrimSpace(in.Type), MethodID: strings.TrimSpace(in.MethodID)}
	switch out.Type {
	case "feature", "defect", "research", "outcome":
	default:
		return nil, fmt.Errorf("items[%d].delivery.type is invalid", index)
	}
	if !goalPlanDeliveryKey.MatchString(out.MethodID) {
		return nil, fmt.Errorf("items[%d].delivery.method_id is invalid", index)
	}
	if len(in.Criteria) == 0 || len(in.Criteria) > 30 {
		return nil, fmt.Errorf("items[%d].delivery requires between 1 and 30 criteria", index)
	}
	seen := make(map[string]bool, len(in.Criteria))
	for _, c := range in.Criteria {
		c.ID = strings.TrimSpace(c.ID)
		c.Expected = strings.TrimSpace(c.Expected)
		c.Verification = strings.TrimSpace(c.Verification)
		if !goalPlanDeliveryKey.MatchString(c.ID) || seen[c.ID] {
			return nil, fmt.Errorf("items[%d].delivery criterion identifiers must be valid and unique", index)
		}
		if c.Expected == "" || c.Verification == "" || checkGoalPlanText(c.Expected, 2000) != nil || checkGoalPlanText(c.Verification, 2000) != nil {
			return nil, fmt.Errorf("items[%d].delivery criterion %s requires an expected result and verification (up to 2000 characters each)", index, c.ID)
		}
		seen[c.ID] = true
		out.Criteria = append(out.Criteria, c)
	}
	return out, nil
}

func plannedAcceptanceTrace(issue db.Issue, delivery *GoalPlanDelivery) ([]byte, error) {
	if delivery == nil {
		return nil, nil
	}
	trace, err := acceptance.Decode(issue.AcceptanceCriteria)
	if err != nil || len(trace.Criteria) > 0 {
		return nil, err
	}
	for _, criterion := range delivery.Criteria {
		revision := trace.Revision
		if err := trace.Apply(acceptance.Change{
			BaseRevision: &revision,
			Action:       "criterion",
			CriterionID:  criterion.ID,
			Text:         criterion.Expected + "\n\n" + criterion.Verification,
		}); err != nil {
			return nil, err
		}
	}
	return json.Marshal(trace)
}
