package service

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5/pgtype"
	"sync"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestIssueContinuationReviewEscalatesOnce(t *testing.T) {
	f, w, issue, reviewer := wakeFixture(t)
	manager := f.privateAgentOwnedBy(t, f.UserID, "manager")
	f.Exec(t, "UPDATE issue SET status='in_review',assignee_type='agent',assignee_id=$2,updated_at=now()-interval '2 days' WHERE id=$1", issue, reviewer)
	svc := IssueContinuation{Tasks: w.Tasks, ManagerID: parseTestUUID(t, manager)}
	scan := func() {
		if err := svc.Tick(context.Background()); err != nil {
			t.Error(err)
		}
	}
	var group sync.WaitGroup
	for i := 0; i < 2; i++ {
		group.Add(1)
		go func() { defer group.Done(); scan() }()
	}
	group.Wait()
	count := func(agent string, want int) {
		t.Helper()
		var n int
		if err := f.Pool.QueryRow(context.Background(), "SELECT count(*) FROM agent_task_queue WHERE issue_id=$1 AND agent_id=$2", issue, agent).Scan(&n); err != nil || n != want {
			t.Fatalf("runs=%d want %d (%v)", n, want, err)
		}
	}
	count(reviewer, 1)
	finish := func() {
		f.Exec(t, "UPDATE agent_task_queue SET status='completed',completed_at=now()-interval '2 minutes' WHERE issue_id=$1", issue)
	}
	finish()
	scan()
	count(manager, 1)
	finish()
	scan()
	count(manager, 1)
	f.Exec(t, "UPDATE issue SET status='done' WHERE id=$1", issue)
	scan()
	count(manager, 1)
}

func TestIssueContinuationHonorsActiveRunsAndDependencies(t *testing.T) {
	f, w, issue, agent := wakeFixture(t)
	c := IssueContinuation{Tasks: w.Tasks, ManagerID: parseTestUUID(t, agent)}
	f.Exec(t, "UPDATE issue SET status='blocked',updated_at=now()-interval '2 days' WHERE id=$1", issue)
	upstream := f.Issue(t, "prerequisite")
	wakeCreate(t, f, w, issue, WakeupInput{AgentID: agent, Kind: "event", Condition: []byte(`{"type":"other_issue","issue_id":"` + upstream + `","state":"done"}`), Instruction: "continue after acceptance"})
	pending := f.Task(t, agent, testutil.Cols{"issue_id": issue, "status": "running", "runtime_id": testutil.Raw("(SELECT runtime_id FROM agent WHERE id='" + agent + "')")})
	check := func(want int) {
		t.Helper()
		if err := c.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := f.Pool.QueryRow(context.Background(), "SELECT count(*) FROM agent_task_queue WHERE issue_id=$1 AND id<>$2", issue, pending).Scan(&n); err != nil || n != want {
			t.Fatalf("recovery runs=%d want %d (%v)", n, want, err)
		}
	}
	check(0)
	f.Exec(t, "UPDATE agent_task_queue SET status='completed',completed_at=now()-interval '2 minutes' WHERE id=$1", pending)
	check(0)
	f.Exec(t, "UPDATE issue SET status='cancelled' WHERE id=$1", upstream)
	check(1)

}

func TestIssueContinuationCancellationWithdrawsQueueAndStopsRecovery(t *testing.T) {
	f, w, issue, agent := wakeFixture(t)
	ctx := context.Background()
	s := IssueContinuation{Tasks: w.Tasks, ManagerID: parseTestUUID(t, agent)}
	setStatus := func(status string) {
		f.Exec(t, "UPDATE issue SET status=$2,updated_at=now()-interval '1 hour' WHERE id=$1", issue, status)
	}
	setStatus("todo")
	if err := s.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	var id pgtype.UUID
	if err := f.Pool.QueryRow(ctx, "SELECT id FROM agent_task_queue WHERE issue_id=$1", issue).Scan(&id); err != nil {
		t.Fatal(err)
	}
	task, err := f.q.GetAgentTask(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.CheckClaim(ctx, task); err != nil {
		t.Fatalf("expected open continuation to remain claimable: %v", err)
	}
	setStatus("cancelled")
	closed, err := f.q.GetIssue(ctx, issue)
	if err != nil {
		t.Fatal(err)
	}
	dropped, err := StopClosedIssueWakeups(ctx, f.q, closed)
	if err != nil || len(dropped) != 1 || dropped[0].ID != id || dropped[0].Status != "cancelled" {
		t.Fatalf("did not withdraw pending recovery: %v %v", dropped, err)
	}
	if err = w.CheckClaim(ctx, task); !errors.Is(err, ErrWakeupForbidden) {
		t.Fatalf("closed task claim = %v", err)
	}
	f.Exec(t, "UPDATE agent_task_queue SET completed_at=now()-interval '2 hours' WHERE id=$1", id)
	if err = s.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	if err = f.Pool.QueryRow(ctx, "SELECT count(*) FROM agent_task_queue WHERE issue_id=$1", issue).Scan(&n); err != nil || n != 1 {
		t.Fatalf("cancelled issue redispatched: total=%d err=%v", n, err)
	}
}

func TestIssueContinuationBacklogRechecksChangedPrerequisite(t *testing.T) {
	f, w, issue, agent := wakeFixture(t)
	ctx := context.Background()
	pre := f.Issue(t, "upstream")
	f.Exec(t, `UPDATE issue SET status='backlog',updated_at=now()-interval '2 days',
 metadata=jsonb_build_object('backlog_plan',(SELECT jsonb_build_object('state','waiting_dependency','next','Review the prerequisite','dependencies',
 jsonb_build_array(jsonb_build_object('issue',ws.issue_prefix||'-'||p.number,'status',p.status)))::text
 FROM issue p JOIN workspace ws ON ws.id=p.workspace_id WHERE p.id=$2)) WHERE id=$1`, issue, pre)
	scan := func(want int) {
		t.Helper()
		c := IssueContinuation{Tasks: w.Tasks, ManagerID: parseTestUUID(t, agent)}
		if err := c.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := f.Pool.QueryRow(ctx, "SELECT count(*) FROM agent_task_queue WHERE issue_id=$1", issue).Scan(&n); err != nil || n != want {
			t.Fatalf("planning runs=%d want %d (%v)", n, want, err)
		}
	}
	scan(0)
	f.Exec(t, "UPDATE issue SET status='done' WHERE id=$1", pre)
	scan(1)
	f.Exec(t, "UPDATE agent_task_queue SET status='completed', completed_at=now()-interval '2 days' WHERE issue_id=$1", issue)
	scan(1)
	f.Exec(t, "UPDATE issue SET status='blocked' WHERE id=$1", pre)
	scan(2)
	var status string
	if err := f.Pool.QueryRow(ctx, "SELECT status FROM issue WHERE id=$1", issue).Scan(&status); err != nil || status != "backlog" {
		t.Fatalf("planning changed status to %q: %v", status, err)
	}
}

// A member's decision belongs to its requesting story. Sibling consumers wait
// for the deliverable on that story, even if an older plan also lists the choice.
func TestIssueContinuationSiblingWaitsForDeliverable(t *testing.T) {
	f, w, consumer, agent := wakeFixture(t)
	source := f.Issue(t, "prepare shared questions")
	member := f.Issue(t, "choose sources", testutil.Cols{"assignee_type": "member", "assignee_id": f.UserID})
	ctx := context.Background()
	issueRef := func(id string) string {
		t.Helper()
		var ref string
		if err := f.Pool.QueryRow(ctx, "SELECT w.issue_prefix||'-'||i.number FROM issue i JOIN workspace w ON w.id=i.workspace_id WHERE i.id=$1", id).Scan(&ref); err != nil {
			t.Fatal(err)
		}
		return ref
	}
	f.Exec(t, `UPDATE issue SET status='todo', metadata=jsonb_build_object('member_todo_key','todo-choice','member_todo_source',$2::text), updated_at=now()-interval '2 days' WHERE id=$1`, member, issueRef(source))
	f.Exec(t, "UPDATE issue SET status='backlog' WHERE id=$1", source)
	meta := condition(t, map[string]any{"backlog_plan": map[string]any{"state": "needs_story", "next": "wait for reviewed questions", "dependencies": []map[string]string{{"issue": issueRef(member), "status": "todo"}, {"issue": issueRef(source), "status": "backlog"}}}})
	f.Exec(t, "UPDATE issue SET status='backlog', metadata=$2,updated_at=now()-interval '2 days' WHERE id=$1", consumer, meta)
	svc := IssueContinuation{Tasks: w.Tasks, ManagerID: parseTestUUID(t, agent)}
	scan := func(want int) {
		t.Helper()
		if err := svc.Tick(ctx); err != nil {
			t.Fatal(err)
		}
		if got := f.Count(t, "SELECT count(*) FROM agent_task_queue WHERE issue_id=$1", consumer); got != want {
			t.Fatalf("consumer dispatched %d, want %d", got, want)
		}
	}
	scan(0)
	if got := f.Count(t, "SELECT count(*) FROM agent_task_queue WHERE issue_id=$1", member); got != 0 {
		t.Fatal("waiting for the member started a coordinator")
	}
	f.Exec(t, "UPDATE issue SET status='done' WHERE id=$1", member)
	scan(0)
	f.Exec(t, "UPDATE issue SET status='in_review' WHERE id=$1", source)
	scan(1)
}
