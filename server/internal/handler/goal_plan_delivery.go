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

// A saved plan owns the proposed contract. The issue owns its execution and
// never replaces existing acceptance evidence when a plan is edited.
func (h *Handler) issuePlanDelivery(ctx context.Context, issue db.Issue, goalID string) (*GoalPlanDelivery, error) {
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
	for _, item := range content.Items {
		if item.Delivery != nil && item.Binding.IssueID != nil && *item.Binding.IssueID == uuidToString(issue.ID) {
			return item.Delivery, nil
		}
	}
	return nil, nil
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
