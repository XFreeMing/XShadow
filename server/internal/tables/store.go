package tables

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// GroupUpdate models the three-state group field update in SaveView/CreateView.
type GroupUpdate struct {
	Set   bool
	Value *string
}

// ConfigUpdate models the two-state config field update in SaveView/CreateView.
type ConfigUpdate struct {
	Set   bool
	Value any
}

// ViewWrite holds validated/raw parameters for CreateView and SaveView.
type ViewWrite struct {
	Name    string
	Kind    *string
	Config  ConfigUpdate
	Columns []string
	Filter  *StoredFilter
	Sort    *StoredSort
	Group   GroupUpdate
}

func ListTables(ctx context.Context, pool Pool, workspace string) ([]TableDto, error) {
	created := tsExpr("created_at")
	updated := tsExpr("updated_at")
	sql := fmt.Sprintf(
		`SELECT id::text, name, %s, %s
		 FROM generic_table
		 WHERE workspace_id = $1::uuid
		 ORDER BY created_at DESC, id DESC`, created, updated)
	rows, err := pool.Query(ctx, sql, workspace)
	if err != nil {
		return nil, logFailed(err)
	}
	defer rows.Close()
	out := make([]TableDto, 0)
	for rows.Next() {
		t, err := scanTable(rows)
		if err != nil {
			return nil, logFailed(err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, logFailed(err)
	}
	return out, nil
}

func CreateTable(ctx context.Context, pool Pool, workspace, name string) (TableCreated, error) {
	clean, err := CleanName(name)
	if err != nil {
		return TableCreated{}, err
	}
	tx, err := beginTx(ctx, pool)
	if err != nil {
		return TableCreated{}, err
	}
	defer tx.Rollback(ctx)
	table, err := insertTable(ctx, tx, workspace, clean)
	if err != nil {
		return TableCreated{}, err
	}
	view, err := insertView(ctx, tx, workspace, table.ID, StoredView{
		Name:   "全部",
		Kind:   "grid",
		Config: map[string]any{},
	}, 0)
	if err != nil {
		return TableCreated{}, err
	}
	if err := commitTx(ctx, tx); err != nil {
		return TableCreated{}, err
	}
	return TableCreated{Table: table, View: view}, nil
}

func RenameTable(ctx context.Context, pool Pool, workspace, tableID, name string, actor *Actor) (TableDto, error) {
	clean, err := CleanName(name)
	if err != nil {
		return TableDto{}, err
	}
	tx, err := beginTx(ctx, pool)
	if err != nil {
		return TableDto{}, err
	}
	defer tx.Rollback(ctx)
	if err := lockTable(ctx, tx, workspace, tableID); err != nil {
		return TableDto{}, err
	}
	created := tsExpr("created_at")
	updated := tsExpr("updated_at")
	sql := fmt.Sprintf(
		`UPDATE generic_table
		 SET name = $3, updated_at = now()
		 WHERE id = $1::uuid AND workspace_id = $2::uuid
		 RETURNING id::text, name, %s, %s`, created, updated)
	renamed, err := scanTable(tx.QueryRow(ctx, sql, tableID, workspace, clean))
	if errors.Is(err, pgx.ErrNoRows) {
		return TableDto{}, errNotFound("找不到这张表")
	}
	if err != nil {
		return TableDto{}, logFailed(err)
	}
	if actor != nil {
		if err := publishMetadataEvent(ctx, tx, workspace, renamed.ID, "table.renamed", "table", renamed.ID, *actor); err != nil {
			return TableDto{}, err
		}
	}
	if err := commitTx(ctx, tx); err != nil {
		return TableDto{}, err
	}
	return renamed, nil
}

func DeleteTable(ctx context.Context, pool Pool, workspace, tableID string) (DeletedDto, error) {
	tableLower := strings.ToLower(tableID)
	tx, err := beginTx(ctx, pool)
	if err != nil {
		return DeletedDto{}, err
	}
	defer tx.Rollback(ctx)
	if err := lockWorkspaceTablesSorted(ctx, tx, workspace, tableLower); err != nil {
		return DeletedDto{}, err
	}

	var propName string
	err = tx.QueryRow(ctx,
		`SELECT name FROM issue_property
		 WHERE workspace_id = $1::uuid
		   AND type = 'record_relation'
		   AND lower(COALESCE(config->>'target_table_id', '')) = $2
		 ORDER BY name
		 LIMIT 1`,
		workspace, tableLower,
	).Scan(&propName)
	if err == nil {
		return DeletedDto{}, errReferenceConflict(
			"table_in_use",
			fmt.Sprintf("无法删除表格：工作区任务属性「%s」正在关联这张表", propName),
			"当前存在指向该表的任务 record_relation 属性定义；在相关任务属性不再引用该表前无法删除表格",
		)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return DeletedDto{}, logFailed(err)
	}

	var otherTableName, fieldName string
	err = tx.QueryRow(ctx,
		`SELECT t.name, f.name
		 FROM generic_field f
		 JOIN generic_table t ON t.id = f.table_id AND t.workspace_id = f.workspace_id
		 WHERE f.workspace_id = $1::uuid
		   AND f.table_id <> $2::uuid
		   AND f.kind = 'relation'
		   AND lower(COALESCE(f.config->>'target_table_id', '')) = $3
		 ORDER BY t.name, f.name
		 LIMIT 1`,
		workspace, tableLower, tableLower,
	).Scan(&otherTableName, &fieldName)
	if err == nil {
		return DeletedDto{}, errReferenceConflict(
			"table_in_use",
			fmt.Sprintf("无法删除表格：表格「%s」的关联字段「%s」指向当前表", otherTableName, fieldName),
			"请先删除或解除其他表中指向当前表的关联字段后再删除表格",
		)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return DeletedDto{}, logFailed(err)
	}

	err = tx.QueryRow(ctx,
		`SELECT t.name, f.name
		 FROM generic_field f
		 JOIN generic_table t ON t.id = f.table_id AND t.workspace_id = f.workspace_id
		 WHERE f.workspace_id = $1::uuid
		   AND f.table_id <> $2::uuid
		   AND f.kind = 'lookup'
		   AND lower(COALESCE(f.config->>'target_field_id', '')) IN (
		       SELECT lower(id::text)
		       FROM generic_field
		       WHERE workspace_id = $1::uuid AND table_id = $2::uuid
		   )
		 ORDER BY t.name, f.name
		 LIMIT 1`,
		workspace, tableLower,
	).Scan(&otherTableName, &fieldName)
	if err == nil {
		return DeletedDto{}, errReferenceConflict(
			"table_in_use",
			fmt.Sprintf("无法删除表格：表格「%s」的查找字段「%s」依赖当前表的字段", otherTableName, fieldName),
			"请先删除其他表中引用当前表字段的查找字段后再删除表格",
		)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return DeletedDto{}, logFailed(err)
	}

	deleteStmts := []string{
		`DELETE FROM generic_cell
		 WHERE workspace_id = $1::uuid
		   AND record_id IN (
		       SELECT id FROM generic_record WHERE workspace_id = $1::uuid AND table_id = $2::uuid
		   )`,
		`DELETE FROM generic_record_comment WHERE workspace_id = $1::uuid AND table_id = $2::uuid`,
		`DELETE FROM generic_presence WHERE workspace_id = $1::uuid AND table_id = $2::uuid`,
		`INSERT INTO issue_attachment_object_delete_outbox (
		    attachment_id, workspace_id, storage_key, object_url
		 )
		 SELECT id, workspace_id, storage_key, object_url
		 FROM generic_table_attachment
		 WHERE workspace_id = $1::uuid AND table_id = $2::uuid
		 ON CONFLICT (attachment_id) DO NOTHING`,
		`DELETE FROM generic_table_attachment WHERE workspace_id = $1::uuid AND table_id = $2::uuid`,
		`DELETE FROM generic_record_batch_idempotency WHERE workspace_id = $1::uuid AND table_id = $2::uuid`,
		`DELETE FROM generic_change WHERE workspace_id = $1::uuid AND table_id = $2::uuid`,
		`DELETE FROM generic_record WHERE workspace_id = $1::uuid AND table_id = $2::uuid`,
		`DELETE FROM generic_view WHERE workspace_id = $1::uuid AND table_id = $2::uuid`,
		`DELETE FROM generic_field WHERE workspace_id = $1::uuid AND table_id = $2::uuid`,
		`DELETE FROM generic_table WHERE workspace_id = $1::uuid AND id = $2::uuid`,
	}
	for _, stmt := range deleteStmts {
		if _, err := tx.Exec(ctx, stmt, workspace, tableLower); err != nil {
			return DeletedDto{}, logFailed(err)
		}
	}

	if err := commitTx(ctx, tx); err != nil {
		return DeletedDto{}, err
	}
	return DeletedDto{Deleted: true, ID: tableLower}, nil
}

func ListFields(ctx context.Context, pool Pool, workspace, tableID string) ([]FieldDto, error) {
	tx, err := beginTx(ctx, pool)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	table, err := loadTable(ctx, tx, workspace, tableID)
	if err != nil {
		return nil, err
	}
	fields, err := loadFields(ctx, tx, workspace, table.ID)
	if err != nil {
		return nil, err
	}
	hydrateFormulaFields(fields)
	if err := commitTx(ctx, tx); err != nil {
		return nil, err
	}
	return fields, nil
}

func GetField(ctx context.Context, pool Pool, workspace, tableID, fieldID string) (FieldDto, error) {
	fields, err := ListFields(ctx, pool, workspace, tableID)
	if err != nil {
		return FieldDto{}, err
	}
	for _, item := range fields {
		if strings.EqualFold(item.ID, fieldID) {
			return item, nil
		}
	}
	return FieldDto{}, errNotFound("找不到这个字段")
}

func ListRecords(
	ctx context.Context,
	pool Pool,
	workspace, tableID string,
	viewID *string,
	limitOpt, offsetOpt *int64,
) (RecordListDto, error) {
	limit := int64(100)
	if limitOpt != nil {
		limit = *limitOpt
	}
	if limit < 1 || limit > 200 {
		return RecordListDto{}, errInvalid("记录分页数量需要在 1 到 200 之间")
	}
	offset := int64(0)
	if offsetOpt != nil {
		offset = *offsetOpt
	}
	if offset < 0 {
		return RecordListDto{}, errInvalid("记录偏移量不能为负数")
	}
	tx, err := beginTx(ctx, pool)
	if err != nil {
		return RecordListDto{}, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SET TRANSACTION ISOLATION LEVEL REPEATABLE READ`); err != nil {
		return RecordListDto{}, logFailed(err)
	}
	sheet, err := loadSheet(ctx, tx, workspace, tableID, viewID)
	if err != nil {
		return RecordListDto{}, err
	}
	if err := commitTx(ctx, tx); err != nil {
		return RecordListDto{}, err
	}

	total := int64(len(sheet.Records))
	start := int(offset)
	if start > len(sheet.Records) {
		start = len(sheet.Records)
	}
	end := start + int(limit)
	if end > len(sheet.Records) {
		end = len(sheet.Records)
	}
	page := make([]RecordDto, end-start)
	copy(page, sheet.Records[start:end])
	hasMore := int64(end) < total
	var nextOffset *int64
	if hasMore {
		v := int64(end)
		nextOffset = &v
	}
	return RecordListDto{
		Records:      page,
		Total:        total,
		Limit:        limit,
		Offset:       offset,
		HasMore:      hasMore,
		NextOffset:   nextOffset,
		ActiveViewID: sheet.ActiveViewID,
	}, nil
}

func GetRecord(ctx context.Context, pool Pool, workspace, tableID, recordID string) (RecordDto, error) {
	tx, err := beginTx(ctx, pool)
	if err != nil {
		return RecordDto{}, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SET TRANSACTION ISOLATION LEVEL REPEATABLE READ`); err != nil {
		return RecordDto{}, logFailed(err)
	}
	table, err := loadTable(ctx, tx, workspace, tableID)
	if err != nil {
		return RecordDto{}, err
	}
	fields, err := loadFields(ctx, tx, workspace, table.ID)
	if err != nil {
		return RecordDto{}, err
	}
	record, err := loadRecordInTable(ctx, tx, workspace, table.ID, fields, recordID)
	if err != nil {
		return RecordDto{}, err
	}
	records := []RecordDto{record}
	hasDerived := false
	for _, f := range fields {
		if f.Kind == "lookup" || f.Kind == "formula" {
			hasDerived = true
			break
		}
	}
	if hasDerived {
		if err := projectSheet(ctx, tx, workspace, table.ID, fields, records, primaryRowsSubset); err != nil {
			return RecordDto{}, err
		}
	}
	if err := commitTx(ctx, tx); err != nil {
		return RecordDto{}, err
	}
	return records[0], nil
}

func ListViews(ctx context.Context, pool Pool, workspace, tableID string) ([]ViewDto, error) {
	tx, err := beginTx(ctx, pool)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	table, err := loadTable(ctx, tx, workspace, tableID)
	if err != nil {
		return nil, err
	}
	views, err := loadViews(ctx, tx, workspace, table.ID)
	if err != nil {
		return nil, err
	}
	if err := commitTx(ctx, tx); err != nil {
		return nil, err
	}
	return views, nil
}

func GetView(ctx context.Context, pool Pool, workspace, tableID, viewID string) (ViewDto, error) {
	tx, err := beginTx(ctx, pool)
	if err != nil {
		return ViewDto{}, err
	}
	defer tx.Rollback(ctx)
	table, err := loadTable(ctx, tx, workspace, tableID)
	if err != nil {
		return ViewDto{}, err
	}
	view, err := loadView(ctx, tx, workspace, table.ID, viewID)
	if err != nil {
		return ViewDto{}, err
	}
	if err := commitTx(ctx, tx); err != nil {
		return ViewDto{}, err
	}
	return view, nil
}

func GetSheet(ctx context.Context, pool Pool, workspace, tableID string, viewID *string) (SheetDto, error) {
	tx, err := beginTx(ctx, pool)
	if err != nil {
		return SheetDto{}, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SET TRANSACTION ISOLATION LEVEL REPEATABLE READ`); err != nil {
		return SheetDto{}, logFailed(err)
	}
	sheet, err := loadSheet(ctx, tx, workspace, tableID, viewID)
	if err != nil {
		return SheetDto{}, err
	}
	if err := commitTx(ctx, tx); err != nil {
		return SheetDto{}, err
	}
	return sheet, nil
}

func ListRecordOptions(ctx context.Context, pool Pool, workspace, tableID string) ([]RecordOptionDto, error) {
	tx, err := beginTx(ctx, pool)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	table, err := loadTable(ctx, tx, workspace, tableID)
	if err != nil {
		return nil, err
	}
	fields, err := loadFields(ctx, tx, workspace, table.ID)
	if err != nil {
		return nil, err
	}
	records, err := loadRecords(ctx, tx, workspace, table.ID, fields)
	if err != nil {
		return nil, err
	}
	if err := commitTx(ctx, tx); err != nil {
		return nil, err
	}
	out := make([]RecordOptionDto, len(records))
	for i, rec := range records {
		out[i] = RecordOptionDto{
			ID:    rec.ID,
			Label: recordOptionLabel(fields, rec),
		}
	}
	return out, nil
}

func recordOptionLabel(fields []FieldDto, record RecordDto) string {
	if len(fields) > 0 {
		if label, ok := formatOptionValue(fields[0], record.Values[fields[0].ID]); ok {
			return label
		}
	}
	for i := 1; i < len(fields); i++ {
		if fields[i].Kind == "text" {
			if label, ok := formatOptionValue(fields[i], record.Values[fields[i].ID]); ok {
				return label
			}
		}
	}
	var short strings.Builder
	for _, r := range record.ID {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F') {
			short.WriteRune(r)
			if short.Len() >= 8 {
				break
			}
		}
	}
	return fmt.Sprintf("未命名记录 (%s)", short.String())
}

func formatOptionValue(field FieldDto, value any) (string, bool) {
	if value == nil {
		return "", false
	}
	var raw string
	switch field.Kind {
	case "text", "date":
		s, ok := value.(string)
		if !ok {
			return "", false
		}
		raw = strings.TrimSpace(s)
	case "number":
		f, ok := asFloat64(value)
		if !ok {
			return "", false
		}
		raw = strconv.FormatFloat(f, 'f', -1, 64)
	case "checkbox":
		b, ok := value.(bool)
		if !ok {
			return "", false
		}
		if b {
			raw = "是"
		} else {
			raw = "否"
		}
	case "single_select":
		picked, ok := value.(string)
		if !ok {
			return "", false
		}
		label := picked
		if cfgMap, ok := field.Config.(map[string]any); ok {
			if opts, ok := cfgMap["options"].([]any); ok {
				for _, optRaw := range opts {
					if opt, ok := optRaw.(map[string]any); ok {
						if id, _ := opt["id"].(string); id == picked {
							if lbl, ok := opt["label"].(string); ok {
								label = lbl
							}
							break
						}
					}
				}
			}
		}
		raw = strings.TrimSpace(label)
	default:
		return "", false
	}
	firstLine := raw
	if idx := strings.IndexAny(raw, "\r\n"); idx >= 0 {
		firstLine = raw[:idx]
	}
	firstLine = strings.TrimSpace(firstLine)
	if firstLine == "" {
		return "", false
	}
	if utf8.RuneCountInString(firstLine) > 80 {
		runes := []rune(firstLine)
		firstLine = string(runes[:80])
	}
	return firstLine, true
}

func CreateField(
	ctx context.Context,
	pool Pool,
	workspace, tableID, name, kind string,
	rawConfig any,
	actor *Actor,
) (FieldDto, error) {
	clean, err := CleanName(name)
	if err != nil {
		return FieldDto{}, err
	}
	config, err := NormalizeConfig(kind, rawConfig)
	if err != nil {
		return FieldDto{}, err
	}
	tx, err := beginTx(ctx, pool)
	if err != nil {
		return FieldDto{}, err
	}
	defer tx.Rollback(ctx)

	if kind == "relation" {
		field, err := createRelationField(ctx, tx, workspace, tableID, clean, config)
		if err != nil {
			return FieldDto{}, err
		}
		if actor != nil {
			if err := publishMetadataEvent(ctx, tx, workspace, tableID, "field.created", "field", field.ID, *actor); err != nil {
				return FieldDto{}, err
			}
			if rel, err := relationConfigFromStored(field.Config); err == nil && rel.Bidirectional && rel.ReverseFieldID != nil {
				if err := publishMetadataEvent(ctx, tx, workspace, rel.TargetTableID, "field.created", "field", *rel.ReverseFieldID, *actor); err != nil {
					return FieldDto{}, err
				}
			}
		}
		if err := commitTx(ctx, tx); err != nil {
			return FieldDto{}, err
		}
		return field, nil
	}

	if kind == "lookup" || kind == "formula" {
		field, err := createDerivedField(ctx, tx, workspace, tableID, clean, kind, config)
		if err != nil {
			return FieldDto{}, err
		}
		if actor != nil {
			if err := publishMetadataEvent(ctx, tx, workspace, tableID, "field.created", "field", field.ID, *actor); err != nil {
				return FieldDto{}, err
			}
		}
		if err := commitTx(ctx, tx); err != nil {
			return FieldDto{}, err
		}
		return field, nil
	}

	if err := lockTable(ctx, tx, workspace, tableID); err != nil {
		return FieldDto{}, err
	}
	pos, err := nextFieldPosition(ctx, tx, workspace, tableID)
	if err != nil {
		return FieldDto{}, err
	}
	cfgJSON, err := marshalCompactString(config)
	if err != nil {
		return FieldDto{}, errFailed(err)
	}
	created := tsExpr("created_at")
	updated := tsExpr("updated_at")
	sql := fmt.Sprintf(
		`INSERT INTO generic_field (workspace_id, table_id, name, kind, config, position)
		 VALUES ($1::uuid, $2::uuid, $3, $4, $5::jsonb, $6)
		 RETURNING id::text, name, kind, config::text, position, %s, %s`, created, updated)
	field, err := scanFieldRow(tx.QueryRow(ctx, sql, workspace, tableID, clean, kind, cfgJSON, pos), "请重试添加字段")
	if err != nil {
		return FieldDto{}, err
	}
	if actor != nil {
		if err := publishMetadataEvent(ctx, tx, workspace, tableID, "field.created", "field", field.ID, *actor); err != nil {
			return FieldDto{}, err
		}
	}
	if err := commitTx(ctx, tx); err != nil {
		return FieldDto{}, err
	}
	return field, nil
}

func UpdateField(
	ctx context.Context,
	pool Pool,
	workspace, tableID, fieldID string,
	name *string,
	hasConfig bool,
	rawConfig any,
	actor *Actor,
) (FieldDto, error) {
	if name == nil && !hasConfig {
		return FieldDto{}, errInvalid("没有要保存的字段变更")
	}
	var cleanName *string
	if name != nil {
		c, err := CleanName(*name)
		if err != nil {
			return FieldDto{}, err
		}
		cleanName = &c
	}
	tx, err := beginTx(ctx, pool)
	if err != nil {
		return FieldDto{}, err
	}
	defer tx.Rollback(ctx)

	if hasConfig {
		updated, err := updateDerivedFieldConfig(ctx, tx, workspace, tableID, fieldID, cleanName, rawConfig)
		if err != nil {
			return FieldDto{}, err
		}
		if actor != nil {
			if err := publishMetadataEvent(ctx, tx, workspace, tableID, "field.updated", "field", updated.ID, *actor); err != nil {
				return FieldDto{}, err
			}
		}
		if err := commitTx(ctx, tx); err != nil {
			return FieldDto{}, err
		}
		return updated, nil
	}

	if err := lockTable(ctx, tx, workspace, tableID); err != nil {
		return FieldDto{}, err
	}
	created := tsExpr("created_at")
	updated := tsExpr("updated_at")
	sql := fmt.Sprintf(
		`UPDATE generic_field
		 SET name = $4, updated_at = now()
		 WHERE id = $1::uuid AND table_id = $2::uuid AND workspace_id = $3::uuid
		 RETURNING id::text, name, kind, config::text, position, %s, %s`, created, updated)
	updatedField, err := scanFieldRowOptional(
		tx.QueryRow(ctx, sql, fieldID, tableID, workspace, *cleanName),
		"找不到这个字段",
		"请重试重命名字段",
	)
	if err != nil {
		return FieldDto{}, err
	}
	if updatedField.Kind == "formula" {
		fields, err := loadFields(ctx, tx, workspace, tableID)
		if err != nil {
			return FieldDto{}, err
		}
		names := make(map[string]string, len(fields))
		for _, item := range fields {
			names[item.ID] = item.Name
		}
		hydrateSingleFormula(&updatedField, names)
	}
	if actor != nil {
		if err := publishMetadataEvent(ctx, tx, workspace, tableID, "field.updated", "field", updatedField.ID, *actor); err != nil {
			return FieldDto{}, err
		}
	}
	if err := commitTx(ctx, tx); err != nil {
		return FieldDto{}, err
	}
	return updatedField, nil
}

func DeleteField(
	ctx context.Context,
	pool Pool,
	workspace, tableID, fieldID string,
	actor *Actor,
) (DeletedDto, error) {
	tableLower := strings.ToLower(tableID)
	fieldLower := strings.ToLower(fieldID)
	tx, err := beginTx(ctx, pool)
	if err != nil {
		return DeletedDto{}, err
	}
	defer tx.Rollback(ctx)
	if err := lockWorkspaceTablesSorted(ctx, tx, workspace, tableLower); err != nil {
		return DeletedDto{}, err
	}

	wsRows, err := tx.Query(ctx,
		`SELECT id::text, table_id::text, name, kind, config::text
		 FROM generic_field
		 WHERE workspace_id = $1::uuid
		 ORDER BY table_id, position, id`,
		workspace,
	)
	if err != nil {
		return DeletedDto{}, logFailed(err)
	}
	type wsField struct {
		id      string
		tableID string
		name    string
		kind    string
		config  any
	}
	var allFields []wsField
	for wsRows.Next() {
		var f wsField
		var raw string
		if err := wsRows.Scan(&f.id, &f.tableID, &f.name, &f.kind, &raw); err != nil {
			wsRows.Close()
			return DeletedDto{}, logFailed(err)
		}
		f.id = strings.ToLower(f.id)
		f.tableID = strings.ToLower(f.tableID)
		if err := json.Unmarshal([]byte(raw), &f.config); err != nil {
			wsRows.Close()
			return DeletedDto{}, errFailed(err)
		}
		allFields = append(allFields, f)
	}
	wsRows.Close()
	if err := wsRows.Err(); err != nil {
		return DeletedDto{}, logFailed(err)
	}

	var target *wsField
	for i := range allFields {
		if allFields[i].tableID == tableLower && allFields[i].id == fieldLower {
			target = &allFields[i]
			break
		}
	}
	if target == nil {
		return DeletedDto{}, errNotFound("找不到这个字段")
	}

	if target.kind == "relation" {
		if rel, err := relationConfigFromStored(target.config); err == nil && (rel.Bidirectional || rel.ReverseFieldID != nil) {
			return DeletedDto{}, errReferenceConflict(
				"field_in_use",
				fmt.Sprintf("无法删除双向关联字段「%s」：该字段与目标表反向字段成对绑定", target.name),
				"当前模型阻断单独删除双向关联字段以防止反向关联悬空；请保留成对结构或改用单向关联字段",
			)
		}
	}
	for _, other := range allFields {
		if other.id == fieldLower {
			continue
		}
		if other.kind == "relation" {
			if rel, err := relationConfigFromStored(other.config); err == nil && rel.ReverseFieldID != nil && *rel.ReverseFieldID == fieldLower {
				return DeletedDto{}, errReferenceConflict(
					"field_in_use",
					fmt.Sprintf("无法删除字段「%s」：它是关联字段「%s」的反向关联字段", target.name, other.name),
					"当前模型阻断单独删除双向关联字段以防止成对关联悬空",
				)
			}
		}
	}

	views, err := loadViews(ctx, tx, workspace, tableLower)
	if err != nil {
		return DeletedDto{}, err
	}
	for _, view := range views {
		if viewReferencesField(view, fieldLower) {
			return DeletedDto{}, errReferenceConflict(
				"field_in_use",
				fmt.Sprintf("无法删除字段：视图「%s」正在引用该字段（列、筛选、排序、分组或视图配置）", view.Name),
				"请先在对应视图中移除该字段的列、筛选、排序、分组或视图配置后再删除字段",
			)
		}
	}

	for _, other := range allFields {
		if other.id == fieldLower {
			continue
		}
		deps := fieldDependencies(other.kind, other.config)
		for _, dep := range deps {
			if strings.EqualFold(dep, fieldLower) {
				kindLabel := "查找"
				if other.kind == "formula" {
					kindLabel = "公式"
				}
				return DeletedDto{}, errReferenceConflict(
					"field_in_use",
					fmt.Sprintf("无法删除字段：%s字段「%s」依赖该字段", kindLabel, other.name),
					"请先修改或删除依赖该字段的公式或查找字段后再删除",
				)
			}
		}
	}

	var propName string
	err = tx.QueryRow(ctx,
		`SELECT name FROM issue_property
		 WHERE workspace_id = $1::uuid
		   AND type = 'lookup'
		   AND lower(COALESCE(config->>'target_field_id', '')) = $2
		 ORDER BY name
		 LIMIT 1`,
		workspace, fieldLower,
	).Scan(&propName)
	if err == nil {
		return DeletedDto{}, errReferenceConflict(
			"field_in_use",
			fmt.Sprintf("无法删除字段：工作区任务查找属性「%s」依赖该字段", propName),
			"当前存在引用该字段的任务 lookup 属性定义；在相关任务属性解除对该字段的引用前无法删除字段",
		)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return DeletedDto{}, logFailed(err)
	}

	if _, err := tx.Exec(ctx,
		`DELETE FROM generic_cell
		 WHERE workspace_id = $1::uuid
		   AND field_id = $2::uuid
		   AND record_id IN (
		       SELECT id FROM generic_record WHERE workspace_id = $1::uuid AND table_id = $3::uuid
		   )`,
		workspace, fieldLower, tableLower,
	); err != nil {
		return DeletedDto{}, logFailed(err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE generic_presence
		 SET field_id = NULL, updated_at = now()
		 WHERE workspace_id = $1::uuid AND table_id = $2::uuid AND field_id = $3::uuid`,
		workspace, tableLower, fieldLower,
	); err != nil {
		return DeletedDto{}, logFailed(err)
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM generic_field
		 WHERE workspace_id = $1::uuid AND table_id = $2::uuid AND id = $3::uuid`,
		workspace, tableLower, fieldLower,
	); err != nil {
		return DeletedDto{}, logFailed(err)
	}

	if actor != nil {
		if err := publishMetadataEvent(ctx, tx, workspace, tableLower, "field.deleted", "field", fieldLower, *actor); err != nil {
			return DeletedDto{}, err
		}
	}
	if err := commitTx(ctx, tx); err != nil {
		return DeletedDto{}, err
	}
	return DeletedDto{Deleted: true, ID: fieldLower}, nil
}

func CreateRecord(
	ctx context.Context,
	pool Pool,
	workspace, tableID string,
	values map[string]any,
	actor *Actor,
) (RecordDto, error) {
	tx, err := beginTx(ctx, pool)
	if err != nil {
		return RecordDto{}, err
	}
	defer tx.Rollback(ctx)
	touched := mapKeys(values)
	lockedTables, err := prepareTableLocks(ctx, tx, workspace, tableID, touched)
	if err != nil {
		return RecordDto{}, err
	}
	fields, err := loadFields(ctx, tx, workspace, tableID)
	if err != nil {
		return RecordDto{}, err
	}
	normalized, err := normalizeValues(fields, values)
	if err != nil {
		return RecordDto{}, err
	}
	record, err := createRecordInTx(ctx, tx, workspace, tableID, fields, normalized, lockedTables, actor, nil)
	if err != nil {
		return RecordDto{}, err
	}
	if err := commitTx(ctx, tx); err != nil {
		return RecordDto{}, err
	}
	return record, nil
}

func SubmitForm(
	ctx context.Context,
	pool Pool,
	workspace, tableID, viewID string,
	values map[string]any,
	actor *Actor,
) (RecordDto, error) {
	tx, err := beginTx(ctx, pool)
	if err != nil {
		return RecordDto{}, err
	}
	defer tx.Rollback(ctx)
	touched := mapKeys(values)
	lockedTables, err := prepareTableLocks(ctx, tx, workspace, tableID, touched)
	if err != nil {
		return RecordDto{}, err
	}
	viewDto, err := loadView(ctx, tx, workspace, tableID, viewID)
	if err != nil {
		return RecordDto{}, err
	}
	fields, err := loadFields(ctx, tx, workspace, tableID)
	if err != nil {
		return RecordDto{}, err
	}
	defs := defsOf(fields)
	normalized, err := ValidateFormSubmission(viewDto.Kind, viewDto.Columns, viewDto.Config, defs, values)
	if err != nil {
		return RecordDto{}, err
	}
	record, err := createRecordInTx(ctx, tx, workspace, tableID, fields, normalized, lockedTables, actor, nil)
	if err != nil {
		return RecordDto{}, err
	}
	if err := commitTx(ctx, tx); err != nil {
		return RecordDto{}, err
	}
	return record, nil
}

func validateAttachmentRefs(
	ctx context.Context,
	tx pgx.Tx,
	workspace, tableID string,
	fields []FieldDto,
	normalized map[string]cellPatch,
) error {
	attachFields := make(map[string]struct{})
	for _, f := range fields {
		if f.Kind == "attachment" {
			attachFields[f.ID] = struct{}{}
		}
	}
	if len(attachFields) == 0 {
		return nil
	}
	referenced := make(map[string]struct{})
	for fieldID, cell := range normalized {
		if _, ok := attachFields[fieldID]; !ok || !cell.valid {
			continue
		}
		items, ok := cell.value.([]any)
		if !ok {
			continue
		}
		for _, item := range items {
			id, ok := item.(string)
			if !ok {
				return errInvalid("附件标识必须是有效的 UUID")
			}
			referenced[strings.ToLower(id)] = struct{}{}
		}
	}
	if len(referenced) == 0 {
		return nil
	}
	ids := make([]string, 0, len(referenced))
	for id := range referenced {
		ids = append(ids, id)
	}
	rows, err := tx.Query(ctx,
		`SELECT lower(id::text)
		 FROM generic_table_attachment
		 WHERE workspace_id = $1::uuid
		   AND table_id = $2::uuid
		   AND status = 'ready'
		   AND id = ANY($3::text[]::uuid[])`,
		workspace, tableID, ids,
	)
	if err != nil {
		return logFailed(err)
	}
	defer rows.Close()
	ready := make(map[string]struct{})
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return logFailed(err)
		}
		ready[id] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return logFailed(err)
	}
	if len(ready) != len(referenced) {
		return errInvalid("附件不存在、尚未就绪或不属于当前表")
	}
	return nil
}

func createRecordInTx(
	ctx context.Context,
	tx pgx.Tx,
	workspace, tableID string,
	fields []FieldDto,
	normalized map[string]cellPatch,
	lockedTables []string,
	actor *Actor,
	batchID *string,
) (RecordDto, error) {
	if err := validateAttachmentRefs(ctx, tx, workspace, tableID, fields, normalized); err != nil {
		return RecordDto{}, err
	}
	reversePlan, err := validateAndPlanReverse(ctx, tx, workspace, fields, nil, normalized, lockedTables)
	if err != nil {
		return RecordDto{}, err
	}
	if reversePlan.hasWrites() && actor == nil {
		return RecordDto{}, errInvalid("创建包含双向关联的记录需要协作者身份")
	}
	created := tsExpr("created_at")
	updated := tsExpr("updated_at")
	sql := fmt.Sprintf(
		`INSERT INTO generic_record (workspace_id, table_id)
		 VALUES ($1::uuid, $2::uuid)
		 RETURNING id::text, revision, %s, %s`, created, updated)
	var id, createdAt, updatedAt string
	var rev int64
	if err := tx.QueryRow(ctx, sql, workspace, tableID).Scan(&id, &rev, &createdAt, &updatedAt); err != nil {
		return RecordDto{}, logFailed(err)
	}
	if err := writeCells(ctx, tx, workspace, id, normalized); err != nil {
		return RecordDto{}, err
	}
	if actor != nil {
		d, err := digestPatch(normalized)
		if err != nil {
			return RecordDto{}, err
		}
		recIDCopy := id
		if err := insertEvent(ctx, tx, eventWrite{
			workspace:      workspace,
			table:          tableID,
			eventType:      "record.created",
			topic:          "record",
			entityID:       id,
			recordID:       &recIDCopy,
			batchID:        batchID,
			baseRevision:   0,
			clientRevision: 0,
			recordRevision: 1,
			values:         normalized,
			actor:          *actor,
			digest:         d,
		}); err != nil {
			return RecordDto{}, err
		}
		if reversePlan.hasWrites() {
			if err := applyReversePlan(ctx, tx, workspace, id, reversePlan, *actor, batchID); err != nil {
				return RecordDto{}, err
			}
		}
	}
	return loadRecord(ctx, tx, workspace, fields, id)
}

func DeleteRecord(
	ctx context.Context,
	pool Pool,
	workspace, tableID, recordID string,
	expectedRevision int64,
	actor Actor,
) (DeletedDto, error) {
	if expectedRevision <= 0 {
		return DeletedDto{}, errInvalid("记录版本号无效")
	}
	tableLower := strings.ToLower(tableID)
	recordLower := strings.ToLower(recordID)
	tx, err := beginTx(ctx, pool)
	if err != nil {
		return DeletedDto{}, err
	}
	defer tx.Rollback(ctx)
	if err := lockWorkspaceTablesSorted(ctx, tx, workspace, tableLower); err != nil {
		return DeletedDto{}, err
	}
	deleted, err := deleteRecordInTx(ctx, tx, workspace, tableLower, recordLower, expectedRevision, actor, nil)
	if err != nil {
		return DeletedDto{}, err
	}
	if err := commitTx(ctx, tx); err != nil {
		return DeletedDto{}, err
	}
	return deleted, nil
}

func deleteRecordInTx(
	ctx context.Context,
	tx pgx.Tx,
	workspace, tableLower, recordID string,
	expectedRevision int64,
	actor Actor,
	batchID *string,
) (DeletedDto, error) {
	if expectedRevision <= 0 {
		return DeletedDto{}, errInvalid("记录版本号无效")
	}
	recordLower := strings.ToLower(recordID)
	current, err := lockRecord(ctx, tx, workspace, tableLower, recordLower)
	if err != nil {
		return DeletedDto{}, err
	}
	if current.Revision != expectedRevision {
		fields, err := loadFields(ctx, tx, workspace, tableLower)
		if err != nil {
			return DeletedDto{}, err
		}
		fresh, err := loadRecord(ctx, tx, workspace, fields, recordLower)
		if err != nil {
			return DeletedDto{}, err
		}
		return DeletedDto{}, errConflict("这条记录已在其他窗口更新，无法按过期版本删除", fresh)
	}

	var refByIssue bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS(
		     SELECT 1
		     FROM issue_property p
		     JOIN issue i ON i.workspace_id = p.workspace_id
		     WHERE p.workspace_id = $1::uuid
		       AND p.type = 'record_relation'
		       AND lower(COALESCE(p.config->>'target_table_id', '')) = $2
		       AND i.properties ? p.id::text
		       AND i.properties -> p.id::text @> jsonb_build_array($3::text)
		 )`,
		workspace, tableLower, recordLower,
	).Scan(&refByIssue); err != nil {
		return DeletedDto{}, logFailed(err)
	}
	if refByIssue {
		return DeletedDto{}, errReferenceConflict(
			"record_in_use",
			"无法删除记录：仍有任务引用了这条记录",
			"请先在引用该记录的任务中解除对应的 record_relation 关联后再删除记录",
		)
	}

	var refByRecord bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS(
		     SELECT 1
		     FROM generic_field f
		     JOIN generic_cell c ON c.workspace_id = f.workspace_id AND c.field_id = f.id
		     WHERE f.workspace_id = $1::uuid
		       AND f.kind = 'relation'
		       AND lower(COALESCE(f.config->>'target_table_id', '')) = $2
		       AND c.record_id <> $3::uuid
		       AND c.value @> jsonb_build_array($4::text)
		 )`,
		workspace, tableLower, recordLower, recordLower,
	).Scan(&refByRecord); err != nil {
		return DeletedDto{}, logFailed(err)
	}
	if refByRecord {
		return DeletedDto{}, errReferenceConflict(
			"record_in_use",
			"无法删除记录：仍有其他记录通过关联字段引用了这条记录",
			"请先清除其他记录中指向该记录的关联字段值后再删除记录",
		)
	}

	if _, err := tx.Exec(ctx,
		`DELETE FROM generic_cell WHERE workspace_id = $1::uuid AND record_id = $2::uuid`,
		workspace, recordLower,
	); err != nil {
		return DeletedDto{}, logFailed(err)
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM generic_record_comment
		 WHERE workspace_id = $1::uuid AND table_id = $2::uuid AND record_id = $3::uuid`,
		workspace, tableLower, recordLower,
	); err != nil {
		return DeletedDto{}, logFailed(err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE generic_presence
		 SET record_id = NULL, field_id = NULL, updated_at = now()
		 WHERE workspace_id = $1::uuid AND table_id = $2::uuid AND record_id = $3::uuid`,
		workspace, tableLower, recordLower,
	); err != nil {
		return DeletedDto{}, logFailed(err)
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM generic_record
		 WHERE workspace_id = $1::uuid AND table_id = $2::uuid AND id = $3::uuid`,
		workspace, tableLower, recordLower,
	); err != nil {
		return DeletedDto{}, logFailed(err)
	}

	d := DigestStr(fmt.Sprintf("record.deleted:%s:%d", recordLower, current.Revision))
	recCopy := recordLower
	if err := insertEvent(ctx, tx, eventWrite{
		workspace:      workspace,
		table:          tableLower,
		eventType:      "record.deleted",
		topic:          "record",
		entityID:       recordLower,
		recordID:       &recCopy,
		batchID:        batchID,
		baseRevision:   current.Revision,
		clientRevision: expectedRevision,
		recordRevision: current.Revision,
		values:         map[string]cellPatch{},
		actor:          actor,
		digest:         d,
	}); err != nil {
		return DeletedDto{}, err
	}

	revCopy := current.Revision
	return DeletedDto{Deleted: true, ID: recordLower, Revision: &revCopy}, nil
}

func CreateView(
	ctx context.Context,
	pool Pool,
	workspace, tableID string,
	write ViewWrite,
	actor *Actor,
) (ViewDto, error) {
	tx, err := beginTx(ctx, pool)
	if err != nil {
		return ViewDto{}, err
	}
	defer tx.Rollback(ctx)
	if err := lockTable(ctx, tx, workspace, tableID); err != nil {
		return ViewDto{}, err
	}
	fields, err := loadFieldDefs(ctx, tx, workspace, tableID)
	if err != nil {
		return ViewDto{}, err
	}
	kind := "grid"
	if write.Kind != nil {
		kind = *write.Kind
	}
	var cfg any = map[string]any{}
	if write.Config.Set {
		cfg = write.Config.Value
	}
	var group *string
	if write.Group.Set {
		group = write.Group.Value
	}
	spec, err := NormalizeView(tableID, write.Name, kind, cfg, write.Columns, write.Filter, write.Sort, group, fields)
	if err != nil {
		return ViewDto{}, err
	}
	saved, err := insertView(ctx, tx, workspace, tableID, spec, 0)
	if err != nil {
		return ViewDto{}, err
	}
	if actor != nil {
		if err := publishMetadataEvent(ctx, tx, workspace, tableID, "view.created", "view", saved.ID, *actor); err != nil {
			return ViewDto{}, err
		}
	}
	if err := commitTx(ctx, tx); err != nil {
		return ViewDto{}, err
	}
	return saved, nil
}

func SaveView(
	ctx context.Context,
	pool Pool,
	workspace, tableID, viewID string,
	write ViewWrite,
	actor *Actor,
) (ViewDto, error) {
	tx, err := beginTx(ctx, pool)
	if err != nil {
		return ViewDto{}, err
	}
	defer tx.Rollback(ctx)
	if err := lockTable(ctx, tx, workspace, tableID); err != nil {
		return ViewDto{}, err
	}
	existing, err := loadViewForUpdate(ctx, tx, workspace, tableID, viewID)
	if err != nil {
		return ViewDto{}, err
	}
	if write.Kind != nil {
		if !containsStr(ViewKinds, *write.Kind) {
			return ViewDto{}, errInvalid("不支持的视图类型")
		}
		if *write.Kind != existing.kind {
			return ViewDto{}, errInvalid("视图类型创建后不能修改")
		}
	}
	fields, err := loadFieldDefs(ctx, tx, workspace, tableID)
	if err != nil {
		return ViewDto{}, err
	}
	group := existing.group
	if write.Group.Set {
		group = write.Group.Value
	}
	cfg := existing.config
	if write.Config.Set {
		cfg = write.Config.Value
	}
	spec, err := NormalizeView(tableID, write.Name, existing.kind, cfg, write.Columns, write.Filter, write.Sort, group, fields)
	if err != nil {
		return ViewDto{}, err
	}
	saved, err := updateView(ctx, tx, workspace, tableID, viewID, spec)
	if err != nil {
		return ViewDto{}, err
	}
	if actor != nil {
		if err := publishMetadataEvent(ctx, tx, workspace, tableID, "view.updated", "view", saved.ID, *actor); err != nil {
			return ViewDto{}, err
		}
	}
	if err := commitTx(ctx, tx); err != nil {
		return ViewDto{}, err
	}
	return saved, nil
}

func DeleteView(
	ctx context.Context,
	pool Pool,
	workspace, tableID, viewID string,
	actor *Actor,
) (DeletedDto, error) {
	tableLower := strings.ToLower(tableID)
	viewLower := strings.ToLower(viewID)
	tx, err := beginTx(ctx, pool)
	if err != nil {
		return DeletedDto{}, err
	}
	defer tx.Rollback(ctx)
	if err := lockTable(ctx, tx, workspace, tableLower); err != nil {
		return DeletedDto{}, err
	}
	views, err := loadViews(ctx, tx, workspace, tableLower)
	if err != nil {
		return DeletedDto{}, err
	}
	var target *ViewDto
	for i := range views {
		if strings.EqualFold(views[i].ID, viewLower) {
			target = &views[i]
			break
		}
	}
	if target == nil {
		return DeletedDto{}, errNotFound("找不到这个视图")
	}
	if target.Kind == "grid" {
		gridCount := 0
		for _, v := range views {
			if v.Kind == "grid" {
				gridCount++
			}
		}
		if gridCount <= 1 {
			return DeletedDto{}, errReferenceConflict(
				"last_grid_view",
				"无法删除表格中最后一个网格视图",
				"每张表至少需要保留一个网格视图；请先新建其他网格视图后再删除",
			)
		}
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM generic_view
		 WHERE id = $1::uuid AND table_id = $2::uuid AND workspace_id = $3::uuid`,
		viewLower, tableLower, workspace,
	); err != nil {
		return DeletedDto{}, logFailed(err)
	}
	if actor != nil {
		if err := publishMetadataEvent(ctx, tx, workspace, tableLower, "view.deleted", "view", viewLower, *actor); err != nil {
			return DeletedDto{}, err
		}
	}
	if err := commitTx(ctx, tx); err != nil {
		return DeletedDto{}, err
	}
	return DeletedDto{Deleted: true, ID: viewLower}, nil
}

type existingViewMeta struct {
	kind   string
	config any
	group  *string
}

func loadViewForUpdate(ctx context.Context, tx pgx.Tx, workspace, tableID, viewID string) (existingViewMeta, error) {
	var kind, configRaw string
	var group *string
	err := tx.QueryRow(ctx,
		`SELECT kind, config::text, group_field
		 FROM generic_view
		 WHERE id = $1::uuid AND table_id = $2::uuid AND workspace_id = $3::uuid
		 FOR UPDATE`,
		viewID, tableID, workspace,
	).Scan(&kind, &configRaw, &group)
	if errors.Is(err, pgx.ErrNoRows) {
		return existingViewMeta{}, errNotFound("找不到这个视图")
	}
	if err != nil {
		return existingViewMeta{}, logFailed(err)
	}
	var cfg any
	if err := json.Unmarshal([]byte(configRaw), &cfg); err != nil {
		return existingViewMeta{}, errFailed(err)
	}
	return existingViewMeta{kind: kind, config: cfg, group: group}, nil
}

func loadView(ctx context.Context, tx pgx.Tx, workspace, tableID, viewID string) (ViewDto, error) {
	created := tsExpr("created_at")
	updated := tsExpr("updated_at")
	sql := fmt.Sprintf(
		`SELECT id::text, name, kind, config::text, columns::text, filter::text, sort::text, group_field, %s, %s
		 FROM generic_view
		 WHERE id = $1::uuid AND table_id = $2::uuid AND workspace_id = $3::uuid`, created, updated)
	v, err := scanView(tx.QueryRow(ctx, sql, viewID, tableID, workspace))
	if errors.Is(err, pgx.ErrNoRows) {
		return ViewDto{}, errNotFound("找不到这个视图")
	}
	if err != nil {
		return ViewDto{}, err
	}
	return v, nil
}

func loadSheet(ctx context.Context, tx pgx.Tx, workspace, tableID string, viewID *string) (SheetDto, error) {
	table, err := loadTable(ctx, tx, workspace, tableID)
	if err != nil {
		return SheetDto{}, err
	}
	fields, err := loadFields(ctx, tx, workspace, table.ID)
	if err != nil {
		return SheetDto{}, err
	}
	views, err := loadViews(ctx, tx, workspace, table.ID)
	if err != nil {
		return SheetDto{}, err
	}
	var active *string
	var activeView *ViewDto
	if viewID == nil {
		if len(views) > 0 {
			id := views[0].ID
			active = &id
			activeView = &views[0]
		}
	} else {
		for i := range views {
			if views[i].ID == *viewID {
				id := views[i].ID
				active = &id
				activeView = &views[i]
				break
			}
		}
		if activeView == nil {
			return SheetDto{}, errNotFound("找不到这个视图")
		}
	}

	records, err := loadRecords(ctx, tx, workspace, table.ID, fields)
	if err != nil {
		return SheetDto{}, err
	}
	if err := projectSheet(ctx, tx, workspace, table.ID, fields, records, primaryRowsAll); err != nil {
		return SheetDto{}, err
	}

	if activeView != nil {
		if activeView.Filter != nil {
			for _, f := range fields {
				if f.ID == activeView.Filter.FieldID {
					def := defOf(f)
					filtered := records[:0]
					for _, rec := range records {
						val, exists := rec.Values[f.ID]
						if MatchesFilter(def, val, exists && val != nil, *activeView.Filter) {
							filtered = append(filtered, rec)
						}
					}
					records = filtered
					break
				}
			}
		}
		if activeView.Sort != nil {
			for _, f := range fields {
				if f.ID == activeView.Sort.FieldID {
					def := defOf(f)
					desc := activeView.Sort.Direction == "desc"
					sort.Slice(records, func(i, j int) bool {
						lv, lex := records[i].Values[f.ID]
						rv, rex := records[j].Values[f.ID]
						lk := computeSortKey(def, lv, lex && lv != nil)
						rk := computeSortKey(def, rv, rex && rv != nil)
						lhas := lk.kind != sortKeyNone
						rhas := rk.kind != sortKeyNone
						switch {
						case !lhas && !rhas:
							return records[i].ID < records[j].ID
						case !lhas && rhas:
							return false
						case lhas && !rhas:
							return true
						default:
							cmp := compareSortKey(lk, rk)
							if desc {
								cmp = -cmp
							}
							if cmp != 0 {
								return cmp < 0
							}
							return records[i].ID < records[j].ID
						}
					})
					break
				}
			}
		}
	}

	return SheetDto{
		Table:        table,
		Fields:       fields,
		Views:        views,
		Records:      records,
		ActiveViewID: active,
	}, nil
}

func defOf(field FieldDto) FieldDef {
	return FieldDef{ID: field.ID, Kind: field.Kind, Config: field.Config}
}

func defsOf(fields []FieldDto) []FieldDef {
	out := make([]FieldDef, len(fields))
	for i, f := range fields {
		out[i] = defOf(f)
	}
	return out
}

func beginTx(ctx context.Context, pool Pool) (pgx.Tx, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, logFailed(err)
	}
	return tx, nil
}

func commitTx(ctx context.Context, tx pgx.Tx) error {
	if err := tx.Commit(ctx); err != nil {
		return logFailed(err)
	}
	return nil
}

func lockTable(ctx context.Context, tx pgx.Tx, workspace, tableID string) error {
	var id string
	err := tx.QueryRow(ctx,
		`SELECT id::text FROM generic_table WHERE id = $1::uuid AND workspace_id = $2::uuid FOR UPDATE`,
		tableID, workspace,
	).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return errNotFound("找不到这张表")
	}
	if err != nil {
		return logFailed(err)
	}
	return nil
}

func lockWorkspaceTablesSorted(ctx context.Context, tx pgx.Tx, workspace, primaryTable string) error {
	primary := strings.ToLower(primaryTable)
	rows, err := tx.Query(ctx,
		`SELECT id::text FROM generic_table WHERE workspace_id = $1::uuid ORDER BY id FOR UPDATE`,
		workspace,
	)
	if err != nil {
		return logFailed(err)
	}
	defer rows.Close()
	exists := false
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return logFailed(err)
		}
		if strings.EqualFold(id, primary) {
			exists = true
		}
	}
	if err := rows.Err(); err != nil {
		return logFailed(err)
	}
	if !exists {
		return errNotFound("找不到这张表")
	}
	return nil
}

func publishMetadataEvent(
	ctx context.Context,
	tx pgx.Tx,
	workspace, tableID, eventType, topic, entityID string,
	actor Actor,
) error {
	d := DigestStr(fmt.Sprintf("%s:%s", eventType, entityID))
	return insertEvent(ctx, tx, eventWrite{
		workspace:      workspace,
		table:          tableID,
		eventType:      eventType,
		topic:          topic,
		entityID:       entityID,
		recordID:       nil,
		batchID:        nil,
		baseRevision:   0,
		clientRevision: 0,
		recordRevision: 0,
		values:         map[string]cellPatch{},
		actor:          actor,
		digest:         d,
	})
}

func jsonContainsUUID(value any, targetLower string) bool {
	switch v := value.(type) {
	case string:
		return strings.EqualFold(v, targetLower)
	case []any:
		for _, item := range v {
			if jsonContainsUUID(item, targetLower) {
				return true
			}
		}
	case map[string]any:
		for _, item := range v {
			if jsonContainsUUID(item, targetLower) {
				return true
			}
		}
	}
	return false
}

func viewReferencesField(view ViewDto, fieldID string) bool {
	target := strings.ToLower(fieldID)
	for _, col := range view.Columns {
		if strings.EqualFold(col, target) {
			return true
		}
	}
	if view.Filter != nil && strings.EqualFold(view.Filter.FieldID, target) {
		return true
	}
	if view.Sort != nil && strings.EqualFold(view.Sort.FieldID, target) {
		return true
	}
	if view.Group != nil && strings.EqualFold(*view.Group, target) {
		return true
	}
	return jsonContainsUUID(view.Config, target)
}

func nextFieldPosition(ctx context.Context, tx pgx.Tx, workspace, tableID string) (int32, error) {
	var pos int32
	if err := tx.QueryRow(ctx,
		`SELECT (COALESCE(MAX(position), 0) + 1)::int FROM generic_field WHERE table_id = $1::uuid AND workspace_id = $2::uuid`,
		tableID, workspace,
	).Scan(&pos); err != nil {
		return 0, logFailed(err)
	}
	return pos, nil
}

func insertTable(ctx context.Context, tx pgx.Tx, workspace, name string) (TableDto, error) {
	created := tsExpr("created_at")
	updated := tsExpr("updated_at")
	sql := fmt.Sprintf(
		`INSERT INTO generic_table (workspace_id, name)
		 VALUES ($1::uuid, $2)
		 RETURNING id::text, name, %s, %s`, created, updated)
	t, err := scanTable(tx.QueryRow(ctx, sql, workspace, name))
	if err != nil {
		return TableDto{}, logFailed(err)
	}
	return t, nil
}

func insertView(ctx context.Context, tx pgx.Tx, workspace, tableID string, view StoredView, order int32) (ViewDto, error) {
	created := tsExpr("created_at")
	updated := tsExpr("updated_at")
	cfgStr, err := marshalCompactString(view.Config)
	if err != nil {
		return ViewDto{}, errFailed(err)
	}
	colsOpt, err := marshalOptionalSlice(view.Columns)
	if err != nil {
		return ViewDto{}, errFailed(err)
	}
	filterOpt, err := marshalOptionalPtr(view.Filter)
	if err != nil {
		return ViewDto{}, errFailed(err)
	}
	sortOpt, err := marshalOptionalPtr(view.Sort)
	if err != nil {
		return ViewDto{}, errFailed(err)
	}
	sql := fmt.Sprintf(
		`INSERT INTO generic_view (workspace_id, table_id, name, kind, config, columns, filter, sort, group_field, created_at, updated_at)
		 VALUES ($1::uuid, $2::uuid, $3, $4, $5::jsonb, $6::jsonb, $7::jsonb, $8::jsonb, $9,
		         now() + $10::int * interval '1 microsecond', now() + $10::int * interval '1 microsecond')
		 RETURNING id::text, name, kind, config::text, columns::text, filter::text, sort::text, group_field, %s, %s`,
		created, updated)
	return scanView(tx.QueryRow(ctx, sql, workspace, tableID, view.Name, view.Kind, cfgStr, colsOpt, filterOpt, sortOpt, view.Group, order))
}

func updateView(ctx context.Context, tx pgx.Tx, workspace, tableID, viewID string, spec StoredView) (ViewDto, error) {
	created := tsExpr("created_at")
	updated := tsExpr("updated_at")
	cfgStr, err := marshalCompactString(spec.Config)
	if err != nil {
		return ViewDto{}, errFailed(err)
	}
	colsOpt, err := marshalOptionalSlice(spec.Columns)
	if err != nil {
		return ViewDto{}, errFailed(err)
	}
	filterOpt, err := marshalOptionalPtr(spec.Filter)
	if err != nil {
		return ViewDto{}, errFailed(err)
	}
	sortOpt, err := marshalOptionalPtr(spec.Sort)
	if err != nil {
		return ViewDto{}, errFailed(err)
	}
	sql := fmt.Sprintf(
		`UPDATE generic_view
		 SET name = $4, config = $5::jsonb, columns = $6::jsonb, filter = $7::jsonb, sort = $8::jsonb, group_field = $9, updated_at = now()
		 WHERE id = $1::uuid AND table_id = $2::uuid AND workspace_id = $3::uuid
		 RETURNING id::text, name, kind, config::text, columns::text, filter::text, sort::text, group_field, %s, %s`,
		created, updated)
	return scanView(tx.QueryRow(ctx, sql, viewID, tableID, workspace, spec.Name, cfgStr, colsOpt, filterOpt, sortOpt, spec.Group))
}

func loadTable(ctx context.Context, tx pgx.Tx, workspace, tableID string) (TableDto, error) {
	created := tsExpr("created_at")
	updated := tsExpr("updated_at")
	sql := fmt.Sprintf(
		`SELECT id::text, name, %s, %s
		 FROM generic_table
		 WHERE id = $1::uuid AND workspace_id = $2::uuid`, created, updated)
	t, err := scanTable(tx.QueryRow(ctx, sql, tableID, workspace))
	if errors.Is(err, pgx.ErrNoRows) {
		return TableDto{}, errNotFound("找不到这张表")
	}
	if err != nil {
		return TableDto{}, logFailed(err)
	}
	return t, nil
}

func loadFields(ctx context.Context, tx pgx.Tx, workspace, tableID string) ([]FieldDto, error) {
	created := tsExpr("created_at")
	updated := tsExpr("updated_at")
	sql := fmt.Sprintf(
		`SELECT id::text, name, kind, config::text, position, %s, %s
		 FROM generic_field
		 WHERE table_id = $1::uuid AND workspace_id = $2::uuid
		 ORDER BY position, id`, created, updated)
	rows, err := tx.Query(ctx, sql, tableID, workspace)
	if err != nil {
		return nil, logFailed(err)
	}
	defer rows.Close()
	out := make([]FieldDto, 0)
	for rows.Next() {
		f, err := scanField(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, logFailed(err)
	}
	return out, nil
}

func loadFieldDefs(ctx context.Context, tx pgx.Tx, workspace, tableID string) ([]FieldDef, error) {
	fields, err := loadFields(ctx, tx, workspace, tableID)
	if err != nil {
		return nil, err
	}
	return defsOf(fields), nil
}

func loadViews(ctx context.Context, tx pgx.Tx, workspace, tableID string) ([]ViewDto, error) {
	created := tsExpr("created_at")
	updated := tsExpr("updated_at")
	sql := fmt.Sprintf(
		`SELECT id::text, name, kind, config::text, columns::text, filter::text, sort::text, group_field, %s, %s
		 FROM generic_view
		 WHERE table_id = $1::uuid AND workspace_id = $2::uuid
		 ORDER BY created_at, id`, created, updated)
	rows, err := tx.Query(ctx, sql, tableID, workspace)
	if err != nil {
		return nil, logFailed(err)
	}
	defer rows.Close()
	out := make([]ViewDto, 0)
	for rows.Next() {
		v, err := scanView(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, logFailed(err)
	}
	return out, nil
}

func loadRecords(ctx context.Context, tx pgx.Tx, workspace, tableID string, fields []FieldDto) ([]RecordDto, error) {
	created := tsExpr("created_at")
	updated := tsExpr("updated_at")
	sql := fmt.Sprintf(
		`SELECT id::text, revision, %s, %s
		 FROM generic_record
		 WHERE table_id = $1::uuid AND workspace_id = $2::uuid
		 ORDER BY created_at, id`, created, updated)
	rows, err := tx.Query(ctx, sql, tableID, workspace)
	if err != nil {
		return nil, logFailed(err)
	}
	type recMeta struct {
		id        string
		revision  int64
		createdAt string
		updatedAt string
	}
	var metas []recMeta
	for rows.Next() {
		var m recMeta
		if err := rows.Scan(&m.id, &m.revision, &m.createdAt, &m.updatedAt); err != nil {
			rows.Close()
			return nil, logFailed(err)
		}
		metas = append(metas, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, logFailed(err)
	}

	cellRows, err := tx.Query(ctx,
		`SELECT c.record_id::text, c.field_id::text, c.value::text
		 FROM generic_cell c
		 JOIN generic_record r ON r.id = c.record_id
		 WHERE r.table_id = $1::uuid AND r.workspace_id = $2::uuid AND c.workspace_id = $2::uuid`,
		tableID, workspace,
	)
	if err != nil {
		return nil, logFailed(err)
	}
	defer cellRows.Close()
	byRecord := make(map[string]map[string]any)
	for cellRows.Next() {
		var recordID, fieldID string
		var raw *string
		if err := cellRows.Scan(&recordID, &fieldID, &raw); err != nil {
			return nil, logFailed(err)
		}
		var val any
		if raw != nil {
			if err := json.Unmarshal([]byte(*raw), &val); err != nil {
				return nil, errFailed(err)
			}
		}
		m := byRecord[recordID]
		if m == nil {
			m = make(map[string]any)
			byRecord[recordID] = m
		}
		m[fieldID] = val
	}
	if err := cellRows.Err(); err != nil {
		return nil, logFailed(err)
	}

	out := make([]RecordDto, len(metas))
	for i, m := range metas {
		out[i] = buildRecordDto(m.id, m.revision, m.createdAt, m.updatedAt, fields, byRecord[m.id])
	}
	return out, nil
}

func lockRecord(ctx context.Context, tx pgx.Tx, workspace, tableID, recordID string) (RecordDto, error) {
	created := tsExpr("created_at")
	updated := tsExpr("updated_at")
	sql := fmt.Sprintf(
		`SELECT id::text, revision, %s, %s
		 FROM generic_record
		 WHERE id = $1::uuid AND table_id = $2::uuid AND workspace_id = $3::uuid
		 FOR UPDATE`, created, updated)
	var id, createdAt, updatedAt string
	var rev int64
	err := tx.QueryRow(ctx, sql, recordID, tableID, workspace).Scan(&id, &rev, &createdAt, &updatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return RecordDto{}, errNotFound("找不到这条记录")
	}
	if err != nil {
		return RecordDto{}, logFailed(err)
	}
	return buildRecordDto(id, rev, createdAt, updatedAt, nil, nil), nil
}

func loadRecordInTable(ctx context.Context, tx pgx.Tx, workspace, tableID string, fields []FieldDto, recordID string) (RecordDto, error) {
	created := tsExpr("created_at")
	updated := tsExpr("updated_at")
	sql := fmt.Sprintf(
		`SELECT id::text, revision, %s, %s
		 FROM generic_record
		 WHERE id = $1::uuid AND table_id = $2::uuid AND workspace_id = $3::uuid`, created, updated)
	var id, createdAt, updatedAt string
	var rev int64
	err := tx.QueryRow(ctx, sql, recordID, tableID, workspace).Scan(&id, &rev, &createdAt, &updatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return RecordDto{}, errNotFound("找不到这条记录")
	}
	if err != nil {
		return RecordDto{}, logFailed(err)
	}
	vals, err := loadRecordCells(ctx, tx, workspace, recordID)
	if err != nil {
		return RecordDto{}, err
	}
	return buildRecordDto(id, rev, createdAt, updatedAt, fields, vals), nil
}

func loadRecord(ctx context.Context, tx pgx.Tx, workspace string, fields []FieldDto, recordID string) (RecordDto, error) {
	created := tsExpr("created_at")
	updated := tsExpr("updated_at")
	sql := fmt.Sprintf(
		`SELECT id::text, revision, %s, %s
		 FROM generic_record
		 WHERE id = $1::uuid AND workspace_id = $2::uuid`, created, updated)
	var id, createdAt, updatedAt string
	var rev int64
	if err := tx.QueryRow(ctx, sql, recordID, workspace).Scan(&id, &rev, &createdAt, &updatedAt); err != nil {
		return RecordDto{}, logFailed(err)
	}
	vals, err := loadRecordCells(ctx, tx, workspace, recordID)
	if err != nil {
		return RecordDto{}, err
	}
	return buildRecordDto(id, rev, createdAt, updatedAt, fields, vals), nil
}

func loadRecordCells(ctx context.Context, tx pgx.Tx, workspace, recordID string) (map[string]any, error) {
	rows, err := tx.Query(ctx,
		`SELECT field_id::text, value::text FROM generic_cell WHERE record_id = $1::uuid AND workspace_id = $2::uuid`,
		recordID, workspace,
	)
	if err != nil {
		return nil, logFailed(err)
	}
	defer rows.Close()
	vals := make(map[string]any)
	for rows.Next() {
		var fieldID string
		var raw *string
		if err := rows.Scan(&fieldID, &raw); err != nil {
			return nil, logFailed(err)
		}
		var val any
		if raw != nil {
			if err := json.Unmarshal([]byte(*raw), &val); err != nil {
				return nil, errFailed(err)
			}
		}
		vals[fieldID] = val
	}
	if err := rows.Err(); err != nil {
		return nil, logFailed(err)
	}
	return vals, nil
}

func writeCells(ctx context.Context, tx pgx.Tx, workspace, recordID string, values map[string]cellPatch) error {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, fieldID := range keys {
		patch := values[fieldID]
		if !patch.valid {
			if _, err := tx.Exec(ctx,
				`DELETE FROM generic_cell WHERE record_id = $1::uuid AND field_id = $2::uuid AND workspace_id = $3::uuid`,
				recordID, fieldID, workspace,
			); err != nil {
				return logFailed(err)
			}
			continue
		}
		encoded, err := marshalCompactString(patch.value)
		if err != nil {
			return errFailed(err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO generic_cell (workspace_id, record_id, field_id, value)
			 VALUES ($1::uuid, $2::uuid, $3::uuid, $4::jsonb)
			 ON CONFLICT (record_id, field_id)
			 DO UPDATE SET value = EXCLUDED.value, workspace_id = EXCLUDED.workspace_id`,
			workspace, recordID, fieldID, encoded,
		); err != nil {
			return logFailed(err)
		}
	}
	return nil
}

func normalizeValues(fields []FieldDto, values map[string]any) (map[string]cellPatch, error) {
	defs := defsOf(fields)
	byID := make(map[string]FieldDef, len(defs))
	for _, d := range defs {
		byID[d.ID] = d
	}
	keys := mapKeys(values)
	sort.Strings(keys)
	normalized := make(map[string]cellPatch, len(values))
	for _, fieldID := range keys {
		field, ok := byID[fieldID]
		if !ok {
			return nil, errInvalid("有字段不属于这张表")
		}
		stored, valid, err := NormalizeValue(field, values[fieldID])
		if err != nil {
			return nil, err
		}
		normalized[fieldID] = cellPatch{value: stored, valid: valid}
	}
	return normalized, nil
}

func buildRecordDto(id string, rev int64, createdAt, updatedAt string, fields []FieldDto, values map[string]any) RecordDto {
	complete := make(map[string]any, len(fields))
	for _, f := range fields {
		if values != nil {
			if v, ok := values[f.ID]; ok {
				complete[f.ID] = v
				continue
			}
		}
		complete[f.ID] = nil
	}
	return RecordDto{
		ID:        id,
		Revision:  rev,
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
		Values:    complete,
	}
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanTable(row rowScanner) (TableDto, error) {
	var t TableDto
	err := row.Scan(&t.ID, &t.Name, &t.CreatedAt, &t.UpdatedAt)
	return t, err
}

func scanField(row rowScanner) (FieldDto, error) {
	var f FieldDto
	var cfgText string
	if err := row.Scan(&f.ID, &f.Name, &f.Kind, &cfgText, &f.Position, &f.CreatedAt, &f.UpdatedAt); err != nil {
		return FieldDto{}, err
	}
	if err := json.Unmarshal([]byte(cfgText), &f.Config); err != nil {
		return FieldDto{}, errFailed(err)
	}
	return f, nil
}

func scanFieldRow(row pgx.Row, retryMsg string) (FieldDto, error) {
	f, err := scanField(row)
	if err != nil {
		return FieldDto{}, mapWriteErr(err, retryMsg)
	}
	return f, nil
}

func scanFieldRowOptional(row pgx.Row, notFoundMsg, retryMsg string) (FieldDto, error) {
	f, err := scanField(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return FieldDto{}, errNotFound(notFoundMsg)
	}
	if err != nil {
		return FieldDto{}, mapWriteErr(err, retryMsg)
	}
	return f, nil
}

func scanView(row rowScanner) (ViewDto, error) {
	var v ViewDto
	var cfgRaw string
	var colsRaw, filterRaw, sortRaw *string
	if err := row.Scan(
		&v.ID, &v.Name, &v.Kind, &cfgRaw,
		&colsRaw, &filterRaw, &sortRaw, &v.Group,
		&v.CreatedAt, &v.UpdatedAt,
	); err != nil {
		return ViewDto{}, err
	}
	if err := json.Unmarshal([]byte(cfgRaw), &v.Config); err != nil {
		return ViewDto{}, errFailed(err)
	}
	if err := unmarshalOptionalJSON(colsRaw, &v.Columns); err != nil {
		return ViewDto{}, errFailed(err)
	}
	if err := unmarshalOptionalJSON(filterRaw, &v.Filter); err != nil {
		return ViewDto{}, errFailed(err)
	}
	if err := unmarshalOptionalJSON(sortRaw, &v.Sort); err != nil {
		return ViewDto{}, errFailed(err)
	}
	return v, nil
}

func unmarshalOptionalJSON(raw *string, dest any) error {
	if raw == nil || *raw == "" || *raw == "null" {
		return nil
	}
	return json.Unmarshal([]byte(*raw), dest)
}

func marshalOptionalSlice(s []string) (*string, error) {
	if s == nil {
		return nil, nil
	}
	b, err := marshalCompactString(s)
	if err != nil {
		return nil, err
	}
	return &b, nil
}

func marshalOptionalPtr[T any](p *T) (*string, error) {
	if p == nil {
		return nil, nil
	}
	b, err := marshalCompactString(p)
	if err != nil {
		return nil, err
	}
	return &b, nil
}

func logFailed(err error) *StoreError {
	if err != nil {
		var se *StoreError
		if errors.As(err, &se) {
			return se
		}
		slog.Error("generic table query failed", "error", err)
	}
	return errFailed(err)
}

func mapKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func containsStr(slice []string, item string) bool {
	for _, v := range slice {
		if v == item {
			return true
		}
	}
	return false
}

func cleanActorLabel(raw, source, id string) string {
	var b strings.Builder
	count := 0
	for _, r := range raw {
		if unicode.IsControl(r) {
			continue
		}
		b.WriteRune(r)
		count++
		if count >= 40 {
			break
		}
	}
	trimmed := strings.TrimSpace(b.String())
	if trimmed != "" {
		return trimmed
	}
	hex := make([]rune, 0, len(id))
	for _, r := range id {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F') {
			hex = append(hex, r)
		}
	}
	tail := string(hex)
	if len(hex) > 4 {
		tail = string(hex[len(hex)-4:])
	}
	if source == "agent" {
		return "智能体" + tail
	}
	return "成员" + tail
}
