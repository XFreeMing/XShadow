//go:build linux

package daemon_test

// Process-level verification of the goal plan run chain.
//
// The test starts the real server binary and the real daemon binary from this
// tree, forces task claiming over the production HTTP route (the transport
// proxy below refuses WebSocket upgrades), and lets a test-owned `opencode`
// executable read the prompt the daemon received. The fake executor locates the
// controlled local_directory checkout through the absolute path in the prompt,
// reads its repo SHA and file bytes, reads a seeded existing task through the
// run's own task-token authorization, and submits the plan with this tree's CLI
// through the real task-token auth path.
//
// Everything is opt-in because it needs a migrated, isolated database and two
// built binaries:
//
//	MULTICA_CHAIN_TEST=1
//	MULTICA_CHAIN_SERVER_BIN   absolute path to ./cmd/server build
//	MULTICA_CHAIN_CLI_BIN      absolute path to ./cmd/multica build
//	MULTICA_CHAIN_DATABASE_URL isolated PostgreSQL URL (port 15432 here)
//	MULTICA_CHAIN_PORT         isolated API port
//	MULTICA_CHAIN_PROFILE_CONFIG  seeded CLI profile JSON (server_url/token/workspace_id)
//	MULTICA_CHAIN_ARTIFACTS    directory for raw logs and evidence
//	MULTICA_CHAIN_EXPECT_DB    optional exact database name assertion
//	MULTICA_CHAIN_STABLE_DAEMON_PORT_FILE  host daemon.port record to prove the
//	                           started PIDs are not the stable daemon's
//	MULTICA_CHAIN_STABLE_CLI_BIN  runtime CLI used for the stop-time
//	                           `daemon status --output json` check
//	MULTICA_CHAIN_STABLE_PROFILE  profile of the stable daemon that CLI reports
//	MULTICA_CHAIN_STABLE_HOME  real home holding that CLI's profile
//
// Immediately before every signal, the test asks the runtime CLI for the stable
// daemon's current pid/status. A failed query, invalid JSON, non-running
// status, or a pid that matches the child refuses the signal and records the
// unstopped pid, process group, log and reason. The answer is never cached
// between signals. Children are only ever stopped through the exact process
// group this test started, never by executable name.
//
// Child processes are started with an explicit environment (exec.Cmd.Env), so
// no ambient MULTICA_* value, task token, or host PATH entry reaches them.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	defaultHealth = 19514
)

type chainConfig struct {
	serverBin     string
	cliBin        string
	dbURL         string
	apiPort       string
	profileConfig string
	artifacts     string
	expectDB      string
	stableCLIBin  string
	stableProfile string
	stableHome    string
}

type httpResult struct {
	Status int
	Body   []byte
}

func loadChainConfig(t *testing.T) chainConfig {
	t.Helper()
	if os.Getenv("MULTICA_CHAIN_TEST") != "1" {
		t.Skip("set MULTICA_CHAIN_TEST=1 to run the process-level goal plan chain")
	}
	cfg := chainConfig{
		serverBin:     os.Getenv("MULTICA_CHAIN_SERVER_BIN"),
		cliBin:        os.Getenv("MULTICA_CHAIN_CLI_BIN"),
		dbURL:         os.Getenv("MULTICA_CHAIN_DATABASE_URL"),
		apiPort:       os.Getenv("MULTICA_CHAIN_PORT"),
		profileConfig: os.Getenv("MULTICA_CHAIN_PROFILE_CONFIG"),
		artifacts:     os.Getenv("MULTICA_CHAIN_ARTIFACTS"),
		expectDB:      os.Getenv("MULTICA_CHAIN_EXPECT_DB"),
		stableCLIBin:  os.Getenv("MULTICA_CHAIN_STABLE_CLI_BIN"),
		stableProfile: os.Getenv("MULTICA_CHAIN_STABLE_PROFILE"),
		stableHome:    os.Getenv("MULTICA_CHAIN_STABLE_HOME"),
	}
	for name, value := range map[string]string{
		"MULTICA_CHAIN_SERVER_BIN":     cfg.serverBin,
		"MULTICA_CHAIN_CLI_BIN":        cfg.cliBin,
		"MULTICA_CHAIN_DATABASE_URL":   cfg.dbURL,
		"MULTICA_CHAIN_PORT":           cfg.apiPort,
		"MULTICA_CHAIN_PROFILE_CONFIG": cfg.profileConfig,
		"MULTICA_CHAIN_ARTIFACTS":      cfg.artifacts,
		"MULTICA_CHAIN_STABLE_CLI_BIN": cfg.stableCLIBin,
		"MULTICA_CHAIN_STABLE_PROFILE": cfg.stableProfile,
		"MULTICA_CHAIN_STABLE_HOME":    cfg.stableHome,
	} {
		if strings.TrimSpace(value) == "" {
			t.Fatalf("%s is required", name)
		}
	}
	for _, bin := range []string{cfg.serverBin, cfg.cliBin} {
		if st, err := os.Stat(bin); err != nil || st.IsDir() || st.Mode()&0o111 == 0 {
			t.Fatalf("%s is not an executable file: %v", bin, err)
		}
	}
	assertIsolatedDatabase(t, cfg.dbURL, cfg.expectDB)
	if _, err := os.Stat(cfg.profileConfig); err != nil {
		t.Fatalf("profile config: %v", err)
	}
	if err := os.MkdirAll(cfg.artifacts, 0o755); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// assertIsolatedDatabase refuses to run against the shared stable databases.
func assertIsolatedDatabase(t *testing.T, raw, expect string) {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse database URL: %v", err)
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		t.Fatalf("database URL scheme = %q", u.Scheme)
	}
	if u.Port() != "15432" {
		t.Fatalf("database URL must target PostgreSQL on 15432, got %q", u.Port())
	}
	if host := u.Hostname(); host != "127.0.0.1" && host != "localhost" {
		t.Fatalf("database URL host = %q", host)
	}
	db := strings.TrimPrefix(u.Path, "/")
	if db == "" {
		t.Fatal("database URL has no database name")
	}
	if db == "multica_xshadow_623" || db == "multica_xshadow_goal_dev_384" || db == "multica" {
		t.Fatalf("refusing shared/stable database %q", db)
	}
	if expect != "" && db != expect {
		t.Fatalf("database %q is not the expected isolated database %q", db, expect)
	}
}

type profileFile struct {
	ServerURL   string `json:"server_url"`
	WorkspaceID string `json:"workspace_id"`
	Token       string `json:"token"`
}

func readProfile(t *testing.T, path string) profileFile {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var p profileFile
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("parse profile: %v", err)
	}
	if !strings.HasPrefix(p.Token, "mul_") {
		t.Fatalf("expected a real mul_ PAT in the seeded profile, got prefix %q", tokenPrefix(p.Token))
	}
	if p.WorkspaceID == "" || p.WorkspaceID == "00000000-0000-0000-0000-000000000000" {
		t.Fatal("profile has no workspace id")
	}
	return p
}

func tokenPrefix(token string) string {
	if token == "" {
		return ""
	}
	if len(token) < 4 {
		return token
	}
	return token[:4]
}

type apiClient struct {
	base      string
	token     string
	workspace string
	client    *http.Client
}

func newAPIClient(base, token, workspace string) *apiClient {
	return &apiClient{
		base:      strings.TrimRight(base, "/"),
		token:     token,
		workspace: workspace,
		client: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				Proxy: nil,
			},
		},
	}
}

func (c *apiClient) do(t *testing.T, method, path string, body any) httpResult {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, c.base+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("X-Workspace-ID", c.workspace)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("%s %s: read body: %v", method, path, err)
	}
	return httpResult{Status: resp.StatusCode, Body: raw}
}

func (c *apiClient) mustJSON(t *testing.T, method, path string, body any, out any, want ...int) {
	t.Helper()
	res := c.do(t, method, path, body)
	if len(want) > 0 {
		ok := false
		for _, status := range want {
			if res.Status == status {
				ok = true
				break
			}
		}
		if !ok {
			t.Fatalf("%s %s: status %d, body %.2000s", method, path, res.Status, res.Body)
		}
	}
	if out != nil {
		if err := json.Unmarshal(res.Body, out); err != nil {
			t.Fatalf("%s %s: decode %.2000s: %v", method, path, res.Body, err)
		}
	}
}

func writeArtifact(t *testing.T, dir, name string, value any) {
	t.Helper()
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func waitFor(t *testing.T, timeout time.Duration, what string, fn func() (bool, string)) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		ok, detail := fn()
		if ok {
			return
		}
		last = detail
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s; last: %s", timeout, what, last)
}

// fakeOpenCode is the test-owned executor. It is the only agent CLI on the
// daemon's PATH, and it reads the prompt the daemon actually handed it.
const fakeOpenCode = `#!/bin/sh
PATH=/usr/bin:/bin
D="$CHAIN_FAKE_DIR"
mkdir -p "$D"
printf '%s\n' "argv=$*" >> "$D/invocations.log"
case "$1" in
--version) echo 1.18.31; exit 0;;
models) exit 0;;
run) ;;
*) echo "unexpected argv: $*" >&2; exit 1;;
esac
cat > "$D/prompt.txt"
pwd > "$D/pwd.txt"
tok="$MULTICA_TOKEN"
printf 'task=%s\nagent=%s\nworkspace=%s\nserver=%s\ntoken_prefix=%s\ntoken_len=%s\n' \
  "$MULTICA_TASK_ID" "$MULTICA_AGENT_ID" "$MULTICA_WORKSPACE_ID" "$MULTICA_SERVER_URL" \
  "$(printf '%s' "$tok" | cut -c1-4)" "$(printf '%s' "$tok" | wc -c)" > "$D/task-env.txt"
goal=$(sed -n 's/.*--goal \([0-9a-f-]*\) --generation.*/\1/p' "$D/prompt.txt" | head -n 1)
gen=$(sed -n 's/.*--generation \([0-9a-f-]*\).*/\1/p' "$D/prompt.txt" | head -n 1)
proj=$(sed -n 's/.*- project_id: \([0-9a-f-]*\).*/\1/p' "$D/prompt.txt" | head -n 1)
path=$(sed -n 's/.*"local_path":[[:space:]]*"\([^"]*\)".*/\1/p' "$D/prompt.txt" | head -n 1)
rid=$(sed -n 's/.*- id=\([0-9a-f-]*\) type=local_directory.*/\1/p' "$D/prompt.txt" | head -n 1)
[ -z "$rid" ] && rid="$path"
printf 'goal=%s\ngeneration=%s\nproject=%s\nresource_path=%s\nresource_id=%s\n' "$goal" "$gen" "$proj" "$path" "$rid" > "$D/parsed.txt"
if [ -z "$goal" ] || [ -z "$gen" ] || [ -z "$proj" ] || [ -z "$path" ]; then
  echo "prompt parse failed: goal=$goal gen=$gen project=$proj path=$path" > "$D/cli.err"
  exit 2
fi
# The controlled local_directory is a real checkout; read its actual HEAD.
repo_sha=$(git -C "$path" rev-parse HEAD 2> "$D/git.err")
# Read the file once and hash exactly the bytes that were read.
cat "$path/context.md" > "$D/context-read.bin"
sha=$(sha256sum "$D/context-read.bin" | cut -d' ' -f1)
content=$(cat "$D/context-read.bin")
read_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
# Read the seeded existing task through the run's own task-token authorization.
"$MULTICA_CLI_PATH" issue list --project "$proj" --output json > "$D/issues.json" 2> "$D/issues.err"
printf '%s\n' "$?" > "$D/issues.rc"
issue_line=$(python3 - "$D/issues.json" <<'PY'
import json, sys
try:
    doc = json.load(open(sys.argv[1]))
except Exception:
    sys.exit(1)
issues = doc.get("issues") or []
pick = next((i for i in issues if str(i.get("title", "")).startswith("DEV-6 chain existing task")), issues[0] if issues else None)
if not pick:
    sys.exit(1)
print("\t".join([str(pick.get("id", "")), str(pick.get("revision", 0)), str(pick.get("title", "")), str(pick.get("identifier", ""))]))
PY
)
issue_id=$(printf '%s' "$issue_line" | cut -f1)
issue_rev=$(printf '%s' "$issue_line" | cut -f2)
issue_title=$(printf '%s' "$issue_line" | cut -f3)
issue_ident=$(printf '%s' "$issue_line" | cut -f4)
printf 'issue_id=%s\nrevision=%s\ntitle=%s\nidentifier=%s\nread_at=%s\n' \
  "$issue_id" "$issue_rev" "$issue_title" "$issue_ident" "$read_at" > "$D/tasks-read.txt"
printf 'context_sha256=%s\ncontext=%s\nrepo_sha=%s\nread_at=%s\n' "$sha" "$content" "$repo_sha" "$read_at" > "$D/context-read.txt"
if [ -z "$repo_sha" ] || [ -z "$issue_id" ] || [ -z "$issue_rev" ]; then
  echo "controlled read failed: repo_sha=$repo_sha issue_id=$issue_id revision=$issue_rev" > "$D/cli.err"
  exit 3
fi
CHAIN_REPO_SHA="$repo_sha" CHAIN_CONTEXT_SHA="$sha" CHAIN_CONTEXT_CONTENT="$content" \
CHAIN_READ_AT="$read_at" CHAIN_ISSUE_ID="$issue_id" CHAIN_ISSUE_REV="$issue_rev" \
CHAIN_ISSUE_TITLE="$issue_title" CHAIN_ISSUE_IDENT="$issue_ident" \
CHAIN_PATH="$path" CHAIN_RESOURCE_ID="$rid" \
python3 - "$D/plan.json" <<'PY'
import json, os, sys

env = os.environ
item_a = "a1111111-1111-4111-8111-111111111111"
item_b = "b2222222-2222-4222-8222-222222222222"
item_c = "c3333333-3333-4333-8333-333333333333"
file_ref = {
    "kind": "file",
    "ref": env["CHAIN_PATH"] + "/context.md",
    "version": "sha256:" + env["CHAIN_CONTEXT_SHA"],
    "note": env["CHAIN_CONTEXT_CONTENT"],
}
task_ref = {
    "kind": "task",
    "ref": env["CHAIN_ISSUE_ID"],
    "version": "revision " + env["CHAIN_ISSUE_REV"],
    "note": env["CHAIN_ISSUE_TITLE"] + " (" + env["CHAIN_ISSUE_IDENT"] + ")",
}
plan = {
    "items": [
        {
            "id": item_a,
            "title": "Controlled checkout item",
            "outcome": "The planning run produced this item from the checkout it actually read.",
            "acceptance_criteria": "The stored candidate records the checkout repo SHA and the digest of the bytes read.",
            "evidence_refs": [file_ref],
            "binding": {"type": "new"},
            "coordination": {"parallel_reason": "Independent of the seeded existing task; both can start together."},
            "stage": None,
        },
        {
            "id": item_b,
            "title": "Existing task item",
            "outcome": "The planning run read the seeded task through its task-token authorization.",
            "acceptance_criteria": "The stored evidence names the task UUID and the revision that was read.",
            "evidence_refs": [task_ref],
            "binding": {"type": "existing", "issue_id": env["CHAIN_ISSUE_ID"]},
            "coordination": {"parallel_reason": "Read-only task evidence; independent of the checkout item."},
            "stage": None,
        },
        {
            "id": item_c,
            "title": "Dependent item",
            "outcome": "This item consumes both the checkout context and the seeded task evidence.",
            "acceptance_criteria": "The stored edge carries the reason it must wait for the checkout item.",
            "evidence_refs": [file_ref, task_ref],
            "binding": {"type": "new"},
            "stage": None,
        },
    ],
    "edges": [
        {
            "dependent_item_id": item_c,
            "prerequisite_item_id": item_a,
            "reason": "The dependent item consumes the context the controlled checkout item reads first.",
        },
    ],
    "context_snapshot": {
        "repo_sha": env["CHAIN_REPO_SHA"],
        "files": [
            {
                "path": env["CHAIN_PATH"] + "/context.md",
                "lines": "1",
                "summary": env["CHAIN_CONTEXT_CONTENT"],
                "sha256": env["CHAIN_CONTEXT_SHA"],
            }
        ],
        "tasks": [
            {
                "issue_id": env["CHAIN_ISSUE_ID"],
                "revision": int(env["CHAIN_ISSUE_REV"]),
                "read_at": env["CHAIN_READ_AT"],
            }
        ],
        "resources": [{"id": env["CHAIN_RESOURCE_ID"], "read": True}],
        "missing": [],
        "read_at": env["CHAIN_READ_AT"],
    },
}
with open(sys.argv[1], "w", encoding="utf-8") as f:
    json.dump(plan, f, ensure_ascii=False)
PY
"$MULTICA_CLI_PATH" goal plan-result --goal "$goal" --generation "$gen" --file "$D/plan.json" > "$D/cli.out" 2> "$D/cli.err"
rc=$?
printf '%s\n' "$rc" > "$D/cli.rc"
if [ "$rc" -ne 0 ]; then exit "$rc"; fi
printf '%s\n' '{"type":"step_start","sessionID":"ses-chain","part":{"type":"step-start"}}'
printf '%s\n' "{\"type\":\"text\",\"sessionID\":\"ses-chain\",\"part\":{\"type\":\"text\",\"text\":\"submitted plan for $gen\"}}"
printf '%s\n' '{"type":"step_finish","sessionID":"ses-chain","part":{"type":"step-finish","reason":"stop","tokens":{"input":1,"output":1}}}'
exit 0
`

type childProc struct {
	name string
	cmd  *exec.Cmd
	log  *os.File
	done chan error

	// guard, when set, is consulted immediately before this process receives a
	// signal: it asks the runtime CLI which daemon is the stable one.
	guard *stableDaemonGuard

	// stopOnce makes cleanup single-shot: the t.Cleanup registered right after a
	// successful start and any explicit stop call share one signal sequence, so
	// an early failure never stops the same process twice.
	stopOnce   sync.Once
	stopResult map[string]any

	// expectRefusal is set only by regression tests that drive an unverified
	// stop on purpose. Everywhere else a refusal fails the run.
	expectRefusal bool
}

// stableDaemonGuard asks the runtime CLI for the stable daemon's current
// identity. It holds no cached answer: stopProcess calls it immediately before
// every signal, because an answer from an earlier signal says nothing about
// the process group about to be signalled.
type stableDaemonGuard struct {
	cliBin  string
	profile string
	home    string
	workDir string

	// query replaces the runtime CLI in tests. It returns the same shape as the
	// CLI path: stable_daemon_pid/stable_daemon_status on a verified answer, or
	// an "error" entry when the daemon cannot be identified.
	query func(context.Context) map[string]any
}

func newStableDaemonGuard(cliBin, profile, home, workDir string) *stableDaemonGuard {
	return &stableDaemonGuard{cliBin: cliBin, profile: profile, home: home, workDir: workDir}
}

// check performs a fresh query. Runtime CLI failures, timeouts and unparsable
// output all come back as an "error" entry, which the caller must treat as a
// refusal to signal rather than as a missing answer to ignore.
func (g *stableDaemonGuard) check() map[string]any {
	if g.query != nil {
		return g.query(context.Background())
	}
	out := map[string]any{"cli": g.cliBin, "profile": g.profile}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, g.cliBin, "daemon", "status", "--profile", g.profile, "--output", "json")
	cmd.Dir = g.workDir
	cmd.Env = []string{
		"HOME=" + g.home,
		"PATH=/usr/bin:/bin",
		"TMPDIR=" + g.workDir,
	}
	raw, err := cmd.Output()
	if err != nil {
		out["error"] = err.Error()
		return out
	}
	for key, value := range decodeDaemonStatus(raw) {
		out[key] = value
	}
	return out
}

// decodeDaemonStatus turns one CLI response into the guard result. Invalid
// JSON, a missing pid, or a status other than running all become an "error"
// entry, which stops the caller from sending a signal.
func decodeDaemonStatus(raw []byte) map[string]any {
	out := map[string]any{}
	var rec struct {
		PID    int    `json:"pid"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(raw, &rec); err != nil {
		out["error"] = "decode daemon status: " + err.Error()
		return out
	}
	out["stable_daemon_pid"] = rec.PID
	out["stable_daemon_status"] = rec.Status
	if rec.PID <= 0 || rec.Status != "running" {
		out["error"] = fmt.Sprintf("daemon status=%q pid=%d", rec.Status, rec.PID)
	}
	return out
}

func startChild(t *testing.T, name, workDir, bin string, args, env []string, logPath string, guard *stableDaemonGuard) *childProc {
	t.Helper()
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, args...)
	// Never inherit the test process's cwd: it sits inside an agent task
	// workdir, whose daemon-task marker would make the CLI refuse to start a
	// local daemon. The child gets the isolated test root instead.
	cmd.Dir = workDir
	cmd.Env = env
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		logFile.Close()
		t.Fatalf("start %s: %v", name, err)
	}
	p := &childProc{name: name, cmd: cmd, log: logFile, done: make(chan error, 1), guard: guard}
	// Register cleanup before anything else can fail. Every later t.Fatal or
	// panic path (health check, PID validation, daemon log creation or start)
	// then unwinds through this stop without a separate defer having to be
	// reached first.
	t.Cleanup(func() { p.stop(t, 30*time.Second) })
	go func() { p.done <- cmd.Wait() }()
	t.Logf("%s started pid=%d", name, cmd.Process.Pid)
	return p
}

// stop terminates exactly the process group this test started (Setpgid makes
// the child pid the group id) and records how it exited. Repeated calls return
// the first result and send no further signal. Each individual signal is gated
// by its own fresh stable-daemon query.
func (p *childProc) stop(t *testing.T, timeout time.Duration) map[string]any {
	t.Helper()
	p.stopOnce.Do(func() { p.stopResult = p.stopProcess(t, timeout) })
	return p.stopResult
}

func (p *childProc) stopProcess(t *testing.T, timeout time.Duration) map[string]any {
	result := map[string]any{"name": p.name, "signals_sent": 0}
	checks := []map[string]any{}
	finish := func() map[string]any {
		result["stop_time_checks"] = checks
		p.log.Close()
		return result
	}
	if p.cmd.Process == nil {
		result["pid"] = 0
		result["stopped_by"] = "not-started"
		return finish()
	}
	pid := p.cmd.Process.Pid
	result["pid"] = pid
	result["process_group"] = -pid
	result["log"] = p.log.Name()
	if pid <= 0 {
		result["stopped_by"] = "not-started"
		return finish()
	}

	// The runtime CLI answer immediately before SIGTERM. No answer, an
	// unparsable answer, or an answer naming this pid refuses the signal.
	if !p.verifyBeforeSignal(t, pid, "before SIGTERM", &checks, result) {
		result["stopped_by"] = "refused-unverified-SIGTERM"
		result["unstopped_pid"] = pid
		result["alive_at_refusal"] = processAlive(pid)
		return finish()
	}
	result["signals_sent"] = 1
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil && err != syscall.ESRCH {
		result["signal_error"] = err.Error()
	}
	select {
	case <-p.done:
		result["stopped_by"] = "SIGTERM"
	case <-time.After(timeout):
		// Escalating to SIGKILL is a second signal, so it needs its own fresh
		// verification. If that query fails, the process is deliberately left
		// running and reported: signalling on an unverified answer is not an
		// option, and neither is an unverified kill.
		if !p.verifyBeforeSignal(t, pid, "before SIGKILL", &checks, result) {
			result["stopped_by"] = "refused-unverified-SIGKILL"
			result["unstopped_pid"] = pid
			result["alive_at_refusal"] = processAlive(pid)
			return finish()
		}
		result["signals_sent"] = 2
		if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
			result["signal_error"] = err.Error()
		}
		<-p.done
		result["stopped_by"] = "SIGKILL"
	}
	return finish()
}

// verifyBeforeSignal asks the guard for the stable daemon's current identity
// and records the answer. It returns false, and fails the run, whenever the
// answer cannot establish that this exact pid is safe to signal.
func (p *childProc) verifyBeforeSignal(t *testing.T, pid int, signal string, checks *[]map[string]any, result map[string]any) bool {
	t.Helper()
	entry := map[string]any{"signal": signal}
	if p.guard == nil {
		entry["error"] = "no runtime CLI guard configured"
	} else {
		for key, value := range p.guard.check() {
			entry[key] = value
		}
		if msg, _ := entry["error"].(string); msg == "" {
			stablePID, _ := entry["stable_daemon_pid"].(int)
			status, _ := entry["stable_daemon_status"].(string)
			switch {
			case stablePID <= 0 || status != "running":
				entry["error"] = fmt.Sprintf("unverifiable daemon status=%q pid=%d", status, stablePID)
			case stablePID == pid:
				entry["error"] = fmt.Sprintf("runtime CLI reports own pid %d as the stable daemon", pid)
			}
		}
	}
	*checks = append(*checks, entry)
	msg, _ := entry["error"].(string)
	if msg == "" {
		return true
	}
	result["refusal"] = signal + ": " + msg
	if !p.expectRefusal {
		t.Errorf("refusing to signal %s pid %d (%s): %s", p.name, pid, signal, msg)
	}
	return false
}

// chainCleanup collects every child started by the chain test. The report is
// written from a t.Cleanup registered before the first start, so it runs after
// the per-process cleanups no matter where the test stopped.
type chainCleanup struct {
	mu         sync.Mutex
	procs      []*childProc
	stablePID  int
	profileDir string
}

func (c *chainCleanup) add(p *childProc) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.procs = append(c.procs, p)
}

func (c *chainCleanup) write(t *testing.T, artifacts, pat string) {
	report := map[string]any{"stable_daemon_pid": c.stablePID, "proxy": "closed"}
	processes := []map[string]any{}
	touched := false
	c.mu.Lock()
	procs := append([]*childProc(nil), c.procs...)
	c.mu.Unlock()
	for _, p := range procs {
		res := p.stop(t, 30*time.Second)
		processes = append(processes, res)
		report[p.name] = res
		if pid, ok := res["pid"].(int); ok && c.stablePID != 0 && pid == c.stablePID {
			touched = true
		}
	}
	report["processes"] = processes
	report["stable_daemon_touched"] = touched
	report["stop_time_verification"] = "fresh runtime CLI daemon status --output json immediately before every SIGTERM and SIGKILL; no signal on an unverified answer"
	for _, name := range []string{"server", "daemon"} {
		if res, ok := report[name].(map[string]any); ok {
			report[name+"_pid"] = res["pid"]
		}
	}

	// The foreground daemon routes structured logs into its profile's rotating
	// daemon.log rather than the inherited stdout, so copy it out of the
	// temporary HOME before it disappears.
	copied := []string{}
	for _, name := range []string{"daemon.log", "daemon.err.log"} {
		raw, err := os.ReadFile(filepath.Join(c.profileDir, name))
		if err != nil {
			continue
		}
		if os.WriteFile(filepath.Join(artifacts, "profile-"+name), raw, 0o600) == nil {
			copied = append(copied, "profile-"+name)
		}
	}
	report["profile_logs"] = copied
	writeArtifact(t, artifacts, "cleanup.json", report)
	assertNoCredentials(t, artifacts, pat)
}

// stableDaemonPID reads the daemon.port record of the host's stable daemon.
// The package TestMain points HOME at a scratch directory, so the real path
// must be supplied through MULTICA_CHAIN_STABLE_DAEMON_PORT_FILE.
func stableDaemonPID(t *testing.T) int {
	t.Helper()
	path := strings.TrimSpace(os.Getenv("MULTICA_CHAIN_STABLE_DAEMON_PORT_FILE"))
	if path == "" {
		t.Logf("MULTICA_CHAIN_STABLE_DAEMON_PORT_FILE not set; stable daemon pid unknown")
		return 0
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Logf("read stable daemon port record: %v", err)
		return 0
	}
	var rec struct {
		Port int `json:"port"`
		PID  int `json:"pid"`
	}
	if json.Unmarshal(raw, &rec) != nil {
		return 0
	}
	return rec.PID
}

func portFree(port int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	ln.Close()
	return true
}

func healthPortForProfile(profile string) int {
	var h int
	for _, b := range []byte(profile) {
		h += int(b)
	}
	return defaultHealth + 1 + (h % 1000)
}

func pickProfile() (string, int) {
	for i := 0; i < 200; i++ {
		profile := fmt.Sprintf("dev6chain%d", i)
		port := healthPortForProfile(profile)
		if portFree(port) {
			return profile, port
		}
	}
	return "dev6chain", healthPortForProfile("dev6chain")
}

func waitHTTPOK(client *http.Client, url string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return true
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	return false
}

type proxyRecord struct {
	Method     string `json:"method"`
	Path       string `json:"path"`
	AuthPrefix string `json:"auth_prefix"`
	Upgrade    bool   `json:"upgrade_rejected"`
	Body       string `json:"body,omitempty"`
}

// chainCandidate is the submitted plan read back from the server. The fake
// executor fills it from the checkout and the task it actually read.
type chainCandidate struct {
	Items           []chainItem   `json:"items"`
	Edges           []chainEdge   `json:"edges"`
	ContextSnapshot chainSnapshot `json:"context_snapshot"`
}

type chainItem struct {
	ID           string             `json:"id"`
	Title        string             `json:"title"`
	EvidenceRefs []chainEvidenceRef `json:"evidence_refs"`
	Binding      chainBinding       `json:"binding"`
	Coordination *chainCoordination `json:"coordination"`
}

type chainEvidenceRef struct {
	Kind    string `json:"kind"`
	Ref     string `json:"ref"`
	Version string `json:"version"`
}

type chainBinding struct {
	Type    string `json:"type"`
	IssueID string `json:"issue_id"`
}

type chainCoordination struct {
	ParallelReason string `json:"parallel_reason"`
}

type chainEdge struct {
	DependentItemID    string `json:"dependent_item_id"`
	PrerequisiteItemID string `json:"prerequisite_item_id"`
	Reason             string `json:"reason"`
}

type chainSnapshot struct {
	RepoSHA string `json:"repo_sha"`
	Files   []struct {
		Path    string `json:"path"`
		Summary string `json:"summary"`
		SHA256  string `json:"sha256"`
	} `json:"files"`
	Tasks []struct {
		IssueID  string `json:"issue_id"`
		Revision int64  `json:"revision"`
		ReadAt   string `json:"read_at"`
	} `json:"tasks"`
	Resources []struct {
		ID   string `json:"id"`
		Read bool   `json:"read"`
	} `json:"resources"`
	ReadAt string `json:"read_at"`
}

// The fake executor submits exactly this graph. The assertions below compare
// stable node ids and the agreed strings, so a different edge direction, an
// extra chained edge, or a missing parallel reason fails the run.
const (
	chainItemA = "a1111111-1111-4111-8111-111111111111"
	chainItemB = "b2222222-2222-4222-8222-222222222222"
	chainItemC = "c3333333-3333-4333-8333-333333333333"

	chainItemATitle = "Controlled checkout item"
	chainItemBTitle = "Existing task item"
	chainItemCTitle = "Dependent item"

	chainParallelReasonA = "Independent of the seeded existing task; both can start together."
	chainParallelReasonB = "Read-only task evidence; independent of the checkout item."
	chainEdgeReason      = "The dependent item consumes the context the controlled checkout item reads first."
)

// validateChainCandidateGraph accepts only the agreed fixture graph: the three
// stable nodes, one C-depends-on-A edge with the agreed reason, and the agreed
// parallel reasons on A and B. It is deliberately a fixture assertion, not a
// general dependency-graph engine.
func validateChainCandidateGraph(c chainCandidate, seededTaskID string, seededRevision int64) error {
	wantTitles := map[string]string{
		chainItemA: chainItemATitle,
		chainItemB: chainItemBTitle,
		chainItemC: chainItemCTitle,
	}
	wantParallel := map[string]string{
		chainItemA: chainParallelReasonA,
		chainItemB: chainParallelReasonB,
	}
	if len(c.Items) != len(wantTitles) {
		return fmt.Errorf("candidate has %d items, want exactly %d", len(c.Items), len(wantTitles))
	}
	seen := map[string]bool{}
	for _, item := range c.Items {
		wantTitle, ok := wantTitles[item.ID]
		if !ok {
			return fmt.Errorf("candidate has unexpected item id %q", item.ID)
		}
		if seen[item.ID] {
			return fmt.Errorf("candidate repeats item id %q", item.ID)
		}
		seen[item.ID] = true
		if item.Title != wantTitle {
			return fmt.Errorf("item %s title = %q, want %q", item.ID, item.Title, wantTitle)
		}
		if want, required := wantParallel[item.ID]; required {
			if item.Coordination == nil || item.Coordination.ParallelReason != want {
				return fmt.Errorf("item %s parallel_reason = %+v, want %q", item.ID, item.Coordination, want)
			}
		}
		if item.ID == chainItemB {
			if item.Binding.Type != "existing" || item.Binding.IssueID != seededTaskID {
				return fmt.Errorf("item B binding = %+v, want existing %s", item.Binding, seededTaskID)
			}
			wantVersion := fmt.Sprintf("revision %d", seededRevision)
			found := false
			for _, ref := range item.EvidenceRefs {
				if ref.Kind == "task" && ref.Ref == seededTaskID && ref.Version == wantVersion {
					found = true
				}
			}
			if !found {
				return fmt.Errorf("item B has no task evidence %s at %q", seededTaskID, wantVersion)
			}
		}
	}
	if len(c.Edges) != 1 {
		return fmt.Errorf("candidate has %d edges, want exactly one C-depends-on-A edge", len(c.Edges))
	}
	edge := c.Edges[0]
	if edge.DependentItemID != chainItemC || edge.PrerequisiteItemID != chainItemA {
		return fmt.Errorf("candidate edge = %s -> %s, want %s -> %s",
			edge.DependentItemID, edge.PrerequisiteItemID, chainItemC, chainItemA)
	}
	if edge.Reason != chainEdgeReason {
		return fmt.Errorf("candidate edge reason = %q, want %q", edge.Reason, chainEdgeReason)
	}
	return nil
}

type recorder struct {
	mu      sync.Mutex
	records []proxyRecord
}

func (r *recorder) add(rec proxyRecord) {
	r.mu.Lock()
	r.records = append(r.records, rec)
	r.mu.Unlock()
}

func (r *recorder) snapshot() []proxyRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]proxyRecord, len(r.records))
	copy(out, r.records)
	return out
}

func (r *recorder) countClaim() int {
	n := 0
	for _, rec := range r.snapshot() {
		if rec.Method == http.MethodPost && (strings.HasSuffix(rec.Path, "/tasks/claim") || strings.HasSuffix(rec.Path, "/api/daemon/claim")) {
			n++
		}
	}
	return n
}

func (r *recorder) countResults(authPrefix string) int {
	n := 0
	for _, rec := range r.snapshot() {
		if rec.Method == http.MethodPost && strings.Contains(rec.Path, "/plan/generations/") && strings.HasSuffix(rec.Path, "/result") && rec.AuthPrefix == authPrefix {
			n++
		}
	}
	return n
}

func TestGoalPlanProcessChain(t *testing.T) {
	cfg := loadChainConfig(t)
	profile := readProfile(t, cfg.profileConfig)
	stablePID := stableDaemonPID(t)
	t.Logf("stable daemon pid=%d", stablePID)

	root := t.TempDir()
	home := filepath.Join(root, "home")
	binDir := filepath.Join(root, "bin")
	workspacesRoot := filepath.Join(root, "workspaces")
	fakeDir := filepath.Join(cfg.artifacts, "fake")
	controlledDir := filepath.Join(root, "controlled")
	for _, dir := range []string{home, binDir, workspacesRoot, fakeDir, controlledDir, filepath.Join(home, ".multica")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	marker := fmt.Sprintf("CHAIN_CONTEXT_MARKER=%d", time.Now().UnixNano())
	contextPath := filepath.Join(controlledDir, "context.md")
	if err := os.WriteFile(contextPath, []byte(marker+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	contextRaw, _ := os.ReadFile(contextPath)
	contextSHA := sha256Hex(contextRaw)
	// The planning run must report the actual repo SHA of the checkout it was
	// pointed at, not a fixture constant.
	repoSHA := initControlledCheckout(t, controlledDir)

	fakePath := filepath.Join(binDir, "opencode")
	if err := os.WriteFile(fakePath, []byte(fakeOpenCode), 0o700); err != nil {
		t.Fatal(err)
	}

	serverBase := "http://127.0.0.1:" + cfg.apiPort
	proxyTarget, _ := url.Parse(serverBase)

	rec := &recorder{}
	rp := httputil.NewSingleHostReverseProxy(proxyTarget)
	origDirector := rp.Director
	rp.Director = func(req *http.Request) {
		origDirector(req)
		req.Host = proxyTarget.Host
	}
	proxyHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authPrefix := tokenPrefix(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		entry := proxyRecord{Method: r.Method, Path: r.URL.Path, AuthPrefix: authPrefix}
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			entry.Upgrade = true
			rec.add(entry)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/tasks/claim") || strings.HasSuffix(r.URL.Path, "/api/daemon/claim") {
			if raw, err := io.ReadAll(io.LimitReader(r.Body, 4096)); err == nil {
				entry.Body = string(raw)
				r.Body = io.NopCloser(bytes.NewReader(raw))
			}
		}
		rec.add(entry)
		rp.ServeHTTP(w, r)
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	proxySrv := &http.Server{Handler: proxyHandler}
	go proxySrv.Serve(ln)
	defer proxySrv.Close()
	proxyURL := "http://" + ln.Addr().String()
	t.Logf("proxy %s -> %s", proxyURL, serverBase)

	// The daemon gets its own isolated HOME and profile config. The token is the
	// real PAT the managed environment already authenticated with.
	daemonProfile, predictedHealthPort := pickProfile()
	t.Logf("profile %s predicted health port %d", daemonProfile, predictedHealthPort)
	daemonHomeConfigDir := filepath.Join(home, ".multica", "profiles", daemonProfile)
	if err := os.MkdirAll(daemonHomeConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	daemonCfg := map[string]string{
		"server_url":   proxyURL,
		"workspace_id": profile.WorkspaceID,
		"token":        profile.Token,
	}
	rawCfg, _ := json.Marshal(daemonCfg)
	if err := os.WriteFile(filepath.Join(daemonHomeConfigDir, "config.json"), rawCfg, 0o600); err != nil {
		t.Fatal(err)
	}

	serverEnv := []string{
		"HOME=" + home,
		"PATH=/usr/bin:/bin",
		"TMPDIR=" + filepath.Join(root, "tmp"),
		"PORT=" + cfg.apiPort,
		"DATABASE_URL=" + cfg.dbURL,
		"APP_ENV=development",
		"ALLOW_SIGNUP=true",
		"JWT_SECRET=dev6-chain-process-test-secret",
		"MULTICA_DEV_VERIFICATION_CODE=888888",
	}
	os.MkdirAll(filepath.Join(root, "tmp"), 0o700)

	httpc := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}}

	// Register the evidence writer before starting anything: child cleanups are
	// registered later and therefore run first, so cleanup.json always reflects
	// the stop that already happened — including when the test fails right after
	// the server became healthy and before the daemon could start.
	cleanup := &chainCleanup{stablePID: stablePID, profileDir: daemonHomeConfigDir}
	t.Cleanup(func() { cleanup.write(t, cfg.artifacts, profile.Token) })

	// Each child owns its guard; the guard itself is stateless, so every signal
	// performs a new runtime CLI query rather than reusing an earlier answer.
	serverGuard := newStableDaemonGuard(cfg.stableCLIBin, cfg.stableProfile, cfg.stableHome, root)
	server := startChild(t, "server", root, cfg.serverBin, nil, serverEnv, filepath.Join(cfg.artifacts, "server.log"), serverGuard)
	cleanup.add(server)
	if !waitHTTPOK(httpc, serverBase+"/health", 90*time.Second) {
		t.Fatalf("server did not become healthy; see %s", filepath.Join(cfg.artifacts, "server.log"))
	}
	var health struct {
		PID    int    `json:"pid"`
		Commit string `json:"commit"`
	}
	resp, err := httpc.Get(serverBase + "/health")
	if err != nil {
		t.Fatal(err)
	}
	json.NewDecoder(resp.Body).Decode(&health)
	resp.Body.Close()
	if health.PID != server.cmd.Process.Pid {
		t.Fatalf("health pid %d does not match started server pid %d", health.PID, server.cmd.Process.Pid)
	}
	t.Logf("server healthy pid=%d commit=%s", health.PID, health.Commit)

	daemonID := fmt.Sprintf("dev6-chain-%d", time.Now().UnixNano())
	daemonEnv := []string{
		"HOME=" + home,
		"PATH=" + binDir,
		"SHELL=",
		"TMPDIR=" + filepath.Join(root, "tmp"),
		"LANG=C.UTF-8",
		"CHAIN_FAKE_DIR=" + fakeDir,
		"MULTICA_OPENCODE_PATH=" + fakePath,
		"MULTICA_OPENCODE_TRANSPORT=run",
	}
	daemonArgs := []string{
		"daemon", "start", "--foreground",
		"--profile", daemonProfile,
		"--daemon-id", daemonID,
		"--device-name", "dev6-chain-test",
		"--workspaces-root", workspacesRoot,
		"--poll-interval", "1s",
		"--max-concurrent-tasks", "1",
		"--agent-timeout", "180s",
		"--no-auto-update",
		"--no-auto-reload",
	}
	daemonGuard := newStableDaemonGuard(cfg.stableCLIBin, cfg.stableProfile, cfg.stableHome, root)
	daemon := startChild(t, "daemon", root, cfg.cliBin, daemonArgs, daemonEnv, filepath.Join(cfg.artifacts, "daemon.log"), daemonGuard)
	cleanup.add(daemon)

	if stablePID != 0 && (stablePID == daemon.cmd.Process.Pid || stablePID == server.cmd.Process.Pid) {
		t.Fatalf("started pid collides with the stable daemon pid %d; refusing to continue", stablePID)
	}

	portRecord := filepath.Join(daemonHomeConfigDir, "daemon.port")
	var daemonHealth struct {
		PID           int               `json:"pid"`
		Profile       string            `json:"profile"`
		Agents        []string          `json:"agents"`
		SkippedAgents map[string]string `json:"skipped_agents"`
	}
	waitFor(t, 60*time.Second, "daemon health record", func() (bool, string) {
		raw, err := os.ReadFile(portRecord)
		if err != nil {
			return false, err.Error()
		}
		var rec struct {
			Port int `json:"port"`
			PID  int `json:"pid"`
		}
		if json.Unmarshal(raw, &rec) != nil || rec.Port == 0 {
			return false, string(raw)
		}
		resp, err := httpc.Get(fmt.Sprintf("http://127.0.0.1:%d/health", rec.Port))
		if err != nil {
			return false, err.Error()
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return false, fmt.Sprintf("status %d", resp.StatusCode)
		}
		return json.NewDecoder(resp.Body).Decode(&daemonHealth) == nil, "decode"
	})
	if daemonHealth.PID != daemon.cmd.Process.Pid {
		t.Fatalf("daemon health pid %d does not match started pid %d", daemonHealth.PID, daemon.cmd.Process.Pid)
	}
	agentSet := strings.Join(daemonHealth.Agents, ",")
	if agentSet != "opencode" {
		t.Fatalf("daemon discovered agents %q; the isolated PATH must expose only the test-owned opencode", agentSet)
	}
	var skippedByHost []string
	for name, reason := range daemonHealth.SkippedAgents {
		if name != "opencode" {
			skippedByHost = append(skippedByHost, name+": "+reason)
		}
	}
	t.Logf("daemon health pid=%d profile=%s agents=%v skipped=%v", daemonHealth.PID, daemonHealth.Profile, daemonHealth.Agents, daemonHealth.SkippedAgents)

	api := newAPIClient(serverBase, profile.Token, profile.WorkspaceID)

	var runtimes []struct {
		ID        string `json:"id"`
		Provider  string `json:"provider"`
		DaemonID  string `json:"daemon_id"`
		Status    string `json:"status"`
		Workspace string `json:"workspace_id"`
	}
	runtimeID := ""
	waitFor(t, 90*time.Second, "opencode runtime registration", func() (bool, string) {
		res := api.do(t, http.MethodGet, "/api/runtimes", nil)
		if res.Status != http.StatusOK {
			return false, fmt.Sprintf("status %d %.500s", res.Status, res.Body)
		}
		runtimes = nil
		if err := json.Unmarshal(res.Body, &runtimes); err != nil {
			return false, err.Error()
		}
		for _, rt := range runtimes {
			if rt.Provider == "opencode" && rt.DaemonID == daemonID {
				runtimeID = rt.ID
				return true, ""
			}
		}
		return false, "runtime not listed yet"
	})
	t.Logf("runtime %s registered for daemon %s", runtimeID, daemonID)

	// ---- fixtures, created through the real HTTP API -------------------------
	var project struct {
		ID string `json:"id"`
	}
	runTag := fmt.Sprintf("%d", time.Now().UnixNano())
	api.mustJSON(t, http.MethodPost, "/api/projects", map[string]any{
		"title":       "DEV-6 process chain project " + runTag,
		"description": "Isolated fixture for the goal plan process chain.",
	}, &project, http.StatusCreated)

	// Seed one existing task in the project. The planning run has to read it
	// through its own task-token authorization; the expected values below only
	// bound the assertion, they are not written into the submitted plan.
	var seededTask struct {
		ID         string `json:"id"`
		Identifier string `json:"identifier"`
		Title      string `json:"title"`
		Revision   int64  `json:"revision"`
	}
	seededTaskTitle := "DEV-6 chain existing task " + runTag
	api.mustJSON(t, http.MethodPost, "/api/issues", map[string]any{
		"title":       seededTaskTitle,
		"description": "Existing task seeded before the planning run; the run must read it, not invent it.",
		"project_id":  project.ID,
		"status":      "todo",
		"priority":    "medium",
	}, &seededTask, http.StatusCreated)
	if seededTask.ID == "" || seededTask.Revision < 1 {
		t.Fatalf("seeded existing task = %+v, want an id and revision", seededTask)
	}

	var resource struct {
		ID string `json:"id"`
	}
	api.mustJSON(t, http.MethodPost, "/api/projects/"+project.ID+"/resources", map[string]any{
		"resource_type": "local_directory",
		"resource_ref": map[string]any{
			"local_path":     controlledDir,
			"daemon_id":      daemonID,
			"execution_mode": "in_place",
			"label":          "chain controlled directory",
		},
	}, &resource, http.StatusCreated)

	var agent struct {
		ID string `json:"id"`
	}
	api.mustJSON(t, http.MethodPost, "/api/agents", map[string]any{
		"name":        "DEV-6 chain agent " + runTag,
		"description": "Process chain fixture agent.",
		"runtime_id":  runtimeID,
	}, &agent, http.StatusCreated)

	var goal struct {
		ID string `json:"id"`
	}
	intent := "验证受控 context.md 能被规划任务读取"
	api.mustJSON(t, http.MethodPost, "/api/goals", map[string]any{
		"title":               "DEV-6 process chain goal " + runTag,
		"intent":              intent,
		"acceptance_criteria": "候选计划记录受控文件摘要",
		"project_id":          project.ID,
	}, &goal, http.StatusCreated)

	// The database assertions compare complete row sets, not just the ids the
	// API already reported. Every stage records its SQL, parameters, rows and
	// query status; a failed query fails the test.
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, cfg.dbURL)
	if err != nil {
		t.Fatalf("connect to the isolated test database: %v", err)
	}
	t.Cleanup(func() { conn.Close(context.Background()) })

	var dbStages []dbStageEvidence
	recordStage := func(ev dbStageEvidence) {
		dbStages = append(dbStages, ev)
		writeArtifact(t, cfg.artifacts, "db-stages.json", dbStages)
	}
	runStage := func(stage, sql string, params []string, args ...any) []map[string]string {
		rows, queryErr := queryStageRows(ctx, conn, sql, args...)
		ev := dbStageEvidence{Stage: stage, SQL: sql, Params: params, Rows: rows, Count: len(rows), OK: queryErr == nil}
		if queryErr != nil {
			ev.Error = queryErr.Error()
		}
		recordStage(ev)
		if queryErr != nil {
			t.Fatalf("database stage %q failed: %v", stage, queryErr)
		}
		return rows
	}

	requestKey := fmt.Sprintf("chain-%d", time.Now().UnixNano())
	generationBody := map[string]any{
		"intent":            "生成一份包含受控文件依据的计划草稿",
		"request_key":       requestKey,
		"expected_revision": 0,
		"agent_id":          agent.ID,
	}
	issuePath := "/api/issues?project_id=" + project.ID + "&limit=100"
	var issuesBefore projectIssues
	api.mustJSON(t, http.MethodGet, issuePath, nil, &issuesBefore, http.StatusOK)
	writeArtifact(t, cfg.artifacts, "issues-before-generation.json", issuesBefore)
	if len(issuesBefore.Issues) != 1 || issuesBefore.Issues[0].ID != seededTask.ID {
		t.Fatalf("project issues before the run = %+v, want only the seeded task", issuesBefore.Issues)
	}
	if rows := runStage("before-generation/generations", generationRowSQL,
		[]string{profile.WorkspaceID, goal.ID}, profile.WorkspaceID, goal.ID); len(rows) != 0 {
		t.Fatalf("generation rows before the run = %d, want none", len(rows))
	}
	if rows := runStage("before-generation/tasks", taskRowSQL,
		[]string{agent.ID}, agent.ID); len(rows) != 0 {
		t.Fatalf("queue task rows before the run = %d, want none", len(rows))
	}

	var created struct {
		ID     string `json:"id"`
		State  string `json:"state"`
		TaskID string `json:"task_id"`
	}
	api.mustJSON(t, http.MethodPost, "/api/goals/"+goal.ID+"/plan/generations", generationBody, &created, http.StatusAccepted)
	t.Logf("generation %s queued task=%s", created.ID, created.TaskID)

	writeArtifact(t, cfg.artifacts, "fixtures.json", map[string]any{
		"project_id":          project.ID,
		"resource_id":         resource.ID,
		"agent_id":            agent.ID,
		"goal_id":             goal.ID,
		"generation_id":       created.ID,
		"task_id":             created.TaskID,
		"runtime_id":          runtimeID,
		"daemon_id":           daemonID,
		"context_path":        contextPath,
		"context_sha":         contextSHA,
		"repo_sha":            repoSHA,
		"existing_task_id":    seededTask.ID,
		"existing_task_rev":   seededTask.Revision,
		"existing_task_title": seededTask.Title,
		"marker":              marker,
	})

	var final struct {
		ID        string          `json:"id"`
		State     string          `json:"state"`
		TaskID    string          `json:"task_id"`
		Candidate json.RawMessage `json:"candidate"`
		Error     string          `json:"error"`
	}
	waitFor(t, 150*time.Second, "generation result", func() (bool, string) {
		res := api.do(t, http.MethodGet, "/api/goals/"+goal.ID+"/plan/generations/"+created.ID, nil)
		if res.Status != http.StatusOK {
			return false, fmt.Sprintf("status %d %.500s", res.Status, res.Body)
		}
		if err := json.Unmarshal(res.Body, &final); err != nil {
			return false, err.Error()
		}
		switch final.State {
		case "succeeded":
			return true, ""
		case "failed":
			return false, "generation failed: " + final.Error
		default:
			return false, "state " + final.State
		}
	})
	writeArtifact(t, cfg.artifacts, "generation.json", json.RawMessage(mustMarshal(t, final)))

	// ---- evidence assertions -------------------------------------------------
	if final.TaskID != "" && created.TaskID != "" && final.TaskID != created.TaskID {
		t.Fatalf("generation task changed: %s -> %s", created.TaskID, final.TaskID)
	}
	candidate := string(final.Candidate)
	if !strings.Contains(candidate, marker) {
		t.Fatalf("candidate does not contain the controlled file content: %.2000s", candidate)
	}
	if !strings.Contains(candidate, "sha256:"+contextSHA) {
		t.Fatalf("candidate does not record the controlled file digest: %.2000s", candidate)
	}
	if !strings.Contains(candidate, resource.ID) {
		t.Fatalf("candidate does not reference the input resource id %s: %.2000s", resource.ID, candidate)
	}

	var submitted chainCandidate
	if err := json.Unmarshal(final.Candidate, &submitted); err != nil {
		t.Fatalf("decode candidate: %v", err)
	}
	if submitted.ContextSnapshot.RepoSHA != repoSHA {
		t.Fatalf("candidate repo_sha = %q, want the controlled checkout HEAD %q",
			submitted.ContextSnapshot.RepoSHA, repoSHA)
	}
	if len(submitted.ContextSnapshot.Files) == 0 || submitted.ContextSnapshot.Files[0].SHA256 != contextSHA {
		t.Fatalf("candidate file digest does not match the bytes the run read: %+v (want %s)",
			submitted.ContextSnapshot.Files, contextSHA)
	}
	if len(submitted.ContextSnapshot.Tasks) != 1 ||
		submitted.ContextSnapshot.Tasks[0].IssueID != seededTask.ID ||
		submitted.ContextSnapshot.Tasks[0].Revision != seededTask.Revision ||
		submitted.ContextSnapshot.Tasks[0].ReadAt == "" {
		t.Fatalf("candidate task snapshot = %+v, want issue %s at revision %d with a read time",
			submitted.ContextSnapshot.Tasks, seededTask.ID, seededTask.Revision)
	}
	// The exact graph is asserted twice: on the plan the fake executor actually
	// submitted, and on the candidate the server stored and handed back.
	planRaw, err := os.ReadFile(filepath.Join(fakeDir, "plan.json"))
	if err != nil {
		t.Fatalf("fake executor submitted no plan: %v", err)
	}
	var planCandidate chainCandidate
	if err := json.Unmarshal(planRaw, &planCandidate); err != nil {
		t.Fatalf("decode submitted plan: %v", err)
	}
	if err := validateChainCandidateGraph(planCandidate, seededTask.ID, seededTask.Revision); err != nil {
		t.Fatalf("submitted plan graph: %v", err)
	}
	if err := validateChainCandidateGraph(submitted, seededTask.ID, seededTask.Revision); err != nil {
		t.Fatalf("read-back candidate graph: %v", err)
	}

	promptRaw, err := os.ReadFile(filepath.Join(fakeDir, "prompt.txt"))
	if err != nil {
		t.Fatalf("fake executor captured no prompt: %v", err)
	}
	prompt := string(promptRaw)
	if !strings.Contains(prompt, controlledDir) {
		t.Fatalf("prompt does not carry the resource path %s", controlledDir)
	}
	wantSubmit := fmt.Sprintf("goal plan-result --goal %s --generation %s", goal.ID, created.ID)
	if !strings.Contains(prompt, wantSubmit) {
		t.Fatalf("prompt does not carry %q", wantSubmit)
	}

	parsedRaw, _ := os.ReadFile(filepath.Join(fakeDir, "parsed.txt"))
	parsed := string(parsedRaw)
	for _, want := range []string{"goal=" + goal.ID, "generation=" + created.ID, "resource_path=" + controlledDir, "resource_id=" + resource.ID} {
		if !strings.Contains(parsed, want) {
			t.Fatalf("fake executor did not locate the run from the prompt: %q missing in %q", want, parsed)
		}
	}
	readRaw, _ := os.ReadFile(filepath.Join(fakeDir, "context-read.txt"))
	readText := string(readRaw)
	if !strings.Contains(readText, "context_sha256="+contextSHA) || !strings.Contains(readText, marker) {
		t.Fatalf("fake executor did not read the controlled file: %q", readText)
	}
	if !strings.Contains(readText, "repo_sha="+repoSHA) {
		t.Fatalf("fake executor did not read the checkout HEAD %s: %q", repoSHA, readText)
	}
	taskReadRaw, _ := os.ReadFile(filepath.Join(fakeDir, "tasks-read.txt"))
	taskRead := string(taskReadRaw)
	wantIssueID := "issue_id=" + seededTask.ID
	wantIssueRev := "revision=" + strconv.FormatInt(seededTask.Revision, 10)
	if !strings.Contains(taskRead, wantIssueID) || !strings.Contains(taskRead, wantIssueRev) {
		t.Fatalf("fake executor did not read the seeded task %s at %s: %q", seededTask.ID, wantIssueRev, taskRead)
	}
	issuesRC, _ := os.ReadFile(filepath.Join(fakeDir, "issues.rc"))
	if strings.TrimSpace(string(issuesRC)) != "0" {
		issuesErr, _ := os.ReadFile(filepath.Join(fakeDir, "issues.err"))
		t.Fatalf("task-token issue list failed rc=%q stderr=%s", strings.TrimSpace(string(issuesRC)), issuesErr)
	}
	issuesJSON, _ := os.ReadFile(filepath.Join(fakeDir, "issues.json"))
	if !strings.Contains(string(issuesJSON), seededTask.ID) {
		t.Fatalf("task-token issue list did not return the seeded task %s: %.2000s", seededTask.ID, string(issuesJSON))
	}
	taskEnvRaw, _ := os.ReadFile(filepath.Join(fakeDir, "task-env.txt"))
	if !strings.Contains(string(taskEnvRaw), "token_prefix=mat_") {
		t.Fatalf("daemon did not inject a task-scoped token: %q", string(taskEnvRaw))
	}
	rcRaw, _ := os.ReadFile(filepath.Join(fakeDir, "cli.rc"))
	if strings.TrimSpace(string(rcRaw)) != "0" {
		cliErr, _ := os.ReadFile(filepath.Join(fakeDir, "cli.err"))
		t.Fatalf("CLI result submission failed rc=%q stderr=%s", strings.TrimSpace(string(rcRaw)), cliErr)
	}
	cliOut, _ := os.ReadFile(filepath.Join(fakeDir, "cli.out"))
	if !strings.Contains(string(cliOut), "Submitted plan for generation "+created.ID) {
		t.Fatalf("unexpected CLI output: %q", string(cliOut))
	}

	// After the first run the database must hold exactly one generation and one
	// queue task for this fixture. The generation query is scoped to this
	// fixture's workspace and goal, the task query to this run's agent, so any
	// additional row with an unknown id is visible. The project must still hold
	// exactly the task seeded before the run.
	genAfterRun := runStage("after-first-run/generations", generationRowSQL,
		[]string{profile.WorkspaceID, goal.ID}, profile.WorkspaceID, goal.ID)
	if err := assertGenerationRowSet(genAfterRun, created.ID, created.TaskID, requestKey); err != nil {
		t.Fatalf("after the first run, the generation row set is wrong: %v", err)
	}
	sessionID := genAfterRun[0]["chat_session_id"]
	if sessionID == "" {
		t.Fatalf("the generation row carries no chat_session_id: %v", genAfterRun[0])
	}
	taskAfterRun := runStage("after-first-run/tasks", taskRowSQL, []string{agent.ID}, agent.ID)
	if err := assertTaskRowSet(taskAfterRun, created.TaskID, sessionID); err != nil {
		t.Fatalf("after the first run, the queue task row set is wrong: %v", err)
	}
	var afterRun projectIssues
	api.mustJSON(t, http.MethodGet, issuePath, nil, &afterRun, http.StatusOK)
	writeArtifact(t, cfg.artifacts, "issues-after-run.json", afterRun)
	if err := compareIssueSets(issuesBefore, afterRun); err != nil {
		t.Fatalf("the first run changed the project's issue set: %v", err)
	}

	// A same-key retry must replay the existing generation instead of starting
	// a second run or a second task.
	var replayed struct {
		ID     string `json:"id"`
		State  string `json:"state"`
		TaskID string `json:"task_id"`
	}
	replay := api.do(t, http.MethodPost, "/api/goals/"+goal.ID+"/plan/generations", generationBody)
	if replay.Status != http.StatusOK {
		t.Fatalf("same-key retry: status %d, body %.2000s", replay.Status, replay.Body)
	}
	if err := json.Unmarshal(replay.Body, &replayed); err != nil {
		t.Fatalf("decode replay: %v", err)
	}
	writeArtifact(t, cfg.artifacts, "replay.json", json.RawMessage(mustMarshal(t, replayed)))
	if replayed.ID != created.ID || replayed.TaskID != created.TaskID {
		t.Fatalf("same-key retry did not replay the original generation: %+v vs %s/%s",
			replayed, created.ID, created.TaskID)
	}
	// The same-key retry must leave the row sets unchanged: still exactly the
	// original generation with the original task, and the same project issues.
	genAfterReplay := runStage("after-same-key-retry/generations", generationRowSQL,
		[]string{profile.WorkspaceID, goal.ID}, profile.WorkspaceID, goal.ID)
	if err := assertGenerationRowSet(genAfterReplay, created.ID, created.TaskID, requestKey); err != nil {
		t.Fatalf("after the same-key retry, the generation row set is wrong: %v", err)
	}
	if got := genAfterReplay[0]["chat_session_id"]; got != sessionID {
		t.Fatalf("same-key retry changed the generation session: %q -> %q", sessionID, got)
	}
	taskAfterReplay := runStage("after-same-key-retry/tasks", taskRowSQL, []string{agent.ID}, agent.ID)
	if err := assertTaskRowSet(taskAfterReplay, created.TaskID, sessionID); err != nil {
		t.Fatalf("after the same-key retry, the queue task row set is wrong: %v", err)
	}
	var afterReplay projectIssues
	api.mustJSON(t, http.MethodGet, issuePath, nil, &afterReplay, http.StatusOK)
	writeArtifact(t, cfg.artifacts, "issues-after-replay.json", afterReplay)
	if err := compareIssueSets(issuesBefore, afterReplay); err != nil {
		t.Fatalf("the same-key retry changed the project's issue set: %v", err)
	}

	// Negative proof for both row-set assertions: insert one extra row inside
	// this fixture's scope while the original id stays present, rerun the same
	// query, and require the row-set assertion to fail. The probe row is then
	// removed and the restored row set is re-checked. Probes only touch this
	// test's own database.
	probeAndCheck := func(stage, sql string, params []string, args []any,
		insert string, insertArgs []any, assertion func([]map[string]string) error, deleteSQL string) {
		t.Helper()
		var probeID string
		if err := conn.QueryRow(ctx, insert, insertArgs...).Scan(&probeID); err != nil {
			t.Fatalf("%s: insert probe row: %v", stage, err)
		}
		t.Cleanup(func() {
			_, _ = conn.Exec(context.Background(), deleteSQL, probeID)
		})
		rows, queryErr := queryStageRows(ctx, conn, sql, args...)
		mutation := fmt.Sprintf("extra row inserted by: %s; probe id=%s", strings.Join(strings.Fields(insert), " "), probeID)
		detection := dbStageEvidence{Stage: stage + "/detection", SQL: sql, Params: params, Rows: rows, Count: len(rows), OK: queryErr == nil, Mutation: mutation}
		if queryErr != nil {
			detection.Error = queryErr.Error()
			recordStage(detection)
			t.Fatalf("%s: probe query failed: %v", stage, queryErr)
		}
		assertionErr := assertion(rows)
		if assertionErr == nil {
			detection.Error = "the extra probe row was not detected"
			recordStage(detection)
			t.Fatalf("%s: an extra row was not detected: %v", stage, rows)
		}
		detection.AssertionError = assertionErr.Error()
		detection.Detected = true
		recordStage(detection)
		if _, err := conn.Exec(ctx, deleteSQL, probeID); err != nil {
			t.Fatalf("%s: remove probe row: %v", stage, err)
		}
		restored, queryErr := queryStageRows(ctx, conn, sql, args...)
		cleanup := dbStageEvidence{
			Stage: stage + "/cleanup", SQL: sql, Params: params, Rows: restored, Count: len(restored), OK: queryErr == nil,
			Cleanup: fmt.Sprintf("probe %s deleted with %s", probeID, deleteSQL),
		}
		if queryErr == nil {
			if err := assertion(restored); err != nil {
				cleanup.AssertionError = err.Error()
			}
		} else {
			cleanup.Error = queryErr.Error()
		}
		recordStage(cleanup)
		if queryErr != nil {
			t.Fatalf("%s: cleanup query failed: %v", stage, queryErr)
		}
		if cleanup.AssertionError != "" {
			t.Fatalf("%s: row set not restored after cleanup: %s", stage, cleanup.AssertionError)
		}
	}
	probeAndCheck("negative-extra-generation", generationRowSQL,
		[]string{profile.WorkspaceID, goal.ID}, []any{profile.WorkspaceID, goal.ID},
		`INSERT INTO goal_plan_generation (workspace_id, goal_id, project_id, request_key, intent, agent_id)
		 SELECT workspace_id, goal_id, project_id, $2, 'row-set probe', agent_id
		 FROM goal_plan_generation WHERE id = $1::uuid
		 RETURNING id::text`,
		[]any{created.ID, requestKey + "-probe"},
		func(rows []map[string]string) error {
			return assertGenerationRowSet(rows, created.ID, created.TaskID, requestKey)
		},
		`DELETE FROM goal_plan_generation WHERE id = $1::uuid`)
	probeAndCheck("negative-extra-task", taskRowSQL, []string{agent.ID}, []any{agent.ID},
		`INSERT INTO agent_task_queue (agent_id, issue_id, chat_session_id, status, completed_at)
		 SELECT agent_id, issue_id, chat_session_id, 'completed', now()
		 FROM agent_task_queue WHERE id = $1::uuid
		 RETURNING id::text`,
		[]any{created.TaskID},
		func(rows []map[string]string) error {
			return assertTaskRowSet(rows, created.TaskID, sessionID)
		},
		`DELETE FROM agent_task_queue WHERE id = $1::uuid`)

	records := rec.snapshot()
	writeArtifact(t, cfg.artifacts, "proxy.json", records)
	if n := rec.countClaim(); n == 0 {
		t.Fatalf("no HTTP task claim reached the server; claims must not use the WebSocket transport")
	}
	if n := rec.countResults("mat_"); n != 1 {
		t.Fatalf("result submissions using the task token = %d, want exactly one", n)
	}
	taskListRead := false
	for _, record := range records {
		if record.Method == http.MethodGet && strings.HasPrefix(record.Path, "/api/issues") && record.AuthPrefix == "mat_" {
			taskListRead = true
		}
	}
	if !taskListRead {
		t.Fatalf("the run never read existing tasks with its task token: %+v", records)
	}
	wsRejected := 0
	for _, record := range records {
		if record.Upgrade {
			wsRejected++
		}
	}
	t.Logf("proxy: %d records, %d HTTP claims, %d WS upgrades rejected", len(records), rec.countClaim(), wsRejected)

	// Isolation evidence: the daemon only ever saw the test-owned executable,
	// and its environment named only the test proxy.
	invocations, _ := os.ReadFile(filepath.Join(fakeDir, "invocations.log"))
	if !strings.Contains(string(invocations), "argv=--version") || !strings.Contains(string(invocations), "argv=run ") {
		t.Fatalf("fake executor invocation log is incomplete: %q", string(invocations))
	}
	if !strings.Contains(string(taskEnvRaw), "server="+proxyURL) {
		t.Fatalf("task env server is not the test proxy: %q", string(taskEnvRaw))
	}
	if len(skippedByHost) > 0 {
		t.Logf("daemon skipped agents: %v", skippedByHost)
	}

	// Never print credentials: scan the artifacts for the PAT or a task token.
	assertNoCredentials(t, cfg.artifacts, profile.Token)

	t.Logf("chain OK: generation=%s state=%s task=%s runtime=%s", final.ID, final.State, final.TaskID, runtimeID)
}

// initControlledCheckout turns the controlled resource directory into a real
// git checkout and returns its HEAD. The fake executor reads this SHA with
// `git rev-parse HEAD`, so the candidate's repo_sha is the actual revision the
// run saw, not a value copied from the fixture.
func initControlledCheckout(t *testing.T, dir string) string {
	t.Helper()
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("git is required to create the controlled checkout: %v", err)
	}
	env := []string{
		"HOME=" + dir,
		"PATH=/usr/bin:/bin",
		"TMPDIR=" + dir,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=DEV-6 Chain",
		"GIT_AUTHOR_EMAIL=dev6-chain@example.invalid",
		"GIT_COMMITTER_NAME=DEV-6 Chain",
		"GIT_COMMITTER_EMAIL=dev6-chain@example.invalid",
	}
	run := func(args ...string) string {
		cmd := exec.Command(gitBin, args...)
		cmd.Dir = dir
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q")
	run("add", "context.md")
	run("commit", "-q", "-m", "seed controlled checkout")
	head := run("rev-parse", "HEAD")
	if len(head) != 40 {
		t.Fatalf("controlled checkout HEAD = %q, want a 40-character commit SHA", head)
	}
	return head
}

// TestChainCandidateGraphRejectsWrongShapes is the minimal negative proof for
// validateChainCandidateGraph: an inverted edge, a chained pair instead of the
// single C->A wait, a missing parallel reason, and a task-evidence version that
// merely shares the revision digit are all rejected.
func TestChainCandidateGraphRejectsWrongShapes(t *testing.T) {
	const taskID = "01a1d077-cdb4-79bc-9781-05ea9b56b861"
	const taskRevision int64 = 1

	base := func() chainCandidate {
		return chainCandidate{
			Items: []chainItem{
				{
					ID: chainItemA, Title: chainItemATitle,
					Coordination: &chainCoordination{ParallelReason: chainParallelReasonA},
				},
				{
					ID: chainItemB, Title: chainItemBTitle,
					Binding: chainBinding{Type: "existing", IssueID: taskID},
					EvidenceRefs: []chainEvidenceRef{{
						Kind: "task", Ref: taskID, Version: "revision 1",
					}},
					Coordination: &chainCoordination{ParallelReason: chainParallelReasonB},
				},
				{ID: chainItemC, Title: chainItemCTitle},
			},
			Edges: []chainEdge{{
				DependentItemID: chainItemC, PrerequisiteItemID: chainItemA, Reason: chainEdgeReason,
			}},
		}
	}
	if err := validateChainCandidateGraph(base(), taskID, taskRevision); err != nil {
		t.Fatalf("baseline fixture must pass: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*chainCandidate)
	}{
		{"inverted A depends on B", func(c *chainCandidate) {
			c.Edges[0] = chainEdge{DependentItemID: chainItemA, PrerequisiteItemID: chainItemB, Reason: chainEdgeReason}
		}},
		{"chained A depends on B and B depends on C", func(c *chainCandidate) {
			c.Edges = []chainEdge{
				{DependentItemID: chainItemA, PrerequisiteItemID: chainItemB, Reason: chainEdgeReason},
				{DependentItemID: chainItemB, PrerequisiteItemID: chainItemC, Reason: chainEdgeReason},
			}
		}},
		{"missing parallel reason on A", func(c *chainCandidate) {
			c.Items[0].Coordination = nil
		}},
		{"task evidence revision 11 for revision 1", func(c *chainCandidate) {
			c.Items[1].EvidenceRefs[0].Version = "revision 11"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			candidate := base()
			tc.mutate(&candidate)
			if err := validateChainCandidateGraph(candidate, taskID, taskRevision); err == nil {
				t.Fatalf("mutated graph %q was accepted", tc.name)
			} else {
				t.Logf("rejected as expected: %v", err)
			}
		})
	}
}

type issueRow struct {
	ID       string `json:"id"`
	Revision int64  `json:"revision"`
	Title    string `json:"title"`
}

type projectIssues struct {
	Issues []issueRow `json:"issues"`
	Total  int        `json:"total"`
}

// issueSetKey compares the fixture project's issues by identity and revision,
// so either a new issue or an in-place revision change is visible.
func issueSetKey(issues projectIssues) []string {
	keys := make([]string, 0, len(issues.Issues))
	for _, issue := range issues.Issues {
		keys = append(keys, fmt.Sprintf("%s|rev=%d|%s", issue.ID, issue.Revision, issue.Title))
	}
	sort.Strings(keys)
	return keys
}

func compareIssueSets(before, after projectIssues) error {
	beforeKeys, afterKeys := issueSetKey(before), issueSetKey(after)
	if len(beforeKeys) != len(afterKeys) {
		return fmt.Errorf("issue set changed: before %v, after %v", beforeKeys, afterKeys)
	}
	for i := range beforeKeys {
		if beforeKeys[i] != afterKeys[i] {
			return fmt.Errorf("issue set changed at %d: before %v, after %v", i, beforeKeys, afterKeys)
		}
	}
	return nil
}

type dbStageEvidence struct {
	Stage          string              `json:"stage"`
	SQL            string              `json:"sql"`
	Params         []string            `json:"params"`
	Rows           []map[string]string `json:"rows"`
	Count          int                 `json:"count"`
	OK             bool                `json:"ok"`
	Error          string              `json:"error,omitempty"`
	Mutation       string              `json:"mutation,omitempty"`
	AssertionError string              `json:"assertion_error,omitempty"`
	Detected       bool                `json:"mutation_detected,omitempty"`
	Cleanup        string              `json:"cleanup,omitempty"`
}

const generationRowSQL = `SELECT id::text, workspace_id::text, goal_id::text, request_key, task_id::text, chat_session_id::text, state
FROM goal_plan_generation
WHERE workspace_id = $1::uuid AND goal_id = $2::uuid
ORDER BY id`

const taskRowSQL = `SELECT id::text, agent_id::text, chat_session_id::text, status
FROM agent_task_queue
WHERE agent_id = $1::uuid
ORDER BY id`

func queryStageRows(ctx context.Context, conn *pgx.Conn, sql string, args ...any) ([]map[string]string, error) {
	rows, err := conn.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	fields := rows.FieldDescriptions()
	out := []map[string]string{}
	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			return nil, err
		}
		row := make(map[string]string, len(fields))
		for i, field := range fields {
			if values[i] == nil {
				row[string(field.Name)] = ""
				continue
			}
			row[string(field.Name)] = fmt.Sprint(values[i])
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func assertGenerationRowSet(rows []map[string]string, wantID, wantTaskID, wantRequestKey string) error {
	if len(rows) != 1 {
		return fmt.Errorf("generation row set has %d rows, want exactly 1: %v", len(rows), rows)
	}
	row := rows[0]
	if row["id"] != wantID {
		return fmt.Errorf("generation row id = %q, want %q", row["id"], wantID)
	}
	if row["task_id"] != wantTaskID {
		return fmt.Errorf("generation row task_id = %q, want %q", row["task_id"], wantTaskID)
	}
	if row["request_key"] != wantRequestKey {
		return fmt.Errorf("generation row request_key = %q, want %q", row["request_key"], wantRequestKey)
	}
	if row["state"] == "" {
		return fmt.Errorf("generation row has no state: %v", row)
	}
	return nil
}

func assertTaskRowSet(rows []map[string]string, wantID, wantSession string) error {
	if len(rows) != 1 {
		return fmt.Errorf("queue task row set has %d rows, want exactly 1: %v", len(rows), rows)
	}
	row := rows[0]
	if row["id"] != wantID {
		return fmt.Errorf("queue task row id = %q, want %q", row["id"], wantID)
	}
	if wantSession == "" || row["chat_session_id"] != wantSession {
		return fmt.Errorf("queue task chat_session_id = %q, want %q", row["chat_session_id"], wantSession)
	}
	return nil
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func sha256Hex(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func assertNoCredentials(t *testing.T, dir, pat string) {
	t.Helper()
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		if pat != "" && bytes.Contains(raw, []byte(pat)) {
			t.Fatalf("artifact %s contains the profile PAT", path)
		}
		for _, field := range bytes.Fields(raw) {
			if bytes.HasPrefix(field, []byte("mat_")) && len(field) > 12 {
				t.Fatalf("artifact %s contains what looks like a task token", path)
			}
		}
		return nil
	})
}
