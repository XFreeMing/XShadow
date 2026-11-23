package handler

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/skill"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestSkillActivityRecordsTheDeliveredVersionAndReview(t *testing.T) {
	ctx := context.Background()
	saved := createVersionTestSkill(t, "test-skill-activity", []CreateSkillFileRequest{
		{Path: "notes.md", Content: "reference"},
	})
	dbfx.Cleanup(t, "DELETE FROM skill_run_observation WHERE skill_id = $1", saved.ID)
	issueID := dbfx.Issue(t, "Skill activity sample", testutil.Cols{"number": 799870})
	agentID := seededHandlerTestAgent(t)
	id := dbfx.Task(t, agentID, testutil.Cols{
		"issue_id": issueID, "runtime_id": testRuntimeID, "status": "dispatched",
		"dispatched_at": testutil.Raw("now() - interval '1 minute'"),
	})
	task, err := testHandler.Queries.GetAgentTask(ctx, parseUUID(id))
	if err != nil {
		t.Fatal(err)
	}
	hash1, err := skill.VersionHash(saved.Name, saved.Description, "v1 body",
		[]skill.VersionFile{{Path: "notes.md", Content: "reference"}})
	if err != nil {
		t.Fatal(err)
	}
	h := testHandler
	first := []protocol.PreparedSkill{{ID: saved.ID, ContentHash: hash1}}

	read := func() skillActivityResponse {
		t.Helper()
		var activity skillActivityResponse
		req := withURLParam(newRequest(http.MethodGet, "/api/skills/"+saved.ID+"/activity?days=7", nil), "id", saved.ID)
		testutil.Call(t, h.GetSkillActivity, req).Want(http.StatusOK).JSON(&activity)
		return activity
	}
	if got := read(); got.Counts.Available != 0 {
		t.Fatalf("a claim that never started counted: %+v", got.Counts)
	}

	updateSkillForVersionTest(t, saved.ID, UpdateSkillRequest{Content: strPtr("v2 body")})
	hash2, err := skill.VersionHash(saved.Name, saved.Description, "v2 body",
		[]skill.VersionFile{{Path: "notes.md", Content: "reference"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, "UPDATE agent_task_queue SET dispatched_at = now() WHERE id = $1", id); err != nil {
		t.Fatal(err)
	}
	reclaimed, err := testHandler.Queries.GetAgentTask(ctx, parseUUID(id))
	if err != nil {
		t.Fatal(err)
	}
	second := []protocol.PreparedSkill{{ID: saved.ID, ContentHash: hash2}}
	startSkillActivityRun(t, task, first, http.StatusConflict)
	startSkillActivityRun(t, reclaimed, second, http.StatusOK)
	startSkillActivityRun(t, reclaimed, second, http.StatusOK)
	if _, err := testPool.Exec(ctx, "UPDATE agent_task_queue SET status = 'completed', started_at = now(), completed_at = now() WHERE id = $1", id); err != nil {
		t.Fatal(err)
	}
	got := read()
	if got.Counts.Available != 1 || got.Counts.Adopted != 0 || len(got.Runs) != 1 {
		t.Fatalf("reclaim or availability miscounted: %+v", got)
	}
	run := got.Runs[0]
	if run.ContentHash != hash2 || run.Version == nil || *run.Version != 2 {
		t.Fatalf("run did not keep delivered v2: %+v", run)
	}

	review := func(hash, outcome, evidence string, want int) {
		t.Helper()
		req := withURLParam(newRequest(http.MethodPost, "/api/skills/"+saved.ID+"/activity/review", map[string]string{
			"run_id": id, "content_hash": hash, "outcome": outcome, "evidence": evidence,
		}), "id", saved.ID)
		testutil.Call(t, h.ReviewSkillActivity, req).Want(want)
	}
	review(hash1, "helped", "Parallel edges resolve separately.", http.StatusConflict)
	review(hash2, "helped", " ", http.StatusBadRequest)
	review(hash2, "helped", "Parallel edges resolve separately.", http.StatusNoContent)
	review(hash2, "helped", "Parallel edges resolve separately.", http.StatusNoContent)
	got = read()
	if got.Counts.Reviewed != 1 || got.Counts.Adopted != 1 || got.Counts.Helped != 1 || got.Runs[0].Evidence == "" {
		t.Fatalf("review was not counted exactly once: %+v", got)
	}
	review(hash2, "not_applied", "No graph was needed on this run.", http.StatusNoContent)
	if got = read(); got.Counts.Adopted != 0 || got.Counts.NotApplied != 1 {
		t.Fatalf("a review of non-adoption counted as adoption: %+v", got.Counts)
	}
}

func TestSkillActivityHidesPrivateProjectRunsBeforeCounting(t *testing.T) {
	f := newPrivateProjectFixture(t)
	ctx := context.Background()
	saved := createVersionTestSkill(t, "test-skill-activity-visibility", nil)
	dbfx.Cleanup(t, "DELETE FROM skill_run_observation WHERE skill_id = $1", saved.ID)
	hash, err := skill.VersionHash(saved.Name, saved.Description, saved.Content, []skill.VersionFile{})
	if err != nil {
		t.Fatal(err)
	}
	for _, issueID := range []string{f.secretIssue, f.publicIssue} {
		id := dbfx.Task(t, seededHandlerTestAgent(t), testutil.Cols{
			"issue_id": issueID, "runtime_id": testRuntimeID, "status": "dispatched",
			"dispatched_at": testutil.Raw("now()"),
		})
		task, err := testHandler.Queries.GetAgentTask(ctx, parseUUID(id))
		if err != nil {
			t.Fatal(err)
		}
		startSkillActivityRun(t, task, []protocol.PreparedSkill{{ID: saved.ID, ContentHash: hash}}, http.StatusOK)
		if _, err := testPool.Exec(ctx, "UPDATE agent_task_queue SET status = 'completed', started_at = now() WHERE id = $1", id); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		viewer string
		want   int
	}{{f.insider, 2}, {f.outsider, 1}} {
		var result skillActivityResponse
		req := withURLParam(asUser(newRequest(http.MethodGet, "/api/skills/"+saved.ID+"/activity", nil), tc.viewer), "id", saved.ID)
		testutil.Call(t, testHandler.GetSkillActivity, req).Want(http.StatusOK).JSON(&result)
		if result.Counts.Available != tc.want || len(result.Runs) != tc.want || result.Versions[0].Available != tc.want {
			t.Fatalf("visibility was not applied to all views: %+v", result)
		}
	}
}

func startSkillActivityRun(t *testing.T, task db.AgentTaskQueue, skills []protocol.PreparedSkill, want int) {
	t.Helper()
	id := uuidToString(task.ID)
	req := withURLParam(newOwnerDaemonRequest(http.MethodPost, "/api/daemon/tasks/"+id+"/start", map[string]any{
		"runtime_id": uuidToString(task.RuntimeID), "dispatched_at": task.DispatchedAt.Time.Format(time.RFC3339Nano),
		"prepared_skills": skills,
	}), "taskId", id)
	testutil.Call(t, testHandler.StartTask, req).Want(want)
}
