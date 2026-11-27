package tables

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

type relationConfig struct {
	TargetTableID  string
	Bidirectional  bool
	ReverseFieldID *string
}

func parseRelationConfigInput(config any) (relationConfig, error) {
	obj, ok := config.(map[string]any)
	if !ok {
		return relationConfig{}, errInvalid("关联字段需要指定目标表")
	}
	for k := range obj {
		if k != "target_table_id" && k != "bidirectional" && k != "reverse_field_id" {
			return relationConfig{}, errInvalid("关联字段配置包含不支持的参数")
		}
	}
	if rev, exists := obj["reverse_field_id"]; exists && rev != nil {
		return relationConfig{}, errInvalid("反向关联字段由系统自动生成")
	}
	rawTarget, ok := obj["target_table_id"].(string)
	if !ok {
		return relationConfig{}, errInvalid("关联字段需要有效的目标表标识")
	}
	trimmedTarget := strings.TrimSpace(rawTarget)
	if !IsUUID(trimmedTarget) {
		return relationConfig{}, errInvalid("关联字段需要有效的目标表标识")
	}
	bidirectional := false
	if rawBi, exists := obj["bidirectional"]; exists {
		b, ok := rawBi.(bool)
		if !ok {
			return relationConfig{}, errInvalid("双向关联开关需要是布尔值")
		}
		bidirectional = b
	}
	return relationConfig{
		TargetTableID: strings.ToLower(trimmedTarget),
		Bidirectional: bidirectional,
	}, nil
}

func relationConfigFromStored(config any) (relationConfig, error) {
	obj, ok := config.(map[string]any)
	if !ok {
		return relationConfig{}, errFailed(nil)
	}
	rawTarget, ok := obj["target_table_id"].(string)
	if !ok || !IsUUID(rawTarget) {
		return relationConfig{}, errFailed(nil)
	}
	bi, _ := obj["bidirectional"].(bool)
	var revPtr *string
	if rawRev, exists := obj["reverse_field_id"]; exists && rawRev != nil {
		revStr, ok := rawRev.(string)
		if !ok || !IsUUID(revStr) {
			return relationConfig{}, errFailed(nil)
		}
		lower := strings.ToLower(revStr)
		revPtr = &lower
	}
	return relationConfig{
		TargetTableID:  strings.ToLower(rawTarget),
		Bidirectional:  bi,
		ReverseFieldID: revPtr,
	}, nil
}

func (c relationConfig) toJSON() map[string]any {
	out := map[string]any{
		"target_table_id": c.TargetTableID,
		"bidirectional":   c.Bidirectional,
	}
	if c.ReverseFieldID != nil {
		out["reverse_field_id"] = *c.ReverseFieldID
	}
	return out
}

func normalizeRelationCell(value any) (any, bool, error) {
	if value == nil {
		return nil, false, nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil, false, errInvalid("关联字段需要记录标识列表")
	}
	if len(items) == 0 {
		return nil, false, nil
	}
	seen := make(map[string]struct{}, len(items))
	out := make([]any, 0, len(items))
	for _, item := range items {
		raw, ok := item.(string)
		if !ok {
			return nil, false, errInvalid("关联记录标识无效")
		}
		trimmed := strings.TrimSpace(raw)
		if !IsUUID(trimmed) {
			return nil, false, errInvalid("关联记录标识无效")
		}
		lower := strings.ToLower(trimmed)
		if _, dup := seen[lower]; !dup {
			seen[lower] = struct{}{}
			out = append(out, lower)
		}
	}
	return out, true, nil
}

func extractRelationIDs(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok {
			out = append(out, strings.ToLower(s))
		}
	}
	return out
}

func validateTargetTable(sourceTable, targetTable string, bidirectional bool) error {
	if bidirectional && strings.EqualFold(sourceTable, targetTable) {
		return errInvalid("双向关联字段不能指向当前表")
	}
	return nil
}

func sortedUniqueTables(tables []string) []string {
	seen := make(map[string]struct{}, len(tables))
	out := make([]string, 0, len(tables))
	for _, t := range tables {
		lower := strings.ToLower(t)
		if _, dup := seen[lower]; !dup {
			seen[lower] = struct{}{}
			out = append(out, lower)
		}
	}
	sort.Strings(out)
	return out
}

func lockTablesSorted(ctx context.Context, tx pgx.Tx, workspace, primaryTable string, extraTables []string) ([]string, error) {
	primary := strings.ToLower(primaryTable)
	all := make([]string, 0, len(extraTables)+1)
	all = append(all, primary)
	all = append(all, extraTables...)
	ordered := sortedUniqueTables(all)
	for _, tableID := range ordered {
		var id string
		err := tx.QueryRow(ctx,
			`SELECT id::text FROM generic_table WHERE id = $1::uuid AND workspace_id = $2::uuid FOR UPDATE`,
			tableID, workspace,
		).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			if tableID == primary {
				return nil, errNotFound("找不到这张表")
			}
			return nil, errInvalid("目标表不存在或不属于当前工作区")
		}
		if err != nil {
			return nil, errFailed(err)
		}
	}
	return ordered, nil
}

func pickReverseFieldName(sourceTableName, sourceFieldName string, existing map[string]struct{}) string {
	trimmedTable := strings.TrimSpace(sourceTableName)
	base := truncateRunes(trimmedTable, 80)
	if base != "" {
		if _, dup := existing[base]; !dup {
			return base
		}
	}
	withField := formatWithSuffix(trimmedTable, fmt.Sprintf(" (%s)", sourceFieldName))
	if withField != "" {
		if _, dup := existing[withField]; !dup {
			return withField
		}
	}
	for i := 2; i < 10_000; i++ {
		cand := formatWithSuffix(trimmedTable, fmt.Sprintf(" (%d)", i))
		if cand != "" {
			if _, dup := existing[cand]; !dup {
				return cand
			}
		}
	}
	return truncateRunes(sourceFieldName+" 反向", 80)
}

func formatWithSuffix(base, suffix string) string {
	suffixLen := utf8.RuneCountInString(suffix)
	if suffixLen >= 80 {
		return truncateRunes(suffix, 80)
	}
	return truncateRunes(base, 80-suffixLen) + suffix
}

func truncateRunes(text string, max int) string {
	if utf8.RuneCountInString(text) <= max {
		return text
	}
	out := make([]rune, 0, max)
	for _, r := range text {
		if len(out) >= max {
			break
		}
		out = append(out, r)
	}
	return string(out)
}

func createRelationField(ctx context.Context, tx pgx.Tx, workspace, table, name string, config any) (FieldDto, error) {
	rel, err := relationConfigFromStored(config)
	if err != nil {
		return FieldDto{}, err
	}
	if err := validateTargetTable(table, rel.TargetTableID, rel.Bidirectional); err != nil {
		return FieldDto{}, err
	}
	if _, err := lockTablesSorted(ctx, tx, workspace, table, []string{rel.TargetTableID}); err != nil {
		return FieldDto{}, err
	}
	created := tsExpr("created_at")
	updated := tsExpr("updated_at")
	if !rel.Bidirectional {
		pos, err := nextFieldPosition(ctx, tx, workspace, table)
		if err != nil {
			return FieldDto{}, err
		}
		cfgJSON, err := marshalCompactString(rel.toJSON())
		if err != nil {
			return FieldDto{}, errFailed(err)
		}
		row := tx.QueryRow(ctx, fmt.Sprintf(
			`INSERT INTO generic_field (workspace_id, table_id, name, kind, config, position)
			 VALUES ($1::uuid, $2::uuid, $3, 'relation', $4::jsonb, $5)
			 RETURNING id::text, name, kind, config::text, position, %s, %s`, created, updated),
			workspace, table, name, cfgJSON, pos,
		)
		return scanFieldRow(row, "请重试添加字段")
	}

	var sourceFieldID, reverseFieldID string
	if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text, gen_random_uuid()::text`).Scan(&sourceFieldID, &reverseFieldID); err != nil {
		return FieldDto{}, errFailed(err)
	}
	var sourceTableName string
	if err := tx.QueryRow(ctx, `SELECT name FROM generic_table WHERE id = $1::uuid AND workspace_id = $2::uuid`, table, workspace).Scan(&sourceTableName); err != nil {
		return FieldDto{}, errFailed(err)
	}
	rows, err := tx.Query(ctx, `SELECT name FROM generic_field WHERE table_id = $1::uuid AND workspace_id = $2::uuid`, rel.TargetTableID, workspace)
	if err != nil {
		return FieldDto{}, errFailed(err)
	}
	existing := make(map[string]struct{})
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return FieldDto{}, errFailed(err)
		}
		existing[n] = struct{}{}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return FieldDto{}, errFailed(err)
	}
	reverseName := pickReverseFieldName(sourceTableName, name, existing)

	revIDCopy := reverseFieldID
	srcIDCopy := sourceFieldID
	sourceCfg := relationConfig{
		TargetTableID:  rel.TargetTableID,
		Bidirectional:  true,
		ReverseFieldID: &revIDCopy,
	}
	reverseCfg := relationConfig{
		TargetTableID:  strings.ToLower(table),
		Bidirectional:  true,
		ReverseFieldID: &srcIDCopy,
	}
	sourceCfgStr, err := marshalCompactString(sourceCfg.toJSON())
	if err != nil {
		return FieldDto{}, errFailed(err)
	}
	reverseCfgStr, err := marshalCompactString(reverseCfg.toJSON())
	if err != nil {
		return FieldDto{}, errFailed(err)
	}

	insertSQL := fmt.Sprintf(
		`INSERT INTO generic_field (id, workspace_id, table_id, name, kind, config, position)
		 VALUES ($1::uuid, $2::uuid, $3::uuid, $4, 'relation', $5::jsonb, $6)
		 RETURNING id::text, name, kind, config::text, position, %s, %s`, created, updated)
	srcPos, err := nextFieldPosition(ctx, tx, workspace, table)
	if err != nil {
		return FieldDto{}, err
	}
	srcField, err := scanFieldRow(
		tx.QueryRow(ctx, insertSQL, sourceFieldID, workspace, table, name, sourceCfgStr, srcPos),
		"请重试添加字段",
	)
	if err != nil {
		return FieldDto{}, err
	}
	revPos, err := nextFieldPosition(ctx, tx, workspace, rel.TargetTableID)
	if err != nil {
		return FieldDto{}, err
	}
	if _, err := scanFieldRow(
		tx.QueryRow(ctx, insertSQL, reverseFieldID, workspace, rel.TargetTableID, reverseName, reverseCfgStr, revPos),
		"请重试添加反向关联字段",
	); err != nil {
		return FieldDto{}, err
	}
	return srcField, nil
}

func prepareTableLocks(ctx context.Context, tx pgx.Tx, workspace, table string, touchedFieldIDs []string) ([]string, error) {
	var targets []string
	if len(touchedFieldIDs) > 0 {
		rows, err := tx.Query(ctx,
			`SELECT config::text
			 FROM generic_field
			 WHERE workspace_id = $1::uuid
			   AND table_id = $2::uuid
			   AND kind = 'relation'
			   AND id = ANY($3::text[]::uuid[])`,
			workspace, table, touchedFieldIDs,
		)
		if err != nil {
			return nil, errFailed(err)
		}
		for rows.Next() {
			var raw string
			if err := rows.Scan(&raw); err != nil {
				rows.Close()
				return nil, errFailed(err)
			}
			var val any
			if err := json.Unmarshal([]byte(raw), &val); err != nil {
				rows.Close()
				return nil, errFailed(err)
			}
			rel, err := relationConfigFromStored(val)
			if err != nil {
				rows.Close()
				return nil, err
			}
			if rel.Bidirectional {
				targets = append(targets, rel.TargetTableID)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, errFailed(err)
		}
	}
	return lockTablesSorted(ctx, tx, workspace, table, targets)
}

type reverseOpKind int

const (
	reverseOpAdd reverseOpKind = iota + 1
	reverseOpRemove
)

type reverseOp struct {
	reverseFieldID string
	op             reverseOpKind
}

type reverseKey struct {
	tableID  string
	recordID string
}

type reversePlan struct {
	ops map[reverseKey][]reverseOp
}

func (p reversePlan) hasWrites() bool {
	return len(p.ops) > 0
}

func diffRelationIDs(oldIDs, newIDs []string) (added, removed []string) {
	oldSet := make(map[string]struct{}, len(oldIDs))
	for _, id := range oldIDs {
		oldSet[id] = struct{}{}
	}
	newSet := make(map[string]struct{}, len(newIDs))
	for _, id := range newIDs {
		newSet[id] = struct{}{}
	}
	for _, id := range newIDs {
		if _, ok := oldSet[id]; !ok {
			added = append(added, id)
		}
	}
	for _, id := range oldIDs {
		if _, ok := newSet[id]; !ok {
			removed = append(removed, id)
		}
	}
	return added, removed
}

func validateAndPlanReverse(
	ctx context.Context,
	tx pgx.Tx,
	workspace string,
	fields []FieldDto,
	oldValues map[string]any,
	normalized map[string]cellPatch,
	lockedTables []string,
) (reversePlan, error) {
	plan := reversePlan{ops: make(map[reverseKey][]reverseOp)}
	fieldByID := make(map[string]FieldDto, len(fields))
	for _, f := range fields {
		fieldByID[f.ID] = f
	}
	// Process field IDs in deterministic order.
	keys := make([]string, 0, len(normalized))
	for k := range normalized {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, fieldID := range keys {
		nextPatch := normalized[fieldID]
		field, ok := fieldByID[fieldID]
		if !ok || field.Kind != "relation" {
			continue
		}
		rel, err := relationConfigFromStored(field.Config)
		if err != nil {
			return reversePlan{}, err
		}
		if rel.Bidirectional {
			if !slices.Contains(lockedTables, rel.TargetTableID) {
				return reversePlan{}, errInvalid("关联字段定义已变更，请重试")
			}
			if rel.ReverseFieldID == nil {
				return reversePlan{}, errFailed(nil)
			}
			if err := ensureReverseField(ctx, tx, workspace, rel.TargetTableID, *rel.ReverseFieldID); err != nil {
				return reversePlan{}, err
			}
		} else {
			if err := ensureTargetTable(ctx, tx, workspace, rel.TargetTableID); err != nil {
				return reversePlan{}, err
			}
		}

		var newIDs []string
		if nextPatch.valid {
			newIDs = extractRelationIDs(nextPatch.value)
		}
		if len(newIDs) > 0 {
			if err := ensureTargetRecords(ctx, tx, workspace, rel.TargetTableID, newIDs); err != nil {
				return reversePlan{}, err
			}
		}

		if rel.Bidirectional {
			revFieldID := *rel.ReverseFieldID
			var oldIDs []string
			if oldValues != nil {
				oldIDs = extractRelationIDs(oldValues[fieldID])
			}
			added, removed := diffRelationIDs(oldIDs, newIDs)
			for _, targetRec := range added {
				k := reverseKey{tableID: rel.TargetTableID, recordID: targetRec}
				plan.ops[k] = append(plan.ops[k], reverseOp{reverseFieldID: revFieldID, op: reverseOpAdd})
			}
			for _, targetRec := range removed {
				k := reverseKey{tableID: rel.TargetTableID, recordID: targetRec}
				plan.ops[k] = append(plan.ops[k], reverseOp{reverseFieldID: revFieldID, op: reverseOpRemove})
			}
		}
	}
	return plan, nil
}

func applyReversePlan(
	ctx context.Context,
	tx pgx.Tx,
	workspace string,
	sourceRecordID string,
	plan reversePlan,
	actor Actor,
	batchID *string,
) error {
	sourceID := strings.ToLower(sourceRecordID)
	keys := make([]reverseKey, 0, len(plan.ops))
	for k := range plan.ops {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].tableID != keys[j].tableID {
			return keys[i].tableID < keys[j].tableID
		}
		return keys[i].recordID < keys[j].recordID
	})

	for _, k := range keys {
		ops := plan.ops[k]
		current, err := lockRecord(ctx, tx, workspace, k.tableID, k.recordID)
		if err != nil {
			var se *StoreError
			if errors.As(err, &se) && se.Kind == ErrKindNotFound {
				allRemove := true
				for _, op := range ops {
					if op.op != reverseOpRemove {
						allRemove = false
						break
					}
				}
				if allRemove {
					continue
				}
				return errInvalid("关联的目标记录不存在或不属于目标表")
			}
			return err
		}

		patch := make(map[string]cellPatch)
		for _, op := range ops {
			list, err := loadCellIDs(ctx, tx, workspace, k.recordID, op.reverseFieldID)
			if err != nil {
				return err
			}
			changed := false
			switch op.op {
			case reverseOpAdd:
				if !slices.Contains(list, sourceID) {
					list = append(list, sourceID)
					changed = true
				}
			case reverseOpRemove:
				before := len(list)
				filtered := list[:0]
				for _, id := range list {
					if id != sourceID {
						filtered = append(filtered, id)
					}
				}
				list = filtered
				changed = len(list) != before
			}
			if changed {
				if len(list) == 0 {
					patch[op.reverseFieldID] = cellPatch{valid: false}
				} else {
					arr := make([]any, len(list))
					for i, id := range list {
						arr[i] = id
					}
					patch[op.reverseFieldID] = cellPatch{value: arr, valid: true}
				}
			}
		}

		if len(patch) == 0 {
			continue
		}
		if err := writeCells(ctx, tx, workspace, k.recordID, patch); err != nil {
			return err
		}
		if err := bumpRecordRevision(ctx, tx, workspace, k.tableID, k.recordID); err != nil {
			return err
		}
		nextRev := current.Revision + 1
		revDigest, err := digestPatch(patch)
		if err != nil {
			return err
		}
		if err := insertChange(ctx, tx, changeWrite{
			workspace:      workspace,
			table:          k.tableID,
			record:         k.recordID,
			base:           current.Revision,
			clientRevision: current.Revision,
			recordRevision: nextRev,
			values:         patch,
			actor:          actor,
			digest:         revDigest,
			batchID:        batchID,
		}); err != nil {
			return err
		}
	}
	return nil
}

func ensureTargetTable(ctx context.Context, tx pgx.Tx, workspace, targetTableID string) error {
	var one int
	err := tx.QueryRow(ctx, `SELECT 1 FROM generic_table WHERE id = $1::uuid AND workspace_id = $2::uuid`, targetTableID, workspace).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return errInvalid("目标表不存在或不属于当前工作区")
	}
	if err != nil {
		return errFailed(err)
	}
	return nil
}

func ensureReverseField(ctx context.Context, tx pgx.Tx, workspace, targetTableID, reverseFieldID string) error {
	var one int
	err := tx.QueryRow(ctx,
		`SELECT 1 FROM generic_field
		 WHERE id = $1::uuid AND table_id = $2::uuid AND workspace_id = $3::uuid AND kind = 'relation'`,
		reverseFieldID, targetTableID, workspace,
	).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return errInvalid("反向关联字段不存在")
	}
	if err != nil {
		return errFailed(err)
	}
	return nil
}

func ensureTargetRecords(ctx context.Context, tx pgx.Tx, workspace, targetTableID string, recordIDs []string) error {
	var count int64
	err := tx.QueryRow(ctx,
		`SELECT COUNT(*)::bigint FROM generic_record
		 WHERE workspace_id = $1::uuid AND table_id = $2::uuid AND id = ANY($3::text[]::uuid[])`,
		workspace, targetTableID, recordIDs,
	).Scan(&count)
	if err != nil {
		return errFailed(err)
	}
	if int(count) != len(recordIDs) {
		return errInvalid("关联的目标记录不存在或不属于目标表")
	}
	return nil
}

func loadCellIDs(ctx context.Context, tx pgx.Tx, workspace, recordID, fieldID string) ([]string, error) {
	var raw *string
	err := tx.QueryRow(ctx,
		`SELECT value::text FROM generic_cell
		 WHERE workspace_id = $1::uuid AND record_id = $2::uuid AND field_id = $3::uuid`,
		workspace, recordID, fieldID,
	).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) || raw == nil {
		return nil, nil
	}
	if err != nil {
		return nil, errFailed(err)
	}
	var val any
	if err := json.Unmarshal([]byte(*raw), &val); err != nil {
		return nil, errFailed(err)
	}
	return extractRelationIDs(val), nil
}
