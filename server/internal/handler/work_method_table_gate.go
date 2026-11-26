package handler

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/issuetype"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/workmethod"
)

func issueTypeHasProperty(issueType db.IssueType, propertyID pgtype.UUID) bool {
	fields, err := issuetype.ParseFields(issueType.Fields)
	if err != nil {
		return false
	}
	for _, field := range fields {
		if field.PropertyID == uuidToString(propertyID) {
			return true
		}
	}
	return false
}

func (h *Handler) requireWorkMethodTableGate(w http.ResponseWriter, r *http.Request, issue db.Issue, run workmethod.Run, userID string) bool {
	if run.Method == nil || run.Step < 0 || run.Step >= len(run.Method.Steps) {
		return true
	}
	gate := run.Method.Steps[run.Step].TableGate
	if gate == nil || parseUUIDOrInvalid(gate.IssueTypeID) != issue.IssueTypeID {
		return true
	}
	ctx := r.Context()
	propertyID := parseUUIDOrInvalid(gate.PropertyID)
	if service.ValidateTaskReadinessFormula(ctx, h.Queries, issue.WorkspaceID, propertyID) != nil {
		writeErrorCode(w, http.StatusConflict, "work_method_formula_unavailable", "当前步骤引用的公式属性不可用，请检查任务表格。")
		return false
	}
	value, err := h.IssueReadBridge.ProjectTaskFormula(ctx, issue.WorkspaceID, parseUUIDOrInvalid(userID), issue.ID, gate.PropertyID)
	var ready *bool
	if err != nil || json.Unmarshal(value, &ready) != nil || ready == nil {
		writeErrorCode(w, http.StatusServiceUnavailable, "work_method_formula_unavailable", "无法计算当前步骤的证据公式，请重试。")
		return false
	}
	if !*ready {
		writeErrorCode(w, http.StatusConflict, "work_method_evidence_not_ready", "当前步骤的证据尚未齐备，请在原任务表格补齐属性后重试。")
		return false
	}
	// SaveIssueWorkMethodRun compares the original issue revision, including
	// changes to the cells the formula just read.
	return true
}

// The original outcome remains in the task body when it exceeds a text cell.
func tableTaskProperties(ctx context.Context, tx pgx.Tx, q *db.Queries, workspace pgtype.UUID, binding workmethod.TableTask, evidence string) (pgtype.UUID, []byte, error) {
	typeID := parseUUIDOrInvalid(binding.IssueTypeID)
	seedID := parseUUIDOrInvalid(binding.SeedPropertyID)
	runes := []rune(evidence)
	if len(runes) > 2000 {
		evidence = string(runes[:1970]) + "…（完整证据见本任务正文）"
	}
	encoded, err := json.Marshal(evidence)
	if err != nil {
		return typeID, nil, err
	}
	properties, err := service.ResolveIssueCreateTypeAndProperties(ctx, tx, q, workspace, typeID, map[pgtype.UUID]json.RawMessage{seedID: encoded})
	return typeID, properties, err
}
