package service

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// DB-backed coverage for managed dependency release. The fixture is a goal
// plan with independent nodes A and B and a strong edge C -> A: C may start
// only after A is a success terminal with a currently valid persisted
// acceptance. B is deliberately left unfinished throughout, proving C does not
// wait for unrelated nodes.
type dependencyFixture struct {
	svc           *TaskService
	issues        *IssueService
	fx            *testutil.Fixture
	workspaceID   string
	ownerID       string
	agentID       string
	runtimeID     string
	goalID        string
	issueA        string
	issueB        string
	issueC        string
	authorization string
	review        string
}

func newDependencyFixture(t *testing.T) *dependencyFixture {
	t.Helper()
	pool := sharedTestPool(t)
	bootstrap := testutil.New(pool, "", "")
	suffix := time.Now().UnixNano()
	ownerID := bootstrap.User(t,
		fmt.Sprintf("dependency-owner-%d", suffix),
		fmt.Sprintf("dependency-owner-%d@example.com", suffix),
	)
	workspaceID := bootstrap.Workspace(t,
		fmt.Sprintf("dependency-%d", suffix),
		fmt.Sprintf("dependency-%d", suffix),
	)
	fx := testutil.New(pool, workspaceID, ownerID)
	fx.Member(t, workspaceID, ownerID, "owner")
	projectID := fx.Project(t, "dependency project")
	runtimeID := fx.Runtime(t, "dependency runtime", testutil.Cols{
		"visibility": "public",
		"owner_id":   ownerID,
	})
	agentID := fx.Agent(t, "dependency agent", runtimeID, testutil.Cols{
		"owner_id":             ownerID,
		"max_concurrent_tasks": 5,
	})
	goalID := fx.Insert(t, "goal", testutil.Cols{
		"workspace_id": workspaceID,
		"project_id":   projectID,
		"title":        "dependency goal",
		"status":       "in_progress",
		"creator_type": "member",
		"creator_id":   ownerID,
		"owner_type":   "member",
		"owner_id":     ownerID,
	})
	issueA := fx.Issue(t, "A prerequisite", testutil.Cols{
		"status": "in_review", "assignee_type": "agent", "assignee_id": agentID,
	})
	issueB := fx.Issue(t, "B independent", testutil.Cols{
		"status": "in_progress", "assignee_type": "agent", "assignee_id": agentID,
	})
	issueC := fx.Issue(t, "C dependent", testutil.Cols{
		"status": "todo", "assignee_type": "agent", "assignee_id": agentID,
	})
	for _, issueID := range []string{issueA, issueB, issueC} {
		fx.InsertNoID(t, "goal_issue", testutil.Cols{
			"workspace_id": workspaceID,
			"goal_id":      goalID,
			"issue_id":     issueID,
		}, "workspace_id = $1 AND issue_id = $2", workspaceID, issueID)
	}
	content, err := json.Marshal(map[string]any{
		"items": []map[string]any{
			{"id": "node-a", "title": "A", "binding": map[string]any{"type": "existing", "issue_id": issueA}},
			{"id": "node-b", "title": "B", "binding": map[string]any{"type": "existing", "issue_id": issueB}},
			{"id": "node-c", "title": "C", "binding": map[string]any{"type": "existing", "issue_id": issueC}},
		},
		"edges": []map[string]any{{
			"dependent_item_id":    "node-c",
			"prerequisite_item_id": "node-a",
			"reason":               "C builds on A's delivered interface",
		}},
	})
	if err != nil {
		t.Fatalf("marshal plan content: %v", err)
	}
	fx.Insert(t, "goal_plan", testutil.Cols{
		"workspace_id": workspaceID,
		"goal_id":      goalID,
		"project_id":   projectID,
		"revision":     1,
		"content":      string(content),
	})
	authorization := fx.Insert(t, "closure_authorization", testutil.Cols{
		"workspace_id":    workspaceID,
		"project_id":      projectID,
		"subject_kind":    "issue",
		"subject_id":      issueA,
		"actions":         []string{"issue_close"},
		"covers_children": false,
		"state":           "active",
		"revision":        1,
		"granted_by_type": "member",
		"granted_by_id":   ownerID,
	})
	review := fx.Insert(t, "closure_review", testutil.Cols{
		"workspace_id":     workspaceID,
		"project_id":       projectID,
		"subject_kind":     "issue",
		"subject_id":       issueA,
		"action":           "issue_close",
		"delivery_version": "e7206f4d5ca0967b4868c70fae3f82e99f16ccf1",
		"delivery_tree":    "50c145a22f6e099fc68f9be21933a89fb2d7c790",
		"author_type":      "agent",
		"author_id":        agentID,
		"author_model":     "deepseek-v4.1-flash",
		"reviewer_type":    "member",
		"reviewer_id":      ownerID,
		"verdict":          "pass",
		"criteria":         testutil.Raw("'[]'::jsonb"),
		"platforms":        testutil.Raw("'[]'::jsonb"),
		"missing":          testutil.Raw("'[]'::jsonb"),
		"authorization_id": authorization,
	})

	taskSvc := NewTaskService(db.New(pool), pool, nil, events.New())
	return &dependencyFixture{
		svc:           taskSvc,
		issues:        NewIssueService(db.New(pool), pool, events.New(), nil, taskSvc),
		fx:            fx,
		workspaceID:   workspaceID,
		ownerID:       ownerID,
		agentID:       agentID,
		runtimeID:     runtimeID,
		goalID:        goalID,
		issueA:        issueA,
		issueB:        issueB,
		issueC:        issueC,
		authorization: authorization,
		review:        review,
	}
}

// acceptA is the persisted success the contract requires: A is done and its
// pass review carries accepted_at. Every other prerequisite fact is untouched.
func (f *dependencyFixture) acceptA(t *testing.T) {
	t.Helper()
	f.fx.Exec(t, `UPDATE issue SET status = 'done' WHERE id = $1`, f.issueA)
	f.fx.Exec(t, `UPDATE closure_review SET accepted_at = now(), accepted_by_type = 'member', accepted_by_id = $2 WHERE id = $1`, f.review, f.ownerID)
}

func (f *dependencyFixture) queueTaskForC(t *testing.T) string {
	t.Helper()
	return f.fx.Task(t, f.agentID, testutil.Cols{
		"runtime_id": f.runtimeID,
		"issue_id":   f.issueC,
		"status":     "queued",
	})
}

func (f *dependencyFixture) claim(t *testing.T) *db.AgentTaskQueue {
	t.Helper()
	task, err := f.svc.ClaimTaskForRuntime(context.Background(), util.MustParseUUID(f.runtimeID))
	if err != nil {
		t.Fatalf("ClaimTaskForRuntime: %v", err)
	}
	return task
}

func (f *dependencyFixture) taskStatus(t *testing.T, taskID string) string {
	t.Helper()
	var status string
	f.fx.QueryRow(t, `SELECT status FROM agent_task_queue WHERE id = $1`, taskID).Scan(&status)
	return status
}

func (f *dependencyFixture) loadIssue(t *testing.T, issueID string) db.Issue {
	t.Helper()
	issue, err := f.svc.Queries.GetIssue(context.Background(), util.MustParseUUID(issueID))
	if err != nil {
		t.Fatalf("load issue: %v", err)
	}
	return issue
}

// A dependent queued while A is still in review stays queued: no claim, no
// dispatch. Once A is a done issue with a valid accepted review, the same
// queued task is claimed. B never being finished is irrelevant.
func TestDependentStartsOnlyAfterPrerequisiteIsAccepted(t *testing.T) {
	f := newDependencyFixture(t)
	taskID := f.queueTaskForC(t)

	if task := f.claim(t); task != nil {
		t.Fatalf("claimed %s while A is in review; dependent must stay queued", task.ID)
	}
	if got := f.taskStatus(t, taskID); got != "queued" {
		t.Fatalf("task status = %q, want queued", got)
	}
	// The dependent is still queued, not failed or cancelled: it starts when
	// the evidence appears, without a second enqueue.
	f.acceptA(t)
	claimed := f.claim(t)
	if claimed == nil {
		t.Fatal("dependent was not claimed after A became an accepted success")
	}
	if uuid := util.UUIDToString(claimed.ID); uuid != taskID {
		t.Fatalf("claimed task = %s, want the already queued %s", uuid, taskID)
	}
	if got := util.UUIDToString(claimed.IssueID); got != f.issueC {
		t.Fatalf("claimed issue = %s, want C %s", got, f.issueC)
	}
}

// Status alone never releases a dependent. Every case keeps A looking like a
// success in the issue row (or makes it more clearly non-success) while the
// persisted acceptance is absent or no longer valid.
func TestDependentRefusedWithoutCurrentlyValidAcceptance(t *testing.T) {
	cases := []struct {
		name            string
		status          string
		accepted        bool
		authorization   string
		supersedeReview bool
		wantReason      string
	}{
		{name: "done written by a human without acceptance", status: "done", accepted: false, wantReason: DependencyReasonNotAccepted},
		{name: "done written by PR automation without acceptance", status: "done", accepted: false, wantReason: DependencyReasonNotAccepted},
		{name: "still in review", status: "in_review", accepted: true, wantReason: DependencyReasonNotDone},
		{name: "blocked", status: "blocked", accepted: true, wantReason: DependencyReasonNotDone},
		{name: "cancelled is not success", status: "cancelled", accepted: true, wantReason: DependencyReasonNotDone},
		{name: "grant revoked after acceptance", status: "done", accepted: true, authorization: "revoked", wantReason: DependencyReasonNotAccepted},
		{name: "review superseded by a newer delivery", status: "done", accepted: true, supersedeReview: true, wantReason: DependencyReasonNotAccepted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newDependencyFixture(t)
			taskID := f.queueTaskForC(t)
			f.fx.Exec(t, `UPDATE issue SET status = $2 WHERE id = $1`, f.issueA, tc.status)
			if tc.accepted {
				f.fx.Exec(t, `UPDATE closure_review SET accepted_at = now(), accepted_by_type = 'member', accepted_by_id = $2 WHERE id = $1`, f.review, f.ownerID)
			}
			if tc.authorization != "" {
				f.fx.Exec(t, `UPDATE closure_authorization SET state = $2 WHERE id = $1`, f.authorization, tc.authorization)
			}
			if tc.supersedeReview {
				f.fx.Exec(t, `UPDATE closure_review SET superseded_by = $2 WHERE id = $1`, f.review, f.review)
			}

			state, err := IssueDependenciesReady(context.Background(), f.svc.Queries, f.loadIssue(t, f.issueC))
			if err != nil {
				t.Fatalf("IssueDependenciesReady: %v", err)
			}
			if state.Ready {
				t.Fatalf("readiness = ready, want refusal (%s)", tc.wantReason)
			}
			if state.Reason != tc.wantReason {
				t.Fatalf("reason = %q, want %q", state.Reason, tc.wantReason)
			}
			if task := f.claim(t); task != nil {
				t.Fatalf("claimed %s although the prerequisite acceptance is not currently valid", task.ID)
			}
			if got := f.taskStatus(t, taskID); got != "queued" {
				t.Fatalf("task status = %q, want queued", got)
			}
		})
	}
}

// The same evidence requirement holds at the enqueue predicate: an issue
// write that would start C's agent is refused while A is unaccepted and
// allowed once A is accepted (B remains unfinished).
func TestEnqueuePredicateWaitsForAcceptedPrerequisite(t *testing.T) {
	f := newDependencyFixture(t)
	ctx := context.Background()
	issueC := f.loadIssue(t, f.issueC)
	in := IssueTriggerInput{Issue: issueC, PrevStatus: "backlog", StatusChanged: true}
	if _, ok := f.issues.WillEnqueueRun(ctx, in, IssueTriggerProbe{}); ok {
		t.Fatal("enqueue predicate promised a run while A had no acceptance")
	}
	f.acceptA(t)
	in.Issue = f.loadIssue(t, f.issueC)
	trigger, ok := f.issues.WillEnqueueRun(ctx, in, IssueTriggerProbe{})
	if !ok {
		t.Fatal("enqueue predicate refused C after A became an accepted success")
	}
	if util.UUIDToString(trigger.IssueID) != f.issueC {
		t.Fatalf("trigger issue = %s, want C", util.UUIDToString(trigger.IssueID))
	}
}

// Unknown dependency facts fail closed: an unreadable plan and a prerequisite
// item with no linked issue both refuse release instead of silently treating
// the dependent as unconstrained.
func TestUnknownDependencyFactsBlockRelease(t *testing.T) {
	t.Run("unreadable plan", func(t *testing.T) {
		f := newDependencyFixture(t)
		f.fx.Exec(t, `UPDATE goal_plan SET content = '"not-a-plan"'::jsonb WHERE goal_id = $1`, f.goalID)
		state, err := IssueDependenciesReady(context.Background(), f.svc.Queries, f.loadIssue(t, f.issueC))
		if err != nil {
			t.Fatalf("IssueDependenciesReady: %v", err)
		}
		if state.Ready || state.Reason != DependencyReasonUnreadable {
			t.Fatalf("state = %+v, want unreadable refusal", state)
		}
	})
	t.Run("prerequisite item is not linked to an issue", func(t *testing.T) {
		f := newDependencyFixture(t)
		content, err := json.Marshal(map[string]any{
			"items": []map[string]any{
				{"id": "node-a", "title": "A", "binding": map[string]any{"type": "new"}},
				{"id": "node-c", "title": "C", "binding": map[string]any{"type": "existing", "issue_id": f.issueC}},
			},
			"edges": []map[string]any{{"dependent_item_id": "node-c", "prerequisite_item_id": "node-a", "reason": "needs A"}},
		})
		if err != nil {
			t.Fatalf("marshal content: %v", err)
		}
		f.fx.Exec(t, `UPDATE goal_plan SET content = $2::jsonb WHERE goal_id = $1`, f.goalID, string(content))
		state, err := IssueDependenciesReady(context.Background(), f.svc.Queries, f.loadIssue(t, f.issueC))
		if err != nil {
			t.Fatalf("IssueDependenciesReady: %v", err)
		}
		if state.Ready || state.Reason != DependencyReasonUnknown {
			t.Fatalf("state = %+v, want unknown refusal", state)
		}
	})
}

// A duplicate event must not dispatch twice: the first claim moves the single
// queued row and the second poll has nothing left for C.
func TestAcceptedDependentDispatchesOnce(t *testing.T) {
	f := newDependencyFixture(t)
	f.acceptA(t)
	taskID := f.queueTaskForC(t)
	first := f.claim(t)
	if first == nil {
		t.Fatal("first claim returned nil")
	}
	if second := f.claim(t); second != nil {
		t.Fatalf("second claim dispatched %s; want no duplicate", second.ID)
	}
	if count := f.fx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1`, f.issueC); count != 1 {
		t.Fatalf("task rows for C = %d, want 1 (the existing %s)", count, taskID)
	}
}