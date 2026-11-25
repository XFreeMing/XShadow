//! Close-rule cleanup after a status write lands on a terminal builtin state.
//! Mirrors `service.StopClosedIssueWakeups` SQL in the same transaction and
//! records durable client/runtime notification obligations for post-commit replay.

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

const STOP_CLOSED_ISSUE_WAKEUPS: &str = r#"
WITH cancelled AS (
    UPDATE agent_task_queue SET status = 'cancelled', completed_at = now(), error = CASE
        WHEN context ? 'continuation_gap' THEN 'Issue closed; continuation stopped'
        ELSE 'Issue closed; wakeup disabled'
    END
    WHERE issue_id = $1::uuid
      AND (context->>'wakeup_id' IS NOT NULL OR context ? 'continuation_gap')
      AND status IN ('queued', 'deferred')
      AND started_at IS NULL
    RETURNING id, issue_id, agent_id, status, error, runtime_id
),
notified AS (
    INSERT INTO agent_task_queue_notify_outbox (task_id, next_attempt_at)
    SELECT id, now()
    FROM cancelled
    WHERE runtime_id IS NOT NULL
    ON CONFLICT (task_id) DO UPDATE SET
        next_attempt_at = LEAST(agent_task_queue_notify_outbox.next_attempt_at, EXCLUDED.next_attempt_at)
    RETURNING task_id
),
client_notified AS (
    INSERT INTO agent_task_queue_cancelled_client_outbox (task_id, next_attempt_at)
    SELECT id, now()
    FROM cancelled
    ON CONFLICT (task_id) DO UPDATE SET
        next_attempt_at = LEAST(agent_task_queue_cancelled_client_outbox.next_attempt_at, EXCLUDED.next_attempt_at)
    WHERE agent_task_queue_cancelled_client_outbox.delivery_lease_token IS NULL
    RETURNING task_id
)
SELECT id::text, issue_id::text, agent_id::text, status, COALESCE(error, '')
FROM cancelled
"#;

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

    let rows = sqlx::query(STOP_CLOSED_ISSUE_WAKEUPS)
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
