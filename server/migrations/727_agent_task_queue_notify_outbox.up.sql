-- Durable daemon-wakeup obligation for queue rows inserted outside the Go
-- gateway's post-commit hook (Rust issue pilots). task_id is the idempotency
-- key; coalesced pending slots never create a row.
CREATE TABLE agent_task_queue_notify_outbox (
    task_id UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_error TEXT,
    PRIMARY KEY (task_id)
);
