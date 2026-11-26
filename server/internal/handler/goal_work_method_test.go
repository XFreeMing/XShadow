package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/workmethod"
)

func TestMethodAdmissionOnCreateAndGoalChanges(t *testing.T) {
	research := registerResearchMethodForTest(t)
	stored := func(id, want string) {
		t.Helper()
		var selected string
		if err := testPool.QueryRow(context.Background(), `SELECT COALESCE(work_method_run->'method'->>'id','') FROM issue WHERE id=$1`, id).Scan(&selected); err != nil || selected != want {
			t.Fatalf("%s: choice %s, want %s (%v)", id, selected, want, err)
		}
	}
	issue := createIssueForTest(t, map[string]any{"title": "调研客户满意度"})
	if issue.Revision != 1 {
		t.Fatalf("fresh issue revision = %d", issue.Revision)
	}
	stored(issue.ID, research.ID)
	updateIssueForTest(t, issue.ID, map[string]any{"status": "done"}).Want(http.StatusConflict)
	updateIssueForTest(t, issue.ID, map[string]any{"title": "研发回访面板"}).Want(http.StatusOK)
	stored(issue.ID, "agile")
	updateIssueForTest(t, issue.ID, map[string]any{"title": "本月工作包"}).Want(http.StatusOK)
	stored(issue.ID, "")
	goal := createGoalViaAPI(t, testUserID, map[string]any{"title": "提升留存", "issue_ids": []string{issue.ID}})
	stored(issue.ID, "okr")
	updateIssueForTest(t, issue.ID, map[string]any{"status": "done"}).Want(http.StatusConflict)
	if unlinkGoalIssueViaAPI(t, testUserID, goal.ID, issue.ID) != 204 {
		t.Fatal("unlink")
	}
	stored(issue.ID, "")
	if linkGoalIssueViaAPI(t, testUserID, goal.ID, issue.ID) != 204 {
		t.Fatal("link")
	}
	stored(issue.ID, "okr")
	patch := func(title string) {
		testutil.Call(t, testHandler.UpdateGoal, goalRequest("PATCH", "/api/goals/"+goal.ID, testUserID, map[string]any{"title": title}, map[string]string{"id": goal.ID})).Want(http.StatusOK)
	}
	patch("调研用户需求")
	stored(issue.ID, research.ID)
	base := 0
	var run workmethod.Run
	testutil.Call(t, testHandler.ChangeIssueWorkMethod, withURLParam(newRequest("POST", "/api/issues/"+issue.ID+"/work-method", map[string]any{"action": "advance", "base_revision": base, "evidence": "确定调查范围和受访人"}), "id", issue.ID)).Want(http.StatusOK).JSON(&run)
	patch("提升转化")
	stored(issue.ID, research.ID)
	if unlinkGoalIssueViaAPI(t, testUserID, goal.ID, issue.ID) != 204 {
		t.Fatal("unlink started")
	}
	stored(issue.ID, research.ID)
	done := createIssueForTest(t, map[string]any{"title": "调研已结项事项", "status": "done"})
	stored(done.ID, "")
	cancelled := createIssueForTest(t, map[string]any{"title": "调研已取消事项", "status": "cancelled"})
	stored(cancelled.ID, "")
	ambiguous := createIssueForTest(t, map[string]any{"title": "研发和调研两个方向"})
	stored(ambiguous.ID, "")
}

func TestDeletingGoalOrMovingProjectReleasesUnstartedGoalMethod(t *testing.T) {
	p := createWorkflowTestProject(t, "initial method project")
	newProject := createWorkflowTestProject(t, "new method project")
	for _, operation := range []string{"delete", "move"} {
		issue := createIssueForTest(t, map[string]any{"title": "work package " + operation, "project_id": p})
		goal := createGoalViaAPI(t, testUserID, map[string]any{"title": "提升留存 " + operation, "project_id": p, "issue_ids": []string{issue.ID}})
		if operation == "delete" {
			testutil.Call(t, testHandler.DeleteGoal, goalRequest("DELETE", "/api/goals/"+goal.ID, testUserID, nil, map[string]string{"id": goal.ID})).Want(http.StatusNoContent)
		} else {
			updateIssueForTest(t, issue.ID, map[string]any{"project_id": newProject}).Want(http.StatusOK)
		}
		updateIssueForTest(t, issue.ID, map[string]any{"status": "done"}).Want(http.StatusOK)
	}
}
