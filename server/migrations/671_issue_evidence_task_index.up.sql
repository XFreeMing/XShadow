CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_issue_evidence_task ON agent_task_queue ((context->>'evidence_issue_id')) WHERE context->>'evidence_issue_id' IS NOT NULL;
