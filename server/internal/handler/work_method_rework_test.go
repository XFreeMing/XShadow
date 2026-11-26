package handler

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/pkg/workmethod"
)

func TestRepeatedReworkCreatesOneAnalysisChild(t *testing.T) {
	seedTestCatalog(t)
	ctx := context.Background()
	runtime := dbfx.Runtime(t, "Rework analysis runtime")
	chief := dbfx.Agent(t, "Rework analysis chief", runtime)
	squad := dbfx.Squad(t, "Rework analysis", chief)
	prior := testHandler.ReworkAnalysisSquadID
	testHandler.ReworkAnalysisSquadID = util.MustParseUUID(squad)
	t.Cleanup(func() { testHandler.ReworkAnalysisSquadID = prior })
	project := deliveryProject(t, "Rework analysis project", chief)
	goal := createGoalViaAPI(t, testUserID, map[string]any{"title": "Reliable delivery", "project_id": project, "status": "in_progress"})
	var original IssueResponse
	testutil.Call(t, testHandler.CreateIssue, newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID,
		map[string]any{"title": "Review a fixed reference", "project_id": project, "status": "in_review"})).Want(201).JSON(&original)
	id := original.ID
	testutil.Call(t, testHandler.LinkGoalIssue, goalRequest("PUT", "/api/goals/"+goal.ID+"/issues/"+id, testUserID, nil,
		map[string]string{"id": goal.ID, "issueId": id})).Want(204)
	actor := &workmethod.Actor{Kind: "member", ID: testUserID}
	run := workmethod.Run{Revision: 2, State: "review", CurrentActor: actor, Method: &workmethod.Template{
		ID: "rework-test", Kind: "development", Name: "Development", Steps: []workmethod.Step{{Name: "Review coverage", Owner: actor, Reviewer: actor}},
	}, History: []workmethod.Event{{Action: "start"}, {Action: "advance", Actor: "member/" + testUserID}}}
	data, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	dbfx.Exec(t, "UPDATE issue SET work_method_run=$2 WHERE id=$1", id, data)
	change := func(action, evidence string, revision int) {
		t.Helper()
		req := goalRequest("POST", "/api/issues/"+id+"/work-method", testUserID,
			map[string]any{"action": action, "evidence": evidence, "base_revision": revision}, map[string]string{"id": id})
		testutil.Call(t, testHandler.ChangeIssueWorkMethod, req).Want(200)
	}
	count := func(want int) {
		t.Helper()
		var got int
		dbfx.QueryRow(t, "SELECT count(*) FROM issue WHERE parent_issue_id=$1", id).Scan(&got)
		if got != want {
			t.Fatalf("analysis children = %d, want %d", got, want)
		}
	}
	change("rework", "Wrong edition was returned", 2)
	count(0)
	change("advance", "First retry", 3)
	change("rework", "Still returning the wrong edition", 4)
	count(1)
	var child, description, assignee, linkedGoal string
	if err := testPool.QueryRow(ctx, `SELECT i.id,i.description,i.assignee_id,g.goal_id FROM issue i
		JOIN goal_issue g ON g.issue_id=i.id WHERE i.parent_issue_id=$1`, id).Scan(&child, &description, &assignee, &linkedGoal); err != nil {
		t.Fatal(err)
	}
	if assignee != squad || linkedGoal != goal.ID || !strings.Contains(description, "Wrong edition was returned") ||
		!strings.Contains(description, "Still returning the wrong edition") || !strings.Contains(description, "先验证设计假设") {
		t.Fatalf("unexpected analysis %s, squad %s, goal %s: %s", child, assignee, linkedGoal, description)
	}
	var dispatchedLeader, dispatchedSquad string
	if err := testPool.QueryRow(ctx, "SELECT agent_id,squad_id FROM agent_task_queue WHERE issue_id=$1", child).Scan(&dispatchedLeader, &dispatchedSquad); err != nil {
		t.Fatal(err)
	}
	if dispatchedLeader != chief || dispatchedSquad != squad {
		t.Fatalf("analysis dispatched to %s/%s, want %s/%s", dispatchedLeader, dispatchedSquad, chief, squad)
	}
	change("advance", "Second retry", 5)
	change("rework", "Same review returned again", 6)
	count(1)
}
