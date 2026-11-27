package tables

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
)

const (
	TemplateVersion   uint32 = 1
	maxTemplateFields        = 200
	maxTemplateViews         = 100
	maxSampleRecords         = 50
)

var viewConfigSingleFieldKeys = []string{
	"title_field_id",
	"group_field_id",
	"date_field_id",
	"end_date_field_id",
	"parent_field_id",
}

type UnsupportedDependencyDto struct {
	FieldID         string  `json:"field_id"`
	FieldName       string  `json:"field_name"`
	Kind            string  `json:"kind"`
	Code            string  `json:"code"`
	TargetTableID   *string `json:"target_table_id,omitempty"`
	RelationFieldID *string `json:"relation_field_id,omitempty"`
	TargetFieldID   *string `json:"target_field_id,omitempty"`
}

type TemplateErrorKind int

const (
	TemplateErrStore TemplateErrorKind = iota
	TemplateErrNotFound
	TemplateErrInvalid
	TemplateErrUnsupportedDependency
)

type TemplateError struct {
	Kind         TemplateErrorKind
	Code         string
	Message      string
	Dependencies []UnsupportedDependencyDto
	StoreErr     *StoreError
}

func (e *TemplateError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.StoreErr != nil {
		return e.StoreErr.Error()
	}
	return "模板操作失败"
}

func tmplInvalid(code, msg string) *TemplateError {
	return &TemplateError{Kind: TemplateErrInvalid, Code: code, Message: msg}
}

func tmplNotFound(msg string) *TemplateError {
	return &TemplateError{Kind: TemplateErrNotFound, Code: "not_found", Message: msg}
}

func tmplUnsupported(msg string, deps []UnsupportedDependencyDto) *TemplateError {
	return &TemplateError{
		Kind:         TemplateErrUnsupportedDependency,
		Code:         "unsupported_template_dependency",
		Message:      msg,
		Dependencies: deps,
	}
}

func tmplFromStore(err error) *TemplateError {
	if err == nil {
		return nil
	}
	var te *TemplateError
	if errors.As(err, &te) {
		return te
	}
	se := errFailed(err)
	if se.Kind == ErrKindNotFound {
		return tmplNotFound(se.Message)
	}
	return &TemplateError{Kind: TemplateErrStore, StoreErr: se, Message: se.Message}
}

type TableTemplateDto struct {
	TemplateVersion uint32                    `json:"template_version"`
	SourceTableID   string                    `json:"source_table_id"`
	Name            string                    `json:"name"`
	Fields          []TemplateFieldDto        `json:"fields"`
	Views           []TemplateViewDto         `json:"views"`
	SampleRecords   []TemplateSampleRecordDto `json:"sample_records,omitempty"`
}

type TemplateFieldDto struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Position int32  `json:"position"`
	Config   any    `json:"config"`
}

type TemplateViewDto struct {
	ID      string        `json:"id"`
	Name    string        `json:"name"`
	Kind    string        `json:"kind"`
	Config  any           `json:"config"`
	Columns []string      `json:"columns"`
	Filter  *StoredFilter `json:"filter"`
	Sort    *StoredSort   `json:"sort"`
	Group   *string       `json:"group"`
}

type TemplateSampleRecordDto struct {
	Values map[string]any `json:"values"`
}

type TemplateImportRequest struct {
	TemplateVersion uint32                      `json:"template_version"`
	SourceTableID   *string                     `json:"source_table_id"`
	Name            string                      `json:"name"`
	Fields          []TemplateFieldInput        `json:"fields"`
	Views           []TemplateViewInput         `json:"views"`
	SampleRecords   []TemplateSampleRecordInput `json:"sample_records"`
}

type TemplateFieldInput struct {
	ID       *string `json:"id"`
	Name     string  `json:"name"`
	Kind     string  `json:"kind"`
	Position *int32  `json:"position"`
	Config   any     `json:"config"`
}

type TemplateViewInput struct {
	ID      *string       `json:"id"`
	Name    string        `json:"name"`
	Kind    *string       `json:"kind"`
	Config  any           `json:"config"`
	Columns []string      `json:"columns"`
	Filter  *StoredFilter `json:"filter"`
	Sort    *StoredSort   `json:"sort"`
	Group   *string       `json:"group"`
}

type TemplateSampleRecordInput struct {
	Values map[string]any `json:"values"`
}

type TemplateIDMap struct {
	SourceTableID *string           `json:"source_table_id,omitempty"`
	TableID       string            `json:"table_id"`
	Fields        map[string]string `json:"fields"`
	Views         map[string]string `json:"views"`
}

type TemplateImportResponse struct {
	Table        TableDto      `json:"table"`
	Fields       []FieldDto    `json:"fields"`
	Views        []ViewDto     `json:"views"`
	Records      []RecordDto   `json:"records"`
	ActiveViewID *string       `json:"active_view_id"`
	IDMap        TemplateIDMap `json:"id_map"`
}

type TableCreatedWithSchema struct {
	Table  TableDto   `json:"table"`
	View   ViewDto    `json:"view"`
	Fields []FieldDto `json:"fields"`
}

func ParseSampleRecordIDsQuery(raw *string) ([]string, *TemplateError) {
	if raw == nil {
		return nil, nil
	}
	trimmed := strings.TrimSpace(*raw)
	if trimmed == "" {
		return nil, nil
	}
	seen := make(map[string]struct{})
	var ids []string
	for _, part := range strings.Split(trimmed, ",") {
		cand := strings.TrimSpace(part)
		if !IsUUID(cand) {
			return nil, tmplInvalid("invalid_value", "sample_record_ids 包含无效的记录标识")
		}
		lower := strings.ToLower(cand)
		if _, dup := seen[lower]; !dup {
			seen[lower] = struct{}{}
			ids = append(ids, lower)
		}
		if len(ids) > maxSampleRecords {
			return nil, tmplInvalid("invalid_value", fmt.Sprintf("最多导出 %d 条示例记录", maxSampleRecords))
		}
	}
	return ids, nil
}

func ExportTemplate(
	ctx context.Context,
	pool Pool,
	workspace, tableID string,
	sampleRecordIDs []string,
) (TableTemplateDto, *TemplateError) {
	if len(sampleRecordIDs) > maxSampleRecords {
		return TableTemplateDto{}, tmplInvalid("invalid_value", fmt.Sprintf("最多导出 %d 条示例记录", maxSampleRecords))
	}
	tx, err := beginTx(ctx, pool)
	if err != nil {
		return TableTemplateDto{}, tmplFromStore(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SET TRANSACTION ISOLATION LEVEL REPEATABLE READ`); err != nil {
		return TableTemplateDto{}, tmplFromStore(logFailed(err))
	}
	table, err := loadTable(ctx, tx, workspace, tableID)
	if err != nil {
		return TableTemplateDto{}, tmplFromStore(err)
	}
	fields, err := loadFields(ctx, tx, workspace, table.ID)
	if err != nil {
		return TableTemplateDto{}, tmplFromStore(err)
	}
	views, err := loadViews(ctx, tx, workspace, table.ID)
	if err != nil {
		return TableTemplateDto{}, tmplFromStore(err)
	}

	unsupported, terr := checkExportDependencies(table.ID, fields)
	if terr != nil {
		return TableTemplateDto{}, terr
	}
	if len(unsupported) > 0 {
		return TableTemplateDto{}, tmplUnsupported("模板导出不支持跨表关联或外部查找依赖", unsupported)
	}
	for _, view := range views {
		if terr := checkPortableViewFilter(view.Filter, func(id string) (string, bool) {
			for _, f := range fields {
				if f.ID == id {
					return f.Kind, true
				}
			}
			return "", false
		}); terr != nil {
			return TableTemplateDto{}, terr
		}
	}

	hydrateFormulaFields(fields)

	var sampleRecords []TemplateSampleRecordDto
	if len(sampleRecordIDs) > 0 {
		sampleRecords, terr = exportSampleRecords(ctx, tx, workspace, table.ID, fields, sampleRecordIDs)
		if terr != nil {
			return TableTemplateDto{}, terr
		}
	}

	if err := commitTx(ctx, tx); err != nil {
		return TableTemplateDto{}, tmplFromStore(err)
	}

	exportFields := make([]TemplateFieldDto, len(fields))
	for i, f := range fields {
		exportFields[i] = TemplateFieldDto{
			ID:       f.ID,
			Name:     f.Name,
			Kind:     f.Kind,
			Position: f.Position,
			Config:   cleanExportFieldConfig(f, table.ID),
		}
	}
	exportViews := make([]TemplateViewDto, len(views))
	for i, v := range views {
		exportViews[i] = TemplateViewDto{
			ID:      v.ID,
			Name:    v.Name,
			Kind:    v.Kind,
			Config:  v.Config,
			Columns: v.Columns,
			Filter:  v.Filter,
			Sort:    v.Sort,
			Group:   v.Group,
		}
	}
	return TableTemplateDto{
		TemplateVersion: TemplateVersion,
		SourceTableID:   table.ID,
		Name:            table.Name,
		Fields:          exportFields,
		Views:           exportViews,
		SampleRecords:   sampleRecords,
	}, nil
}

func checkExportDependencies(tableID string, fields []FieldDto) ([]UnsupportedDependencyDto, *TemplateError) {
	byID := make(map[string]FieldDto, len(fields))
	for _, f := range fields {
		byID[strings.ToLower(f.ID)] = f
	}
	var unsupported []UnsupportedDependencyDto
	for _, f := range fields {
		switch f.Kind {
		case "relation":
			rel, err := relationConfigFromStored(f.Config)
			if err != nil {
				return nil, tmplFromStore(err)
			}
			targetCopy := rel.TargetTableID
			if rel.Bidirectional || rel.ReverseFieldID != nil {
				unsupported = append(unsupported, UnsupportedDependencyDto{
					FieldID:       f.ID,
					FieldName:     f.Name,
					Kind:          f.Kind,
					Code:          "bidirectional_relation_unsupported",
					TargetTableID: &targetCopy,
				})
			} else if !strings.EqualFold(rel.TargetTableID, tableID) {
				unsupported = append(unsupported, UnsupportedDependencyDto{
					FieldID:       f.ID,
					FieldName:     f.Name,
					Kind:          f.Kind,
					Code:          "external_relation_unsupported",
					TargetTableID: &targetCopy,
				})
			}
		case "lookup":
			lookup, err := lookupConfigFromStored(f.Config)
			if err != nil {
				return nil, tmplInvalid("invalid_template", err.Error())
			}
			relField, relFound := byID[lookup.RelationFieldID]
			relIsSelf := false
			var targetTableID *string
			if relFound && relField.Kind == "relation" {
				if rel, err := relationConfigFromStored(relField.Config); err == nil {
					t := rel.TargetTableID
					targetTableID = &t
					if !rel.Bidirectional && rel.ReverseFieldID == nil && strings.EqualFold(rel.TargetTableID, tableID) {
						relIsSelf = true
					}
				}
			}
			_, targetIsLocal := byID[lookup.TargetFieldID]
			if !relIsSelf || !targetIsLocal {
				relIDCopy := lookup.RelationFieldID
				tgtIDCopy := lookup.TargetFieldID
				unsupported = append(unsupported, UnsupportedDependencyDto{
					FieldID:         f.ID,
					FieldName:       f.Name,
					Kind:            f.Kind,
					Code:            "external_lookup_unsupported",
					TargetTableID:   targetTableID,
					RelationFieldID: &relIDCopy,
					TargetFieldID:   &tgtIDCopy,
				})
			}
		}
	}
	return unsupported, nil
}

func cleanExportFieldConfig(field FieldDto, tableID string) any {
	switch field.Kind {
	case "relation":
		return relationConfig{
			TargetTableID: strings.ToLower(tableID),
			Bidirectional: false,
		}.toJSON()
	case "lookup":
		if cfg, err := lookupConfigFromStored(field.Config); err == nil {
			return cfg.toJSON()
		}
		return field.Config
	default:
		return field.Config
	}
}

func isPortableSampleKind(kind string) bool {
	switch kind {
	case "text", "number", "date", "single_select", "checkbox":
		return true
	default:
		return false
	}
}

func exportSampleRecords(
	ctx context.Context,
	tx pgx.Tx,
	workspace, tableID string,
	fields []FieldDto,
	requestedIDs []string,
) ([]TemplateSampleRecordDto, *TemplateError) {
	portableFields := make([]string, 0)
	for _, f := range fields {
		if isPortableSampleKind(f.Kind) {
			portableFields = append(portableFields, f.ID)
		}
	}
	rows, err := tx.Query(ctx,
		`SELECT r.id::text, c.field_id::text, c.value::text
		 FROM generic_record r
		 LEFT JOIN generic_cell c ON c.record_id = r.id AND c.workspace_id = r.workspace_id
		   AND c.field_id IN (SELECT unnest($4::text[])::uuid)
		 WHERE r.workspace_id = $1::uuid AND r.table_id = $2::uuid
		   AND r.id IN (SELECT unnest($3::text[])::uuid)`,
		workspace, tableID, requestedIDs, portableFields,
	)
	if err != nil {
		return nil, tmplFromStore(logFailed(err))
	}
	defer rows.Close()
	byRecord := make(map[string]map[string]any)
	for rows.Next() {
		var recID string
		var fieldID, rawVal *string
		if err := rows.Scan(&recID, &fieldID, &rawVal); err != nil {
			return nil, tmplFromStore(logFailed(err))
		}
		m, exists := byRecord[recID]
		if !exists {
			m = make(map[string]any)
			byRecord[recID] = m
		}
		if fieldID != nil && rawVal != nil {
			var val any
			if err := json.Unmarshal([]byte(*rawVal), &val); err != nil {
				return nil, tmplFromStore(errFailed(err))
			}
			if val != nil {
				m[*fieldID] = val
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, tmplFromStore(logFailed(err))
	}

	out := make([]TemplateSampleRecordDto, 0, len(requestedIDs))
	for _, reqID := range requestedIDs {
		vals, ok := byRecord[reqID]
		if !ok {
			return nil, tmplNotFound("找不到指定的示例记录")
		}
		out = append(out, TemplateSampleRecordDto{Values: vals})
	}
	return out, nil
}

func CreateTableWithFields(
	ctx context.Context,
	pool Pool,
	workspace, name string,
	fields []TemplateFieldInput,
) (TableCreatedWithSchema, *TemplateError) {
	req := TemplateImportRequest{
		TemplateVersion: TemplateVersion,
		Name:            name,
		Fields:          fields,
	}
	created, terr := ImportTemplate(ctx, pool, workspace, req, nil)
	if terr != nil {
		return TableCreatedWithSchema{}, terr
	}
	if len(created.Views) == 0 {
		return TableCreatedWithSchema{}, tmplFromStore(errFailed(nil))
	}
	return TableCreatedWithSchema{
		Table:  created.Table,
		View:   created.Views[len(created.Views)-1],
		Fields: created.Fields,
	}, nil
}

func ImportTemplate(
	ctx context.Context,
	pool Pool,
	workspace string,
	req TemplateImportRequest,
	actor *Actor,
) (TemplateImportResponse, *TemplateError) {
	if req.TemplateVersion != TemplateVersion {
		return TemplateImportResponse{}, tmplInvalid(
			"unsupported_template_version",
			fmt.Sprintf("不支持的模板版本：%d（当前仅支持版本 %d）", req.TemplateVersion, TemplateVersion),
		)
	}
	tableName, err := CleanName(req.Name)
	if err != nil {
		return TemplateImportResponse{}, tmplInvalid("invalid_value", err.Error())
	}
	var sourceTableID *string
	if req.SourceTableID != nil {
		trimmed := strings.TrimSpace(*req.SourceTableID)
		if trimmed != "" {
			if !IsUUID(trimmed) {
				return TemplateImportResponse{}, tmplInvalid("invalid_template", "source_table_id 必须是有效的 UUID")
			}
			lower := strings.ToLower(trimmed)
			sourceTableID = &lower
		}
	}
	if len(req.Fields) > maxTemplateFields {
		return TemplateImportResponse{}, tmplInvalid("invalid_template", fmt.Sprintf("模板字段数量不能超过 %d 个", maxTemplateFields))
	}
	if len(req.Views) > maxTemplateViews {
		return TemplateImportResponse{}, tmplInvalid("invalid_template", fmt.Sprintf("模板视图数量不能超过 %d 个", maxTemplateViews))
	}
	if len(req.Views) > 0 {
		hasGrid := false
		for _, v := range req.Views {
			kind := "grid"
			if v.Kind != nil {
				kind = *v.Kind
			}
			if kind == "grid" {
				hasGrid = true
				break
			}
		}
		if !hasGrid {
			return TemplateImportResponse{}, tmplInvalid("invalid_template", "模板必须保留至少一个网格视图")
		}
	}
	if len(req.SampleRecords) > maxSampleRecords {
		return TemplateImportResponse{}, tmplInvalid("invalid_template", fmt.Sprintf("模板示例记录数量不能超过 %d 条", maxSampleRecords))
	}

	preparedFields, terr := prepareTemplateFields(req.Fields)
	if terr != nil {
		return TemplateImportResponse{}, terr
	}

	tx, err := beginTx(ctx, pool)
	if err != nil {
		return TemplateImportResponse{}, tmplFromStore(err)
	}
	defer tx.Rollback(ctx)

	table, err := insertTable(ctx, tx, workspace, tableName)
	if err != nil {
		return TemplateImportResponse{}, tmplFromStore(err)
	}

	newFieldIDs := make([]string, len(preparedFields))
	for i := range preparedFields {
		var id string
		if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
			return TemplateImportResponse{}, tmplFromStore(logFailed(err))
		}
		newFieldIDs[i] = strings.ToLower(id)
	}

	fieldIDMap := make(map[string]string)
	publicFieldIDMap := make(map[string]string)
	for i, spec := range preparedFields {
		if spec.hasSourceID {
			fieldIDMap[spec.canonicalSourceID] = newFieldIDs[i]
			publicFieldIDMap[spec.displaySourceID] = newFieldIDs[i]
		}
	}

	resolvedConfigs, terr := resolveAndValidateTemplateFields(table.ID, sourceTableID, preparedFields, newFieldIDs, fieldIDMap)
	if terr != nil {
		return TemplateImportResponse{}, terr
	}

	created := tsExpr("created_at")
	updated := tsExpr("updated_at")
	insertFieldSQL := fmt.Sprintf(
		`INSERT INTO generic_field (id, workspace_id, table_id, name, kind, config, position)
		 VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, $6::jsonb, $7)
		 RETURNING id::text, name, kind, config::text, position, %s, %s`, created, updated)
	insertedFields := make([]FieldDto, 0, len(preparedFields))
	for idx, spec := range preparedFields {
		cfgStr, err := marshalCompactString(resolvedConfigs[idx])
		if err != nil {
			return TemplateImportResponse{}, tmplFromStore(errFailed(err))
		}
		f, err := scanFieldRow(
			tx.QueryRow(ctx, insertFieldSQL, newFieldIDs[idx], workspace, table.ID, spec.name, spec.kind, cfgStr, int32(idx+1)),
			"请重试导入字段",
		)
		if err != nil {
			return TemplateImportResponse{}, tmplFromStore(err)
		}
		insertedFields = append(insertedFields, f)
	}

	fieldDefs := defsOf(insertedFields)
	insertedViews := make([]ViewDto, 0)
	viewIDMap := make(map[string]string)

	if len(req.Views) == 0 {
		v, err := insertView(ctx, tx, workspace, table.ID, StoredView{
			Name:   "全部",
			Kind:   "grid",
			Config: map[string]any{},
		}, 0)
		if err != nil {
			return TemplateImportResponse{}, tmplFromStore(err)
		}
		insertedViews = append(insertedViews, v)
	} else {
		seenViewIDs := make(map[string]struct{})
		for order, v := range req.Views {
			var srcViewID *string
			if v.ID != nil {
				trimmed := strings.TrimSpace(*v.ID)
				if trimmed == "" {
					return TemplateImportResponse{}, tmplInvalid("invalid_template", "模板视图标识不能为空")
				}
				canon := canonicalRefKey(trimmed)
				if _, dup := seenViewIDs[canon]; dup {
					return TemplateImportResponse{}, tmplInvalid("invalid_template", fmt.Sprintf("模板中存在重复的视图标识：%s", trimmed))
				}
				seenViewIDs[canon] = struct{}{}
				srcViewID = &trimmed
			}
			storedView, terr := rebindAndValidateView(table.ID, v, fieldIDMap, fieldDefs)
			if terr != nil {
				return TemplateImportResponse{}, terr
			}
			createdView, err := insertView(ctx, tx, workspace, table.ID, storedView, int32(order))
			if err != nil {
				return TemplateImportResponse{}, tmplFromStore(err)
			}
			if srcViewID != nil {
				viewIDMap[*srcViewID] = createdView.ID
			}
			insertedViews = append(insertedViews, createdView)
		}
	}

	records := make([]RecordDto, 0, len(req.SampleRecords))
	if len(req.SampleRecords) > 0 {
		byNewID := make(map[string]FieldDto, len(insertedFields))
		for _, f := range insertedFields {
			byNewID[f.ID] = f
		}
		locked := []string{table.ID}
		for _, sample := range req.SampleRecords {
			reboundValues, terr := rebindSampleRecordValues(sample.Values, fieldIDMap, byNewID)
			if terr != nil {
				return TemplateImportResponse{}, terr
			}
			normalized, err := normalizeValues(insertedFields, reboundValues)
			if err != nil {
				return TemplateImportResponse{}, tmplFromStore(err)
			}
			rec, err := createRecordInTx(ctx, tx, workspace, table.ID, insertedFields, normalized, locked, actor, nil)
			if err != nil {
				return TemplateImportResponse{}, tmplFromStore(err)
			}
			records = append(records, rec)
		}
	}

	hasDerived := false
	for _, f := range insertedFields {
		if f.Kind == "lookup" || f.Kind == "formula" {
			hasDerived = true
			break
		}
	}
	if len(records) > 0 && hasDerived {
		if err := projectSheet(ctx, tx, workspace, table.ID, insertedFields, records, primaryRowsAll); err != nil {
			return TemplateImportResponse{}, tmplFromStore(err)
		}
	} else {
		hydrateFormulaFields(insertedFields)
	}

	if err := commitTx(ctx, tx); err != nil {
		return TemplateImportResponse{}, tmplFromStore(err)
	}

	var activeViewID *string
	if len(insertedViews) > 0 {
		id := insertedViews[0].ID
		activeViewID = &id
	}
	return TemplateImportResponse{
		Table:        table,
		Fields:       insertedFields,
		Views:        insertedViews,
		Records:      records,
		ActiveViewID: activeViewID,
		IDMap: TemplateIDMap{
			SourceTableID: sourceTableID,
			TableID:       table.ID,
			Fields:        publicFieldIDMap,
			Views:         viewIDMap,
		},
	}, nil
}

type preparedTemplateField struct {
	hasSourceID       bool
	canonicalSourceID string
	displaySourceID   string
	name              string
	kind              string
	config            any
}

func canonicalRefKey(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if IsUUID(trimmed) {
		return strings.ToLower(trimmed)
	}
	return trimmed
}

func prepareTemplateFields(fields []TemplateFieldInput) ([]preparedTemplateField, *TemplateError) {
	seenNames := make(map[string]struct{}, len(fields))
	seenIDs := make(map[string]struct{}, len(fields))
	out := make([]preparedTemplateField, 0, len(fields))
	for _, f := range fields {
		name, err := CleanName(f.Name)
		if err != nil {
			return nil, tmplInvalid("invalid_value", err.Error())
		}
		if _, dup := seenNames[name]; dup {
			return nil, tmplInvalid("duplicate_field_name", fmt.Sprintf("模板中存在重复的字段名称：%s", name))
		}
		seenNames[name] = struct{}{}
		if !slices.Contains(Kinds, f.Kind) {
			return nil, tmplInvalid("invalid_value", fmt.Sprintf("不支持的字段类型：%s", f.Kind))
		}
		if f.Position != nil && *f.Position <= 0 {
			return nil, tmplInvalid("invalid_template", "字段位置必须是正整数")
		}
		pf := preparedTemplateField{
			name:   name,
			kind:   f.Kind,
			config: f.Config,
		}
		if f.ID != nil {
			trimmed := strings.TrimSpace(*f.ID)
			if trimmed == "" {
				return nil, tmplInvalid("invalid_template", "模板字段标识不能为空")
			}
			canon := canonicalRefKey(trimmed)
			if _, dup := seenIDs[canon]; dup {
				return nil, tmplInvalid("duplicate_field_id", fmt.Sprintf("模板中存在重复的字段标识：%s", trimmed))
			}
			seenIDs[canon] = struct{}{}
			pf.hasSourceID = true
			pf.canonicalSourceID = canon
			pf.displaySourceID = trimmed
		}
		out = append(out, pf)
	}
	return out, nil
}

func resolveAndValidateTemplateFields(
	newTableID string,
	sourceTableID *string,
	prepared []preparedTemplateField,
	newFieldIDs []string,
	fieldIDMap map[string]string,
) ([]any, *TemplateError) {
	resolved := make([]any, len(prepared))
	var unsupported []UnsupportedDependencyDto
	kindByNewID := make(map[string]string, len(prepared))
	nameByNewID := make(map[string]string, len(prepared))
	for i, spec := range prepared {
		kindByNewID[newFieldIDs[i]] = spec.kind
		nameByNewID[newFieldIDs[i]] = spec.name
	}

	// Pass A: non-derived fields (base fields and self-relations).
	for idx, spec := range prepared {
		if spec.kind == "lookup" || spec.kind == "formula" {
			continue
		}
		if spec.kind == "relation" {
			labelID := spec.name
			if spec.hasSourceID {
				labelID = spec.displaySourceID
			}
			cfg, dep, invMsg := rebindRelationConfig(newTableID, sourceTableID, labelID, spec.name, spec.config)
			if dep != nil {
				unsupported = append(unsupported, *dep)
				continue
			}
			if invMsg != "" {
				return nil, tmplInvalid("invalid_value", invMsg)
			}
			resolved[idx] = cfg
			continue
		}
		norm, err := NormalizeConfig(spec.kind, spec.config)
		if err != nil {
			return nil, tmplInvalid("invalid_value", err.Error())
		}
		resolved[idx] = norm
	}

	// Pass B: lookup fields.
	for idx, spec := range prepared {
		if spec.kind != "lookup" {
			continue
		}
		labelID := spec.name
		if spec.hasSourceID {
			labelID = spec.displaySourceID
		}
		rawRel, rawTarget, terr := parseTemplateLookupKeys(spec.config)
		if terr != nil {
			return nil, terr
		}
		mappedRel, relOK := fieldIDMap[canonicalRefKey(rawRel)]
		mappedTarget, targetOK := fieldIDMap[canonicalRefKey(rawTarget)]
		relIsLocal := relOK && kindByNewID[mappedRel] == "relation"
		if !relOK || !targetOK || !relIsLocal {
			rCopy := rawRel
			tCopy := rawTarget
			unsupported = append(unsupported, UnsupportedDependencyDto{
				FieldID:         labelID,
				FieldName:       spec.name,
				Kind:            spec.kind,
				Code:            "external_lookup_unsupported",
				RelationFieldID: &rCopy,
				TargetFieldID:   &tCopy,
			})
			continue
		}
		rebound := lookupConfig{RelationFieldID: mappedRel, TargetFieldID: mappedTarget}.toJSON()
		norm, err := NormalizeConfig("lookup", rebound)
		if err != nil {
			return nil, tmplInvalid("invalid_value", err.Error())
		}
		resolved[idx] = norm
	}

	if len(unsupported) > 0 {
		return nil, tmplUnsupported("模板包含不支持的外部关联或跨表查找依赖", unsupported)
	}

	// Pass C: formula fields.
	for idx, spec := range prepared {
		if spec.kind != "formula" {
			continue
		}
		source, err := parseFormulaInput(spec.config)
		if err != nil {
			return nil, tmplInvalid("invalid_value", err.Error())
		}
		expr, err := CompileFormula(source, func(token string) (string, bool) {
			for i, cand := range prepared {
				if cand.name == token {
					return newFieldIDs[i], true
				}
			}
			if mapped, ok := fieldIDMap[canonicalRefKey(token)]; ok {
				return mapped, true
			}
			return "", false
		})
		if err != nil {
			return nil, tmplInvalid("invalid_value", err.Error())
		}
		rendered := expr.Source(func(id string) (string, bool) {
			n, ok := nameByNewID[id]
			return n, ok
		})
		stored, err := storedFormulaConfig(expr, rendered)
		if err != nil {
			return nil, tmplFromStore(err)
		}
		resolved[idx] = stored
	}

	// Cycle detection across derived fields in the new table.
	graph := make(map[string][]string, len(prepared))
	for idx, spec := range prepared {
		graph[newFieldIDs[idx]] = fieldDependencies(spec.kind, resolved[idx])
	}
	for idx, spec := range prepared {
		if (spec.kind == "lookup" || spec.kind == "formula") && hasCycleFrom(graph, newFieldIDs[idx]) {
			return nil, tmplInvalid("invalid_value", "字段存在循环引用")
		}
	}

	return resolved, nil
}

func rebindRelationConfig(
	newTableID string,
	sourceTableID *string,
	fieldID, fieldName string,
	raw any,
) (any, *UnsupportedDependencyDto, string) {
	obj, ok := raw.(map[string]any)
	if !ok {
		return nil, nil, "关联字段需要指定目标表"
	}
	for k := range obj {
		if k != "target_table_id" && k != "bidirectional" && k != "reverse_field_id" {
			return nil, nil, "关联字段配置包含不支持的参数"
		}
	}
	rawTargetStr, ok := obj["target_table_id"].(string)
	if !ok || strings.TrimSpace(rawTargetStr) == "" {
		return nil, nil, "关联字段需要有效的目标表标识"
	}
	rawTarget := strings.TrimSpace(rawTargetStr)
	bidirectional := false
	if rawBi, exists := obj["bidirectional"]; exists && rawBi != nil {
		b, ok := rawBi.(bool)
		if !ok {
			return nil, nil, "双向关联开关需要是布尔值"
		}
		bidirectional = b
	}
	hasReverse := false
	if rawRev, exists := obj["reverse_field_id"]; exists && rawRev != nil {
		hasReverse = true
	}

	targetLower := strings.ToLower(rawTarget)
	if bidirectional || hasReverse {
		return nil, &UnsupportedDependencyDto{
			FieldID:       fieldID,
			FieldName:     fieldName,
			Kind:          "relation",
			Code:          "bidirectional_relation_unsupported",
			TargetTableID: &targetLower,
		}, ""
	}
	isSelf := strings.EqualFold(rawTarget, "self") || (sourceTableID != nil && strings.EqualFold(rawTarget, *sourceTableID))
	if !isSelf {
		return nil, &UnsupportedDependencyDto{
			FieldID:       fieldID,
			FieldName:     fieldName,
			Kind:          "relation",
			Code:          "external_relation_unsupported",
			TargetTableID: &targetLower,
		}, ""
	}

	rebound := relationConfig{
		TargetTableID: strings.ToLower(newTableID),
		Bidirectional: false,
	}.toJSON()
	norm, err := NormalizeConfig("relation", rebound)
	if err != nil {
		return nil, nil, err.Error()
	}
	if err := validateTargetTable(newTableID, newTableID, false); err != nil {
		return nil, nil, "关联字段配置无效"
	}
	return norm, nil, ""
}

func parseTemplateLookupKeys(config any) (string, string, *TemplateError) {
	obj, ok := config.(map[string]any)
	if !ok {
		return "", "", tmplInvalid("invalid_value", "查找字段需要指定关联字段和目标字段")
	}
	for k := range obj {
		if k != "relation_field_id" && k != "target_field_id" {
			return "", "", tmplInvalid("invalid_value", "查找字段配置包含不支持的参数")
		}
	}
	rawRel, ok := obj["relation_field_id"].(string)
	if !ok || strings.TrimSpace(rawRel) == "" {
		return "", "", tmplInvalid("invalid_value", "查找字段需要有效的关联字段标识")
	}
	rawTarget, ok := obj["target_field_id"].(string)
	if !ok || strings.TrimSpace(rawTarget) == "" {
		return "", "", tmplInvalid("invalid_value", "查找字段需要有效的目标字段标识")
	}
	return strings.TrimSpace(rawRel), strings.TrimSpace(rawTarget), nil
}

func rebindFieldRef(fieldIDMap map[string]string, raw, ctxLabel string) (string, *TemplateError) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", tmplInvalid("unknown_field_reference", fmt.Sprintf("%s 引用了空的字段标识", ctxLabel))
	}
	mapped, ok := fieldIDMap[canonicalRefKey(trimmed)]
	if !ok {
		return "", tmplInvalid("unknown_field_reference", fmt.Sprintf("%s 引用了未知字段：%s", ctxLabel, trimmed))
	}
	return mapped, nil
}

func rebindAndValidateView(
	newTableID string,
	view TemplateViewInput,
	fieldIDMap map[string]string,
	defs []FieldDef,
) (StoredView, *TemplateError) {
	kind := "grid"
	if view.Kind != nil {
		kind = *view.Kind
	}
	var columns []string
	if view.Columns != nil {
		columns = make([]string, 0, len(view.Columns))
		for _, id := range view.Columns {
			mapped, terr := rebindFieldRef(fieldIDMap, id, "视图可见列")
			if terr != nil {
				return StoredView{}, terr
			}
			columns = append(columns, mapped)
		}
	}
	var filter *StoredFilter
	if view.Filter != nil {
		mapped, terr := rebindFieldRef(fieldIDMap, view.Filter.FieldID, "视图筛选")
		if terr != nil {
			return StoredView{}, terr
		}
		filter = &StoredFilter{
			FieldID: mapped,
			Op:      view.Filter.Op,
			Value:   cloneJSON(view.Filter.Value),
		}
	}
	var sortSpec *StoredSort
	if view.Sort != nil {
		mapped, terr := rebindFieldRef(fieldIDMap, view.Sort.FieldID, "视图排序")
		if terr != nil {
			return StoredView{}, terr
		}
		sortSpec = &StoredSort{
			FieldID:   mapped,
			Direction: view.Sort.Direction,
		}
	}
	var group *string
	if view.Group != nil {
		mapped, terr := rebindFieldRef(fieldIDMap, *view.Group, "视图分组")
		if terr != nil {
			return StoredView{}, terr
		}
		group = &mapped
	}
	cfg, terr := rebindViewConfig(view.Config, fieldIDMap)
	if terr != nil {
		return StoredView{}, terr
	}
	if terr := checkPortableViewFilter(filter, func(id string) (string, bool) {
		for _, d := range defs {
			if d.ID == id {
				return d.Kind, true
			}
		}
		return "", false
	}); terr != nil {
		return StoredView{}, terr
	}
	stored, err := NormalizeView(newTableID, view.Name, kind, cfg, columns, filter, sortSpec, group, defs)
	if err != nil {
		return StoredView{}, tmplInvalid("invalid_value", err.Error())
	}
	return stored, nil
}

func checkPortableViewFilter(filter *StoredFilter, fieldKind func(id string) (string, bool)) *TemplateError {
	if filter != nil && filter.Op == "eq" {
		if k, ok := fieldKind(filter.FieldID); ok && (k == "relation" || k == "attachment") {
			return tmplInvalid("unportable_view_filter", "模板视图不能按附件或关联记录的标识筛选")
		}
	}
	return nil
}

func rebindViewConfig(raw any, fieldIDMap map[string]string) (any, *TemplateError) {
	if raw == nil {
		return map[string]any{}, nil
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return nil, tmplInvalid("invalid_value", "视图配置必须是对象")
	}
	rebound := make(map[string]any, len(obj))
	for key, val := range obj {
		if slices.Contains(viewConfigSingleFieldKeys, key) {
			if val == nil {
				rebound[key] = nil
				continue
			}
			rawID, ok := val.(string)
			if !ok {
				return nil, tmplInvalid("invalid_value", "视图配置中的字段标识无效")
			}
			mapped, terr := rebindFieldRef(fieldIDMap, rawID, "视图配置 "+key)
			if terr != nil {
				return nil, terr
			}
			rebound[key] = mapped
		} else if key == "required_field_ids" {
			if val == nil {
				rebound[key] = nil
				continue
			}
			items, ok := val.([]any)
			if !ok {
				return nil, tmplInvalid("invalid_value", "必填字段列表格式不正确")
			}
			mappedItems := make([]any, 0, len(items))
			for _, item := range items {
				rawID, ok := item.(string)
				if !ok {
					return nil, tmplInvalid("invalid_value", "必填字段标识无效")
				}
				mapped, terr := rebindFieldRef(fieldIDMap, rawID, "视图配置 required_field_ids")
				if terr != nil {
					return nil, terr
				}
				mappedItems = append(mappedItems, mapped)
			}
			rebound[key] = mappedItems
		} else {
			rebound[key] = cloneJSON(val)
		}
	}
	return rebound, nil
}

func rebindSampleRecordValues(
	rawValues map[string]any,
	fieldIDMap map[string]string,
	fieldsByNewID map[string]FieldDto,
) (map[string]any, *TemplateError) {
	rebound := make(map[string]any, len(rawValues))
	for rawKey, val := range rawValues {
		newID, terr := rebindFieldRef(fieldIDMap, rawKey, "示例记录字段")
		if terr != nil {
			return nil, terr
		}
		if field, ok := fieldsByNewID[newID]; ok && val != nil && (field.Kind == "attachment" || field.Kind == "relation") {
			code := "sample_relation_unsupported"
			if field.Kind == "attachment" {
				code = "sample_attachment_unsupported"
			}
			return nil, tmplUnsupported("便携模板示例记录不支持包含附件或关联记录引用", []UnsupportedDependencyDto{{
				FieldID:   rawKey,
				FieldName: field.Name,
				Kind:      field.Kind,
				Code:      code,
			}})
		}
		rebound[newID] = val
	}
	return rebound, nil
}
