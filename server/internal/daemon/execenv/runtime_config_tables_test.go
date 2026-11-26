package execenv

import (
	"strings"
	"testing"
)

func TestRuntimeBriefsDiscoverTablesViaPlatformSkill(t *testing.T) {
	t.Parallel()

	kinds := map[string]TaskContextForEnv{
		"issue": {
			IssueID:     "issue-1",
			AgentID:     "a-1",
			AgentName:   "Coder",
			AgentSkills: []SkillContextForEnv{platformSkillFixture()},
		},
		"issue-workflow-step": {
			IssueID:            "issue-2",
			AgentID:            "a-1",
			AgentName:          "Coder",
			StepContext:        true,
			HasProjectWorkflow: true,
			AgentSkills:        []SkillContextForEnv{platformSkillFixture()},
		},
		"chat": {
			ChatSessionID: "chat-1",
			AgentID:       "a-1",
			AgentName:     "Coder",
			AgentSkills:   []SkillContextForEnv{platformSkillFixture()},
		},
		"chat-slack": {
			ChatSessionID:   "chat-2",
			ChatChannelType: ChannelTypeSlack,
			AgentID:         "a-1",
			AgentName:       "Coder",
			AgentSkills:     []SkillContextForEnv{platformSkillFixture()},
		},
		"quick-create": {
			QuickCreatePrompt: "Create an issue from table triage",
			AgentID:           "a-1",
			AgentName:         "Coder",
			AgentSkills:       []SkillContextForEnv{platformSkillFixture()},
		},
		"autopilot-run-only": {
			AutopilotRunID: "run-1",
			AutopilotID:    "ap-1",
			AgentID:        "a-1",
			AgentName:      "Coder",
			AgentSkills:    []SkillContextForEnv{platformSkillFixture()},
		},
		"squad-leader": {
			IssueID:       "issue-3",
			IsSquadLeader: true,
			AgentID:       "a-1",
			AgentName:     "Leader",
			AgentSkills:   []SkillContextForEnv{platformSkillFixture()},
		},
	}

	providers := []string{"codex", "cursor", "kimi", "antigravity", "claude", "traecli"}

	for kindName, ctx := range kinds {
		for _, provider := range providers {
			kindName, ctx, provider := kindName, ctx, provider
			t.Run(kindName+"/"+provider, func(t *testing.T) {
				t.Parallel()
				brief := buildMetaSkillContent(provider, ctx)
				if !strings.Contains(brief, "tables") {
					t.Errorf("brief missing tables domain mention:\n%s", brief)
				}
				if provider == "traecli" {
					want := ".traecli/skills/multica-platform/references/tables.md"
					if !strings.Contains(brief, want) {
						t.Errorf("traecli brief missing explicit table reference path %q:\n%s", want, brief)
					}
					return
				}
				if !strings.Contains(brief, "references/tables.md") || !strings.Contains(brief, "`multica-platform`") {
					t.Errorf("brief missing table skill pointer (`references/tables.md` + `multica-platform`):\n%s", brief)
				}
			})
		}
	}
}

func TestRuntimeBriefTableHintFollowsBuiltinIdentityAndCollisions(t *testing.T) {
	t.Parallel()

	user := platformSkillFixture()
	user.BuiltinName = ""
	hidden := platformSkillFixture()
	hidden.Content = "---\nname: multica-platform\ndisable-model-invocation: true\n---\n\nHidden."

	for _, provider := range []string{"codex", "cursor", "kimi", "antigravity", "claude", "traecli"} {
		provider := provider
		t.Run("collision/"+provider, func(t *testing.T) {
			t.Parallel()
			ctx := TaskContextForEnv{
				IssueID:     "issue-collide",
				AgentSkills: []SkillContextForEnv{user, platformSkillFixture()},
			}
			brief := buildMetaSkillContent(provider, ctx)
			if provider == "traecli" {
				want := ".traecli/skills/multica-platform-multica/references/tables.md"
				if !strings.Contains(brief, want) {
					t.Errorf("traecli collided brief missing %q:\n%s", want, brief)
				}
			} else {
				if !strings.Contains(brief, "references/tables.md") || !strings.Contains(brief, "`multica-platform-multica`") {
					t.Errorf("collided brief missing renamed slug pointer:\n%s", brief)
				}
			}
		})

		t.Run("no-visible-builtin/"+provider, func(t *testing.T) {
			t.Parallel()
			for _, skills := range [][]SkillContextForEnv{nil, {user}, {hidden}, {user, hidden}} {
				brief := buildMetaSkillContent(provider, TaskContextForEnv{
					ChatSessionID: "chat-no-builtin",
					AgentSkills:   skills,
				})
				if strings.Contains(brief, "references/tables.md") {
					t.Errorf("brief advertised references/tables.md without visible builtin (skills=%+v):\n%s", skills, brief)
				}
			}
		})
	}
}
