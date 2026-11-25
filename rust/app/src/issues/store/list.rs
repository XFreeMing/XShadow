use std::collections::{HashMap, hash_map::Entry};

use serde::{Deserialize, Serialize};
use serde_json::Value;
use sqlx::{PgPool, Postgres, QueryBuilder};

mod groups;
mod table;
pub use groups::{IssueTableGroupsQuery, table_groups};
pub use table::{IssueTableQuery, table_rows};

use super::{
    CallerActor, HiddenProjects, ISSUE_SELECT_COLUMNS, IssueReadError, IssueResponse,
    duplicate_issue_refs, issue_from_row, labels_by_issue, resolve_caller_access,
    resolve_custom_status, workspace_issue_prefix,
};

#[derive(Deserialize, Default)]
#[serde(default)]
pub struct IssueListQuery {
    open_only: bool,
    created_by_user_id: String,
    created_by_id: Option<String>,
    terminal_status_keys: Vec<String>,
    statuses: Vec<String>,
    category_status_keys: Option<Vec<String>>,
    priorities: Vec<String>,
    assignee_id: Option<String>,
    assignee_ids: Vec<String>,
    assignee_types: Vec<String>,
    scope_assignee: Option<ActorFilter>,
    assignee_filters: Vec<ActorFilter>,
    include_no_assignee: bool,
    creator_id: Option<String>,
    scope_creator: Option<ActorFilter>,
    creator_filters: Vec<ActorFilter>,
    project_id: Option<String>,
    project_ids: Vec<String>,
    project_statuses: Vec<String>,
    include_no_project: bool,
    label_ids: Vec<String>,
    ids: Option<Vec<String>>,
    working_only: bool,
    working_issue_ids: Option<Vec<String>>,
    unread_issue_ids: Option<Vec<String>>,
    top_level_only: bool,
    search_patterns: Vec<String>,
    search_number: Option<i32>,
    scheduled: bool,
    metadata: Option<Value>,
    properties: Vec<Vec<Value>>,
    date: Option<DateFilter>,
    involves_user_id: Option<String>,
    my_relations: Option<Relations>,
    relations: Option<Relations>,
    due_dates: Option<DueDates>,
    advanced: Option<Box<FilterNode>>,
    sort: String,
    property_sort: Option<PropertySort>,
    descending: bool,
    status_order: Vec<String>,
    limit: i64,
    offset: i64,
}

#[derive(Deserialize, Default)]
#[serde(default)]
struct FilterNode {
    match_any: bool,
    negate: bool,
    children: Vec<FilterNode>,
    query: Option<IssueListQuery>,
}

#[derive(Deserialize)]
struct Relations {
    user_id: String,
    kinds: Vec<RelationKind>,
}

#[derive(Deserialize)]
#[serde(rename_all = "snake_case")]
enum RelationKind {
    Assigned,
    Created,
    Subscribed,
    MyAgents,
}

#[derive(Deserialize)]
struct DueDates {
    today: String,
    kinds: Vec<DueDateKind>,
}

#[derive(Deserialize)]
#[serde(rename_all = "snake_case")]
enum DueDateKind {
    Overdue,
    Today,
    ThisWeek,
    #[serde(rename = "next_7_days")]
    Next7Days,
    None,
}

#[derive(Deserialize)]
struct PropertySort {
    id: String,
    kind: String,
    #[serde(default)]
    options: Vec<String>,
}

#[derive(Deserialize)]
struct ActorFilter {
    #[serde(rename = "type")]
    kind: String,
    id: String,
}

#[derive(Deserialize)]
struct DateFilter {
    column: DateColumn,
    start: String,
    end: String,
}

#[derive(Deserialize)]
#[serde(rename_all = "snake_case")]
enum DateColumn {
    CreatedAt,
    UpdatedAt,
}

#[derive(Serialize)]
pub struct IssueListResponse {
    created_by_user_id: String,
    issues: Vec<IssueResponse>,
    total: i64,
}

pub async fn list_issues(
    pool: &PgPool,
    workspace_id: &str,
    actor: &CallerActor,
    query: IssueListQuery,
) -> Result<IssueListResponse, IssueReadError> {
    let hidden = resolve_caller_access(pool, workspace_id, actor).await?;
    let hidden_ids: Vec<_> = hidden.ids.iter().cloned().collect();
    let prefix = workspace_issue_prefix(pool, workspace_id)
        .await?
        .unwrap_or_default();

    let mut page =
        QueryBuilder::<Postgres>::new(format!("SELECT {ISSUE_SELECT_COLUMNS} FROM issue i"));
    add_filters(&mut page, workspace_id, &hidden_ids, &query);
    if query.open_only {
        page.push(" ORDER BY i.position ASC, i.created_at DESC");
    } else {
        IssueSort::from_query(&query).append_order(&mut page);
        page.push(" LIMIT ")
            .push_bind(query.limit.clamp(1, 100))
            .push(" OFFSET ")
            .push_bind(query.offset.max(0));
    }
    let rows = page.build().fetch_all(pool).await.map_err(db_error)?;
    let total = if query.open_only {
        rows.len() as i64
    } else {
        let mut count = QueryBuilder::<Postgres>::new("SELECT COUNT(*) FROM issue i");
        add_filters(&mut count, workspace_id, &hidden_ids, &query);
        count
            .build_query_scalar()
            .fetch_one(pool)
            .await
            .map_err(db_error)?
    };

    let issues = project_rows(pool, workspace_id, &prefix, &hidden, &rows).await?;
    Ok(IssueListResponse {
        created_by_user_id: query.created_by_user_id,
        issues,
        total,
    })
}

async fn project_rows(
    pool: &PgPool,
    workspace_id: &str,
    prefix: &str,
    hidden: &HiddenProjects,
    rows: &[sqlx::postgres::PgRow],
) -> Result<Vec<IssueResponse>, IssueReadError> {
    let rows: Vec<_> = rows.iter().map(issue_from_row).collect::<Result<_, _>>()?;
    let issue_ids: Vec<_> = rows.iter().map(|row| row.id.clone()).collect();
    let original_ids: Vec<_> = rows
        .iter()
        .filter(|row| row.status == "cancelled")
        .filter_map(|row| row.duplicate_of_issue_id.clone())
        .collect();
    let mut labels = labels_by_issue(pool, workspace_id, &issue_ids).await;
    let originals = duplicate_issue_refs(pool, workspace_id, prefix, &original_ids, hidden).await;
    let mut statuses = HashMap::new();
    let mut issues = Vec::with_capacity(rows.len());
    for issue in rows {
        if let Entry::Vacant(entry) = statuses.entry(issue.status.clone()) {
            let resolved = resolve_custom_status(pool, workspace_id, &issue.status).await;
            entry.insert(resolved);
        }
        let (category, name) = &statuses[&issue.status];
        let duplicate = issue
            .duplicate_of_issue_id
            .as_deref()
            .filter(|_| issue.status == "cancelled")
            .and_then(|id| originals.get(id))
            .cloned();
        let mut response = issue.into_response(prefix);
        response.status_category = category.clone();
        response.status_name = name.clone();
        response.labels = labels.remove(&response.id).unwrap_or_default();
        response.duplicate_of = duplicate;
        issues.push(response);
    }
    Ok(issues)
}

fn db_error(_: sqlx::Error) -> IssueReadError {
    IssueReadError::DatabaseFailed
}

fn add_filters<'a>(
    sql: &mut QueryBuilder<'a, Postgres>,
    workspace_id: &'a str,
    hidden_ids: &'a [String],
    query: &'a IssueListQuery,
) {
    sql.push(" WHERE i.workspace_id = ")
        .push_bind(workspace_id)
        .push("::uuid");
    if !hidden_ids.is_empty() {
        sql.push(" AND (i.project_id IS NULL OR NOT (i.project_id = ANY(")
            .push_bind(hidden_ids)
            .push("::uuid[])))");
    }
    add_query_conditions(sql, query);
}

fn add_query_conditions<'a>(sql: &mut QueryBuilder<'a, Postgres>, query: &'a IssueListQuery) {
    if query.open_only {
        sql.push(" AND NOT (i.status = ANY(")
            .push_bind(&query.terminal_status_keys)
            .push("))");
    }
    if let Some(keys) = &query.category_status_keys {
        sql.push(" AND i.status = ANY(").push_bind(keys).push(")");
    }
    for (column, values) in [
        ("i.status", &query.statuses),
        ("i.priority", &query.priorities),
        ("i.assignee_type", &query.assignee_types),
    ] {
        if !values.is_empty() {
            sql.push(" AND ")
                .push(column)
                .push(" = ANY(")
                .push_bind(values)
                .push(")");
        }
    }
    for (column, value) in [
        ("i.assignee_id", &query.assignee_id),
        ("i.creator_id", &query.creator_id),
        ("i.project_id", &query.project_id),
    ] {
        if let Some(value) = value {
            sql.push(" AND ")
                .push(column)
                .push(" = ")
                .push_bind(value)
                .push("::uuid");
        }
    }
    if !query.assignee_ids.is_empty() {
        sql.push(" AND i.assignee_id = ANY(")
            .push_bind(&query.assignee_ids)
            .push("::uuid[])");
    }
    if let Some(creator) = &query.created_by_id {
        sql.push(" AND ");
        add_created_by_user(sql, creator);
    }
    add_actor_filter(
        sql,
        "assignee",
        &query.assignee_filters,
        query.include_no_assignee,
    );
    add_actor_filter(sql, "creator", &query.creator_filters, false);
    if let Some(actor) = &query.scope_assignee {
        add_actor_filter(sql, "assignee", std::slice::from_ref(actor), false);
    }
    if let Some(actor) = &query.scope_creator {
        add_actor_filter(sql, "creator", std::slice::from_ref(actor), false);
    }
    add_project_filter(sql, query);
    if !query.project_statuses.is_empty() {
        sql.push(" AND EXISTS (SELECT 1 FROM project p WHERE p.id = i.project_id AND p.workspace_id = i.workspace_id AND p.status = ANY(")
            .push_bind(&query.project_statuses)
            .push("))");
    }
    if !query.label_ids.is_empty() {
        sql.push(" AND EXISTS (SELECT 1 FROM issue_to_label l WHERE l.issue_id = i.id AND l.label_id = ANY(")
            .push_bind(&query.label_ids).push("::uuid[]))");
    }
    for ids in [
        &query.ids,
        &query.working_issue_ids,
        &query.unread_issue_ids,
    ]
    .into_iter()
    .flatten()
    {
        sql.push(" AND i.id = ANY(")
            .push_bind(ids)
            .push("::uuid[])");
    }
    if query.working_only {
        sql.push(" AND EXISTS (SELECT 1 FROM agent_task_queue atq WHERE atq.issue_id = i.id AND atq.status = 'running')");
    }
    if query.top_level_only {
        sql.push(" AND i.parent_issue_id IS NULL");
    }
    if query.scheduled {
        sql.push(" AND (i.start_date IS NOT NULL OR i.due_date IS NOT NULL)");
    }
    add_search_filter(sql, query);
    if let Some(metadata) = &query.metadata {
        sql.push(" AND i.metadata @> ")
            .push_bind(metadata.to_string())
            .push("::jsonb");
    }
    for group in &query.properties {
        sql.push(" AND (");
        for (index, alternative) in group.iter().enumerate() {
            if index != 0 {
                sql.push(" OR ");
            }
            add_property_filter(sql, alternative);
        }
        sql.push(")");
    }
    if let Some(date) = &query.date {
        let column = match date.column {
            DateColumn::CreatedAt => "i.created_at",
            DateColumn::UpdatedAt => "i.updated_at",
        };
        sql.push(" AND ")
            .push(column)
            .push(" >= ")
            .push_bind(&date.start)
            .push("::timestamptz")
            .push(" AND ")
            .push(column)
            .push(" < ")
            .push_bind(&date.end)
            .push("::timestamptz");
    }
    if let Some(user) = &query.involves_user_id {
        sql.push(" AND ");
        add_involves_user_filter(sql, user);
    }
    for relations in [&query.my_relations, &query.relations]
        .into_iter()
        .flatten()
    {
        add_relations(sql, relations);
    }
    if let Some(due_dates) = &query.due_dates {
        add_due_dates(sql, due_dates);
    }
    if let Some(node) = &query.advanced {
        sql.push(" AND ");
        add_filter_node(sql, node);
    }
}

fn add_filter_node<'a>(sql: &mut QueryBuilder<'a, Postgres>, node: &'a FilterNode) {
    sql.push("(");
    if let Some(query) = &node.query {
        sql.push("TRUE");
        add_query_conditions(sql, query);
    } else {
        let separator = if node.match_any { " OR " } else { " AND " };
        for (index, child) in node.children.iter().enumerate() {
            if index != 0 {
                sql.push(separator);
            }
            add_filter_node(sql, child);
        }
    }
    sql.push(")");
    if node.negate {
        sql.push(" IS NOT TRUE");
    }
}

fn add_created_by_user<'a>(sql: &mut QueryBuilder<'a, Postgres>, user: &'a str) {
    sql.push("((i.creator_type = 'member' AND i.creator_id = ")
        .push_bind(user)
        .push("::uuid) OR i.requester_user_id = ")
        .push_bind(user)
        .push("::uuid)");
}

fn add_relations<'a>(sql: &mut QueryBuilder<'a, Postgres>, relations: &'a Relations) {
    sql.push(" AND (");
    let user = &relations.user_id;
    for (index, kind) in relations.kinds.iter().enumerate() {
        if index != 0 {
            sql.push(" OR ");
        }
        match kind {
            RelationKind::Assigned => {
                sql.push("(i.assignee_type = 'member' AND i.assignee_id = ")
                    .push_bind(user)
                    .push("::uuid)");
            }
            RelationKind::Created => add_created_by_user(sql, user),
            RelationKind::Subscribed => {
                sql.push("EXISTS (SELECT 1 FROM issue_subscriber s WHERE s.issue_id = i.id AND s.user_type = 'member' AND s.user_id = ")
                    .push_bind(user)
                    .push("::uuid AND s.unsubscribed_at IS NULL)");
            }
            RelationKind::MyAgents => add_involves_user_filter(sql, user),
        }
    }
    sql.push(")");
}

fn add_due_dates<'a>(sql: &mut QueryBuilder<'a, Postgres>, dates: &'a DueDates) {
    sql.push(" AND (");
    for (index, kind) in dates.kinds.iter().enumerate() {
        if index != 0 {
            sql.push(" OR ");
        }
        match kind {
            DueDateKind::Overdue => {
                sql.push("i.due_date < ")
                    .push_bind(&dates.today)
                    .push("::date");
            }
            DueDateKind::Today => {
                sql.push("i.due_date = ")
                    .push_bind(&dates.today)
                    .push("::date");
            }
            DueDateKind::ThisWeek => {
                sql.push("(i.due_date >= date_trunc('week', ")
                    .push_bind(&dates.today)
                    .push("::date::timestamp)::date AND i.due_date < date_trunc('week', ")
                    .push_bind(&dates.today)
                    .push("::date::timestamp)::date + 7)");
            }
            DueDateKind::Next7Days => {
                sql.push("(i.due_date >= ")
                    .push_bind(&dates.today)
                    .push("::date AND i.due_date < ")
                    .push_bind(&dates.today)
                    .push("::date + 7)");
            }
            DueDateKind::None => {
                sql.push("i.due_date IS NULL");
            }
        }
    }
    sql.push(")");
}

fn add_actor_filter<'a>(
    sql: &mut QueryBuilder<'a, Postgres>,
    role: &str,
    actors: &'a [ActorFilter],
    include_unassigned: bool,
) {
    if actors.is_empty() && !include_unassigned {
        return;
    }
    sql.push(" AND (");
    let mut items = sql.separated(" OR ");
    if include_unassigned {
        items.push("(i.assignee_type IS NULL AND i.assignee_id IS NULL)");
    }
    for actor in actors {
        items
            .push("(i.")
            .push_unseparated(role)
            .push_unseparated("_type = ")
            .push_bind_unseparated(&actor.kind)
            .push_unseparated(" AND i.")
            .push_unseparated(role)
            .push_unseparated("_id = ")
            .push_bind_unseparated(&actor.id)
            .push_unseparated("::uuid)");
    }
    items.push_unseparated(")");
}

fn add_project_filter<'a>(sql: &mut QueryBuilder<'a, Postgres>, query: &'a IssueListQuery) {
    if query.project_ids.is_empty() && !query.include_no_project {
        return;
    }
    sql.push(" AND (");
    let mut items = sql.separated(" OR ");
    if query.include_no_project {
        items.push("i.project_id IS NULL");
    }
    if !query.project_ids.is_empty() {
        items
            .push("i.project_id = ANY(")
            .push_bind_unseparated(&query.project_ids)
            .push_unseparated("::uuid[])");
    }
    items.push_unseparated(")");
}

fn add_search_filter<'a>(sql: &mut QueryBuilder<'a, Postgres>, query: &'a IssueListQuery) {
    if query.search_number.is_none() && query.search_patterns.is_empty() {
        return;
    }
    sql.push(" AND (");
    if !query.search_patterns.is_empty() {
        sql.push("(");
        let mut words = sql.separated(" AND ");
        for pattern in &query.search_patterns {
            words
                .push("LOWER(i.title) LIKE ")
                .push_bind_unseparated(pattern);
        }
        words.push_unseparated(")");
    }
    if let Some(number) = query.search_number {
        if !query.search_patterns.is_empty() {
            sql.push(" OR ");
        }
        sql.push("i.number = ").push_bind(number);
    }
    sql.push(")");
}

fn add_involves_user_filter<'a>(sql: &mut QueryBuilder<'a, Postgres>, user: &'a str) {
    sql.push("EXISTS (SELECT 1 FROM (SELECT ")
        .push_bind(user)
        .push(
            "::uuid AS id) p WHERE
        (i.assignee_type = 'agent' AND EXISTS (
            SELECT 1 FROM agent a WHERE a.id = i.assignee_id
              AND a.workspace_id = i.workspace_id AND a.owner_id = p.id
        ))
        OR (i.assignee_type = 'squad' AND EXISTS (
            SELECT 1 FROM squad s WHERE s.id = i.assignee_id AND s.workspace_id = i.workspace_id
            AND (
                EXISTS (SELECT 1 FROM squad_member m WHERE m.squad_id = s.id
                    AND m.member_type = 'member' AND m.member_id = p.id)
                OR EXISTS (SELECT 1 FROM agent a WHERE a.id = s.leader_id
                    AND a.workspace_id = i.workspace_id AND a.owner_id = p.id)
                OR EXISTS (SELECT 1 FROM squad_member m JOIN agent a ON a.id = m.member_id
                    WHERE m.squad_id = s.id AND m.member_type = 'agent'
                    AND a.workspace_id = i.workspace_id AND a.owner_id = p.id)
            )
        )))",
        );
}

fn add_property_filter<'a>(sql: &mut QueryBuilder<'a, Postgres>, pattern: &'a Value) {
    if let Some(definition) = pattern.get("__none__").and_then(Value::as_str) {
        sql.push("NOT (i.properties ? ")
            .push_bind(definition)
            .push(")");
        return;
    }
    let Some(operator) = pattern.get("__op__").and_then(Value::as_str) else {
        sql.push("i.properties @> ")
            .push_bind(pattern.to_string())
            .push("::jsonb");
        return;
    };
    let (Some(definition), Some(value)) = (
        pattern.get("def").and_then(Value::as_str),
        pattern.get("value").and_then(Value::as_str),
    ) else {
        sql.push("FALSE");
        return;
    };
    match operator {
        "contains" => {
            sql.push("(jsonb_typeof(i.properties -> ")
                .push_bind(definition)
                .push(") = 'string' AND (i.properties ->> ")
                .push_bind(definition)
                .push(") ILIKE ")
                .push_bind(format!("%{value}%"))
                .push(")");
        }
        "gt" | "gte" | "lt" | "lte" => {
            let cmp = match operator {
                "gt" => " > ",
                "gte" => " >= ",
                "lt" => " < ",
                _ => " <= ",
            };
            sql.push("CASE WHEN jsonb_typeof(i.properties -> ")
                .push_bind(definition)
                .push(") = 'number' THEN (i.properties ->> ")
                .push_bind(definition)
                .push(")::numeric END")
                .push(cmp)
                .push_bind(value)
                .push("::numeric");
        }
        "before" | "after" => {
            let cmp = if operator == "before" { " < " } else { " > " };
            sql.push("(jsonb_typeof(i.properties -> ")
                .push_bind(definition)
                .push(") = 'string' AND (i.properties ->> ")
                .push_bind(definition)
                .push(")")
                .push(cmp)
                .push_bind(value)
                .push(")");
        }
        _ => {
            sql.push("FALSE");
        }
    }
}

enum IssueSortKey<'a> {
    Position,
    Column(&'static str),
    Priority,
    Status(&'a [String]),
    Property(&'a PropertySort),
}

struct IssueSort<'a> {
    key: IssueSortKey<'a>,
    cast: &'static str,
    descending: bool,
    nulls_last: bool,
    id_only_tie: bool,
}

impl<'a> IssueSort<'a> {
    fn from_query(query: &'a IssueListQuery) -> Self {
        use IssueSortKey::{Column, Position, Priority, Property, Status};
        let (key, cast, nulls_last, id_only_tie) = match query.sort.as_str() {
            "title" => (Column("i.title"), "text", false, false),
            "created_at" => (Column("i.created_at"), "timestamptz", false, false),
            "updated_at" => (Column("i.updated_at"), "timestamptz", false, false),
            "start_date" => (Column("i.start_date"), "date", true, false),
            "due_date" => (Column("i.due_date"), "date", true, false),
            "last_activity_at" => (Column("i.last_activity_at"), "timestamptz", true, true),
            "priority" => (Priority, "integer", false, false),
            "status" => (Status(&query.status_order), "integer", false, false),
            "property" => match query.property_sort.as_ref() {
                Some(property)
                    if property.kind == "number"
                        || (property.kind == "select" && !property.options.is_empty()) =>
                {
                    (Property(property), "numeric", true, false)
                }
                Some(property) if matches!(property.kind.as_str(), "date" | "text" | "url") => {
                    (Property(property), "text", true, false)
                }
                _ => (Position, "double precision", false, false),
            },
            _ => (Position, "double precision", false, false),
        };
        let descending = query.descending && !matches!(key, Position);
        Self {
            key,
            cast,
            descending,
            nulls_last,
            id_only_tie,
        }
    }

    fn append_key(&self, sql: &mut QueryBuilder<'a, Postgres>) {
        match self.key {
            IssueSortKey::Position => {
                sql.push("i.position");
            }
            IssueSortKey::Column(column) => {
                sql.push(column);
            }
            IssueSortKey::Priority => {
                sql.push("CASE i.priority WHEN 'urgent' THEN 0 WHEN 'high' THEN 1 WHEN 'medium' THEN 2 WHEN 'low' THEN 3 ELSE 4 END");
            }
            IssueSortKey::Status(order) => {
                sql.push("COALESCE(array_position(")
                    .push_bind(order)
                    .push("::text[], i.status), 100000)");
            }
            IssueSortKey::Property(property) => match property.kind.as_str() {
                "number" => {
                    sql.push("CASE WHEN jsonb_typeof(i.properties -> ")
                        .push_bind(&property.id)
                        .push(") = 'number' THEN (i.properties ->> ")
                        .push_bind(&property.id)
                        .push(")::numeric END");
                }
                "select" => {
                    // Public cursors store the original zero-based sort key.
                    sql.push("(array_position(")
                        .push_bind(&property.options)
                        .push("::text[], i.properties ->> ")
                        .push_bind(&property.id)
                        .push(") - 1)");
                }
                _ => {
                    sql.push("NULLIF(i.properties ->> ")
                        .push_bind(&property.id)
                        .push(", '')");
                }
            },
        }
    }

    fn append_order(&self, sql: &mut QueryBuilder<'a, Postgres>) {
        sql.push(" ORDER BY ");
        self.append_key(sql);
        sql.push(if self.descending { " DESC" } else { " ASC" });
        if self.nulls_last {
            sql.push(" NULLS LAST");
        }
        if !self.id_only_tie {
            sql.push(", i.created_at DESC");
        }
        sql.push(", i.id DESC");
    }
}
