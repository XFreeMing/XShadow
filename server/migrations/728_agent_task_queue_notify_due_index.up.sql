CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_agent_task_queue_notify_due
    ON agent_task_queue_notify_outbox (next_attempt_at, created_at, task_id);
