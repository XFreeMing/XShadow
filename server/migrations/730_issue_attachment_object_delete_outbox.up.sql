-- Durable workspace-upload cleanup obligations recorded before Rust deletes
-- attachment rows during native issue delete. attachment_id is the idempotency key.
CREATE TABLE issue_attachment_object_delete_outbox (
    attachment_id UUID NOT NULL,
    workspace_id UUID NOT NULL,
    storage_key TEXT NOT NULL,
    object_url TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    delivery_lease_token UUID,
    last_error TEXT,
    PRIMARY KEY (attachment_id)
);

CREATE INDEX idx_issue_attachment_object_delete_due
    ON issue_attachment_object_delete_outbox (next_attempt_at, created_at, attachment_id);