use std::collections::BTreeMap;

use serde::{Deserialize, Serialize};
use serde_json::Value;
use sqlx::{PgPool, Postgres, QueryBuilder, Row};

use super::{IssueListQuery, add_filters, db_error};
use crate::issues::store::{CallerActor, IssueReadError, resolve_caller_access_on};

#[derive(Deserialize)]
pub struct IssueTableGroupsQuery {
    query: IssueListQuery,
    dimension: GroupDimension,
    secondary: Option<Secondary>,
    include_empty: bool,
    limit: i64,
    cursor: Option<GroupPosition>,
}

#[derive(Deserialize)]
#[serde(tag = "kind", rename_all = "snake_case")]
enum GroupDimension {
    Status {
        order: Vec<String>,
    },
    StatusCategory {
        order: Vec<String>,
        categories: Categories,
        lifecycle: bool,
    },
    Assignee,
    Project,
    Parent {
        prefix: String,
    },
    SelectProperty {
        id: String,
        options: Vec<String>,
    },
    CheckboxProperty {
        id: String,
    },
}

type Categories = BTreeMap<String, String>;

#[derive(Deserialize)]
struct Secondary {
    categories: Option<Categories>,
    visible: Option<Vec<String>>,
    promoted_parent_statuses: Option<Vec<String>>,
}

#[derive(Deserialize)]
struct GroupPosition {
    order: i32,
    sort: String,
    value: String,
}

#[derive(Serialize)]
struct GroupRow {
    value: String,
    count: i64,
    secondary_counts: Value,
    sort: String,
    context: Value,
    order: i32,
}

#[derive(Serialize)]
pub struct IssueTableGroupsPage {
    rows: Vec<GroupRow>,
    total: i64,
}

pub async fn table_groups(
    pool: &PgPool,
    workspace_id: &str,
    actor: &CallerActor,
    input: IssueTableGroupsQuery,
) -> Result<IssueTableGroupsPage, IssueReadError> {
    let mut tx = pool.begin().await.map_err(db_error)?;
    sqlx::query("SET TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY")
        .execute(&mut *tx)
        .await
        .map_err(db_error)?;
    let hidden = resolve_caller_access_on(&mut tx, workspace_id, actor).await?;
    let hidden_ids: Vec<_> = hidden.ids.into_iter().collect();
    let mut sql = QueryBuilder::<Postgres>::new(
        "WITH membership AS NOT MATERIALIZED (SELECT i.* FROM issue i",
    );
    add_filters(&mut sql, workspace_id, &hidden_ids, &input.query);
    sql.push(")");
    append_grouping(
        &mut sql,
        &input.dimension,
        input.secondary.as_ref(),
        input.include_empty,
    );

    sql.push(", ranked AS (SELECT group_value, issue_count, secondary_counts, ");
    input.dimension.append_order(&mut sql);
    sql.push(" AS group_order, ");
    input.dimension.append_sort(&mut sql, workspace_id);
    sql.push(" AS group_sort, ");
    input.dimension.append_context(&mut sql);
    sql.push(" AS group_context, SUM(visible_count) OVER ()::bigint AS total FROM grouped");
    if matches!(input.dimension, GroupDimension::Parent { .. }) {
        sql.push(
            " LEFT JOIN issue p ON p.id = NULLIF(group_value, '__no_parent__')::uuid
                   AND p.workspace_id = ",
        )
        .push_bind(workspace_id)
        .push("::uuid");
        if !hidden_ids.is_empty() {
            sql.push(" AND (p.project_id IS NULL OR NOT (p.project_id = ANY(")
                .push_bind(&hidden_ids)
                .push("::uuid[])))");
        }
    }
    sql.push(
        ") SELECT group_value, issue_count, secondary_counts::text, group_order, group_sort,
              group_context::text, total FROM ranked",
    );
    if let Some(cursor) = &input.cursor {
        sql.push(" WHERE (group_order, group_sort, group_value) > (")
            .push_bind(cursor.order)
            .push("::int, ")
            .push_bind(&cursor.sort)
            .push("::text, ")
            .push_bind(&cursor.value)
            .push("::text)");
    }
    sql.push(" ORDER BY group_order, group_sort, group_value LIMIT ")
        .push_bind(input.limit.clamp(1, 100) + 1);
    let raw = sql.build().fetch_all(&mut *tx).await.map_err(db_error)?;
    tx.commit().await.map_err(db_error)?;
    let mut page = IssueTableGroupsPage {
        rows: Vec::with_capacity(raw.len()),
        total: 0,
    };
    for row in raw {
        let secondary: String = row.try_get(2).map_err(db_error)?;
        let context: String = row.try_get(5).map_err(db_error)?;
        page.total = row.try_get(6).map_err(db_error)?;
        page.rows.push(GroupRow {
            value: row.try_get(0).map_err(db_error)?,
            count: row.try_get(1).map_err(db_error)?,
            secondary_counts: serde_json::from_str(&secondary)
                .map_err(|_| IssueReadError::DatabaseFailed)?,
            order: row.try_get(3).map_err(db_error)?,
            sort: row.try_get(4).map_err(db_error)?,
            context: serde_json::from_str(&context).map_err(|_| IssueReadError::DatabaseFailed)?,
        });
    }
    Ok(page)
}

fn append_grouping<'q>(
    sql: &mut QueryBuilder<'q, Postgres>,
    dimension: &'q GroupDimension,
    secondary: Option<&'q Secondary>,
    include_empty: bool,
) {
    if let Some(secondary) = secondary {
        let promoted = secondary.promoted_parent_statuses.as_ref();
        if let Some(statuses) = promoted {
            sql.push(
                ", promoted_parents AS (
                SELECT DISTINCT child.parent_issue_id AS id FROM membership child
                WHERE child.parent_issue_id IS NOT NULL AND child.status = ANY(",
            )
            .push_bind(statuses)
            .push("::text[]))");
        }
        sql.push(", cells AS (SELECT ");
        dimension.append_value(sql);
        sql.push(" AS group_value, ");
        append_status(sql, secondary.categories.as_ref());
        sql.push(" AS secondary_value, COUNT(*)");
        if promoted.is_some() {
            sql.push(
                " FILTER (WHERE NOT (i.parent_issue_id IS NULL AND
                           EXISTS (SELECT 1 FROM promoted_parents p WHERE p.id = i.id)))",
            );
        }
        sql.push(
            " AS cell_count FROM membership i GROUP BY 1, 2),
                  grouped AS (SELECT group_value, SUM(cell_count)::bigint AS issue_count, ",
        );
        append_visible_count(sql, secondary.visible.as_deref());
        sql.push("::bigint AS visible_count, jsonb_object_agg(secondary_value, cell_count) AS secondary_counts
                  FROM cells GROUP BY group_value");
        if let Some(visible) = &secondary.visible {
            sql.push(" HAVING ");
            append_visible_count(sql, Some(visible));
            sql.push(" > 0");
        }
        sql.push(")");
        return;
    }

    sql.push(", actual AS (SELECT ");
    dimension.append_value(sql);
    sql.push(" AS group_value, COUNT(*) AS issue_count FROM membership i GROUP BY 1)");
    let expected = if include_empty {
        dimension.expected_values()
    } else {
        None
    };
    if let Some(expected) = expected {
        sql.push(", expected AS (SELECT unnest(")
            .push_bind(expected)
            .push("::text[]) AS group_value),
            grouped AS (
              SELECT e.group_value, COALESCE(a.issue_count, 0)::bigint AS issue_count,
                     COALESCE(a.issue_count, 0)::bigint AS visible_count, '{}'::jsonb AS secondary_counts
              FROM expected e LEFT JOIN actual a USING (group_value)
              UNION ALL
              SELECT a.group_value, a.issue_count, a.issue_count, '{}'::jsonb
              FROM actual a WHERE NOT EXISTS (SELECT 1 FROM expected e WHERE e.group_value = a.group_value)
            )");
    } else {
        sql.push(
            ", grouped AS (SELECT group_value, issue_count, issue_count AS visible_count,
                                  '{}'::jsonb AS secondary_counts FROM actual)",
        );
    }
}

fn append_visible_count<'q>(sql: &mut QueryBuilder<'q, Postgres>, visible: Option<&'q [String]>) {
    if let Some(visible) = visible {
        sql.push("COALESCE(SUM(cell_count) FILTER (WHERE secondary_value = ANY(")
            .push_bind(visible)
            .push("::text[])), 0)");
    } else {
        sql.push("SUM(cell_count)");
    }
}

fn append_status(sql: &mut QueryBuilder<'_, Postgres>, categories: Option<&Categories>) {
    if let Some(categories) = categories {
        sql.push("COALESCE(")
            .push_bind(serde_json::json!(categories).to_string())
            .push("::jsonb ->> i.status, i.status)");
    } else {
        sql.push("i.status");
    }
}

impl GroupDimension {
    fn append_value<'q>(&'q self, sql: &mut QueryBuilder<'q, Postgres>) {
        match self {
            Self::Status { .. } => append_status(sql, None),
            Self::StatusCategory { categories, .. } => append_status(sql, Some(categories)),
            Self::Assignee => {
                sql.push("CASE WHEN i.assignee_type IS NULL OR i.assignee_id IS NULL THEN '__unassigned__' ELSE i.assignee_type || ':' || i.assignee_id::text END");
            }
            Self::Project => {
                sql.push("COALESCE(i.project_id::text, '__no_project__')");
            }
            Self::Parent { .. } => {
                sql.push("COALESCE(i.parent_issue_id::text, '__no_parent__')");
            }
            Self::SelectProperty { id, options } => {
                sql.push("CASE WHEN NOT (i.properties ? ")
                    .push_bind(id)
                    .push("::text) THEN 'unset:' WHEN jsonb_typeof(i.properties -> ")
                    .push_bind(id)
                    .push("::text) = 'string' THEN (CASE WHEN i.properties ->> ")
                    .push_bind(id)
                    .push("::text = ANY(")
                    .push_bind(options)
                    .push("::text[]) THEN 'value:' ELSE 'unavailable:' END) || (i.properties ->> ")
                    .push_bind(id)
                    .push("::text) ELSE 'unavailable:' END");
            }
            Self::CheckboxProperty { id } => {
                sql.push("CASE WHEN NOT (i.properties ? ")
                    .push_bind(id)
                    .push("::text) THEN 'unset:' WHEN jsonb_typeof(i.properties -> ")
                    .push_bind(id)
                    .push("::text) = 'boolean' THEN 'value:' || (i.properties ->> ")
                    .push_bind(id)
                    .push("::text) ELSE 'unavailable:' END");
            }
        }
    }

    fn append_sort<'q>(&'q self, sql: &mut QueryBuilder<'q, Postgres>, workspace_id: &'q str) {
        match self {
            Self::Assignee => {
                sql.push("LOWER(COALESCE(CASE split_part(group_value, ':', 1)
                           WHEN 'member' THEN (SELECT u.name FROM \"user\" u WHERE u.id = split_part(group_value, ':', 2)::uuid)
                           WHEN 'agent' THEN (SELECT a.name FROM agent a WHERE a.workspace_id = ")
                    .push_bind(workspace_id)
                    .push("::uuid AND a.id = split_part(group_value, ':', 2)::uuid)
                           WHEN 'squad' THEN (SELECT s.name FROM squad s WHERE s.workspace_id = ")
                    .push_bind(workspace_id)
                    .push("::uuid AND s.id = split_part(group_value, ':', 2)::uuid) END, ''))");
            }
            Self::Project => {
                sql.push("LOWER(COALESCE((SELECT p.title FROM project p WHERE p.workspace_id = ")
                    .push_bind(workspace_id)
                    .push("::uuid AND p.id = NULLIF(group_value, '__no_project__')::uuid), ''))");
            }
            Self::Parent { .. } => {
                sql.push("LOWER(COALESCE(p.title, ''))");
            }
            _ => {
                sql.push("group_value");
            }
        }
    }

    fn append_order<'q>(&'q self, sql: &mut QueryBuilder<'q, Postgres>) {
        match self {
            Self::StatusCategory {
                lifecycle: true, ..
            } => {
                sql.push("CASE group_value WHEN 'unstarted' THEN 0 WHEN 'started' THEN 1 WHEN 'done' THEN 2 WHEN 'closed' THEN 3 ELSE 4 END");
            }
            Self::Status { order } | Self::StatusCategory { order, .. } => {
                sql.push("COALESCE(array_position(")
                    .push_bind(order)
                    .push("::text[], group_value), 100000)");
            }
            Self::Assignee => {
                sql.push("CASE split_part(group_value, ':', 1) WHEN 'member' THEN 0 WHEN 'agent' THEN 1 WHEN 'squad' THEN 2 ELSE 3 END");
            }
            Self::Project => {
                sql.push("CASE WHEN group_value = '__no_project__' THEN 0 ELSE 1 END");
            }
            Self::Parent { .. } => {
                sql.push("CASE WHEN group_value = '__no_parent__' THEN 0 ELSE 1 END");
            }
            Self::SelectProperty { options, .. } => {
                let ordered_values = options
                    .iter()
                    .map(|value| format!("value:{value}"))
                    .collect::<Vec<_>>();
                sql.push("CASE WHEN group_value LIKE 'value:%' THEN COALESCE(array_position(")
                    .push_bind(ordered_values)
                    .push("::text[], group_value), 100000) WHEN group_value LIKE 'unavailable:%' THEN 100001 ELSE 100002 END");
            }
            Self::CheckboxProperty { .. } => {
                sql.push("CASE group_value WHEN 'value:false' THEN 0 WHEN 'value:true' THEN 1 WHEN 'unavailable:' THEN 2 ELSE 3 END");
            }
        }
    }

    fn append_context<'q>(&'q self, sql: &mut QueryBuilder<'q, Postgres>) {
        if let Self::Parent { prefix } = self {
            sql.push(
                "CASE WHEN p.id IS NULL THEN '{}'::jsonb ELSE jsonb_build_object(
                          'parent', jsonb_build_object('id', p.id::text, 'number', p.number,
                          'identifier', ",
            )
            .push_bind(prefix)
            .push("::text || '-' || p.number::text, 'title', p.title, 'status', p.status)) END");
        } else {
            sql.push("'{}'::jsonb");
        }
    }

    fn expected_values(&self) -> Option<Vec<String>> {
        match self {
            Self::SelectProperty { options, .. } => Some(
                options
                    .iter()
                    .map(|value| format!("value:{value}"))
                    .chain(std::iter::once("unset:".into()))
                    .collect(),
            ),
            Self::CheckboxProperty { .. } => Some(vec![
                "value:false".into(),
                "value:true".into(),
                "unset:".into(),
            ]),
            _ => None,
        }
    }
}
