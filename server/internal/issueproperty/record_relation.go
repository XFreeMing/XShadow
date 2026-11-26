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

// RecordRelationConfig is the stored definition config for record_relation.
type RecordRelationConfig struct {
	TargetTableID string `json:"target_table_id"`
}

// IsRecordRelation reports whether values must resolve against generic_table rows.
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
			if key != "target_table_id" {
				return RecordRelationConfig{}, fmt.Errorf("record_relation config does not accept %q", key)
			}
		}
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return RecordRelationConfig{}, errors.New("config must be a JSON object")
		}
	}
	id := strings.TrimSpace(cfg.TargetTableID)
	if id == "" {
		return RecordRelationConfig{}, errors.New("record_relation properties require target_table_id in config")
	}
	parsed, err := uuid.Parse(id)
	if err != nil {
		return RecordRelationConfig{}, errors.New("target_table_id must be a UUID")
	}
	cfg.TargetTableID = parsed.String()
	return cfg, nil
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

var ErrRecordRelationRecords = errors.New("one or more record ids are invalid for the configured target table")

// VerifyRecordRelationRefs ensures every id belongs to the configured table
// in this workspace. A write transaction holds key-share locks on the links.
func VerifyRecordRelationRefs(
	ctx context.Context,
	tx DBTX,
	workspaceID, targetTableID string,
	recordIDs []string,
	lockRecords bool,
) error {
	if len(recordIDs) == 0 {
		return ErrRecordRelationRecords
	}
	wsUUID, err := uuid.Parse(workspaceID)
	if err != nil {
		return fmt.Errorf("invalid workspace_id: %w", err)
	}
	tableUUID, err := uuid.Parse(targetTableID)
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
