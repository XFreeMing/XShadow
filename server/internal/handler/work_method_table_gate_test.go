package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/workmethod"
)

func TestLiveTaskFormulaGatesWorkMethodAndWakesOnce(t *testing.T) {
	suffix := uuid.NewString()[:8]
	addr, _ := startRustCandidateForTest(t, "task-formula-"+suffix)
	h := *testHandler
	h.IssueReadBridge = &IssueReadBridge{Upstream: addr, Token: "task-formula-" + suffix}
	tasks := *h.TaskService
	tasks.ProjectIssueFormula = h.IssueReadBridge.ProjectTaskFormula
	h.TaskService = &tasks
	ctx := context.Background()

	interaction := createTestProperty(t, map[string]any{"name": "Interaction " + suffix, "type": "checkbox"})
	prototype := createTestProperty(t, map[string]any{"name": "Prototype " + suffix, "type": "text"})
	expression := fmt.Sprintf("IF(({%s} = BLANK), FALSE, IF({%s}, IF({%s}, TRUE, FALSE), TRUE))", interaction.Name, interaction.Name, prototype.Name)
	var formula PropertyResponse
	testutil.Call(t, h.CreateProperty, newRequest("POST", "/api/properties", map[string]any{
		"name": "Design ready " + suffix, "type": "formula", "config": map[string]any{"expression": expression},
	})).Want(http.StatusCreated).JSON(&formula)
	t.Cleanup(func() { deleteTestProperty(t, formula.ID) })

	issueType := createTestIssueType(t, map[string]any{"name": "Story " + suffix, "fields": []map[string]any{
		{"property_id": interaction.ID}, {"property_id": prototype.ID}, {"property_id": formula.ID},
	}})
	method := workmethod.Template{Name: "Evidence " + suffix, Kind: "research", Review: "Review the recorded result",
		Steps: []workmethod.Step{{Name: "Design", Output: "Interactive prototype when needed",
			TableGate: &workmethod.TableGate{IssueTypeID: issueType.ID, PropertyID: formula.ID}}}}
	testutil.Call(t, h.CreateWorkMethod, newRequest("POST", "/api/work-methods", method)).Want(http.StatusCreated).JSON(&method)
	issue := createIssueWithPropertyBody(t, map[string]any{"title": "Task " + suffix, "issue_type_id": issueType.ID, "status": "in_progress"})
	path := "/api/issues/" + issue.ID
	withIssue := func(verb, subpath string, body any) *http.Request {
		return withURLParam(newRequest(verb, path+subpath, body), "id", issue.ID)
	}
	var run workmethod.Run
	testutil.Call(t, h.GetIssueWorkMethod, withIssue("GET", "/work-method", nil)).Want(http.StatusOK).JSON(&run)
	if run.Method == nil || run.Method.ID != method.ID || run.Source != "issue_type" {
		t.Fatalf("unexpected type recommendation: %+v", run)
	}
	advance := func() *testutil.Response {
		return testutil.Call(t, h.ChangeIssueWorkMethod, withIssue("POST", "/work-method", map[string]any{
			"action": "advance", "base_revision": 0, "evidence": "Reviewed the design",
		}))
	}
	var blocked struct {
		Code string `json:"code"`
	}
	advance().Want(http.StatusConflict).JSON(&blocked)
	if blocked.Code != "work_method_evidence_not_ready" {
		t.Fatal(blocked)
	}

	svc := service.IssueWakeupService{Tasks: &tasks}
	agent := handlerTestAgentID(t)
	condition, err := json.Marshal(service.WakeupCondition{Type: "issue_field", Field: "formula", PropertyID: formula.ID, Value: json.RawMessage("true")})
	if err != nil {
		t.Fatal(err)
	}
	createRule := func() string {
		rule, err := svc.Create(ctx, parseUUID(issue.ID), parseUUID(testUserID), pgtype.UUID{}, service.WakeupInput{
			AgentID: agent, Kind: "event", Mode: "once", Instruction: "Review the design evidence", Condition: condition,
		})
		if err != nil {
			t.Fatal(err)
		}
		return uuidToString(rule.ID)
	}
	rule := createRule()
	if again := createRule(); again != rule {
		t.Fatalf("registration retry created rule %s", again)
	}
	t.Cleanup(func() { cleanupChildDoneIssue(issue.ID) })
	tick := func() {
		if err := svc.TickWorkspaces(ctx, parseUUID(testWorkspaceID)); err != nil {
			t.Fatal(err)
		}
	}
	set := func(id string, value any) {
		req := withIssuePropertyParams(newRequest("PUT", path+"/properties/"+id, map[string]any{"value": value}), issue.ID, id)
		testutil.Call(t, h.SetIssueProperty, req).Want(http.StatusOK)
	}
	assertFormula := func(want string) {
		value, err := h.IssueReadBridge.ProjectTaskFormula(ctx, parseUUID(testWorkspaceID), parseUUID(testUserID), parseUUID(issue.ID), formula.ID)
		if err != nil || string(value) != want {
			t.Fatalf("formula = %s, err = %v, want %s", value, err, want)
		}
	}
	assertFormula("false")
	tick()
	set(interaction.ID, false)
	assertFormula("true")
	set(interaction.ID, true)
	assertFormula("false")
	tick()
	if n := wakeupRunsOf(t, rule); n != 0 {
		t.Fatalf("incomplete evidence queued %d runs", n)
	}
	setStatusAsRun(t, issue.ID, "blocked", "", "")
	set(prototype.ID, "Fixture /prototype, change identity and observe denied access")
	assertFormula("true")
	tick()
	if n := wakeupRunsOf(t, rule); n != 0 {
		t.Fatalf("blocked task queued %d runs", n)
	}
	setStatusAsRun(t, issue.ID, "in_progress", "", "")
	tick()
	tick()
	if n := wakeupRunsOf(t, rule); n != 1 {
		t.Fatalf("ready event queued %d runs, want one", n)
	}
	if again := createRule(); again != rule {
		t.Fatalf("consumed registration retry created rule %s", again)
	}
	advance().Want(http.StatusOK).JSON(&run)
	if run.State != "review" || run.Revision != 1 {
		t.Fatalf("ready evidence did not enter review: %+v", run)
	}
	req := withIssuePropertyParams(newRequest("DELETE", path+"/properties/"+prototype.ID, nil), issue.ID, prototype.ID)
	testutil.Call(t, h.DeleteIssueProperty, req).Want(http.StatusOK)
	testutil.Call(t, h.ChangeIssueWorkMethod, withIssue("POST", "/work-method", map[string]any{
		"action": "approve", "base_revision": 1, "evidence": "Reviewed after changes",
	})).Want(http.StatusConflict)
}
