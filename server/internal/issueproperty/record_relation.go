package issueproperty

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// MaxRecordRelationValues caps linked rows per task property — mirrors multi_select.
const MaxRecordRelationValues = 50

// RecordRelationTargetKindIssues marks a same-table relation pointing to
// workspace-visible issues instead of a generic table.
const RecordRelationTargetKindIssues = "issues"

// RecordRelationConfig is the stored definition config for record_relation.
// TargetTableID and TargetKind are mutually exclusive.
type RecordRelationConfig struct {
	TargetTableID string `json:"target_table_id,omitempty"`
	TargetKind    string `json:"target_kind,omitempty"`
}

// IsIssuesTarget reports whether the relation targets workspace issues.
func (c RecordRelationConfig) IsIssuesTarget() bool {
	return c.TargetKind == RecordRelationTargetKindIssues
}

// SameTarget reports whether two canonical configs point to the same target.
func (c RecordRelationConfig) SameTarget(other RecordRelationConfig) bool {
	return c.TargetKind == other.TargetKind && c.TargetTableID == other.TargetTableID
}

// IsRecordRelation reports whether values must resolve against linked rows or issues.
func IsRecordRelation(propertyType string) bool {
	return propertyType == "record_relation"
}

// ParseRecordRelationConfig validates and canonicalizes definition config.
func ParseRecordRelationConfig(raw []byte) (RecordRelationConfig, error) {
	var cfg RecordRelationConfig
	if len(raw) != 0 {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(raw, &object); err != nil {
			return RecordRelationConfig{}, errors.New("config must be a JSON object")
		}
		for key := range object {
			if key != "target_table_id" && key != "target_kind" {
				return RecordRelationConfig{}, fmt.Errorf("record_relation config does not accept %q", key)
			}
		}
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return RecordRelationConfig{}, errors.New("config must be a JSON object")
		}
	}
	tableID := strings.TrimSpace(cfg.TargetTableID)
	targetKind := strings.TrimSpace(cfg.TargetKind)
	if tableID != "" && targetKind != "" {
		return RecordRelationConfig{}, errors.New("record_relation config cannot specify both target_table_id and target_kind")
	}
	if targetKind != "" {
		if targetKind != RecordRelationTargetKindIssues {
			return RecordRelationConfig{}, fmt.Errorf("record_relation target_kind must be %q", RecordRelationTargetKindIssues)
		}
		return RecordRelationConfig{TargetKind: RecordRelationTargetKindIssues}, nil
	}
	if tableID == "" {
		return RecordRelationConfig{}, errors.New("record_relation properties require target_table_id or target_kind in config")
	}
	parsed, err := uuid.Parse(tableID)
	if err != nil {
		return RecordRelationConfig{}, errors.New("target_table_id must be a UUID")
	}
	return RecordRelationConfig{TargetTableID: parsed.String()}, nil
}

// ParseRecordIDList validates a record_relation value: non-empty array of UUID
// strings, duplicates dropped, caller order preserved.
func ParseRecordIDList(items []any) ([]string, error) {
	if len(items) == 0 {
		return nil, errors.New("value must be a non-empty array of record id strings")
	}
	if len(items) > MaxRecordRelationValues {
		return nil, fmt.Errorf("value cannot list more than %d records", MaxRecordRelationValues)
	}
	seen := make(map[string]struct{}, len(items))
	out := make([]string, 0, len(items))
	for _, item := range items {
		raw, ok := item.(string)
		if !ok {
			return nil, errors.New("value must be a non-empty array of record id strings")
		}
		raw = strings.TrimSpace(raw)
		parsed, err := uuid.Parse(raw)
		if err != nil {
			return nil, errors.New("each record id must be a UUID")
		}
		canonical := parsed.String()
		if _, dup := seen[canonical]; dup {
			continue
		}
		seen[canonical] = struct{}{}
		out = append(out, canonical)
	}
	if len(out) == 0 {
		return nil, errors.New("value must be a non-empty array of record id strings")
	}
	return out, nil
}

// DBTX is the narrow database surface record_relation resolution needs.
type DBTX interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

var (
	ErrRecordRelationRecords = errors.New("one or more record ids are invalid for the configured target table")
	ErrRecordRelationIssues  = errors.New("one or more issue ids are invalid or not workspace-visible")
	ErrRecordRelationSelfRef = errors.New("an issue cannot reference itself")
)

// VerifyRecordRelationRefs ensures every id belongs to the configured target
// (generic_table row or workspace-visible issue) in this workspace. A write
// transaction holds key-share locks on the linked rows.
func VerifyRecordRelationRefs(
	ctx context.Context,
	tx DBTX,
	workspaceID string,
	cfg RecordRelationConfig,
	sourceIssueID string,
	recordIDs []string,
	lockRecords bool,
) error {
	if cfg.IsIssuesTarget() {
		return verifyIssueRelationRefs(ctx, tx, workspaceID, sourceIssueID, recordIDs, lockRecords)
	}
	if len(recordIDs) == 0 {
		return ErrRecordRelationRecords
	}
	wsUUID, err := uuid.Parse(workspaceID)
	if err != nil {
		return fmt.Errorf("invalid workspace_id: %w", err)
	}
	tableUUID, err := uuid.Parse(cfg.TargetTableID)
	if err != nil {
		return fmt.Errorf("invalid target_table_id: %w", err)
	}
	const selectRecords = `SELECT r.id FROM generic_record r
		JOIN generic_table t ON t.id = r.table_id AND t.workspace_id = r.workspace_id
		WHERE r.workspace_id = $1 AND r.table_id = $2 AND r.id = ANY($3::uuid[])`
	query := selectRecords
	if lockRecords {
		query += ` ORDER BY r.id FOR KEY SHARE OF r`
	}
	var count int64
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM (`+query+`) linked`, wsUUID, tableUUID, recordIDs).Scan(&count); err != nil {
		return fmt.Errorf("resolve record_relation records: %w", err)
	}
	if int(count) != len(recordIDs) {
		return ErrRecordRelationRecords
	}
	return nil
}

func verifyIssueRelationRefs(
	ctx context.Context,
	tx DBTX,
	workspaceID, sourceIssueID string,
	issueIDs []string,
	lockRecords bool,
) error {
	if len(issueIDs) == 0 {
		return ErrRecordRelationIssues
	}
	wsUUID, err := uuid.Parse(workspaceID)
	if err != nil {
		return fmt.Errorf("invalid workspace_id: %w", err)
	}
	if trimmed := strings.TrimSpace(sourceIssueID); trimmed != "" {
		sourceUUID, err := uuid.Parse(trimmed)
		if err != nil {
			return fmt.Errorf("invalid source_issue_id: %w", err)
		}
		canonicalSource := sourceUUID.String()
		for _, id := range issueIDs {
			if strings.EqualFold(id, canonicalSource) {
				return ErrRecordRelationSelfRef
			}
		}
	}
	const selectIssues = `SELECT i.id FROM issue i
		LEFT JOIN project p ON p.id = i.project_id AND p.workspace_id = i.workspace_id
		WHERE i.workspace_id = $1
		  AND i.id = ANY($2::uuid[])
		  AND (i.project_id IS NULL OR (p.id IS NOT NULL AND p.visibility = 'workspace'))`
	query := selectIssues
	if lockRecords {
		query += ` ORDER BY i.id FOR KEY SHARE OF i`
	}
	var count int64
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM (`+query+`) linked`, wsUUID, issueIDs).Scan(&count); err != nil {
		return fmt.Errorf("resolve record_relation issues: %w", err)
	}
	if int(count) != len(issueIDs) {
		return ErrRecordRelationIssues
	}
	return nil
}

// RecordIDsInValue decodes canonical record_relation JSON for caller-side resolution.
func RecordIDsInValue(stored []byte) ([]string, error) {
	var values []string
	if err := json.Unmarshal(stored, &values); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		parsed, err := uuid.Parse(strings.TrimSpace(value))
		if err != nil {
			return nil, fmt.Errorf("record id %q must be a UUID", value)
		}
		out = append(out, parsed.String())
	}
	return out, nil
}
