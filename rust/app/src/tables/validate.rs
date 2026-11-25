use std::collections::{BTreeMap, BTreeSet};

use serde_json::{Map, Number, Value};

pub const KINDS: &[&str] = &["text", "number", "date", "single_select", "checkbox"];

#[derive(Clone)]
pub struct FieldDef {
    pub id: String,
    pub kind: String,
    pub config: Value,
}

#[derive(Clone, Debug, PartialEq, serde::Serialize, serde::Deserialize)]
pub struct StoredFilter {
    pub field_id: String,
    pub op: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub value: Option<Value>,
}

#[derive(Clone, Debug, PartialEq, serde::Serialize, serde::Deserialize)]
pub struct StoredSort {
    pub field_id: String,
    pub direction: String,
}

#[derive(Clone, Debug, PartialEq, serde::Serialize)]
pub struct StoredView {
    pub name: String,
    pub columns: Option<Vec<String>>,
    pub filter: Option<StoredFilter>,
    pub sort: Option<StoredSort>,
    pub group: Option<String>,
}

pub fn clean_name(raw: &str) -> Result<String, &'static str> {
    let name = raw.trim();
    let len = name.chars().count();
    if !(1..=80).contains(&len) {
        return Err("名称需要 1 到 80 个字符");
    }
    Ok(name.to_owned())
}

pub fn normalize_config(kind: &str, config: &Value) -> Result<Value, &'static str> {
    if !KINDS.contains(&kind) {
        return Err("不支持的字段类型");
    }
    if kind != "single_select" {
        if config.is_null() || config == &Value::Object(Map::new()) {
            return Ok(Value::Object(Map::new()));
        }
        return Err("这个字段类型不能带选项");
    }
    let Some(options) = config.get("options").and_then(Value::as_array) else {
        return Err("单选字段需要选项");
    };
    if options.is_empty() || options.len() > 50 {
        return Err("单选选项需要 1 到 50 个");
    }
    let mut seen = BTreeSet::new();
    let mut normalized = Vec::new();
    for option in options {
        let id = option
            .get("id")
            .and_then(Value::as_str)
            .ok_or("选项缺少标识")?;
        let label = option
            .get("label")
            .and_then(Value::as_str)
            .map(str::trim)
            .ok_or("选项缺少名称")?;
        if !is_option_id(id) {
            return Err("选项标识只能使用小写字母、数字、下划线和短横线");
        }
        let label_len = label.chars().count();
        if !(1..=80).contains(&label_len) {
            return Err("选项名称需要 1 到 80 个字符");
        }
        if !seen.insert(id.to_owned()) {
            return Err("选项标识不能重复");
        }
        let mut item = Map::new();
        item.insert("id".to_owned(), Value::String(id.to_owned()));
        item.insert("label".to_owned(), Value::String(label.to_owned()));
        normalized.push(Value::Object(item));
    }
    let mut object = Map::new();
    object.insert("options".to_owned(), Value::Array(normalized));
    Ok(Value::Object(object))
}

/// `Ok(None)` clears the cell. Checkbox `false` stays `Some(false)`.
pub fn normalize_value(field: &FieldDef, value: &Value) -> Result<Option<Value>, &'static str> {
    if value.is_null() {
        return Ok(None);
    }
    match field.kind.as_str() {
        "text" => {
            let text = value.as_str().ok_or("文本字段只能填写文字")?;
            if text.trim().is_empty() {
                return Ok(None);
            }
            if text.chars().count() > 4000 {
                return Err("文本不能超过 4000 个字符");
            }
            Ok(Some(Value::String(text.to_owned())))
        }
        "number" => {
            let number = value.as_number().ok_or("数字字段只能填写数字")?;
            let finite = number
                .as_f64()
                .filter(|item| item.is_finite())
                .ok_or("数字超出可保存的范围")?;
            let stored = Number::from_f64(finite).ok_or("数字超出可保存的范围")?;
            Ok(Some(Value::Number(stored)))
        }
        "date" => {
            let text = value.as_str().ok_or("日期需要使用 YYYY-MM-DD")?;
            if !is_calendar_date(text) {
                return Err("日期需要是真实存在的 YYYY-MM-DD");
            }
            Ok(Some(Value::String(text.to_owned())))
        }
        "single_select" => {
            let picked = value.as_str().ok_or("单选字段只能选择一个选项")?;
            if !option_ids(field).contains(&picked) {
                return Err("选项不在这个字段里");
            }
            Ok(Some(Value::String(picked.to_owned())))
        }
        "checkbox" => {
            let checked = value.as_bool().ok_or("勾选字段只能是勾选、未勾选或清空")?;
            Ok(Some(Value::Bool(checked)))
        }
        _ => Err("不支持的字段类型"),
    }
}

pub fn normalize_view(
    name: &str,
    columns: Option<&[String]>,
    filter: Option<&StoredFilter>,
    sort: Option<&StoredSort>,
    group: Option<&str>,
    fields: &[FieldDef],
) -> Result<StoredView, &'static str> {
    let by_id: BTreeMap<&str, &FieldDef> = fields
        .iter()
        .map(|field| (field.id.as_str(), field))
        .collect();
    let columns = match columns {
        None => None,
        Some(ids) => {
            let mut seen = BTreeSet::new();
            for id in ids {
                if !by_id.contains_key(id.as_str()) {
                    return Err("可见列里有不属于这张表的字段");
                }
                if !seen.insert(id.clone()) {
                    return Err("可见列不能重复");
                }
            }
            Some(ids.to_vec())
        }
    };
    let filter = match filter {
        None => None,
        Some(item) => Some(check_filter(item, &by_id)?),
    };
    let sort = match sort {
        None => None,
        Some(item) => {
            let field = by_id
                .get(item.field_id.as_str())
                .copied()
                .ok_or("排序字段不属于这张表")?;
            if item.direction != "asc" && item.direction != "desc" {
                return Err("排序方向只能是升序或降序");
            }
            let _ = field;
            Some(item.clone())
        }
    };
    let group = match group {
        None => None,
        Some(id) => {
            if !by_id.contains_key(id) {
                return Err("分组字段不属于这张表");
            }
            Some(id.to_owned())
        }
    };
    Ok(StoredView {
        name: clean_name(name)?,
        columns,
        filter,
        sort,
        group,
    })
}

pub fn matches(field: &FieldDef, cell: Option<&Value>, filter: &StoredFilter) -> bool {
    match filter.op.as_str() {
        "is_empty" => cell.is_none(),
        "is_not_empty" => cell.is_some(),
        "eq" => cell.is_some_and(|value| {
            values_equal(field, value, filter.value.as_ref().unwrap_or(&Value::Null))
        }),
        "contains" => match (field.kind.as_str(), cell, filter.value.as_ref()) {
            ("text", Some(Value::String(text)), Some(Value::String(needle))) => {
                text.contains(needle)
            }
            _ => false,
        },
        _ => false,
    }
}

pub fn sort_key(field: &FieldDef, cell: Option<&Value>) -> Option<SortKey> {
    let value = cell?;
    match field.kind.as_str() {
        "text" => value.as_str().map(|text| SortKey::Text(text.to_owned())),
        "number" => value.as_f64().map(SortKey::Number),
        "date" => value.as_str().map(|text| SortKey::Text(text.to_owned())),
        "checkbox" => value
            .as_bool()
            .map(|checked| SortKey::Number(if checked { 1.0 } else { 0.0 })),
        "single_select" => value.as_str().and_then(|picked| {
            option_ids(field)
                .iter()
                .position(|id| *id == picked)
                .map(|index| SortKey::Number(index as f64))
        }),
        _ => None,
    }
}

#[derive(Clone, PartialEq, PartialOrd)]
pub enum SortKey {
    Number(f64),
    Text(String),
}

fn check_filter(
    filter: &StoredFilter,
    fields: &BTreeMap<&str, &FieldDef>,
) -> Result<StoredFilter, &'static str> {
    let field = fields
        .get(filter.field_id.as_str())
        .copied()
        .ok_or("筛选字段不属于这张表")?;
    match filter.op.as_str() {
        "is_empty" | "is_not_empty" => {
            if filter.value.as_ref().is_some_and(|value| !value.is_null()) {
                return Err("为空判断不需要比较值");
            }
            Ok(StoredFilter {
                field_id: field.id.clone(),
                op: filter.op.clone(),
                value: None,
            })
        }
        "eq" | "contains" => {
            if filter.op == "contains" && field.kind != "text" {
                return Err("只有文本字段可以按包含筛选");
            }
            let value = filter.value.as_ref().ok_or("筛选需要一个比较值")?;
            let stored = normalize_value(field, value)?.ok_or("筛选不能和空值比较，请改用为空")?;
            Ok(StoredFilter {
                field_id: field.id.clone(),
                op: filter.op.clone(),
                value: Some(stored),
            })
        }
        _ => Err("不支持的筛选方式"),
    }
}

fn values_equal(field: &FieldDef, left: &Value, right: &Value) -> bool {
    if field.kind == "number" {
        return left.as_f64() == right.as_f64();
    }
    left == right
}

fn option_ids(field: &FieldDef) -> Vec<&str> {
    field
        .config
        .get("options")
        .and_then(Value::as_array)
        .map(|options| {
            options
                .iter()
                .filter_map(|option| option.get("id").and_then(Value::as_str))
                .collect()
        })
        .unwrap_or_default()
}

fn is_option_id(value: &str) -> bool {
    let mut chars = value.chars();
    let Some(first) = chars.next() else {
        return false;
    };
    if !first.is_ascii_lowercase() && !first.is_ascii_digit() {
        return false;
    }
    let mut count = 1;
    for ch in chars {
        if !(ch.is_ascii_lowercase() || ch.is_ascii_digit() || ch == '_' || ch == '-') {
            return false;
        }
        count += 1;
        if count > 64 {
            return false;
        }
    }
    true
}

fn is_calendar_date(value: &str) -> bool {
    let bytes = value.as_bytes();
    if bytes.len() != 10 || bytes[4] != b'-' || bytes[7] != b'-' {
        return false;
    }
    let Ok(year) = value[0..4].parse::<i32>() else {
        return false;
    };
    let Ok(month) = value[5..7].parse::<u32>() else {
        return false;
    };
    let Ok(day) = value[8..10].parse::<u32>() else {
        return false;
    };
    if !(1..=12).contains(&month) {
        return false;
    }
    day >= 1 && day <= days_in_month(year, month)
}

fn days_in_month(year: i32, month: u32) -> u32 {
    match month {
        1 | 3 | 5 | 7 | 8 | 10 | 12 => 31,
        4 | 6 | 9 | 11 => 30,
        2 if is_leap(year) => 29,
        2 => 28,
        _ => 0,
    }
}

fn is_leap(year: i32) -> bool {
    (year % 4 == 0 && year % 100 != 0) || year % 400 == 0
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn field(kind: &str, config: Value) -> FieldDef {
        FieldDef {
            id: "field-1".to_owned(),
            kind: kind.to_owned(),
            config,
        }
    }

    fn select_field() -> FieldDef {
        field(
            "single_select",
            json!({"options": [{"id": "high", "label": "高"}, {"id": "low", "label": "低"}]}),
        )
    }

    #[test]
    fn checkbox_keeps_false_and_rejects_other_falsy_values() {
        let checkbox = field("checkbox", json!({}));
        assert_eq!(
            normalize_value(&checkbox, &json!(false)).unwrap(),
            Some(json!(false))
        );
        assert_eq!(
            normalize_value(&checkbox, &json!(true)).unwrap(),
            Some(json!(true))
        );
        assert_eq!(normalize_value(&checkbox, &Value::Null).unwrap(), None);
        assert!(normalize_value(&checkbox, &json!(0)).is_err());
        assert!(normalize_value(&checkbox, &json!("")).is_err());
        assert!(normalize_value(&checkbox, &json!("false")).is_err());
    }

    #[test]
    fn empty_checkbox_does_not_match_an_equals_false_filter() {
        let checkbox = field("checkbox", json!({}));
        let filter = StoredFilter {
            field_id: checkbox.id.clone(),
            op: "eq".to_owned(),
            value: Some(json!(false)),
        };
        assert!(!matches(&checkbox, None, &filter));
        assert!(matches(&checkbox, Some(&json!(false)), &filter));
        let empty = StoredFilter {
            field_id: checkbox.id.clone(),
            op: "is_empty".to_owned(),
            value: None,
        };
        assert!(matches(&checkbox, None, &empty));
        assert!(!matches(&checkbox, Some(&json!(false)), &empty));
    }

    #[test]
    fn text_number_date_and_select_reject_the_wrong_shape() {
        let text = field("text", json!({}));
        assert_eq!(normalize_value(&text, &json!("  ")).unwrap(), None);
        assert_eq!(
            normalize_value(&text, &json!("资料")).unwrap(),
            Some(json!("资料"))
        );
        assert!(normalize_value(&text, &json!(1)).is_err());

        let number = field("number", json!({}));
        assert_eq!(
            normalize_value(&number, &json!(0)).unwrap(),
            Some(json!(0.0))
        );
        assert!(normalize_value(&number, &json!("0")).is_err());

        let date = field("date", json!({}));
        assert!(normalize_value(&date, &json!("2026-11-25")).is_ok());
        assert!(normalize_value(&date, &json!("2026-02-31")).is_err());

        let select = select_field();
        assert_eq!(
            normalize_value(&select, &json!("high")).unwrap(),
            Some(json!("high"))
        );
        assert!(normalize_value(&select, &json!("missing")).is_err());
    }

    #[test]
    fn filter_identity_is_the_field_id() {
        let mut named = select_field();
        let filter = StoredFilter {
            field_id: named.id.clone(),
            op: "eq".to_owned(),
            value: Some(json!("high")),
        };
        assert!(matches(&named, Some(&json!("high")), &filter));
        named.id = "renamed-but-this-struct-id-changed".to_owned();
        let still = StoredFilter {
            field_id: "field-1".to_owned(),
            op: "eq".to_owned(),
            value: Some(json!("high")),
        };
        assert!(matches(&select_field(), Some(&json!("high")), &still));
        let _ = named;
    }
}
