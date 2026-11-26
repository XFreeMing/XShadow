package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/multica-ai/multica/server/internal/cli"
)

const (
	testTableID1        = "11111111-1111-4111-8111-111111111111"
	testTableFieldID1   = "22222222-2222-4222-8222-222222222222"
	testTableRecordID1  = "33333333-3333-4333-8333-333333333333"
	testTableRecordID2  = "44444444-4444-4444-8444-444444444444"
	testTableViewID1    = "55555555-5555-4555-8555-555555555555"
	testTableCommentID1 = "66666666-6666-4666-8666-666666666666"
	testTableIssueID1   = "77777777-7777-4777-8777-777777777777"
	testTablePropRelID  = "88888888-8888-4888-8888-888888888888"
	testTablePropLookID = "99999999-9999-4999-8999-999999999999"
	testTablePropFormID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	testTableAgentID    = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	testTableTaskID     = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
)

func resetCobraFlags(cmd *cobra.Command) {
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		_ = f.Value.Set(f.DefValue)
		f.Changed = false
	})
	for _, sub := range cmd.Commands() {
		resetCobraFlags(sub)
	}
}

func runRootTableCommand(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	resetCobraFlags(rootCmd)
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&bytes.Buffer{})
	if stdin != "" {
		rootCmd.SetIn(strings.NewReader(stdin))
	} else {
		rootCmd.SetIn(strings.NewReader(""))
	}
	rootCmd.SetArgs(args)
	t.Cleanup(func() {
		resetCobraFlags(rootCmd)
		rootCmd.SetArgs(nil)
		rootCmd.SetIn(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})
	err := rootCmd.Execute()
	return out.String(), err
}

func setTaskTableTestEnv(t *testing.T, serverURL string) {
	t.Helper()
	setCLITestServerEnv(t, serverURL)
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-test")
	t.Setenv("MULTICA_TOKEN", "mat_table_test_token")
	t.Setenv("MULTICA_AGENT_ID", testTableAgentID)
	t.Setenv("MULTICA_TASK_ID", testTableTaskID)
}

func TestTableCommandRegisteredOnRoot(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"table"})
	if err != nil || cmd == nil || cmd.Name() != "table" {
		t.Fatalf("rootCmd.Find(table) = (%v, %v), want registered table command", cmd, err)
	}
	for _, path := range [][]string{
		{"table", "tasks", "rows"},
		{"table", "tasks", "groups"},
		{"table", "tasks", "facets"},
		{"table", "list"},
		{"table", "get"},
		{"table", "create"},
		{"table", "rename"},
		{"table", "field", "create"},
		{"table", "field", "update"},
		{"table", "record", "options"},
		{"table", "record", "create"},
		{"table", "record", "update"},
		{"table", "record", "comment", "list"},
		{"table", "record", "comment", "create"},
		{"table", "view", "create"},
		{"table", "view", "update"},
		{"table", "view", "submit"},
	} {
		found, _, err := rootCmd.Find(path)
		if err != nil || found == nil || found.Name() != path[len(path)-1] {
			t.Errorf("rootCmd.Find(%v) = (%v, %v), want subcommand %q", path, found, err, path[len(path)-1])
		}
	}
}

func TestTableTasksEndpointsPreserveJSONBigIntsCursorsAndTaskIdentity(t *testing.T) {
	type capturedReq struct {
		method    string
		path      string
		auth      string
		workspace string
		agentID   string
		taskID    string
		body      string
	}
	var mu sync.Mutex
	var reqs []capturedReq

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		mu.Lock()
		reqs = append(reqs, capturedReq{
			method:    r.Method,
			path:      r.URL.Path,
			auth:      r.Header.Get("Authorization"),
			workspace: r.Header.Get("X-Workspace-ID"),
			agentID:   r.Header.Get("X-Agent-ID"),
			taskID:    r.Header.Get("X-Task-ID"),
			body:      string(bodyBytes),
		})
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/issues/table/rows":
			if strings.Contains(string(bodyBytes), `"bad-cursor"`) {
				w.WriteHeader(http.StatusConflict)
				_, _ = io.WriteString(w, `{"error":"cursor_query_mismatch","message":"cursor does not belong to this table query"}`)
				return
			}
			_, _ = io.WriteString(w, `{
				"query_fingerprint":"sha256:abc123",
				"group_key":"status:todo",
				"parent_id":null,
				"total":2,
				"branch_total":1,
				"next_cursor":"cursor_page_2",
				"rows":[{"issue":{"id":"`+testTableIssueID1+`","identifier":"XSH-42","title":"Table task","status":"todo","big_seq":9007199254740993123},"direct_child_count":3}]
			}`)
		case "/api/issues/table/groups":
			_, _ = io.WriteString(w, `{
				"query_fingerprint":"sha256:abc123",
				"total":2,
				"next_cursor":"group_cursor_2",
				"groups":[{"key":"status:todo","value":{"kind":"status","status":"todo"},"count":2}]
			}`)
		case "/api/issues/table/facets":
			_, _ = io.WriteString(w, `{
				"query_fingerprint":"sha256:abc123",
				"total":2,
				"facets":[{"kind":"status","values":[{"key":"todo","count":2}]}]
			}`)
		case "/api/issues/" + testTableIssueID1:
			_, _ = io.WriteString(w, `{"id":"`+testTableIssueID1+`","identifier":"XSH-42","title":"Table task","status":"todo"}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	setTaskTableTestEnv(t, srv.URL)

	workDir := t.TempDir()
	t.Chdir(workDir)
	queryJSON := `{"query":{"scope":{"kind":"workspace"},"filters":{"statuses":["todo"],"properties":{"` + testTablePropRelID + `":[9007199254740993123]}},"sort":{"field":"position","direction":"asc"}},"group":{"kind":"status"},"group_key":"status:todo","hierarchy":{"enabled":false},"parent_id":null,"page":{"limit":20}}`
	if err := os.WriteFile(filepath.Join(workDir, "query.json"), []byte(queryJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	rowsOut, err := runRootTableCommand(t, "", "table", "tasks", "rows", "--input", "query.json")
	if err != nil {
		t.Fatalf("table tasks rows failed: %v", err)
	}
	for _, want := range []string{
		`"query_fingerprint": "sha256:abc123"`,
		`"group_key": "status:todo"`,
		`"next_cursor": "cursor_page_2"`,
		`"direct_child_count": 3`,
		`"id": "` + testTableIssueID1 + `"`,
		`9007199254740993123`,
	} {
		if !strings.Contains(rowsOut, want) {
			t.Errorf("rows output missing %s\noutput:\n%s", want, rowsOut)
		}
	}

	groupsJSON := `{"query":{"scope":{"kind":"workspace"},"filters":{"statuses":["todo"]},"sort":{"field":"position","direction":"asc"}},"group":{"kind":"status"},"page":{"limit":20}}`
	groupsOut, err := runRootTableCommand(t, groupsJSON, "table", "tasks", "groups", "--input", "-")
	if err != nil {
		t.Fatalf("table tasks groups failed: %v", err)
	}
	for _, want := range []string{`"query_fingerprint": "sha256:abc123"`, `"next_cursor": "group_cursor_2"`, `"key": "status:todo"`} {
		if !strings.Contains(groupsOut, want) {
			t.Errorf("groups output missing %s\noutput:\n%s", want, groupsOut)
		}
	}

	facetsJSON := `{"query":{"scope":{"kind":"workspace"},"filters":{"statuses":["todo"]},"sort":{"field":"position","direction":"asc"}},"facets":[{"kind":"status"}]}`
	facetsOut, err := runRootTableCommand(t, facetsJSON, "table", "tasks", "facets", "--input", "-")
	if err != nil {
		t.Fatalf("table tasks facets failed: %v", err)
	}
	if !strings.Contains(facetsOut, `"kind": "status"`) || !strings.Contains(facetsOut, `"query_fingerprint": "sha256:abc123"`) {
		t.Errorf("unexpected facets output:\n%s", facetsOut)
	}

	// Same task identity can read back the exact issue returned by table tasks rows.
	issueOut, err := captureStdout(t, func() error {
		_, err := runRootTableCommand(t, "", "issue", "get", testTableIssueID1, "--output", "json")
		return err
	})
	if err != nil {
		t.Fatalf("issue get returned error: %v", err)
	}
	if !strings.Contains(issueOut, testTableIssueID1) || !strings.Contains(issueOut, `"XSH-42"`) {
		t.Errorf("issue get output did not match task identity:\n%s", issueOut)
	}

	// Cursor mismatch 409 must not be swallowed or retried.
	beforeConflict := len(reqs)
	conflictQuery := `{"query":{"scope":{"kind":"workspace"},"filters":{},"sort":{"field":"position","direction":"asc"}},"group":{"kind":"none"},"group_key":null,"hierarchy":{"enabled":false},"parent_id":null,"page":{"limit":20,"cursor":"bad-cursor"}}`
	_, conflictErr := runRootTableCommand(t, conflictQuery, "table", "tasks", "rows", "--input", "-")
	if conflictErr == nil {
		t.Fatal("expected 409 conflict error for mismatched cursor")
	}
	var httpErr *cli.HTTPError
	if !errors.As(conflictErr, &httpErr) || httpErr.StatusCode != http.StatusConflict {
		t.Fatalf("conflictErr = %v, want *cli.HTTPError with status 409", conflictErr)
	}
	if gotCalls := len(reqs) - beforeConflict; gotCalls != 1 {
		t.Fatalf("conflict call count = %d, want 1 (no retry)", gotCalls)
	}
	if formatted := cli.FormatError(conflictErr, false); !strings.Contains(formatted, "cursor_query_mismatch") {
		t.Errorf("FormatError(conflictErr) = %q, want cursor_query_mismatch surfaced", formatted)
	}

	// Verify request headers and big integer preservation on the wire.
	if len(reqs) < 4 {
		t.Fatalf("captured %d requests, want at least 4", len(reqs))
	}
	if !strings.Contains(reqs[0].body, "9007199254740993123") {
		t.Errorf("request body did not preserve big integer: %s", reqs[0].body)
	}
	for i, req := range reqs[:4] {
		if req.auth != "Bearer mat_table_test_token" || req.workspace != "ws-test" || req.agentID != testTableAgentID || req.taskID != testTableTaskID {
			t.Errorf("req[%d] (%s %s) headers = auth:%q ws:%q agent:%q task:%q", i, req.method, req.path, req.auth, req.workspace, req.agentID, req.taskID)
		}
	}
}

func TestGenericTableCRUDAndRevisionConflict(t *testing.T) {
	var mu sync.Mutex
	recordRevision := int64(1)
	recordValue := "initial"
	patchAttempts := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/tables":
			_, _ = io.WriteString(w, `{"tables":[{"id":"`+testTableID1+`","name":"Roadmap"}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/tables":
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"table":{"id":"`+testTableID1+`","name":"Spec Table"},"view":{"id":"`+testTableViewID1+`","name":"All"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/tables/"+testTableID1:
			if got := r.URL.Query().Get("view"); got != testTableViewID1 {
				t.Errorf("get table view query = %q, want %q", got, testTableViewID1)
			}
			mu.Lock()
			rev, val := recordRevision, recordValue
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"table":          map[string]any{"id": testTableID1, "name": "Renamed"},
				"fields":         []any{map[string]any{"id": testTableFieldID1, "name": "Summary", "kind": "text"}},
				"views":          []any{map[string]any{"id": testTableViewID1, "name": "Grid"}},
				"records":        []any{map[string]any{"id": testTableRecordID1, "revision": rev, "values": map[string]any{testTableFieldID1: val}}},
				"active_view_id": testTableViewID1,
			})
		case r.Method == http.MethodPatch && r.URL.Path == "/api/tables/"+testTableID1:
			_, _ = io.WriteString(w, `{"table":{"id":"`+testTableID1+`","name":"Renamed"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/tables/"+testTableID1+"/fields":
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"field":{"id":"`+testTableFieldID1+`","name":"Amount","kind":"number"}}`)
		case r.Method == http.MethodPatch && r.URL.Path == "/api/tables/"+testTableID1+"/fields/"+testTableFieldID1:
			_, _ = io.WriteString(w, `{"field":{"id":"`+testTableFieldID1+`","name":"Total","kind":"number"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/tables/"+testTableID1+"/record-options":
			_, _ = io.WriteString(w, `{"records":[{"id":"`+testTableRecordID1+`","label":"Row A"}]}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/tables/"+testTableID1+"/records":
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"record":{"id":"`+testTableRecordID1+`","revision":1,"values":{"`+testTableFieldID1+`":9007199254740993123}}}`)
		case r.Method == http.MethodPatch && r.URL.Path == "/api/tables/"+testTableID1+"/records/"+testTableRecordID1:
			mu.Lock()
			patchAttempts++
			var req struct {
				ExpectedRevision int64          `json:"expected_revision"`
				Values           map[string]any `json:"values"`
			}
			_ = json.Unmarshal(bodyBytes, &req)
			if req.ExpectedRevision != recordRevision {
				curRev, curVal := recordRevision, recordValue
				mu.Unlock()
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"code":  "revision_conflict",
					"error": "记录已被其他人更新",
					"record": map[string]any{
						"id":       testTableRecordID1,
						"revision": curRev,
						"values":   map[string]any{testTableFieldID1: curVal},
					},
				})
				return
			}
			recordRevision++
			if v, ok := req.Values[testTableFieldID1].(string); ok {
				recordValue = v
			}
			newRev, newVal := recordRevision, recordValue
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"record": map[string]any{
					"id":       testTableRecordID1,
					"revision": newRev,
					"values":   map[string]any{testTableFieldID1: newVal},
				},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/tables/"+testTableID1+"/views":
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"view":{"id":"`+testTableViewID1+`","name":"Board","kind":"kanban"}}`)
		case r.Method == http.MethodPatch && r.URL.Path == "/api/tables/"+testTableID1+"/views/"+testTableViewID1:
			_, _ = io.WriteString(w, `{"view":{"id":"`+testTableViewID1+`","name":"Board v2","kind":"kanban"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/tables/"+testTableID1+"/views/"+testTableViewID1+"/submit":
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"record":{"id":"`+testTableRecordID2+`","revision":1,"values":{}}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/tables/"+testTableID1+"/records/"+testTableRecordID1+"/comments":
			if r.URL.Query().Get("after") != "3" || r.URL.Query().Get("limit") != "10" {
				t.Errorf("comment list query = %q, want after=3&limit=10", r.URL.RawQuery)
			}
			_, _ = io.WriteString(w, `{"comments":[{"id":"`+testTableCommentID1+`","seq":4,"body":"checked"}],"has_more":false,"next_cursor":null}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/tables/"+testTableID1+"/records/"+testTableRecordID1+"/comments":
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"comment":{"id":"`+testTableCommentID1+`","seq":5,"body":"done"}}`)
		default:
			t.Errorf("unexpected generic table request: %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	setTaskTableTestEnv(t, srv.URL)

	workDir := t.TempDir()
	t.Chdir(workDir)

	if out, err := runRootTableCommand(t, "", "table", "list"); err != nil || !strings.Contains(out, testTableID1) {
		t.Fatalf("table list = (%q, %v)", out, err)
	}
	if out, err := runRootTableCommand(t, `{"name":"Spec Table"}`, "table", "create", "--input", "-"); err != nil || !strings.Contains(out, `"Spec Table"`) {
		t.Fatalf("table create = (%q, %v)", out, err)
	}
	if out, err := runRootTableCommand(t, `{"name":"Renamed"}`, "table", "rename", testTableID1, "--input", "-"); err != nil || !strings.Contains(out, `"Renamed"`) {
		t.Fatalf("table rename = (%q, %v)", out, err)
	}
	if out, err := runRootTableCommand(t, `{"name":"Amount","kind":"number"}`, "table", "field", "create", testTableID1, "--input", "-"); err != nil || !strings.Contains(out, testTableFieldID1) {
		t.Fatalf("table field create = (%q, %v)", out, err)
	}
	if out, err := runRootTableCommand(t, `{"name":"Total"}`, "table", "field", "update", testTableID1, testTableFieldID1, "--input", "-"); err != nil || !strings.Contains(out, `"Total"`) {
		t.Fatalf("table field update = (%q, %v)", out, err)
	}
	if out, err := runRootTableCommand(t, "", "table", "record", "options", testTableID1); err != nil || !strings.Contains(out, `"Row A"`) {
		t.Fatalf("table record options = (%q, %v)", out, err)
	}
	if out, err := runRootTableCommand(t, `{"values":{"`+testTableFieldID1+`":9007199254740993123}}`, "table", "record", "create", testTableID1, "--input", "-"); err != nil || !strings.Contains(out, `9007199254740993123`) {
		t.Fatalf("table record create = (%q, %v)", out, err)
	}

	// First optimistic update succeeds (revision 1 -> 2).
	firstPatch := `{"expected_revision":1,"values":{"` + testTableFieldID1 + `":"winner"}}`
	if out, err := runRootTableCommand(t, firstPatch, "table", "record", "update", testTableID1, testTableRecordID1, "--input", "-"); err != nil || !strings.Contains(out, `"revision": 2`) {
		t.Fatalf("first table record update = (%q, %v)", out, err)
	}

	// Second update with stale revision 1 must return 409 without retrying or overwriting.
	stalePatch := `{"expected_revision":1,"values":{"` + testTableFieldID1 + `":"stale-loser"}}`
	_, conflictErr := runRootTableCommand(t, stalePatch, "table", "record", "update", testTableID1, testTableRecordID1, "--input", "-")
	if conflictErr == nil {
		t.Fatal("second table record update with stale revision succeeded, want 409 conflict")
	}
	var httpErr *cli.HTTPError
	if !errors.As(conflictErr, &httpErr) || httpErr.StatusCode != http.StatusConflict {
		t.Fatalf("second update err = %v, want *cli.HTTPError 409", conflictErr)
	}
	if patchAttempts != 2 {
		t.Fatalf("patchAttempts = %d, want 2 (1 success + 1 failed conflict with zero retry)", patchAttempts)
	}
	if formatted := cli.FormatError(conflictErr, false); !strings.Contains(formatted, "revision_conflict") || !strings.Contains(formatted, `"winner"`) {
		t.Errorf("FormatError(conflictErr) = %q, want revision_conflict and current record value", formatted)
	}

	// Read back table get --view and verify current value stayed "winner" at revision 2.
	getOut, err := runRootTableCommand(t, "", "table", "get", testTableID1, "--view", testTableViewID1)
	if err != nil {
		t.Fatalf("table get failed: %v", err)
	}
	if !strings.Contains(getOut, `"winner"`) || strings.Contains(getOut, `"stale-loser"`) {
		t.Fatalf("table get showed unexpected value:\n%s", getOut)
	}

	if out, err := runRootTableCommand(t, `{"name":"Board","kind":"kanban","columns":null,"filter":null,"sort":null}`, "table", "view", "create", testTableID1, "--input", "-"); err != nil || !strings.Contains(out, `"Board"`) {
		t.Fatalf("table view create = (%q, %v)", out, err)
	}
	if out, err := runRootTableCommand(t, `{"name":"Board v2","columns":null,"filter":null,"sort":null}`, "table", "view", "update", testTableID1, testTableViewID1, "--input", "-"); err != nil || !strings.Contains(out, `"Board v2"`) {
		t.Fatalf("table view update = (%q, %v)", out, err)
	}
	if out, err := runRootTableCommand(t, `{"values":{}}`, "table", "view", "submit", testTableID1, testTableViewID1, "--input", "-"); err != nil || !strings.Contains(out, testTableRecordID2) {
		t.Fatalf("table view submit = (%q, %v)", out, err)
	}
	if out, err := runRootTableCommand(t, "", "table", "record", "comment", "list", testTableID1, testTableRecordID1, "--after", "3", "--limit", "10"); err != nil || !strings.Contains(out, `"checked"`) {
		t.Fatalf("table record comment list = (%q, %v)", out, err)
	}
	if out, err := runRootTableCommand(t, `{"body":"done"}`, "table", "record", "comment", "create", testTableID1, testTableRecordID1, "--input", "-"); err != nil || !strings.Contains(out, `"done"`) {
		t.Fatalf("table record comment create = (%q, %v)", out, err)
	}
}

func TestTableInvalidInputsAndUUIDsMakeZeroNetworkRequests(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	setTaskTableTestEnv(t, srv.URL)

	outer := t.TempDir()
	workDir := filepath.Join(outer, "work")
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		t.Fatal(err)
	}
	outsideFile := filepath.Join(outer, "outside.json")
	if err := os.WriteFile(outsideFile, []byte(`{"name":"escape"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	symlinkEscape := filepath.Join(workDir, "escape-link.json")
	if err := os.Symlink(outsideFile, symlinkEscape); err != nil {
		t.Fatal(err)
	}
	oversizedFile := filepath.Join(workDir, "huge.json")
	if err := os.WriteFile(oversizedFile, []byte(`{"pad":"`+strings.Repeat("a", (1<<20)+16)+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(workDir)

	cases := []struct {
		name  string
		stdin string
		args  []string
	}{
		{name: "bad table uuid on get", args: []string{"table", "get", "not-a-uuid"}},
		{name: "path traversal in table uuid", args: []string{"table", "get", "../issues"}},
		{name: "bad view uuid on get", args: []string{"table", "get", testTableID1, "--view", "not-a-uuid"}},
		{name: "bad field uuid", stdin: `{"name":"x"}`, args: []string{"table", "field", "update", testTableID1, "bad-id", "--input", "-"}},
		{name: "bad record uuid", stdin: `{"expected_revision":1,"values":{}}`, args: []string{"table", "record", "update", testTableID1, "bad-id", "--input", "-"}},
		{name: "bad view uuid on update", stdin: `{"name":"x"}`, args: []string{"table", "view", "update", testTableID1, "bad-id", "--input", "-"}},
		{name: "bad record uuid on comment list", args: []string{"table", "record", "comment", "list", testTableID1, "bad-id"}},
		{name: "missing input flag", args: []string{"table", "tasks", "rows"}},
		{name: "empty stdin", stdin: "   ", args: []string{"table", "tasks", "rows", "--input", "-"}},
		{name: "array instead of object", stdin: `[1,2,3]`, args: []string{"table", "tasks", "rows", "--input", "-"}},
		{name: "null instead of object", stdin: `null`, args: []string{"table", "tasks", "rows", "--input", "-"}},
		{name: "multiple json objects", stdin: `{"a":1}{"b":2}`, args: []string{"table", "tasks", "rows", "--input", "-"}},
		{name: "malformed json", stdin: `{"a":`, args: []string{"table", "tasks", "rows", "--input", "-"}},
		{name: "relative path traversal", args: []string{"table", "create", "--input", "../outside.json"}},
		{name: "absolute path outside workdir", args: []string{"table", "create", "--input", outsideFile}},
		{name: "symlink escaping workdir", args: []string{"table", "create", "--input", "escape-link.json"}},
		{name: "oversized input file", args: []string{"table", "tasks", "rows", "--input", "huge.json"}},
		{name: "oversized stdin", stdin: `{"pad":"` + strings.Repeat("b", (1<<20)+16) + `"}`, args: []string{"table", "tasks", "rows", "--input", "-"}},
		{name: "record update missing expected_revision", stdin: `{"values":{}}`, args: []string{"table", "record", "update", testTableID1, testTableRecordID1, "--input", "-"}},
		{name: "record update non-integer expected_revision", stdin: `{"expected_revision":"1","values":{}}`, args: []string{"table", "record", "update", testTableID1, testTableRecordID1, "--input", "-"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := hits.Load()
			_, err := runRootTableCommand(t, tc.stdin, tc.args...)
			if err == nil {
				t.Fatalf("expected error for %s, got nil", tc.name)
			}
			if after := hits.Load(); after != before {
				t.Fatalf("%s made %d HTTP requests, want 0", tc.name, after-before)
			}
		})
	}
}

func TestIssuePropertySetRecordRelationAndComputedReadOnly(t *testing.T) {
	var putCalls int
	var lastPutBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/issues/"+testTableIssueID1:
			_, _ = io.WriteString(w, `{"id":"`+testTableIssueID1+`","identifier":"XSH-42","title":"Linked"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/properties":
			_, _ = io.WriteString(w, `{"properties":[
				{"id":"`+testTablePropRelID+`","name":"Linked Row","type":"record_relation","config":{"target_table_id":"`+testTableID1+`"}},
				{"id":"`+testTablePropLookID+`","name":"Row Status","type":"lookup","config":{"relation_property_id":"`+testTablePropRelID+`","target_field_id":"`+testTableFieldID1+`"}},
				{"id":"`+testTablePropFormID+`","name":"Risk Score","type":"formula","config":{"expression":"1 + 1"}}
			]}`)
		case r.Method == http.MethodPut && r.URL.Path == "/api/issues/"+testTableIssueID1+"/properties/"+testTablePropRelID:
			putCalls++
			_ = json.NewDecoder(r.Body).Decode(&lastPutBody)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"properties": map[string]any{
					testTablePropRelID: lastPutBody["value"],
				},
			})
		default:
			t.Errorf("unexpected property request: %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	setTaskTableTestEnv(t, srv.URL)

	// Comma-separated record UUIDs should be encoded as a JSON array of canonical UUIDs.
	out, err := captureStdout(t, func() error {
		_, err := runRootTableCommand(t, "", "issue", "property", "set", testTableIssueID1,
			"--name", "Linked Row",
			"--value", strings.ToUpper(testTableRecordID1)+", "+testTableRecordID2+", "+testTableRecordID1,
			"--output", "json",
		)
		return err
	})
	if err != nil {
		t.Fatalf("issue property set record_relation failed: %v", err)
	}
	if putCalls != 1 {
		t.Fatalf("putCalls = %d, want 1", putCalls)
	}
	gotSlice, ok := lastPutBody["value"].([]any)
	if !ok || len(gotSlice) != 2 || gotSlice[0] != testTableRecordID1 || gotSlice[1] != testTableRecordID2 {
		t.Fatalf("PUT value = %#v, want [%s %s]", lastPutBody["value"], testTableRecordID1, testTableRecordID2)
	}
	if !strings.Contains(out, testTableRecordID1) || !strings.Contains(out, testTableRecordID2) {
		t.Errorf("output missing record IDs:\n%s", out)
	}

	// JSON array form is also accepted.
	_, err = captureStdout(t, func() error {
		_, err := runRootTableCommand(t, "", "issue", "property", "set", testTableIssueID1,
			"--name", "Linked Row",
			"--value", `["`+testTableRecordID2+`"]`,
			"--output", "json",
		)
		return err
	})
	if err != nil {
		t.Fatalf("issue property set JSON array form failed: %v", err)
	}
	if putCalls != 2 {
		t.Fatalf("putCalls = %d, want 2", putCalls)
	}

	// Invalid record UUID fails before PUT.
	_, err = runRootTableCommand(t, "", "issue", "property", "set", testTableIssueID1, "--name", "Linked Row", "--value", "not-a-uuid")
	if err == nil {
		t.Fatal("expected error for invalid record_relation UUID")
	}
	if putCalls != 2 {
		t.Fatalf("invalid UUID triggered PUT (putCalls=%d)", putCalls)
	}

	// Computed properties (lookup, formula) fail fast as read-only before PUT.
	for _, propName := range []string{"Row Status", "Risk Score"} {
		_, err := runRootTableCommand(t, "", "issue", "property", "set", testTableIssueID1, "--name", propName, "--value", "x")
		if err == nil {
			t.Fatalf("expected read-only error for computed property %q", propName)
		}
		if putCalls != 2 {
			t.Fatalf("computed property %q triggered PUT (putCalls=%d)", propName, putCalls)
		}
	}

	// property list --output json preserves target_table_id / relation_property_id / expression.
	listOut, err := captureStdout(t, func() error {
		_, err := runRootTableCommand(t, "", "property", "list", "--output", "json")
		return err
	})
	if err != nil {
		t.Fatalf("property list failed: %v", err)
	}
	for _, want := range []string{testTableID1, testTablePropRelID, testTableFieldID1, `"expression": "1 + 1"`} {
		if !strings.Contains(listOut, want) {
			t.Errorf("property list output missing %s\noutput:\n%s", want, listOut)
		}
	}
}
