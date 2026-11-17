package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

const testGoalUUID = "fe02ac84-e258-4286-9918-5c23bde88e7c"

// newGoalTestCmd mirrors the flag surface of one goal subcommand so the run
// functions can be exercised without the package-level cobra commands, which
// cannot be reused across tests.
func newGoalTestCmd(use string) *cobra.Command {
	cmd := &cobra.Command{Use: use}
	cmd.Flags().String("output", "table", "")
	cmd.Flags().Int("limit", goalIssueListDefaultLimit, "")
	cmd.Flags().Int("offset", 0, "")
	cmd.Flags().Bool("all", false, "")
	cmd.Flags().String("file", "", "")
	return cmd
}

// prepareGoalCLITest points the API client at srv and clears the task-scoped
// environment the agent runtime exports, which would otherwise make the CLI
// demand a mat_ token.
func prepareGoalCLITest(t *testing.T, srvURL string) {
	t.Helper()
	t.Chdir(t.TempDir())
	t.Setenv("MULTICA_SERVER_URL", srvURL)
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-1")
	t.Setenv("MULTICA_TOKEN", "test-token")
	t.Setenv("MULTICA_AGENT_ID", "")
	t.Setenv("MULTICA_TASK_ID", "")
	t.Setenv("MULTICA_DAEMON_PORT", "")
}

func goalIssuesBody(ids ...string) map[string]any {
	issues := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		issues = append(issues, map[string]any{
			"id":         id,
			"identifier": strings.ToUpper(id),
			"title":      "task " + id,
			"status":     "todo",
			"priority":   "high",
		})
	}
	return map[string]any{"issues": issues, "total": len(issues)}
}

func TestRunGoalGetSendsWorkspaceHeaderAndPrintsJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/goals/"+testGoalUUID {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("X-Workspace-ID"); got != "ws-1" {
			t.Errorf("X-Workspace-ID = %q, want ws-1", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":         testGoalUUID,
			"title":      "个人第二大脑",
			"status":     "active",
			"project_id": "75cf9eae-304a-44a7-939d-0ad6825213b1",
		})
	}))
	defer srv.Close()
	prepareGoalCLITest(t, srv.URL)

	cmd := newGoalTestCmd("get")
	_ = cmd.Flags().Set("output", "json")
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := runGoalGet(cmd, []string{testGoalUUID}); err != nil {
		t.Fatalf("runGoalGet: %v", err)
	}
	var goal map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &goal); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout.String())
	}
	if goal["title"] != "个人第二大脑" {
		t.Errorf("title = %v", goal["title"])
	}
}

func TestRunGoalIssuesAllReadsUntilEmptyPage(t *testing.T) {
	var offsets []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/goals/"+testGoalUUID+"/issues" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("limit"); got != "3" {
			t.Errorf("limit = %q, want 3", got)
		}
		offset := r.URL.Query().Get("offset")
		offsets = append(offsets, offset)
		switch offset {
		case "":
			// The server applies a smaller page than --limit asked for, and
			// its count query failed so total is just the page length.
			_ = json.NewEncoder(w).Encode(goalIssuesBody("a", "b"))
		case "2":
			_ = json.NewEncoder(w).Encode(goalIssuesBody("c", "d"))
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"issues": []any{}, "total": 4})
		}
	}))
	defer srv.Close()
	prepareGoalCLITest(t, srv.URL)

	cmd := newGoalTestCmd("issues")
	_ = cmd.Flags().Set("limit", "3")
	_ = cmd.Flags().Set("all", "true")
	_ = cmd.Flags().Set("output", "json")
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := runGoalIssues(cmd, []string{testGoalUUID}); err != nil {
		t.Fatalf("runGoalIssues: %v", err)
	}
	var result struct {
		Issues  []map[string]any `json:"issues"`
		HasMore bool             `json:"has_more"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout.String())
	}
	if len(result.Issues) != 4 {
		t.Fatalf("collected %d issues, want 4: %s", len(result.Issues), stdout.String())
	}
	if result.HasMore {
		t.Error("has_more should be false after reading every page")
	}
	if strings.Join(offsets, ",") != ",2,4" {
		t.Errorf("offsets = %v, want [\"\", \"2\", \"4\"]", offsets)
	}
}

func TestRunGoalIssuesSinglePageTrustsLargerTotal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("limit") != "2" {
			t.Errorf("limit = %q, want 2", r.URL.Query().Get("limit"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issues": goalIssuesBody("a", "b")["issues"],
			"total":  15,
		})
	}))
	defer srv.Close()
	prepareGoalCLITest(t, srv.URL)

	cmd := newGoalTestCmd("issues")
	_ = cmd.Flags().Set("limit", "2")
	_ = cmd.Flags().Set("output", "json")
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := runGoalIssues(cmd, []string{testGoalUUID}); err != nil {
		t.Fatalf("runGoalIssues: %v", err)
	}
	var result struct {
		Total   int  `json:"total"`
		HasMore bool `json:"has_more"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout.String())
	}
	if result.Total != 15 || !result.HasMore {
		t.Errorf("total=%d has_more=%v, want 15 and true", result.Total, result.HasMore)
	}
}

func TestRunGoalIssuesDoesNotEndOnUnreliableTotal(t *testing.T) {
	var offsets []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A failed count reports the page length as total. A page whose total
		// equals its own length must not end an --all walk early.
		offset := r.URL.Query().Get("offset")
		offsets = append(offsets, offset)
		switch offset {
		case "":
			_ = json.NewEncoder(w).Encode(goalIssuesBody("a"))
		case "1":
			_ = json.NewEncoder(w).Encode(goalIssuesBody("b"))
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"issues": []any{}, "total": 0})
		}
	}))
	defer srv.Close()
	prepareGoalCLITest(t, srv.URL)

	cmd := newGoalTestCmd("issues")
	_ = cmd.Flags().Set("limit", "1")
	_ = cmd.Flags().Set("all", "true")
	_ = cmd.Flags().Set("output", "json")
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := runGoalIssues(cmd, []string{testGoalUUID}); err != nil {
		t.Fatalf("runGoalIssues: %v", err)
	}
	var result struct {
		Issues []map[string]any `json:"issues"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout.String())
	}
	if len(result.Issues) != 2 {
		t.Fatalf("collected %d issues, want 2: %s", len(result.Issues), stdout.String())
	}
	if strings.Join(offsets, ",") != ",1,2" {
		t.Errorf("offsets = %v, want [\"\", \"1\", \"2\"]", offsets)
	}
}

func TestRunGoalLinkContinuesAfterConflict(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Fatalf("method = %s, want PUT", r.Method)
		}
		paths = append(paths, r.URL.Path)
		if strings.HasSuffix(r.URL.Path, "/DEV-70") {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":"the issue is already linked to another goal","code":"issue_already_linked"}`))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	prepareGoalCLITest(t, srv.URL)

	cmd := newGoalTestCmd("link")
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err := runGoalLink(cmd, []string{testGoalUUID, "DEV-68", "DEV-70", "DEV-71"})
	if err != errSilent {
		t.Fatalf("err = %v, want errSilent so the command exits non-zero", err)
	}
	if got := len(paths); got != 3 {
		t.Fatalf("made %d requests, want all 3 attempted: %v", got, paths)
	}
	if !strings.Contains(stdout.String(), "Linked DEV-68") || !strings.Contains(stdout.String(), "Linked DEV-71") {
		t.Errorf("stdout missing successful links: %s", stdout.String())
	}
	if strings.Contains(stdout.String(), "DEV-70") {
		t.Errorf("conflicting link must not be reported as linked: %s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "already linked to another goal") || !strings.Contains(stderr.String(), "issue_already_linked") {
		t.Errorf("stderr missing the server reason and code: %s", stderr.String())
	}
}

func TestRunGoalLinkJSONReportsEveryResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/DEV-70") {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":"the issue is already linked to another goal","code":"issue_already_linked"}`))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	prepareGoalCLITest(t, srv.URL)

	cmd := newGoalTestCmd("link")
	_ = cmd.Flags().Set("output", "json")
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := runGoalLink(cmd, []string{testGoalUUID, "DEV-68", "DEV-70"}); err != errSilent {
		t.Fatalf("err = %v, want errSilent", err)
	}
	var result struct {
		GoalID  string           `json:"goal_id"`
		Results []map[string]any `json:"results"`
		Failed  int              `json:"failed"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout.String())
	}
	if result.GoalID != testGoalUUID || result.Failed != 1 || len(result.Results) != 2 {
		t.Fatalf("unexpected summary: %+v", result)
	}
	if result.Results[0]["ok"] != true || result.Results[1]["ok"] != false {
		t.Errorf("per-item results = %+v", result.Results)
	}
	if stderr.Len() != 0 {
		t.Errorf("JSON mode must keep stderr free of per-item lines: %s", stderr.String())
	}
}

func TestRunGoalUnlinkIsIdempotent(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Fatalf("method = %s, want DELETE", r.Method)
		}
		paths = append(paths, r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	prepareGoalCLITest(t, srv.URL)

	cmd := newGoalTestCmd("unlink")
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := runGoalUnlink(cmd, []string{testGoalUUID, "DEV-68", "DEV-69"}); err != nil {
		t.Fatalf("runGoalUnlink: %v", err)
	}
	if len(paths) != 2 {
		t.Fatalf("paths = %v", paths)
	}
	if !strings.Contains(stdout.String(), "Unlinked DEV-68") || !strings.Contains(stdout.String(), "Unlinked DEV-69") {
		t.Errorf("stdout = %s", stdout.String())
	}
}

func TestRunGoalRecordStillCreatesGoal(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/goals" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": testGoalUUID, "title": "目标"})
	}))
	defer srv.Close()
	prepareGoalCLITest(t, srv.URL)

	path := t.TempDir() + "/goal.json"
	if err := os.WriteFile(path, []byte(`{"title":"目标","issue_ids":["DEV-1"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := newGoalTestCmd("record")
	_ = cmd.Flags().Set("file", path)
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := runGoalRecord(cmd, nil); err != nil {
		t.Fatalf("runGoalRecord: %v", err)
	}
	if body["title"] != "目标" {
		t.Errorf("posted body = %+v", body)
	}
	if !strings.Contains(stdout.String(), "](/goals/"+testGoalUUID+")") {
		t.Errorf("stdout = %s", stdout.String())
	}
}
