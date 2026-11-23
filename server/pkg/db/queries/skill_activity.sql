-- name: RecordSkillRunAvailability :exec
INSERT INTO skill_run_observation (workspace_id, skill_id, task_id, dispatched_at, content_hash, version)
SELECT s.workspace_id, s.id, t.id, t.dispatched_at, offered.content_hash,
       (SELECT v.version FROM skill_version v
         WHERE v.skill_id = s.id AND v.workspace_id = s.workspace_id
           AND v.content_hash = offered.content_hash ORDER BY v.version DESC LIMIT 1)
FROM (SELECT unnest(sqlc.arg(skill_ids)::uuid[]) AS skill_id,
             unnest(sqlc.arg(content_hashes)::text[]) AS content_hash) AS offered
JOIN skill s ON s.id = offered.skill_id AND s.workspace_id = sqlc.arg(workspace_id)
JOIN agent_task_queue t ON t.id = sqlc.arg(task_id) AND t.runtime_id = sqlc.arg(runtime_id)
JOIN issue i ON i.id = t.issue_id AND i.workspace_id = s.workspace_id
WHERE t.dispatched_at = sqlc.arg(dispatched_at) AND t.started_at IS NOT NULL AND t.status = 'running'
  AND t.chat_session_id IS NULL
ON CONFLICT (workspace_id, skill_id, task_id) DO UPDATE
SET dispatched_at = EXCLUDED.dispatched_at, content_hash = EXCLUDED.content_hash,
    version = EXCLUDED.version, provided_at = EXCLUDED.provided_at,
    reviewed_at = NULL, reviewed_by = NULL, outcome = NULL, evidence = NULL
WHERE skill_run_observation.dispatched_at < EXCLUDED.dispatched_at;

-- name: ListSkillRunObservations :many
SELECT sqlc.embed(skill_run_observation), agent_task_queue.status,
       issue.id AS issue_id, issue.number AS issue_number, issue.project_id
FROM skill_run_observation
JOIN agent_task_queue ON agent_task_queue.id = skill_run_observation.task_id
    AND agent_task_queue.dispatched_at = skill_run_observation.dispatched_at
JOIN issue ON issue.id = agent_task_queue.issue_id
    AND issue.workspace_id = skill_run_observation.workspace_id
WHERE skill_run_observation.workspace_id = sqlc.arg(workspace_id)
  AND skill_run_observation.skill_id = sqlc.arg(skill_id)
  AND skill_run_observation.provided_at >= sqlc.arg(since)::timestamptz
  AND agent_task_queue.chat_session_id IS NULL
  AND agent_task_queue.started_at IS NOT NULL
  AND agent_task_queue.status IN ('running', 'completed', 'failed', 'cancelled')
ORDER BY skill_run_observation.provided_at DESC, skill_run_observation.task_id DESC;

-- name: ReviewSkillRunObservation :execrows
UPDATE skill_run_observation so
SET outcome = sqlc.arg(outcome), evidence = sqlc.arg(evidence),
    reviewed_by = sqlc.arg(reviewed_by),
    reviewed_at = CASE
      WHEN (so.outcome, so.evidence, so.reviewed_by) IS DISTINCT FROM
           (sqlc.arg(outcome), sqlc.arg(evidence), sqlc.arg(reviewed_by)) THEN now()
      ELSE so.reviewed_at
    END
FROM agent_task_queue t, issue i
WHERE so.workspace_id = sqlc.arg(workspace_id) AND so.skill_id = sqlc.arg(skill_id)
  AND so.task_id = sqlc.arg(task_id) AND so.content_hash = sqlc.arg(content_hash)
  AND t.id = so.task_id AND t.dispatched_at = so.dispatched_at
  AND t.status IN ('completed', 'failed', 'cancelled') AND t.started_at IS NOT NULL
  AND t.chat_session_id IS NULL AND i.id = t.issue_id AND i.workspace_id = so.workspace_id;
