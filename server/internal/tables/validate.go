package tables

import (
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Kinds lists all supported generic-table field kinds.
var Kinds = []string{
	"text",
	"number",
	"date",
	"single_select",
	"checkbox",
	"relation",
	"lookup",
	"formula",
	"attachment",
}

// ViewKinds lists all supported generic-table view kinds.
var ViewKinds = []string{
	"grid",
	"kanban",
	"gallery",
	"calendar",
	"gantt",
	"org_chart",
	"form",
}

const maxAttachmentCellItems = 50

// FieldDef is a compact field definition used during validation and view filtering/sorting.
type FieldDef struct {
	ID     string
	Kind   string
	Config any
}

func IsEditableKind(kind string) bool {
	return kind != "lookup" && kind != "formula"
}

func IsUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	groups := [5]int{8, 4, 4, 4, 12}
	idx := 0
	for g, length := range groups {
		if g > 0 {
			if value[idx] != '-' {
				return false
			}
			idx++
		}
		for range length {
			if !isASCIIHex(value[idx]) {
				return false
			}
			idx++
		}
	}
	return idx == len(value)
}

func isASCIIHex(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')
}

func CleanName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	n := utf8.RuneCountInString(name)
	if n < 1 || n > 80 {
		return "", errInvalid("名称需要 1 到 80 个字符")
	}
	return name, nil
}

func NormalizeConfig(kind string, config any) (any, error) {
	if !slices.Contains(Kinds, kind) {
		return nil, errInvalid("不支持的字段类型")
	}
	if kind == "relation" {
		cfg, err := parseRelationConfigInput(config)
		if err != nil {
			return nil, err
		}
		return cfg.toJSON(), nil
	}
	if kind == "lookup" {
		cfg, err := parseLookupConfigInput(config)
		if err != nil {
			return nil, err
		}
		return cfg.toJSON(), nil
	}
	if kind == "formula" {
		expr, err := parseFormulaInput(config)
		if err != nil {
			return nil, err
		}
		return map[string]any{"expression": expr}, nil
	}
	if kind != "single_select" {
		if config == nil {
			return map[string]any{}, nil
		}
		if m, ok := config.(map[string]any); ok && len(m) == 0 {
			return map[string]any{}, nil
		}
		return nil, errInvalid("这个字段类型不能带选项")
	}
	obj, ok := config.(map[string]any)
	if !ok {
		return nil, errInvalid("单选字段需要选项")
	}
	options, ok := obj["options"].([]any)
	if !ok {
		return nil, errInvalid("单选字段需要选项")
	}
	if len(options) == 0 || len(options) > 50 {
		return nil, errInvalid("单选选项需要 1 到 50 个")
	}
	seen := make(map[string]struct{}, len(options))
	normalized := make([]any, 0, len(options))
	for _, rawOpt := range options {
		optMap, ok := rawOpt.(map[string]any)
		if !ok {
			return nil, errInvalid("选项缺少标识")
		}
		id, ok := optMap["id"].(string)
		if !ok {
			return nil, errInvalid("选项缺少标识")
		}
		rawLabel, ok := optMap["label"].(string)
		if !ok {
			return nil, errInvalid("选项缺少名称")
		}
		label := strings.TrimSpace(rawLabel)
		if !isOptionID(id) {
			return nil, errInvalid("选项标识只能使用小写字母、数字、下划线和短横线")
		}
		labelLen := utf8.RuneCountInString(label)
		if labelLen < 1 || labelLen > 80 {
			return nil, errInvalid("选项名称需要 1 到 80 个字符")
		}
		if _, dup := seen[id]; dup {
			return nil, errInvalid("选项标识不能重复")
		}
		seen[id] = struct{}{}
		normalized = append(normalized, map[string]any{"id": id, "label": label})
	}
	return map[string]any{"options": normalized}, nil
}

// NormalizeValue returns (value, hasValue, err). hasValue==false clears the cell.
// Checkbox false stays (false, true, nil). Lookup/formula fields are read-only.
func NormalizeValue(field FieldDef, value any) (any, bool, error) {
	if field.Kind == "lookup" || field.Kind == "formula" {
		return nil, false, errInvalid("计算字段和查找字段是只读的，不能直接写入")
	}
	if value == nil {
		return nil, false, nil
	}
	switch field.Kind {
	case "text":
		text, ok := value.(string)
		if !ok {
			return nil, false, errInvalid("文本字段只能填写文字")
		}
		if strings.TrimSpace(text) == "" {
			return nil, false, nil
		}
		if utf8.RuneCountInString(text) > 4000 {
			return nil, false, errInvalid("文本不能超过 4000 个字符")
		}
		return text, true, nil
	case "number":
		num, ok := asFloat64(value)
		if !ok {
			return nil, false, errInvalid("数字字段只能填写数字")
		}
		if math.IsNaN(num) || math.IsInf(num, 0) {
			return nil, false, errInvalid("数字超出可保存的范围")
		}
		return num, true, nil
	case "date":
		text, ok := value.(string)
		if !ok {
			return nil, false, errInvalid("日期需要使用 YYYY-MM-DD")
		}
		if !isCalendarDate(text) {
			return nil, false, errInvalid("日期需要是真实存在的 YYYY-MM-DD")
		}
		return text, true, nil
	case "single_select":
		picked, ok := value.(string)
		if !ok {
			return nil, false, errInvalid("单选字段只能选择一个选项")
		}
		if !slices.Contains(optionIDs(field), picked) {
			return nil, false, errInvalid("选项不在这个字段里")
		}
		return picked, true, nil
	case "checkbox":
		checked, ok := value.(bool)
		if !ok {
			return nil, false, errInvalid("勾选字段只能是勾选、未勾选或清空")
		}
		return checked, true, nil
	case "relation":
		return normalizeRelationCell(value)
	case "attachment":
		return NormalizeAttachmentCell(value)
	default:
		return nil, false, errInvalid("不支持的字段类型")
	}
}

func NormalizeAttachmentCell(value any) (any, bool, error) {
	items, ok := value.([]any)
	if !ok {
		return nil, false, errInvalid("附件字段需要附件标识数组")
	}
	if len(items) == 0 {
		return nil, false, nil
	}
	if len(items) > maxAttachmentCellItems {
		return nil, false, errInvalid("单个附件单元格最多关联 50 个附件")
	}
	seen := make(map[string]struct{}, len(items))
	out := make([]any, 0, len(items))
	for _, item := range items {
		raw, ok := item.(string)
		if !ok {
			return nil, false, errInvalid("附件标识必须是有效的 UUID")
		}
		trimmed := strings.TrimSpace(raw)
		if !IsUUID(trimmed) {
			return nil, false, errInvalid("附件标识必须是有效的 UUID")
		}
		lower := strings.ToLower(trimmed)
		if _, dup := seen[lower]; !dup {
			seen[lower] = struct{}{}
			out = append(out, lower)
		}
	}
	return out, true, nil
}

func NormalizeView(
	tableID string,
	name string,
	kind string,
	config any,
	columns []string,
	filter *StoredFilter,
	sortSpec *StoredSort,
	group *string,
	fields []FieldDef,
) (StoredView, error) {
	if !slices.Contains(ViewKinds, kind) {
		return StoredView{}, errInvalid("不支持的视图类型")
	}
	byID := make(map[string]FieldDef, len(fields))
	for _, f := range fields {
		byID[f.ID] = f
	}
	var normCols []string
	if columns != nil {
		seen := make(map[string]struct{}, len(columns))
		normCols = make([]string, 0, len(columns))
		for _, id := range columns {
			if _, ok := byID[id]; !ok {
				return StoredView{}, errInvalid("可见列里有不属于这张表的字段")
			}
			if _, dup := seen[id]; dup {
				return StoredView{}, errInvalid("可见列不能重复")
			}
			seen[id] = struct{}{}
			normCols = append(normCols, id)
		}
	}
	var normFilter *StoredFilter
	if filter != nil {
		f, err := checkViewFilter(*filter, byID)
		if err != nil {
			return StoredView{}, err
		}
		normFilter = &f
	}
	var normSort *StoredSort
	if sortSpec != nil {
		if _, ok := byID[sortSpec.FieldID]; !ok {
			return StoredView{}, errInvalid("排序字段不属于这张表")
		}
		if sortSpec.Direction != "asc" && sortSpec.Direction != "desc" {
			return StoredView{}, errInvalid("排序方向只能是升序或降序")
		}
		cp := *sortSpec
		normSort = &cp
	}
	var normGroup *string
	if group != nil {
		if _, ok := byID[*group]; !ok {
			return StoredView{}, errInvalid("分组字段不属于这张表")
		}
		g := *group
		normGroup = &g
	}
	normCfg, err := normalizeViewConfig(tableID, kind, config, normCols, byID)
	if err != nil {
		return StoredView{}, err
	}
	cleanName, err := CleanName(name)
	if err != nil {
		return StoredView{}, err
	}
	return StoredView{
		Name:    cleanName,
		Kind:    kind,
		Config:  normCfg,
		Columns: normCols,
		Filter:  normFilter,
		Sort:    normSort,
		Group:   normGroup,
	}, nil
}

func normalizeViewConfig(tableID, kind string, raw any, columns []string, byID map[string]FieldDef) (any, error) {
	if raw == nil {
		return map[string]any{}, nil
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return nil, errInvalid("视图配置必须是对象")
	}
	var allowed []string
	switch kind {
	case "grid":
		allowed = nil
	case "kanban":
		allowed = []string{"group_field_id", "title_field_id"}
	case "gallery":
		allowed = []string{"title_field_id"}
	case "calendar":
		allowed = []string{"date_field_id", "title_field_id"}
	case "gantt":
		allowed = []string{"date_field_id", "end_date_field_id", "title_field_id"}
	case "org_chart":
		allowed = []string{"parent_field_id", "title_field_id"}
	case "form":
		allowed = []string{"required_field_ids"}
	default:
		return nil, errInvalid("不支持的视图类型")
	}
	for k := range obj {
		if !slices.Contains(allowed, k) {
			return nil, errInvalid("视图配置包含不支持的参数")
		}
	}
	normalized := map[string]any{}
	if id, has, err := optionalConfigFieldID(obj, "title_field_id"); err != nil {
		return nil, err
	} else if has {
		if _, ok := byID[id]; !ok {
			return nil, errInvalid("标题字段不属于这张表")
		}
		normalized["title_field_id"] = id
	}
	if id, has, err := optionalConfigFieldID(obj, "group_field_id"); err != nil {
		return nil, err
	} else if has {
		field, ok := byID[id]
		if !ok {
			return nil, errInvalid("看板分组字段不属于这张表")
		}
		if field.Kind != "single_select" {
			return nil, errInvalid("看板分组字段必须是单选字段")
		}
		normalized["group_field_id"] = id
	}
	if id, has, err := optionalConfigFieldID(obj, "date_field_id"); err != nil {
		return nil, err
	} else if has {
		field, ok := byID[id]
		if !ok {
			return nil, errInvalid("日期字段不属于这张表")
		}
		if field.Kind != "date" {
			return nil, errInvalid("视图日期字段必须是日期类型")
		}
		normalized["date_field_id"] = id
	}
	if id, has, err := optionalConfigFieldID(obj, "end_date_field_id"); err != nil {
		return nil, err
	} else if has {
		field, ok := byID[id]
		if !ok {
			return nil, errInvalid("结束日期字段不属于这张表")
		}
		if field.Kind != "date" {
			return nil, errInvalid("结束日期字段必须是日期类型")
		}
		normalized["end_date_field_id"] = id
	}
	if id, has, err := optionalConfigFieldID(obj, "parent_field_id"); err != nil {
		return nil, err
	} else if has {
		field, ok := byID[id]
		if !ok {
			return nil, errInvalid("上级关联字段不属于这张表")
		}
		if field.Kind != "relation" {
			return nil, errInvalid("组织架构上级字段必须是单向本表关联字段")
		}
		rel, err := relationConfigFromStored(field.Config)
		if err != nil {
			return nil, errInvalid("组织架构上级字段关联配置无效")
		}
		if rel.Bidirectional || !strings.EqualFold(rel.TargetTableID, tableID) {
			return nil, errInvalid("组织架构上级字段必须是单向本表关联字段")
		}
		normalized["parent_field_id"] = id
	}
	if rawReq, exists := obj["required_field_ids"]; exists && rawReq != nil {
		items, ok := rawReq.([]any)
		if !ok {
			return nil, errInvalid("必填字段列表格式不正确")
		}
		var shown map[string]struct{}
		if columns != nil {
			shown = make(map[string]struct{}, len(columns))
			for _, c := range columns {
				shown[c] = struct{}{}
			}
		}
		seen := make(map[string]struct{}, len(items))
		required := make([]any, 0, len(items))
		for _, item := range items {
			rawStr, ok := item.(string)
			if !ok {
				return nil, errInvalid("必填字段标识无效")
			}
			trimmed := strings.TrimSpace(rawStr)
			if !IsUUID(trimmed) {
				return nil, errInvalid("必填字段标识无效")
			}
			id := strings.ToLower(trimmed)
			field, ok := byID[id]
			if !ok {
				return nil, errInvalid("必填字段不属于这张表")
			}
			if !IsEditableKind(field.Kind) {
				return nil, errInvalid("只读计算字段不能设为表单必填")
			}
			if shown != nil {
				if _, vis := shown[id]; !vis {
					return nil, errInvalid("表单必填字段必须在可见列中显示")
				}
			}
			if _, dup := seen[id]; dup {
				return nil, errInvalid("必填字段不能重复")
			}
			seen[id] = struct{}{}
			required = append(required, id)
		}
		if len(required) > 0 {
			normalized["required_field_ids"] = required
		}
	}
	return normalized, nil
}

func optionalConfigFieldID(obj map[string]any, key string) (string, bool, error) {
	val, exists := obj[key]
	if !exists || val == nil {
		return "", false, nil
	}
	raw, ok := val.(string)
	if !ok {
		return "", false, errInvalid("视图配置中的字段标识无效")
	}
	trimmed := strings.TrimSpace(raw)
	if !IsUUID(trimmed) {
		return "", false, errInvalid("视图配置中的字段标识无效")
	}
	return strings.ToLower(trimmed), true, nil
}

func effectiveFormFieldIDs(fields []FieldDef, columns []string) map[string]struct{} {
	editable := make(map[string]struct{}, len(fields))
	for _, f := range fields {
		if IsEditableKind(f.Kind) {
			editable[f.ID] = struct{}{}
		}
	}
	if columns == nil {
		return editable
	}
	out := make(map[string]struct{}, len(columns))
	for _, id := range columns {
		if _, ok := editable[id]; ok {
			out[id] = struct{}{}
		}
	}
	return out
}

func ValidateFormSubmission(
	viewKind string,
	viewColumns []string,
	viewConfig any,
	fields []FieldDef,
	values map[string]any,
) (map[string]cellPatch, error) {
	if viewKind != "form" {
		return nil, errInvalid("只有表单视图支持提交记录")
	}
	byID := make(map[string]FieldDef, len(fields))
	for _, f := range fields {
		byID[f.ID] = f
	}
	enabled := effectiveFormFieldIDs(fields, viewColumns)
	normalized := make(map[string]cellPatch, len(values))
	for fieldID, rawVal := range values {
		field, ok := byID[fieldID]
		if !ok {
			return nil, errInvalid("有字段不属于这张表")
		}
		if !IsEditableKind(field.Kind) {
			return nil, errInvalid("计算字段和查找字段是只读的，不能通过表单提交")
		}
		if _, ok := enabled[fieldID]; !ok {
			return nil, errInvalid("提交了表单未启用的字段")
		}
		stored, hasVal, err := NormalizeValue(field, rawVal)
		if err != nil {
			return nil, err
		}
		normalized[fieldID] = cellPatch{value: stored, valid: hasVal}
	}
	if cfgMap, ok := viewConfig.(map[string]any); ok {
		if reqList, ok := cfgMap["required_field_ids"].([]any); ok {
			for _, item := range reqList {
				reqID, ok := item.(string)
				if !ok {
					continue
				}
				patch, exists := normalized[reqID]
				if !exists || !patch.valid {
					return nil, errInvalid("表单必填字段不能为空")
				}
			}
		}
	}
	return normalized, nil
}

// cellPatch distinguishes a cleared cell (valid=false -> nil in JSON / DELETE in DB)
// from a set value (valid=true, including false or 0).
type cellPatch struct {
	value any
	valid bool
}

func MatchesFilter(field FieldDef, cell any, hasCell bool, filter StoredFilter) bool {
	if !hasCell || cell == nil {
		hasCell = false
		cell = nil
	}
	switch filter.Op {
	case "is_empty":
		if !hasCell {
			return true
		}
		if field.Kind == "lookup" {
			if arr, ok := cell.([]any); ok {
				return len(arr) == 0
			}
		}
		return false
	case "is_not_empty":
		if !hasCell {
			return false
		}
		if field.Kind == "lookup" {
			if arr, ok := cell.([]any); ok {
				return len(arr) > 0
			}
		}
		return true
	case "eq":
		if !hasCell {
			return false
		}
		return filterValuesEqual(field, cell, filter.Value)
	case "contains":
		if !hasCell || (field.Kind != "text" && field.Kind != "formula") {
			return false
		}
		text, ok1 := cell.(string)
		needle, ok2 := filter.Value.(string)
		return ok1 && ok2 && strings.Contains(text, needle)
	default:
		return false
	}
}

type sortKeyKind int

const (
	sortKeyNone sortKeyKind = iota
	sortKeyNumber
	sortKeyText
)

type sortKey struct {
	kind sortKeyKind
	num  float64
	text string
}

func compareSortKey(a, b sortKey) int {
	if a.kind == sortKeyNumber && b.kind == sortKeyNumber {
		if a.num < b.num {
			return -1
		}
		if a.num > b.num {
			return 1
		}
		return 0
	}
	if a.kind == sortKeyText && b.kind == sortKeyText {
		return strings.Compare(a.text, b.text)
	}
	// Matches Rust PartialOrd on enum SortKey { Number, Text }: Number < Text.
	if a.kind < b.kind {
		return -1
	}
	if a.kind > b.kind {
		return 1
	}
	return 0
}

func computeSortKey(field FieldDef, cell any, hasCell bool) sortKey {
	if !hasCell || cell == nil {
		return sortKey{kind: sortKeyNone}
	}
	switch field.Kind {
	case "text", "date":
		if s, ok := cell.(string); ok {
			return sortKey{kind: sortKeyText, text: s}
		}
	case "number":
		if n, ok := asFloat64(cell); ok {
			return sortKey{kind: sortKeyNumber, num: n}
		}
	case "checkbox":
		if b, ok := cell.(bool); ok {
			if b {
				return sortKey{kind: sortKeyNumber, num: 1}
			}
			return sortKey{kind: sortKeyNumber, num: 0}
		}
	case "single_select":
		if picked, ok := cell.(string); ok {
			if idx := slices.Index(optionIDs(field), picked); idx >= 0 {
				return sortKey{kind: sortKeyNumber, num: float64(idx)}
			}
		}
	case "formula":
		if n, ok := asFloat64(cell); ok {
			return sortKey{kind: sortKeyNumber, num: n}
		}
		if s, ok := cell.(string); ok {
			return sortKey{kind: sortKeyText, text: s}
		}
		if b, ok := cell.(bool); ok {
			if b {
				return sortKey{kind: sortKeyNumber, num: 1}
			}
			return sortKey{kind: sortKeyNumber, num: 0}
		}
	}
	return sortKey{kind: sortKeyNone}
}

func checkViewFilter(filter StoredFilter, fields map[string]FieldDef) (StoredFilter, error) {
	field, ok := fields[filter.FieldID]
	if !ok {
		return StoredFilter{}, errInvalid("筛选字段不属于这张表")
	}
	switch filter.Op {
	case "is_empty", "is_not_empty":
		if filter.Value != nil {
			return StoredFilter{}, errInvalid("为空判断不需要比较值")
		}
		return StoredFilter{FieldID: field.ID, Op: filter.Op}, nil
	case "eq", "contains":
		if filter.Op == "contains" && field.Kind != "text" && field.Kind != "formula" {
			return StoredFilter{}, errInvalid("只有文本或公式字段可以按包含筛选")
		}
		if filter.Value == nil {
			return StoredFilter{}, errInvalid("筛选需要一个比较值")
		}
		var stored any
		if field.Kind == "formula" {
			var err error
			stored, err = normalizeFormulaFilterValue(filter.Op, filter.Value)
			if err != nil {
				return StoredFilter{}, err
			}
		} else if field.Kind == "lookup" {
			return StoredFilter{}, errInvalid("查找字段不支持按值筛选")
		} else {
			val, hasVal, err := NormalizeValue(field, filter.Value)
			if err != nil {
				return StoredFilter{}, err
			}
			if !hasVal {
				return StoredFilter{}, errInvalid("筛选不能和空值比较，请改用为空")
			}
			stored = val
		}
		return StoredFilter{FieldID: field.ID, Op: filter.Op, Value: stored}, nil
	default:
		return StoredFilter{}, errInvalid("不支持的筛选方式")
	}
}

func normalizeFormulaFilterValue(op string, value any) (any, error) {
	if op == "contains" {
		text, ok := value.(string)
		if !ok {
			return nil, errInvalid("包含筛选需要文本")
		}
		if strings.TrimSpace(text) == "" {
			return nil, errInvalid("筛选不能和空值比较，请改用为空")
		}
		return text, nil
	}
	if value == nil {
		return nil, errInvalid("筛选不能和空值比较，请改用为空")
	}
	if text, ok := value.(string); ok {
		if strings.TrimSpace(text) == "" {
			return nil, errInvalid("筛选不能和空值比较，请改用为空")
		}
		return text, nil
	}
	if num, ok := asFloat64(value); ok {
		if math.IsNaN(num) || math.IsInf(num, 0) {
			return nil, errInvalid("数字超出可保存的范围")
		}
		return num, nil
	}
	if b, ok := value.(bool); ok {
		return b, nil
	}
	return nil, errInvalid("公式筛选只支持文本、数字或布尔值")
}

func filterValuesEqual(field FieldDef, left, right any) bool {
	lf, lok := asFloat64(left)
	rf, rok := asFloat64(right)
	if field.Kind == "number" || (field.Kind == "formula" && lok && rok) {
		return lok && rok && lf == rf
	}
	return jsonEqual(left, right)
}

func optionIDs(field FieldDef) []string {
	obj, ok := field.Config.(map[string]any)
	if !ok {
		return nil
	}
	opts, ok := obj["options"].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(opts))
	for _, raw := range opts {
		if m, ok := raw.(map[string]any); ok {
			if id, ok := m["id"].(string); ok {
				out = append(out, id)
			}
		}
	}
	return out
}

func isOptionID(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		isLowerDigit := (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
		if i == 0 {
			if !isLowerDigit {
				return false
			}
			continue
		}
		if !isLowerDigit && c != '_' && c != '-' {
			return false
		}
	}
	return true
}

func isCalendarDate(value string) bool {
	if len(value) != 10 || value[4] != '-' || value[7] != '-' {
		return false
	}
	year, err1 := strconv.Atoi(value[0:4])
	month, err2 := strconv.Atoi(value[5:7])
	day, err3 := strconv.Atoi(value[8:10])
	if err1 != nil || err2 != nil || err3 != nil {
		return false
	}
	if month < 1 || month > 12 {
		return false
	}
	return day >= 1 && day <= daysInMonth(year, month)
}

func daysInMonth(year, month int) int {
	switch month {
	case 1, 3, 5, 7, 8, 10, 12:
		return 31
	case 4, 6, 9, 11:
		return 30
	case 2:
		if (year%4 == 0 && year%100 != 0) || year%400 == 0 {
			return 29
		}
		return 28
	default:
		return 0
	}
}
