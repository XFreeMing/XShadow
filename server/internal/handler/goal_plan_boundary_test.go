package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// Coverage for the three boundaries the review asked for on top of the goal
// plan draft:
//   - one protected read fixes the run's project, resources, draft revision,
//     base_revision, session project and prompt;
//   - a run created for a project the goal has left is never served, applied,
//     replayed or written back;
//   - generation and the delete paths take their locks in one order.
//
// The interleaving tests drive one real handler in a goroutine and hold the
// other side of the contention in a transaction this test owns, so the wait is
// observed through pg_blocking_pids instead of a sleep.

// planProjectWithResource creates a workspace-visible project with one github
// resource and returns both ids.
func planProjectWithResource(t *testing.T, title, url string) (projectID, resourceID string) {
	t.Helper()
	project := createTestProjectForResources(t, title)
	var resource ProjectResourceResponse
	testutil.Call(t, testHandler.CreateProjectResource, withURLParam(
		newRequest("POST", "/api/projects/"+project.ID+"/resources", map[string]any{
			"resource_type": "github_repo",
			"resource_ref":  map[string]any{"url": url, "ref": "main"},
		}), "id", project.ID)).Want(http.StatusCreated).JSON(&resource)
	return project.ID, resource.ID
}

// startGoalPlanGeneration runs the create handler in a goroutine. postGenerationRaw
// never touches testing.T, so the goroutine only hands the recorder back.
func startGoalPlanGeneration(goalID string, body map[string]any) <-chan *httptest.ResponseRecorder {
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- postGenerationRaw(goalID, body) }()
	return done
}

func awaitGoalPlanResponse(t *testing.T, done <-chan *httptest.ResponseRecorder, what string) *httptest.ResponseRecorder {
	t.Helper()
	select {
	case rec := <-done:
		return rec
	case <-time.After(20 * time.Second):
		t.Fatalf("%s did not finish", what)
		return nil
	}
}

func beginGoalPlanLockTx(t *testing.T) pgx.Tx {
	t.Helper()
	tx, err := testPool.Begin(context.Background())
	if err != nil {
		t.Fatalf("begin lock transaction: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	return tx
}

func execPlanLockTx(t *testing.T, tx pgx.Tx, sql string, args ...any) {
	t.Helper()
	if _, err := tx.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec %q: %v", strings.TrimSpace(sql), err)
	}
}

type goalPlanErrorBody struct {
	Error           string `json:"error"`
	Code            string `json:"code"`
	CurrentRevision *int32 `json:"current_revision"`
}

func getGenerationAsUserRaw(t *testing.T, userID, goalID, generationID string) *httptest.ResponseRecorder {
	t.Helper()
	return testutil.Call(t, testHandler.GetGoalPlanGeneration,
		goalRequest(http.MethodGet, "/api/goals/"+goalID+"/plan/generations/"+generationID, userID, nil,
			map[string]string{"id": goalID, "generationId": generationID})).ResponseRecorder
}

// TestGoalPlanGenerationFixesInputAtRevision pins every value a run derives from
// the protected read: the stored input context, base_revision, the chat session
// and the prompt text. A later save must not change any of them.
func TestGoalPlanGenerationFixesInputAtRevision(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	resourceURL := "https://github.com/example/fixed-input-" + t.Name()
	projectID, resourceID := planProjectWithResource(t, "Plan fixed input project "+t.Name(), resourceURL)
	goal := planTestGoal(t, projectID)
	agentID := createHandlerTestAgent(t, "Plan Fixed Input Agent", []byte("[]"))

	itemID := "018f3a9e-4444-7c2b-9f1e-000000000001"
	code, saved := putPlan(t, goal.ID, map[string]any{
		"expected_revision": 0,
		"items": []map[string]any{{
			"id":      itemID,
			"title":   "first deliverable",
			"binding": map[string]any{"type": "new"},
		}},
		"edges": []map[string]any{},
	})
	if code != http.StatusOK || saved.Revision != 1 || len(saved.Items) != 1 {
		t.Fatalf("save revision 1 = %d rev=%d items=%d, want 200/1/1", code, saved.Revision, len(saved.Items))
	}

	code, created := postGeneration(t, goal.ID, map[string]any{
		"intent":            "fix the input",
		"request_key":       "fixed-input",
		"expected_revision": 1,
		"agent_id":          agentID,
	})
	if code != http.StatusAccepted {
		t.Fatalf("CreateGoalPlanGeneration = %d, want 202", code)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE chat_session_id = $1`, *created.ChatSessionID)
		testPool.Exec(ctx, `DELETE FROM chat_message WHERE chat_session_id = $1`, *created.ChatSessionID)
		testPool.Exec(ctx, `DELETE FROM chat_session WHERE id = $1`, *created.ChatSessionID)
		testPool.Exec(ctx, `DELETE FROM goal_plan_generation WHERE goal_id = $1`, goal.ID)
	})
	if created.BaseRevision != 1 || created.CurrentRevision != 1 {
		t.Fatalf("generation revisions = %d/%d, want 1/1", created.BaseRevision, created.CurrentRevision)
	}

	var snapshot struct {
		ProjectID    string `json:"project_id"`
		PlanRevision int32  `json:"plan_revision"`
		Resources    []struct {
			ID   string `json:"id"`
			Type string `json:"type"`
			Read bool   `json:"read"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(created.InputContext, &snapshot); err != nil {
		t.Fatalf("decode input_context %s: %v", created.InputContext, err)
	}
	if snapshot.ProjectID != projectID || snapshot.PlanRevision != 1 {
		t.Fatalf("input_context project=%q plan_revision=%d, want %q/1",
			snapshot.ProjectID, snapshot.PlanRevision, projectID)
	}
	if len(snapshot.Resources) != 1 || snapshot.Resources[0].ID != resourceID || snapshot.Resources[0].Read {
		t.Fatalf("input_context resources = %+v, want one unread pointer to %s", snapshot.Resources, resourceID)
	}

	// The prompt is the run's whole instruction: it must carry the same fixed
	// revision, the saved content and only this project's resources, plus the
	// CLI hand-back command naming this generation.
	var prompt string
	dbfx.QueryRow(t, `SELECT content FROM chat_message WHERE chat_session_id = $1 AND role = 'user'`,
		*created.ChatSessionID).Scan(&prompt)
	for _, want := range []string{
		"revision 1",
		"first deliverable",
		"base_revision: 1",
		"id=" + resourceID,
		"type=github_repo",
		resourceURL,
		"goal plan-result --goal " + goal.ID,
		"--generation " + created.ID,
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt is missing %q:\n%s", want, prompt)
		}
	}

	// The user keeps editing while the run is out.
	edited := append([]GoalPlanItem{}, saved.Items...)
	edited = append(edited, GoalPlanItem{
		ID:      "018f3a9e-4444-7c2b-9f1e-000000000002",
		Title:   "manual node",
		Binding: GoalPlanItemBinding{Type: "new"},
	})
	code, second := putPlan(t, goal.ID, map[string]any{
		"expected_revision": 1,
		"items":             edited,
		"edges":             []GoalPlanEdge{},
	})
	if code != http.StatusOK || second.Revision != 2 {
		t.Fatalf("save revision 2 = %d rev=%d, want 200/2", code, second.Revision)
	}

	code, generation := getGeneration(t, goal.ID, created.ID)
	if code != http.StatusOK {
		t.Fatalf("GetGoalPlanGeneration = %d, want 200", code)
	}
	if generation.CurrentRevision != 2 || generation.BaseRevision != 1 {
		t.Fatalf("generation current/base = %d/%d, want 2/1", generation.CurrentRevision, generation.BaseRevision)
	}
	var fixedInput, storedInput map[string]any
	if err := json.Unmarshal(created.InputContext, &fixedInput); err != nil {
		t.Fatalf("decode fixed input: %v", err)
	}
	if err := json.Unmarshal(generation.InputContext, &storedInput); err != nil {
		t.Fatalf("decode stored input %s: %v", generation.InputContext, err)
	}
	if !reflect.DeepEqual(fixedInput, storedInput) {
		t.Fatalf("input_context changed after a newer save: %v != %v", storedInput, fixedInput)
	}

	// The result is fixed to revision 1 while the draft is at 2, so it is kept
	// as a stale candidate instead of replacing the user's newer edit.
	code, submitted := submitPlanResultAsRun(t,
		mintGoalPlanTaskToken(t, testWorkspaceID, *created.TaskID, agentID), goal.ID, created.ID,
		map[string]any{
			"items": []map[string]any{{
				"title":   "candidate built on revision 1",
				"binding": map[string]any{"type": "new"},
			}},
			"edges": []map[string]any{},
		})
	if code != http.StatusOK {
		t.Fatalf("SubmitGoalPlanGenerationResult = %d, want 200", code)
	}
	if !submitted.Stale || submitted.Applied || submitted.BaseRevision != 1 {
		t.Fatalf("stale=%v applied=%v base=%d, want stale, unapplied, base 1",
			submitted.Stale, submitted.Applied, submitted.BaseRevision)
	}
	if submitted.ObservedRevision == nil || *submitted.ObservedRevision != 2 || submitted.CurrentRevision != 2 {
		t.Fatalf("observed/current = %v/%d, want 2/2", submitted.ObservedRevision, submitted.CurrentRevision)
	}
	_, finalPlan := getPlan(t, goal.ID)
	if finalPlan.Revision != 2 || len(finalPlan.Items) != len(edited) {
		t.Fatalf("plan after stale candidate revision=%d items=%d, want 2/%d",
			finalPlan.Revision, len(finalPlan.Items), len(edited))
	}
}

// TestGoalPlanGenerationRevisionInterleaving saves revision N+1 while a run
// built on revision N is already inside its protected transaction. The run must
// observe the newer revision and refuse, never write a generation or session
// against the stale base.
func TestGoalPlanGenerationRevisionInterleaving(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	projectID, _ := planProjectWithResource(t, "Plan revision interleaving "+t.Name(),
		"https://github.com/example/revision-interleaving-"+t.Name())
	goal := planTestGoal(t, projectID)
	agentID := createHandlerTestAgent(t, "Plan Revision Interleaving Agent", []byte("[]"))

	itemID := "018f3a9e-5555-7c2b-9f1e-000000000001"
	if code, saved := putPlan(t, goal.ID, map[string]any{
		"expected_revision": 0,
		"items": []map[string]any{{
			"id": itemID, "title": "node at revision 1", "binding": map[string]any{"type": "new"},
		}},
		"edges": []map[string]any{},
	}); code != http.StatusOK || saved.Revision != 1 {
		t.Fatalf("save revision 1 = %d, want 200", code)
	}

	tx := beginGoalPlanLockTx(t)
	execPlanLockTx(t, tx, `SELECT id FROM workspace WHERE id = $1 FOR UPDATE`, testWorkspaceID)
	holderPID := holderBackendPID(t, ctx, tx)
	done := startGoalPlanGeneration(goal.ID, map[string]any{
		"intent":            "plan on revision 1",
		"request_key":       "revision-interleaving",
		"expected_revision": 1,
		"agent_id":          agentID,
	})
	if !waitForWaiterBlockedBy(t, holderPID, 10*time.Second) {
		t.Fatalf("generation never parked behind the workspace row lock; its draft read is not inside the protected transaction")
	}
	select {
	case rec := <-done:
		t.Fatalf("generation returned before taking the lock: %d %s", rec.Code, rec.Body.String())
	default:
	}
	// The concurrent save wins the race and moves the draft to revision 2.
	execPlanLockTx(t, tx, `UPDATE goal_plan SET revision = revision + 1 WHERE workspace_id = $1 AND goal_id = $2`,
		testWorkspaceID, goal.ID)
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit revision bump: %v", err)
	}

	rec := awaitGoalPlanResponse(t, done, "generation behind a revision bump")
	if rec.Code != http.StatusConflict {
		t.Fatalf("generation against a newer revision = %d %s, want 409", rec.Code, rec.Body.String())
	}
	var body goalPlanErrorBody
	decodePlanJSON(t, rec, &body)
	if body.Code != "plan_revision_conflict" {
		t.Fatalf("conflict code = %q, want plan_revision_conflict (%s)", body.Code, rec.Body.String())
	}
	if body.CurrentRevision == nil || *body.CurrentRevision != 2 {
		t.Fatalf("current_revision = %v, want 2", body.CurrentRevision)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM goal_plan_generation WHERE goal_id = $1`, goal.ID); n != 0 {
		t.Fatalf("generation rows = %d, want 0", n)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM chat_session WHERE title = $1`, goal.Title); n != 0 {
		t.Fatalf("chat sessions = %d, want 0", n)
	}
}

// TestGoalPlanGenerationProjectSwitchInterleaving moves the goal to another
// project while a run is already past its pre-read. The run must answer an
// explicit conflict instead of mixing project A's resources with project B.
func TestGoalPlanGenerationProjectSwitchInterleaving(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	projectA, resourceA := planProjectWithResource(t, "Plan switch project A "+t.Name(),
		"https://github.com/example/switch-a-"+t.Name())
	projectB := createTestProjectForResources(t, "Plan switch project B "+t.Name()).ID
	goal := planTestGoal(t, projectA)
	agentID := createHandlerTestAgent(t, "Plan Switch Interleaving Agent", []byte("[]"))

	tx := beginGoalPlanLockTx(t)
	execPlanLockTx(t, tx, `SELECT id FROM workspace WHERE id = $1 FOR UPDATE`, testWorkspaceID)
	holderPID := holderBackendPID(t, ctx, tx)
	done := startGoalPlanGeneration(goal.ID, map[string]any{
		"intent":            "plan in project A",
		"request_key":       "project-switch",
		"expected_revision": 0,
		"agent_id":          agentID,
	})
	if !waitForWaiterBlockedBy(t, holderPID, 10*time.Second) {
		t.Fatalf("generation never parked behind the workspace row lock")
	}
	select {
	case rec := <-done:
		t.Fatalf("generation returned before taking the lock: %d %s", rec.Code, rec.Body.String())
	default:
	}
	execPlanLockTx(t, tx, `UPDATE goal SET project_id = $1, updated_at = now() WHERE id = $2 AND workspace_id = $3`,
		projectB, goal.ID, testWorkspaceID)
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit project move: %v", err)
	}

	rec := awaitGoalPlanResponse(t, done, "generation behind a project move")
	if rec.Code != http.StatusConflict {
		t.Fatalf("generation across a project move = %d %s, want 409", rec.Code, rec.Body.String())
	}
	var body goalPlanErrorBody
	decodePlanJSON(t, rec, &body)
	if body.Code != "goal_project_conflict" {
		t.Fatalf("conflict code = %q, want goal_project_conflict (%s)", body.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), resourceA) || strings.Contains(rec.Body.String(), projectA) {
		t.Fatalf("conflict body leaked old-project content: %s", rec.Body.String())
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM goal_plan_generation WHERE goal_id = $1`, goal.ID); n != 0 {
		t.Fatalf("generation rows = %d, want 0", n)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM chat_session WHERE title = $1`, goal.Title); n != 0 {
		t.Fatalf("chat sessions = %d, want 0", n)
	}
	var currentProject string
	dbfx.QueryRow(t, `SELECT project_id FROM goal WHERE id = $1`, goal.ID).Scan(&currentProject)
	if currentProject != projectB {
		t.Fatalf("goal project = %q, want the moved project %q", currentProject, projectB)
	}
}

// seedSiblingProject creates a project, its goal and a generation row so a
// project delete elsewhere can be shown to leave them alone.
func seedSiblingProject(t *testing.T, projectTitle, goalTitle, requestKey string) (projectID, goalID string) {
	t.Helper()
	projectID = dbfx.Project(t, projectTitle)
	goalID = dbfx.Insert(t, "goal", testutil.Cols{
		"workspace_id": testWorkspaceID,
		"project_id":   projectID,
		"title":        goalTitle,
		"creator_type": "member",
		"creator_id":   testUserID,
	})
	agentID := createHandlerTestAgent(t, goalTitle+" Agent", []byte("[]"))
	dbfx.Insert(t, "goal_plan_generation", testutil.Cols{
		"workspace_id": testWorkspaceID,
		"goal_id":      goalID,
		"project_id":   projectID,
		"request_key":  requestKey,
		"intent":       "seed",
		"agent_id":     agentID,
	})
	return projectID, goalID
}

func assertSiblingProjectIntact(t *testing.T, projectID, goalID string) {
	t.Helper()
	if n := dbfx.Count(t, `SELECT count(*) FROM project WHERE id = $1`, projectID); n != 1 {
		t.Fatalf("sibling project rows = %d, want 1", n)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM goal WHERE id = $1`, goalID); n != 1 {
		t.Fatalf("sibling goal rows = %d, want 1", n)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM goal_plan_generation WHERE goal_id = $1`, goalID); n != 1 {
		t.Fatalf("sibling generation rows = %d, want 1", n)
	}
}

// TestGoalPlanGenerationAndProjectDeleteInterleavings holds each side of the
// project-delete/generation contention in turn. Both orders must finish with a
// success or an explicit conflict/not-visible answer, never a deadlock surfacing
// as 500.
func TestGoalPlanGenerationAndProjectDeleteInterleavings(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	t.Run("generation waits behind project delete", func(t *testing.T) {
		ctx := context.Background()
		projectID, _ := planProjectWithResource(t, "Plan delete first project "+t.Name(),
			"https://github.com/example/delete-first-"+t.Name())
		goal := planTestGoal(t, projectID)
		agentID := createHandlerTestAgent(t, "Plan Delete First Agent", []byte("[]"))
		siblingProject, siblingGoal := seedSiblingProject(t,
			"Plan delete first sibling project "+t.Name(),
			"Plan delete first sibling goal "+t.Name(), "delete-first-sibling")

		tx := beginGoalPlanLockTx(t)
		// DeleteProject's first lock: the project row FOR UPDATE.
		execPlanLockTx(t, tx, `SELECT id FROM project WHERE id = $1 AND workspace_id = $2 FOR UPDATE`,
			projectID, testWorkspaceID)
		holderPID := holderBackendPID(t, ctx, tx)
		done := startGoalPlanGeneration(goal.ID, map[string]any{
			"intent":            "plan during project delete",
			"request_key":       "delete-first",
			"expected_revision": 0,
			"agent_id":          agentID,
		})
		if !waitForWaiterBlockedBy(t, holderPID, 10*time.Second) {
			t.Fatalf("generation never parked behind the project row lock")
		}
		select {
		case rec := <-done:
			t.Fatalf("generation returned before taking the lock: %d %s", rec.Code, rec.Body.String())
		default:
		}
		// Continue in the same order DeleteProject uses: project lock held
		// first, then the goals, then the project-scoped plan rows and rows.
		execPlanLockTx(t, tx, `SELECT id FROM goal WHERE workspace_id = $1 AND project_id = $2 FOR UPDATE`,
			testWorkspaceID, projectID)
		execPlanLockTx(t, tx, `DELETE FROM goal_plan_generation WHERE workspace_id = $1 AND project_id = $2`,
			testWorkspaceID, projectID)
		execPlanLockTx(t, tx, `DELETE FROM goal_plan WHERE workspace_id = $1 AND project_id = $2`,
			testWorkspaceID, projectID)
		execPlanLockTx(t, tx, `DELETE FROM goal WHERE workspace_id = $1 AND project_id = $2`,
			testWorkspaceID, projectID)
		execPlanLockTx(t, tx, `DELETE FROM project WHERE id = $1 AND workspace_id = $2`,
			projectID, testWorkspaceID)
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit project delete transaction: %v", err)
		}

		rec := awaitGoalPlanResponse(t, done, "generation behind a project delete")
		if rec.Code == http.StatusInternalServerError {
			t.Fatalf("generation surfaced a 500 instead of an explicit answer: %s", rec.Body.String())
		}
		if rec.Code != http.StatusConflict && rec.Code != http.StatusNotFound {
			t.Fatalf("generation during project delete = %d %s, want 409 or 404", rec.Code, rec.Body.String())
		}
		if rec.Code == http.StatusConflict {
			var body goalPlanErrorBody
			decodePlanJSON(t, rec, &body)
			if body.Code != "goal_project_conflict" {
				t.Fatalf("conflict code = %q, want goal_project_conflict", body.Code)
			}
		}
		if n := dbfx.Count(t, `SELECT count(*) FROM goal_plan_generation WHERE goal_id = $1`, goal.ID); n != 0 {
			t.Fatalf("generation rows = %d, want 0", n)
		}
		if n := dbfx.Count(t, `SELECT count(*) FROM chat_session WHERE title = $1`, goal.Title); n != 0 {
			t.Fatalf("chat sessions = %d, want 0", n)
		}
		if n := dbfx.Count(t, `SELECT count(*) FROM project WHERE id = $1`, projectID); n != 0 {
			t.Fatalf("deleted project rows = %d, want 0", n)
		}
		assertSiblingProjectIntact(t, siblingProject, siblingGoal)
	})

	t.Run("project delete waits behind generation", func(t *testing.T) {
		ctx := context.Background()
		projectID, _ := planProjectWithResource(t, "Plan delete second project "+t.Name(),
			"https://github.com/example/delete-second-"+t.Name())
		goal := planTestGoal(t, projectID)
		agentID := createHandlerTestAgent(t, "Plan Delete Second Agent", []byte("[]"))
		siblingProject, siblingGoal := seedSiblingProject(t,
			"Plan delete second sibling project "+t.Name(),
			"Plan delete second sibling goal "+t.Name(), "delete-second-sibling")

		// Hold the generation path's lock set, in its order, and record the row
		// the run would have created.
		tx := beginGoalPlanLockTx(t)
		execPlanLockTx(t, tx, `SELECT id FROM workspace WHERE id = $1 FOR KEY SHARE`, testWorkspaceID)
		execPlanLockTx(t, tx, `SELECT id FROM project WHERE id = $1 AND workspace_id = $2 FOR KEY SHARE`,
			projectID, testWorkspaceID)
		execPlanLockTx(t, tx, `SELECT id FROM goal WHERE id = $1 AND workspace_id = $2 FOR UPDATE`,
			goal.ID, testWorkspaceID)
		execPlanLockTx(t, tx, `SELECT pg_advisory_xact_lock(hashtextextended('goal_plan:' || $1::text, 0))`, goal.ID)
		execPlanLockTx(t, tx, `
			INSERT INTO goal_plan_generation (
				workspace_id, goal_id, project_id, request_key, intent, agent_id,
				base_revision, state, input_context
			) VALUES ($1, $2, $3, 'delete-second', 'seed', $4, 0, 'preparing', '{}'::jsonb)`,
			testWorkspaceID, goal.ID, projectID, agentID)
		holderPID := holderBackendPID(t, ctx, tx)

		done := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			req := withURLParam(newRequest(http.MethodDelete, "/api/projects/"+projectID, nil), "id", projectID)
			rec := httptest.NewRecorder()
			testHandler.DeleteProject(rec, req)
			done <- rec
		}()
		if !waitForWaiterBlockedBy(t, holderPID, 10*time.Second) {
			t.Fatalf("DeleteProject never parked behind the generation's lock set")
		}
		select {
		case rec := <-done:
			t.Fatalf("DeleteProject returned before taking the lock: %d %s", rec.Code, rec.Body.String())
		default:
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit generation lock set: %v", err)
		}

		rec := awaitGoalPlanResponse(t, done, "project delete behind a generation")
		if rec.Code != http.StatusNoContent {
			t.Fatalf("DeleteProject = %d %s, want 204", rec.Code, rec.Body.String())
		}
		if n := dbfx.Count(t, `SELECT count(*) FROM goal_plan_generation WHERE goal_id = $1`, goal.ID); n != 0 {
			t.Fatalf("generation rows after delete = %d, want 0", n)
		}
		if n := dbfx.Count(t, `SELECT count(*) FROM project WHERE id = $1`, projectID); n != 0 {
			t.Fatalf("deleted project rows = %d, want 0", n)
		}
		assertSiblingProjectIntact(t, siblingProject, siblingGoal)
	})
}

// TestGoalPlanOldProjectCandidateRejectedAfterMove covers the legal move: no
// saved plan, no in-flight run, candidate unapplied. Every path that could
// hand project A's run back to the goal now in project B answers one conflict
// code without A's content, and nothing is written.
func TestGoalPlanOldProjectCandidateRejectedAfterMove(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	projectA, resourceA := planProjectWithResource(t, "Plan old candidate A "+t.Name(),
		"https://github.com/example/old-candidate-a-"+t.Name())
	projectB, resourceB := planProjectWithResource(t, "Plan old candidate B "+t.Name(),
		"https://github.com/example/old-candidate-b-"+t.Name())
	goal := planTestGoal(t, projectA)
	agentID := createHandlerTestAgent(t, "Plan Old Candidate Agent", []byte("[]"))

	code, created := postGeneration(t, goal.ID, map[string]any{
		"intent":            "a plan for project A",
		"request_key":       "old-project-candidate",
		"expected_revision": 0,
		"agent_id":          agentID,
	})
	if code != http.StatusAccepted {
		t.Fatalf("CreateGoalPlanGeneration = %d, want 202", code)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE chat_session_id = $1`, *created.ChatSessionID)
		testPool.Exec(ctx, `DELETE FROM chat_session WHERE id = $1`, *created.ChatSessionID)
		testPool.Exec(ctx, `DELETE FROM goal_plan_generation WHERE goal_id = $1`, goal.ID)
	})

	// Every candidate node is new; the only thing that makes it project-specific
	// is the run's fixed input.
	candidateItems := []map[string]any{{
		"title":   "A-only candidate node",
		"outcome": "only meaningful in project A",
		"binding": map[string]any{"type": "new"},
	}}
	code, submitted := submitPlanResultAsRun(t,
		mintGoalPlanTaskToken(t, testWorkspaceID, *created.TaskID, agentID), goal.ID, created.ID,
		map[string]any{"items": candidateItems, "edges": []map[string]any{}})
	if code != http.StatusOK || submitted.State != "succeeded" || !planHasCandidate(submitted.Candidate) {
		t.Fatalf("candidate result = %d state=%q, want 200 succeeded with a candidate", code, submitted.State)
	}

	// Legal move: nothing is saved and nothing is running.
	code, moved := updateGoalViaAPI(t, testUserID, goal.ID, map[string]any{"project_id": projectB})
	if code != http.StatusOK || moved.ProjectID == nil || *moved.ProjectID != projectB {
		t.Fatalf("move goal to project B = %d %+v, want 200 with the new project", code, moved)
	}

	// GET: the old candidate and its input evidence are not served.
	rec := getGenerationRaw(t, goal.ID, created.ID)
	if rec.Code != http.StatusConflict {
		t.Fatalf("GET old-project generation = %d %s, want 409", rec.Code, rec.Body.String())
	}
	var conflict goalPlanErrorBody
	decodePlanJSON(t, rec, &conflict)
	if conflict.Code != "generation_project_conflict" {
		t.Fatalf("GET conflict code = %q, want generation_project_conflict", conflict.Code)
	}
	if strings.Contains(rec.Body.String(), "A-only candidate node") || strings.Contains(rec.Body.String(), resourceA) {
		t.Fatalf("GET conflict body leaked the old project's candidate or input: %s", rec.Body.String())
	}

	// Apply: refused, and the refused save leaves no plan behind. The apply
	// carries the server-assigned node ids, which is what a client would send.
	var candidate GoalPlanCandidate
	if err := jsonUnmarshalStrict(submitted.Candidate, &candidate); err != nil {
		t.Fatalf("decode candidate: %v", err)
	}
	code, _ = putPlan(t, goal.ID, map[string]any{
		"expected_revision": 0,
		"items":             candidate.Items,
		"edges":             candidate.Edges,
		"generation_id":     created.ID,
	})
	if code != http.StatusConflict {
		t.Fatalf("apply old-project candidate = %d, want 409", code)
	}
	code, plan := getPlan(t, goal.ID)
	if code != http.StatusOK || plan.ID != nil || plan.Revision != 0 {
		t.Fatalf("plan after refused apply id=%v revision=%d, want none at revision 0", plan.ID, plan.Revision)
	}

	// Same request_key replay: resolves to the old run, then refuses it.
	replay := postGenerationRaw(goal.ID, map[string]any{
		"intent":            "a plan for project A",
		"request_key":       "old-project-candidate",
		"expected_revision": 0,
		"agent_id":          agentID,
	})
	if replay.Code != http.StatusConflict {
		t.Fatalf("replayed old-project request_key = %d %s, want 409", replay.Code, replay.Body.String())
	}
	var replayBody goalPlanErrorBody
	decodePlanJSON(t, replay, &replayBody)
	if replayBody.Code != "generation_project_conflict" {
		t.Fatalf("replay conflict code = %q, want generation_project_conflict", replayBody.Code)
	}
	if n := dbfx.Count(t, `SELECT count(*) FROM goal_plan_generation WHERE goal_id = $1`, goal.ID); n != 1 {
		t.Fatalf("generation rows after replay = %d, want the original 1", n)
	}

	// Result entry: ownership still resolves, but the run is not allowed to
	// write anything for the moved goal.
	lateRec := postPlanResultRaw(mintGoalPlanTaskToken(t, testWorkspaceID, *created.TaskID, agentID), "",
		goal.ID, created.ID, map[string]any{
			"items": []map[string]any{{
				"title":   "should not be stored",
				"binding": map[string]any{"type": "new"},
			}},
			"edges": []map[string]any{},
		}, nil)
	if lateRec.Code != http.StatusConflict {
		t.Fatalf("result for old-project run = %d %s, want 409", lateRec.Code, lateRec.Body.String())
	}
	var lateBody goalPlanErrorBody
	decodePlanJSON(t, lateRec, &lateBody)
	if lateBody.Code != "generation_project_conflict" {
		t.Fatalf("result conflict code = %q, want generation_project_conflict", lateBody.Code)
	}
	if strings.Contains(lateRec.Body.String(), "should not be stored") {
		t.Fatalf("result conflict body echoed the new candidate: %s", lateRec.Body.String())
	}
	var storedCandidate []byte
	dbfx.QueryRow(t, `SELECT candidate FROM goal_plan_generation WHERE id = $1`, created.ID).Scan(&storedCandidate)
	if !strings.Contains(string(storedCandidate), "A-only candidate node") ||
		strings.Contains(string(storedCandidate), "should not be stored") {
		t.Fatalf("stored candidate changed after the refused result: %s", storedCandidate)
	}

	// A new request in project B starts clean on B's own resources.
	code, fresh := postGeneration(t, goal.ID, map[string]any{
		"intent":            "a plan for project B",
		"request_key":       "new-project-generation",
		"expected_revision": 0,
		"agent_id":          agentID,
	})
	if code != http.StatusAccepted {
		t.Fatalf("new generation in project B = %d, want 202", code)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE chat_session_id = $1`, *fresh.ChatSessionID)
		testPool.Exec(ctx, `DELETE FROM chat_session WHERE id = $1`, *fresh.ChatSessionID)
	})
	var freshSnapshot struct {
		ProjectID string `json:"project_id"`
		Resources []struct {
			ID string `json:"id"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(fresh.InputContext, &freshSnapshot); err != nil {
		t.Fatalf("decode fresh input_context: %v", err)
	}
	if freshSnapshot.ProjectID != projectB || len(freshSnapshot.Resources) != 1 ||
		freshSnapshot.Resources[0].ID != resourceB {
		t.Fatalf("fresh input_context = %+v, want project B with resource %s", freshSnapshot, resourceB)
	}
	if strings.Contains(string(fresh.InputContext), resourceA) {
		t.Fatalf("fresh input_context still carries project A's resource: %s", fresh.InputContext)
	}
}

// TestGoalPlanOldProjectCandidateHiddenWhenCallerSeesOnlyB is the same refusal
// for a caller who cannot see project A at all. Hiding the goal answered 404
// before the move; after the move to a visible project the goal is readable but
// its old run still answers the conflict, not A's candidate.
func TestGoalPlanOldProjectCandidateHiddenWhenCallerSeesOnlyB(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	f := newPrivateProjectFixture(t)
	goal := createGoalViaAPI(t, testUserID, map[string]any{
		"title":      "Plan private move " + t.Name(),
		"project_id": f.projectID,
	})
	agentID := createHandlerTestAgent(t, "Plan Private Move Agent", []byte("[]"))
	code, created := postGeneration(t, goal.ID, map[string]any{
		"intent":            "plan against the private project",
		"request_key":       "private-project-run",
		"expected_revision": 0,
		"agent_id":          agentID,
	})
	if code != http.StatusAccepted {
		t.Fatalf("CreateGoalPlanGeneration = %d, want 202", code)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE chat_session_id = $1`, *created.ChatSessionID)
		testPool.Exec(ctx, `DELETE FROM chat_session WHERE id = $1`, *created.ChatSessionID)
		testPool.Exec(ctx, `DELETE FROM goal_plan_generation WHERE goal_id = $1`, goal.ID)
	})
	code, _ = submitPlanResultAsRun(t,
		mintGoalPlanTaskToken(t, testWorkspaceID, *created.TaskID, agentID), goal.ID, created.ID,
		map[string]any{
			"items": []map[string]any{{
				"title":   "private A candidate",
				"binding": map[string]any{"type": "new"},
			}},
			"edges": []map[string]any{},
		})
	if code != http.StatusOK {
		t.Fatalf("candidate result = %d, want 200", code)
	}

	// Before the move the goal is invisible through project A: the run is not
	// reachable at all.
	if rec := getGenerationAsUserRaw(t, f.outsider, goal.ID, created.ID); rec.Code != http.StatusNotFound {
		t.Fatalf("outsider GET run in the hidden project = %d, want 404", rec.Code)
	}

	publicTarget := createTestProjectForResources(t, "Plan private target "+t.Name())
	code, moved := updateGoalViaAPI(t, testUserID, goal.ID, map[string]any{"project_id": publicTarget.ID})
	if code != http.StatusOK || moved.ProjectID == nil || *moved.ProjectID != publicTarget.ID {
		t.Fatalf("move goal to the visible project = %d %+v, want 200", code, moved)
	}

	// The moved goal is now readable, but the run is not.
	if code, _ := getGoalViaAPI(t, f.outsider, goal.ID); code != http.StatusOK {
		t.Fatalf("outsider GET moved goal = %d, want 200", code)
	}
	rec := getGenerationAsUserRaw(t, f.outsider, goal.ID, created.ID)
	if rec.Code != http.StatusConflict {
		t.Fatalf("outsider GET old-project run = %d %s, want 409", rec.Code, rec.Body.String())
	}
	var body goalPlanErrorBody
	decodePlanJSON(t, rec, &body)
	if body.Code != "generation_project_conflict" {
		t.Fatalf("conflict code = %q, want generation_project_conflict", body.Code)
	}
	if strings.Contains(rec.Body.String(), "private A candidate") {
		t.Fatalf("conflict body leaked the hidden project's candidate: %s", rec.Body.String())
	}
}

// TestGoalPlanRunEvidenceChain follows one run across the components this
// checkout can actually exercise without a daemon or a model: the handler's
// fixed read, the chat task the daemon would claim, the prompt it would hand
// over, the task-token result entry the CLI calls, and the stored candidate.
func TestGoalPlanRunEvidenceChain(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	projectURL := "https://github.com/example/evidence-chain-" + t.Name()
	docsURL := "https://github.com/example/evidence-docs-" + t.Name()
	projectID, repoResource := planProjectWithResource(t, "Plan evidence chain "+t.Name(), projectURL)
	var docsResource ProjectResourceResponse
	testutil.Call(t, testHandler.CreateProjectResource, withURLParam(
		newRequest("POST", "/api/projects/"+projectID+"/resources", map[string]any{
			"resource_type": "github_repo",
			"resource_ref":  map[string]any{"url": docsURL, "ref": "docs"},
		}), "id", projectID)).Want(http.StatusCreated).JSON(&docsResource)

	goal := planTestGoal(t, projectID)
	agentID := createHandlerTestAgent(t, "Plan Evidence Chain Agent", []byte("[]"))
	code, created := postGeneration(t, goal.ID, map[string]any{
		"intent":            "produce a plan from the project's own resources",
		"request_key":       "evidence-chain",
		"expected_revision": 0,
		"agent_id":          agentID,
	})
	if code != http.StatusAccepted {
		t.Fatalf("CreateGoalPlanGeneration = %d, want 202", code)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE chat_session_id = $1`, *created.ChatSessionID)
		testPool.Exec(ctx, `DELETE FROM chat_message WHERE chat_session_id = $1`, *created.ChatSessionID)
		testPool.Exec(ctx, `DELETE FROM chat_session WHERE id = $1`, *created.ChatSessionID)
		testPool.Exec(ctx, `DELETE FROM goal_plan_generation WHERE goal_id = $1`, goal.ID)
	})

	// Link 1: the run is an ordinary queued agent task on the agent's runtime,
	// in the session the generation recorded.
	var taskStatus, taskRuntime, taskSession string
	dbfx.QueryRow(t, `SELECT status, runtime_id, chat_session_id FROM agent_task_queue WHERE id = $1`,
		*created.TaskID).Scan(&taskStatus, &taskRuntime, &taskSession)
	if taskStatus != "queued" || taskSession != *created.ChatSessionID {
		t.Fatalf("task status/session = %q/%q, want queued/%q", taskStatus, taskSession, *created.ChatSessionID)
	}
	var agentRuntime string
	dbfx.QueryRow(t, `SELECT runtime_id FROM agent WHERE id = $1`, agentID).Scan(&agentRuntime)
	if taskRuntime != agentRuntime {
		t.Fatalf("task runtime = %q, want the agent runtime %q", taskRuntime, agentRuntime)
	}

	// Link 2: the prompt carries the project's own resources, the fixed base
	// revision and the CLI hand-back command naming this goal and generation.
	var prompt string
	dbfx.QueryRow(t, `SELECT content FROM chat_message WHERE chat_session_id = $1 AND role = 'user'`,
		*created.ChatSessionID).Scan(&prompt)
	for _, want := range []string{
		"id=" + repoResource,
		"id=" + docsResource.ID,
		"type=github_repo",
		projectURL,
		docsURL,
		"base_revision: 0",
		"goal plan-result --goal " + goal.ID,
		"--generation " + created.ID,
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt is missing %q:\n%s", want, prompt)
		}
	}

	// Link 3: the run's task token, not a member session, submits the result
	// the CLI would post, and the candidate records what the run claims to have
	// read.
	code, submitted := submitPlanResultAsRun(t,
		mintGoalPlanTaskToken(t, testWorkspaceID, *created.TaskID, agentID), goal.ID, created.ID,
		map[string]any{
			"items": []map[string]any{{
				"title":   "deliverable from the listed resources",
				"binding": map[string]any{"type": "new"},
			}},
			"edges": []map[string]any{},
			"context_snapshot": map[string]any{
				"repo_sha": "cafebabe",
				"resources": []map[string]any{
					{"id": repoResource, "read": true},
					{"id": docsResource.ID, "read": true},
				},
				"read_at": "2026-11-12T00:00:00Z",
			},
		})
	if code != http.StatusOK || submitted.State != "succeeded" || !planHasCandidate(submitted.Candidate) {
		t.Fatalf("result = %d state=%q, want 200 succeeded with a candidate", code, submitted.State)
	}

	// Link 4: the stored input context is still the one the run started with.
	var storedInput []byte
	dbfx.QueryRow(t, `SELECT input_context FROM goal_plan_generation WHERE id = $1`, created.ID).Scan(&storedInput)
	var fixedInput, persistedInput map[string]any
	if err := json.Unmarshal(created.InputContext, &fixedInput); err != nil {
		t.Fatalf("decode fixed input: %v", err)
	}
	if err := json.Unmarshal(storedInput, &persistedInput); err != nil {
		t.Fatalf("decode stored input: %v", err)
	}
	if !reflect.DeepEqual(fixedInput, persistedInput) {
		t.Fatalf("stored input_context changed across the run: %v != %v", persistedInput, fixedInput)
	}

	var candidate GoalPlanCandidate
	if err := jsonUnmarshalStrict(submitted.Candidate, &candidate); err != nil {
		t.Fatalf("decode candidate: %v", err)
	}
	var evidence struct {
		RepoSHA   string `json:"repo_sha"`
		Resources []struct {
			ID   string `json:"id"`
			Read bool   `json:"read"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(candidate.ContextSnapshot, &evidence); err != nil {
		t.Fatalf("decode candidate evidence: %v", err)
	}
	if evidence.RepoSHA != "cafebabe" || len(evidence.Resources) != 2 ||
		evidence.Resources[0].ID != repoResource || !evidence.Resources[0].Read ||
		evidence.Resources[1].ID != docsResource.ID || !evidence.Resources[1].Read {
		t.Fatalf("candidate evidence = %+v, want the two listed resources marked read", evidence)
	}
}
