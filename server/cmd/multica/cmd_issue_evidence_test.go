package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestIssueEvidenceCLI(t *testing.T) {
	t.Chdir(t.TempDir())
	const input = `{"request_id":"a","question":"Inspect frozen original"}`
	if err := os.WriteFile("input.json", []byte(input), 0600); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/issues/DEV-69/evidence-runs" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if r.Method == "GET" {
			io.WriteString(w, `[{"output":"frozen result"}]`)
			return
		}
		body, err := io.ReadAll(r.Body)
		if r.Method != "POST" || err != nil || string(bytes.TrimSpace(body)) != input {
			t.Errorf("wrong request %s %s", r.Method, body)
		}
		io.WriteString(w, `{"task_id":"queued-task"}`)
	}))
	defer srv.Close()
	setCLITestServerEnv(t, srv.URL)
	for _, tc := range []struct {
		args []string
		want string
	}{{[]string{"DEV-69"}, "frozen result"}, {[]string{"DEV-69", "--request-file", "input.json"}, "queued-task"}} {
		cmd := newIssueEvidenceCommand()
		var stdout bytes.Buffer
		cmd.SetOut(&stdout)
		cmd.SetArgs(tc.args)
		if err := cmd.Execute(); err != nil || !strings.Contains(stdout.String(), tc.want) {
			t.Fatalf("args=%v output=%q err=%v", tc.args, stdout.String(), err)
		}
	}
}
