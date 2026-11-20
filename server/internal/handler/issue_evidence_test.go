package handler

import (
	"crypto/sha256"
	"fmt"
	"log/slog"
	"net/http"

	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/daemon/execenv"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestIssueEvidenceRunFromActiveOwner(t *testing.T) {
	ctx := t.Context()
	helper, runtimeID, daemonID := createRuntimeGuardAgent(t, ctx)
	dbfx.Exec(t, "UPDATE agent_runtime SET provider='claude' WHERE id=$1", runtimeID)
	writer := dbfx.Agent(t, "Implementer", testRuntimeID)
	project := dbfx.Project(t, "Evidence workflow")
	mainDir := t.TempDir()
	dbfx.Insert(t, "project_resource", testutil.Cols{"workspace_id": testWorkspaceID, "project_id": project, "resource_type": "local_directory", "resource_ref": fmt.Sprintf(`{"daemon_id":%q,"local_path":%q,"execution_mode":"in_place"}`, daemonID, mainDir), "label": "writer's bound directory"})
	issue := dbfx.Issue(t, "Collect evidence", testutil.Cols{"project_id": project, "assignee_type": "agent", "assignee_id": writer})
	source := dbfx.Task(t, writer, testutil.Cols{"issue_id": issue, "runtime_id": testRuntimeID, "status": "running", "session_id": "writer-session", "work_dir": mainDir, "originator_user_id": testUserID, "accountable_user_id": testUserID, "originator_source": "direct_human"})
	writerToken := mintGoalPlanTaskToken(t, testWorkspaceID, source, writer)
	other := dbfx.Issue(t, "Unrelated issue")
	content := "frozen document"
	store := installProxyModeStorage(t)
	file := func(issueID string) string {
		id := seedPreviewAttachment(t, store, "frozen/"+issueID+".txt", "frozen.txt", "text/plain", []byte(content))
		dbfx.Exec(t, "UPDATE attachment SET issue_id=$1 WHERE id=$2", issueID, id)
		return id
	}
	selected, foreign := file(issue), file(other)
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(content)))
	input := evidenceRequest{RequestID: uuid.NewString(), AgentID: helper, Question: "Count the letters.", Inputs: []evidenceInput{{selected, digest}}}
	router := chi.NewRouter()
	router.Use(middleware.Auth(testHandler.Queries, nil, nil, nil), middleware.RequireWorkspaceMember(testHandler.Queries))
	router.Post("/api/issues/{id}/evidence-runs", testHandler.StartIssueEvidenceRun)
	router.Get("/api/issues/{id}/evidence-runs", testHandler.ListIssueEvidenceRuns)
	router.Get("/api/issues/{id}/usage", testHandler.GetIssueUsage)
	router.Get("/api/attachments/{id}", testHandler.GetAttachmentByID)
	router.Post("/api/chat-sessions/{sessionId}/messages", testHandler.SendChatMessage)
	router.Delete("/api/chat-sessions/{sessionId}", testHandler.DeleteChatSession)
	router.Get("/api/chat-sessions/{sessionId}", testHandler.GetChatSession)
	call := func(token, method, path string, body any, status int) *testutil.Response {
		t.Helper()
		req := newRequest(method, path, body)
		req.Header.Del("X-User-ID")
		req.Header.Set("Authorization", "Bearer "+token)
		return testutil.Call(t, router.ServeHTTP, req).Want(status)
	}
	path := "/api/issues/" + issue + "/evidence-runs"
	member := memberSessionToken(t, testUserID)
	call(writerToken, "POST", "/api/issues/"+other+"/evidence-runs", input, 403)
	wrong := input
	wrong.Inputs = []evidenceInput{{foreign, digest}}
	call(member, "POST", path, wrong, 404)
	stranger := dbfx.User(t, "Other owner", uuid.NewString()+"@example.test")
	private := dbfx.Agent(t, "Private helper", runtimeID, testutil.Cols{"owner_id": stranger})
	denied := input
	denied.AgentID = private
	call(writerToken, "POST", path, denied, 403)
	method := func(owner string) {
		run := fmt.Sprintf(`{"state":"working","revision":2,"method":{"revision":1,"steps":[{"owner":{"kind":"agent","id":%q}}]}}`, owner)
		dbfx.Exec(t, "UPDATE issue SET work_method_run=$1 WHERE id=$2", run, issue)
	}
	method(helper)
	call(writerToken, "POST", path, input, 403)
	method(writer)
	dbfx.Exec(t, `UPDATE agent_task_queue SET context='{"work_method_revision":1}' WHERE id=$1`, source)
	call(writerToken, "POST", path, input, 403)
	dbfx.Exec(t, `UPDATE agent_task_queue SET context='{"work_method_revision":2}' WHERE id=$1`, source)
	taskID := call(writerToken, "POST", path, input, 201).Map()["task_id"].(string)
	if got := call(writerToken, "POST", path, input, 200).Map()["task_id"]; got != taskID {
		t.Fatalf("duplicate request returned %v", got)
	}
	wrong = input
	wrong.Question = "different request"
	call(writerToken, "POST", path, wrong, 409)
	queued, err := testHandler.Queries.GetAgentTask(ctx, parseUUID(taskID))
	if err != nil || queued.DelegatedFromTaskID != parseUUID(source) || queued.OriginatorUserID != parseUUID(testUserID) || queued.OriginatorSource.String != "delegation" || queued.InitiatorUserID.Valid || queued.Status != "queued" {
		t.Fatalf("unexpected queued task or attribution: source=%v attribution=%v status=%v err=%v", queued.DelegatedFromTaskID, queued.OriginatorSource, queued.Status, err)
	}
	var claim struct{ Task *AgentTaskResponse }
	req := withURLParam(newOwnerDaemonRequest("POST", "/claim", nil), "runtimeId", runtimeID)
	testutil.Call(t, testHandler.ClaimTaskByRuntime, req).Want(200).JSON(&claim)
	got := claim.Task
	if got == nil || got.ID != taskID {
		t.Fatal("expected named helper to claim its dedicated task")
	}
	if got.PriorWorkDir != "" || got.PriorSessionID != "" || got.ProjectID != "" || len(got.ProjectResources) != 0 || len(got.Repos) != 0 || got.IssueID != "" || got.AuthToken == "" {
		t.Fatal("evidence claim inherited workspace content, session, or directory, or lacks its scoped token")
	}
	if !strings.Contains(got.ChatMessage, selected) || !strings.Contains(got.ChatMessage, digest) || strings.Contains(got.ChatMessage, foreign) {
		t.Fatalf("unexpected helper input: %s", got.ChatMessage)
	}
	call(member, "POST", "/api/chat-sessions/"+got.ChatSessionID+"/messages", map[string]string{"content": "unrelated private text"}, 409)
	call(member, "DELETE", "/api/chat-sessions/"+got.ChatSessionID, nil, 409)
	call(member, "GET", "/api/chat-sessions/"+got.ChatSessionID, nil, 404)
	call(got.AuthToken, "GET", "/api/attachments/"+foreign, nil, 403)
	call(got.AuthToken, "POST", path, input, 403)
	call(got.AuthToken, "GET", path, nil, 403)
	metadata := call(got.AuthToken, "GET", "/api/attachments/"+selected, nil, 200).Map()
	if metadata["id"] != selected {
		t.Fatal("missing authorized attachment")
	}
	env, err := execenv.Prepare(execenv.PrepareParams{WorkspacesRoot: t.TempDir(), WorkspaceID: got.WorkspaceID, TaskID: got.ID}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer env.ReleaseLock()
	if env.WorkDir == mainDir || !strings.HasPrefix(env.WorkDir, env.RootDir+string(os.PathSeparator)) {
		t.Fatalf("invalid helper workdir %s", env.WorkDir)
	}
	download := withURLParam(newRequest("GET", metadata["download_url"].(string), nil), "id", selected)
	inputBytes := testutil.Call(t, testHandler.DownloadAttachmentWithCapability, download).Want(200).Body.Bytes()
	if fmt.Sprintf("%x", sha256.Sum256(inputBytes)) != digest {
		t.Fatal("frozen input digest mismatch")
	}
	if err := os.WriteFile(filepath.Join(env.WorkDir, "evidence.txt"), inputBytes, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(mainDir, "evidence.txt")); !os.IsNotExist(err) {
		t.Fatal("writer directory changed")
	}
	callback := func(h http.HandlerFunc, data any) {
		t.Helper()
		testutil.Call(t, h, withURLParam(newOwnerDaemonRequest("POST", "/api/daemon/tasks/"+taskID, data), "taskId", taskID)).Want(200)
	}
	callback(testHandler.StartTask, map[string]any{"runtime_id": got.RuntimeID, "dispatched_at": got.DispatchedAt})
	callback(testHandler.ReportTaskUsage, map[string]any{"usage": []map[string]any{{"provider": "anthropic", "model": "evidence-fixture", "input_tokens": 13, "output_tokens": 2}}})
	answer := "Frozen document matched " + digest
	callback(testHandler.CompleteTask, map[string]any{"output": answer, "work_dir": env.WorkDir})
	var rows []issueEvidenceReceipt
	call(member, "GET", path, nil, 200).JSON(&rows)
	if len(rows) != 1 || rows[0].Output != answer || rows[0].Status != "completed" || len(rows[0].Usage) != 1 || rows[0].Usage[0].InputTokens != 13 {
		t.Fatalf("issue evidence receipts = %+v", rows)
	}
	total := call(member, "GET", "/api/issues/"+issue+"/usage", nil, 200).Map()
	if total["total_input_tokens"] != float64(13) || total["terminal_task_count"] != float64(1) {
		t.Fatalf("issue usage=%+v", total)
	}
	active, err := testHandler.Queries.GetAgentTask(ctx, parseUUID(source))
	if err != nil || active.Status != "running" || active.WorkDir.String != mainDir {
		t.Fatal("writer's task changed")
	}
	call(member, "GET", "/api/issues/"+other+"/evidence-runs", nil, 200).JSON(&rows)
	if len(rows) != 0 {
		t.Fatal("receipt leaked to another issue")
	}
	dbfx.Exec(t, "UPDATE agent_task_queue SET status='completed' WHERE id=$1", source)
	call(writerToken, "POST", path, input, 403)
	input.RequestID = uuid.NewString()
	humanID := call(member, "POST", path, input, 201).Map()["task_id"].(string)
	human, err := testHandler.Queries.GetAgentTask(ctx, parseUUID(humanID))
	if err != nil || human.OriginatorSource.String != "direct_human" || human.DelegatedFromTaskID.Valid {
		t.Fatal("incorrect member run attribution")
	}
}
