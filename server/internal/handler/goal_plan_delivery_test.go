package handler

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/workmethod"
)

func TestGoalPlanDeliverySeedsAcceptanceAndPreservesStartedMethod(t *testing.T) {
	project := createProjectForPlan(t, "Typed delivery project")
	goal := planTestGoal(t, project)
	issue := createIssueForTest(t, map[string]any{
		"title":      "Compare retrieval evidence",
		"project_id": project,
	})
	if status := linkGoalIssueViaAPI(t, testUserID, goal.ID, issue.ID); status != http.StatusNoContent {
		t.Fatalf("link issue: HTTP %d", status)
	}
	const nodeID = "018f3a9e-2222-7c2b-9f1e-000000000021"
	delivery := &GoalPlanDelivery{
		Type: "research", MethodID: "seven-step",
		Criteria: []GoalPlanCriterion{{ID: "E1", Expected: "The conclusion cites the comparison", Verification: "Open both source versions and compare"}},
	}
	content := GoalPlanContent{
		Items: []GoalPlanItem{
			{ID: nodeID, Title: "Compare the results", Binding: GoalPlanItemBinding{Type: "existing", IssueID: &issue.ID}, Delivery: delivery},
			{ID: "018f3a9e-2222-7c2b-9f1e-000000000022", Title: "Source archive", Binding: GoalPlanItemBinding{Type: "new"}},
		},
		Edges: []GoalPlanEdge{{DependentItemID: nodeID, PrerequisiteItemID: "018f3a9e-2222-7c2b-9f1e-000000000022", Kind: "conditional", Reason: "Only when the comparison uses archival data"}},
	}
	save := func(revision, want int) {
		t.Helper()
		rec := putPlanRaw(t, goal.ID, map[string]any{
			"expected_revision": revision,
			"items":             content.Items, "edges": content.Edges,
		})
		if rec.Code != want {
			t.Fatalf("save plan: HTTP %d %s, want %d", rec.Code, rec.Body.String(), want)
		}
	}
	save(0, http.StatusBadRequest)
	raw, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testHandler.Queries.CreateGoalPlan(t.Context(), db.CreateGoalPlanParams{
		WorkspaceID: parseUUID(testWorkspaceID), GoalID: parseUUID(goal.ID), ProjectID: parseUUID(project),
		Content: raw, ContextSnapshot: []byte("{}"),
	}); err != nil {
		t.Fatal(err)
	}
	save(1, http.StatusOK)
	req := func(method, suffix string, body any) *http.Request {
		return withURLParam(newRequest(method, "/api/issues/"+issue.ID+suffix, body), "id", issue.ID)
	}
	var ready struct {
		workmethod.Run
		Planned *GoalPlanDelivery `json:"planned_delivery"`
	}
	testutil.Call(t, testHandler.GetIssueWorkMethod, req(http.MethodGet, "/work-method", nil)).Want(200).JSON(&ready)
	if ready.Method == nil || ready.Method.ID != "seven-step" || ready.Planned == nil || ready.Planned.Criteria[0].ID != "E1" || ready.State != "ready" {
		t.Fatalf("issue did not read plan delivery: %+v", ready)
	}
	original, err := testHandler.Queries.GetIssue(t.Context(), parseUUID(issue.ID))
	if err != nil {
		t.Fatal(err)
	}
	brief := testHandler.claimProjectWorkflow(t.Context(), original, nil, false)
	for _, want := range []string{"research", "seven-step", "E1", "Source archive", "conditional", "Only when the comparison uses archival data"} {
		if brief == nil || !strings.Contains(brief.Brief, want) {
			t.Fatalf("claim missing %q: %+v", want, brief)
		}
	}
	revision := ready.Revision
	testutil.Call(t, testHandler.ChangeIssueWorkMethod, req(http.MethodPost, "/work-method", workmethod.Change{Action: "start", MethodID: "seven-step", BaseRevision: &revision})).Want(200)

	readTrace := func() IssueResponse {
		t.Helper()
		var view IssueResponse
		testutil.Call(t, testHandler.GetIssue, req(http.MethodGet, "", nil)).Want(200).JSON(&view)
		return view
	}
	started := readTrace()
	if started.AcceptanceTrace == nil || len(started.AcceptanceTrace.Criteria) != 1 ||
		started.AcceptanceTrace.Criteria[0].ID != "E1" ||
		started.AcceptanceTrace.Criteria[0].Versions[0] != "The conclusion cites the comparison\n\nOpen both source versions and compare" {
		t.Fatalf("planned criterion was not initialized: %+v", started.AcceptanceTrace)
	}

	delivery.MethodID = "okr"
	delivery.Criteria[0].Expected = "A later planning proposal"
	save(2, http.StatusOK)
	var after workmethod.Run
	testutil.Call(t, testHandler.GetIssueWorkMethod, req(http.MethodGet, "/work-method", nil)).Want(200).JSON(&after)
	if after.Method == nil || after.Method.ID != "seven-step" || after.State != "working" {
		t.Fatalf("edited plan overwrote the frozen method: %+v", after)
	}
	trace := readTrace().AcceptanceTrace
	if trace == nil || trace.Revision != started.AcceptanceTrace.Revision || len(trace.Criteria[0].Versions) != 1 {
		t.Fatalf("edited plan overwrote the issue's acceptance: %+v", trace)
	}
}
