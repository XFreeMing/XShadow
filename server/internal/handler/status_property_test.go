package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestStatusPropertyUsesCatalogAndPreservesArchivedValues(t *testing.T) {
	suffix := uuid.NewString()[:8]
	entry := createTestCustomStatus(t, "phase_"+suffix, issuestatus.CategoryDone)
	property := createTestProperty(t, map[string]any{"name": "Verification " + suffix, "type": "status"})
	if len(property.Config.Options) != 0 {
		t.Fatalf("status property stored local options: %#v", property.Config)
	}
	testutil.Call(t, testHandler.CreateProperty, newRequest("POST", "/api/properties", map[string]any{
		"name": "Copied enum " + suffix, "type": "status",
		"config": map[string]any{"options": []map[string]string{{"name": "Local option", "color": "#123456"}}},
	})).Want(http.StatusBadRequest)

	typeWithDefault := createTestIssueType(t, map[string]any{
		"name":   "Default phase " + suffix,
		"fields": []map[string]any{{"property_id": property.ID, "default_value": entry.Key}},
	})
	if status := archiveStatusVia(t, entry); status != http.StatusConflict {
		t.Fatalf("archive a template default returned %d", status)
	}

	issue := createIssueWithPropertyBody(t, map[string]any{
		"title": "Review handoff " + suffix, "status": "todo", "issue_type_id": typeWithDefault.ID,
	})
	if issue.Status != "todo" || issue.Properties[property.ID] != entry.Key {
		t.Fatalf("task status %s, status field %v", issue.Status, issue.Properties[property.ID])
	}

	if w := setIssuePropertyRaw(t, issue.ID, property.ID, "missing_phase"); w.Code != http.StatusBadRequest {
		t.Fatalf("unknown status accepted: %d %s", w.Code, w.Body)
	}
	testutil.Call(t, testHandler.UpdateIssueStatus, withURLParam(newRequest("PATCH", "/api/issue-statuses/"+uuidToString(entry.ID),
		map[string]any{"name": "Renamed phase", "color": "#345678"}), "id", uuidToString(entry.ID))).Want(http.StatusOK)
	testutil.Call(t, testHandler.UpdateIssueType, withURLParam(newRequest("PATCH", "/api/issue-types/"+typeWithDefault.ID,
		map[string]any{"fields": []map[string]any{{"property_id": property.ID}}}), "id", typeWithDefault.ID)).Want(http.StatusOK)
	if status := archiveStatusVia(t, entry); status != http.StatusOK {
		t.Fatalf("archive a historical property value returned %d", status)
	}
	current, err := testHandler.Queries.GetIssue(context.Background(), parseUUID(issue.ID))
	if err != nil {
		t.Fatal(err)
	}
	var values map[string]string
	if err := json.Unmarshal(current.Properties, &values); err != nil || values[property.ID] != entry.Key || current.Status != "todo" {
		t.Fatalf("rename/archive changed stored values: status=%s, properties=%s, err=%v", current.Status, current.Properties, err)
	}
	if w := setIssuePropertyRaw(t, issue.ID, property.ID, entry.Key); w.Code != http.StatusBadRequest {
		t.Fatalf("archived status accepted: %d %s", w.Code, w.Body)
	}
	condition, err := json.Marshal(service.WakeupCondition{
		Type: "issue_field", Field: "property", PropertyID: property.ID, Value: json.RawMessage(`"done"`),
	})
	if err != nil {
		t.Fatal(err)
	}
	rule := createRule(t, issue.ID, handlerTestAgentID(t), pgtype.UUID{}, service.WakeupInput{
		Kind: "event", Mode: "once", Instruction: "Verify the field change", Condition: condition,
	})
	t.Cleanup(func() { cleanupChildDoneIssue(issue.ID) })
	if w := setIssuePropertyRaw(t, issue.ID, property.ID, "done"); w.Code != http.StatusOK {
		t.Fatalf("active built-in status rejected: %d %s", w.Code, w.Body)
	}
	wakeups := service.IssueWakeupService{Tasks: testHandler.TaskService}
	if err := wakeups.TickWorkspaces(context.Background(), parseUUID(testWorkspaceID)); err != nil {
		t.Fatal(err)
	}
	if runs := wakeupRunsOf(t, rule); runs != 1 {
		t.Fatalf("status field produced %d wakeups, want 1", runs)
	}
}
