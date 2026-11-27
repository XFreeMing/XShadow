package tables

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

const MaxIssueProjectionBatch = 200

type issueLookupConfig struct {
	RelationPropertyID string
	TargetFieldID      string
}

func parseIssueLookupConfigInput(config any) (issueLookupConfig, error) {
	obj, ok := config.(map[string]any)
	if !ok {
		return issueLookupConfig{}, errInvalid("查找属性需要关联字段与目标字段")
	}
	for k := range obj {
		if k != "relation_property_id" && k != "target_field_id" {
			return issueLookupConfig{}, errInvalid("查找属性配置包含不支持的参数")
		}
	}
	rawRel, ok := obj["relation_property_id"].(string)
	if !ok {
		return issueLookupConfig{}, errInvalid("查找属性需要有效的 record_relation 属性")
	}
	relTrim := strings.TrimSpace(rawRel)
	if !IsUUID(relTrim) {
		return issueLookupConfig{}, errInvalid("查找属性需要有效的 record_relation 属性")
	}
	rawTarget, ok := obj["target_field_id"].(string)
	if !ok {
		return issueLookupConfig{}, errInvalid("查找属性需要有效的目标字段")
	}
	targetTrim := strings.TrimSpace(rawTarget)
	if !IsUUID(targetTrim) {
		return issueLookupConfig{}, errInvalid("查找属性需要有效的目标字段")
	}
	return issueLookupConfig{
		RelationPropertyID: strings.ToLower(relTrim),
		TargetFieldID:      strings.ToLower(targetTrim),
	}, nil
}

func issueLookupConfigFromStored(config any) (issueLookupConfig, error) {
	obj, ok := config.(map[string]any)
	if !ok {
		return issueLookupConfig{}, errInvalid("查找属性配置无效")
	}
	rawRel, ok := obj["relation_property_id"].(string)
	if !ok || !IsUUID(rawRel) {
		return issueLookupConfig{}, errInvalid("查找属性配置无效")
	}
	rawTarget, ok := obj["target_field_id"].(string)
	if !ok || !IsUUID(rawTarget) {
		return issueLookupConfig{}, errInvalid("查找属性配置无效")
	}
	return issueLookupConfig{
		RelationPropertyID: strings.ToLower(rawRel),
		TargetFieldID:      strings.ToLower(rawTarget),
	}, nil
}

func (c issueLookupConfig) toJSON() map[string]any {
	return map[string]any{
		"relation_property_id": c.RelationPropertyID,
		"target_field_id":      c.TargetFieldID,
	}
}

func parseIssueFormulaInput(config any) (string, error) {
	obj, ok := config.(map[string]any)
	if !ok {
		return "", errInvalid("公式属性需要表达式")
	}
	for k := range obj {
		if k != "expression" {
			return "", errInvalid("公式属性配置包含不支持的参数")
		}
	}
	expr, ok := obj["expression"].(string)
	if !ok {
		return "", errInvalid("公式表达式不能为空")
	}
	trimmed := strings.TrimSpace(expr)
	if trimmed == "" {
		return "", errInvalid("公式表达式不能为空")
	}
	return trimmed, nil
}

type issuePropertyDef struct {
	id     string
	name   string
	kind   string
	config any
}

func compileIssueDerivedConfig(defs []issuePropertyDef, propertyID, kind string, rawConfig any) (any, error) {
	idLower := strings.ToLower(propertyID)
	var stored any
	switch kind {
	case "lookup":
		lookup, err := parseIssueLookupConfigInput(rawConfig)
		if err != nil {
			return nil, err
		}
		var relDef *issuePropertyDef
		for i := range defs {
			if defs[i].id == lookup.RelationPropertyID && defs[i].kind == "record_relation" {
				relDef = &defs[i]
				break
			}
		}
		if relDef == nil {
			return nil, errInvalid("查找引用的 record_relation 不存在")
		}
		cfgMap, _ := relDef.config.(map[string]any)
		targetTable, _ := cfgMap["target_table_id"].(string)
		if !IsUUID(targetTable) {
			return nil, errInvalid("record_relation 缺少目标表")
		}
		stored = lookup.toJSON()
	case "formula":
		source, err := parseIssueFormulaInput(rawConfig)
		if err != nil {
			return nil, err
		}
		nameMap := make(map[string]string, len(defs))
		for _, d := range defs {
			if d.id != idLower {
				nameMap[d.id] = d.name
			}
		}
		expr, err := CompileFormula(source, func(name string) (string, bool) {
			for _, d := range defs {
				if d.name == name && d.id != idLower {
					return d.id, true
				}
			}
			for _, d := range defs {
				if strings.EqualFold(d.id, name) && d.id != idLower {
					return d.id, true
				}
			}
			return "", false
		})
		if err != nil {
			return nil, errInvalid(err.Error())
		}
		rendered := expr.Source(func(id string) (string, bool) {
			n, ok := nameMap[id]
			return n, ok
		})
		stored, err = storedFormulaConfig(expr, rendered)
		if err != nil {
			return nil, err
		}
	default:
		return nil, errInvalid("不支持的属性类型")
	}

	graph := issuePropertyDependencyGraph(defs)
	graph[idLower] = issuePropertyDependencies(kind, stored)
	if hasCycleFrom(graph, idLower) {
		return nil, errInvalid("属性存在循环引用")
	}
	return stored, nil
}

func issuePropertyDependencies(kind string, config any) []string {
	switch kind {
	case "lookup":
		if cfg, err := issueLookupConfigFromStored(config); err == nil {
			return []string{cfg.RelationPropertyID}
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

func issuePropertyDependencyGraph(defs []issuePropertyDef) map[string][]string {
	graph := make(map[string][]string)
	for _, d := range defs {
		if d.kind == "lookup" || d.kind == "formula" {
			graph[strings.ToLower(d.id)] = issuePropertyDependencies(d.kind, d.config)
		}
	}
	return graph
}

func verifyIssueLookupTargetField(ctx context.Context, tx pgx.Tx, workspace, relationPropID, targetFieldID string) error {
	var rawCfg string
	err := tx.QueryRow(ctx,
		`SELECT config::text FROM issue_property
		 WHERE id = $1::uuid AND workspace_id = $2::uuid AND type = 'record_relation'`,
		relationPropID, workspace,
	).Scan(&rawCfg)
	if errors.Is(err, pgx.ErrNoRows) {
		return errInvalid("查找引用的 record_relation 不存在")
	}
	if err != nil {
		return logFailed(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(rawCfg), &parsed); err != nil {
		return errFailed(err)
	}
	targetTable, _ := parsed["target_table_id"].(string)
	if !IsUUID(targetTable) {
		return errInvalid("record_relation 缺少目标表")
	}
	var exists bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS(
		     SELECT 1 FROM generic_field
		     WHERE id = $1::uuid AND table_id = $2::uuid AND workspace_id = $3::uuid
		 )`,
		targetFieldID, targetTable, workspace,
	).Scan(&exists); err != nil {
		return logFailed(err)
	}
	if !exists {
		return errInvalid("查找的目标字段不存在")
	}
	return nil
}

// CompileIssuePropertyConfigInTx compiles and validates a native task `lookup` or `formula`
// property definition config inside an existing transaction.
func CompileIssuePropertyConfigInTx(
	ctx context.Context,
	tx pgx.Tx,
	workspaceID string,
	propertyID *string,
	kind string,
	rawConfig json.RawMessage,
) (json.RawMessage, error) {
	if kind != "lookup" && kind != "formula" {
		return nil, errInvalid("unsupported derived kind")
	}
	var cfgVal any
	if err := json.Unmarshal(rawConfig, &cfgVal); err != nil {
		return nil, errInvalid("属性配置格式无效")
	}
	defs, err := loadIssuePropertyDefs(ctx, tx, workspaceID)
	if err != nil {
		return nil, err
	}
	propID := "00000000-0000-4000-8000-000000000000"
	if propertyID != nil && strings.TrimSpace(*propertyID) != "" {
		propID = strings.TrimSpace(*propertyID)
	}
	stored, err := compileIssueDerivedConfig(defs, propID, kind, cfgVal)
	if err != nil {
		return nil, err
	}
	if kind == "lookup" {
		lookup, err := issueLookupConfigFromStored(stored)
		if err != nil {
			return nil, errInvalid("lookup config invalid after compile")
		}
		if err := verifyIssueLookupTargetField(ctx, tx, workspaceID, lookup.RelationPropertyID, lookup.TargetFieldID); err != nil {
			return nil, err
		}
	}
	encoded, err := marshalCompact(stored)
	if err != nil {
		return nil, errFailed(err)
	}
	return json.RawMessage(encoded), nil
}

// CompileIssuePropertyConfig compiles and validates a native task `lookup` or `formula`
// property definition config against workspace issue properties and generic table targets.
func CompileIssuePropertyConfig(
	ctx context.Context,
	pool Pool,
	workspaceID string,
	propertyID *string,
	kind string,
	rawConfig json.RawMessage,
) (json.RawMessage, error) {
	tx, err := beginTx(ctx, pool)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	encoded, err := CompileIssuePropertyConfigInTx(ctx, tx, workspaceID, propertyID, kind, rawConfig)
	if err != nil {
		return nil, err
	}
	if err := commitTx(ctx, tx); err != nil {
		return nil, err
	}
	return encoded, nil
}

// ProjectIssueProperties evaluates read-only lookup and formula properties for the given
// already-authorized issue IDs in a single REPEATABLE READ READ ONLY snapshot transaction.
func ProjectIssueProperties(
	ctx context.Context,
	pool Pool,
	workspaceID string,
	issueIDs []string,
) (map[string]map[string]json.RawMessage, error) {
	if len(issueIDs) > MaxIssueProjectionBatch {
		return nil, errInvalid("too many issue ids")
	}
	if len(issueIDs) == 0 {
		return map[string]map[string]json.RawMessage{}, nil
	}

	tx, err := beginTx(ctx, pool)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SET TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY`); err != nil {
		return nil, logFailed(err)
	}

	defs, err := loadIssuePropertyDefs(ctx, tx, workspaceID)
	if err != nil {
		return nil, err
	}
	hasDerived := false
	for _, d := range defs {
		if d.kind == "lookup" || d.kind == "formula" {
			hasDerived = true
			break
		}
	}
	if !hasDerived {
		return map[string]map[string]json.RawMessage{}, nil
	}

	seen := make(map[string]struct{}, len(issueIDs))
	idList := make([]string, 0, len(issueIDs))
	for _, raw := range issueIDs {
		id := strings.ToLower(strings.TrimSpace(raw))
		if IsUUID(id) {
			if _, ok := seen[id]; !ok {
				seen[id] = struct{}{}
				idList = append(idList, id)
			}
		}
	}
	if len(idList) == 0 {
		return nil, errInvalid("no valid issue ids")
	}

	rows, err := tx.Query(ctx,
		`SELECT id::text, properties::text FROM issue
		 WHERE workspace_id = $1::uuid AND id = ANY($2::uuid[])`,
		workspaceID, idList,
	)
	if err != nil {
		return nil, logFailed(err)
	}
	storedByIssue := make(map[string]map[string]any, len(idList))
	foundIDs := make([]string, 0, len(idList))
	for rows.Next() {
		var id, propsRaw string
		if err := rows.Scan(&id, &propsRaw); err != nil {
			rows.Close()
			return nil, logFailed(err)
		}
		var bag map[string]any
		if err := json.Unmarshal([]byte(propsRaw), &bag); err != nil || bag == nil {
			bag = map[string]any{}
		}
		foundIDs = append(foundIDs, id)
		storedByIssue[id] = bag
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, logFailed(err)
	}

	computed, err := mergeComputedIssueProperties(ctx, tx, workspaceID, defs, foundIDs, storedByIssue)
	if err != nil {
		return nil, err
	}
	if err := commitTx(ctx, tx); err != nil {
		return nil, err
	}

	out := make(map[string]map[string]json.RawMessage, len(computed))
	for issueID, bag := range computed {
		encodedBag := make(map[string]json.RawMessage, len(bag))
		for propID, val := range bag {
			b, err := marshalCompact(val)
			if err != nil {
				return nil, errFailed(err)
			}
			encodedBag[propID] = json.RawMessage(b)
		}
		out[issueID] = encodedBag
	}
	return out, nil
}

// ProjectTaskFormula evaluates a single formula/lookup property for one issue.
func ProjectTaskFormula(ctx context.Context, pool Pool, workspaceID, issueID, propertyID string) (json.RawMessage, error) {
	props, err := ProjectIssueProperties(ctx, pool, workspaceID, []string{issueID})
	if err != nil {
		return nil, err
	}
	bag := props[strings.ToLower(strings.TrimSpace(issueID))]
	if bag == nil {
		return nil, nil
	}
	return bag[strings.ToLower(strings.TrimSpace(propertyID))], nil
}

func loadIssuePropertyDefs(ctx context.Context, tx pgx.Tx, workspaceID string) ([]issuePropertyDef, error) {
	rows, err := tx.Query(ctx,
		`SELECT id::text, name, type, config::text FROM issue_property
		 WHERE workspace_id = $1::uuid AND archived_at IS NULL`,
		workspaceID,
	)
	if err != nil {
		return nil, logFailed(err)
	}
	defer rows.Close()
	var defs []issuePropertyDef
	for rows.Next() {
		var d issuePropertyDef
		var raw string
		if err := rows.Scan(&d.id, &d.name, &d.kind, &raw); err != nil {
			return nil, logFailed(err)
		}
		if err := json.Unmarshal([]byte(raw), &d.config); err != nil {
			return nil, errFailed(err)
		}
		defs = append(defs, d)
	}
	if err := rows.Err(); err != nil {
		return nil, logFailed(err)
	}
	return defs, nil
}

func mergeComputedIssueProperties(
	ctx context.Context,
	tx pgx.Tx,
	workspaceID string,
	defs []issuePropertyDef,
	issueIDs []string,
	storedByIssue map[string]map[string]any,
) (map[string]map[string]any, error) {
	if len(issueIDs) == 0 {
		return map[string]map[string]any{}, nil
	}
	if len(issueIDs) > MaxIssueProjectionBatch {
		return nil, errInvalid("too many issues in projection batch")
	}
	var derived []issuePropertyDef
	for _, d := range defs {
		if d.kind == "lookup" || d.kind == "formula" {
			derived = append(derived, d)
		}
	}
	if len(derived) == 0 {
		return map[string]map[string]any{}, nil
	}

	derivedOrder := topoDerivedProperties(defs)

	relationMeta := make(map[string]string) // propID -> target_table_id
	for _, d := range defs {
		if d.kind == "record_relation" {
			if cfgMap, ok := d.config.(map[string]any); ok {
				if tbl, ok := cfgMap["target_table_id"].(string); ok {
					relationMeta[d.id] = strings.ToLower(tbl)
				}
			}
		}
	}

	lookupCfgs := make(map[string]issueLookupConfig)
	for _, d := range derived {
		if d.kind == "lookup" {
			if cfg, err := issueLookupConfigFromStored(d.config); err == nil {
				lookupCfgs[d.id] = cfg
			}
		}
	}

	batchLoads := make(map[tableFieldRef]map[string]struct{})
	for _, issueID := range issueIDs {
		stored, ok := storedByIssue[issueID]
		if !ok {
			continue
		}
		for _, d := range derived {
			if d.kind != "lookup" {
				continue
			}
			lookup, ok := lookupCfgs[d.id]
			if !ok {
				continue
			}
			targetTable, ok := relationMeta[lookup.RelationPropertyID]
			if !ok {
				continue
			}
			recIDs := recordIDsFromRelationValue(stored[lookup.RelationPropertyID])
			if len(recIDs) == 0 {
				continue
			}
			key := tableFieldRef{tableID: targetTable, fieldID: lookup.TargetFieldID}
			set := batchLoads[key]
			if set == nil {
				set = make(map[string]struct{})
				batchLoads[key] = set
			}
			for _, rid := range recIDs {
				set[rid] = struct{}{}
			}
		}
	}

	lookupCache, err := batchLookupTargetValues(ctx, tx, workspaceID, batchLoads)
	if err != nil {
		return nil, err
	}

	defByID := make(map[string]issuePropertyDef, len(defs))
	for _, d := range defs {
		defByID[d.id] = d
	}

	result := make(map[string]map[string]any)
	for _, issueID := range issueIDs {
		stored, ok := storedByIssue[issueID]
		if !ok {
			continue
		}
		merged := projectComputedForIssue(defByID, derivedOrder, lookupCfgs, relationMeta, lookupCache, stored)
		if len(merged) > 0 {
			result[issueID] = merged
		}
	}
	return result, nil
}

type propEvalResult struct {
	value any
	err   error
}

func projectComputedForIssue(
	defByID map[string]issuePropertyDef,
	derivedOrder []string,
	lookupCfgs map[string]issueLookupConfig,
	relationMeta map[string]string,
	lookupCache map[tableFieldRef]map[string]any,
	stored map[string]any,
) map[string]any {
	out := make(map[string]any)
	cache := make(map[string]propEvalResult)
	for _, propID := range derivedOrder {
		def, ok := defByID[propID]
		if !ok {
			continue
		}
		var res propEvalResult
		switch def.kind {
		case "lookup":
			v, err := evalIssueLookup(def, stored, lookupCfgs, relationMeta, lookupCache)
			res = propEvalResult{value: v, err: err}
		case "formula":
			v, err := evalIssueFormula(defByID, stored, def, cache)
			res = propEvalResult{value: v, err: err}
		default:
			continue
		}
		if res.err != nil {
			out[propID] = nil
		} else {
			out[propID] = res.value
		}
		cache[propID] = res
	}
	return out
}

func topoDerivedProperties(defs []issuePropertyDef) []string {
	var derived []string
	graph := make(map[string][]string)
	for _, d := range defs {
		if d.kind == "lookup" || d.kind == "formula" {
			derived = append(derived, d.id)
			graph[d.id] = issuePropertyDependencies(d.kind, d.config)
		}
	}
	var order []string
	visited := make(map[string]struct{}, len(derived))
	var visit func(node string)
	visit = func(node string) {
		if _, ok := visited[node]; ok {
			return
		}
		visited[node] = struct{}{}
		for _, dep := range graph[node] {
			if _, isDerived := graph[dep]; isDerived {
				visit(dep)
			}
		}
		order = append(order, node)
	}
	for _, seed := range derived {
		visit(seed)
	}
	return order
}

func evalIssueLookup(
	def issuePropertyDef,
	stored map[string]any,
	lookupCfgs map[string]issueLookupConfig,
	relationMeta map[string]string,
	lookupCache map[tableFieldRef]map[string]any,
) (any, error) {
	lookup, ok := lookupCfgs[def.id]
	if !ok {
		return nil, errors.New("查找属性配置无效")
	}
	targetTable, ok := relationMeta[lookup.RelationPropertyID]
	if !ok {
		return nil, errors.New("查找引用的 record_relation 不存在")
	}
	recordIDs := recordIDsFromRelationValue(stored[lookup.RelationPropertyID])
	if len(recordIDs) == 0 {
		return []any{}, nil
	}
	key := tableFieldRef{tableID: targetTable, fieldID: lookup.TargetFieldID}
	values, ok := lookupCache[key]
	if !ok {
		return nil, errors.New("查找目标不可用")
	}
	ordered := make([]any, 0, len(recordIDs))
	for _, rid := range recordIDs {
		if val, exists := values[rid]; exists {
			ordered = append(ordered, val)
		}
	}
	return ordered, nil
}

func evalIssueFormula(
	defByID map[string]issuePropertyDef,
	stored map[string]any,
	def issuePropertyDef,
	cache map[string]propEvalResult,
) (any, error) {
	expr, err := parseStoredAST(def.config)
	if err != nil {
		return nil, errors.New("公式语法树无效")
	}
	return expr.Evaluate(func(propID string) (any, error) {
		if d, ok := defByID[propID]; ok && (d.kind == "lookup" || d.kind == "formula") {
			res, exists := cache[propID]
			if !exists {
				return nil, errors.New("依赖尚未计算")
			}
			if res.err != nil {
				return nil, res.err
			}
			return res.value, nil
		}
		return stored[propID], nil
	})
}

func recordIDsFromRelationValue(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if id, ok := item.(string); ok && IsUUID(id) {
			out = append(out, strings.ToLower(id))
		}
	}
	return out
}

func batchLookupTargetValues(
	ctx context.Context,
	tx pgx.Tx,
	workspace string,
	loads map[tableFieldRef]map[string]struct{},
) (map[tableFieldRef]map[string]any, error) {
	out := make(map[tableFieldRef]map[string]any, len(loads))
	keys := make([]tableFieldRef, 0, len(loads))
	for k := range loads {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].tableID != keys[j].tableID {
			return keys[i].tableID < keys[j].tableID
		}
		return keys[i].fieldID < keys[j].fieldID
	})
	for _, key := range keys {
		set := loads[key]
		if len(set) == 0 {
			out[key] = map[string]any{}
			continue
		}
		list := make([]string, 0, len(set))
		for id := range set {
			list = append(list, id)
		}
		sort.Strings(list)
		vals, err := valuesForTableRecords(ctx, tx, workspace, key.tableID, list, key.fieldID)
		if err != nil {
			return nil, err
		}
		out[key] = vals
	}
	return out, nil
}

func valuesForTableRecords(
	ctx context.Context,
	tx pgx.Tx,
	workspace, tableID string,
	recordIDs []string,
	fieldID string,
) (map[string]any, error) {
	fields, err := loadFields(ctx, tx, workspace, tableID)
	if err != nil {
		return nil, err
	}
	var target *FieldDto
	for i := range fields {
		if fields[i].ID == fieldID {
			target = &fields[i]
			break
		}
	}
	if target == nil {
		return map[string]any{}, nil
	}
	derived := target.Kind == "lookup" || target.Kind == "formula"
	rows, err := tx.Query(ctx,
		`SELECT r.id::text, r.revision, r.created_at::text, r.updated_at::text,
		        c.field_id::text, c.value::text
		 FROM generic_record r
		 LEFT JOIN generic_cell c ON c.workspace_id = r.workspace_id AND c.record_id = r.id
		   AND ($4::bool OR c.field_id = $5::uuid)
		 WHERE r.workspace_id = $1::uuid AND r.table_id = $2::uuid
		   AND r.id = ANY($3::text[]::uuid[])`,
		workspace, tableID, recordIDs, derived, fieldID,
	)
	if err != nil {
		return nil, logFailed(err)
	}
	byID := make(map[string]*RecordDto)
	var order []string
	for rows.Next() {
		var id, cAt, uAt string
		var rev int64
		var key, raw *string
		if err := rows.Scan(&id, &rev, &cAt, &uAt, &key, &raw); err != nil {
			rows.Close()
			return nil, logFailed(err)
		}
		rec, exists := byID[id]
		if !exists {
			rec = &RecordDto{
				ID:        id,
				Revision:  rev,
				CreatedAt: cAt,
				UpdatedAt: uAt,
				Values:    make(map[string]any),
			}
			byID[id] = rec
			order = append(order, id)
		}
		if key != nil && raw != nil {
			var val any
			if err := json.Unmarshal([]byte(*raw), &val); err != nil {
				rows.Close()
				return nil, errFailed(err)
			}
			rec.Values[*key] = val
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, logFailed(err)
	}
	sort.Strings(order)
	records := make([]RecordDto, len(order))
	for i, id := range order {
		records[i] = *byID[id]
	}
	if derived {
		if err := projectSheet(ctx, tx, workspace, tableID, fields, records, primaryRowsAll); err != nil {
			return nil, err
		}
	}
	out := make(map[string]any, len(records))
	for _, rec := range records {
		out[rec.ID] = rec.Values[fieldID]
	}
	return out, nil
}
