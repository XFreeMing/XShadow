-- name: ProposeProjectSkill :exec
INSERT INTO project_skill_proposal (workspace_id, project_id, issue_id, method_revision, trace_revision, candidate_commit, lesson)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (workspace_id, project_id, issue_id, method_revision) DO NOTHING;

-- name: ListProjectSkillProposals :many
SELECT p.*, i.number AS issue_number
FROM project_skill_proposal p
JOIN issue i ON i.id = p.issue_id AND i.workspace_id = p.workspace_id AND i.project_id = p.project_id
WHERE p.workspace_id = $1 AND p.project_id = $2
ORDER BY p.proposed_at DESC, p.issue_id, p.method_revision DESC;

-- name: LockProjectSkillProposal :one
SELECT * FROM project_skill_proposal
WHERE workspace_id = $1 AND project_id = $2 AND issue_id = $3 AND method_revision = $4
FOR UPDATE;

-- name: DecideProjectSkillProposal :exec
UPDATE project_skill_proposal
SET decided_at = now(), decided_by = @decided_by, skill_id = sqlc.narg(skill_id), skill_version = sqlc.narg(skill_version)
WHERE workspace_id = @workspace_id AND project_id = @project_id AND issue_id = @issue_id AND method_revision = @method_revision;

-- name: ListProjectSkills :many
SELECT * FROM skill WHERE workspace_id = $1 AND project_id = $2 ORDER BY name;

-- name: ListProjectSkillsByIDs :many
SELECT * FROM skill
WHERE workspace_id = $1 AND project_id = $2 AND id = ANY(sqlc.arg(skill_ids)::uuid[])
ORDER BY name;

-- name: GetProjectSkillByName :one
SELECT * FROM skill WHERE workspace_id = $1 AND project_id = $2 AND name = $3 FOR UPDATE;

-- name: DeleteProjectSkillProposals :exec
DELETE FROM project_skill_proposal WHERE workspace_id = $1 AND project_id = $2;

-- name: DeleteProjectSkills :exec
WITH ids AS (
    SELECT s.id FROM skill s WHERE s.workspace_id = sqlc.arg(workspace_id) AND s.project_id = sqlc.arg(project_id)
), labels AS (
    DELETE FROM skill_to_label WHERE skill_to_label.skill_id IN (SELECT id FROM ids)
), versions AS (
    DELETE FROM skill_version WHERE skill_version.workspace_id = sqlc.arg(workspace_id) AND skill_version.skill_id IN (SELECT id FROM ids)
), observations AS (
    DELETE FROM skill_run_observation WHERE skill_run_observation.workspace_id = sqlc.arg(workspace_id) AND skill_run_observation.skill_id IN (SELECT id FROM ids)
)
DELETE FROM skill WHERE id IN (SELECT id FROM ids);

-- name: DeleteIssueSkillProposals :exec
DELETE FROM project_skill_proposal WHERE workspace_id = $1 AND issue_id = $2;

-- name: DeleteWorkspaceSkillProposals :exec
DELETE FROM project_skill_proposal WHERE workspace_id = $1;
