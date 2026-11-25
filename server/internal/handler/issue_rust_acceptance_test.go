package handler

import (
	"encoding/json"
	"testing"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestRustAcceptanceAfterValidRejectsPartialSnapshot(t *testing.T) {
	issueID := parseUUID("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	wsID := parseUUID("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
	issue := db.Issue{ID: issueID, WorkspaceID: wsID, Revision: 4}
	expected, err := json.Marshal(map[string]any{
		"revision": 5,
		"criteria": []map[string]any{{
			"id": "C1", "versions": []string{"v1"}, "cases": []any{}, "implementations": []any{},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var partial ordinaryIssueSnapshot
	if err := json.Unmarshal([]byte(`{
		"id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		"workspace_id":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	}`), &partial); err != nil {
		t.Fatal(err)
	}
	if rustAcceptanceAfterValid(issue, expected, partial) {
		t.Fatal("partial upstream snapshot must not validate")
	}
}
