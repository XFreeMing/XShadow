CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_project_skill_proposal_identity ON project_skill_proposal (workspace_id, project_id, issue_id, method_revision);
