package handler

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/issueclosure"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// DEV-7 closure contract at the write boundary. These tests exercise the
// persisted path — grant, independent review, accepted close — rather than the
// pure decision table, which issueclosure's own tests cover.
//
// The delivery identity is fixed: the reviewed baseline of this slice.
const (
	closureTestDelivery = "e7206f4d5ca0967b4868c70fae3f82e99f16ccf1"
	closureTestTree     = "50c145a22f6e099fc68f9be21933a89fb2d7c790"
)

func closureTestAgent(t *testing.T, name, model string) string {
	t.Helper()
	return createHandlerTestAgent(t, name, nil, testutil.Cols{"model": model})
}

func closureTestManagedIssue(t *testing.T, authorAgentID string) (issueID, goalID string) {
	t.Helper()
	issueID = dbfx.Issue(t, "DEV-7 closure target", testutil.Cols{
		"status":        "in_progress",
		"assignee_type": "agent",
		"assignee_id":   authorAgentID,
	})
	goalID = dbfx.Insert(t, "goal", testutil.Cols{
		"workspace_id": testWorkspaceID,
		"title":        "DEV-7 closure goal",
		"status":       "in_progress",
		"owner_type":   "agent",
		"owner_id":     authorAgentID,
		"creator_type": "member",
		"creator_id":   testUserID,
	})
	dbfx.InsertNoID(t, "goal_issue", testutil.Cols{
		"workspace_id": testWorkspaceID,
		"goal_id":      goalID,
		"issue_id":     issueID,
	}, "workspace_id = $1 AND issue_id = $2", testWorkspaceID, issueID)
	t.Cleanup(func() {
		for _, id := range []string{issueID, goalID} {
			testPool.Exec(context.Background(), `DELETE FROM closure_review WHERE subject_id = $1`, id)
			testPool.Exec(context.Background(), `DELETE FROM closure_authorization WHERE subject_id = $1`, id)
		}
	})
	return issueID, goalID
}

func closureAgentRequest(method, path string, body any, agentID, taskID string) *http.Request {
	req := newRequest(method, path, body)
	req.Header.Set("X-Actor-Source", "task_token")
	req.Header.Set("X-Agent-ID", agentID)
	req.Header.Set("X-Task-ID", taskID)
	return req
}

func closureGrantIssue(t *testing.T, issueID, action string) {
	t.Helper()
	req := newRequest("POST", "/api/issues/"+issueID, map[string]any{"actions": []string{action}})
	testutil.Call(t, testHandler.GrantIssueClosureAuthorization, withURLParam(req, "id", issueID)).Want(http.StatusCreated)
}

func closurePutIssueStatus(t *testing.T, issueID, agentID, taskID, status string) *testutil.Response {
	t.Helper()
	req := closureAgentRequest("PUT", "/api/issues/"+issueID, map[string]any{"status": status}, agentID, taskID)
	return testutil.Call(t, testHandler.UpdateIssue, withURLParam(req, "id", issueID))
}

func closurePostReview(t *testing.T, path, id, agentID, taskID, verdict string) map[string]any {
	t.Helper()
	req := closureAgentRequest("POST", path, map[string]any{
		"delivery_version":  closureTestDelivery,
		"delivery_tree":     closureTestTree,
		"criteria_revision": "dev-7-criteria-r1",
		"verdict":           verdict,
		"criteria": []map[string]any{
			{"criterion": "closure gate refuses an unearned close", "result": "pass", "evidence": "handler tests"},
		},
		"platforms": []string{"linux"},
	}, agentID, taskID)
	return testutil.Call(t, testHandler.CreateIssueClosureReview, withURLParam(req, "id", id)).Want(http.StatusCreated).Map()
}

func closureIssueStatus(t *testing.T, issueID string) string {
	t.Helper()
	var status string
	dbfx.QueryRow(t, `SELECT status FROM issue WHERE id = $1`, issueID).Scan(&status)
	return status
}

// closureLinkPullRequest attaches one delivery pull request to the issue so
// the required-deliverable source has a real row to read.
func closureLinkPullRequest(t *testing.T, issueID, state string) string {
	t.Helper()
	prID := dbfx.Insert(t, "github_pull_request", testutil.Cols{
		"workspace_id":    testWorkspaceID,
		"installation_id": int64(1),
		"repo_owner":      "XFreeMing",
		"repo_name":       "XShadow",
		"pr_number":       int(time.Now().UnixNano() % 1_000_000_000),
		"title":           "DEV-7 delivery",
		"state":           state,
		"html_url":        "https://github.com/XFreeMing/XShadow/pull/1",
		"pr_created_at":   testutil.Raw("now()"),
		"pr_updated_at":   testutil.Raw("now()"),
	})
	dbfx.InsertNoID(t, "issue_pull_request", testutil.Cols{
		"issue_id":        issueID,
		"pull_request_id": prID,
	}, "issue_id = $1 AND pull_request_id = $2", issueID, prID)
	return prID
}

func TestUnmergedPullRequestBlocksClosureAndMergedReleasesIt(t *testing.T) {
	author := closureTestAgent(t, "closure author", "deepseek-v4.1-flash")
	reviewer := closureTestAgent(t, "closure reviewer", "gpt-5-codex")
	issueID, _ := closureTestManagedIssue(t, author)
	authorTask := createHandlerTestTaskForAgentOnIssue(t, author, issueID)
	reviewerTask := createHandlerTestTaskForAgentOnIssue(t, reviewer, issueID)

	closureGrantIssue(t, issueID, "issue_close")
	closurePostReview(t, "/api/issues/"+issueID, issueID, reviewer, reviewerTask, "pass")
	prID := closureLinkPullRequest(t, issueID, "open")

	// The independent pass is technical acceptance only. The delivery the
	// issue declares as required is not there yet, so the close is refused and
	// the issue stays open instead of claiming a finished delivery.
	body := closurePutIssueStatus(t, issueID, author, authorTask, "done").WantOneOf(http.StatusConflict).Map()
	if body["outcome"] != string(issueclosure.Blocked) || body["reason"] != "missing_deliverable" {
		t.Fatalf("decision = %v/%v, body = %v", body["outcome"], body["reason"], body)
	}
	if got := closureIssueStatus(t, issueID); got != "in_progress" {
		t.Fatalf("status after missing deliverable = %q, want in_progress", got)
	}
	var accepted int
	dbfx.QueryRow(t, `SELECT count(*) FROM closure_review WHERE subject_id = $1 AND accepted_at IS NOT NULL`, issueID).Scan(&accepted)
	if accepted != 0 {
		t.Fatalf("accepted reviews = %d after a refused write", accepted)
	}

	// Once the required pull request is merged the same evidence closes it.
	dbfx.Exec(t, `UPDATE github_pull_request SET state = 'merged', merged_at = now() WHERE id = $1`, prID)
	closurePutIssueStatus(t, issueID, author, authorTask, "done").WantOneOf(http.StatusOK)
	if got := closureIssueStatus(t, issueID); got != "done" {
		t.Fatalf("status after the deliverable merged = %q, want done", got)
	}
}

func TestAgentDoneOnManagedIssueNeedsRecordedDelivery(t *testing.T) {
	author := closureTestAgent(t, "closure author", "deepseek-v4.1-flash")
	issueID, _ := closureTestManagedIssue(t, author)
	taskID := createHandlerTestTaskForAgentOnIssue(t, author, issueID)

	body := closurePutIssueStatus(t, issueID, author, taskID, "done").WantOneOf(http.StatusConflict).Map()
	if body["code"] != "closure_not_allowed" {
		t.Fatalf("refusal code = %v, body = %v", body["code"], body)
	}
	if got := closureIssueStatus(t, issueID); got != "in_progress" {
		t.Fatalf("status after refused write = %q, want in_progress", got)
	}
	var accepted int
	dbfx.QueryRow(t, `SELECT count(*) FROM closure_review WHERE subject_id = $1 AND accepted_at IS NOT NULL`, issueID).Scan(&accepted)
	if accepted != 0 {
		t.Fatalf("accepted reviews = %d after a refused write", accepted)
	}
}

func TestAgentDoneWithGrantAndIndependentPassCloses(t *testing.T) {
	author := closureTestAgent(t, "closure author", "deepseek-v4.1-flash")
	reviewer := closureTestAgent(t, "closure reviewer", "gpt-5-codex")
	issueID, _ := closureTestManagedIssue(t, author)
	authorTask := createHandlerTestTaskForAgentOnIssue(t, author, issueID)
	reviewerTask := createHandlerTestTaskForAgentOnIssue(t, reviewer, issueID)

	closureGrantIssue(t, issueID, "issue_close")

	// A pass from the implementing run itself is not independent acceptance.
	closurePostReview(t, "/api/issues/"+issueID, issueID, author, authorTask, "pass")
	body := closurePutIssueStatus(t, issueID, author, authorTask, "done").WantOneOf(http.StatusConflict).Map()
	if body["reason"] != "review_not_independent" {
		t.Fatalf("self-review refusal reason = %v, body = %v", body["reason"], body)
	}

	// A different model in a different run is the independent acceptance.
	closurePostReview(t, "/api/issues/"+issueID, issueID, reviewer, reviewerTask, "pass")
	closurePutIssueStatus(t, issueID, author, authorTask, "done").WantOneOf(http.StatusOK)
	if got := closureIssueStatus(t, issueID); got != "done" {
		t.Fatalf("status after accepted close = %q, want done", got)
	}

	var acceptedBy, acceptedAt string
	dbfx.QueryRow(t,
		`SELECT accepted_by_id::text, accepted_at::text FROM closure_review WHERE subject_id = $1 AND accepted_at IS NOT NULL`,
		issueID,
	).Scan(&acceptedBy, &acceptedAt)
	if acceptedBy != author {
		t.Fatalf("accepted_by = %s, want the closer %s", acceptedBy, author)
	}
}

func TestIndependentPassWithoutGrantWaitsForHuman(t *testing.T) {
	author := closureTestAgent(t, "closure author", "deepseek-v4.1-flash")
	reviewer := closureTestAgent(t, "closure reviewer", "gpt-5-codex")
	issueID, _ := closureTestManagedIssue(t, author)
	authorTask := createHandlerTestTaskForAgentOnIssue(t, author, issueID)
	reviewerTask := createHandlerTestTaskForAgentOnIssue(t, reviewer, issueID)

	closurePostReview(t, "/api/issues/"+issueID, issueID, reviewer, reviewerTask, "pass")
	body := closurePutIssueStatus(t, issueID, author, authorTask, "done").WantOneOf(http.StatusConflict).Map()
	if body["outcome"] != string(issueclosure.WaitHuman) || body["reason"] != "no_authorization" {
		t.Fatalf("decision = %v/%v, body = %v", body["outcome"], body["reason"], body)
	}
	if got := closureIssueStatus(t, issueID); got != "in_progress" {
		t.Fatalf("status = %q, want in_progress", got)
	}
}

func TestClosureViewReportsTheSameDecision(t *testing.T) {
	author := closureTestAgent(t, "closure author", "deepseek-v4.1-flash")
	issueID, _ := closureTestManagedIssue(t, author)
	createHandlerTestTaskForAgentOnIssue(t, author, issueID)

	req := newRequest("GET", "/api/issues/"+issueID+"/closure", nil)
	got := testutil.Call(t, testHandler.GetIssueClosure, withURLParam(req, "id", issueID)).Want(http.StatusOK).Map()
	if got["subject_type"] != "issue" || got["managed"] != true {
		t.Fatalf("closure view = %v", got)
	}
	if got["outcome"] != string(issueclosure.Blocked) {
		t.Fatalf("outcome = %v, want the same refusal the gate applies", got["outcome"])
	}
}

func TestMemberDoneIsStillTheHumanEndpoint(t *testing.T) {
	author := closureTestAgent(t, "closure author", "deepseek-v4.1-flash")
	issueID, _ := closureTestManagedIssue(t, author)

	req := newRequest("PUT", "/api/issues/"+issueID, map[string]any{"status": "done"})
	testutil.Call(t, testHandler.UpdateIssue, withURLParam(req, "id", issueID)).Want(http.StatusOK)
	if got := closureIssueStatus(t, issueID); got != "done" {
		t.Fatalf("member close left status = %q", got)
	}
}

func TestGoalCompletedIsGatedOnceEnrolled(t *testing.T) {
	author := closureTestAgent(t, "closure author", "deepseek-v4.1-flash")
	reviewer := closureTestAgent(t, "closure reviewer", "gpt-5-codex")
	issueID, goalID := closureTestManagedIssue(t, author)
	createHandlerTestTaskForAgentOnIssue(t, author, issueID)
	reviewerTask := createHandlerTestTaskForAgentOnIssue(t, reviewer, "")

	grant := newRequest("POST", "/api/goals/"+goalID+"/closure/authorizations", map[string]any{"actions": []string{"goal_accept"}})
	testutil.Call(t, testHandler.GrantGoalClosureAuthorization, withURLParam(grant, "id", goalID)).Want(http.StatusCreated)

	put := func() *testutil.Response {
		req := closureAgentRequest("PUT", "/api/goals/"+goalID, map[string]any{"status": "completed"}, author, "")
		return testutil.Call(t, testHandler.UpdateGoal, withURLParam(req, "id", goalID))
	}
	body := put().WantOneOf(http.StatusConflict).Map()
	if body["code"] != "closure_not_allowed" {
		t.Fatalf("goal refusal = %v", body)
	}

	reviewReq := closureAgentRequest("POST", "/api/goals/"+goalID+"/closure/reviews", map[string]any{
		"delivery_version": closureTestDelivery,
		"delivery_tree":    closureTestTree,
		"verdict":          "pass",
	}, reviewer, reviewerTask)
	testutil.Call(t, testHandler.CreateGoalClosureReview, withURLParam(reviewReq, "id", goalID)).Want(http.StatusCreated)

	put().WantOneOf(http.StatusOK)
	var status string
	dbfx.QueryRow(t, `SELECT status FROM goal WHERE id = $1`, goalID).Scan(&status)
	if status != "completed" {
		t.Fatalf("goal status = %q, want completed", status)
	}
}

// The review write path stamps acceptance with the delivery and scope that are
// current on the server. A caller holding an older view cannot record a review
// that is stale the moment it lands: it is told to re-read and review the
// current delivery instead.
func TestClosureReviewRefusesStaleDelivery(t *testing.T) {
	author := closureTestAgent(t, "closure author", "deepseek-v4.1-flash")
	reviewer := closureTestAgent(t, "closure reviewer", "gpt-5-codex")
	issueID, _ := closureTestManagedIssue(t, author)
	reviewerTask := createHandlerTestTaskForAgentOnIssue(t, reviewer, issueID)

	prID := closureLinkPullRequest(t, issueID, "open")
	dbfx.Exec(t, `UPDATE github_pull_request SET head_sha = 'aaaa1111' WHERE id = $1`, prID)

	post := func(version, criteria string) *testutil.Response {
		body := map[string]any{
			"delivery_version": version,
			"verdict":          "pass",
			"criteria":         []map[string]any{{"criterion": "delivered", "result": "pass", "evidence": "tests"}},
			"platforms":        []string{"linux"},
		}
		if criteria != "" {
			body["criteria_revision"] = criteria
		}
		req := closureAgentRequest("POST", "/api/issues/"+issueID, body, reviewer, reviewerTask)
		return testutil.Call(t, testHandler.CreateIssueClosureReview, withURLParam(req, "id", issueID))
	}

	if got := post("bbbb2222", "").Want(http.StatusConflict).Map(); got["code"] != "closure_delivery_stale" {
		t.Fatalf("stale delivery code = %v, want closure_delivery_stale", got)
	}
	if got := post("aaaa1111", "").Want(http.StatusCreated).Map(); got["delivery_version"] != "aaaa1111" {
		t.Fatalf("review delivery = %v, want aaaa1111", got)
	}

	// The registered head moves. The old acceptance is invalid immediately and
	// a review for it can no longer be recorded — no new review is needed to
	// make the old one stale.
	dbfx.Exec(t, `UPDATE github_pull_request SET head_sha = 'cccc3333' WHERE id = $1`, prID)
	if got := post("aaaa1111", "").Want(http.StatusConflict).Map(); got["code"] != "closure_delivery_stale" {
		t.Fatalf("code after head advance = %v, want closure_delivery_stale", got)
	}
	if got := post("cccc3333", "").Want(http.StatusCreated).Map(); got["delivery_version"] != "cccc3333" {
		t.Fatalf("review against the new head delivery = %v", got)
	}
}

// Saving the plan bumps its revision. Acceptance scope follows that revision,
// so reviews against the old scope are refused and the closure view reports
// the revision facts were recorded for.
func TestClosureReviewRefusesStalePlanRevision(t *testing.T) {
	author := closureTestAgent(t, "closure author", "deepseek-v4.1-flash")
	reviewer := closureTestAgent(t, "closure reviewer", "gpt-5-codex")
	issueID, goalID := closureTestManagedIssue(t, author)
	reviewerTask := createHandlerTestTaskForAgentOnIssue(t, reviewer, issueID)

	projectID := createProjectForPlan(t, "Closure criteria project "+t.Name())
	dbfx.Exec(t, `UPDATE goal SET project_id = $2 WHERE id = $1`, goalID, projectID)
	dbfx.Insert(t, "goal_plan", testutil.Cols{
		"workspace_id": testWorkspaceID,
		"goal_id":      goalID,
		"project_id":   projectID,
		"revision":     1,
		"content":      testutil.Raw(`'{"items": [], "edges": []}'::jsonb`),
	})
	revision1 := "plan:" + goalID + ":r1"

	post := func(criteria string) *testutil.Response {
		body := map[string]any{
			"delivery_version": closureTestDelivery,
			"verdict":          "pass",
			"criteria":         []map[string]any{{"criterion": "delivered", "result": "pass", "evidence": "tests"}},
			"platforms":        []string{"linux"},
		}
		if criteria != "" {
			body["criteria_revision"] = criteria
		}
		req := closureAgentRequest("POST", "/api/issues/"+issueID, body, reviewer, reviewerTask)
		return testutil.Call(t, testHandler.CreateIssueClosureReview, withURLParam(req, "id", issueID))
	}

	if got := post("dev-7-criteria-r1").Want(http.StatusConflict).Map(); got["code"] != "closure_criteria_stale" {
		t.Fatalf("stale criteria code = %v, want closure_criteria_stale", got)
	}
	// An omitted revision is stamped with the authoritative scope.
	if got := post("").Want(http.StatusCreated).Map(); got["criteria_revision"] != revision1 {
		t.Fatalf("review criteria_revision = %v, want %s", got["criteria_revision"], revision1)
	}
	view := testutil.Call(t, testHandler.GetIssueClosure,
		withURLParam(newRequest("GET", "/api/issues/"+issueID+"/closure", nil), "id", issueID)).
		Want(http.StatusOK).Map()
	if view["criteria_revision"] != revision1 {
		t.Fatalf("closure view criteria_revision = %v, want %s", view["criteria_revision"], revision1)
	}

	dbfx.Exec(t, `UPDATE goal_plan SET revision = 2 WHERE goal_id = $1`, goalID)
	revision2 := "plan:" + goalID + ":r2"
	view = testutil.Call(t, testHandler.GetIssueClosure,
		withURLParam(newRequest("GET", "/api/issues/"+issueID+"/closure", nil), "id", issueID)).
		Want(http.StatusOK).Map()
	if view["criteria_revision"] != revision2 {
		t.Fatalf("closure view after plan save = %v, want %s", view["criteria_revision"], revision2)
	}
	if got := post(revision1).Want(http.StatusConflict).Map(); got["code"] != "closure_criteria_stale" {
		t.Fatalf("code after plan revision bump = %v, want closure_criteria_stale", got)
	}
}
