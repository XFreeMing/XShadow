-- name: LockQueueNotificationForDelivery :one
SELECT task_id, created_at, attempt_count, next_attempt_at, last_error
FROM agent_task_queue_notify_outbox
WHERE task_id = $1 AND next_attempt_at <= now()
FOR UPDATE SKIP LOCKED;

-- name: DeleteQueueNotificationOutbox :exec
DELETE FROM agent_task_queue_notify_outbox WHERE task_id = $1;

-- name: ListDueQueueNotifications :many
SELECT task_id
FROM agent_task_queue_notify_outbox
WHERE next_attempt_at <= now()
ORDER BY next_attempt_at, created_at, task_id
LIMIT sqlc.arg(batch_limit);

-- name: BumpQueueNotificationRetry :exec
UPDATE agent_task_queue_notify_outbox
SET attempt_count = attempt_count + 1,
    next_attempt_at = sqlc.arg(next_attempt_at),
    last_error = sqlc.narg(last_error)
WHERE task_id = $1;
