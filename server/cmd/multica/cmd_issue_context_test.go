package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

const (
	testContextIssueID  = "8de8b4d4-6f8a-4d4e-9a19-3f3a0f7c9d21"
	testContextGoalID   = "b15074ee-5922-4059-bdf5-9d557a9cdadb"
	testContextOtherID  = "0d9a4d6e-1a40-4c99-9b7c-2f5b8f2c1e33"
	testContextProject  = "75cf9eae-304a-44a7-939d-0ad6825213b1"
	testContextOldGoal  = "11111111-2222-4333-8444-555555555555"
	testContextPlanItem = "plan-item-1"
)

func newIssueContextTestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "context"}
	cmd.Flags().String("output", issueContextJSONOutputFormat, "")
	return cmd
}

type contextTestServer struct {
	t        *testing.T
	mux      *http.ServeMux
	requests []string
}

func newContextTestServer(t *testing.T) *contextTestServer {
	t.Helper()
	s := &contextTestServer{t: t, mux: http.NewServeMux()}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.requests = append(s.requests, r.Method+" "+r.URL.RequestURI())
		s.mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	prepareGoalCLITest(t, srv.URL)
	return s
}

func (s *contextTestServer) handle(path string, fn func(w http.ResponseWriter, r *http.Request)) {
	s.mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			s.t.Errorf("method = %s, want GET for %s", r.Method, r.URL.Path)
		}
		fn(w, r)
	})
}

func (s *contextTestServer) json(path string, value any) {
	s.handle(path, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(value)
	})
}

// goals serves one page of goals and an empty page for any later offset, the
// way the server terminates a full pagination walk.
func (s *contextTestServer) goals(goals ...map[string]any) {
	if goals == nil {
		goals = []map[string]any{}
	}
	s.handle("/api/goals", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("offset") != "" {
			_ = json.NewEncoder(w).Encode(map[string]any{"goals": []any{}, "total": len(goals)})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"goals": goals, "total": len(goals)})
	})
}

// linkedIssues serves one page of linked issues and an empty page afterwards.
func (s *contextTestServer) linkedIssues(goalID string, ids ...string) {
	if ids == nil {
		ids = []string{}
	}
	s.handle("/api/goals/"+goalID+"/issues", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("offset") != "" {
			_ = json.NewEncoder(w).Encode(map[string]any{"issues": []any{}, "total": len(ids)})
			return
		}
		_ = json.NewEncoder(w).Encode(linkedGoalBody(ids...))
	})
}

func contextGoalBody(projectID any) map[string]any {
	return map[string]any{
		"id": testContextGoalID, "title": "目标", "status": "active",
		"intent": "意图", "scope": "范围",
		"acceptance_criteria": "目标验收",
		"project_id":          projectID, "updated_at": "2026-11-18T07:00:00Z",
	}
}

func (s *contextTestServer) run(t *testing.T, ref string) (issueContextPayload, error) {
	t.Helper()
	cmd := newIssueContextTestCmd()
	var stdout, stderr strings.Builder
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	err := runIssueContext(cmd, []string{ref})
	if err != nil {
		return issueContextPayload{}, err
	}
	var payload issueContextPayload
	if err := json.Unmarshal([]byte(stdout.String()), &payload); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout.String())
	}
	return payload, nil
}

func contextIssueBody(projectID any, description string, revision int64) map[string]any {
	return map[string]any{
		"id":          testContextIssueID,
		"identifier":  "DEV-1",
		"number":      1,
		"title":       "任务标题不应被当作验收条件",
		"status":      "todo",
		"project_id":  projectID,
		"revision":    revision,
		"updated_at":  "2026-11-18T08:00:00Z",
		"description": description,
	}
}

func linkedGoalBody(issueIDs ...string) map[string]any {
	issues := make([]map[string]any, 0, len(issueIDs))
	for _, id := range issueIDs {
		issues = append(issues, map[string]any{"id": id, "identifier": "DEV-X", "title": "another task"})
	}
	return map[string]any{"issues": issues, "total": len(issues)}
}

const realDailyStoryBody = `作为知识所有者，我希望取得可复现的开源模块组合、真实串联证据和架构方案，以便优先复用现有项目、保留旧资料并开始体验首个增量。

## 目标

沿用个人第二大脑目标。

## 范围与可观察验收

本故事完成后，用户能看到并运行以下结果：

1. 用户能打开模块清单并看到来源、版本与许可证。
2. 用户能按清单重现一次串联运行并取得日志。
3. 用户能打开架构方案，指出旧资料的去留。

## 交付验收与当前状态

交付负责人提供逐条验收证据。
当前为 backlog，未派发。

## 目标归属

本故事归属建立可追溯、可维护并供军团复用的个人第二大脑。
`

func TestParseStorySectionsReadsRealDailyAcceptanceHeading(t *testing.T) {
	story := buildStoryContext(realDailyStoryBody, 57)
	if story.Acceptance.State != "found" {
		t.Fatalf("acceptance state = %s (%s), want found", story.Acceptance.State, story.Acceptance.Reason)
	}
	if len(story.Acceptance.Items) != 3 {
		t.Fatalf("items = %+v, want the three numbered results only", story.Acceptance.Items)
	}
	if got := story.Acceptance.Items[0].Text; !strings.Contains(got, "模块清单") {
		t.Errorf("first item = %q", got)
	}
	// The lead paragraph before the list is scope context, not an item.
	for _, item := range story.Acceptance.Items {
		if strings.Contains(item.Text, "本故事完成后") {
			t.Errorf("lead paragraph was read as an acceptance item: %q", item.Text)
		}
	}
	if len(story.Acceptance.Sections) != 1 || story.Acceptance.Sections[0].Heading != "范围与可观察验收" {
		t.Errorf("sections = %+v", story.Acceptance.Sections)
	}
	if story.Statement == nil || !strings.Contains(story.Statement.Text, "作为知识所有者") {
		t.Fatalf("statement = %+v", story.Statement)
	}
	if story.Statement.Basis != "story_statement" {
		t.Errorf("statement basis = %s", story.Statement.Basis)
	}
}

func TestClassifyStorySectionAcceptsTemplateHeadings(t *testing.T) {
	cases := map[string]string{
		"范围与可观察验收":            storySectionAcceptance,
		"可观察验收":               storySectionAcceptance,
		"验收":                  storySectionAcceptance,
		"手动验收":                storySectionAcceptance,
		"验收结果":                storySectionAcceptance,
		"Acceptance criteria": storySectionAcceptance,
		"对目标的贡献":              storySectionContribution,
		"Contribution":        storySectionContribution,
		"交付物":                 storySectionResult,
		"背景":                  storySectionOther,
		"标题":                  storySectionOther,
	}
	for heading, want := range cases {
		if got := classifyStorySection(heading); got != want {
			t.Errorf("classifyStorySection(%q) = %s, want %s", heading, got, want)
		}
	}
}

func TestParseStoryInlineAcceptanceLabels(t *testing.T) {
	body := `作为维护者，我希望只读地查看任务上下文，以便避免错误归属。

- 交付物：` + "`multica issue context`" + `
- 可观察验收：运行命令后，输出的目标层与故事层分开列出。
- 本故事验收条件：没有关联目标时，有效目标是当前故事。

## 备注

- 该备注不是验收条件。
`
	story := buildStoryContext(body, 4)
	if story.Acceptance.State != "found" {
		t.Fatalf("acceptance = %+v", story.Acceptance)
	}
	if len(story.Acceptance.Items) != 2 {
		t.Fatalf("items = %+v, want the two inline acceptance items", story.Acceptance.Items)
	}
	if strings.Contains(story.Acceptance.Items[0].Text, "交付物") {
		t.Errorf("a result label was read as acceptance: %q", story.Acceptance.Items[0].Text)
	}
}

func TestParseStoryIgnoresFencedCode(t *testing.T) {
	body := "## 验收条件\n\n1. 用户能运行命令。\n\n```bash\nmultica issue context DEV-1\n2. 这不是验收条目\n```\n"
	story := buildStoryContext(body, 1)
	if len(story.Acceptance.Items) != 1 {
		t.Fatalf("items = %+v, want only the item outside the fence", story.Acceptance.Items)
	}
}

func TestBuildStoryContextEmptyBodyIsHonest(t *testing.T) {
	story := buildStoryContext("", 0)
	if story.Body.State != "empty" {
		t.Errorf("body state = %s", story.Body.State)
	}
	if story.Acceptance.State != "insufficient" || !strings.Contains(story.Acceptance.Reason, "empty") {
		t.Errorf("acceptance = %+v", story.Acceptance)
	}
	if story.Result.State != "unavailable" || story.Contribution.State != "unavailable" {
		t.Errorf("result/contribution = %+v / %+v", story.Result, story.Contribution)
	}
}

func TestIssueContextLinkedGoalUsesPlanOutcomeAsContribution(t *testing.T) {
	s := newContextTestServer(t)
	s.json("/api/issues/DEV-1", contextIssueBody(testContextProject, realDailyStoryBody, 57))
	s.goals(map[string]any{
		"id": testContextGoalID, "title": "建立个人第二大脑", "status": "active",
		"intent": "复用现有项目", "scope": "模块、证据与方案",
		"acceptance_criteria": "整体目标验收：75% 复用率",
		"project_id":          testContextProject, "updated_at": "2026-11-18T07:00:00Z",
	})
	s.json("/api/goals/"+testContextGoalID+"/issues", linkedGoalBody(testContextIssueID))
	s.json("/api/goals/"+testContextGoalID, map[string]any{
		"id": testContextGoalID, "title": "建立个人第二大脑", "status": "active",
		"intent": "复用现有项目", "scope": "模块、证据与方案",
		"acceptance_criteria": "整体目标验收：75% 复用率",
		"project_id":          testContextProject, "updated_at": "2026-11-18T07:00:00Z",
	})
	s.json("/api/goals/"+testContextGoalID+"/plan", map[string]any{
		"goal_id": testContextGoalID, "revision": 12, "state": "draft",
		"items": []map[string]any{
			{"id": testContextPlanItem, "title": "首个增量", "outcome": "用户能打开并运行首个增量，看到来源与日志", "binding": map[string]any{"type": "existing", "issue_id": testContextIssueID}},
			{"id": "plan-item-2", "title": "其他节点", "outcome": "其他目标的贡献", "binding": map[string]any{"type": "existing", "issue_id": testContextOtherID}},
		},
		"edges": []any{},
	})
	s.json("/api/issues/"+testContextIssueID+"/comments", []any{})

	payload, err := s.run(t, "DEV-1")
	if err != nil {
		t.Fatalf("runIssueContext: %v", err)
	}
	if payload.EffectiveGoal.Layer != "goal" || payload.EffectiveGoal.ID != testContextGoalID {
		t.Errorf("effective goal = %+v", payload.EffectiveGoal)
	}
	contribution := payload.Story.Contribution
	if contribution.Basis != "goal_plan_outcome" || !strings.Contains(contribution.Text, "首个增量") {
		t.Fatalf("contribution = %+v", contribution)
	}
	if !strings.Contains(contribution.Source, testContextPlanItem) || !strings.Contains(contribution.Source, "12") {
		t.Errorf("contribution source = %q", contribution.Source)
	}
	if strings.Contains(contribution.Text, "其他目标") {
		t.Errorf("another plan item leaked into the contribution: %q", contribution.Text)
	}
	// Goal and story criteria stay separate.
	if !strings.Contains(payload.Goal.AcceptanceCriteria, "整体目标验收") {
		t.Errorf("goal acceptance = %q", payload.Goal.AcceptanceCriteria)
	}
	if len(payload.Story.Acceptance.Items) != 3 {
		t.Errorf("story acceptance = %+v", payload.Story.Acceptance.Items)
	}
}

func TestIssueContextFallsBackToStoryStatement(t *testing.T) {
	s := newContextTestServer(t)
	s.json("/api/issues/DEV-1", contextIssueBody(testContextProject, realDailyStoryBody, 57))
	s.goals(map[string]any{
		"id": testContextGoalID, "title": "目标", "project_id": testContextProject,
	})
	s.json("/api/goals/"+testContextGoalID+"/issues", linkedGoalBody(testContextIssueID))
	s.json("/api/goals/"+testContextGoalID, map[string]any{
		"id": testContextGoalID, "title": "目标", "project_id": testContextProject,
		"acceptance_criteria": "目标验收",
	})
	s.json("/api/goals/"+testContextGoalID+"/plan", map[string]any{
		"goal_id": testContextGoalID, "revision": 0, "state": "draft", "items": []any{},
	})
	s.json("/api/issues/"+testContextIssueID+"/comments", []any{})

	payload, err := s.run(t, "DEV-1")
	if err != nil {
		t.Fatalf("runIssueContext: %v", err)
	}
	if payload.Story.Contribution.Basis != "story_statement" {
		t.Fatalf("contribution = %+v", payload.Story.Contribution)
	}
	if !strings.Contains(payload.Story.Contribution.Text, "作为知识所有者") {
		t.Errorf("contribution text = %q", payload.Story.Contribution.Text)
	}
}

func TestIssueContextUnlinkedTaskUsesStoryAsEffectiveGoal(t *testing.T) {
	s := newContextTestServer(t)
	s.json("/api/issues/DEV-1", contextIssueBody(testContextProject, realDailyStoryBody, 8))
	s.goals(map[string]any{
		"id": testContextGoalID, "title": "同项目其他目标", "project_id": testContextProject,
	})
	s.linkedIssues(testContextGoalID, testContextOtherID)
	s.json("/api/issues/"+testContextIssueID+"/comments", []any{})

	payload, err := s.run(t, "DEV-1")
	if err != nil {
		t.Fatalf("runIssueContext: %v", err)
	}
	if payload.Goal.State != "none" || payload.Goal.Reason != "not_linked_to_any_goal" {
		t.Fatalf("goal = %+v", payload.Goal)
	}
	if payload.EffectiveGoal.Layer != "story" || payload.EffectiveGoal.Basis != "story_self" {
		t.Fatalf("effective goal = %+v", payload.EffectiveGoal)
	}
	if payload.EffectiveGoal.Title != "任务标题不应被当作验收条件" || payload.EffectiveGoal.Revision != 8 {
		t.Errorf("effective goal should be the story itself: %+v", payload.EffectiveGoal)
	}
	if payload.Story.Contribution.State != "not_applicable" {
		t.Errorf("contribution = %+v", payload.Story.Contribution)
	}
}

func TestIssueContextNoVisibleGoal(t *testing.T) {
	s := newContextTestServer(t)
	s.json("/api/issues/DEV-1", contextIssueBody(testContextProject, realDailyStoryBody, 8))
	s.goals()
	s.json("/api/issues/"+testContextIssueID+"/comments", []any{})

	payload, err := s.run(t, "DEV-1")
	if err != nil {
		t.Fatalf("runIssueContext: %v", err)
	}
	if payload.Goal.Reason != "no_visible_goal_in_project" {
		t.Errorf("goal = %+v", payload.Goal)
	}
	if payload.EffectiveGoal.Layer != "story" {
		t.Errorf("effective goal = %+v", payload.EffectiveGoal)
	}
}

func TestIssueContextStandaloneTaskFiltersStandaloneGoals(t *testing.T) {
	var projectFilter string
	s := newContextTestServer(t)
	s.handle("/api/goals", func(w http.ResponseWriter, r *http.Request) {
		projectFilter = r.URL.Query().Get("project_id")
		_ = json.NewEncoder(w).Encode(map[string]any{"goals": []any{}, "total": 0})
	})
	s.json("/api/issues/DEV-1", contextIssueBody(nil, realDailyStoryBody, 8))
	s.json("/api/issues/"+testContextIssueID+"/comments", []any{})

	if _, err := s.run(t, "DEV-1"); err != nil {
		t.Fatalf("runIssueContext: %v", err)
	}
	if projectFilter != "none" {
		t.Errorf("project filter = %q, want none for an explicit null project_id", projectFilter)
	}
}

func TestIssueContextRejectsMissingProjectField(t *testing.T) {
	s := newContextTestServer(t)
	body := contextIssueBody(testContextProject, realDailyStoryBody, 8)
	delete(body, "project_id")
	s.json("/api/issues/DEV-1", body)

	if _, err := s.run(t, "DEV-1"); err == nil || !strings.Contains(err.Error(), "project_id") {
		t.Fatalf("err = %v, want a missing project_id error", err)
	}
}

func TestIssueContextFailsClosedOnNullCollections(t *testing.T) {
	cases := []struct {
		name  string
		setup func(s *contextTestServer)
		want  string
	}{
		{
			name: "null goals",
			setup: func(s *contextTestServer) {
				s.json("/api/issues/DEV-1", contextIssueBody(testContextProject, realDailyStoryBody, 8))
				s.json("/api/issues/"+testContextIssueID+"/comments", []any{})
				s.handle("/api/goals", func(w http.ResponseWriter, r *http.Request) {
					_, _ = w.Write([]byte(`{"goals":null,"total":0}`))
				})
			},
			want: "null instead of an array",
		},
		{
			name: "missing goals field",
			setup: func(s *contextTestServer) {
				s.json("/api/issues/DEV-1", contextIssueBody(testContextProject, realDailyStoryBody, 8))
				s.json("/api/issues/"+testContextIssueID+"/comments", []any{})
				s.handle("/api/goals", func(w http.ResponseWriter, r *http.Request) {
					_, _ = w.Write([]byte(`{"total":0}`))
				})
			},
			want: "no goals array",
		},
		{
			name: "null issues",
			setup: func(s *contextTestServer) {
				s.json("/api/issues/DEV-1", contextIssueBody(testContextProject, realDailyStoryBody, 8))
				s.json("/api/issues/"+testContextIssueID+"/comments", []any{})
				s.goals(map[string]any{
					"id": testContextGoalID, "title": "目标", "project_id": testContextProject,
				})
				s.handle("/api/goals/"+testContextGoalID+"/issues", func(w http.ResponseWriter, r *http.Request) {
					_, _ = w.Write([]byte(`{"issues":null,"total":0}`))
				})
			},
			want: "issues",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newContextTestServer(t)
			tc.setup(s)
			_, err := s.run(t, "DEV-1")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestIssueContextAbortsOnRepeatedPages(t *testing.T) {
	t.Run("goals", func(t *testing.T) {
		s := newContextTestServer(t)
		s.json("/api/issues/DEV-1", contextIssueBody(testContextProject, realDailyStoryBody, 8))
		s.json("/api/issues/"+testContextIssueID+"/comments", []any{})
		s.handle("/api/goals", func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"goals": []map[string]any{{
				"id": testContextGoalID, "title": "目标", "project_id": testContextProject,
			}}, "total": 2})
		})
		_, err := s.run(t, "DEV-1")
		if err == nil || !strings.Contains(err.Error(), "same goals page") {
			t.Fatalf("err = %v, want a repeated-page error", err)
		}
	})

	t.Run("goal issues", func(t *testing.T) {
		s := newContextTestServer(t)
		s.json("/api/issues/DEV-1", contextIssueBody(testContextProject, realDailyStoryBody, 8))
		s.json("/api/issues/"+testContextIssueID+"/comments", []any{})
		s.goals(map[string]any{
			"id": testContextGoalID, "title": "目标", "project_id": testContextProject,
		})
		s.handle("/api/goals/"+testContextGoalID+"/issues", func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(linkedGoalBody(testContextOtherID))
		})
		_, err := s.run(t, "DEV-1")
		if err == nil || !strings.Contains(err.Error(), "same issues page") {
			t.Fatalf("err = %v, want a repeated-page error", err)
		}
	})
}

func TestIssueContextReadsEveryGoalPage(t *testing.T) {
	var offsets []string
	s := newContextTestServer(t)
	s.json("/api/issues/DEV-1", contextIssueBody(testContextProject, realDailyStoryBody, 8))
	s.handle("/api/goals", func(w http.ResponseWriter, r *http.Request) {
		offset := r.URL.Query().Get("offset")
		offsets = append(offsets, offset)
		switch offset {
		case "":
			_ = json.NewEncoder(w).Encode(map[string]any{"goals": []map[string]any{{
				"id": testContextOtherID, "title": "另一个目标", "project_id": testContextProject,
			}}, "total": 2})
		case "1":
			_ = json.NewEncoder(w).Encode(map[string]any{"goals": []map[string]any{{
				"id": testContextGoalID, "title": "目标", "project_id": testContextProject,
			}}, "total": 2})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"goals": []any{}, "total": 2})
		}
	})
	s.json("/api/goals/"+testContextOtherID+"/issues", linkedGoalBody())
	s.json("/api/goals/"+testContextGoalID+"/issues", linkedGoalBody(testContextIssueID))
	s.json("/api/goals/"+testContextGoalID, map[string]any{
		"id": testContextGoalID, "title": "目标", "project_id": testContextProject,
	})
	s.json("/api/goals/"+testContextGoalID+"/plan", map[string]any{
		"goal_id": testContextGoalID, "revision": 0, "state": "draft", "items": []any{},
	})
	s.json("/api/issues/"+testContextIssueID+"/comments", []any{})

	payload, err := s.run(t, "DEV-1")
	if err != nil {
		t.Fatalf("runIssueContext: %v", err)
	}
	if payload.Goal.ID != testContextGoalID || payload.Goal.Resolution.GoalsScanned != 2 {
		t.Errorf("goal = %+v", payload.Goal)
	}
	if strings.Join(offsets, ",") != ",1,2" {
		t.Errorf("offsets = %v, want the walk to end on the empty page at offset 2", offsets)
	}
}

func TestIssueContextReflectsEditAndRebindWithoutCaching(t *testing.T) {
	state := struct {
		goalID      string
		revision    int64
		description string
	}{testContextOldGoal, 3, "## 验收条件\n\n1. 旧验收。\n"}
	s := newContextTestServer(t)
	s.handle("/api/issues/DEV-1", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(contextIssueBody(testContextProject, state.description, state.revision))
	})
	s.handle("/api/goals", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("offset") != "" {
			_ = json.NewEncoder(w).Encode(map[string]any{"goals": []any{}, "total": 1})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"goals": []map[string]any{{
			"id": state.goalID, "title": "当前目标", "project_id": testContextProject,
		}}, "total": 1})
	})
	s.mux.HandleFunc("/api/goals/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/issues"):
			_ = json.NewEncoder(w).Encode(linkedGoalBody(testContextIssueID))
		case strings.HasSuffix(r.URL.Path, "/plan"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"goal_id": state.goalID, "revision": 0, "state": "draft", "items": []any{},
			})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": state.goalID, "title": "当前目标", "project_id": testContextProject,
			})
		}
	})
	s.json("/api/issues/"+testContextIssueID+"/comments", []any{})

	first, err := s.run(t, "DEV-1")
	if err != nil {
		t.Fatalf("first read: %v", err)
	}
	if first.Goal.ID != testContextOldGoal || first.Story.Acceptance.Items[0].Text != "旧验收。" {
		t.Fatalf("first read = %+v", first)
	}

	state.goalID = testContextGoalID
	state.revision = 4
	state.description = "## 验收条件\n\n1. 新验收。\n"

	second, err := s.run(t, "DEV-1")
	if err != nil {
		t.Fatalf("second read: %v", err)
	}
	if second.Goal.ID != testContextGoalID {
		t.Errorf("rebind not visible: goal = %+v", second.Goal)
	}
	if second.Story.Revision != 4 || second.Story.Acceptance.Items[0].Text != "新验收。" {
		t.Errorf("edit not visible: revision=%d items=%+v", second.Story.Revision, second.Story.Acceptance.Items)
	}
	if second.EffectiveGoal.ID != testContextGoalID {
		t.Errorf("effective goal not refreshed: %+v", second.EffectiveGoal)
	}
}

func TestIssueContextRejectsShortUUIDPrefixWithoutRequest(t *testing.T) {
	s := newContextTestServer(t)
	_, err := s.run(t, "8de8b4d4")
	if err == nil || !strings.Contains(err.Error(), "short UUID prefix") {
		t.Fatalf("err = %v", err)
	}
	if len(s.requests) != 0 {
		t.Errorf("made requests before rejecting the ref: %v", s.requests)
	}
}

func TestIssueContextPermissionAndNotFoundErrorsStayDistinct(t *testing.T) {
	cases := []struct {
		status int
		want   string
	}{
		{http.StatusForbidden, "access denied"},
		{http.StatusNotFound, "not found or not visible"},
		{http.StatusInternalServerError, "HTTP 500"},
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			s := newContextTestServer(t)
			s.handle("/api/issues/DEV-1", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"error":"server reason"}`))
			})
			_, err := s.run(t, "DEV-1")
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "server reason") {
				t.Fatalf("err = %v, want %q with the server reason", err, tc.want)
			}
		})
	}
}

func TestIssueContextGoalReadFailureIsNotNoGoal(t *testing.T) {
	s := newContextTestServer(t)
	s.json("/api/issues/DEV-1", contextIssueBody(testContextProject, realDailyStoryBody, 8))
	s.json("/api/issues/"+testContextIssueID+"/comments", []any{})
	s.goals(map[string]any{
		"id": testContextGoalID, "title": "目标", "project_id": testContextProject,
	})
	s.json("/api/goals/"+testContextGoalID+"/issues", linkedGoalBody(testContextIssueID))
	s.handle("/api/goals/"+testContextGoalID, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"detail failed"}`))
	})

	_, err := s.run(t, "DEV-1")
	if err == nil || !strings.Contains(err.Error(), "read goal") || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("err = %v, want a goal read failure", err)
	}
}

func TestIssueContextPlanReadFailureIsReported(t *testing.T) {
	s := newContextTestServer(t)
	s.json("/api/issues/DEV-1", contextIssueBody(testContextProject, realDailyStoryBody, 8))
	s.json("/api/issues/"+testContextIssueID+"/comments", []any{})
	s.goals(map[string]any{
		"id": testContextGoalID, "title": "目标", "project_id": testContextProject,
	})
	s.json("/api/goals/"+testContextGoalID+"/issues", linkedGoalBody(testContextIssueID))
	s.json("/api/goals/"+testContextGoalID, map[string]any{
		"id": testContextGoalID, "title": "目标", "project_id": testContextProject,
	})
	s.handle("/api/goals/"+testContextGoalID+"/plan", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"plan failed"}`))
	})

	_, err := s.run(t, "DEV-1")
	if err == nil || !strings.Contains(err.Error(), "plan") {
		t.Fatalf("err = %v, want a plan read failure", err)
	}
}

func TestIssueContextRefusesCrossProjectGoal(t *testing.T) {
	s := newContextTestServer(t)
	s.json("/api/issues/DEV-1", contextIssueBody(testContextProject, realDailyStoryBody, 8))
	s.json("/api/issues/"+testContextIssueID+"/comments", []any{})
	s.goals(map[string]any{
		"id": testContextGoalID, "title": "外项目目标", "project_id": testContextProject,
	})
	s.json("/api/goals/"+testContextGoalID+"/issues", linkedGoalBody(testContextIssueID))
	s.json("/api/goals/"+testContextGoalID, map[string]any{
		"id": testContextGoalID, "title": "外项目目标", "project_id": testContextOtherID,
	})

	_, err := s.run(t, "DEV-1")
	if err == nil || !strings.Contains(err.Error(), "refusing to attribute") {
		t.Fatalf("err = %v, want a cross-project refusal", err)
	}
}

func TestIssueContextInsufficientAcceptanceAndTitleIsNotCriteria(t *testing.T) {
	s := newContextTestServer(t)
	s.json("/api/issues/DEV-1", contextIssueBody(testContextProject, "背景说明，没有验收章节。", 2))
	s.json("/api/goals", map[string]any{"goals": []any{}, "total": 0})
	s.json("/api/issues/"+testContextIssueID+"/comments", []any{})

	payload, err := s.run(t, "DEV-1")
	if err != nil {
		t.Fatalf("runIssueContext: %v", err)
	}
	if payload.Story.Acceptance.State != "insufficient" || len(payload.Story.Acceptance.Items) != 0 {
		t.Fatalf("acceptance = %+v", payload.Story.Acceptance)
	}
	if !strings.Contains(payload.Story.Acceptance.Reason, "no dedicated acceptance section") {
		t.Errorf("reason = %q", payload.Story.Acceptance.Reason)
	}
	if strings.Contains(payload.Story.Body.Text, payload.Story.Title) {
		// The body never gains the title; the title is not acceptance evidence.
		t.Errorf("body unexpectedly contains the title")
	}
}

func TestIssueContextFeedbackAndMalformedResponses(t *testing.T) {
	t.Run("feedback included and sorted", func(t *testing.T) {
		s := newContextTestServer(t)
		s.json("/api/issues/DEV-1", contextIssueBody(testContextProject, realDailyStoryBody, 8))
		s.json("/api/goals", map[string]any{"goals": []any{}, "total": 0})
		s.json("/api/issues/"+testContextIssueID+"/comments", []any{
			map[string]any{"id": "c2", "created_at": "2026-11-18T09:00:00Z", "author_type": "member", "content": "第二条"},
			map[string]any{"id": "c1", "created_at": "2026-11-18T08:00:00Z", "author_type": "agent", "content": "第一条"},
		})
		payload, err := s.run(t, "DEV-1")
		if err != nil {
			t.Fatalf("runIssueContext: %v", err)
		}
		if payload.Story.Feedback.State != "included" || len(payload.Story.Feedback.Items) != 2 {
			t.Fatalf("feedback = %+v", payload.Story.Feedback)
		}
		if payload.Story.Feedback.Items[0].Content != "第一条" {
			t.Errorf("feedback order = %+v", payload.Story.Feedback.Items)
		}
	})

	t.Run("null comments", func(t *testing.T) {
		s := newContextTestServer(t)
		s.json("/api/issues/DEV-1", contextIssueBody(testContextProject, realDailyStoryBody, 8))
		s.json("/api/goals", map[string]any{"goals": []any{}, "total": 0})
		s.handle("/api/issues/"+testContextIssueID+"/comments", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`null`))
		})
		if _, err := s.run(t, "DEV-1"); err == nil || !strings.Contains(err.Error(), "comments") {
			t.Fatalf("err = %v, want a comments error", err)
		}
	})

	t.Run("malformed issue", func(t *testing.T) {
		s := newContextTestServer(t)
		s.handle("/api/issues/DEV-1", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{`))
		})
		if _, err := s.run(t, "DEV-1"); err == nil || !strings.Contains(err.Error(), "read issue") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestIssueContextTextOutputSeparatesLayers(t *testing.T) {
	s := newContextTestServer(t)
	s.json("/api/issues/DEV-1", contextIssueBody(testContextProject, realDailyStoryBody, 57))
	s.json("/api/goals", map[string]any{"goals": []any{}, "total": 0})
	s.json("/api/issues/"+testContextIssueID+"/comments", []any{})

	cmd := newIssueContextTestCmd()
	_ = cmd.Flags().Set("output", issueContextTextOutputFormat)
	var stdout strings.Builder
	cmd.SetOut(&stdout)
	cmd.SetErr(&strings.Builder{})
	if err := runIssueContext(cmd, []string{"DEV-1"}); err != nil {
		t.Fatalf("runIssueContext: %v", err)
	}
	text := stdout.String()
	for _, want := range []string{"Effective goal: story", "Goal: none", "Story acceptance (found)", "Story contribution (none)", "Story statement:"} {
		if !strings.Contains(text, want) {
			t.Errorf("text output missing %q:\n%s", want, text)
		}
	}
}
