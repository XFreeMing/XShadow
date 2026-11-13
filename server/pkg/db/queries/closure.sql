-- DEV-7 closure contract persistence. An authorization is a scoped, readable
-- grant; a review is one independent verdict about one delivery. Neither is
-- derived: the service layer writes them only after its own checks.

-- name: GetActiveClosureAuthorization :one
-- The newest active grant for one subject. Linkage to a goal is checked by the
-- caller against goal_issue; a same-project or parent relation never matches.
SELECT * FROM closure_authorization
WHERE workspace_id = $1
  AND subject_kind = $2
  AND subject_id = $3
  AND state = 'active'
ORDER BY created_at DESC, id DESC
LIMIT 1;

-- name: GetClosureAuthorizationByID :one
SELECT * FROM closure_authorization
WHERE id = $1 AND workspace_id = $2;

-- name: GetLatestClosureAuthorization :one
-- Whether a subject has ever been enrolled in the closure contract. A revoked
-- grant still enrolls it: completion must then wait for a human rather than
-- silently fall back to the legacy path.
SELECT * FROM closure_authorization
WHERE workspace_id = $1
  AND subject_kind = $2
  AND subject_id = $3
ORDER BY created_at DESC, id DESC
LIMIT 1;

-- name: CreateClosureAuthorization :one
INSERT INTO closure_authorization (
    workspace_id, project_id, subject_kind, subject_id, actions, covers_children,
    criteria_revision, source_ref, granted_by_type, granted_by_id, expires_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11
) RETURNING *;

-- name: RevokeClosureAuthorization :one
UPDATE closure_authorization SET
    state = 'revoked',
    revoked_at = now(),
    updated_at = now(),
    revision = revision + 1
WHERE id = $1 AND workspace_id = $2 AND state = 'active'
RETURNING *;

-- name: ListClosureReviews :many
SELECT * FROM closure_review
WHERE workspace_id = $1 AND subject_kind = $2 AND subject_id = $3
ORDER BY recorded_at DESC, id DESC;

-- name: GetClosureReviewByIdempotencyKey :one
SELECT * FROM closure_review
WHERE workspace_id = $1 AND idempotency_key = $2 AND idempotency_key <> '';

-- name: CreateClosureReview :one
INSERT INTO closure_review (
    workspace_id, project_id, subject_kind, subject_id, action,
    delivery_version, delivery_tree, criteria_revision,
    author_type, author_id, author_run_id, author_model,
    reviewer_type, reviewer_id, reviewer_run_id, reviewer_model,
    verdict, criteria, platforms, missing, reason, idempotency_key
) VALUES (
    sqlc.arg('workspace_id'), sqlc.arg('project_id'), sqlc.arg('subject_kind'), sqlc.arg('subject_id'), sqlc.arg('action'),
    sqlc.arg('delivery_version'), sqlc.arg('delivery_tree'), sqlc.arg('criteria_revision'),
    sqlc.arg('author_type'), sqlc.arg('author_id'), sqlc.arg('author_run_id'), sqlc.arg('author_model'),
    sqlc.arg('reviewer_type'), sqlc.arg('reviewer_id'), sqlc.arg('reviewer_run_id'), sqlc.arg('reviewer_model'),
    sqlc.arg('verdict'), sqlc.arg('criteria'), sqlc.arg('platforms'), sqlc.arg('missing'), sqlc.arg('reason'), sqlc.arg('idempotency_key')
) RETURNING *;

-- name: SupersedeClosureReviews :exec
-- A new delivery makes every verdict about another version stale. Superseded
-- rows stay for audit but never satisfy the contract.
UPDATE closure_review SET superseded_by = sqlc.arg('superseded_by')
WHERE workspace_id = sqlc.arg('workspace_id')
  AND subject_kind = sqlc.arg('subject_kind')
  AND subject_id = sqlc.arg('subject_id')
  AND superseded_by IS NULL
  AND delivery_version <> sqlc.arg('delivery_version');

-- name: AcceptClosureReview :one
-- The persisted completion action. Only an unaccepted, passing review can be
-- accepted, so a repeated request returns no row and the caller replays. The
-- grant it ran under is recorded with the action: downstream release refuses
-- evidence whose grant was revoked or expired after this write.
UPDATE closure_review SET
    accepted_at = now(),
    accepted_by_type = $3,
    accepted_by_id = $4,
    authorization_id = $5
WHERE id = $1 AND workspace_id = $2 AND accepted_at IS NULL AND verdict = 'pass'
RETURNING *;

-- name: GetValidAcceptedClosureReview :one
-- The currently valid acceptance for one issue. Valid means: a pass verdict
-- for the action, already accepted, not superseded by a newer delivery, and
-- backed by an authorization that is still active, unexpired and still allows
-- closing. accepted_at alone is not evidence: a revoked grant, a replaced
-- delivery or a stale row must not release a downstream dependent.
--
-- The caller also passes the CURRENT authoritative facts when it knows them:
-- delivery_version is the head SHA registered on the issue's linked PR and
-- criteria_revision is the goal plan revision the issue is bound to. A stored
-- acceptance that names an earlier delivery or an earlier plan revision stops
-- matching the moment that fact changes — no new review is needed to
-- invalidate it. An empty parameter means the fact is not registered for this
-- issue and therefore cannot constrain the acceptance.
SELECT r.* FROM closure_review AS r
JOIN closure_authorization AS a
  ON a.id = r.authorization_id AND a.workspace_id = r.workspace_id
WHERE r.workspace_id = sqlc.arg('workspace_id')
  AND r.subject_kind = 'issue'
  AND r.subject_id = sqlc.arg('subject_id')
  AND r.action = 'issue_close'
  AND r.verdict = 'pass'
  AND r.accepted_at IS NOT NULL
  AND r.superseded_by IS NULL
  AND (COALESCE(sqlc.narg('delivery_version')::text, '') = ''
       OR r.delivery_version = sqlc.narg('delivery_version')::text)
  AND (COALESCE(sqlc.narg('criteria_revision')::text, '') = ''
       OR r.criteria_revision = sqlc.narg('criteria_revision')::text)
  AND a.state = 'active'
  AND (a.expires_at IS NULL OR a.expires_at > now())
  AND 'issue_close' = ANY(a.actions)
ORDER BY r.recorded_at DESC, r.id DESC
LIMIT 1;

-- name: DeleteClosureReviewsBySubject :exec
DELETE FROM closure_review
WHERE workspace_id = $1 AND subject_kind = $2 AND subject_id = $3;

-- name: DeleteClosureAuthorizationsBySubject :exec
DELETE FROM closure_authorization
WHERE workspace_id = $1 AND subject_kind = $2 AND subject_id = $3;

-- name: DeleteWorkspaceClosureReviews :exec
DELETE FROM closure_review WHERE workspace_id = $1;

-- name: DeleteWorkspaceClosureAuthorizations :exec
DELETE FROM closure_authorization WHERE workspace_id = $1;

-- name: GetLatestIssueAuthor :one
-- The implementing run behind an issue. The server records it, so a client
-- cannot name a different author to manufacture independence.
SELECT t.agent_id, t.id AS task_id, COALESCE(a.model, '')::text AS model
FROM agent_task_queue t
JOIN agent a ON a.id = t.agent_id
JOIN issue i ON i.id = t.issue_id
WHERE t.issue_id = $1
  AND (i.assignee_type IS DISTINCT FROM 'agent' OR t.agent_id = i.assignee_id)
ORDER BY t.created_at DESC
LIMIT 1;

-- name: GetLatestGoalAuthor :one
-- Goals are delivered through linked issues; their author is the most recent
-- implementing run among those issues.
SELECT t.agent_id, t.id AS task_id, COALESCE(a.model, '')::text AS model
FROM goal_issue gi
JOIN agent_task_queue t ON t.issue_id = gi.issue_id
JOIN agent a ON a.id = t.agent_id
JOIN issue i ON i.id = t.issue_id
WHERE gi.workspace_id = $1 AND gi.goal_id = $2
  AND (i.assignee_type IS DISTINCT FROM 'agent' OR t.agent_id = i.assignee_id)
ORDER BY t.created_at DESC
LIMIT 1;