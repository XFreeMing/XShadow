package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/redis/go-redis/v9"

	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// The single-commit boundary: the generation, its chat session, the task, the
// prompt message and the generation -> task binding belong to one transaction
// with one final commit. These tests inject failures at every statement and at
// the commit itself, watch the rows from an independent connection while the
// transaction is still open, and prove a retry converges on exactly one
// committed run.

var errInjectedPlanFault = fmt.Errorf("injected goal plan transaction fault")

// planFaultTx decorates a real transaction. The hook runs before the
// statement executes, so returning an error fails the statement inside the
// caller's transaction and exercises the same rollback a database error would.
type planFaultTx struct {
	pgx.Tx
	hook   func(ctx context.Context, sql string) error
	commit func(ctx context.Context, tx pgx.Tx) error
}

func (t *planFaultTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if err := t.hook(ctx, sql); err != nil {
		return pgconn.CommandTag{}, err
	}
	return t.Tx.Exec(ctx, sql, args...)
}

func (t *planFaultTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if err := t.hook(ctx, sql); err != nil {
		return nil, err
	}
	return t.Tx.Query(ctx, sql, args...)
}

func (t *planFaultTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if err := t.hook(ctx, sql); err != nil {
		return planFaultRow{err: err}
	}
	return t.Tx.QueryRow(ctx, sql, args...)
}

func (t *planFaultTx) Commit(ctx context.Context) error {
	if t.commit != nil {
		return t.commit(ctx, t.Tx)
	}
	return t.Tx.Commit(ctx)
}

type planFaultRow struct{ err error }

func (r planFaultRow) Scan(...any) error { return r.err }

type planFaultStarter struct {
	base   txStarter
	hook   func(ctx context.Context, sql string) error
	commit func(ctx context.Context, tx pgx.Tx) error
}

func (s *planFaultStarter) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := s.base.Begin(ctx)
	if err != nil {
		return nil, err
	}
	hook := s.hook
	if hook == nil {
		hook = func(context.Context, string) error { return nil }
	}
	return &planFaultTx{Tx: tx, hook: hook, commit: s.commit}, nil
}

// swapPlanTxStarter installs a transaction decorator for the duration of one
// request. The returned restore func lets a test drop it before retrying.
func swapPlanTxStarter(t *testing.T, starter txStarter) (restore func()) {
	t.Helper()
	previous := testHandler.TxStarter
	testHandler.TxStarter = starter
	restored := false
	restore = func() {
		if !restored {
			testHandler.TxStarter = previous
			restored = true
		}
	}
	t.Cleanup(restore)
	return restore
}

// failOnStatement returns a hook that injects an error the first time a
// statement contains marker. The statement never reaches the database.
func failOnStatement(marker string, seen *bool) func(context.Context, string) error {
	var mu sync.Mutex
	return func(_ context.Context, sql string) error {
		if !strings.Contains(sql, marker) {
			return nil
		}
		mu.Lock()
		defer mu.Unlock()
		if *seen {
			return nil
		}
		*seen = true
		return errInjectedPlanFault
	}
}

// planRunCounts counts the rows one request could have left behind, scoped by
// the fixture ids this test created: its own goal and its own agent (and
// therefore its own sessions, tasks and messages).
type planRunCounts struct {
	generations int
	sessions    int
	tasks       int
	messages    int
}

func countPlanRun(t *testing.T, goalID, agentID string) planRunCounts {
	t.Helper()
	ctx := context.Background()
	var c planRunCounts
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM goal_plan_generation WHERE goal_id = $1`, goalID).Scan(&c.generations); err != nil {
		t.Fatalf("count generations: %v", err)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM chat_session WHERE agent_id = $1`, agentID).Scan(&c.sessions); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE agent_id = $1`, agentID).Scan(&c.tasks); err != nil {
		t.Fatalf("count tasks: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
		SELECT count(*) FROM chat_message m
		JOIN chat_session s ON s.id = m.chat_session_id
		WHERE s.agent_id = $1`, agentID).Scan(&c.messages); err != nil {
		t.Fatalf("count messages: %v", err)
	}
	return c
}

func (c planRunCounts) assertEmpty(t *testing.T, when string) {
	t.Helper()
	if c.generations != 0 || c.sessions != 0 || c.tasks != 0 || c.messages != 0 {
		t.Fatalf("%s: residual rows generation=%d session=%d task=%d message=%d, want none",
			when, c.generations, c.sessions, c.tasks, c.messages)
	}
}

func (c planRunCounts) assertOneRun(t *testing.T, when string) {
	t.Helper()
	if c.generations != 1 || c.sessions != 1 || c.tasks != 1 || c.messages != 1 {
		t.Fatalf("%s: generation=%d session=%d task=%d message=%d, want exactly one of each",
			when, c.generations, c.sessions, c.tasks, c.messages)
	}
}

// planTestRuntimeAndAgent gives one test its own runtime and agent, so claim
// and residue assertions cannot observe another test's rows.
func planTestRuntimeAndAgent(t *testing.T, name string) (runtimeID, agentID string) {
	t.Helper()
	runtimeID = dbfx.Runtime(t, name+" runtime")
	agentID = dbfx.Agent(t, name+" agent", runtimeID, testutil.Cols{
		"visibility":      "workspace",
		"permission_mode": "public_to",
		"instructions":    "",
		"custom_env":      testutil.Raw("'{}'::jsonb"),
		"custom_args":     testutil.Raw("'[]'::jsonb"),
		"mcp_config":      []byte("[]"),
	})
	dbfx.Exec(t, `
		INSERT INTO agent_invocation_target (agent_id, target_type, target_id)
		VALUES ($1, 'workspace', $2)
		ON CONFLICT (agent_id, target_type, target_id) DO NOTHING
	`, agentID, testWorkspaceID)
	return runtimeID, agentID
}

// cleanupPlanRun removes the rows a generation request created. It is safe to
// call when none exist.
func cleanupPlanRun(t *testing.T, goalID, agentID string) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		testPool.Exec(ctx, `
			DELETE FROM agent_task_queue
			WHERE chat_session_id IN (SELECT id FROM chat_session WHERE agent_id = $1)`, agentID)
		testPool.Exec(ctx, `
			DELETE FROM chat_message
			WHERE chat_session_id IN (SELECT id FROM chat_session WHERE agent_id = $1)`, agentID)
		testPool.Exec(ctx, `DELETE FROM chat_session WHERE agent_id = $1`, agentID)
		testPool.Exec(ctx, `DELETE FROM goal_plan_generation WHERE goal_id = $1`, goalID)
	})
}

func planGenerationRow(t *testing.T, goalID, requestKey string) (db.GoalPlanGeneration, bool) {
	t.Helper()
	row, err := testHandler.Queries.GetGoalPlanGenerationByRequestKey(context.Background(),
		db.GetGoalPlanGenerationByRequestKeyParams{
			WorkspaceID: parseUUID(testWorkspaceID),
			GoalID:      parseUUID(goalID),
			RequestKey:  requestKey,
		})
	if err == pgx.ErrNoRows {
		return db.GoalPlanGeneration{}, false
	}
	if err != nil {
		t.Fatalf("load generation by request key: %v", err)
	}
	return row, true
}

func planGenerationBody(requestKey, intent, agentID string, revision int32) map[string]any {
	return map[string]any{
		"intent":            intent,
		"request_key":       requestKey,
		"expected_revision": revision,
		"agent_id":          agentID,
	}
}

// planServerRoot resolves the Go module root from the test's package directory
// (server/internal/handler), so the chain test builds the same checkout.
func planServerRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	return filepath.Dir(filepath.Dir(wd))
}

// newPlanResultServer exposes the result endpoint with the real auth stack so
// the CLI makes an ordinary authenticated HTTP call.
func newPlanResultServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(goalPlanResultRouter())
	t.Cleanup(srv.Close)
	return srv
}

// TestGoalPlanGenerationFaultsLeaveNoResidue injects a failure at each write on
// the run's path. Every case must answer 500 and leave nothing behind: the
// independent connection sees no generation, session, task or message, and the
// goal can move to another project because no preparing row blocks it.
func TestGoalPlanGenerationFaultsLeaveNoResidue(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	cases := []struct {
		name   string
		marker string
	}{
		{"after generation before session", "INSERT INTO chat_session ("},
		{"after session before task", "INSERT INTO agent_task_queue ("},
		{"after task before prompt message", "INSERT INTO chat_message ("},
		{"at generation binding", "UPDATE goal_plan_generation SET"},
		{"after binding at session touch", "UPDATE chat_session SET updated_at"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			project := createTestProjectForResources(t, "Plan fault project "+tc.name)
			goal := planTestGoal(t, project.ID)
			_, agentID := planTestRuntimeAndAgent(t, "Plan fault "+tc.name)
			cleanupPlanRun(t, goal.ID, agentID)

			seen := false
			starter := &planFaultStarter{base: testHandler.TxStarter, hook: failOnStatement(tc.marker, &seen)}
			restore := swapPlanTxStarter(t, starter)

			code, _ := postGeneration(t, goal.ID, planGenerationBody("fault", "fault injection", agentID, 0))
			restore()
			if code != 500 {
				t.Fatalf("CreateGoalPlanGeneration = %d, want 500 after injected %q failure", code, tc.marker)
			}
			if !seen {
				t.Fatalf("injected failure %q never matched a statement", tc.marker)
			}
			countPlanRun(t, goal.ID, agentID).assertEmpty(t, "after "+tc.name)

			// No generation row blocks a project move.
			target := createTestProjectForResources(t, "Plan fault target "+tc.name)
			if code, moved := updateGoalViaAPI(t, testUserID, goal.ID, map[string]any{"project_id": target.ID}); code != 200 || moved.ProjectID == nil || *moved.ProjectID != target.ID {
				t.Fatalf("goal project move after rollback = %d %+v, want 200 with the new project", code, moved)
			}
		})
	}
}

// TestGoalPlanGenerationUncommittedRowsAreInvisible holds the transaction open
// after the task exists. An independent connection — including the daemon's
// claim path — must see none of it; after the commit it must see the complete
// run and be able to claim the task without any notification.
func TestGoalPlanGenerationUncommittedRowsAreInvisible(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	project := createTestProjectForResources(t, "Plan invisibility project")
	goal := planTestGoal(t, project.ID)
	runtimeID, agentID := planTestRuntimeAndAgent(t, "Plan invisibility")
	cleanupPlanRun(t, goal.ID, agentID)

	reached := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	starter := &planFaultStarter{base: testHandler.TxStarter, hook: func(_ context.Context, sql string) error {
		if strings.Contains(sql, "INSERT INTO chat_message (") {
			once.Do(func() { close(reached) })
			<-release
		}
		return nil
	}}
	restore := swapPlanTxStarter(t, starter)
	defer restore()

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- postGenerationRaw(goal.ID, planGenerationBody("invisibility", "hold the transaction", agentID, 0))
	}()

	select {
	case <-reached:
	case <-time.After(20 * time.Second):
		t.Fatal("generation never reached the prompt message insert")
	}

	countPlanRun(t, goal.ID, agentID).assertEmpty(t, "while the transaction is open")

	claimCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	claimed, err := testHandler.TaskService.ClaimTaskForRuntime(claimCtx, parseUUID(runtimeID))
	cancel()
	if err != nil {
		t.Fatalf("claim before commit: %v", err)
	}
	if claimed != nil {
		t.Fatalf("claim before commit returned task %s; uncommitted work must be invisible", uuidToString(claimed.ID))
	}

	close(release)
	var result *httptest.ResponseRecorder
	select {
	case result = <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("generation did not finish after the insert was released")
	}
	if result.Code != 202 {
		t.Fatalf("CreateGoalPlanGeneration = %d %s, want 202", result.Code, result.Body.String())
	}
	countPlanRun(t, goal.ID, agentID).assertOneRun(t, "after commit")

	// The same poll-based claim path now finds the durable task with no
	// explicit notification in the test.
	claimCtx, cancel = context.WithTimeout(ctx, 5*time.Second)
	claimed, err = testHandler.TaskService.ClaimTaskForRuntime(claimCtx, parseUUID(runtimeID))
	cancel()
	if err != nil {
		t.Fatalf("claim after commit: %v", err)
	}
	if claimed == nil {
		t.Fatal("claim after commit returned no task; a committed queued task must be claimable")
	}
	if claimed.AgentID != parseUUID(agentID) {
		t.Fatalf("claimed agent = %s, want %s", uuidToString(claimed.AgentID), agentID)
	}
	if !claimed.ChatSessionID.Valid {
		t.Fatal("claimed task has no chat session")
	}
	var sessionAgent string
	if err := testPool.QueryRow(ctx, `SELECT agent_id FROM chat_session WHERE id = $1`,
		uuidToString(claimed.ChatSessionID)).Scan(&sessionAgent); err != nil {
		t.Fatalf("load claimed session: %v", err)
	}
	if sessionAgent != agentID {
		t.Fatalf("claimed task session agent = %s, want %s", sessionAgent, agentID)
	}
}

// TestGoalPlanGenerationCommitOutcomeUnknown covers a commit that actually
// landed while the client saw an error (lost response). The handler must not
// write a failure or delete the session: it re-reads the request key and
// reports the committed run, and a retry adds nothing.
func TestGoalPlanGenerationCommitOutcomeUnknown(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	project := createTestProjectForResources(t, "Plan unknown commit project")
	goal := planTestGoal(t, project.ID)
	runtimeID, agentID := planTestRuntimeAndAgent(t, "Plan unknown commit")
	cleanupPlanRun(t, goal.ID, agentID)

	starter := &planFaultStarter{
		base: testHandler.TxStarter,
		commit: func(ctx context.Context, tx pgx.Tx) error {
			if err := tx.Commit(ctx); err != nil {
				return err
			}
			return errInjectedPlanFault // committed, but the caller cannot know
		},
	}
	restore := swapPlanTxStarter(t, starter)
	body := planGenerationBody("unknown-commit", "commit landed", agentID, 0)
	code, created := postGeneration(t, goal.ID, body)
	restore()
	if code != 200 {
		t.Fatalf("CreateGoalPlanGeneration = %d, want 200 resolved from the committed row", code)
	}
	if created.TaskID == nil || created.ChatSessionID == nil || created.State != "queued" {
		t.Fatalf("resolved run = %+v, want a queued run with task and session", created)
	}
	countPlanRun(t, goal.ID, agentID).assertOneRun(t, "after an unconfirmed commit")

	// The retry resolves to the same committed rows.
	code, replay := postGeneration(t, goal.ID, body)
	if code != 200 {
		t.Fatalf("same-key retry = %d, want 200 replay", code)
	}
	if replay.ID != created.ID || replay.TaskID == nil || *replay.TaskID != *created.TaskID {
		t.Fatalf("replay = id %s task %v, want the original %s/%s", replay.ID, replay.TaskID, created.ID, *created.TaskID)
	}
	countPlanRun(t, goal.ID, agentID).assertOneRun(t, "after the same-key retry")

	claimCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	claimed, err := testHandler.TaskService.ClaimTaskForRuntime(claimCtx, parseUUID(runtimeID))
	cancel()
	if err != nil || claimed == nil {
		t.Fatalf("claim the committed task = %v/%v, want the task", claimed, err)
	}
}

// TestGoalPlanGenerationCommitFailureRollsBackAndRetryCommitsOnce simulates a
// commit that did not land. The handler answers a retryable error, nothing is
// persisted, and the retry with the same key creates exactly one run.
func TestGoalPlanGenerationCommitFailureRollsBackAndRetryCommitsOnce(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	project := createTestProjectForResources(t, "Plan failed commit project")
	goal := planTestGoal(t, project.ID)
	_, agentID := planTestRuntimeAndAgent(t, "Plan failed commit")
	cleanupPlanRun(t, goal.ID, agentID)

	starter := &planFaultStarter{
		base: testHandler.TxStarter,
		commit: func(ctx context.Context, tx pgx.Tx) error {
			_ = tx.Rollback(ctx)
			return errInjectedPlanFault
		},
	}
	restore := swapPlanTxStarter(t, starter)
	body := planGenerationBody("failed-commit", "commit never landed", agentID, 0)
	code, _ := postGeneration(t, goal.ID, body)
	restore()
	if code != 503 {
		t.Fatalf("CreateGoalPlanGeneration = %d, want a retryable 503", code)
	}
	countPlanRun(t, goal.ID, agentID).assertEmpty(t, "after a failed commit")

	code, created := postGeneration(t, goal.ID, body)
	if code != 202 {
		t.Fatalf("same-key retry = %d, want 202", code)
	}
	countPlanRun(t, goal.ID, agentID).assertOneRun(t, "after the retry")

	row, ok := planGenerationRow(t, goal.ID, "failed-commit")
	if !ok {
		t.Fatal("retry left no generation for its request key")
	}
	if row.State != "queued" || !row.TaskID.Valid || !row.ChatSessionID.Valid {
		t.Fatalf("retry generation state=%q task=%v session=%v, want queued and bound",
			row.State, row.TaskID.Valid, row.ChatSessionID.Valid)
	}
	if uuidToString(row.TaskID) != *created.TaskID || uuidToString(row.ChatSessionID) != *created.ChatSessionID {
		t.Fatalf("retry generation bound to %s/%s, want the response ids %s/%s",
			uuidToString(row.TaskID), uuidToString(row.ChatSessionID), *created.TaskID, *created.ChatSessionID)
	}
}

// TestGoalPlanGenerationBindingGuard pins the SQL guard: only an unbound
// preparing row may be bound, so a failed or succeeded run can never re-enter
// the queue.
func TestGoalPlanGenerationBindingGuard(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	project := createTestProjectForResources(t, "Plan binding guard project")
	goal := planTestGoal(t, project.ID)
	_, agentID := planTestRuntimeAndAgent(t, "Plan binding guard")
	cleanupPlanRun(t, goal.ID, agentID)

	insert := func(requestKey, state string) string {
		var id string
		if err := testPool.QueryRow(ctx, `
			INSERT INTO goal_plan_generation (
				workspace_id, goal_id, project_id, request_key, intent, agent_id,
				base_revision, state, input_context
			) VALUES ($1, $2, $3, $4, 'seed', $5, 0, $6, '{}'::jsonb)
			RETURNING id`,
			testWorkspaceID, goal.ID, project.ID, requestKey, agentID, state,
		).Scan(&id); err != nil {
			t.Fatalf("seed %s generation: %v", state, err)
		}
		return id
	}
	bind := func(id, sessionID, taskID string) error {
		_, err := testHandler.Queries.SetGoalPlanGenerationRun(ctx, db.SetGoalPlanGenerationRunParams{
			ChatSessionID: parseUUID(sessionID),
			TaskID:        parseUUID(taskID),
			ID:            parseUUID(id),
			WorkspaceID:   parseUUID(testWorkspaceID),
		})
		return err
	}
	randomUUID := func() string {
		var id string
		if err := testPool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
			t.Fatalf("generate uuid: %v", err)
		}
		return id
	}

	for _, terminal := range []string{"failed", "succeeded"} {
		terminal := terminal
		t.Run(terminal, func(t *testing.T) {
			id := insert("guard-"+terminal, terminal)
			if err := bind(id, randomUUID(), randomUUID()); err != pgx.ErrNoRows {
				t.Fatalf("bind %s generation err = %v, want pgx.ErrNoRows", terminal, err)
			}
			var state string
			var taskID, sessionID pgtype.UUID
			if err := testPool.QueryRow(ctx, `
				SELECT state, task_id, chat_session_id FROM goal_plan_generation WHERE id = $1`, id,
			).Scan(&state, &taskID, &sessionID); err != nil {
				t.Fatalf("re-read %s generation: %v", terminal, err)
			}
			if state != terminal || taskID.Valid || sessionID.Valid {
				t.Fatalf("%s generation after refused bind = state %q task %v session %v, want unchanged and unbound",
					terminal, state, taskID.Valid, sessionID.Valid)
			}
		})
	}

	t.Run("preparing", func(t *testing.T) {
		id := insert("guard-preparing", "preparing")
		sessionID, taskID := randomUUID(), randomUUID()
		if err := bind(id, sessionID, taskID); err != nil {
			t.Fatalf("bind preparing generation: %v", err)
		}
		if err := bind(id, sessionID, taskID); err != pgx.ErrNoRows {
			t.Fatalf("second bind err = %v, want pgx.ErrNoRows (already bound)", err)
		}
	})
}

// TestGoalPlanGenerationConcurrentSameKey runs two requests with one key at
// once. The advisory lock serialises them: one creates, one replays, and the
// database holds exactly one run.
func TestGoalPlanGenerationConcurrentSameKey(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	project := createTestProjectForResources(t, "Plan concurrent key project")
	goal := planTestGoal(t, project.ID)
	_, agentID := planTestRuntimeAndAgent(t, "Plan concurrent key")
	cleanupPlanRun(t, goal.ID, agentID)

	body := planGenerationBody("concurrent", "same key twice", agentID, 0)
	var wg sync.WaitGroup
	responses := make([]planResponse, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec := postGenerationRaw(goal.ID, body)
			responses[i] = planResponse{code: rec.Code, body: rec.Body.Bytes()}
		}(i)
	}
	wg.Wait()

	var created, replayed int
	var ids []string
	for _, resp := range responses {
		switch resp.code {
		case 202:
			created++
		case 200:
			replayed++
		default:
			t.Fatalf("concurrent request = %d %s, want 202 or 200", resp.code, resp.body)
		}
		var parsed GoalPlanGenerationResponse
		if err := json.Unmarshal(resp.body, &parsed); err != nil {
			t.Fatalf("decode concurrent response: %v", err)
		}
		ids = append(ids, parsed.ID)
	}
	if created != 1 || replayed != 1 {
		t.Fatalf("concurrent outcomes = %d created / %d replayed, want 1/1", created, replayed)
	}
	if ids[0] != ids[1] {
		t.Fatalf("concurrent requests resolved to different generations %s and %s", ids[0], ids[1])
	}
	countPlanRun(t, goal.ID, agentID).assertOneRun(t, "after concurrent same-key requests")

	// The same key with different parameters is refused and writes nothing.
	code, _ := postGeneration(t, goal.ID, planGenerationBody("concurrent", "different intent", agentID, 0))
	if code != 409 {
		t.Fatalf("different-parameter request = %d, want 409", code)
	}
	countPlanRun(t, goal.ID, agentID).assertOneRun(t, "after a refused different-parameter request")
}

// planResponse carries a goroutine hand-off without testing.T.
type planResponse struct {
	code int
	body []byte
}

// TestGoalPlanGenerationReplaySurvivesPreparationFailure proves the stashed
// preparation error does not block a saved replay: after the agent is
// archived, the original request key still resolves to its run, while a new
// request key is refused and writes nothing.
func TestGoalPlanGenerationReplaySurvivesPreparationFailure(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	project := createTestProjectForResources(t, "Plan replay after archive project")
	goal := planTestGoal(t, project.ID)
	_, agentID := planTestRuntimeAndAgent(t, "Plan replay after archive")
	cleanupPlanRun(t, goal.ID, agentID)

	body := planGenerationBody("replay-after-archive", "survive preparation", agentID, 0)
	code, created := postGeneration(t, goal.ID, body)
	if code != 202 {
		t.Fatalf("initial generation = %d, want 202", code)
	}

	dbfx.Exec(t, `UPDATE agent SET archived_at = now() WHERE id = $1`, agentID)
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `UPDATE agent SET archived_at = NULL WHERE id = $1`, agentID)
	})

	code, replay := postGeneration(t, goal.ID, body)
	if code != 200 || replay.ID != created.ID || replay.TaskID == nil || *replay.TaskID != *created.TaskID {
		t.Fatalf("replay after archive = %d %+v, want the original run %s", code, replay, created.ID)
	}
	countPlanRun(t, goal.ID, agentID).assertOneRun(t, "after replaying with an archived agent")

	code, _ = postGeneration(t, goal.ID, planGenerationBody("new-after-archive", "new request", agentID, 0))
	if code != 409 {
		t.Fatalf("new request with an archived agent = %d, want 409", code)
	}
	countPlanRun(t, goal.ID, agentID).assertOneRun(t, "after a refused new request")

	dbfx.Exec(t, `UPDATE agent SET archived_at = NULL WHERE id = $1`, agentID)
	code, _ = postGeneration(t, goal.ID, planGenerationBody("new-after-archive", "new request", agentID, 0))
	if code != 202 {
		t.Fatalf("new request after unarchive = %d, want 202", code)
	}
	// The original run and the replacement run are both present; the refusal
	// above added nothing.
	counts := countPlanRun(t, goal.ID, agentID)
	if counts.generations != 2 || counts.sessions != 2 || counts.tasks != 2 || counts.messages != 2 {
		t.Fatalf("after the replacement run: generation=%d session=%d task=%d message=%d, want two complete runs",
			counts.generations, counts.sessions, counts.tasks, counts.messages)
	}
}

// TestGoalPlanRunReadChainThroughCLI walks the chain this checkout can actually
// run without a model: the handler fixes the prompt, the daemon's shared claim
// path claims the task, a test-built fake executor parses the resource ref out
// of that prompt, reads a controlled file, and hands the plan to this build's
// CLI; the CLI's HTTP call stores the candidate. The evidence stored is the
// content the executor actually read, not a hand-filled flag.
func TestGoalPlanRunReadChainThroughCLI(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	if testing.Short() {
		t.Skip("builds the CLI binary")
	}
	ctx := context.Background()

	dataDir := t.TempDir()
	contextPath := filepath.Join(dataDir, "context.md")
	const fileVersion = "ctx-v7"
	const fileProject = "project: xshadow-goal-dev"
	if err := os.WriteFile(contextPath, []byte("version: "+fileVersion+"\n"+fileProject+"\n"), 0600); err != nil {
		t.Fatal(err)
	}

	project := createTestProjectForResources(t, "Plan read chain project")
	var resource ProjectResourceResponse
	testutil.Call(t, testHandler.CreateProjectResource, withURLParam(
		newRequest("POST", "/api/projects/"+project.ID+"/resources", map[string]any{
			"resource_type": "local_directory",
			"resource_ref": map[string]any{
				"local_path": dataDir,
				"daemon_id":  "plan-read-chain-test-daemon",
				"label":      "plan context",
			},
		}), "id", project.ID)).Want(201).JSON(&resource)

	goal := planTestGoal(t, project.ID)
	runtimeID, agentID := planTestRuntimeAndAgent(t, "Plan read chain")
	cleanupPlanRun(t, goal.ID, agentID)

	code, created := postGeneration(t, goal.ID, planGenerationBody("read-chain", "plan from the project's files", agentID, 0))
	if code != 202 {
		t.Fatalf("CreateGoalPlanGeneration = %d, want 202", code)
	}
	if created.TaskID == nil || created.ChatSessionID == nil {
		t.Fatalf("generation = %+v, want a task and session", created)
	}

	// Daemon side: the shared poll claim path picks the queued task.
	claimCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	claimed, err := testHandler.TaskService.ClaimTaskForRuntime(claimCtx, parseUUID(runtimeID))
	cancel()
	if err != nil {
		t.Fatalf("claim plan task: %v", err)
	}
	if claimed == nil || uuidToString(claimed.ID) != *created.TaskID {
		t.Fatalf("claimed task = %+v, want %s", claimed, *created.TaskID)
	}

	// The prompt is what the daemon hands the executor. It must name the real
	// resource ref the fake executor will read.
	var prompt string
	dbfx.QueryRow(t, `SELECT content FROM chat_message WHERE chat_session_id = $1 AND role = 'user'`,
		*created.ChatSessionID).Scan(&prompt)
	if !strings.Contains(prompt, "id="+resource.ID) || !strings.Contains(prompt, dataDir) {
		t.Fatalf("prompt does not name the resource and its local path:\n%s", prompt)
	}
	promptPath := filepath.Join(t.TempDir(), "prompt.txt")
	if err := os.WriteFile(promptPath, []byte(prompt), 0600); err != nil {
		t.Fatal(err)
	}

	cliPath := filepath.Join(t.TempDir(), "multica")
	serverRoot := planServerRoot(t)
	build := exec.CommandContext(ctx, "go", "build", "-o", cliPath, "./cmd/multica")
	build.Dir = serverRoot
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build this checkout's CLI: %v\n%s", err, out)
	}

	// The fake executor is written by the test; it never looks up or runs a
	// user-installed agent/model CLI.
	executorDir := t.TempDir()
	executorPath := filepath.Join(executorDir, "fake-plan-executor.sh")
	planPath := filepath.Join(executorDir, "plan.json")
	executor := `set -eu
prompt_file="$1"; plan_out="$2"; cli="$3"; goal="$4"; generation="$5"; resource_id="$6"
line=$(grep -F "id=$resource_id" "$prompt_file" | head -n1)
[ -n "$line" ] || { echo "resource not present in prompt" >&2; exit 1; }
ref=${line#* ref=}
path=$(printf '%s' "$ref" | sed -n 's/.*"local_path"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')
[ -n "$path" ] || { echo "local_path not present in ref: [$ref] line=[$line]" >&2; exit 1; }
content=$(tr '\n' ' ' < "$path/context.md")
version=$(sed -n 's/^version: //p' "$path/context.md" | head -n1)
[ -n "$version" ] || { echo "context file has no version" >&2; exit 1; }
cat > "$plan_out" <<JSON
{"items":[{"title":"deliverable read from the project context","binding":{"type":"new"}}],
 "edges":[],
 "context_snapshot":{"files":[{"path":"context.md","version":"$version","summary":"$content"}],
 "resources":[{"id":"$resource_id","read":true}],
 "read_at":"2026-11-12T00:00:00Z"}}
JSON
exec "$cli" goal plan-result --goal "$goal" --generation "$generation" --file "$plan_out" --output json
`
	if err := os.WriteFile(executorPath, []byte(executor), 0700); err != nil {
		t.Fatal(err)
	}

	srv := newPlanResultServer(t)
	token := mintGoalPlanTaskToken(t, testWorkspaceID, *created.TaskID, agentID)

	run := exec.CommandContext(ctx, "sh", executorPath,
		promptPath, planPath, cliPath, goal.ID, created.ID, resource.ID)
	run.Env = append(os.Environ(),
		"MULTICA_SERVER_URL="+srv.URL,
		"MULTICA_WORKSPACE_ID="+testWorkspaceID,
		"MULTICA_TOKEN="+token,
		"MULTICA_AGENT_ID="+agentID,
		"MULTICA_TASK_ID="+*created.TaskID,
		"HOME="+t.TempDir(),
	)
	out, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("fake executor + CLI failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), `"state":"succeeded"`) {
		t.Fatalf("CLI output = %s, want a succeeded result", out)
	}

	row, ok := planGenerationRow(t, goal.ID, "read-chain")
	if !ok {
		t.Fatal("CLI result left no generation")
	}
	if row.State != "succeeded" || len(row.Candidate) == 0 {
		t.Fatalf("generation state=%q candidate=%s, want succeeded with a candidate", row.State, row.Candidate)
	}
	var candidate GoalPlanCandidate
	if err := json.Unmarshal(row.Candidate, &candidate); err != nil {
		t.Fatalf("decode stored candidate: %v", err)
	}
	var evidence struct {
		Files []struct {
			Path    string `json:"path"`
			Version string `json:"version"`
			Summary string `json:"summary"`
		} `json:"files"`
		Resources []struct {
			ID   string `json:"id"`
			Read bool   `json:"read"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(candidate.ContextSnapshot, &evidence); err != nil {
		t.Fatalf("decode candidate evidence: %v", err)
	}
	if len(evidence.Files) != 1 || evidence.Files[0].Path != "context.md" ||
		evidence.Files[0].Version != fileVersion || !strings.Contains(evidence.Files[0].Summary, fileProject) {
		t.Fatalf("stored evidence = %+v, want the version and content the executor read from %s",
			evidence.Files, contextPath)
	}
	if len(evidence.Resources) != 1 || evidence.Resources[0].ID != resource.ID || !evidence.Resources[0].Read {
		t.Fatalf("stored resource evidence = %+v, want %s read", evidence.Resources, resource.ID)
	}
	var inputContext []byte
	dbfx.QueryRow(t, `SELECT input_context FROM goal_plan_generation WHERE id = $1`, created.ID).Scan(&inputContext)
	var fixedInput, storedInput map[string]any
	if err := json.Unmarshal(created.InputContext, &fixedInput); err != nil {
		t.Fatalf("decode response input context: %v", err)
	}
	if err := json.Unmarshal(inputContext, &storedInput); err != nil {
		t.Fatalf("decode stored input context: %v", err)
	}
	if !reflect.DeepEqual(fixedInput, storedInput) {
		t.Fatalf("stored input context changed across the run: %v != %v", storedInput, fixedInput)
	}
}

// planClaimRedis is an in-memory stand-in for the Redis commands EmptyClaimCache
// uses. It keeps the version semantics real so the claim path's invalidation
// contract runs without a Redis service; every other command is unimplemented.
type planClaimRedis struct {
	redis.UniversalClient
	mu     sync.Mutex
	values map[string]string
}

func newPlanClaimRedis() *planClaimRedis {
	return &planClaimRedis{values: make(map[string]string)}
}

func (r *planClaimRedis) Get(_ context.Context, key string) *redis.StringCmd {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, ok := r.values[key]
	if !ok {
		return redis.NewStringResult("", redis.Nil)
	}
	return redis.NewStringResult(value, nil)
}

func (r *planClaimRedis) Set(_ context.Context, key string, value any, _ time.Duration) *redis.StatusCmd {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.values[key] = fmt.Sprint(value)
	return redis.NewStatusResult("OK", nil)
}

func (r *planClaimRedis) Incr(_ context.Context, key string) *redis.IntCmd {
	r.mu.Lock()
	defer r.mu.Unlock()
	n, _ := strconv.Atoi(r.values[key])
	n++
	r.values[key] = strconv.Itoa(n)
	return redis.NewIntResult(int64(n), nil)
}

func (r *planClaimRedis) Expire(_ context.Context, _ string, _ time.Duration) *redis.BoolCmd {
	return redis.NewBoolResult(true, nil)
}

func (r *planClaimRedis) Pipeline() redis.Pipeliner {
	return &planClaimPipeline{cache: r}
}

type planClaimPipeline struct {
	redis.Pipeliner
	cache *planClaimRedis
}

func (p *planClaimPipeline) Get(ctx context.Context, key string) *redis.StringCmd {
	return p.cache.Get(ctx, key)
}

func (p *planClaimPipeline) Incr(ctx context.Context, key string) *redis.IntCmd {
	return p.cache.Incr(ctx, key)
}

func (p *planClaimPipeline) Expire(ctx context.Context, key string, ttl time.Duration) *redis.BoolCmd {
	return p.cache.Expire(ctx, key, ttl)
}

func (p *planClaimPipeline) Exec(context.Context) ([]redis.Cmder, error) {
	return nil, nil
}

// TestGoalPlanCommittedRunReachableThroughEmptyClaimCache walks the daemon's
// ordinary poll claim path after a committed generation. The runtime first
// caches an "empty" verdict, then the post-commit enqueue must invalidate it and
// the next poll must claim the one task. The test never calls a notifier: the
// handler's own post-commit finalize is the production path, and the claim
// itself is the existing poll path, so a wakeup alone cannot mask a stale cache.
func TestGoalPlanCommittedRunReachableThroughEmptyClaimCache(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()

	project := createTestProjectForResources(t, "Plan claim cache project")
	goal := planTestGoal(t, project.ID)
	runtimeID, agentID := planTestRuntimeAndAgent(t, "Plan claim cache")
	cleanupPlanRun(t, goal.ID, agentID)

	// One TaskService owns both the post-commit notification and the claim, as
	// the server wiring does; splitting them would leave the empty verdict stale.
	h := *testHandler
	h.TaskService = service.NewTaskService(testHandler.Queries, testHandler.TxStarter, testHandler.TaskService.Hub, testHandler.TaskService.Bus)
	h.TaskService.EmptyClaim = service.NewEmptyClaimCache(newPlanClaimRedis())
	cache := h.TaskService.EmptyClaim

	claimed, err := h.TaskService.ClaimTaskForRuntime(ctx, parseUUID(runtimeID))
	if err != nil {
		t.Fatalf("idle claim: %v", err)
	}
	if claimed != nil {
		t.Fatalf("idle claim returned a task: %+v", claimed)
	}
	if !cache.IsEmpty(ctx, runtimeID) {
		t.Fatal("precondition: an idle poll must cache the runtime's empty verdict")
	}

	req := goalRequest(http.MethodPost, "/api/goals/"+goal.ID+"/plan/generations", testUserID,
		planGenerationBody("claim-cache", "plan from the cached-empty runtime", agentID, 0),
		map[string]string{"id": goal.ID})
	rec := httptest.NewRecorder()
	h.CreateGoalPlanGeneration(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("CreateGoalPlanGeneration = %d: %s", rec.Code, rec.Body.String())
	}
	var created GoalPlanGenerationResponse
	decodePlanJSON(t, rec, &created)
	if created.TaskID == nil {
		t.Fatalf("generation response = %+v, want a task", created)
	}
	if cache.IsEmpty(ctx, runtimeID) {
		t.Fatal("post-commit enqueue must invalidate the runtime's cached empty verdict")
	}

	claimCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	claimed, err = h.TaskService.ClaimTaskForRuntime(claimCtx, parseUUID(runtimeID))
	cancel()
	if err != nil {
		t.Fatalf("claim plan task: %v", err)
	}
	if claimed == nil || uuidToString(claimed.ID) != *created.TaskID {
		t.Fatalf("claimed task = %+v, want %s", claimed, *created.TaskID)
	}
}
