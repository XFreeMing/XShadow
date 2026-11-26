package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTableHistoryCommandsRegistered(t *testing.T) {
	for _, path := range [][]string{
		{"table", "history"},
		{"table", "history", "list"},
		{"table", "history", "snapshot"},
		{"table", "history", "deleted"},
	} {
		found, _, err := rootCmd.Find(path)
		if err != nil || found == nil || found.Name() != path[len(path)-1] {
			t.Errorf("rootCmd.Find(%v) = (%v, %v), want subcommand %q", path, found, err, path[len(path)-1])
		}
	}
}

func TestTableHistoryListSnapshotAndDeletedEndpoints(t *testing.T) {
	var requests []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/tables/" + testTableID1 + "/history":
			_, _ = w.Write([]byte(`{"table_id":"` + testTableID1 + `","baseline_revision":1,"latest_revision":5,"revisions":[{"revision":5,"recorded_at":"2026-11-26T01:05:00Z","baseline":false,"summary":{"table_changes":0,"field_changes":0,"view_changes":0,"record_changes":1,"cell_changes":1,"total_changes":2}}],"limit":10,"has_more":false,"next_cursor":null}`))
		case "/api/tables/" + testTableID1 + "/history/snapshot":
			_, _ = w.Write([]byte(`{"revision":5,"recorded_at":"2026-11-26T01:05:00Z","baseline":false,"computed_values_mode":"stored_raw_only","table":{"id":"` + testTableID1 + `","name":"Orders","created_at":"2026-11-26T01:00:00Z","updated_at":"2026-11-26T01:05:00Z"},"fields":[],"views":[],"records":[]}`))
		case "/api/tables/" + testTableID1 + "/history/deleted":
			_, _ = w.Write([]byte(`{"table_id":"` + testTableID1 + `","deleted_records":[{"record_id":"` + testTableRecordID2 + `","deleted_at_revision":4,"deleted_at":"2026-11-26T01:04:00Z"}],"limit":20,"has_more":false,"next_cursor":null}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"not found","code":"not_found"}`))
		}
	}))
	defer srv.Close()
	setTaskTableTestEnv(t, srv.URL)

	out, err := runRootTableCommand(t, "", "table", "history", "list", testTableID1, "--limit", "10", "--cursor", "6")
	if err != nil {
		t.Fatalf("table history list failed: %v", err)
	}
	var listResp map[string]any
	if err := json.Unmarshal([]byte(out), &listResp); err != nil {
		t.Fatalf("unmarshal history list: %v", err)
	}
	if listResp["table_id"] != testTableID1 {
		t.Fatalf("history list table_id = %v, want %s", listResp["table_id"], testTableID1)
	}

	out, err = runRootTableCommand(t, "", "table", "history", "snapshot", testTableID1, "--revision", "5")
	if err != nil {
		t.Fatalf("table history snapshot --revision failed: %v", err)
	}
	var snapResp map[string]any
	if err := json.Unmarshal([]byte(out), &snapResp); err != nil {
		t.Fatalf("unmarshal history snapshot: %v", err)
	}
	if snapResp["computed_values_mode"] != "stored_raw_only" {
		t.Fatalf("snapshot computed_values_mode = %v, want stored_raw_only", snapResp["computed_values_mode"])
	}

	_, err = runRootTableCommand(t, "", "table", "history", "snapshot", testTableID1, "--timestamp", "2026-11-26T01:05:00Z")
	if err != nil {
		t.Fatalf("table history snapshot --timestamp failed: %v", err)
	}

	out, err = runRootTableCommand(t, "", "table", "history", "deleted", testTableID1, "--limit", "20", "--cursor", "5:"+testTableRecordID1)
	if err != nil {
		t.Fatalf("table history deleted failed: %v", err)
	}
	if !strings.Contains(out, testTableRecordID2) {
		t.Fatalf("history deleted output = %s, want %s", out, testTableRecordID2)
	}

	want := []string{
		"GET /api/tables/" + testTableID1 + "/history?cursor=6&limit=10",
		"GET /api/tables/" + testTableID1 + "/history/snapshot?revision=5",
		"GET /api/tables/" + testTableID1 + "/history/snapshot?timestamp=2026-11-26T01%3A05%3A00Z",
		"GET /api/tables/" + testTableID1 + "/history/deleted?cursor=5%3A" + testTableRecordID1 + "&limit=20",
	}
	if len(requests) != len(want) {
		t.Fatalf("requests = %v, want %v", requests, want)
	}
	for i := range want {
		if requests[i] != want[i] {
			t.Errorf("request[%d] = %q, want %q", i, requests[i], want[i])
		}
	}
}

func TestTableHistoryLocalValidationRejectsBadFlagsBeforeHTTP(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	setTaskTableTestEnv(t, srv.URL)

	cases := [][]string{
		{"table", "history", "list", "not-a-uuid"},
		{"table", "history", "list", testTableID1, "--limit", "0"},
		{"table", "history", "list", testTableID1, "--limit", "201"},
		{"table", "history", "snapshot", testTableID1},
		{"table", "history", "snapshot", testTableID1, "--revision", "1", "--timestamp", "2026-11-26T01:00:00Z"},
		{"table", "history", "snapshot", testTableID1, "--revision", "0"},
		{"table", "history", "snapshot", testTableID1, "--timestamp", "not-a-time"},
		{"table", "history", "deleted", testTableID1, "--limit", "201"},
	}
	for _, args := range cases {
		if _, err := runRootTableCommand(t, "", args...); err == nil {
			t.Errorf("args %v unexpectedly succeeded", args)
		}
	}
	if called {
		t.Fatal("local validation should fail before issuing any HTTP request")
	}
}
