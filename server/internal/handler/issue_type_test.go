package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func createTestIssueType(t *testing.T, body map[string]any) IssueTypeResponse {
	t.Helper()
	resp := testutil.Call(t, testHandler.CreateIssueType,
		newRequest(http.MethodPost, "/api/issue-types", body),
	).Want(http.StatusCreated)
	var out IssueTypeResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode issue type: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue_type WHERE id = $1`, out.ID)
	})
	return out
}

func TestIssueTypesAcceptanceResearchAndDelivery(t *testing.T) {
	suffix := uuid.NewString()[:8]
	sourceURL := createTestProperty(t, map[string]any{"name": "Source URL " + suffix, "type": "url"})
	readCount := createTestProperty(t, map[string]any{"name": "Read Count " + suffix, "type": "number"})
	dueNotes := createTestProperty(t, map[string]any{"name": "Due Notes " + suffix, "type": "text"})
	reviewConfirmed := createTestProperty(t, map[string]any{"name": "Review Confirmed " + suffix, "type": "checkbox"})

	research := createTestIssueType(t, map[string]any{
		"name": "Research " + suffix,
		"fields": []map[string]any{
			{"property_id": sourceURL.ID, "required": true},
			{"property_id": readCount.ID, "required": false, "default_value": 0},
		},
	})
	if len(research.Fields) != 2 || research.Fields[0].PropertyID != sourceURL.ID || !research.Fields[0].Required {
		t.Fatalf("unexpected research fields: %#v", research.Fields)
	}
	if string(research.Fields[1].DefaultValue) != "0" {
		t.Fatalf("research readCount default = %s, want 0", string(research.Fields[1].DefaultValue))
	}

	delivery := createTestIssueType(t, map[string]any{
		"name": "Delivery " + suffix,
		"fields": []map[string]any{
			{"property_id": dueNotes.ID, "required": true},
			{"property_id": reviewConfirmed.ID, "required": false, "default_value": false},
		},
	})
	if string(delivery.Fields[1].DefaultValue) != "false" {
		t.Fatalf("delivery reviewConfirmed default = %s, want false", string(delivery.Fields[1].DefaultValue))
	}

	// Missing required Source URL fails and rolls back.
	missingTitle := "missing source url " + suffix
	missingResp := testutil.Call(t, testHandler.CreateIssue,
		newRequest(http.MethodPost, "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
			"title":         missingTitle,
			"issue_type_id": research.ID,
		}),
	).Want(http.StatusBadRequest)
	var missingBody map[string]any
	_ = json.NewDecoder(missingResp.Body).Decode(&missingBody)
	if missingBody["code"] != "invalid_issue_property" || missingBody["property_id"] != sourceURL.ID {
		t.Fatalf("unexpected missing-required error body: %#v", missingBody)
	}
	if got := countIssuesWithAtomicPropertyTitle(t, missingTitle); got != 0 {
		t.Fatalf("missing required field persisted %d issue(s)", got)
	}

	// Create Research task with required URL; server applies Read Count default 0.
	researchIssue := createIssueWithPropertyBody(t, map[string]any{
		"title":         "Research task " + suffix,
		"issue_type_id": research.ID,
		"properties": map[string]any{
			sourceURL.ID: "  https://example.com/paper  ",
		},
	})
	if researchIssue.IssueTypeID == nil || *researchIssue.IssueTypeID != research.ID {
		t.Fatalf("research issue_type_id = %v, want %s", researchIssue.IssueTypeID, research.ID)
	}
	if researchIssue.Properties[sourceURL.ID] != "https://example.com/paper" {
		t.Fatalf("sourceURL = %#v, want canonical URL", researchIssue.Properties[sourceURL.ID])
	}
	if researchIssue.Properties[readCount.ID] != float64(0) {
		t.Fatalf("readCount = %#v, want 0", researchIssue.Properties[readCount.ID])
	}

	// Fetch through native GET /api/issues/{identifier} (short ID) preserves type and canonical values.
	getResp := testutil.Call(t, testHandler.GetIssue,
		withURLParam(newRequest(http.MethodGet, "/api/issues/"+researchIssue.Identifier, nil), "id", researchIssue.Identifier),
	).Want(http.StatusOK)
	var fetched IssueResponse
	if err := json.NewDecoder(getResp.Body).Decode(&fetched); err != nil {
		t.Fatalf("decode fetched issue: %v", err)
	}
	if fetched.IssueTypeID == nil || *fetched.IssueTypeID != research.ID {
		t.Fatalf("fetched issue_type_id = %v, want %s", fetched.IssueTypeID, research.ID)
	}
	if fetched.Properties[readCount.ID] != float64(0) {
		t.Fatalf("fetched readCount = %#v, want 0", fetched.Properties[readCount.ID])
	}

	// Create Delivery task with required Due Notes; server applies checkbox default false.
	deliveryIssue := createIssueWithPropertyBody(t, map[string]any{
		"title":         "Delivery task " + suffix,
		"issue_type_id": delivery.ID,
		"properties": map[string]any{
			dueNotes.ID: "Ship by Friday",
		},
	})
	if deliveryIssue.IssueTypeID == nil || *deliveryIssue.IssueTypeID != delivery.ID {
		t.Fatalf("delivery issue_type_id = %v, want %s", deliveryIssue.IssueTypeID, delivery.ID)
	}
	if deliveryIssue.Properties[reviewConfirmed.ID] != false {
		t.Fatalf("reviewConfirmed = %#v, want false", deliveryIssue.Properties[reviewConfirmed.ID])
	}

	// Explicit 0 and false satisfy required fields when required=true.
	strictType := createTestIssueType(t, map[string]any{
		"name": "Strict " + suffix,
		"fields": []map[string]any{
			{"property_id": readCount.ID, "required": true},
			{"property_id": reviewConfirmed.ID, "required": true},
		},
	})
	strictIssue := createIssueWithPropertyBody(t, map[string]any{
		"title":         "Explicit zero and false " + suffix,
		"issue_type_id": strictType.ID,
		"properties": map[string]any{
			readCount.ID:       0,
			reviewConfirmed.ID: false,
		},
	})
	if strictIssue.Properties[readCount.ID] != float64(0) || strictIssue.Properties[reviewConfirmed.ID] != false {
		t.Fatalf("strictIssue properties = %#v, want 0 and false", strictIssue.Properties)
	}

	// Clearing a required property is rejected; clearing an optional property succeeds
	// and preserves all other stored attributes.
	testutil.Call(t, testHandler.DeleteIssueProperty,
		withURLParams(
			newRequest(http.MethodDelete, "/api/issues/"+researchIssue.ID+"/properties/"+sourceURL.ID, nil),
			"id", researchIssue.ID, "propertyId", sourceURL.ID,
		),
	).Want(http.StatusBadRequest)

	delOptResp := testutil.Call(t, testHandler.DeleteIssueProperty,
		withURLParams(
			newRequest(http.MethodDelete, "/api/issues/"+researchIssue.ID+"/properties/"+readCount.ID, nil),
			"id", researchIssue.ID, "propertyId", readCount.ID,
		),
	).Want(http.StatusOK)
	var delOptBody map[string]any
	_ = json.NewDecoder(delOptResp.Body).Decode(&delOptBody)
	propsAfterClear, _ := delOptBody["properties"].(map[string]any)
	if _, stillHasOptional := propsAfterClear[readCount.ID]; stillHasOptional {
		t.Fatalf("optional property was not cleared: %#v", propsAfterClear)
	}
	if propsAfterClear[sourceURL.ID] != "https://example.com/paper" {
		t.Fatalf("required property was not preserved after clearing optional: %#v", propsAfterClear)
	}

	// Status/title update via native PATCH preserves issue_type_id and properties.
	patchResp := testutil.Call(t, testHandler.UpdateIssue,
		withURLParam(newRequest(http.MethodPatch, "/api/issues/"+researchIssue.ID, map[string]any{
			"status": "in_progress",
		}), "id", researchIssue.ID),
	).Want(http.StatusOK)
	var patched IssueResponse
	_ = json.NewDecoder(patchResp.Body).Decode(&patched)
	if patched.Status != "in_progress" {
		t.Fatalf("patched status = %q, want in_progress", patched.Status)
	}
	if patched.IssueTypeID == nil || *patched.IssueTypeID != research.ID {
		t.Fatalf("patched issue_type_id = %v, want %s", patched.IssueTypeID, research.ID)
	}
	if patched.Properties[sourceURL.ID] != "https://example.com/paper" {
		t.Fatalf("patched properties lost sourceURL: %#v", patched.Properties)
	}
}

func TestIssueTypesRejectInvalidForeignAndIllTypedDefinitionsAndPermissions(t *testing.T) {
	suffix := uuid.NewString()[:8]
	numProp := createTestProperty(t, map[string]any{"name": "NumDef " + suffix, "type": "number"})
	urlProp := createTestProperty(t, map[string]any{"name": "UrlDef " + suffix, "type": "url"})
	boolProp := createTestProperty(t, map[string]any{"name": "BoolDef " + suffix, "type": "checkbox"})
	archivedProp := createTestProperty(t, map[string]any{"name": "ArchDef " + suffix, "type": "text"})
	testutil.Call(t, testHandler.UpdateProperty,
		withURLParam(newRequest(http.MethodPatch, "/api/properties/"+archivedProp.ID, map[string]any{"archived": true}), "id", archivedProp.ID),
	).Want(http.StatusOK)

	// Foreign workspace property.
	foreignWorkspaceID := uuid.NewString()
	foreignPropertyID := uuid.NewString()
	if _, err := testPool.Exec(context.Background(), `INSERT INTO workspace (id, name, slug, issue_prefix) VALUES ($1, $2, $3, 'FT')`, foreignWorkspaceID, "Foreign type ws", "foreign-type-"+suffix); err != nil {
		t.Fatalf("create foreign workspace: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue_property WHERE id = $1`, foreignPropertyID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, foreignWorkspaceID)
	})
	if _, err := testPool.Exec(context.Background(), `INSERT INTO issue_property (id, workspace_id, name, type, config) VALUES ($1, $2, 'Foreign', 'text', '{}')`, foreignPropertyID, foreignWorkspaceID); err != nil {
		t.Fatalf("create foreign property: %v", err)
	}

	// Plain member is denied create/update (403), allowed to list (200).
	memberID := createPropertyTestMember(t)
	testutil.Call(t, testHandler.CreateIssueType,
		newRequestAs(memberID, http.MethodPost, "/api/issue-types", map[string]any{
			"name": "MemberDenied " + suffix, "fields": []map[string]any{},
		}),
	).Want(http.StatusForbidden)
	testutil.Call(t, testHandler.ListIssueTypes,
		newRequestAs(memberID, http.MethodGet, "/api/issue-types", nil),
	).Want(http.StatusOK)

	badCases := []struct {
		name   string
		fields []map[string]any
	}{
		{name: "invalid uuid", fields: []map[string]any{{"property_id": "not-a-uuid", "required": false}}},
		{name: "unknown property", fields: []map[string]any{{"property_id": uuid.NewString(), "required": false}}},
		{name: "foreign property", fields: []map[string]any{{"property_id": foreignPropertyID, "required": false}}},
		{name: "archived property", fields: []map[string]any{{"property_id": archivedProp.ID, "required": false}}},
		{name: "duplicate property", fields: []map[string]any{{"property_id": numProp.ID, "required": false}, {"property_id": numProp.ID, "required": true}}},
		{name: "ill-typed number default", fields: []map[string]any{{"property_id": numProp.ID, "required": false, "default_value": "not-a-number"}}},
		{name: "ill-typed url default", fields: []map[string]any{{"property_id": urlProp.ID, "required": false, "default_value": "not-a-url"}}},
		{name: "ill-typed checkbox default", fields: []map[string]any{{"property_id": boolProp.ID, "required": false, "default_value": "yes"}}},
	}
	for _, tc := range badCases {
		t.Run(tc.name, func(t *testing.T) {
			testutil.Call(t, testHandler.CreateIssueType,
				newRequest(http.MethodPost, "/api/issue-types", map[string]any{
					"name":   "Bad " + tc.name + " " + suffix,
					"fields": tc.fields,
				}),
			).Want(http.StatusBadRequest)
		})
	}

	// Unknown issue_type_id on CreateIssue is rejected and rolls back.
	badIssueTitle := "unknown type " + suffix
	testutil.Call(t, testHandler.CreateIssue,
		newRequest(http.MethodPost, "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
			"title":         badIssueTitle,
			"issue_type_id": uuid.NewString(),
		}),
	).Want(http.StatusBadRequest)
	if got := countIssuesWithAtomicPropertyTitle(t, badIssueTitle); got != 0 {
		t.Fatalf("unknown issue_type_id persisted %d issue(s)", got)
	}
}

func TestUntypedLegacyIssueParity(t *testing.T) {
	suffix := uuid.NewString()[:8]
	notes := createTestProperty(t, map[string]any{"name": "LegacyNotes " + suffix, "type": "text"})
	reads := createTestProperty(t, map[string]any{"name": "LegacyReads " + suffix, "type": "number"})
	confirmed := createTestProperty(t, map[string]any{"name": "LegacyConf " + suffix, "type": "checkbox"})

	issue := createIssueWithPropertyBody(t, map[string]any{
		"title": "Legacy untyped " + suffix,
		"properties": map[string]any{
			notes.ID:     "Unchanged old issue value",
			reads.ID:     0,
			confirmed.ID: false,
		},
	})
	if issue.IssueTypeID != nil {
		t.Fatalf("untyped issue_type_id = %v, want nil", issue.IssueTypeID)
	}
	if issue.Properties[notes.ID] != "Unchanged old issue value" ||
		issue.Properties[reads.ID] != float64(0) ||
		issue.Properties[confirmed.ID] != false {
		t.Fatalf("untyped properties mismatch: %#v", issue.Properties)
	}
	// Legacy untyped issues can still clear any property.
	testutil.Call(t, testHandler.DeleteIssueProperty,
		withURLParams(
			newRequest(http.MethodDelete, "/api/issues/"+issue.ID+"/properties/"+notes.ID, nil),
			"id", issue.ID, "propertyId", notes.ID,
		),
	).Want(http.StatusOK)
}
