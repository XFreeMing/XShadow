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
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func cancelledClientNotifyFixture(t *testing.T) (*TaskService, *pgxpool.Pool, context.Context, string, string, string) {
	t.Helper()
	ctx := context.Background()
	pool := newTaskClaimRacePool(t)
	q := db.New(pool)
	bus := events.New()
	svc := NewTaskService(q, pool, nil, bus, &stubWakeup{})

	suffix := time.Now().UnixNano()
	var userID, workspaceID, agentID, issueID, taskID string
	if err := pool.QueryRow(ctx, `INSERT INTO "user" (name, email) VALUES ($1,$2) RETURNING id`,
		"Cancelled client", fmt.Sprintf("cancel-client-%d@multica.ai", suffix)).Scan(&userID); err != nil {
		t.Fatalf("user: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO workspace (name, slug, description, issue_prefix) VALUES ($1,$2,$3,$4) RETURNING id`,
		"Cancelled client", fmt.Sprintf("cancel-client-%d", suffix), "tmp", "CCL").Scan(&workspaceID); err != nil {
		t.Fatalf("workspace: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO member (workspace_id, user_id, role) VALUES ($1,$2,'owner')`, workspaceID, userID); err != nil {
		t.Fatalf("member: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO agent (workspace_id, name, description, runtime_mode, runtime_config, visibility, max_concurrent_tasks, owner_id)
		VALUES ($1, 'CC Agent', '', 'cloud', '{}'::jsonb, 'private', 1, $2)
		RETURNING id`, workspaceID, userID).Scan(&agentID); err != nil {
		t.Fatalf("agent: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO issue (workspace_id, title, status, priority, creator_id, creator_type, number, position)
		VALUES ($1, 'cancel client', 'todo', 'high', $2, 'member', 800002, 0)
		RETURNING id`, workspaceID, userID).Scan(&issueID); err != nil {
		t.Fatalf("issue: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO agent_task_queue (agent_id, issue_id, status, priority, context, workflow_step, completed_at, error)
		VALUES ($1, $2, 'cancelled', 3, '{"wakeup_id":"33333333-3333-4333-8333-333333333333"}'::jsonb, 'todo', now(), 'Issue closed; wakeup disabled')
		RETURNING id`, agentID, issueID).Scan(&taskID); err != nil {
		t.Fatalf("task: %v", err)
	}
	t.Cleanup(func() {
		c := context.Background()
		pool.Exec(c, `DELETE FROM agent_task_queue_cancelled_client_outbox WHERE task_id = $1`, taskID)
		pool.Exec(c, `DELETE FROM agent_task_queue WHERE id = $1`, taskID)
		pool.Exec(c, `DELETE FROM issue WHERE id = $1`, issueID)
		pool.Exec(c, `DELETE FROM agent WHERE id = $1`, agentID)
		pool.Exec(c, `DELETE FROM member WHERE workspace_id = $1`, workspaceID)
		pool.Exec(c, `DELETE FROM workspace WHERE id = $1`, workspaceID)
		pool.Exec(c, `DELETE FROM "user" WHERE id = $1`, userID)
	})
	return svc, pool, ctx, workspaceID, taskID, issueID
}

func TestRecoverPendingCancelledClientNotifications_DeliversTaskCancelled(t *testing.T) {
	svc, pool, ctx, workspaceID, taskID, _ := cancelledClientNotifyFixture(t)
	ch := make(chan events.Event, 1)
	svc.Bus.Subscribe(protocol.EventTaskCancelled, func(e events.Event) {
		select {
		case ch <- e:
		default:
		}
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO agent_task_queue_cancelled_client_outbox (task_id, next_attempt_at)
		VALUES ($1::uuid, now())`, taskID); err != nil {
		t.Fatalf("seed outbox: %v", err)
	}
	result, err := svc.RecoverPendingCancelledClientNotifications(ctx, CancelledClientNotificationSweepBatchSize)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if result.Delivered != 1 {
		t.Fatalf("delivered = %d", result.Delivered)
	}
	select {
	case evt := <-ch:
		if evt.WorkspaceID != workspaceID || evt.TaskID != taskID {
			t.Fatalf("event = %+v", evt)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("missing task:cancelled publish")
	}
	var remaining int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue_cancelled_client_outbox WHERE task_id = $1::uuid`, taskID).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("outbox row must be removed, got %d", remaining)
	}
}

func TestRecoverPendingCancelledClientNotifications_SettlesUnrelatedTerminalRow(t *testing.T) {
	svc, pool, ctx, _, taskID, _ := cancelledClientNotifyFixture(t)
	if _, err := pool.Exec(ctx, `UPDATE agent_task_queue SET error = 'Stopped by user' WHERE id=$1::uuid`, taskID); err != nil {
		t.Fatalf("clear terminal-close marker: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO agent_task_queue_cancelled_client_outbox (task_id, next_attempt_at)
		VALUES ($1::uuid, now())`, taskID); err != nil {
		t.Fatalf("seed outbox: %v", err)
	}
	result, err := svc.RecoverPendingCancelledClientNotifications(ctx, CancelledClientNotificationSweepBatchSize)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if result.Settled != 1 {
		t.Fatalf("settled = %d", result.Settled)
	}
}

func TestDeliverCancelledClientNotification_KeepsObligationUntilBusReturns(t *testing.T) {
	_, pool, ctx, workspaceID, taskID, _ := cancelledClientNotifyFixture(t)
	if _, err := pool.Exec(ctx, `
		INSERT INTO agent_task_queue_cancelled_client_outbox (task_id, next_attempt_at)
		VALUES ($1::uuid, now())`, taskID); err != nil {
		t.Fatalf("seed outbox: %v", err)
	}
	parsed, err := util.ParseUUID(taskID)
	if err != nil {
		t.Fatal(err)
	}
	config := pool.Config()
	config.MaxConns, config.MinConns = 1, 0
	singleConnection, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer singleConnection.Close()
	svc := NewTaskService(db.New(singleConnection), singleConnection, nil, events.New())
	deliverCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	done := make(chan struct{})
	svc.Bus.Subscribe(protocol.EventTaskCancelled, func(e events.Event) {
		var during int
		if qerr := singleConnection.QueryRow(deliverCtx, `SELECT count(*) FROM agent_task_queue_cancelled_client_outbox WHERE task_id = $1::uuid`, taskID).Scan(&during); qerr != nil {
			t.Errorf("obligation probe: %v", qerr)
		} else if during != 1 {
			t.Errorf("obligation during bus dispatch = %d, want 1", during)
		}
		if result, err := svc.RecoverPendingCancelledClientNotifications(deliverCtx, 1); err != nil || result.Scanned != 0 {
			t.Errorf("competing sweep saw the in-flight notification: result=%+v err=%v", result, err)
		}
		if e.WorkspaceID != workspaceID || e.TaskID != taskID {
			t.Errorf("event = %+v", e)
		}
		close(done)
	})
	if _, err := svc.deliverNativeCancelledClientNotification(deliverCtx, workspaceID, parsed); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("missing task:cancelled")
	}
	var after int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue_cancelled_client_outbox WHERE task_id = $1::uuid`, taskID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != 0 {
		t.Fatalf("obligation after ack = %d, want 0", after)
	}
}

func TestCompleteNativeTerminalCloseAfterWrite_DeliversAndClearsClientOutbox(t *testing.T) {
	svc, pool, ctx, workspaceID, taskID, issueID := cancelledClientNotifyFixture(t)
	if _, err := pool.Exec(ctx, `
		INSERT INTO agent_task_queue_cancelled_client_outbox (task_id, next_attempt_at)
		VALUES ($1::uuid, now() - interval '1 second')`, taskID); err != nil {
		t.Fatalf("seed outbox: %v", err)
	}
	ch := make(chan events.Event, 1)
	svc.Bus.Subscribe(protocol.EventTaskCancelled, func(e events.Event) {
		select {
		case ch <- e:
		default:
		}
	})
	parsed, err := util.ParseUUID(taskID)
	if err != nil {
		t.Fatal(err)
	}
	task, err := svc.Queries.GetAgentTask(ctx, parsed)
	if err != nil {
		t.Fatal(err)
	}
	svc.CompleteNativeTerminalCloseAfterWrite(ctx, workspaceID, []db.AgentTaskQueue{task})
	select {
	case evt := <-ch:
		if evt.TaskID != taskID {
			t.Fatalf("event task = %s", evt.TaskID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("missing task:cancelled from fast path")
	}
	var remaining int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue_cancelled_client_outbox WHERE task_id = $1::uuid`, taskID).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("fast path must clear outbox, got %d", remaining)
	}
	_ = issueID
}
