package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func queueNotifyQueuedFixture(t *testing.T) (*TaskService, *pgxpool.Pool, context.Context, string, string) {
	t.Helper()
	ctx := context.Background()
	pool := newTaskClaimRacePool(t)
	q := db.New(pool)
	rdb := newRedisTestClient(t)
	svc := NewTaskService(q, pool, nil, events.New(), &stubWakeup{})
	svc.EmptyClaim = NewEmptyClaimCache(rdb)

	suffix := time.Now().UnixNano()
	var userID, workspaceID, runtimeID, agentID, issueID, taskID string
	if err := pool.QueryRow(ctx, `INSERT INTO "user" (name, email) VALUES ($1,$2) RETURNING id`,
		"Queue notify", fmt.Sprintf("queue-notify-%d@multica.ai", suffix)).Scan(&userID); err != nil {
		t.Fatalf("user: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO workspace (name, slug, description, issue_prefix) VALUES ($1,$2,$3,$4) RETURNING id`,
		"Queue notify", fmt.Sprintf("queue-notify-%d", suffix), "tmp", "QNO").Scan(&workspaceID); err != nil {
		t.Fatalf("workspace: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO member (workspace_id, user_id, role) VALUES ($1,$2,'owner')`, workspaceID, userID); err != nil {
		t.Fatalf("member: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO agent_runtime (workspace_id, daemon_id, name, runtime_mode, provider, status, device_info, metadata, last_seen_at, visibility, owner_id)
		VALUES ($1, 'daemon-qnotify', 'QN RT', 'cloud', 'provider', 'online', 'x', '{}'::jsonb, now(), 'private', $2)
		RETURNING id`, workspaceID, userID).Scan(&runtimeID); err != nil {
		t.Fatalf("runtime: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO agent (workspace_id, name, description, runtime_mode, runtime_config, runtime_id, visibility, max_concurrent_tasks, owner_id)
		VALUES ($1, 'QN Agent', '', 'cloud', '{}'::jsonb, $2, 'private', 1, $3)
		RETURNING id`, workspaceID, runtimeID, userID).Scan(&agentID); err != nil {
		t.Fatalf("agent: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, status, priority, creator_id, creator_type, number, position)
		VALUES ($1, 'queue notify', 'todo', 'high', $2, 'member', 800001, 0)
		RETURNING id`, workspaceID, userID).Scan(&issueID); err != nil {
		t.Fatalf("issue: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, issue_id, status, priority, context, runtime_id, workflow_step)
		VALUES ($1, $2, 'queued', 3, '{}'::jsonb, $3, 'todo')
		RETURNING id`, agentID, issueID, runtimeID).Scan(&taskID); err != nil {
		t.Fatalf("task: %v", err)
	}
	t.Cleanup(func() {
		c := context.Background()
		pool.Exec(c, `DELETE FROM agent_task_queue_notify_outbox WHERE task_id = $1`, taskID)
		pool.Exec(c, `DELETE FROM agent_task_queue WHERE id = $1`, taskID)
		pool.Exec(c, `DELETE FROM issue WHERE id = $1`, issueID)
		pool.Exec(c, `DELETE FROM agent WHERE id = $1`, agentID)
		pool.Exec(c, `DELETE FROM agent_runtime WHERE id = $1`, runtimeID)
		pool.Exec(c, `DELETE FROM member WHERE workspace_id = $1`, workspaceID)
		pool.Exec(c, `DELETE FROM workspace WHERE id = $1`, workspaceID)
		pool.Exec(c, `DELETE FROM "user" WHERE id = $1`, userID)
	})
	return svc, pool, ctx, runtimeID, taskID
}

func TestRecoverPendingQueueNotifications_BumpsBeforeWakeup(t *testing.T) {
	svc, pool, ctx, runtimeID, taskID := queueNotifyQueuedFixture(t)
	wakeup := svc.Wakeup.(*stubWakeup)
	if _, err := pool.Exec(ctx, `
		INSERT INTO agent_task_queue_notify_outbox (task_id, next_attempt_at)
		VALUES ($1::uuid, now())`, taskID); err != nil {
		t.Fatalf("seed outbox: %v", err)
	}
	runtimeKey := runtimeID
	v0 := svc.EmptyClaim.CurrentVersion(ctx, runtimeKey)
	svc.EmptyClaim.MarkEmpty(ctx, runtimeKey, v0)
	if !svc.EmptyClaim.IsEmpty(ctx, runtimeKey) {
		t.Fatal("precondition: empty cache")
	}
	result, err := svc.RecoverPendingQueueNotifications(ctx, QueueNotificationSweepBatchSize)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if result.Delivered != 1 {
		t.Fatalf("delivered = %d", result.Delivered)
	}
	if svc.EmptyClaim.IsEmpty(ctx, runtimeKey) {
		t.Fatal("recovery must invalidate cached empty verdict")
	}
	if len(wakeup.calls) != 1 || wakeup.calls[0].taskID != taskID {
		t.Fatalf("wakeup = %#v", wakeup.calls)
	}
	var remaining int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue_notify_outbox WHERE task_id = $1::uuid`, taskID).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("successful delivery must delete the outbox row, got %d", remaining)
	}
}

func TestRecoverPendingQueueNotifications_SettlesTerminalTask(t *testing.T) {
	svc, pool, ctx, _, taskID := queueNotifyQueuedFixture(t)
	if _, err := pool.Exec(ctx, `UPDATE agent_task_queue SET status='completed' WHERE id=$1::uuid`, taskID); err != nil {
		t.Fatalf("complete task: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO agent_task_queue_notify_outbox (task_id, next_attempt_at)
		VALUES ($1::uuid, now())`, taskID); err != nil {
		t.Fatalf("seed outbox: %v", err)
	}
	result, err := svc.RecoverPendingQueueNotifications(ctx, QueueNotificationSweepBatchSize)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if result.Settled != 1 {
		t.Fatalf("settled = %d", result.Settled)
	}
}

func TestRecoverPendingQueueNotifications_BumpsTerminalCloseCancellation(t *testing.T) {
	svc, pool, ctx, runtimeID, taskID := queueNotifyQueuedFixture(t)
	wakeup := svc.Wakeup.(*stubWakeup)
	if _, err := pool.Exec(ctx, `
		UPDATE agent_task_queue
		SET status = 'cancelled',
		    completed_at = now(),
		    error = 'Issue closed; wakeup disabled',
		    context = '{"wakeup_id":"22222222-2222-4222-8222-222222222222"}'::jsonb
		WHERE id = $1::uuid`, taskID); err != nil {
		t.Fatalf("cancel task: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO agent_task_queue_notify_outbox (task_id, next_attempt_at)
		VALUES ($1::uuid, now())`, taskID); err != nil {
		t.Fatalf("seed outbox: %v", err)
	}
	runtimeKey := runtimeID
	v0 := svc.EmptyClaim.CurrentVersion(ctx, runtimeKey)
	svc.EmptyClaim.MarkEmpty(ctx, runtimeKey, v0)
	result, err := svc.RecoverPendingQueueNotifications(ctx, QueueNotificationSweepBatchSize)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if result.Delivered != 1 {
		t.Fatalf("delivered = %d", result.Delivered)
	}
	if svc.EmptyClaim.IsEmpty(ctx, runtimeKey) {
		t.Fatal("recovery must invalidate cached empty verdict")
	}
	if len(wakeup.calls) != 1 || wakeup.calls[0].taskID != "" {
		t.Fatalf("wakeup = %#v", wakeup.calls)
	}
}

const terminalCloseNotifyOutboxUpsert = `
INSERT INTO agent_task_queue_notify_outbox (task_id, next_attempt_at)
SELECT $1::uuid, now()
WHERE EXISTS (
    SELECT 1 FROM agent_task_queue WHERE id = $1::uuid AND runtime_id IS NOT NULL
)
ON CONFLICT (task_id) DO UPDATE SET
    next_attempt_at = LEAST(agent_task_queue_notify_outbox.next_attempt_at, EXCLUDED.next_attempt_at)`

func TestTerminalCloseNotifyOutboxPinsDelayedRow(t *testing.T) {
	_, pool, ctx, _, taskID := queueNotifyQueuedFixture(t)
	if _, err := pool.Exec(ctx, `
		INSERT INTO agent_task_queue_notify_outbox (task_id, next_attempt_at)
		VALUES ($1::uuid, now() + interval '10 minutes')`, taskID); err != nil {
		t.Fatalf("seed delayed outbox: %v", err)
	}
	if _, err := pool.Exec(ctx, terminalCloseNotifyOutboxUpsert, taskID); err != nil {
		t.Fatalf("pin outbox: %v", err)
	}
	var due time.Time
	if err := pool.QueryRow(ctx, `SELECT next_attempt_at FROM agent_task_queue_notify_outbox WHERE task_id = $1::uuid`, taskID).Scan(&due); err != nil {
		t.Fatal(err)
	}
	if due.After(time.Now().Add(2 * time.Second)) {
		t.Fatalf("next_attempt_at still deferred: %v", due)
	}
}

func TestRecoverPendingQueueNotifications_RetriesRedisWrongTypeAndRecovers(t *testing.T) {
	svc, pool, ctx, runtimeID, taskID := queueNotifyQueuedFixture(t)
	wakeup := svc.Wakeup.(*stubWakeup)
	if _, err := pool.Exec(ctx, `
		INSERT INTO agent_task_queue_notify_outbox (task_id, next_attempt_at)
		VALUES ($1::uuid, now())`, taskID); err != nil {
		t.Fatalf("seed outbox: %v", err)
	}
	versionKey := emptyClaimVersion(runtimeID)
	if err := svc.EmptyClaim.rdb.HSet(ctx, versionKey, "fixture-deny-version-increment", "1").Err(); err != nil {
		t.Fatalf("wrong-type fixture: %v", err)
	}
	result, err := svc.RecoverPendingQueueNotifications(ctx, QueueNotificationSweepBatchSize)
	if err == nil {
		t.Fatal("expected redis error from recovery pass")
	}
	if result.Retried != 1 {
		t.Fatalf("retried = %d, want 1", result.Retried)
	}
	var attempts int32
	if err := pool.QueryRow(ctx, `SELECT attempt_count FROM agent_task_queue_notify_outbox WHERE task_id = $1::uuid`, taskID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts < 1 {
		t.Fatalf("attempt_count = %d, want >= 1", attempts)
	}
	if _, err := pool.Exec(ctx, `UPDATE agent_task_queue_notify_outbox SET next_attempt_at = now() WHERE task_id = $1::uuid`, taskID); err != nil {
		t.Fatalf("force due: %v", err)
	}
	if err := svc.EmptyClaim.rdb.Del(ctx, versionKey).Err(); err != nil {
		t.Fatalf("clear wrong-type key: %v", err)
	}
	result, err = svc.RecoverPendingQueueNotifications(ctx, QueueNotificationSweepBatchSize)
	if err != nil {
		t.Fatalf("second recover: %v", err)
	}
	if result.Delivered != 1 {
		t.Fatalf("delivered = %d", result.Delivered)
	}
	if len(wakeup.calls) != 1 || wakeup.calls[0].taskID != taskID {
		t.Fatalf("wakeup = %#v", wakeup.calls)
	}
}

func TestCompleteIssueStatusPromotionAfterNativeWrite_RemovesOutbox(t *testing.T) {
	svc, pool, ctx, _, taskID := queueNotifyQueuedFixture(t)
	if _, err := pool.Exec(ctx, `
		INSERT INTO agent_task_queue_notify_outbox (task_id, next_attempt_at)
		VALUES ($1::uuid, now() - interval '1 second')`, taskID); err != nil {
		t.Fatalf("seed outbox: %v", err)
	}
	parsed, err := util.ParseUUID(taskID)
	if err != nil {
		t.Fatal(err)
	}
	svc.CompleteIssueStatusPromotionAfterNativeWrite(ctx, parsed)
	var remaining int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue_notify_outbox WHERE task_id = $1::uuid`, taskID).Scan(&remaining); err != nil {
		t.Fatalf("read outbox: %v", err)
	}
	if remaining != 0 {
		t.Fatal("fast path should remove outbox after successful invalidation")
	}
}
