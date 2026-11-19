package handler

import (
	"net/http"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestCreateWorkMethodHandoffMatchesDraftPreview(t *testing.T) {
	seedTestCatalog(t)
	resetDefaultWorkflow(t)
	agent := seededReadyAgentID(t)
	project := createWorkflowTestProject(t, "Mixed methods")
	setColumnHandoff(t, project, "todo", map[string]any{
		"handler":      map[string]any{"type": "agent", "id": agent},
		"instructions": "Deliver the implementation.",
	}).Want(http.StatusOK)
	for _, tc := range []struct {
		title, description  string
		assigned, startsRun bool
	}{
		{"调研客户反馈", "", false, false},
		{"复盘季度计划", "交付物：OKR 关键结果评分", false, false},
		{"开发复盘面板", "", false, true},
		{"调研客户反馈", "", true, true},
	} {
		t.Run(tc.title, func(t *testing.T) {
			draft := map[string]any{"project_id": project, "title": tc.title, "description": tc.description, "is_create": true}
			if tc.assigned {
				draft["assignee_type"] = "agent"
				draft["assignee_id"] = agent
			}
			preview := previewIssueTrigger(t, draft)
			delete(draft, "is_create")
			issue := createIssueForTest(t, draft)
			if preview.ProjectHandoffSuppressed == tc.startsRun || (preview.TotalCount > 0) != tc.startsRun || (issue.AssigneeID != nil) != tc.startsRun || (taskCountFor(t, issue.ID, agent) > 0) != tc.startsRun {
				t.Fatalf("startsRun=%v preview=%+v assignee=%v", tc.startsRun, preview, issue.AssigneeID)
			}
		})
	}

	named := createWorkflowTestProject(t, "Limited workflow")
	flow := createTestWorkflow(t, "Limited "+workflowTestSuffix())
	setProjectWorkflow(t, named, map[string]any{"workflow_id": flow.ID}).Want(http.StatusOK)
	testutil.Call(t, testHandler.CreateIssue, newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"project_id": named, "title": "调研用户反馈", "status": "in_progress",
	})).Want(http.StatusBadRequest)
}
