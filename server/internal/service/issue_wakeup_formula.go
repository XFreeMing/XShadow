package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/issueproperty"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func hasFormulaCondition(raw []byte) bool {
	var condition WakeupCondition
	return json.Unmarshal(raw, &condition) == nil && condition.Type == "issue_field" && condition.Field == "formula"
}

func formulaIssueReady(ctx context.Context, q *db.Queries, w db.IssueWakeup) (bool, error) {
	issue, err := q.GetIssue(ctx, w.IssueID)
	if err != nil || issue.Status == issuestatus.Blocked {
		return false, err
	}
	category, err := issuestatus.CategoryWithError(ctx, q, w.WorkspaceID, issue.Status)
	return category == issuestatus.CategoryStarted, err
}

// Match the subscription to the dependency domain of issue.properties_changed.
// Generic-table writes do not yet contribute hints for dependent task records.
func ValidateTaskReadinessFormula(ctx context.Context, q *db.Queries, workspaceID, root pgtype.UUID) error {
	seen := make(map[pgtype.UUID]struct{})
	var visit func(pgtype.UUID) error
	visit = func(id pgtype.UUID) error {
		if _, ok := seen[id]; ok {
			return nil
		}
		seen[id] = struct{}{}
		def, err := q.GetIssueProperty(ctx, db.GetIssuePropertyParams{WorkspaceID: workspaceID, ID: id})
		if errors.Is(err, pgx.ErrNoRows) {
			return errFormulaDefinition
		}
		if err != nil {
			return err
		}
		if def.ArchivedAt.Valid || def.Type == "lookup" || def.Type == "record_relation" || (id == root && !issueproperty.IsFormula(def.Type)) {
			return errFormulaDefinition
		}
		if !issueproperty.IsFormula(def.Type) {
			return nil
		}
		formula, err := issueproperty.ParseFormulaStoredConfig(def.Config)
		if err != nil {
			return errFormulaDefinition
		}
		for _, raw := range formula.Refs {
			ref, err := util.ParseUUID(raw)
			if err != nil {
				return errFormulaDefinition
			}
			if err := visit(ref); err != nil {
				return err
			}
		}
		return nil
	}
	return visit(root)
}

func (s *IssueWakeupService) evaluateFormulaCondition(ctx context.Context, q *db.Queries, w db.IssueWakeup, c WakeupCondition) (bool, string, map[string]any, error) {
	active, err := formulaIssueReady(ctx, q, w)
	if err != nil || !active {
		return false, "", nil, err
	}
	id, err := util.ParseUUID(c.PropertyID)
	if err != nil {
		return false, "", nil, errFormulaDefinition
	}
	if err := ValidateTaskReadinessFormula(ctx, q, w.WorkspaceID, id); err != nil {
		return false, "", nil, err
	}
	if s.Tasks == nil || s.Tasks.ProjectIssueFormula == nil {
		return false, "", nil, errFormulaUnavailable
	}
	value, err := s.Tasks.ProjectIssueFormula(ctx, w.WorkspaceID, w.CreatedBy, w.IssueID, c.PropertyID)
	if err != nil {
		return false, "", nil, errFormulaUnavailable
	}
	value = canonicalWakeupPayload(value)
	if string(value) != "true" && string(value) != "false" {
		return false, "", nil, errFormulaUnavailable
	}
	return bytes.Equal(value, canonicalWakeupPayload(c.Value)), "formula:" + c.PropertyID + ":" + string(value),
		map[string]any{"property_id": c.PropertyID, "value": json.RawMessage(value)}, nil
}
