-- Durable client-notification obligation for task:cancelled rows produced by
-- native Rust terminal close (wakeup / continuation cancellation). task_id is the
-- idempotency key; delivery_lease_token pairs with next_attempt_at as a short
-- claim lease so the row survives until the in-process bus dispatch returns.
CREATE TABLE agent_task_queue_cancelled_client_outbox (
    task_id UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    delivery_lease_token UUID,
    last_error TEXT,
    PRIMARY KEY (task_id)
);

CREATE INDEX idx_agent_task_queue_cancelled_client_due
    ON agent_task_queue_cancelled_client_outbox (next_attempt_at, created_at, task_id);