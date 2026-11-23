package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/service"
	skillpkg "github.com/multica-ai/multica/server/internal/skill"
	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/pkg/acceptance"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/multica-ai/multica/server/pkg/workmethod"
)

func TestProjectSkillFromIndependentReviewToScopedRun(t *testing.T) {
	seedTestCatalog(t)
	h := testHandler
	agent := seededReadyAgentID(t)
	project := dbfx.Project(t, "Project skill source", testutil.Cols{"visibility": "private"})
	otherProject := dbfx.Project(t, "Unrelated project")
	outsider := dbfx.User(t, "Other project member", "project-skill-"+workflowTestSuffix()+"@example.com")
	dbfx.Member(t, testWorkspaceID, outsider, "member")
	dbfx.Cleanup(t, "DELETE FROM project_skill_proposal WHERE project_id=$1", project)
	commit := strings.Repeat("a", 40)
	trace, err := json.Marshal(acceptance.Trace{Revision: 4, Criteria: []acceptance.Criterion{{
		ID: "C1", Versions: []string{"Verify citation coordinates against source"},
		Cases: []acceptance.TestCase{{ID: "T1", Specification: "Open the second occurrence", Runs: []acceptance.Run{{
			Version: 1, Result: "pass", Candidate: commit, Evidence: "CI/citation-occurrences",
		}}}},
		Implementations: []acceptance.Implementation{{Version: 1, Repository: "team/reader", Branch: "dev", Commit: commit}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	run := workmethod.Run{State: "working", Revision: 1, Method: &workmethod.Template{
		ID: "project-skill-review", Name: "Verify citation workflow", Kind: "development", Review: "Independent verification",
		Steps: []workmethod.Step{{Name: "Delivery", Gate: "delivery", Output: "Verified retrieval",
			Owner: &workmethod.Actor{Kind: "member", ID: testUserID}, Reviewer: &workmethod.Actor{Kind: "agent", ID: agent},
			Checks: []string{"Open both occurrences"},
		}},
	}}
	method, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	list := func(user, project string, want int) projectSkillsView {
		t.Helper()
		req := withURLParam(newRequestAs(user, "GET", "/api/projects/"+project+"/skills", nil), "id", project)
		result := testutil.Call(t, h.ListProjectSkills, req).Want(want)
		var view projectSkillsView
		if want == http.StatusOK {
			result.JSON(&view)
		}
		return view
	}
	lesson := workmethod.ProjectLesson{
		Name: "verify-citation-offsets", When: "When one source contains repeated quotation text",
		Steps:       []string{"Open the second occurrence from the citation link."},
		Verify:      "The selected source position matches the second occurrence.",
		Avoid:       "Matching just the string opens the first hit.",
		CriterionID: "C1", CaseID: "T1",
	}
	workspaceSkill := createVersionTestSkill(t, lesson.Name, nil)
	dbfx.InsertNoID(t, "agent_skill", testutil.Cols{"agent_id": agent, "skill_id": workspaceSkill.ID}, "skill_id = $1", workspaceSkill.ID)
	submit := func(project string, lesson workmethod.ProjectLesson) (string, int) {
		t.Helper()
		id := dbfx.Issue(t, "Check citation delivery", testutil.Cols{
			"project_id": project, "status": "in_progress", "acceptance_criteria": trace, "work_method_run": method,
		})
		url := "/api/issues/" + id + "/work-method"
		var review workmethod.Run
		testutil.Call(t, h.ChangeIssueWorkMethod, withURLParam(newRequest("POST", url, map[string]any{
			"action": "advance", "base_revision": 1, "evidence": "Evidence attached for both positions",
		}), "id", id)).Want(http.StatusOK).JSON(&review)
		task := methodStageTask(t, id, review.Revision)
		if brief := claimBriefForTest(t, id, task); !strings.Contains(brief, "project_lesson") || !strings.Contains(brief, "reproducible evidence") {
			t.Fatalf("final reviewer did not get the learning prompt: %s", brief)
		}
		change := func(action string, lesson workmethod.ProjectLesson) *http.Request {
			req := withURLParam(newRequest("POST", url, map[string]any{
				"action": action, "base_revision": review.Revision, "evidence": "Independently opened both positions",
				"checked_items": []int{0}, "project_lesson": lesson,
			}), "id", id)
			req.Header.Set("X-Agent-ID", agent)
			req.Header.Set("X-Task-ID", uuidToString(task.ID))
			return req
		}
		wrongCase := lesson
		wrongCase.CaseID = "other-case"
		testutil.Call(t, h.ChangeIssueWorkMethod, change("approve", wrongCase)).Want(http.StatusBadRequest)
		testutil.Call(t, h.ChangeIssueWorkMethod, change("rework", lesson)).Want(http.StatusBadRequest)
		var done workmethod.Run
		testutil.Call(t, h.ChangeIssueWorkMethod, change("approve", lesson)).Want(http.StatusOK).JSON(&done)
		if done.State != "closed" {
			t.Fatalf("review did not close: %+v", done)
		}
		testutil.Call(t, h.ChangeIssueWorkMethod, change("approve", lesson)).Want(http.StatusConflict)
		return id, done.Revision
	}
	decide := func(user, project, issue string, revision int, decision string, want int) {
		t.Helper()
		req := withURLParams(newRequestAs(user, "PUT", "/api/projects/"+project+"/skill-proposals/"+issue, map[string]string{"decision": decision}),
			"id", project, "issueId", issue, "revision", fmt.Sprint(revision))
		testutil.Call(t, h.DecideProjectSkill, req).Want(want)
	}
	id, revision := submit(project, lesson)
	pending := list(testUserID, project, http.StatusOK)
	if len(pending.Skills) != 0 || len(pending.Proposals) != 1 || pending.Proposals[0].State != "pending" || pending.Proposals[0].Candidate != commit {
		t.Fatalf("accepted review did not yield exactly one pending skill: %+v", pending)
	}
	list(outsider, project, http.StatusNotFound)
	decide(outsider, project, id, revision, "publish", http.StatusNotFound)
	decide(testUserID, otherProject, id, revision, "publish", http.StatusNotFound)
	decide(testUserID, project, id, revision, "publish", http.StatusNoContent)
	decide(testUserID, project, id, revision, "publish", http.StatusNoContent)
	first := list(testUserID, project, http.StatusOK)
	if len(first.Skills) != 1 || first.Skills[0].Name != lesson.Name || first.Proposals[0].SkillVersion == nil || *first.Proposals[0].SkillVersion != 1 {
		t.Fatalf("expected skill version 1 from approved proposal: %+v", first)
	}
	skillID := first.Skills[0].ID
	dbfx.Cleanup(t, "DELETE FROM skill WHERE id=$1", skillID)
	dbfx.Cleanup(t, "DELETE FROM skill_version WHERE skill_id=$1", skillID)
	dbfx.Cleanup(t, "DELETE FROM skill_run_observation WHERE skill_id=$1", skillID)
	labelID := dbfx.Insert(t, "issue_label", testutil.Cols{
		"workspace_id": testWorkspaceID, "resource_type": "skill", "name": "Scoped experience",
		"color": "#3b82f6",
	})
	labelRequest := withURLParam(newRequest("POST", "/api/skills/"+skillID+"/labels", map[string]string{"label_id": labelID}), "id", skillID)
	testutil.Call(t, h.AttachLabelToSkill, labelRequest).Want(http.StatusOK)
	dbfx.Cleanup(t, "DELETE FROM skill_to_label WHERE skill_id=$1", skillID)
	for user, want := range map[string]int64{testUserID: 1, outsider: 0} {
		var labels struct{ Labels []LabelResponse }
		testutil.Call(t, h.ListLabels, newRequestAs(user, "GET", "/api/labels?resource_type=skill", nil)).Want(http.StatusOK).JSON(&labels)
		if !slices.ContainsFunc(labels.Labels, func(label LabelResponse) bool {
			return label.ID == labelID && label.UsageCount == want
		}) {
			t.Fatal("label usage count did not follow private project visibility")
		}
	}

	lesson.Verify += " Compare the document revision as well."
	secondID, secondRevision := submit(project, lesson)
	decide(testUserID, project, secondID, secondRevision, "publish", http.StatusNoContent)
	second := list(testUserID, project, http.StatusOK)
	if len(second.Skills) != 1 || second.Skills[0].ID != skillID || second.Proposals[0].SkillVersion == nil || *second.Proposals[0].SkillVersion != 2 {
		t.Fatalf("same-name proposal did not advance the existing project skill: %+v", second)
	}
	otherID, otherRevision := submit(otherProject, lesson)
	decide(testUserID, otherProject, otherID, otherRevision, "publish", http.StatusNoContent)
	other := list(testUserID, otherProject, http.StatusOK)
	if len(other.Skills) != 1 || other.Skills[0].Name != lesson.Name || other.Skills[0].ID == skillID {
		t.Fatalf("another project did not get its own skill with the same name: %+v", other)
	}
	otherSkillID := other.Skills[0].ID
	dbfx.Cleanup(t, "DELETE FROM skill WHERE id=$1", otherSkillID)
	dbfx.Cleanup(t, "DELETE FROM skill_version WHERE skill_id=$1", otherSkillID)
	dbfx.Cleanup(t, "DELETE FROM project_skill_proposal WHERE project_id=$1", otherProject)
	req := withURLParam(newRequestAs(outsider, "GET", "/api/skills/"+skillID, nil), "id", skillID)
	testutil.Call(t, h.GetSkill, req).Want(http.StatusNotFound)
	var workspaceSkills []SkillSummaryResponse
	testutil.Call(t, h.ListSkills, newRequest("GET", "/api/skills", nil)).Want(http.StatusOK).JSON(&workspaceSkills)
	if slices.ContainsFunc(workspaceSkills, func(s SkillSummaryResponse) bool { return s.ID == skillID }) {
		t.Fatal("project skill appeared in the workspace catalog")
	}
	if !slices.ContainsFunc(workspaceSkills, func(s SkillSummaryResponse) bool { return s.ID == workspaceSkill.ID && s.Name == lesson.Name }) {
		t.Fatal("the workspace skill's independent name was lost")
	}
	for _, item := range []struct {
		project, originator string
		allowed             bool
	}{
		{project, testUserID, true}, {otherProject, testUserID, false}, {project, outsider, false},
	} {
		issue := dbfx.Issue(t, "Later task", testutil.Cols{"project_id": item.project})
		taskID := dbfx.Task(t, agent, testutil.Cols{
			"issue_id": issue, "runtime_id": testRuntimeID, "status": "dispatched",
			"dispatched_at": testutil.Raw("now()"), "originator_user_id": item.originator, "accountable_user_id": item.originator,
		})
		task, err := h.Queries.GetAgentTask(t.Context(), parseUUID(taskID))
		if err != nil {
			t.Fatal(err)
		}
		runtime, err := h.Queries.GetAgentRuntime(t.Context(), task.RuntimeID)
		if err != nil {
			t.Fatal(err)
		}
		claim, _, _, _, _, failure := h.buildClaimedTaskResponse(newOwnerDaemonRequest("POST", "/claim", nil), &task, runtime, testRuntimeID, testWorkspaceID)
		if failure != nil {
			t.Fatalf("failed to assemble project task: %+v", failure)
		}
		if got := slices.ContainsFunc(claim.Agent.Skills, func(s service.AgentSkillData) bool { return s.ID == skillID }); got != item.allowed {
			t.Fatalf("project skill in task payload = %v, want %v for %+v", got, item.allowed, item)
		}
		if !item.allowed {
			resolveBundles(t, h, testRuntimeID, taskID, workspaceRef(skillID)).Want(http.StatusNotFound)
			continue
		}
		var bundle struct{ Bundles []service.AgentSkillData }
		resolveBundles(t, h, testRuntimeID, taskID, workspaceRef(skillID)).Want(http.StatusOK).JSON(&bundle)
		if len(bundle.Bundles) != 1 || !strings.Contains(bundle.Bundles[0].Content, lesson.Verify) {
			t.Fatalf("later task did not resolve the new version: %+v", bundle)
		}
		released := bundle.Bundles[0]
		hash, err := skillpkg.VersionHash(released.Name, released.Description, released.Content, []skillpkg.VersionFile{})
		if err != nil {
			t.Fatal(err)
		}
		startSkillActivityRun(t, task, []protocol.PreparedSkill{{ID: skillID, ContentHash: hash}}, http.StatusOK)
	}
	var activity skillActivityResponse
	req = withURLParam(newRequest("GET", "/api/skills/"+skillID+"/activity?days=30", nil), "id", skillID)
	testutil.Call(t, h.GetSkillActivity, req).Want(http.StatusOK).JSON(&activity)
	if activity.Counts.Available != 1 || activity.Counts.Adopted != 0 || len(activity.Runs) != 1 ||
		activity.Runs[0].Version == nil || *activity.Runs[0].Version != 2 {
		t.Fatalf("project skill usage did not preserve the supplied version: %+v", activity)
	}
	req = withURLParam(newRequest("DELETE", "/api/projects/"+project, nil), "id", project)
	testutil.Call(t, h.DeleteProject, req).Want(http.StatusNoContent)
	req = withURLParam(newRequest("GET", "/api/skills/"+skillID, nil), "id", skillID)
	testutil.Call(t, h.GetSkill, req).Want(http.StatusNotFound)
}
