package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestTableFullCRUDComputedChangesAndPresence(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/issues/properties/project":
			if !strings.Contains(string(bodyBytes), testTableIssueID1) {
				t.Errorf("computed body missing issue id: %s", string(bodyBytes))
			}
			_, _ = io.WriteString(w, `{"issues":{"`+testTableIssueID1+`":{"values":{"`+testTablePropFormID+`":42}}}}`)
		case r.Method == http.MethodDelete && r.URL.Path == "/api/tables/"+testTableID1:
			_, _ = io.WriteString(w, `{"deleted":true,"id":"`+testTableID1+`"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/tables/"+testTableID1+"/fields":
			_, _ = io.WriteString(w, `{"fields":[{"id":"`+testTableFieldID1+`","name":"Title","kind":"text"}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/tables/"+testTableID1+"/fields/"+testTableFieldID1:
			_, _ = io.WriteString(w, `{"field":{"id":"`+testTableFieldID1+`","name":"Title","kind":"text"}}`)
		case r.Method == http.MethodDelete && r.URL.Path == "/api/tables/"+testTableID1+"/fields/"+testTableFieldID1:
			_, _ = io.WriteString(w, `{"deleted":true,"id":"`+testTableFieldID1+`"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/tables/"+testTableID1+"/records":
			if r.URL.Query().Get("view") != testTableViewID1 || r.URL.Query().Get("limit") != "25" || r.URL.Query().Get("offset") != "5" {
				t.Errorf("record list query = %q, want view+limit+offset", r.URL.RawQuery)
			}
			_, _ = io.WriteString(w, `{"records":[{"id":"`+testTableRecordID1+`","revision":2,"values":{}}],"total":6,"limit":25,"offset":5,"has_more":false,"next_offset":null,"active_view_id":"`+testTableViewID1+`"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/tables/"+testTableID1+"/records/"+testTableRecordID1:
			_, _ = io.WriteString(w, `{"record":{"id":"`+testTableRecordID1+`","revision":2,"values":{}}}`)
		case r.Method == http.MethodDelete && r.URL.Path == "/api/tables/"+testTableID1+"/records/"+testTableRecordID1:
			if r.URL.Query().Get("expected_revision") != "2" {
				t.Errorf("record delete expected_revision = %q, want 2", r.URL.Query().Get("expected_revision"))
			}
			_, _ = io.WriteString(w, `{"deleted":true,"id":"`+testTableRecordID1+`","revision":2}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/tables/"+testTableID1+"/records/batch":
			if !strings.Contains(string(bodyBytes), `"idempotency_key"`) {
				t.Errorf("record batch body missing idempotency_key: %s", string(bodyBytes))
			}
			_, _ = io.WriteString(w, `{"operation":"create","idempotency_key":"`+testTableViewID1+`","batch_id":"`+testTableRecordID1+`","records":[{"id":"`+testTableRecordID1+`","revision":1,"values":{}}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/tables/"+testTableID1+"/views":
			_, _ = io.WriteString(w, `{"views":[{"id":"`+testTableViewID1+`","name":"All","kind":"grid"}]}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/tables/"+testTableID1+"/views/"+testTableViewID1:
			_, _ = io.WriteString(w, `{"view":{"id":"`+testTableViewID1+`","name":"All","kind":"grid"}}`)
		case r.Method == http.MethodDelete && r.URL.Path == "/api/tables/"+testTableID1+"/views/"+testTableViewID1:
			_, _ = io.WriteString(w, `{"deleted":true,"id":"`+testTableViewID1+`"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/tables/"+testTableID1+"/changes":
			if r.URL.Query().Get("since") != "4" {
				t.Errorf("changes since = %q, want 4", r.URL.Query().Get("since"))
			}
			_, _ = io.WriteString(w, `{"latest":5,"next_since":5,"changes":[{"seq":5,"event_type":"record.deleted","topic":"record","entity_id":"`+testTableRecordID1+`","record_id":"`+testTableRecordID1+`","record_revision":2,"fields":{},"actor":{"source":"agent","id":"`+testTableAgentID+`","task_id":"`+testTableTaskID+`","label":"Agent","color":"#7B67EE"}}],"presence":[]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/tables/"+testTableID1+"/presence":
			if !strings.Contains(string(bodyBytes), testTableRecordID1) || !strings.Contains(string(bodyBytes), testTableFieldID1) {
				t.Errorf("presence body = %s", string(bodyBytes))
			}
			_, _ = io.WriteString(w, `{"latest":5,"next_since":5,"changes":[],"presence":[{"actor":{"source":"agent","id":"`+testTableAgentID+`","label":"Agent","color":"#7B67EE"},"record_id":"`+testTableRecordID1+`","field_id":"`+testTableFieldID1+`","online":true}]}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	setTaskTableTestEnv(t, srv.URL)

	if out, err := runRootTableCommand(t, `{"issue_ids":["`+testTableIssueID1+`"]}`, "table", "tasks", "computed", "--input", "-"); err != nil || !strings.Contains(out, `"42"`) && !strings.Contains(out, `: 42`) {
		t.Fatalf("table tasks computed = (%q, %v)", out, err)
	}
	if out, err := runRootTableCommand(t, "", "table", "field", "list", testTableID1); err != nil || !strings.Contains(out, testTableFieldID1) {
		t.Fatalf("table field list = (%q, %v)", out, err)
	}
	if out, err := runRootTableCommand(t, "", "table", "field", "get", testTableID1, testTableFieldID1); err != nil || !strings.Contains(out, `"Title"`) {
		t.Fatalf("table field get = (%q, %v)", out, err)
	}
	if out, err := runRootTableCommand(t, "", "table", "field", "delete", testTableID1, testTableFieldID1); err != nil || !strings.Contains(out, `"deleted": true`) {
		t.Fatalf("table field delete = (%q, %v)", out, err)
	}
	if out, err := runRootTableCommand(t, "", "table", "record", "list", testTableID1, "--view", testTableViewID1, "--limit", "25", "--offset", "5"); err != nil || !strings.Contains(out, `"total": 6`) {
		t.Fatalf("table record list = (%q, %v)", out, err)
	}
	if out, err := runRootTableCommand(t, "", "table", "record", "get", testTableID1, testTableRecordID1); err != nil || !strings.Contains(out, testTableRecordID1) {
		t.Fatalf("table record get = (%q, %v)", out, err)
	}
	if out, err := runRootTableCommand(t, "", "table", "record", "delete", testTableID1, testTableRecordID1, "--expected-revision", "2"); err != nil || !strings.Contains(out, `"deleted": true`) {
		t.Fatalf("table record delete via flag = (%q, %v)", out, err)
	}
	if out, err := runRootTableCommand(t, `{"expected_revision":2}`, "table", "record", "delete", testTableID1, testTableRecordID1, "--input", "-"); err != nil || !strings.Contains(out, `"deleted": true`) {
		t.Fatalf("table record delete via input = (%q, %v)", out, err)
	}
	if out, err := runRootTableCommand(t, `{"idempotency_key":"`+testTableViewID1+`","operation":"create","records":[{"values":{"`+testTableFieldID1+`":"Alpha"}}]}`, "table", "record", "batch", testTableID1, "--input", "-"); err != nil || !strings.Contains(out, `"batch_id"`) {
		t.Fatalf("table record batch = (%q, %v)", out, err)
	}
	if out, err := runRootTableCommand(t, "", "table", "view", "list", testTableID1); err != nil || !strings.Contains(out, testTableViewID1) {
		t.Fatalf("table view list = (%q, %v)", out, err)
	}
	if out, err := runRootTableCommand(t, "", "table", "view", "get", testTableID1, testTableViewID1); err != nil || !strings.Contains(out, `"grid"`) {
		t.Fatalf("table view get = (%q, %v)", out, err)
	}
	if out, err := runRootTableCommand(t, "", "table", "view", "delete", testTableID1, testTableViewID1); err != nil || !strings.Contains(out, `"deleted": true`) {
		t.Fatalf("table view delete = (%q, %v)", out, err)
	}
	if out, err := runRootTableCommand(t, "", "table", "changes", testTableID1, "--since", "4"); err != nil || !strings.Contains(out, `"record.deleted"`) {
		t.Fatalf("table changes = (%q, %v)", out, err)
	}
	if out, err := runRootTableCommand(t, "", "table", "presence", testTableID1, "--record-id", testTableRecordID1, "--field-id", testTableFieldID1); err != nil || !strings.Contains(out, `"online": true`) {
		t.Fatalf("table presence = (%q, %v)", out, err)
	}
	if out, err := runRootTableCommand(t, "", "table", "delete", testTableID1); err != nil || !strings.Contains(out, `"deleted": true`) {
		t.Fatalf("table delete = (%q, %v)", out, err)
	}
}

func TestTableNewSubcommandsRejectInvalidInputsBeforeNetwork(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	setTaskTableTestEnv(t, srv.URL)

	cases := []struct {
		name  string
		stdin string
		args  []string
	}{
		{name: "delete record missing revision", args: []string{"table", "record", "delete", testTableID1, testTableRecordID1}},
		{name: "delete record zero revision", args: []string{"table", "record", "delete", testTableID1, testTableRecordID1, "--expected-revision", "0"}},
		{name: "delete record string revision in input", stdin: `{"expected_revision":"2"}`, args: []string{"table", "record", "delete", testTableID1, testTableRecordID1, "--input", "-"}},
		{name: "delete table bad uuid", args: []string{"table", "delete", "bad-id"}},
		{name: "presence field without record", args: []string{"table", "presence", testTableID1, "--field-id", testTableFieldID1}},
		{name: "changes negative since", args: []string{"table", "changes", testTableID1, "--since", "-1"}},
		{name: "computed missing input", args: []string{"table", "tasks", "computed"}},
		{name: "batch missing input", args: []string{"table", "record", "batch", testTableID1}},
		{name: "batch bad idempotency key", stdin: `{"idempotency_key":"bad","operation":"create","records":[{"values":{}}]}`, args: []string{"table", "record", "batch", testTableID1, "--input", "-"}},
		{name: "batch unknown operation", stdin: `{"idempotency_key":"` + testTableViewID1 + `","operation":"upsert","records":[{"values":{}}]}`, args: []string{"table", "record", "batch", testTableID1, "--input", "-"}},
		{name: "batch empty records", stdin: `{"idempotency_key":"` + testTableViewID1 + `","operation":"create","records":[]}`, args: []string{"table", "record", "batch", testTableID1, "--input", "-"}},
		{name: "batch duplicate update id", stdin: `{"idempotency_key":"` + testTableViewID1 + `","operation":"update","records":[{"id":"` + testTableRecordID1 + `","expected_revision":1,"values":{"` + testTableFieldID1 + `":"a"}},{"id":"` + strings.ToUpper(testTableRecordID1) + `","expected_revision":1,"values":{"` + testTableFieldID1 + `":"b"}}]}`, args: []string{"table", "record", "batch", testTableID1, "--input", "-"}},
		{name: "batch delete zero revision", stdin: `{"idempotency_key":"` + testTableViewID1 + `","operation":"delete","records":[{"id":"` + testTableRecordID1 + `","expected_revision":0}]}`, args: []string{"table", "record", "batch", testTableID1, "--input", "-"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := hits.Load()
			_, err := runRootTableCommand(t, tc.stdin, tc.args...)
			if err == nil {
				t.Fatalf("expected error for %s, got nil", tc.name)
			}
			if hits.Load() != before {
				t.Fatalf("%s hit network, want 0 requests", tc.name)
			}
		})
	}
}
