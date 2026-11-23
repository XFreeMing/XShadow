package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/workmethod"
)

func TestResearchClaimAndProjectHandoffUseIssueMethod(t *testing.T) {
	seedTestCatalog(t)
	agentID := seededReadyAgentID(t)
	project := deliveryProject(t, "Mixed deliveries", agentID)
	id := dbfx.Issue(t, "调研客户留存原因", testutil.Cols{"project_id": project, "status": "todo"})

	// A project with an engineering handler contains a research issue with a
	// recommendation but no pinned method yet. Its first move must not dispatch
	// the engineering run before the researcher can submit the first step.
	updateIssueForTest(t, id, map[string]any{"status": "in_progress"}).Want(http.StatusOK)
	status, _, assignee := issueStatusAndAssignee(t, id)
	if status != "in_progress" || assignee != "" {
		t.Fatalf("research entered %s assigned to %s", status, assignee)
	}
	updateIssueForTest(t, id, map[string]any{"assignee_type": "agent", "assignee_id": agentID}).Want(http.StatusOK)
	task := db.AgentTaskQueue{AgentID: util.MustParseUUID(agentID)}
	claim := claimBriefForTest(t, id, task)
	issue, err := testHandler.Queries.GetIssue(context.Background(), util.MustParseUUID(id))
	if err != nil {
		t.Fatal(err)
	}
	ref := testHandler.getIssuePrefix(context.Background(), issue.WorkspaceID) + "-" + strconv.Itoa(int(issue.Number))
	for _, text := range []string{"seven-step", "界定决策问题", `"$MULTICA_CLI_PATH" issue method ` + ref + " --change-file", "Required output"} {
		if !strings.Contains(claim, text) {
			t.Fatalf("missing %q from claim: %s", text, claim)
		}
	}
	if strings.Contains(claim, "Implement it and open a PR.") {
		t.Fatal(claim)
	}
	guest := claimBriefForTest(t, id, db.AgentTaskQueue{})
	if strings.Contains(guest, "--change-file") || !strings.Contains(guest, "current assignee") {
		t.Fatal(guest)
	}
}

func TestMethodReviewGatesManualBatchAndPullRequestCompletion(t *testing.T) {
	ctx := context.Background()
	seedTestCatalog(t)
	methods := workmethod.Builtins()
	run := workmethod.Recommend(methods, "调研客户流失原因", "", "")
	revision := run.Revision
	if err := run.Apply(workmethod.Change{Action: "advance", BaseRevision: &revision, Evidence: "Research question and scope"}, methods, "reviewer", "2026-11-19T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	id := dbfx.Issue(t, "Pinned research method", testutil.Cols{"status": "in_progress", "work_method_run": data})
	unstarted := dbfx.Issue(t, "Plain task", testutil.Cols{"status": "in_progress"})
	result := updateIssueForTest(t, id, map[string]any{"status": "done"}).Want(http.StatusConflict).Map()
	if result["code"] != "work_method_review_open" {
		t.Fatalf("missing review conflict: %+v", result)
	}
	batchSetStatus(t, []string{id, unstarted}, "done")
	if s, _, _ := issueStatusAndAssignee(t, id); s != "in_progress" {
		t.Fatalf("batch completed research issue: %s", s)
	}
	if s, _, _ := issueStatusAndAssignee(t, unstarted); s != "done" {
		t.Fatalf("batch skipped plain issue: %s", s)
	}
	linkMergedGitHubPRForTest(t, id, "work-method-"+workflowTestSuffix())
	ws := util.MustParseUUID(testWorkspaceID)
	issueID := util.MustParseUUID(id)
	testHandler.maybeAutoCompleteIssue(ctx, ws, issueID, nil)
	if s, _, _ := issueStatusAndAssignee(t, id); s != "in_progress" {
		t.Fatalf("merged PR completed unreviewed method: %s", s)
	}

	// A complete review releases the gate. The state transition itself is
	// exercised by the workmethod package.
	run.State = "closed"
	data, err = json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	dbfx.Exec(t, "UPDATE issue SET work_method_run=$1 WHERE id=$2", data, id)
	testHandler.maybeAutoCompleteIssue(ctx, ws, issueID, nil)
	if s, _, _ := issueStatusAndAssignee(t, id); s != "done" {
		t.Fatalf("merged PR failed to complete reviewed method: %s", s)
	}
}

func TestMemberAnswerCardCanCompleteWithUnstartedRecommendation(t *testing.T) {
	seedTestCatalog(t)
	run := workmethod.Recommend(workmethod.Builtins(), "研发管理确认", "", "")
	if run.State != "ready" || run.Method == nil {
		t.Fatalf("expected unstarted method recommendation: %+v", run)
	}
	raw, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	card := dbfx.Issue(t, "Member decision", testutil.Cols{
		"status": "todo", "assignee_type": "member", "assignee_id": testUserID,
		"work_method_run": raw,
		"metadata":        []byte(`{"member_todo_key":"implementation-choice","member_todo_source":"DEV-102"}`),
	})
	story := dbfx.Issue(t, "Product story", testutil.Cols{
		"status": "todo", "assignee_type": "member", "assignee_id": testUserID, "work_method_run": raw,
	})
	updateIssueForTest(t, card, map[string]any{"status": "done"}).Want(http.StatusOK)
	if status, _, _ := issueStatusAndAssignee(t, card); status != "done" {
		t.Fatalf("answer card status = %s, want done", status)
	}
	completed, err := testHandler.Queries.GetIssue(context.Background(), util.MustParseUUID(card))
	if err != nil || len(completed.WorkMethodRun) > 0 {
		t.Fatalf("answer card retained its pending recommendation: %v", err)
	}
	result := updateIssueForTest(t, story, map[string]any{"status": "done"}).Want(http.StatusConflict).Map()
	if result["code"] != "work_method_review_open" {
		t.Fatalf("expected the regular story's review gate: %+v", result)
	}

	run.State, run.Revision = "review", 2
	raw, err = json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	dbfx.Exec(t, "UPDATE issue SET status='in_review', work_method_run=$1 WHERE id=$2", raw, card)
	result = updateIssueForTest(t, card, map[string]any{"status": "done"}).Want(http.StatusConflict).Map()
	if result["code"] != "work_method_review_open" {
		t.Fatalf("an explicitly started review must remain protected: %+v", result)
	}
}
