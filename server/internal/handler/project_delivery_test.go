package handler

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/acceptance"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/workmethod"
)

func TestProjectActionsDeliveryWaitsForMatchingReclaimReceipt(t *testing.T) {
	seedTestCatalog(t)
	project := dbfx.Project(t, "Project controlled GitHub delivery")
	daemonID := "project-delivery-daemon"
	runtime := dbfx.Runtime(t, "Project delivery runtime", testutil.Cols{
		"daemon_id": daemonID, "runtime_mode": "local",
	})
	foreign := dbfx.Runtime(t, "Other machine", testutil.Cols{
		"daemon_id": "other-project-delivery-daemon", "runtime_mode": "local", "provider": "other-delivery",
	})
	target := workmethod.GitHubDelivery{Repository: "owner/project", BaseRef: "dev"}
	resource, err := validateLocalDirectoryRef(json.RawMessage(`{"local_path":"/project","daemon_id":"project-delivery-daemon","github_actions_delivery":{"repository":"owner/project","base_ref":"dev"}}`))
	if err != nil {
		t.Fatal(err)
	}
	dbfx.Insert(t, "project_resource", testutil.Cols{
		"project_id": project, "workspace_id": testWorkspaceID, "resource_type": "local_directory", "resource_ref": resource,
	})
	head := strings.Repeat("b", 40)
	trace := acceptance.Trace{Criteria: []acceptance.Criterion{{
		ID: "AC1", Versions: []string{"Read new content"},
		Cases:           []acceptance.TestCase{{ID: "T1", Runs: []acceptance.Run{{Version: 1, Result: "pass", Candidate: head, Evidence: "Independent browser"}}}},
		Implementations: []acceptance.Implementation{{Version: 1, Repository: target.Repository, Branch: "agent/test/delivery", Commit: head}},
	}}}
	data := func(value any) []byte {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	m := workmethod.Builtins()[0]
	run := workmethod.Run{Method: &m, State: "review", Step: len(m.Steps) - 1, Revision: 7}
	id := dbfx.Issue(t, "Approved Actions delivery", testutil.Cols{
		"project_id": project, "status": "in_review", "acceptance_criteria": data(trace),
	})
	issue, err := testHandler.Queries.GetIssueInWorkspace(t.Context(), db.GetIssueInWorkspaceParams{ID: parseUUID(id), WorkspaceID: parseUUID(testWorkspaceID)})
	if err != nil {
		t.Fatal(err)
	}
	if err := testHandler.bindProjectDelivery(t.Context(), issue, &run); err != nil || run.ProjectDelivery == nil || *run.Method.GitHubDelivery != target {
		t.Fatalf("method did not inherit project delivery: %+v, %v", run, err)
	}
	dbfx.Exec(t, "UPDATE issue SET work_method_run=$1 WHERE id=$2", data(run), id)
	list := func(runtimeID string) []workmethod.DeliveryJob {
		t.Helper()
		req := withURLParam(newOwnerDaemonRequest(http.MethodGet, "/daemon/project-deliveries", nil), "runtimeId", runtimeID)
		var jobs []workmethod.DeliveryJob
		testutil.Call(t, testHandler.ListProjectDeliveryJobs, req).Want(200).JSON(&jobs)
		return jobs
	}
	if len(list(runtime)) != 0 {
		t.Fatal("an unapproved candidate reached the delivery machine")
	}
	advance := withURLParam(newRequest(http.MethodPost, "/work-method", map[string]any{
		"action": "approve", "base_revision": 7, "evidence": "Independent checks approved",
	}), "id", id)
	testutil.Call(t, testHandler.ChangeIssueWorkMethod, advance).Want(200).JSON(&run)
	jobs := list(runtime)
	if len(jobs) != 1 || jobs[0].Head != head || len(list(foreign)) != 0 {
		t.Fatalf("invalid jobs for the bound delivery machine: %+v", jobs)
	}
	report := func(runtimeID, sha string, receipt workmethod.DeliveryReceipt, status int) {
		t.Helper()
		req := goalRequest(http.MethodPost, "/daemon/project-deliveries/"+id, testUserID, map[string]any{
			"revision": run.Revision, "head": sha, "receipt": receipt,
		}, map[string]string{"runtimeId": runtimeID, "issueId": id})
		testutil.Call(t, testHandler.ReportProjectDelivery, req).Want(status)
	}
	receipt := workmethod.DeliveryReceipt{State: workmethod.DeliveryCleanup, PullRequest: 8, Base: strings.Repeat("a", 40), Merge: strings.Repeat("c", 40)}
	report(foreign, head, receipt, 404)
	report(runtime, receipt.Base, receipt, 409)
	report(runtime, head, receipt, 204)
	if status, _, _ := issueStatusAndAssignee(t, id); status != issuestatus.InReview {
		t.Fatalf("marked done before worktree cleanup: %s", status)
	}
	receipt.State = workmethod.DeliveryDone
	report(runtime, head, receipt, 204)
	report(runtime, head, receipt, 204)
	if status, _, _ := issueStatusAndAssignee(t, id); status != issuestatus.Done || len(list(runtime)) != 0 {
		t.Fatalf("delivery did not close once after cleanup: %s", status)
	}
}
