-- name: ClaimCancelledClientNotificationLease :one
UPDATE agent_task_queue_cancelled_client_outbox
SET delivery_lease_token = sqlc.arg(lease_token),
    next_attempt_at = now() + sqlc.arg(lease_seconds)::integer * interval '1 second'
WHERE task_id = (
    SELECT pending.task_id
    FROM agent_task_queue_cancelled_client_outbox AS pending
    WHERE pending.task_id = sqlc.arg(task_id) AND pending.next_attempt_at <= now()
    FOR UPDATE SKIP LOCKED
)
RETURNING task_id;

-- name: AckCancelledClientNotificationOutbox :execrows
DELETE FROM agent_task_queue_cancelled_client_outbox
WHERE task_id = $1 AND delivery_lease_token = sqlc.arg(lease_token);

-- name: ReleaseCancelledClientNotificationLease :exec
UPDATE agent_task_queue_cancelled_client_outbox
SET delivery_lease_token = NULL,
    attempt_count = LEAST(attempt_count, 2147483646) + 1,
    next_attempt_at = now() +
        LEAST(attempt_count::bigint + 1, sqlc.arg(retry_cap_seconds)::integer) * interval '1 second',
    last_error = sqlc.narg(last_error)
WHERE task_id = $1 AND delivery_lease_token = sqlc.arg(lease_token);

-- name: ListDueCancelledClientNotifications :many
SELECT task_id
FROM agent_task_queue_cancelled_client_outbox
WHERE next_attempt_at <= now()
ORDER BY next_attempt_at, created_at, task_id
LIMIT sqlc.arg(batch_limit);
