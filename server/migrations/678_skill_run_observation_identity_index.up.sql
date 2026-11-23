CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_skill_run_observation_identity ON skill_run_observation (workspace_id, skill_id, task_id);
