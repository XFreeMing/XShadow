-- name: BindIssueEvidenceTask :execrows
-- Only the opening, self-owned input of a new direct chat can be published.
UPDATE agent_task_queue
SET context = jsonb_build_object('evidence_issue_id', (sqlc.arg(issue_id)::uuid)::text, 'evidence_input', @input::jsonb),
    force_fresh_session = true
WHERE id = @task_id AND chat_session_id = @session_id AND chat_input_task_id = id AND issue_id IS NULL AND context IS NULL;

-- name: ListIssueEvidenceTasks :many
-- Automatic retries inherit the context and input owner. Later private turns do not.
SELECT t.id, t.chat_input_task_id, t.agent_id, t.status, t.started_at, t.completed_at, (t.context->'evidence_input')::jsonb AS request,
       COALESCE(t.result->>'output', '')::text AS output, t.failure_reason
FROM agent_task_queue t JOIN chat_session s ON s.id = t.chat_session_id
WHERE s.workspace_id = @workspace_id AND t.context->>'evidence_issue_id' = (sqlc.arg(issue_id)::uuid)::text
  AND t.issue_id IS NULL AND t.chat_input_task_id IS NOT NULL
ORDER BY t.created_at, t.id;

-- name: GetIssueEvidenceRequest :one
SELECT t.id, t.agent_id, (t.context->'evidence_input')::jsonb AS input
FROM agent_task_queue t
WHERE t.chat_session_id = @session_id AND t.chat_input_task_id = t.id AND t.context->>'evidence_issue_id' = (sqlc.arg(issue_id)::uuid)::text;

-- name: IsIssueEvidenceSession :one
SELECT EXISTS (SELECT 1 FROM agent_task_queue t
 WHERE t.chat_session_id = @session_id AND t.chat_input_task_id = t.id AND t.context->>'evidence_issue_id' IS NOT NULL);
