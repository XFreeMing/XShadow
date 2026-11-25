use serde::{Deserialize, Serialize};
use serde_json::Value;
use sqlx::{PgConnection, PgPool, Postgres, QueryBuilder, Row};

use super::{IssueListQuery, add_filters, db_error};
use crate::issues::store::{CallerActor, IssueReadError, resolve_caller_access_on};

#[derive(Deserialize)]
pub struct IssueTableFacetsQuery {
    query: IssueListQuery,
    facets: Vec<Facet>,
    include_total: bool,
    #[serde(default)]
    working_agent_ids: Vec<String>,
}

#[derive(Deserialize)]
struct Facet {
    kind: FacetKind,
    #[serde(default)]
    property_id: String,
    #[serde(default)]
    property_key: String,
    #[serde(default)]
    property_type: String,
    batch: bool,
}

#[derive(Clone, Copy, Deserialize, Serialize)]
#[serde(rename_all = "snake_case")]
enum FacetKind {
    Status,
    Priority,
    Assignee,
    Creator,
    Project,
    Label,
    Property,
    WorkingAgents,
}

#[derive(Serialize)]
struct FacetValue {
    key: String,
    count: i64,
}

#[derive(Serialize)]
struct FacetResult {
    kind: FacetKind,
    #[serde(skip_serializing_if = "String::is_empty")]
    property_id: String,
    values: Vec<FacetValue>,
}

#[derive(Serialize)]
pub struct IssueTableFacetsResult {
    total: i64,
    facets: Vec<FacetResult>,
}

pub async fn table_facets(
    pool: &PgPool,
    workspace_id: &str,
    actor: &CallerActor,
    request: IssueTableFacetsQuery,
) -> Result<IssueTableFacetsResult, IssueReadError> {
    let mut tx = pool.begin().await.map_err(db_error)?;
    sqlx::query("SET TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY")
        .execute(&mut *tx)
        .await
        .map_err(db_error)?;
    let hidden = resolve_caller_access_on(&mut tx, workspace_id, actor).await?;
    let hidden_ids: Vec<_> = hidden.ids.into_iter().collect();
    let mut sql = FacetQuery {
        conn: &mut tx,
        workspace_id,
        hidden_ids: &hidden_ids,
    };

    let mut result = IssueTableFacetsResult {
        total: 0,
        facets: request
            .facets
            .iter()
            .map(|facet| FacetResult {
                kind: facet.kind,
                property_id: facet.property_id.clone(),
                values: Vec::new(),
            })
            .collect(),
    };
    sql.batch(&request, &mut result).await?;
    for (facet, response) in request.facets.iter().zip(&mut result.facets) {
        if !facet.batch {
            let query = facet.without_own_filter(&request.query);
            response.values = sql.one(&query, facet, &request.working_agent_ids).await?;
        }
        response
            .values
            .sort_by(|left, right| left.key.cmp(&right.key));
    }
    tx.commit().await.map_err(db_error)?;
    Ok(result)
}

struct FacetQuery<'db> {
    conn: &'db mut PgConnection,
    workspace_id: &'db str,
    hidden_ids: &'db [String],
}

impl FacetQuery<'_> {
    async fn batch(
        &mut self,
        request: &IssueTableFacetsQuery,
        result: &mut IssueTableFacetsResult,
    ) -> Result<(), IssueReadError> {
        let batch = request
            .facets
            .iter()
            .enumerate()
            .filter(|(_, facet)| facet.batch)
            .map(|(index, facet)| {
                facet
                    .kind
                    .expression()
                    .map(|expr| (index, expr))
                    .ok_or(IssueReadError::DatabaseFailed)
            })
            .collect::<Result<Vec<_>, _>>()?;
        if batch.is_empty() {
            if request.include_total {
                let mut sql = QueryBuilder::new("SELECT count(*) FROM issue i");
                add_filters(&mut sql, self.workspace_id, self.hidden_ids, &request.query);
                result.total = sql
                    .build_query_scalar()
                    .fetch_one(&mut *self.conn)
                    .await
                    .map_err(db_error)?;
            }
            return Ok(());
        }
        // The dimensions with no own filter share one grouping-sets scan.
        let mut sql = QueryBuilder::new("SELECT CASE ");
        for (index, expr) in &batch {
            sql.push("WHEN GROUPING(")
                .push(expr)
                .push(") = 0 THEN ")
                .push_bind(*index as i32)
                .push("::int ");
        }
        sql.push("ELSE -1 END, CASE ");
        for (_, expr) in &batch {
            sql.push("WHEN GROUPING(")
                .push(expr)
                .push(") = 0 THEN (")
                .push(expr)
                .push(")::text ");
        }
        sql.push("ELSE '' END, count(*) FROM issue i");
        add_filters(&mut sql, self.workspace_id, self.hidden_ids, &request.query);
        sql.push(" GROUP BY GROUPING SETS (");
        {
            let mut sets = sql.separated(", ");
            for (_, expr) in &batch {
                sets.push(format!("({expr})"));
            }
            if request.include_total {
                sets.push("()");
            }
        }
        sql.push(")");
        for row in sql
            .build()
            .fetch_all(&mut *self.conn)
            .await
            .map_err(db_error)?
        {
            let index: i32 = row.try_get(0).map_err(db_error)?;
            let count = row.try_get(2).map_err(db_error)?;
            if index < 0 {
                result.total = count;
            } else {
                result.facets[index as usize].values.push(FacetValue {
                    key: row.try_get(1).map_err(db_error)?,
                    count,
                });
            }
        }
        Ok(())
    }

    async fn one(
        &mut self,
        query: &IssueListQuery,
        facet: &Facet,
        working_agent_ids: &[String],
    ) -> Result<Vec<FacetValue>, IssueReadError> {
        let mut sql = QueryBuilder::<Postgres>::new("SELECT ");
        let mut property_predicate = None;
        match facet.kind {
            FacetKind::Label => {
                sql.push(
                    "l.label_id::text, count(DISTINCT i.id) FROM issue i
                          JOIN issue_to_label l ON l.issue_id = i.id",
                );
            }
            FacetKind::WorkingAgents => {
                if working_agent_ids.is_empty() {
                    return Ok(Vec::new());
                }
                sql = QueryBuilder::new(
                    "WITH running AS MATERIALIZED (
                    SELECT atq.agent_id, atq.issue_id FROM unnest(",
                );
                sql.push_bind(working_agent_ids).push("::uuid[]) AS cand(agent_id)
                    JOIN agent_task_queue atq ON atq.agent_id = cand.agent_id AND atq.status = 'running'
                    WHERE atq.chat_session_id IS NULL AND atq.autopilot_run_id IS NULL
                      AND atq.issue_id IS NOT NULL)
                    SELECT r.agent_id::text, count(*) FROM running r
                    WHERE EXISTS (SELECT 1 FROM issue i");
            }
            FacetKind::Property => {
                let key = &facet.property_key;
                match facet.property_type.as_str() {
                    "select" | "actor" | "checkbox" => {
                        property_predicate = Some(if facet.property_type == "checkbox" {
                            "boolean"
                        } else {
                            "string"
                        });
                        sql.push("COALESCE(i.properties ->> ")
                            .push_bind(key)
                            .push("::text, '__none__'), count(*) FROM issue i");
                    }
                    "text" | "url" | "date" | "number" | "multi_text" | "multi_url" => {
                        sql.push("CASE WHEN i.properties ? ").push_bind(key).push(
                            "::text THEN '__set__' ELSE '__none__' END, count(*) FROM issue i",
                        );
                    }
                    "multi_select" | "multi_actor" => {
                        sql.push("COALESCE(pv.value, '__none__'), count(DISTINCT i.id) FROM issue i
                            JOIN LATERAL (SELECT jsonb_array_elements_text(CASE WHEN jsonb_typeof(i.properties -> ")
                            .push_bind(key).push("::text) = 'array' THEN i.properties -> ")
                            .push_bind(key).push("::text ELSE '[]'::jsonb END) AS value
                               UNION ALL SELECT NULL WHERE NOT (i.properties ? ")
                            .push_bind(key).push("::text)) pv(value) ON TRUE");
                    }
                    _ => return Err(IssueReadError::DatabaseFailed),
                }
            }
            _ => {
                let expr = facet
                    .kind
                    .expression()
                    .ok_or(IssueReadError::DatabaseFailed)?;
                sql.push(expr).push(", count(*) FROM issue i");
            }
        }
        add_filters(&mut sql, self.workspace_id, self.hidden_ids, query);
        if let Some(kind) = property_predicate {
            sql.push(" AND (jsonb_typeof(i.properties -> ")
                .push_bind(&facet.property_key)
                .push("::text) = ")
                .push_bind(kind)
                .push(" OR NOT (i.properties ? ")
                .push_bind(&facet.property_key)
                .push("::text))");
        }
        if matches!(facet.kind, FacetKind::WorkingAgents) {
            sql.push(" AND i.id = r.issue_id)");
        }
        sql.push(" GROUP BY 1");
        let entries = sql
            .build_query_as::<(String, i64)>()
            .fetch_all(&mut *self.conn)
            .await
            .map_err(db_error)?;
        Ok(entries
            .into_iter()
            .map(|(key, count)| FacetValue { key, count })
            .collect())
    }
}

impl FacetKind {
    fn expression(self) -> Option<&'static str> {
        match self {
            Self::Status => Some("i.status"),
            Self::Priority => Some("i.priority"),
            Self::Assignee => Some(
                "CASE WHEN i.assignee_type IS NULL OR i.assignee_id IS NULL THEN '__none__' ELSE i.assignee_type || ':' || i.assignee_id::text END",
            ),
            Self::Creator => Some("i.creator_type || ':' || i.creator_id::text"),
            Self::Project => Some("COALESCE(i.project_id::text, '__none__')"),
            _ => None,
        }
    }
}

impl Facet {
    fn without_own_filter(&self, input: &IssueListQuery) -> IssueListQuery {
        let mut query = input.clone();
        // Only flat filters belonging to this dimension are disjunctive.
        // The current scope and the advanced condition tree still apply.
        let dimension = match self.kind {
            FacetKind::Status => {
                query.statuses.clear();
                Some("statuses")
            }
            FacetKind::Priority => {
                query.priorities.clear();
                Some("priorities")
            }
            FacetKind::Assignee => {
                query.assignee_filters.clear();
                query.include_no_assignee = false;
                query.ids = None; // Explicit empty table assignee filter.
                Some("assignees")
            }
            FacetKind::Creator => {
                query.creator_filters.clear();
                Some("creators")
            }
            FacetKind::Project => {
                query.project_ids.clear();
                query.include_no_project = false;
                Some("project_ids")
            }
            FacetKind::Label => {
                query.label_ids.clear();
                Some("label_ids")
            }
            FacetKind::Property => {
                query.properties.retain(|group| {
                    group.first().and_then(property_filter_id) != Some(self.property_id.as_str())
                });
                None
            }
            FacetKind::WorkingAgents => {
                query.working_only = false;
                query.working_issue_ids = None;
                None
            }
        };
        if let Some(dimension) = dimension
            && let Some(node) = &mut query.advanced
        {
            let length = node.children.len();
            node.children
                .retain(|child| child.flat_dimension.as_deref() != Some(dimension));
            if node.children.is_empty() && length != 0 {
                query.advanced = None;
            }
        }
        query
    }
}

fn property_filter_id(value: &Value) -> Option<&str> {
    let pattern = value.as_object()?;
    if let Some(id) = pattern.get("__none__") {
        id.as_str()
    } else if pattern.contains_key("__op__") {
        pattern.get("def")?.as_str()
    } else {
        pattern.keys().next().map(String::as_str)
    }
}
