package service

import (
	"context"
	"errors"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestIssueContinuationUsesGoalPlanOrderAndCurrentVersion(t *testing.T) {
	f, wakes, firstID, manager := wakeFixture(t)
	first := util.UUIDToString(firstID)
	project := f.Project(t, "Second brain")
	goal := f.Insert(t, "goal", testutil.Cols{
		"workspace_id": f.WorkspaceID, "project_id": project, "title": "Knowledge delivery",
		"intent": "Read records before using them", "status": "in_progress",
		"creator_type": "member", "creator_id": f.UserID,
	})
	next := f.Issue(t, "Agent access", testutil.Cols{"project_id": project})
	evidence := f.Issue(t, "Source material permission", testutil.Cols{"project_id": project})
	wait := `{"backlog_plan":{"state":"waiting_scope","next":"Follow the old plan","dependencies":[]}}`
	f.Exec(t, "UPDATE issue SET project_id=$2,status='backlog',metadata=$3,updated_at=now()-interval '2 days' WHERE id=$1", first, project, wait)
	f.Exec(t, "UPDATE issue SET status='backlog',updated_at=now()-interval '2 days' WHERE id IN ($1,$2)", next, evidence)
	for _, id := range []string{first, next, evidence} {
		f.Exec(t, "INSERT INTO goal_issue (workspace_id,goal_id,issue_id) VALUES ($1,$2,$3)", f.WorkspaceID, goal, id)
	}
	// Put the later card first in the stored list. The explicit stages must
	// control selection. The evidence card is linked but has no delivery slot.
	f.Exec(t, `INSERT INTO goal_plan(workspace_id,goal_id,project_id,revision,content)
VALUES ($1,$2,$3,1,jsonb_build_object('items',jsonb_build_array(
 jsonb_build_object('id',$4::text,'stage',2,'binding',jsonb_build_object('issue_id',$4::text)),
 jsonb_build_object('id',$5::text,'stage',1,'binding',jsonb_build_object('issue_id',$5::text))),
 'edges',jsonb_build_array(jsonb_build_object('dependent_item_id',$4::text,'prerequisite_item_id',$5::text))))`, f.WorkspaceID, goal, project, next, first)

	svc := IssueContinuation{Tasks: wakes.Tasks, ManagerID: parseTestUUID(t, manager)}
	tick := func() {
		t.Helper()
		if err := svc.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	count := func(id string, want int) {
		t.Helper()
		if got := f.Count(t, "SELECT count(*) FROM agent_task_queue WHERE issue_id=$1", id); got != want {
			t.Fatalf("issue %s has %d runs, want %d", id, got, want)
		}
	}
	finishRun := func(id string) {
		t.Helper()
		f.Exec(t, "UPDATE agent_task_queue SET status='completed',completed_at=now()-interval '2 days' WHERE issue_id=$1", id)
	}

	tick()
	count(first, 1)
	count(next, 0)
	count(evidence, 0)
	finishRun(first)

	// Receipt-only plan saves do not wake the same coordinator again.
	f.Exec(t, "UPDATE goal_plan SET revision=2, context_snapshot='{\"read_at\":\"later\"}' WHERE goal_id=$1", goal)
	tick()
	count(first, 1)
	count(next, 0)

	// An outcome change requires the coordinator to revisit the same card.
	f.Exec(t, "UPDATE goal_plan SET revision=3, content=jsonb_set(content,'{items,1,outcome}',to_jsonb('Record corrected'::text)) WHERE goal_id=$1", goal)
	tick()
	count(first, 2)
	count(next, 0)
	finishRun(first)

	f.Exec(t, "UPDATE issue SET status='done' WHERE id=$1", first)
	tick()
	count(next, 1)
	var taskID string
	if err := f.Pool.QueryRow(context.Background(), "SELECT id::text FROM agent_task_queue WHERE issue_id=$1", next).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	task, err := db.New(f.Pool).GetAgentTask(context.Background(), parseTestUUID(t, taskID))
	if err != nil {
		t.Fatal(err)
	}
	if err := wakes.checkContinuationClaim(context.Background(), task); err != nil {
		t.Fatalf("claim current goal task: %v", err)
	}
	// A rollback of the prerequisite between queueing and claim invalidates
	// this run. It may not start with a superseded prerequisite.
	f.Exec(t, "UPDATE issue SET status='in_review' WHERE id=$1", first)
	if err := wakes.checkContinuationClaim(context.Background(), task); !errors.Is(err, ErrWakeupForbidden) {
		t.Fatalf("claim with reopened prerequisite = %v", err)
	}
	f.Exec(t, "UPDATE issue SET status='done' WHERE id=$1", first)
	finishRun(next)
	tick()
	count(next, 1)
	count(evidence, 0)

	// A reordered draft cannot start coordination on a second story while the
	// previously selected issue is in delivery.
	f.Exec(t, "UPDATE issue SET status='in_progress',updated_at=now() WHERE id=$1", next)
	f.Exec(t, "UPDATE issue SET status='backlog',updated_at=now()-interval '2 days' WHERE id=$1", first)
	tick()
	count(first, 2)
	count(next, 1)
}
