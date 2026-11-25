//! Close-rule cleanup after a status write lands on a terminal builtin state.
//! Mirrors `service.StopClosedIssueWakeups` SQL in the same transaction.

use serde::Serialize;
use sqlx::{Error, PgConnection, Row};

#[derive(Debug, Clone, Serialize, PartialEq, Eq)]
pub struct CancelledTaskReceipt {
    pub id: String,
    pub issue_id: String,
    pub agent_id: String,
    pub status: String,
    pub error: String,
}

pub async fn stop_closed_issue_wakeups(
    conn: &mut PgConnection,
    issue_id: &str,
) -> Result<Vec<CancelledTaskReceipt>, Error> {
    sqlx::query(
        "UPDATE issue_wakeup SET enabled = false, disabled_at = clock_timestamp(), updated_at = clock_timestamp()
         WHERE issue_id = $1::uuid AND disabled_at IS NULL AND system_rule IS NULL",
    )
    .bind(issue_id)
    .execute(&mut *conn)
    .await?;

    let rows = sqlx::query(
        "UPDATE agent_task_queue SET status = 'cancelled', completed_at = now(), error = CASE
            WHEN context ? 'continuation_gap' THEN 'Issue closed; continuation stopped'
            ELSE 'Issue closed; wakeup disabled'
         END
         WHERE issue_id = $1::uuid
           AND (context->>'wakeup_id' IS NOT NULL OR context ? 'continuation_gap')
           AND status IN ('queued', 'deferred')
           AND started_at IS NULL
         RETURNING id::text, issue_id::text, agent_id::text, status, COALESCE(error, '')",
    )
    .bind(issue_id)
    .fetch_all(&mut *conn)
    .await?;
    rows.iter()
        .map(|row| {
            Ok(CancelledTaskReceipt {
                id: row.try_get(0)?,
                issue_id: row.try_get(1)?,
                agent_id: row.try_get(2)?,
                status: row.try_get(3)?,
                error: row.try_get(4)?,
            })
        })
        .collect()
}
