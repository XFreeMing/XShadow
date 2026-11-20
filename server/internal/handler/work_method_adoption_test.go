package handler

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/pkg/workmethod"
)

func TestWorkingActorReadsNewMethodAndSubmitsWithoutRedispatch(t *testing.T) {
	seedTestCatalog(t)
	agent := seededReadyAgentID(t)
	project := deliveryProject(t, "Adopt revision", agent)
	m := workmethod.Template{Name: "Guidance " + workflowTestSuffix(), Kind: "research", Steps: []workmethod.Step{
		{Name: "Candidate", Instructions: "Initial instructions", Output: "Candidate", Checks: []string{"independent evidence"}, Owner: &workmethod.Actor{Kind: "agent", ID: agent}, Reviewer: &workmethod.Actor{Kind: "member", ID: testUserID}},
	}, Review: "Independent approval"}
	var original workmethod.Template
	testutil.Call(t, testHandler.CreateWorkMethod, newRequest("POST", "/api/work-methods?workspace_id="+testWorkspaceID, m)).Want(201).JSON(&original)
	id := dbfx.Issue(t, "Work with original task", testutil.Cols{"project_id": project, "status": "todo"})
	url := "/api/issues/" + id + "/work-method"
	method := func(command map[string]any, taskID string, want int) workmethod.Run {
		t.Helper()
		req := goalRequest("POST", url, testUserID, command, map[string]string{"id": id})
		if taskID != "" {
			req.Header.Set("X-Agent-ID", agent)
			req.Header.Set("X-Task-ID", taskID)
		}
		resp := testutil.Call(t, testHandler.ChangeIssueWorkMethod, req).Want(want)
		var r workmethod.Run
		if want == 200 {
			resp.JSON(&r)
		}
		return r
	}
	start := method(map[string]any{"action": "select", "base_revision": 0, "method_id": original.ID}, "", 200)
	task := methodStageTask(t, id, start.Revision)
	taskID := uuidToString(task.ID)
	m = original
	m.Steps[0].Instructions = "Inspect the original before comparing the candidate"
	testutil.Call(t, testHandler.UpdateWorkMethod, withURLParam(newRequest("PUT", "/api/work-methods/"+m.ID+"?workspace_id="+testWorkspaceID, struct {
		workmethod.Template
		Base int `json:"base_revision"`
	}{m, m.Revision}), "id", m.ID)).Want(200)
	before, err := testHandler.Queries.GetIssue(t.Context(), util.MustParseUUID(id))
	if err != nil {
		t.Fatal(err)
	}
	adopt := map[string]any{"action": "adopt_revision", "base_revision": start.Revision, "method_id": m.ID, "method_revision": 2, "evidence": "Validated updated guidance"}
	method(adopt, taskID, http.StatusForbidden)
	after := method(adopt, "", 200)
	if dup := method(adopt, "", 200); !reflect.DeepEqual(dup, after) {
		t.Fatal("duplicate history")
	}
	if after.Method.Revision != 2 || !reflect.DeepEqual(start.CurrentActor, after.CurrentActor) || after.Step != start.Step || after.State != start.State || len(after.History) != 2 || taskCountFor(t, id, agent) != 1 {
		t.Fatal(after)
	}
	issue, err := testHandler.Queries.GetIssue(t.Context(), util.MustParseUUID(id))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.AcceptanceCriteria, issue.AcceptanceCriteria) || before.Status != issue.Status || before.AssigneeID != issue.AssigneeID || before.AssigneeType != issue.AssigneeType {
		t.Fatal("board, ledger, or assignee changed on adoption")
	}
	brief := testHandler.claimProjectWorkflow(t.Context(), issue, &task, false)
	if brief == nil || brief.stepMoved || !strings.Contains(brief.Brief, m.Steps[0].Instructions) {
		t.Fatalf("old task could not claim new step: %+v", brief)
	}
	method(map[string]any{"action": "advance", "base_revision": start.Revision, "evidence": "obsolete revision"}, taskID, 409)
	viewReq := goalRequest("GET", url, testUserID, nil, map[string]string{"id": id})
	viewReq.Header.Set("X-Agent-ID", agent)
	viewReq.Header.Set("X-Task-ID", taskID)
	var fresh struct {
		workmethod.Run
		CanAct bool `json:"can_act"`
	}
	testutil.Call(t, testHandler.GetIssueWorkMethod, viewReq).Want(200).JSON(&fresh)
	if !fresh.CanAct || fresh.Method.Revision != 2 {
		t.Fatal(fresh)
	}
	review := method(map[string]any{"action": "advance", "base_revision": fresh.Revision, "evidence": "candidate"}, taskID, 200)
	method(adopt, "", 409)
	method(map[string]any{"action": "approve", "base_revision": review.Revision, "checked_items": []int{0}, "evidence": "cannot self approve"}, taskID, 403)
	done := method(map[string]any{"action": "approve", "base_revision": review.Revision, "checked_items": []int{0}, "evidence": "independent verdict"}, "", 200)
	// Two clients act on the same execution revision. The winner alone may
	// append an event, regardless of whether the row is read before or after it commits.
	raceID := dbfx.Issue(t, "Race guidance adoption against a handoff", testutil.Cols{"project_id": project, "status": "todo"})
	raceURL := "/api/issues/" + raceID + "/work-method"
	startReq := goalRequest("POST", raceURL, testUserID, map[string]any{"action": "select", "base_revision": 0, "method_id": m.ID}, map[string]string{"id": raceID})
	testutil.Call(t, testHandler.ChangeIssueWorkMethod, startReq).Want(200)
	m.Revision = 2
	m.Steps[0].Instructions = "New independent check"
	testutil.Call(t, testHandler.UpdateWorkMethod, withURLParam(newRequest("PUT", "/api/work-methods/"+m.ID+"?workspace_id="+testWorkspaceID, struct {
		workmethod.Template
		Base int `json:"base_revision"`
	}{m, 2}), "id", m.ID)).Want(200)
	bound := methodStageTask(t, raceID, 1)
	requests := []*http.Request{
		goalRequest("POST", raceURL, testUserID, map[string]any{"action": "adopt_revision", "base_revision": 1, "method_id": m.ID, "method_revision": 3, "evidence": "use newer guidance"}, map[string]string{"id": raceID}),
		goalRequest("POST", raceURL, testUserID, map[string]any{"action": "advance", "base_revision": 1, "evidence": "candidate"}, map[string]string{"id": raceID}),
	}
	requests[1].Header.Set("X-Agent-ID", agent)
	requests[1].Header.Set("X-Task-ID", uuidToString(bound.ID))
	gate := make(chan struct{})
	codes := make(chan int, 2)
	for _, req := range requests {
		go func() {
			<-gate
			w := httptest.NewRecorder()
			testHandler.ChangeIssueWorkMethod(w, req)
			codes <- w.Code
		}()
	}
	close(gate)
	results := []int{<-codes, <-codes}
	slices.Sort(results)
	raced := readDeliveryMethod(t, raceID)
	if !slices.Equal(results, []int{200, 409}) || raced.Revision != 2 || len(raced.History) != 2 {
		t.Fatal(results, raced)
	}
	if done.State != "closed" {
		t.Fatal(done)
	}
}
