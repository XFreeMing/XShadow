package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Goal plan coverage for DEV-6: draft persistence, request-key dedupe,
// expected_revision conflicts, late-candidate protection, result ownership and
// cleanup. Everything runs against the real database so the constraints the
// handlers depend on (unique indexes, advisory lock, CAS update) are exercised.

func createProjectForPlan(t *testing.T, title string) string {
	t.Helper()
	project := createTestProjectForResources(t, title)
	w := testutil.Call(t, testHandler.CreateProjectResource, withURLParam(
		newRequest("POST", "/api/projects/"+project.ID+"/resources", map[string]any{
			"resource_type": "github_repo",
			"resource_ref":  map[string]any{"url": "https://github.com/example/" + project.ID, "ref": "main"},
		}), "id", project.ID)).Want(http.StatusCreated)
	_ = w
	return project.ID
}

func planTestGoal(t *testing.T, projectID string) GoalResponse {
	t.Helper()
	body := map[string]any{
		"title":               "Plan draft " + t.Name(),
		"intent":              "turn one sentence into an adjustable plan",
		"acceptance_criteria": "the plan is editable and never executes",
	}
	if projectID != "" {
		body["project_id"] = projectID
	}
	return createGoalViaAPI(t, testUserID, body)
}

func getPlan(t *testing.T, goalID string) (int, GoalPlanResponse) {
	t.Helper()
	var resp GoalPlanResponse
	rec := getPlanRaw(t, goalID)
	if rec.Code == http.StatusOK {
		decodePlanJSON(t, rec, &resp)
	}
	return rec.Code, resp
}

func getPlanRaw(t *testing.T, goalID string) *httptest.ResponseRecorder {
	t.Helper()
	res := testutil.Call(t, testHandler.GetGoalPlan,
		goalRequest(http.MethodGet, "/api/goals/"+goalID+"/plan", testUserID, nil, map[string]string{"id": goalID}))
	return res.ResponseRecorder
}

func putPlan(t *testing.T, goalID string, body map[string]any) (int, GoalPlanResponse) {
	t.Helper()
	var resp GoalPlanResponse
	rec := putPlanRaw(t, goalID, body)
	if rec.Code == http.StatusOK {
		decodePlanJSON(t, rec, &resp)
	}
	return rec.Code, resp
}

func putPlanRaw(t *testing.T, goalID string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	res := testutil.Call(t, testHandler.PutGoalPlan,
		goalRequest(http.MethodPut, "/api/goals/"+goalID+"/plan", testUserID, body, map[string]string{"id": goalID}))
	return res.ResponseRecorder
}

func postGeneration(t *testing.T, goalID string, body map[string]any) (int, GoalPlanGenerationResponse) {
	t.Helper()
	var resp GoalPlanGenerationResponse
	rec := postGenerationRaw(goalID, body)
	if rec.Code == http.StatusAccepted || rec.Code == http.StatusOK {
		decodePlanJSON(t, rec, &resp)
	}
	return rec.Code, resp
}

// postGenerationRaw is goroutine-safe: it never touches testing.T, so the
// two-transaction tests can call it while another transaction holds a lock.
func postGenerationRaw(goalID string, body map[string]any) *httptest.ResponseRecorder {
	req := goalRequest(http.MethodPost, "/api/goals/"+goalID+"/plan/generations", testUserID, body,
		map[string]string{"id": goalID})
	rec := httptest.NewRecorder()
	testHandler.CreateGoalPlanGeneration(rec, req)
	return rec
}

func TestGoalPlanEmptyShapeAndProjectRequirement(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	// A goal without a project has no evidence base: it cannot save a plan.
	goal := planTestGoal(t, "")

	code, plan := getPlan(t, goal.ID)
	if code != http.StatusOK {
		t.Fatalf("GetGoalPlan = %d, want 200", code)
	}
	if plan.ID != nil || plan.Revision != 0 || plan.State != "draft" {
		t.Fatalf("empty plan = %+v, want no id, revision 0, state draft", plan)
	}
	if len(plan.Items) != 0 || len(plan.Edges) != 0 {
		t.Fatalf("empty plan items/edges = %d/%d, want 0/0", len(plan.Items), len(plan.Edges))
	}
	if string(plan.ContextSnapshot) != "{}" {
		t.Fatalf("empty plan context_snapshot = %s, want {}", plan.ContextSnapshot)
	}

	code, _ = putPlan(t, goal.ID, map[string]any{
		"expected_revision": 0,
		"items": []map[string]any{{
			"id": "not-allowed-to-matter", "title": "node", "binding": map[string]any{"type": "new"},
		}},
	})
	if code != http.StatusBadRequest {
		t.Fatalf("PutGoalPlan without project = %d, want 400", code)
	}

	code, _ = postGeneration(t, goal.ID, map[string]any{
		"intent":            "plan it",
		"request_key":       "req-no-project",
		"expected_revision": 0,
		"agent_id":          createHandlerTestAgent(t, "Plan No Project Agent", []byte("[]")),
	})
	if code != http.StatusBadRequest {
		t.Fatalf("CreateGoalPlanGeneration without project = %d, want 400", code)
	}
}

func TestGoalPlanSaveRevisionAndValidation(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	projectID := createProjectForPlan(t, "Plan save project "+t.Name())
	goal := planTestGoal(t, projectID)

	itemID := "018f3a9e-1111-7c2b-9f1e-000000000001"
	code, saved := putPlan(t, goal.ID, map[string]any{
		"expected_revision": 0,
		"items": []map[string]any{{
			"id":                  itemID,
			"title":               "first deliverable",
			"outcome":             "the user can edit the draft",
			"acceptance_criteria": "saving twice keeps both revisions apart",
			"binding":             map[string]any{"type": "new"},
		}},
		"edges": []map[string]any{},
	})
	if code != http.StatusOK {
		t.Fatalf("first PutGoalPlan = %d, want 200", code)
	}
	if saved.Revision != 1 || saved.ID == nil || len(saved.Items) != 1 || saved.Items[0].ID != itemID {
		t.Fatalf("first save = %+v, want revision 1 with the given item id", saved)
	}

	// Same expected_revision: the draft moved on, so this must conflict instead
	// of overwriting the newer revision.
	code, _ = putPlan(t, goal.ID, map[string]any{
		"expected_revision": 0,
		"items":             []map[string]any{{"id": itemID, "title": "stale edit", "binding": map[string]any{"type": "new"}}},
	})
	if code != http.StatusConflict {
		t.Fatalf("stale PutGoalPlan = %d, want 409", code)
	}

	// A dependency cycle can never be executed; saving it is refused.
	secondID := "018f3a9e-1111-7c2b-9f1e-000000000002"
	code, _ = putPlan(t, goal.ID, map[string]any{
		"expected_revision": 1,
		"items": []map[string]any{
			{"id": itemID, "title": "a", "binding": map[string]any{"type": "new"}},
			{"id": secondID, "title": "b", "binding": map[string]any{"type": "new"}},
		},
		"edges": []map[string]any{
			{"dependent_item_id": itemID, "prerequisite_item_id": secondID, "reason": "a waits for b"},
			{"dependent_item_id": secondID, "prerequisite_item_id": itemID, "reason": "b waits for a"},
		},
	})
	if code != http.StatusBadRequest {
		t.Fatalf("cyclic PutGoalPlan = %d, want 400", code)
	}

	// The refused saves left revision 1 untouched.
	_, plan := getPlan(t, goal.ID)
	if plan.Revision != 1 {
		t.Fatalf("revision after refused saves = %d, want 1", plan.Revision)
	}
}

func TestGoalPlanGenerationDedupeLateCandidateAndApply(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	projectID := createProjectForPlan(t, "Plan generation project "+t.Name())
	goal := planTestGoal(t, projectID)
	agentID := createHandlerTestAgent(t, "Plan Generation Agent", []byte("[]"))

	request := map[string]any{
		"intent":            "split the goal into reviewable deliverables",
		"request_key":       "gen-key-1",
		"expected_revision": 0,
		"agent_id":          agentID,
	}
	code, created := postGeneration(t, goal.ID, request)
	if code != http.StatusAccepted {
		t.Fatalf("CreateGoalPlanGeneration = %d, want 202", code)
	}
	if created.ID == "" || created.TaskID == nil || created.ChatSessionID == nil || created.State != "queued" {
		t.Fatalf("generation = %+v, want a queued run with task and session", created)
	}
	if created.BaseRevision != 0 || created.CurrentRevision != 0 {
		t.Fatalf("generation revisions = %d/%d, want 0/0", created.BaseRevision, created.CurrentRevision)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE chat_session_id = $1`, *created.ChatSessionID)
		testPool.Exec(ctx, `DELETE FROM chat_session WHERE id = $1`, *created.ChatSessionID)
		testPool.Exec(ctx, `DELETE FROM goal_plan_generation WHERE goal_id = $1`, goal.ID)
	})

	// A retried request_key resolves to the same generation, not a second run.
	code, replay := postGeneration(t, goal.ID, request)
	if code != http.StatusOK {
		t.Fatalf("replayed generation = %d, want 200", code)
	}
	if replay.ID != created.ID {
		t.Fatalf("replay created a second generation: %s != %s", replay.ID, created.ID)
	}
	var runs int
	testPool.QueryRow(ctx, `SELECT count(*) FROM goal_plan_generation WHERE goal_id = $1`, goal.ID).Scan(&runs)
	if runs != 1 {
		t.Fatalf("generation rows = %d, want 1", runs)
	}

	// Reusing the key for a different request is a caller bug, not a replay.
	conflict := map[string]any{
		"intent":            "something else entirely",
		"request_key":       "gen-key-1",
		"expected_revision": 0,
		"agent_id":          agentID,
	}
	if code, _ := postGeneration(t, goal.ID, conflict); code != http.StatusConflict {
		t.Fatalf("conflicting request_key = %d, want 409", code)
	}

	// The run submits a plan without node ids; the server mints stable ones.
	resultBody := map[string]any{
		"items": []map[string]any{
			{"title": "first", "outcome": "o1", "acceptance_criteria": "a1", "binding": map[string]any{"type": "new"}},
			{"title": "second", "outcome": "o2", "acceptance_criteria": "a2", "binding": map[string]any{"type": "new"}},
		},
		"edges":            []map[string]any{},
		"context_snapshot": map[string]any{"repo_sha": "deadbeef", "read_at": "2026-11-12T00:00:00Z"},
	}
	code, submitted := submitPlanResultAsRun(t,
		mintGoalPlanTaskToken(t, testWorkspaceID, *created.TaskID, agentID), goal.ID, created.ID, resultBody)
	if code != http.StatusOK {
		t.Fatalf("SubmitGoalPlanGenerationResult = %d, want 200", code)
	}
	if submitted.State != "succeeded" || submitted.Candidate == nil || submitted.Applied {
		t.Fatalf("submitted = %+v, want succeeded with an unapplied candidate", submitted)
	}
	if submitted.Stale {
		t.Fatalf("a candidate based on the current revision must not be stale")
	}

	var candidate GoalPlanCandidate
	if err := jsonUnmarshalStrict(submitted.Candidate, &candidate); err != nil {
		t.Fatalf("decode candidate: %v", err)
	}
	if len(candidate.Items) != 2 || candidate.Items[0].ID == "" || candidate.Items[0].ID == candidate.Items[1].ID {
		t.Fatalf("candidate items = %+v, want two distinct server-assigned ids", candidate.Items)
	}

	// Applying the candidate records the saved revision it became, exactly
	// once: a second apply is refused rather than silently rewriting history.
	applyBody := map[string]any{
		"expected_revision": 0,
		"items":             candidate.Items,
		"edges":             candidate.Edges,
		"context_snapshot":  candidate.ContextSnapshot,
		"generation_id":     created.ID,
	}
	code, applied := putPlan(t, goal.ID, applyBody)
	if code != http.StatusOK {
		t.Fatalf("apply candidate = %d, want 200", code)
	}
	if applied.Revision != 1 {
		t.Fatalf("applied revision = %d, want 1", applied.Revision)
	}
	applyBody["expected_revision"] = 1
	if code, _ := putPlan(t, goal.ID, applyBody); code != http.StatusConflict {
		t.Fatalf("re-applying the same generation = %d, want 409", code)
	}

	// A second generation starts from revision 1, but the user keeps editing:
	// the candidate that arrives afterwards is preserved and flagged stale
	// instead of overwriting revision 2.
	request["request_key"] = "gen-key-2"
	request["expected_revision"] = 1
	code, second := postGeneration(t, goal.ID, request)
	if code != http.StatusAccepted {
		t.Fatalf("second generation = %d, want 202", code)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE chat_session_id = $1`, *second.ChatSessionID)
		testPool.Exec(ctx, `DELETE FROM chat_session WHERE id = $1`, *second.ChatSessionID)
	})
	edited := append([]GoalPlanItem{}, candidate.Items...)
	edited = append(edited, GoalPlanItem{
		ID:      "018f3a9e-2222-7c2b-9f1e-000000000009",
		Title:   "manual node",
		Binding: GoalPlanItemBinding{Type: "new"},
	})
	if code, _ := putPlan(t, goal.ID, map[string]any{
		"expected_revision": 1,
		"items":             edited,
		"edges":             []GoalPlanEdge{},
	}); code != http.StatusOK {
		t.Fatalf("manual edit during generation = %d, want 200", code)
	}

	lateBody := map[string]any{
		"items": []map[string]any{
			{"title": "late node", "outcome": "o", "acceptance_criteria": "a", "binding": map[string]any{"type": "new"}},
		},
		"edges": []map[string]any{},
	}
	code, late := submitPlanResultAsRun(t,
		mintGoalPlanTaskToken(t, testWorkspaceID, *second.TaskID, agentID), goal.ID, second.ID, lateBody)
	if code != http.StatusOK {
		t.Fatalf("late result = %d, want 200", code)
	}
	if late.State != "succeeded" || late.Candidate == nil {
		t.Fatalf("late result = %+v, want the candidate kept", late)
	}
	if !late.Stale || late.Applied {
		t.Fatalf("late result stale=%v applied=%v, want stale and unapplied", late.Stale, late.Applied)
	}
	_, finalPlan := getPlan(t, goal.ID)
	if finalPlan.Revision != 2 || len(finalPlan.Items) != len(edited) {
		t.Fatalf("plan after late result revision=%d items=%d, want 2 and %d (the late candidate must not overwrite)",
			finalPlan.Revision, len(finalPlan.Items), len(edited))
	}
}

func TestGoalPlanResultOwnershipAndValidation(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	projectID := createProjectForPlan(t, "Plan result project "+t.Name())
	goal := planTestGoal(t, projectID)
	agentID := createHandlerTestAgent(t, "Plan Result Agent", []byte("[]"))
	code, created := postGeneration(t, goal.ID, map[string]any{
		"intent":            "produce a plan",
		"request_key":       "gen-ownership",
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

	body := map[string]any{"items": []map[string]any{{"title": "x", "binding": map[string]any{"type": "new"}}}}

	// A member session cannot submit a plan for a run, and hand-set task
	// headers do not make it one: the real Auth middleware strips the
	// client-supplied X-Actor-Source/X-Task-ID/X-Agent-ID and only its own
	// mat_ branch is allowed to re-stamp them. No handler-header shortcut is
	// involved here.
	memberRec := postPlanResultRaw(memberSessionToken(t, testUserID), testWorkspaceID, goal.ID, created.ID, body,
		map[string]string{
			"X-Actor-Source": "task_token",
			"X-Task-ID":      *created.TaskID,
			"X-Agent-ID":     agentID,
		})
	if memberRec.Code != http.StatusForbidden || !strings.Contains(memberRec.Body.String(), "task_token_required") {
		t.Fatalf("member forging task headers = %d %s, want 403 task_token_required",
			memberRec.Code, memberRec.Body.String())
	}

	// A task that does not own this generation cannot write it.
	otherTask := createHandlerTestTaskForAgent(t, agentID)
	t.Cleanup(func() { testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE id = $1`, otherTask) })
	otherToken := mintGoalPlanTaskToken(t, testWorkspaceID, otherTask, agentID)
	if code, _ := submitPlanResultAsRun(t, otherToken, goal.ID, created.ID, body); code != http.StatusNotFound {
		t.Fatalf("foreign task result = %d, want 404", code)
	}

	// A run in another workspace cannot reach this goal either: the token's
	// workspace binding scopes the task lookup, so it answers the same 404
	// instead of leaking that a generation exists.
	foreignWorkspace := dbfx.Workspace(t, "Goal Plan Foreign WS "+t.Name(),
		fmt.Sprintf("goal-plan-foreign-%d", time.Now().UnixNano()))
	dbfx.Member(t, foreignWorkspace, testUserID, "owner")
	foreignFixture := testutil.New(testPool, foreignWorkspace, testUserID)
	foreignAgent := foreignFixture.Agent(t, "Goal Plan Foreign Agent", handlerTestRuntimeID(t), nil)
	foreignTask := foreignFixture.Task(t, foreignAgent, testutil.Cols{
		"runtime_id": handlerTestRuntimeID(t),
		"status":     "running",
		"started_at": testutil.Raw("now()"),
	})
	foreignToken := mintGoalPlanTaskToken(t, foreignWorkspace, foreignTask, foreignAgent)
	if code, _ := submitPlanResultAsRun(t, foreignToken, goal.ID, created.ID, body); code != http.StatusNotFound {
		t.Fatalf("cross-workspace task result = %d, want 404", code)
	}

	// The run's own token is the one credential that may submit.
	ownerToken := mintGoalPlanTaskToken(t, testWorkspaceID, *created.TaskID, agentID)
	code, failed := submitPlanResultAsRun(t, ownerToken, goal.ID, created.ID,
		map[string]any{"error": "the project has no readable resources"})
	if code != http.StatusOK {
		t.Fatalf("failure result = %d, want 200", code)
	}
	if failed.State != "failed" || planHasCandidate(failed.Candidate) || failed.Error == "" {
		t.Fatalf("failure result = %+v, want failed with an error and no candidate", failed)
	}

	// Once failed, an idempotent retry returns the stored failure rather than
	// reviving the run.
	if code, again := submitPlanResultAsRun(t, ownerToken, goal.ID, created.ID, body); code != http.StatusOK || again.State != "failed" {
		t.Fatalf("retry after failure = %d state=%q, want 200 failed", code, again.State)
	}
}

func TestGoalPlanProjectMoveBlockedAndGoalDeleteCleanup(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	projectID := createProjectForPlan(t, "Plan move project "+t.Name())
	otherProject := createTestProjectForResources(t, "Plan move target "+t.Name())
	goal := planTestGoal(t, projectID)

	if code, _ := putPlan(t, goal.ID, map[string]any{
		"expected_revision": 0,
		"items": []map[string]any{{
			"id": "018f3a9e-3333-7c2b-9f1e-000000000001", "title": "node", "binding": map[string]any{"type": "new"},
		}},
	}); code != http.StatusOK {
		t.Fatalf("PutGoalPlan = %d, want 200", code)
	}

	// The saved draft names the current project, so the goal cannot move while
	// it exists: the plan would silently describe the wrong codebase.
	code, _ := updateGoalViaAPI(t, testUserID, goal.ID, map[string]any{"project_id": otherProject.ID})
	if code != http.StatusConflict {
		t.Fatalf("project move with a plan = %d, want 409", code)
	}

	// Deleting the goal removes its plan rows with it.
	w := goalRequest(http.MethodDelete, "/api/goals/"+goal.ID, testUserID, nil, map[string]string{"id": goal.ID})
	if code := testutil.Call(t, testHandler.DeleteGoal, w).Code; code != http.StatusNoContent {
		t.Fatalf("DeleteGoal = %d, want 204", code)
	}
	var plans, generations int
	dbfx.QueryRow(t, `
		SELECT
			(SELECT count(*) FROM goal_plan WHERE goal_id = $1),
			(SELECT count(*) FROM goal_plan_generation WHERE goal_id = $1)
	`, goal.ID).Scan(&plans, &generations)
	if plans != 0 || generations != 0 {
		t.Fatalf("plan rows after goal delete plans=%d generations=%d, want 0/0", plans, generations)
	}
}

// goalPlanResultRouter mounts the result endpoint behind the real Auth and
// workspace-member middleware, the same stack the production router uses.
func goalPlanResultRouter() http.Handler {
	router := chi.NewRouter()
	router.Use(middleware.Auth(testHandler.Queries, nil, nil, nil))
	router.Use(middleware.RequireWorkspaceMember(testHandler.Queries))
	router.Post("/api/goals/{id}/plan/generations/{generationId}/result", testHandler.SubmitGoalPlanGenerationResult)
	return router
}

// mintGoalPlanTaskToken mints the short-lived mat_ credential the daemon
// injects into the run's agent process. Requests carry only this token: the
// actor headers are written by Auth from the token row, never by the caller.
func mintGoalPlanTaskToken(t *testing.T, workspaceID, taskID, agentID string) string {
	t.Helper()
	token := fmt.Sprintf("mat_goalplan_%s_%d", taskID, time.Now().UnixNano())
	if _, err := testHandler.Queries.CreateTaskToken(context.Background(), db.CreateTaskTokenParams{
		TokenHash:   auth.HashToken(token),
		TaskID:      parseUUID(taskID),
		AgentID:     parseUUID(agentID),
		WorkspaceID: parseUUID(workspaceID),
		UserID:      parseUUID(testUserID),
		ExpiresAt:   pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
	}); err != nil {
		t.Fatalf("mint task token: %v", err)
	}
	t.Cleanup(func() {
		testHandler.Queries.DeleteTaskTokensByTask(context.Background(), parseUUID(taskID))
	})
	return token
}

// memberSessionToken signs an interactive member session for userID.
func memberSessionToken(t *testing.T, userID string) string {
	t.Helper()
	claims := jwt.MapClaims{
		"sub": userID,
		"exp": time.Now().Add(time.Hour).Unix(),
		"iat": time.Now().Unix(),
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(auth.JWTSecret())
	if err != nil {
		t.Fatalf("sign session token: %v", err)
	}
	return signed
}

func submitPlanResultAsRun(t *testing.T, token, goalID, generationID string, body map[string]any) (int, GoalPlanGenerationResponse) {
	t.Helper()
	rec := postPlanResultRaw(token, "", goalID, generationID, body, nil)
	var resp GoalPlanGenerationResponse
	if rec.Code == http.StatusOK {
		decodePlanJSON(t, rec, &resp)
	}
	return rec.Code, resp
}

// postPlanResultRaw sends a result through the Auth stack. workspaceQuery
// identifies the workspace for a member session; a task token ignores it and
// is scoped by its binding. It never touches testing.T so concurrent tests can
// call it from another goroutine.
func postPlanResultRaw(token, workspaceQuery, goalID, generationID string, body map[string]any, forged map[string]string) *httptest.ResponseRecorder {
	path := "/api/goals/" + goalID + "/plan/generations/" + generationID + "/result"
	if workspaceQuery != "" {
		path += "?workspace_id=" + workspaceQuery
	}
	req := newRequest(http.MethodPost, path, body)
	req.Header.Set("Authorization", "Bearer "+token)
	// Identity must come from the credential, not the transport: these are
	// exactly the headers a forging client would supply.
	req.Header.Del("X-User-ID")
	req.Header.Del("X-Workspace-ID")
	for key, value := range forged {
		req.Header.Set(key, value)
	}
	rec := httptest.NewRecorder()
	goalPlanResultRouter().ServeHTTP(rec, req)
	return rec
}

func decodePlanJSON(t *testing.T, rec *httptest.ResponseRecorder, out any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
}

func jsonUnmarshalStrict(raw []byte, out any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	return dec.Decode(out)
}

// planHasCandidate reports whether a response carried a real candidate; a
// nil RawMessage and the JSON literal null both mean "none".
func planHasCandidate(raw json.RawMessage) bool {
	return len(raw) > 0 && string(raw) != "null"
}

func TestGoalPlanGenerationStateFollowsTask(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	projectID := createProjectForPlan(t, "Plan state project "+t.Name())
	goal := planTestGoal(t, projectID)
	agentID := createHandlerTestAgent(t, "Plan State Agent", []byte("[]"))
	code, created := postGeneration(t, goal.ID, map[string]any{
		"intent":            "produce a plan",
		"request_key":       "gen-state",
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

	// The generation has no daemon hook; its visible state comes from the task
	// the run actually produced, so a client polling it never sees a stale
	// "queued" after the run died.
	taskID := *created.TaskID
	setTaskStatus := func(status string) {
		if _, err := testPool.Exec(ctx,
			`UPDATE agent_task_queue SET status = $2 WHERE id = $1`, taskID, status); err != nil {
			t.Fatalf("set task status %s: %v", status, err)
		}
	}

	setTaskStatus("running")
	if state := getGenerationState(t, goal.ID, created.ID); state != "running" {
		t.Fatalf("state while running = %q, want running", state)
	}

	setTaskStatus("completed")
	_, generation := getGeneration(t, goal.ID, created.ID)
	if generation.State != "failed" {
		t.Fatalf("state after a task completed without a result = %q, want failed", generation.State)
	}
	if generation.Error == "" {
		t.Fatalf("a run that ended without a plan must explain itself, got no error")
	}
}

func TestGoalPlanWorkspaceDeletionRemovesPlanRows(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	marker := fmt.Sprintf("goal-plan-ws-del-%d", time.Now().UnixNano())

	seed := func(suffix string) (workspaceID, goalID string) {
		workspaceID = dbfx.Workspace(t, "Goal Plan WS Delete "+suffix, marker+"-"+suffix)
		dbfx.Member(t, workspaceID, testUserID, "owner")
		fx := testutil.New(testPool, workspaceID, testUserID)
		projectID := fx.Project(t, "Goal Plan WS Delete Project "+suffix)
		goalID = fx.Insert(t, "goal", testutil.Cols{
			"workspace_id": workspaceID,
			"project_id":   projectID,
			"title":        "Goal Plan WS Delete Goal " + suffix,
			"creator_type": "member",
			"creator_id":   testUserID,
		})
		// A minimal generation-shaped row: the workspace sweep must remove it
		// even though no task or chat session backs it.
		fx.Insert(t, "goal_plan", testutil.Cols{
			"workspace_id": workspaceID,
			"goal_id":      goalID,
			"project_id":   projectID,
			"content":      testutil.Raw(`'{"items":[],"edges":[]}'::jsonb`),
		})
		agentID := fx.Agent(t, "Goal Plan WS Delete Agent "+suffix, handlerTestRuntimeID(t), nil)
		fx.Insert(t, "goal_plan_generation", testutil.Cols{
			"workspace_id": workspaceID,
			"goal_id":      goalID,
			"project_id":   projectID,
			"request_key":  "ws-del-" + suffix,
			"intent":       "seed",
			"agent_id":     agentID,
		})
		return workspaceID, goalID
	}

	deletedWorkspace, deletedGoal := seed("deleted")
	keptWorkspace, keptGoal := seed("kept")

	w := httptest.NewRecorder()
	testHandler.DeleteWorkspace(w, withURLParam(
		newRequest(http.MethodDelete, "/api/workspaces/"+deletedWorkspace, nil), "id", deletedWorkspace))
	if w.Code != http.StatusNoContent {
		t.Fatalf("DeleteWorkspace = %d, want 204: %s", w.Code, w.Body.String())
	}

	if plans, generations := planRowsByWorkspace(t, deletedWorkspace, deletedGoal); plans != 0 || generations != 0 {
		t.Fatalf("deleted workspace plan rows plans=%d generations=%d, want 0/0", plans, generations)
	}
	if plans, generations := planRowsByWorkspace(t, keptWorkspace, keptGoal); plans != 1 || generations != 1 {
		t.Fatalf("other workspace plan rows plans=%d generations=%d, want 1/1", plans, generations)
	}
}

func planRowsByWorkspace(t *testing.T, workspaceID, goalID string) (plans, generations int) {
	t.Helper()
	dbfx.QueryRow(t, `
		SELECT
			(SELECT count(*) FROM goal_plan WHERE goal_id = $1 AND workspace_id = $2),
			(SELECT count(*) FROM goal_plan_generation WHERE goal_id = $1 AND workspace_id = $2)
	`, goalID, workspaceID).Scan(&plans, &generations)
	return plans, generations
}

func getGeneration(t *testing.T, goalID, generationID string) (int, GoalPlanGenerationResponse) {
	t.Helper()
	var resp GoalPlanGenerationResponse
	rec := getGenerationRaw(t, goalID, generationID)
	if rec.Code == http.StatusOK {
		decodePlanJSON(t, rec, &resp)
	}
	return rec.Code, resp
}

func getGenerationRaw(t *testing.T, goalID, generationID string) *httptest.ResponseRecorder {
	t.Helper()
	res := testutil.Call(t, testHandler.GetGoalPlanGeneration,
		goalRequest(http.MethodGet, "/api/goals/"+goalID+"/plan/generations/"+generationID, testUserID, nil,
			map[string]string{"id": goalID, "generationId": generationID}))
	return res.ResponseRecorder
}

func getGenerationState(t *testing.T, goalID, generationID string) string {
	t.Helper()
	code, resp := getGeneration(t, goalID, generationID)
	if code != http.StatusOK {
		t.Fatalf("GetGoalPlanGeneration = %d, want 200", code)
	}
	return resp.State
}
