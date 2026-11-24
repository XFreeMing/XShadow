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

	// A reordered draft can resume the first story's planning, but formal
	// delivery continues to belong to the story already in progress.
	f.Exec(t, "UPDATE issue SET status='in_progress',updated_at=now() WHERE id=$1", next)
	f.Exec(t, "UPDATE issue SET status='backlog',updated_at=now()-interval '2 days' WHERE id=$1", first)
	tick()
	count(first, 3)
	count(next, 1)
	if got := f.Count(t, "SELECT count(*) FROM agent_task_queue WHERE issue_id=$1 AND trigger_summary=$2", first, backlogPlanningTrigger); got != 1 {
		t.Fatalf("expected one planning-only run on the reordered card, got %d", got)
	}
}

func TestIssueContinuationPlansBeforeDeliveryIsReady(t *testing.T) {
	f, wakes, currentID, manager := wakeFixture(t)
	ctx := context.Background()
	current := util.UUIDToString(currentID)
	project := f.Project(t, "Second brain")
	goal := f.Insert(t, "goal", testutil.Cols{
		"workspace_id": f.WorkspaceID, "project_id": project, "title": "Knowledge delivery",
		"intent": "Use reviewed records", "status": "in_progress",
		"creator_type": "member", "creator_id": f.UserID,
	})
	later := f.Issue(t, "Agent access", testutil.Cols{"project_id": project})
	design := f.Issue(t, "Evidence design", testutil.Cols{"project_id": project})
	reference := f.Issue(t, "Reference only", testutil.Cols{"project_id": project})
	f.Exec(t, "UPDATE issue SET project_id=$2,status='in_review',updated_at=now() WHERE id=$1", current, project)
	f.Exec(t, "UPDATE issue SET status='backlog',updated_at=now()-interval '2 days' WHERE id IN ($1,$2,$3)", later, design, reference)
	for _, id := range []string{current, later, design, reference} {
		f.Exec(t, "INSERT INTO goal_issue (workspace_id,goal_id,issue_id) VALUES ($1,$2,$3)", f.WorkspaceID, goal, id)
	}
	f.Exec(t, `INSERT INTO goal_plan(workspace_id,goal_id,project_id,revision,content)
VALUES ($1,$2,$3,1,jsonb_build_object('items',jsonb_build_array(
 jsonb_build_object('id',$4::text,'stage',1,'binding',jsonb_build_object('issue_id',$4::text)),
 jsonb_build_object('id',$5::text,'stage',2,'binding',jsonb_build_object('issue_id',$5::text))),
 'edges',jsonb_build_array(jsonb_build_object('dependent_item_id',$5::text,'prerequisite_item_id',$4::text))))`, f.WorkspaceID, goal, project, current, later)
	f.Exec(t, `UPDATE issue SET metadata=jsonb_build_object('backlog_plan',jsonb_build_object(
 'state','needs_story','next','Design the acceptance criteria','dependencies',jsonb_build_array(
 jsonb_build_object('issue',w.issue_prefix||'-'||p.number,'status',p.status))))
 FROM issue p JOIN workspace w ON w.id=p.workspace_id
 WHERE p.id=$1 AND issue.id IN ($2,$3)`, current, later, design)

	svc := IssueContinuation{Tasks: wakes.Tasks, ManagerID: parseTestUUID(t, manager)}
	tick := func(laterCount, designCount int) {
		t.Helper()
		if err := svc.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		for _, want := range []struct {
			id    string
			count int
		}{{later, laterCount}, {design, designCount}, {reference, 0}} {
			if got := f.Count(t, "SELECT count(*) FROM agent_task_queue WHERE issue_id=$1", want.id); got != want.count {
				t.Fatalf("issue %s has %d runs, want %d", want.id, got, want.count)
			}
		}
	}
	tick(1, 1)
	var id string
	if err := f.Pool.QueryRow(ctx, "SELECT id::text FROM agent_task_queue WHERE issue_id=$1", later).Scan(&id); err != nil {
		t.Fatal(err)
	}
	task, err := db.New(f.Pool).GetAgentTask(ctx, parseTestUUID(t, id))
	if err != nil {
		t.Fatal(err)
	}
	if task.TriggerSummary.String != "Issue continuation (planning)" || ReadGoalExecutionTask(task).ID.Valid {
		t.Fatalf("planning run accidentally granted delivery: %s", task.TriggerSummary.String)
	}
	if err := wakes.CheckClaim(ctx, task); err != nil {
		t.Fatalf("claim planning run before delivery is ready: %v", err)
	}
	f.Exec(t, "UPDATE goal SET status='paused' WHERE id=$1", goal)
	if err := wakes.CheckClaim(ctx, task); !errors.Is(err, ErrWakeupForbidden) {
		t.Fatalf("planning claim for paused goal = %v", err)
	}
	f.Exec(t, "UPDATE goal SET status='in_progress' WHERE id=$1", goal)
	f.Exec(t, "UPDATE agent_task_queue SET status='completed',completed_at=now()-interval '2 days' WHERE issue_id IN ($1,$2)", later, design)
	f.Comment(t, design, "Planning discussion receipt", testutil.Cols{"author_type": "agent", "author_id": manager})
	tick(1, 1)
	f.Comment(t, design, "Discuss the missing acceptance example")
	tick(1, 2)
	f.Exec(t, "UPDATE agent_task_queue SET status='completed',completed_at=now()-interval '2 days' WHERE issue_id=$1", design)
	f.Exec(t, "UPDATE issue SET status='blocked',updated_at=now() WHERE id=$1", current)
	tick(2, 3)
	if got := f.Count(t, "SELECT count(*) FROM issue WHERE id IN ($1,$2) AND status='backlog'", later, design); got != 2 {
		t.Fatalf("planning changed delivery status on %d cards", 2-got)
	}
}
