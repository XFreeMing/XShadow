CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_skill_scope_name ON skill (workspace_id, project_id, name) NULLS NOT DISTINCT;
