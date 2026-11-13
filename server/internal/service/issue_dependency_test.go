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
		"workspace_id":      workspaceID,
		"project_id":        projectID,
		"subject_kind":      "issue",
		"subject_id":        issueA,
		"action":            "issue_close",
		"delivery_version":  "e7206f4d5ca0967b4868c70fae3f82e99f16ccf1",
		"delivery_tree":     "50c145a22f6e099fc68f9be21933a89fb2d7c790",
		"criteria_revision": PlanCriteriaRevision(util.MustParseUUID(goalID), 1),
		"author_type":       "agent",
		"author_id":         agentID,
		"author_model":      "deepseek-v4.1-flash",
		"reviewer_type":     "member",
		"reviewer_id":       ownerID,
		"verdict":           "pass",
		"criteria":          testutil.Raw("'[]'::jsonb"),
		"platforms":         testutil.Raw("'[]'::jsonb"),
		"missing":           testutil.Raw("'[]'::jsonb"),
		"authorization_id":  authorization,
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

// fireAtIsNull reports whether the parked row is invisible to time-based
// promotion. That is the difference between "parked until a real event" and
// "retried every poll".
func (f *dependencyFixture) fireAtIsNull(t *testing.T, taskID string) bool {
	t.Helper()
	var isNull bool
	f.fx.QueryRow(t, `SELECT fire_at IS NULL FROM agent_task_queue WHERE id = $1`, taskID).Scan(&isNull)
	return isNull
}

// releaseDependents is the service-level equivalent of the handler's dependency
// event: it arms only rows parked with no fire_at and returns how many moved.
func (f *dependencyFixture) releaseDependents(t *testing.T) int {
	t.Helper()
	armed, err := f.svc.Queries.ReleaseDeferredDependentTasksForIssue(context.Background(), util.MustParseUUID(f.issueC))
	if err != nil {
		t.Fatalf("ReleaseDeferredDependentTasksForIssue: %v", err)
	}
	return len(armed)
}

// linkPullRequest registers an authoritative delivery version for A: the head
// SHA of a linked pull request, which is exactly what the release check reads.
func (f *dependencyFixture) linkPullRequest(t *testing.T, headSHA string) string {
	t.Helper()
	prID := f.fx.Insert(t, "github_pull_request", testutil.Cols{
		"workspace_id":    f.workspaceID,
		"installation_id": int64(1),
		"repo_owner":      "XFreeMing",
		"repo_name":       "XShadow",
		"pr_number":       time.Now().UnixNano() % 1_000_000_000,
		"title":           "dependency delivery",
		"state":           "open",
		"html_url":        "https://github.com/XFreeMing/XShadow/pull/1",
		"pr_created_at":   testutil.Raw("now()"),
		"pr_updated_at":   testutil.Raw("now()"),
		"head_sha":        headSHA,
	})
	f.fx.InsertNoID(t, "issue_pull_request", testutil.Cols{
		"issue_id":        f.issueA,
		"pull_request_id": prID,
	}, "issue_id = $1 AND pull_request_id = $2", f.issueA, prID)
	return prID
}

// acceptReviewFor stamps the persisted acceptance against the current plan
// revision, as the closure endpoint would.
func (f *dependencyFixture) acceptReviewFor(t *testing.T, deliveryVersion string) {
	t.Helper()
	f.fx.Exec(t, `UPDATE closure_review
		SET accepted_at = now(), accepted_by_type = 'member', accepted_by_id = $2,
		    delivery_version = $3,
		    criteria_revision = $4
		WHERE id = $1`, f.review, f.ownerID, deliveryVersion, PlanCriteriaRevision(util.MustParseUUID(f.goalID), 1))
}

func (f *dependencyFixture) ready(t *testing.T) IssueDependencyState {
	t.Helper()
	state, err := IssueDependenciesReady(context.Background(), f.svc.Queries, f.loadIssue(t, f.issueC))
	if err != nil {
		t.Fatalf("IssueDependenciesReady: %v", err)
	}
	return state
}

func (f *dependencyFixture) loadIssue(t *testing.T, issueID string) db.Issue {
	t.Helper()
	issue, err := f.svc.Queries.GetIssue(context.Background(), util.MustParseUUID(issueID))
	if err != nil {
		t.Fatalf("load issue: %v", err)
	}
	return issue
}

// The wait verdict is durable, not only visible in the claim call that parked
// the row. A reader can explain `deferred` with no fire_at, and the release
// event clears the stale verdict before arming the row.
func TestParkedTaskPersistsDependencyWaitAndReleaseClearsIt(t *testing.T) {
	f := newDependencyFixture(t)
	taskID := f.queueTaskForC(t)
	if task := f.claim(t); task != nil {
		t.Fatalf("claimed %s while A is unaccepted", task.ID)
	}

	var reason, detail, event string
	var prerequisites []byte
	f.fx.QueryRow(t, `SELECT COALESCE(context->'dependency_wait'->>'reason', ''),
		COALESCE(context->'dependency_wait'->>'detail', ''),
		COALESCE(context->'dependency_wait'->>'event', ''),
		COALESCE(context->'dependency_wait'->'prerequisites', '[]'::jsonb)
		FROM agent_task_queue WHERE id = $1`, taskID).Scan(&reason, &detail, &event, &prerequisites)
	if reason != DependencyReasonNotDone || detail == "" || event != "dependency_changed" {
		t.Fatalf("persisted wait = %q/%q/%q, want %q with detail and dependency_changed",
			reason, detail, event, DependencyReasonNotDone)
	}
	// The structured prerequisite facts are persisted too, so a client can
	// localize the wait without parsing the English detail.
	var stored []DependencyWaitPrerequisite
	if err := json.Unmarshal(prerequisites, &stored); err != nil {
		t.Fatalf("persisted prerequisites = %s: %v", prerequisites, err)
	}
	if len(stored) != 1 || stored[0].ItemID != "node-a" || stored[0].IssueID != f.issueA ||
		stored[0].Status != "in_review" || stored[0].Reason != DependencyReasonNotDone {
		t.Fatalf("persisted prerequisites = %+v, want node-a/in_review/%s", stored, DependencyReasonNotDone)
	}

	f.acceptA(t)
	if armed := f.releaseDependents(t); armed != 1 {
		t.Fatalf("release armed %d rows, want 1", armed)
	}
	var fireAtNull bool
	var waitCleared bool
	f.fx.QueryRow(t, `SELECT fire_at IS NULL, context->'dependency_wait' IS NULL
		FROM agent_task_queue WHERE id = $1`, taskID).Scan(&fireAtNull, &waitCleared)
	if fireAtNull {
		t.Fatal("released task still has no fire_at")
	}
	if !waitCleared {
		t.Fatal("released task kept its dependency_wait verdict")
	}
}

// A dependent queued while A is still in review is parked: the rejected claim
// takes it out of the claim queue instead of handing it back for every later
// poll. Once A is a done issue with a valid accepted review, a real dependency
// event re-arms the same row and the dependent starts exactly once. B never
// being finished is irrelevant.
func TestDependentStartsOnlyAfterPrerequisiteIsAccepted(t *testing.T) {
	f := newDependencyFixture(t)
	taskID := f.queueTaskForC(t)

	if task := f.claim(t); task != nil {
		t.Fatalf("claimed %s while A is in review; dependent must stay parked", task.ID)
	}
	if got := f.taskStatus(t, taskID); got != "deferred" {
		t.Fatalf("task status after a rejected claim = %q, want deferred", got)
	}
	if !f.fireAtIsNull(t, taskID) {
		t.Fatal("parked task has a fire_at; time-based promotion would retry it every poll")
	}
	// The dependent is not failed or cancelled and owns no second queue entry:
	// it starts when the evidence appears, after the dependency-change event.
	f.acceptA(t)
	if armed := f.releaseDependents(t); armed != 1 {
		t.Fatalf("dependency release armed %d rows, want 1", armed)
	}
	claimed := f.claim(t)
	if claimed == nil {
		t.Fatal("dependent was not claimed after A became an accepted success")
	}
	if uuid := util.UUIDToString(claimed.ID); uuid != taskID {
		t.Fatalf("claimed task = %s, want the already parked %s", uuid, taskID)
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
			if got := f.taskStatus(t, taskID); got != "deferred" {
				t.Fatalf("task status = %q, want deferred (parked, not retried every poll)", got)
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

// A rejected claim must not be retried by the scheduler. Once parked, repeated
// polls and the ordinary deferred promotion both leave the row alone; only a
// real dependency event re-arms it.
func TestRejectedDependencyClaimIsNotPickedUpEveryPoll(t *testing.T) {
	f := newDependencyFixture(t)
	taskID := f.queueTaskForC(t)

	for poll := 0; poll < 4; poll++ {
		if task := f.claim(t); task != nil {
			t.Fatalf("poll %d claimed %s although the prerequisite is unaccepted", poll, task.ID)
		}
	}
	if got := f.taskStatus(t, taskID); got != "deferred" {
		t.Fatalf("task status after repeated polls = %q, want deferred", got)
	}
	if !f.fireAtIsNull(t, taskID) {
		t.Fatal("parked task has a fire_at; every promotion tick would retry it")
	}
	// Even an explicit promotion sweep must not pull a NULL-fire_at row forward.
	if err := f.svc.PromoteDueDeferredTasksForRuntime(context.Background(), util.MustParseUUID(f.runtimeID)); err != nil {
		t.Fatalf("PromoteDueDeferredTasksForRuntime: %v", err)
	}
	if got := f.taskStatus(t, taskID); got != "deferred" {
		t.Fatalf("task status after promotion = %q, want deferred", got)
	}
	if count := f.fx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1`, f.issueC); count != 1 {
		t.Fatalf("task rows for C = %d, want 1", count)
	}
}

// Replaying the dependency-change event is harmless: the first event arms the
// parked row, later duplicates find no NULL-fire_at row, and the queue still
// dispatches exactly one task.
func TestDuplicateDependencyEventDispatchesOnce(t *testing.T) {
	f := newDependencyFixture(t)
	taskID := f.queueTaskForC(t)
	if task := f.claim(t); task != nil {
		t.Fatalf("claimed %s while A is unaccepted", task.ID)
	}
	f.acceptA(t)

	if armed := f.releaseDependents(t); armed != 1 {
		t.Fatalf("first release armed %d rows, want 1", armed)
	}
	if armed := f.releaseDependents(t); armed != 0 {
		t.Fatalf("duplicate release armed %d rows, want 0 (already armed)", armed)
	}
	first := f.claim(t)
	if first == nil {
		t.Fatal("dependent was not claimed after the dependency event")
	}
	if util.UUIDToString(first.ID) != taskID {
		t.Fatalf("claimed task = %s, want %s", util.UUIDToString(first.ID), taskID)
	}
	if second := f.claim(t); second != nil {
		t.Fatalf("second claim dispatched %s; want no duplicate", second.ID)
	}
	if count := f.fx.Count(t, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1`, f.issueC); count != 1 {
		t.Fatalf("task rows for C = %d, want 1", count)
	}
}

// Acceptance names a delivery and an acceptance scope. A registered delivery
// that advances without a new review invalidates it immediately: the parked
// dependent stays parked and is not released by a duplicate event.
func TestNewRegisteredDeliveryBlocksReleaseWithoutNewReview(t *testing.T) {
	f := newDependencyFixture(t)
	prID := f.linkPullRequest(t, "aaaa1111")
	taskID := f.queueTaskForC(t)
	if task := f.claim(t); task != nil {
		t.Fatalf("claimed %s before A was accepted", task.ID)
	}

	// A is delivered and accepted at the registered head.
	f.fx.Exec(t, `UPDATE issue SET status = 'done' WHERE id = $1`, f.issueA)
	f.acceptReviewFor(t, "aaaa1111")
	if state := f.ready(t); !state.Ready {
		t.Fatalf("release blocked while the acceptance matches the registered delivery: %+v", state)
	}

	// HEAD advances. No new review exists yet.
	f.fx.Exec(t, `UPDATE github_pull_request SET head_sha = 'bbbb2222' WHERE id = $1`, prID)
	state := f.ready(t)
	if state.Ready {
		t.Fatal("acceptance for the old head still releases the dependent")
	}
	if state.Reason != DependencyReasonNotAccepted {
		t.Fatalf("reason = %q, want %q", state.Reason, DependencyReasonNotAccepted)
	}
	if armed := f.releaseDependents(t); armed != 1 {
		t.Fatalf("release event armed %d rows, want the parked 1", armed)
	}
	if task := f.claim(t); task != nil {
		t.Fatalf("claim dispatched %s although the registered delivery has no accepted review", task.ID)
	}
	if got := f.taskStatus(t, taskID); got != "deferred" {
		t.Fatalf("task status = %q, want deferred after the failed re-check", got)
	}

	// A review against the new head is the fact that releases it.
	f.acceptReviewFor(t, "bbbb2222")
	if armed := f.releaseDependents(t); armed != 1 {
		t.Fatalf("release after the new review armed %d rows, want 1", armed)
	}
	if claimed := f.claim(t); claimed == nil {
		t.Fatal("dependent was not released after the delivery was re-reviewed")
	}
}

// Saving the plan bumps its revision; acceptance recorded against the old
// revision stops being valid even when no new review arrives.
func TestPlanRevisionBumpInvalidatesAcceptance(t *testing.T) {
	f := newDependencyFixture(t)
	taskID := f.queueTaskForC(t)
	if task := f.claim(t); task != nil {
		t.Fatalf("claimed %s before A was accepted", task.ID)
	}
	f.acceptA(t)
	if state := f.ready(t); !state.Ready {
		t.Fatalf("release blocked at the accepted plan revision: %+v", state)
	}

	f.fx.Exec(t, `UPDATE goal_plan SET revision = 2 WHERE goal_id = $1`, f.goalID)
	state := f.ready(t)
	if state.Ready {
		t.Fatal("acceptance for plan revision 1 still releases after the plan moved to revision 2")
	}
	if state.Reason != DependencyReasonNotAccepted {
		t.Fatalf("reason = %q, want %q", state.Reason, DependencyReasonNotAccepted)
	}
	if armed := f.releaseDependents(t); armed != 1 {
		t.Fatalf("release event armed %d rows, want the parked 1", armed)
	}
	if task := f.claim(t); task != nil {
		t.Fatalf("claim dispatched %s although the plan revision moved past the acceptance", task.ID)
	}
	if got := f.taskStatus(t, taskID); got != "deferred" {
		t.Fatalf("task status = %q, want deferred after the failed re-check", got)
	}
}
