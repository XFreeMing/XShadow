use chrono::DateTime;
use serde::{Deserialize, Serialize};
use sqlx::{PgPool, Postgres, QueryBuilder, Row};

use super::{
    ActorFilter, IssueListQuery, IssueSort, IssueSortKey, add_actor_filter, add_filters, db_error,
    project_rows,
};
use crate::issues::store::{
    CallerActor, ISSUE_SELECT_COLUMNS, IssueReadError, IssueResponse, format_timestamp,
    resolve_caller_access_on, workspace_issue_prefix,
};

#[derive(Deserialize)]
pub struct IssueTableQuery {
    query: IssueListQuery,
    hierarchy: bool,
    parent_id: Option<String>,
    cursor: Option<TablePosition>,
    cursor_time_zone: Option<String>,
    group: Option<Group>,
}

#[derive(Deserialize, Default)]
#[serde(default)]
struct Group {
    statuses: Option<Vec<String>>,
    assignee: Option<ActorFilter>,
    unassigned: bool,
    project_id: Option<String>,
    no_project: bool,
    parent_id: Option<String>,
    no_parent: bool,
    property: Option<GroupProperty>,
    promoted_parent_statuses: Option<Vec<String>>,
}

#[derive(Deserialize)]
struct GroupProperty {
    id: String,
    state: GroupPropertyState,
    json_type: String,
    value: String,
}

#[derive(Deserialize)]
#[serde(rename_all = "snake_case")]
enum GroupPropertyState {
    Unset,
    Value,
    Unavailable,
}

#[derive(Deserialize, Serialize)]
struct TablePosition {
    row_id: String,
    created_at: String,
    sort_value: Option<String>,
}

#[derive(Serialize)]
struct TableRow {
    issue: IssueResponse,
    direct_child_count: i64,
}

#[derive(Serialize)]
pub struct IssueTablePage {
    rows: Vec<TableRow>,
    total: i64,
    next: Option<TablePosition>,
}

pub async fn table_rows(
    pool: &PgPool,
    workspace_id: &str,
    actor: &CallerActor,
    request: IssueTableQuery,
) -> Result<IssueTablePage, IssueReadError> {
    let mut tx = pool.begin().await.map_err(db_error)?;
    sqlx::query("SET TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY")
        .execute(&mut *tx)
        .await
        .map_err(db_error)?;
    if let Some(time_zone) = &request.cursor_time_zone {
        // Go's public cursor retains the database rendering of timestamptz.
        sqlx::query("SELECT set_config('TimeZone', $1, true)")
            .bind(time_zone)
            .execute(&mut *tx)
            .await
            .map_err(db_error)?;
    }
    let hidden = resolve_caller_access_on(&mut tx, workspace_id, actor).await?;
    let hidden_ids: Vec<_> = hidden.ids.iter().cloned().collect();
    let query = &request.query;
    let sort = IssueSort::from_query(query);
    let limit = query.limit.clamp(1, 100);

    let mut page = QueryBuilder::<Postgres>::new(
        "WITH membership AS NOT MATERIALIZED (SELECT i.* FROM issue i",
    );
    add_filters(&mut page, workspace_id, &hidden_ids, query);
    if let Some(group) = &request.group
        && request.hierarchy
    {
        append_group(&mut page, group);
    }
    page.push(")");
    let promotion = request
        .group
        .as_ref()
        .and_then(|group| group.promoted_parent_statuses.as_deref());
    if let Some(statuses) = promotion {
        page.push(", promoted_parents AS (SELECT DISTINCT child.parent_issue_id AS id FROM membership child WHERE child.parent_issue_id IS NOT NULL AND child.status = ANY(")
            .push_bind(statuses)
            .push("::text[]))");
    }
    page.push(", page AS MATERIALIZED (SELECT i.*, (");
    sort.append_key(&mut page);
    page.push(")::text AS table_sort_value FROM membership i WHERE TRUE");
    if request.hierarchy {
        if let Some(parent) = &request.parent_id {
            page.push(" AND i.parent_issue_id = ")
                .push_bind(parent)
                .push("::uuid AND EXISTS (SELECT 1 FROM membership p WHERE p.id = ")
                .push_bind(parent)
                .push("::uuid)");
        } else {
            page.push(" AND (i.parent_issue_id IS NULL OR (SELECT p.id FROM membership p WHERE p.id = i.parent_issue_id) IS NULL)");
        }
    } else if let Some(group) = &request.group {
        append_group(&mut page, group);
        if promotion.is_some() {
            page.push(" AND NOT (i.parent_issue_id IS NULL AND EXISTS (SELECT 1 FROM promoted_parents p WHERE p.id = i.id))");
        }
    }
    if let Some(cursor) = &request.cursor {
        append_cursor(&mut page, &sort, cursor);
    }
    sort.append_order(&mut page);
    page.push(" LIMIT ").push_bind(limit + 1).push(") SELECT ")
        .push(ISSUE_SELECT_COLUMNS)
        .push(", table_sort_value, to_char(i.created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS.US\"Z\"') AS cursor_created_at, ");
    if request.hierarchy {
        page.push("(SELECT count(*) FROM membership child WHERE child.parent_issue_id = i.id)");
    } else {
        page.push("0::bigint");
    }
    page.push(" AS child_count FROM page i");
    sort.append_order(&mut page);
    let mut rows = page.build().fetch_all(&mut *tx).await.map_err(db_error)?;
    let total =
        if request.cursor.is_none() && request.parent_id.is_none() && request.group.is_none() {
            let mut count = QueryBuilder::<Postgres>::new("SELECT count(*) FROM issue i");
            add_filters(&mut count, workspace_id, &hidden_ids, query);
            count
                .build_query_scalar()
                .fetch_one(&mut *tx)
                .await
                .map_err(db_error)?
        } else {
            0
        };
    tx.commit().await.map_err(db_error)?;

    let next = if rows.len() > limit as usize {
        rows.truncate(limit as usize);
        rows.last().map(table_position).transpose()?
    } else {
        None
    };
    let prefix = workspace_issue_prefix(pool, workspace_id)
        .await?
        .unwrap_or_default();
    let issues = project_rows(pool, workspace_id, &prefix, &hidden, &rows).await?;
    let rows = issues
        .into_iter()
        .zip(rows)
        .map(|(issue, row)| {
            Ok(TableRow {
                issue,
                direct_child_count: row.try_get("child_count").map_err(db_error)?,
            })
        })
        .collect::<Result<_, IssueReadError>>()?;
    Ok(IssueTablePage { rows, total, next })
}

fn append_group<'a>(sql: &mut QueryBuilder<'a, Postgres>, group: &'a Group) {
    if let Some(statuses) = &group.statuses {
        sql.push(" AND i.status = ANY(")
            .push_bind(statuses)
            .push("::text[])");
    }
    add_actor_filter(sql, "assignee", group.assignee.as_slice(), group.unassigned);
    for (column, id, empty) in [
        ("i.project_id", &group.project_id, group.no_project),
        ("i.parent_issue_id", &group.parent_id, group.no_parent),
    ] {
        if let Some(id) = id {
            sql.push(" AND ")
                .push(column)
                .push(" = ")
                .push_bind(id)
                .push("::uuid");
        } else if empty {
            sql.push(" AND ").push(column).push(" IS NULL");
        }
    }
    if let Some(property) = &group.property {
        match property.state {
            GroupPropertyState::Unset => {
                sql.push(" AND NOT (i.properties ? ")
                    .push_bind(&property.id)
                    .push(")");
            }
            GroupPropertyState::Value => {
                sql.push(" AND jsonb_typeof(i.properties -> ")
                    .push_bind(&property.id)
                    .push(") = ")
                    .push_bind(&property.json_type)
                    .push(" AND i.properties ->> ")
                    .push_bind(&property.id)
                    .push(" = ")
                    .push_bind(&property.value);
            }
            GroupPropertyState::Unavailable => {
                sql.push(" AND i.properties ? ")
                    .push_bind(&property.id)
                    .push(" AND jsonb_typeof(i.properties -> ")
                    .push_bind(&property.id)
                    .push(") <> ")
                    .push_bind(&property.json_type);
            }
        }
    }
}

fn table_position(row: &sqlx::postgres::PgRow) -> Result<TablePosition, IssueReadError> {
    let created_at: String = row.try_get("cursor_created_at").map_err(db_error)?;
    let created_at =
        DateTime::parse_from_rfc3339(&created_at).map_err(|_| IssueReadError::DatabaseFailed)?;
    Ok(TablePosition {
        row_id: row.try_get(0).map_err(db_error)?,
        created_at: format_timestamp(created_at, true),
        sort_value: row.try_get("table_sort_value").map_err(db_error)?,
    })
}

fn append_cursor<'a>(
    sql: &mut QueryBuilder<'a, Postgres>,
    sort: &IssueSort<'a>,
    cursor: &'a TablePosition,
) {
    if let Some(value) = &cursor.sort_value {
        if matches!(sort.key, IssueSortKey::Position) {
            sql.push(" AND i.position >= ")
                .push_bind(value)
                .push("::double precision");
        }
        sql.push(" AND (");
        if sort.nulls_last {
            sort.append_key(sql);
            sql.push(" IS NULL OR ");
        }
        sort.append_key(sql);
        sql.push(if sort.descending { " < " } else { " > " });
        sql.push_bind(value)
            .push("::")
            .push(sort.cast)
            .push(" OR (");
        sort.append_key(sql);
        sql.push(" = ")
            .push_bind(value)
            .push("::")
            .push(sort.cast)
            .push(" AND ");
        append_tie(sql, sort, cursor);
        sql.push("))");
    } else {
        sql.push(" AND (");
        sort.append_key(sql);
        sql.push(" IS NULL AND ");
        append_tie(sql, sort, cursor);
        sql.push(")");
    }
}

fn append_tie<'a>(
    sql: &mut QueryBuilder<'a, Postgres>,
    sort: &IssueSort<'a>,
    cursor: &'a TablePosition,
) {
    if sort.id_only_tie {
        sql.push("i.id < ").push_bind(&cursor.row_id).push("::uuid");
    } else {
        sql.push("(i.created_at < ")
            .push_bind(&cursor.created_at)
            .push("::timestamptz OR (i.created_at = ")
            .push_bind(&cursor.created_at)
            .push("::timestamptz AND i.id < ")
            .push_bind(&cursor.row_id)
            .push("::uuid))");
    }
}
