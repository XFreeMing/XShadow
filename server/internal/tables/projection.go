package tables

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

const (
	maxEvalDepth = 32
	maxTableHops = 16
)

type lookupConfig struct {
	RelationFieldID string
	TargetFieldID   string
}

func parseLookupConfigInput(config any) (lookupConfig, error) {
	obj, ok := config.(map[string]any)
	if !ok {
		return lookupConfig{}, errInvalid("查找字段需要指定关联字段和目标字段")
	}
	for k := range obj {
		if k != "relation_field_id" && k != "target_field_id" {
			return lookupConfig{}, errInvalid("查找字段配置包含不支持的参数")
		}
	}
	rawRel, ok := obj["relation_field_id"].(string)
	if !ok || !IsUUID(strings.TrimSpace(rawRel)) {
		return lookupConfig{}, errInvalid("查找字段需要有效的关联字段标识")
	}
	rawTarget, ok := obj["target_field_id"].(string)
	if !ok || !IsUUID(strings.TrimSpace(rawTarget)) {
		return lookupConfig{}, errInvalid("查找字段需要有效的目标字段标识")
	}
	return lookupConfig{
		RelationFieldID: strings.ToLower(strings.TrimSpace(rawRel)),
		TargetFieldID:   strings.ToLower(strings.TrimSpace(rawTarget)),
	}, nil
}

func lookupConfigFromStored(config any) (lookupConfig, error) {
	obj, ok := config.(map[string]any)
	if !ok {
		return lookupConfig{}, FormulaError("查找字段配置无效")
	}
	rawRel, ok := obj["relation_field_id"].(string)
	if !ok || !IsUUID(rawRel) {
		return lookupConfig{}, FormulaError("查找字段配置无效")
	}
	rawTarget, ok := obj["target_field_id"].(string)
	if !ok || !IsUUID(rawTarget) {
		return lookupConfig{}, FormulaError("查找字段配置无效")
	}
	return lookupConfig{
		RelationFieldID: strings.ToLower(rawRel),
		TargetFieldID:   strings.ToLower(rawTarget),
	}, nil
}

func (c lookupConfig) toJSON() map[string]any {
	return map[string]any{
		"relation_field_id": c.RelationFieldID,
		"target_field_id":   c.TargetFieldID,
	}
}

func parseFormulaInput(config any) (string, error) {
	obj, ok := config.(map[string]any)
	if !ok {
		return "", errInvalid("公式字段需要表达式")
	}
	for k := range obj {
		if k != "expression" {
			return "", errInvalid("公式字段配置包含不支持的参数")
		}
	}
	raw, ok := obj["expression"].(string)
	if !ok || strings.TrimSpace(raw) == "" {
		return "", errInvalid("公式表达式不能为空")
	}
	return strings.TrimSpace(raw), nil
}

func parseStoredAST(config any) (FormulaExpr, error) {
	obj, ok := config.(map[string]any)
	if !ok {
		return FormulaExpr{}, FormulaError("公式配置缺少语法树")
	}
	astVal, exists := obj["ast"]
	if !exists || astVal == nil {
		return FormulaExpr{}, FormulaError("公式配置缺少语法树")
	}
	raw, err := marshalCompact(astVal)
	if err != nil {
		return FormulaExpr{}, FormulaError("公式语法树无效")
	}
	var expr FormulaExpr
	if err := json.Unmarshal(raw, &expr); err != nil {
		return FormulaExpr{}, FormulaError("公式语法树无效")
	}
	return expr, nil
}

func storedFormulaConfig(expr FormulaExpr, rendered string) (map[string]any, error) {
	rawAST, err := marshalCompact(expr)
	if err != nil {
		return nil, errFailed(err)
	}
	var astObj any
	if err := json.Unmarshal(rawAST, &astObj); err != nil {
		return nil, errFailed(err)
	}
	var refs []string
	expr.Refs(&refs)
	sort.Strings(refs)
	refs = slices.Compact(refs)
	if refs == nil {
		refs = []string{}
	}
	return map[string]any{
		"expression": rendered,
		"ast":        astObj,
		"refs":       refs,
	}, nil
}

func hydrateFormulaFields(fields []FieldDto) {
	names := make(map[string]string, len(fields))
	for _, f := range fields {
		names[f.ID] = f.Name
	}
	for i := range fields {
		if fields[i].Kind == "formula" {
			hydrateSingleFormula(&fields[i], names)
		}
	}
}

func hydrateSingleFormula(field *FieldDto, names map[string]string) {
	if field.Kind != "formula" {
		return
	}
	var expression string
	if expr, err := parseStoredAST(field.Config); err == nil {
		expression = expr.Source(func(id string) (string, bool) {
			name, ok := names[id]
			return name, ok
		})
	} else if m, ok := field.Config.(map[string]any); ok {
		expression, _ = m["expression"].(string)
	}
	field.Config = map[string]any{"expression": expression}
}

type workspaceField struct {
	id      string
	tableID string
	name    string
	kind    string
	config  any
}

func loadWorkspaceFields(ctx context.Context, tx pgx.Tx, workspace string) ([]workspaceField, error) {
	rows, err := tx.Query(ctx,
		`SELECT id::text, table_id::text, name, kind, config::text
		 FROM generic_field
		 WHERE workspace_id = $1::uuid
		 ORDER BY table_id, position, id`,
		workspace,
	)
	if err != nil {
		return nil, errFailed(err)
	}
	defer rows.Close()
	var fields []workspaceField
	for rows.Next() {
		var f workspaceField
		var raw string
		if err := rows.Scan(&f.id, &f.tableID, &f.name, &f.kind, &raw); err != nil {
			return nil, errFailed(err)
		}
		if err := json.Unmarshal([]byte(raw), &f.config); err != nil {
			return nil, errFailed(err)
		}
		fields = append(fields, f)
	}
	if err := rows.Err(); err != nil {
		return nil, errFailed(err)
	}
	return fields, nil
}

func compileDerivedConfig(workspaceFields []workspaceField, tableID, fieldID, kind string, rawConfig any) (any, error) {
	tableLower := strings.ToLower(tableID)
	var stored any
	switch kind {
	case "lookup":
		lookup, err := parseLookupConfigInput(rawConfig)
		if err != nil {
			return nil, err
		}
		var relField *workspaceField
		for i := range workspaceFields {
			f := &workspaceFields[i]
			if f.tableID == tableLower && f.id == lookup.RelationFieldID && f.kind == "relation" {
				relField = f
				break
			}
		}
		if relField == nil {
			return nil, errInvalid("查找字段引用的关联字段不存在")
		}
		rel, err := relationConfigFromStored(relField.config)
		if err != nil {
			return nil, err
		}
		targetExists := false
		for i := range workspaceFields {
			f := &workspaceFields[i]
			if f.tableID == rel.TargetTableID && f.id == lookup.TargetFieldID {
				targetExists = true
				break
			}
		}
		if !targetExists {
			return nil, errInvalid("查找的目标字段不存在或不属于目标表")
		}
		stored = lookup.toJSON()
	case "formula":
		source, err := parseFormulaInput(rawConfig)
		if err != nil {
			return nil, err
		}
		var tableFields []workspaceField
		names := make(map[string]string)
		for _, f := range workspaceFields {
			if f.tableID == tableLower {
				tableFields = append(tableFields, f)
				names[f.id] = f.name
			}
		}
		expr, err := CompileFormula(source, func(name string) (string, bool) {
			for _, f := range tableFields {
				if f.name == name {
					return f.id, true
				}
			}
			for _, f := range tableFields {
				if strings.EqualFold(f.id, name) {
					return f.id, true
				}
			}
			return "", false
		})
		if err != nil {
			return nil, errInvalid(err.Error())
		}
		rendered := expr.Source(func(id string) (string, bool) {
			n, ok := names[id]
			return n, ok
		})
		stored, err = storedFormulaConfig(expr, rendered)
		if err != nil {
			return nil, err
		}
	default:
		return nil, errInvalid("不支持的字段类型")
	}

	graph := buildDependencyGraph(workspaceFields)
	fieldLower := strings.ToLower(fieldID)
	graph[fieldLower] = fieldDependencies(kind, stored)
	if hasCycleFrom(graph, fieldLower) {
		return nil, errInvalid("字段存在循环引用")
	}
	return stored, nil
}

func buildDependencyGraph(fields []workspaceField) map[string][]string {
	graph := make(map[string][]string, len(fields))
	for _, f := range fields {
		graph[strings.ToLower(f.id)] = fieldDependencies(f.kind, f.config)
	}
	return graph
}

func fieldDependencies(kind string, config any) []string {
	switch kind {
	case "lookup":
		if cfg, err := lookupConfigFromStored(config); err == nil {
			return []string{cfg.RelationFieldID, cfg.TargetFieldID}
		}
		return nil
	case "formula":
		if expr, err := parseStoredAST(config); err == nil {
			var refs []string
			expr.Refs(&refs)
			out := make([]string, len(refs))
			for i, r := range refs {
				out[i] = strings.ToLower(r)
			}
			return out
		}
		return nil
	default:
		return nil
	}
}

func hasCycleFrom(graph map[string][]string, start string) bool {
	visiting := make(map[string]struct{})
	visited := make(map[string]struct{})
	var dfs func(node string) bool
	dfs = func(node string) bool {
		if _, inStack := visiting[node]; inStack {
			return true
		}
		visiting[node] = struct{}{}
		for _, dep := range graph[node] {
			if _, inStack := visiting[dep]; inStack {
				return true
			}
			if _, done := visited[dep]; !done && dfs(dep) {
				return true
			}
		}
		delete(visiting, node)
		visited[node] = struct{}{}
		return false
	}
	return dfs(start)
}

func createDerivedField(ctx context.Context, tx pgx.Tx, workspace, table, name, kind string, rawConfig any) (FieldDto, error) {
	if err := lockWorkspaceTablesSorted(ctx, tx, workspace, table); err != nil {
		return FieldDto{}, err
	}
	wsFields, err := loadWorkspaceFields(ctx, tx, workspace)
	if err != nil {
		return FieldDto{}, err
	}
	var fieldID string
	if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&fieldID); err != nil {
		return FieldDto{}, errFailed(err)
	}
	stored, err := compileDerivedConfig(wsFields, table, fieldID, kind, rawConfig)
	if err != nil {
		return FieldDto{}, err
	}
	pos, err := nextFieldPosition(ctx, tx, workspace, table)
	if err != nil {
		return FieldDto{}, err
	}
	storedJSON, err := marshalCompactString(stored)
	if err != nil {
		return FieldDto{}, errFailed(err)
	}
	created := tsExpr("created_at")
	updated := tsExpr("updated_at")
	field, err := scanFieldRow(
		tx.QueryRow(ctx, fmt.Sprintf(
			`INSERT INTO generic_field (id, workspace_id, table_id, name, kind, config, position)
			 VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, $6::jsonb, $7)
			 RETURNING id::text, name, kind, config::text, position, %s, %s`, created, updated),
			fieldID, workspace, table, name, kind, storedJSON, pos,
		),
		"请重试添加字段",
	)
	if err != nil {
		return FieldDto{}, err
	}
	if field.Kind == "formula" {
		names := make(map[string]string)
		for _, item := range wsFields {
			if strings.EqualFold(item.tableID, table) {
				names[item.id] = item.name
			}
		}
		hydrateSingleFormula(&field, names)
	}
	return field, nil
}

func updateDerivedFieldConfig(ctx context.Context, tx pgx.Tx, workspace, table, fieldID string, name *string, rawConfig any) (FieldDto, error) {
	if err := lockWorkspaceTablesSorted(ctx, tx, workspace, table); err != nil {
		return FieldDto{}, err
	}
	wsFields, err := loadWorkspaceFields(ctx, tx, workspace)
	if err != nil {
		return FieldDto{}, err
	}
	tableLower := strings.ToLower(table)
	fieldLower := strings.ToLower(fieldID)
	var existing *workspaceField
	for i := range wsFields {
		if wsFields[i].tableID == tableLower && wsFields[i].id == fieldLower {
			existing = &wsFields[i]
			break
		}
	}
	if existing == nil {
		return FieldDto{}, errNotFound("找不到这个字段")
	}
	if existing.kind != "lookup" && existing.kind != "formula" {
		return FieldDto{}, errInvalid("不支持修改该字段类型的配置")
	}
	stored, err := compileDerivedConfig(wsFields, table, existing.id, existing.kind, rawConfig)
	if err != nil {
		return FieldDto{}, err
	}
	storedJSON, err := marshalCompactString(stored)
	if err != nil {
		return FieldDto{}, errFailed(err)
	}
	created := tsExpr("created_at")
	updated := tsExpr("updated_at")
	field, err := scanFieldRowOptional(
		tx.QueryRow(ctx, fmt.Sprintf(
			`UPDATE generic_field
			 SET name = COALESCE($4, name), config = $5::jsonb, updated_at = now()
			 WHERE id = $1::uuid AND table_id = $2::uuid AND workspace_id = $3::uuid
			 RETURNING id::text, name, kind, config::text, position, %s, %s`, created, updated),
			fieldID, table, workspace, name, storedJSON,
		),
		"找不到这个字段",
		"请重试更新字段",
	)
	if err != nil {
		return FieldDto{}, err
	}
	if field.Kind == "formula" {
		names := make(map[string]string)
		for _, item := range wsFields {
			if item.tableID == tableLower {
				names[item.id] = item.name
			}
		}
		names[field.ID] = field.Name
		hydrateSingleFormula(&field, names)
	}
	return field, nil
}

type parsedFormula struct {
	expr FormulaExpr
	err  error
}

type parsedLookup struct {
	cfg lookupConfig
	err error
}

type tableSnapshot struct {
	fields   map[string]FieldDto
	formulas map[string]parsedFormula
	lookups  map[string]parsedLookup
	records  map[string]map[string]any
}

func newTableSnapshot(fields []FieldDto, records []RecordDto) *tableSnapshot {
	snap := &tableSnapshot{
		fields:   make(map[string]FieldDto, len(fields)),
		formulas: make(map[string]parsedFormula),
		lookups:  make(map[string]parsedLookup),
		records:  make(map[string]map[string]any, len(records)),
	}
	for _, f := range fields {
		if f.Kind == "formula" {
			expr, err := parseStoredAST(f.Config)
			snap.formulas[f.ID] = parsedFormula{expr: expr, err: err}
		} else if f.Kind == "lookup" {
			cfg, err := lookupConfigFromStored(f.Config)
			snap.lookups[f.ID] = parsedLookup{cfg: cfg, err: err}
		}
		snap.fields[f.ID] = f
	}
	for _, rec := range records {
		vals := make(map[string]any, len(rec.Values))
		for k, v := range rec.Values {
			vals[k] = v
		}
		snap.records[rec.ID] = vals
	}
	return snap
}

type tableFieldRef struct {
	tableID string
	fieldID string
}

func (s *tableSnapshot) referencedTargets(onlyFields map[string]struct{}) []tableFieldRef {
	reachable := make(map[string]struct{})
	var queue []string
	if onlyFields != nil {
		for id := range onlyFields {
			if _, ok := reachable[id]; !ok {
				reachable[id] = struct{}{}
				queue = append(queue, id)
			}
		}
	} else {
		for _, f := range s.fields {
			if (f.Kind == "lookup" || f.Kind == "formula") {
				if _, ok := reachable[f.ID]; !ok {
					reachable[f.ID] = struct{}{}
					queue = append(queue, f.ID)
				}
			}
		}
	}
	var targets []tableFieldRef
	for len(queue) > 0 {
		fieldID := queue[0]
		queue = queue[1:]
		f, ok := s.fields[fieldID]
		if !ok {
			continue
		}
		switch f.Kind {
		case "formula":
			if pf, ok := s.formulas[fieldID]; ok && pf.err == nil {
				var refs []string
				pf.expr.Refs(&refs)
				for _, dep := range refs {
					if _, seen := reachable[dep]; !seen {
						reachable[dep] = struct{}{}
						queue = append(queue, dep)
					}
				}
			}
		case "lookup":
			if pl, ok := s.lookups[fieldID]; ok && pl.err == nil {
				if relField, ok := s.fields[pl.cfg.RelationFieldID]; ok && relField.Kind == "relation" {
					if rel, err := relationConfigFromStored(relField.Config); err == nil {
						targets = append(targets, tableFieldRef{tableID: rel.TargetTableID, fieldID: pl.cfg.TargetFieldID})
					}
				}
			}
		}
	}
	return targets
}

type evalCellKey struct {
	tableID  string
	recordID string
	fieldID  string
}

type evalResult struct {
	value any
	err   error
}

type sheetEvaluator struct {
	tables   map[string]*tableSnapshot
	cache    map[evalCellKey]evalResult
	visiting map[evalCellKey]struct{}
}

func newSheetEvaluator(tables map[string]*tableSnapshot) *sheetEvaluator {
	return &sheetEvaluator{
		tables:   tables,
		cache:    make(map[evalCellKey]evalResult),
		visiting: make(map[evalCellKey]struct{}),
	}
}

func (ev *sheetEvaluator) evalCell(tableID, recordID, fieldID string, depth int) (any, error) {
	if depth > maxEvalDepth {
		return nil, FormulaError("计算嵌套过深")
	}
	key := evalCellKey{tableID: tableID, recordID: recordID, fieldID: fieldID}
	if cached, ok := ev.cache[key]; ok {
		return cloneJSON(cached.value), cached.err
	}
	if _, inStack := ev.visiting[key]; inStack {
		return nil, FormulaError("检测到循环引用")
	}
	snap, ok := ev.tables[tableID]
	if !ok {
		return nil, nil
	}
	field, ok := snap.fields[fieldID]
	if !ok {
		return nil, nil
	}
	if field.Kind != "lookup" && field.Kind != "formula" {
		if vals, ok := snap.records[recordID]; ok {
			if v, exists := vals[fieldID]; exists {
				return cloneJSON(v), nil
			}
		}
		return nil, nil
	}
	ev.visiting[key] = struct{}{}
	var val any
	var err error
	if field.Kind == "lookup" {
		val, err = ev.evalLookup(tableID, recordID, fieldID, depth)
	} else {
		val, err = ev.evalFormula(tableID, recordID, fieldID, depth)
	}
	delete(ev.visiting, key)
	ev.cache[key] = evalResult{value: cloneJSON(val), err: err}
	return val, err
}

func (ev *sheetEvaluator) evalLookup(tableID, recordID, fieldID string, depth int) (any, error) {
	snap, ok := ev.tables[tableID]
	if !ok {
		return nil, FormulaError("查找表不存在")
	}
	pl, ok := snap.lookups[fieldID]
	if !ok {
		return nil, FormulaError("查找字段配置无效")
	}
	if pl.err != nil {
		return nil, pl.err
	}
	relField, ok := snap.fields[pl.cfg.RelationFieldID]
	if !ok || relField.Kind != "relation" {
		return nil, FormulaError("查找字段引用的关联字段不存在")
	}
	rel, err := relationConfigFromStored(relField.Config)
	if err != nil {
		return nil, FormulaError("关联字段配置无效")
	}
	var linkedIDs []string
	if recVals, ok := snap.records[recordID]; ok {
		linkedIDs = extractRelationIDs(recVals[pl.cfg.RelationFieldID])
	}
	values := make([]any, 0, len(linkedIDs))
	targetSnap := ev.tables[rel.TargetTableID]
	for _, targetRecID := range linkedIDs {
		if targetSnap == nil {
			continue
		}
		if _, exists := targetSnap.records[targetRecID]; !exists {
			continue
		}
		v, err := ev.evalCell(rel.TargetTableID, targetRecID, pl.cfg.TargetFieldID, depth+1)
		if err != nil {
			return nil, err
		}
		values = append(values, v)
	}
	return values, nil
}

func (ev *sheetEvaluator) evalFormula(tableID, recordID, fieldID string, depth int) (any, error) {
	snap, ok := ev.tables[tableID]
	if !ok {
		return nil, FormulaError("公式表不存在")
	}
	pf, ok := snap.formulas[fieldID]
	if !ok {
		return nil, FormulaError("公式配置无效")
	}
	if pf.err != nil {
		return nil, pf.err
	}
	return pf.expr.Evaluate(func(refFieldID string) (any, error) {
		return ev.evalCell(tableID, recordID, refFieldID, depth+1)
	})
}

type primaryRowsMode int

const (
	primaryRowsAll primaryRowsMode = iota
	primaryRowsSubset
)

func projectSheet(
	ctx context.Context,
	tx pgx.Tx,
	workspace string,
	tableID string,
	fields []FieldDto,
	records []RecordDto,
	mode primaryRowsMode,
) error {
	hasDerived := false
	for _, f := range fields {
		if f.Kind == "lookup" || f.Kind == "formula" {
			hasDerived = true
			break
		}
	}
	if !hasDerived {
		return nil
	}

	primarySnap := newTableSnapshot(fields, records)
	initialTargets := primarySnap.referencedTargets(nil)

	tablesMap := map[string]*tableSnapshot{tableID: primarySnap}
	primaryComplete := mode == primaryRowsAll

	type hopItem struct {
		tableID string
		fieldID string
		hop     int
	}
	pending := make([]hopItem, 0, len(initialTargets))
	for _, t := range initialTargets {
		pending = append(pending, hopItem{tableID: t.tableID, fieldID: t.fieldID, hop: 1})
	}
	visited := make(map[tableFieldRef]struct{})

	for len(pending) > 0 {
		cur := pending[0]
		pending = pending[1:]
		ref := tableFieldRef{tableID: cur.tableID, fieldID: cur.fieldID}
		if cur.hop > maxTableHops {
			continue
		}
		if _, seen := visited[ref]; seen {
			continue
		}
		visited[ref] = struct{}{}

		if _, exists := tablesMap[cur.tableID]; !exists || (cur.tableID == tableID && !primaryComplete) {
			targetFields, err := loadFields(ctx, tx, workspace, cur.tableID)
			if err != nil {
				return err
			}
			targetRecords, err := loadRecords(ctx, tx, workspace, cur.tableID, targetFields)
			if err != nil {
				return err
			}
			tablesMap[cur.tableID] = newTableSnapshot(targetFields, targetRecords)
			if cur.tableID == tableID {
				primaryComplete = true
			}
		}
		if snap := tablesMap[cur.tableID]; snap != nil {
			seed := map[string]struct{}{cur.fieldID: {}}
			for _, next := range snap.referencedTargets(seed) {
				pending = append(pending, hopItem{tableID: next.tableID, fieldID: next.fieldID, hop: cur.hop + 1})
			}
		}
	}

	derivedIDs := make([]string, 0)
	for _, f := range fields {
		if f.Kind == "lookup" || f.Kind == "formula" {
			derivedIDs = append(derivedIDs, f.ID)
		}
	}

	ev := newSheetEvaluator(tablesMap)
	for i := range records {
		if records[i].Values == nil {
			records[i].Values = make(map[string]any)
		}
		for _, fieldID := range derivedIDs {
			val, err := ev.evalCell(tableID, records[i].ID, fieldID, 0)
			if err != nil {
				records[i].Values[fieldID] = nil
				if records[i].Errors == nil {
					records[i].Errors = make(map[string]string)
				}
				records[i].Errors[fieldID] = err.Error()
			} else {
				records[i].Values[fieldID] = val
			}
		}
	}

	hydrateLookupFields(ev.tables, tableID, fields)
	hydrateFormulaFields(fields)
	return nil
}

func hydrateLookupFields(tablesMap map[string]*tableSnapshot, tableID string, fields []FieldDto) {
	for i := range fields {
		if fields[i].Kind != "lookup" {
			continue
		}
		target := resolveLookupTargetField(tablesMap, tableID, fields[i].ID)
		if target == nil {
			continue
		}
		cfgMap, ok := fields[i].Config.(map[string]any)
		if !ok {
			continue
		}
		if target.Kind == "single_select" {
			if targetCfg, ok := target.Config.(map[string]any); ok {
				if opts, exists := targetCfg["options"]; exists {
					cfgMap["target_options"] = cloneJSON(opts)
				}
			}
		} else if target.Kind == "relation" {
			if rel, err := relationConfigFromStored(target.Config); err == nil {
				cfgMap["target_relation_table_id"] = rel.TargetTableID
			}
		}
	}
}

func resolveLookupTargetField(tablesMap map[string]*tableSnapshot, startTableID, startFieldID string) *FieldDto {
	curTable := startTableID
	curField := startFieldID
	seen := make(map[tableFieldRef]struct{})
	for range maxTableHops {
		ref := tableFieldRef{tableID: curTable, fieldID: curField}
		if _, dup := seen[ref]; dup {
			return nil
		}
		seen[ref] = struct{}{}
		snap := tablesMap[curTable]
		if snap == nil {
			return nil
		}
		pl, ok := snap.lookups[curField]
		if !ok || pl.err != nil {
			return nil
		}
		relField, ok := snap.fields[pl.cfg.RelationFieldID]
		if !ok || relField.Kind != "relation" {
			return nil
		}
		rel, err := relationConfigFromStored(relField.Config)
		if err != nil {
			return nil
		}
		targetSnap := tablesMap[rel.TargetTableID]
		if targetSnap == nil {
			return nil
		}
		targetField, ok := targetSnap.fields[pl.cfg.TargetFieldID]
		if !ok {
			return nil
		}
		if targetField.Kind != "lookup" {
			cp := targetField
			return &cp
		}
		curTable = rel.TargetTableID
		curField = pl.cfg.TargetFieldID
	}
	return nil
}
