package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/testutil"
)

// TestGoalWorkspaceDeletionRemovesGoalsAndLinks drives the real workspace
// cascade against two fixtures: the deleted workspace's goal and link must be
// gone while another workspace's goal and link stay untouched. Assertions are
// scoped to the fixture IDs because DB-backed packages share this database.
func TestGoalWorkspaceDeletionRemovesGoalsAndLinks(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	marker := fmt.Sprintf("goal-ws-del-%d", time.Now().UnixNano())

	seed := func(suffix string) (workspaceID, goalID string) {
		workspaceID = dbfx.Workspace(t, "Goal WS Delete "+suffix, marker+"-"+suffix)
		dbfx.Member(t, workspaceID, testUserID, "owner")
		fx := testutil.New(testPool, workspaceID, testUserID)
		projectID := fx.Project(t, "Goal WS Delete Project "+suffix)
		issueID := fx.Issue(t, "Goal WS Delete Issue "+suffix, testutil.Cols{"project_id": projectID})
		goalID = fx.Insert(t, "goal", testutil.Cols{
			"workspace_id": workspaceID,
			"project_id":   projectID,
			"title":        "Goal WS Delete Goal " + suffix,
			"creator_type": "member",
			"creator_id":   testUserID,
		})
		fx.InsertNoID(t, "goal_issue", testutil.Cols{
			"workspace_id": workspaceID,
			"goal_id":      goalID,
			"issue_id":     issueID,
		}, "workspace_id = $1 AND goal_id = $2", workspaceID, goalID)
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

	if goals, links := goalRowsByWorkspace(t, deletedWorkspace, deletedGoal); goals != 0 || links != 0 {
		t.Fatalf("deleted workspace goal rows goals=%d links=%d, want 0/0", goals, links)
	}
	if goals, links := goalRowsByWorkspace(t, keptWorkspace, keptGoal); goals != 1 || links != 1 {
		t.Fatalf("other workspace goal rows goals=%d links=%d, want 1/1", goals, links)
	}
}

func goalRowsByWorkspace(t *testing.T, workspaceID, goalID string) (goals, links int) {
	t.Helper()
	dbfx.QueryRow(t, `
		SELECT
			(SELECT count(*) FROM goal WHERE id = $1 AND workspace_id = $2),
			(SELECT count(*) FROM goal_issue WHERE goal_id = $1 AND workspace_id = $2)
	`, goalID, workspaceID).Scan(&goals, &links)
	return goals, links
}
