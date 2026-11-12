//go:build linux

package daemon_test

// Process-level verification of the goal plan run chain.
//
// The test starts the real server binary and the real daemon binary from this
// tree, forces task claiming over the production HTTP route (the transport
// proxy below refuses WebSocket upgrades), and lets a test-owned `opencode`
// executable read the prompt the daemon received. The fake executor locates the
// controlled local_directory file through the absolute path in the prompt,
// reads it, and submits the plan with this tree's CLI through the real
// task-token auth path.
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
//
// Child processes are started with an explicit environment (exec.Cmd.Env), so
// no ambient MULTICA_* value, task token, or host PATH entry reaches them.

import (
	"bytes"
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
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
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
	}
	for name, value := range map[string]string{
		"MULTICA_CHAIN_SERVER_BIN":     cfg.serverBin,
		"MULTICA_CHAIN_CLI_BIN":        cfg.cliBin,
		"MULTICA_CHAIN_DATABASE_URL":   cfg.dbURL,
		"MULTICA_CHAIN_PORT":           cfg.apiPort,
		"MULTICA_CHAIN_PROFILE_CONFIG": cfg.profileConfig,
		"MULTICA_CHAIN_ARTIFACTS":      cfg.artifacts,
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
printf 'task=%s\nagent=%s\nserver=%s\ntoken_prefix=%s\ntoken_len=%s\n' \
  "$MULTICA_TASK_ID" "$MULTICA_AGENT_ID" "$MULTICA_SERVER_URL" \
  "$(printf '%s' "$tok" | cut -c1-4)" "$(printf '%s' "$tok" | wc -c)" > "$D/task-env.txt"
goal=$(sed -n 's/.*--goal \([0-9a-f-]*\) --generation.*/\1/p' "$D/prompt.txt" | head -n 1)
gen=$(sed -n 's/.*--generation \([0-9a-f-]*\).*/\1/p' "$D/prompt.txt" | head -n 1)
path=$(sed -n 's/.*"local_path":[[:space:]]*"\([^"]*\)".*/\1/p' "$D/prompt.txt" | head -n 1)
rid=$(sed -n 's/.*- id=\([0-9a-f-]*\) type=local_directory.*/\1/p' "$D/prompt.txt" | head -n 1)
[ -z "$rid" ] && rid="$path"
printf 'goal=%s\ngeneration=%s\nresource_path=%s\nresource_id=%s\n' "$goal" "$gen" "$path" "$rid" > "$D/parsed.txt"
if [ -z "$goal" ] || [ -z "$gen" ] || [ -z "$path" ]; then
  echo "prompt parse failed: goal=$goal gen=$gen path=$path" > "$D/cli.err"
  exit 2
fi
content=$(cat "$path/context.md")
sha=$(sha256sum "$path/context.md" | cut -d' ' -f1)
printf 'context_sha256=%s\ncontext=%s\n' "$sha" "$content" > "$D/context-read.txt"
cat > "$D/plan.json" <<PLAN
{"items":[{"title":"Process chain item","outcome":"The planning run produced this item from the resource it actually read.","acceptance_criteria":"The stored candidate records the controlled context digest.","evidence_refs":[{"kind":"file","ref":"$path/context.md","version":"sha256:$sha","note":"$content"}],"binding":{"type":"new"}}],"edges":[],"context_snapshot":{"files":[{"path":"$path/context.md","summary":"$content"}],"resources":[{"id":"$rid","read":true}],"read_at":"2026-11-12T00:00:00Z"}}
PLAN
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
}

func startChild(t *testing.T, name, workDir, bin string, args, env []string, logPath string) *childProc {
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
	p := &childProc{name: name, cmd: cmd, log: logFile, done: make(chan error, 1)}
	go func() { p.done <- cmd.Wait() }()
	t.Logf("%s started pid=%d", name, cmd.Process.Pid)
	return p
}

func (p *childProc) stop(t *testing.T, timeout time.Duration) map[string]any {
	t.Helper()
	result := map[string]any{"name": p.name, "pid": p.cmd.Process.Pid}
	if p.cmd.Process == nil {
		return result
	}
	_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGTERM)
	select {
	case <-p.done:
		result["stopped_by"] = "SIGTERM"
	case <-time.After(timeout):
		_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
		<-p.done
		result["stopped_by"] = "SIGKILL"
	}
	p.log.Close()
	return result
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

	server := startChild(t, "server", root, cfg.serverBin, nil, serverEnv, filepath.Join(cfg.artifacts, "server.log"))
	defer func() {
		if server.cmd.Process != nil {
			t.Logf("server pid=%d", server.cmd.Process.Pid)
		}
	}()
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
	daemon := startChild(t, "daemon", root, cfg.cliBin, daemonArgs, daemonEnv, filepath.Join(cfg.artifacts, "daemon.log"))

	cleanup := map[string]any{}
	defer func() {
		cleanup["daemon"] = daemon.stop(t, 30*time.Second)
		cleanup["server"] = server.stop(t, 30*time.Second)
		cleanup["proxy"] = "closed"
		cleanup["daemon_pid"] = daemon.cmd.Process.Pid
		cleanup["server_pid"] = server.cmd.Process.Pid
		cleanup["stable_daemon_pid"] = stablePID
		cleanup["stable_daemon_touched"] = stablePID != 0 && (stablePID == daemon.cmd.Process.Pid || stablePID == server.cmd.Process.Pid)
		// The foreground daemon routes structured logs into its profile's
		// rotating daemon.log rather than the inherited stdout, so copy it out
		// of the temporary HOME before it disappears.
		copied := []string{}
		for _, name := range []string{"daemon.log", "daemon.err.log"} {
			raw, err := os.ReadFile(filepath.Join(daemonHomeConfigDir, name))
			if err != nil {
				continue
			}
			if os.WriteFile(filepath.Join(cfg.artifacts, "profile-"+name), raw, 0o600) == nil {
				copied = append(copied, "profile-"+name)
			}
		}
		cleanup["profile_logs"] = copied
		writeArtifact(t, cfg.artifacts, "cleanup.json", cleanup)
		assertNoCredentials(t, cfg.artifacts, profile.Token)
	}()
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

	requestKey := fmt.Sprintf("chain-%d", time.Now().UnixNano())
	var created struct {
		ID     string `json:"id"`
		State  string `json:"state"`
		TaskID string `json:"task_id"`
	}
	api.mustJSON(t, http.MethodPost, "/api/goals/"+goal.ID+"/plan/generations", map[string]any{
		"intent":            "生成一份包含受控文件依据的计划草稿",
		"request_key":       requestKey,
		"expected_revision": 0,
		"agent_id":          agent.ID,
	}, &created, http.StatusAccepted)
	t.Logf("generation %s queued task=%s", created.ID, created.TaskID)

	writeArtifact(t, cfg.artifacts, "fixtures.json", map[string]any{
		"project_id":   project.ID,
		"resource_id":  resource.ID,
		"agent_id":     agent.ID,
		"goal_id":      goal.ID,
		"generation_id": created.ID,
		"task_id":      created.TaskID,
		"runtime_id":   runtimeID,
		"daemon_id":    daemonID,
		"context_path": contextPath,
		"context_sha":  contextSHA,
		"marker":       marker,
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
	if !strings.Contains(string(readRaw), "context_sha256="+contextSHA) || !strings.Contains(string(readRaw), marker) {
		t.Fatalf("fake executor did not read the controlled file: %q", string(readRaw))
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

	records := rec.snapshot()
	writeArtifact(t, cfg.artifacts, "proxy.json", records)
	if n := rec.countClaim(); n == 0 {
		t.Fatalf("no HTTP task claim reached the server; claims must not use the WebSocket transport")
	}
	if n := rec.countResults("mat_"); n == 0 {
		t.Fatalf("no result submission used the task token over the real auth route")
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