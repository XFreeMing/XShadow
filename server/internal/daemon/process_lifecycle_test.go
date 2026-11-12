//go:build linux

package daemon_test

// Failure-path coverage for the process lifecycle used by the goal plan chain
// test. The chain starts the server, then the daemon. Anything that fails in
// between (health check, PID validation, daemon log creation, daemon start)
// must still stop the already started server exactly once and leave evidence.
//
// The scenario runs in a child of this test binary so that the deliberately
// failing t.Fatal path is real: the harness starts a test-owned stub process,
// then fails while starting a daemon whose binary does not exist — the same
// point where startChild fails in the chain test. The parent only accepts a
// failed harness whose stub was stopped by the cleanup registered at start.
//
// The stop-time stable-daemon check is scripted here, not executed: these
// tests must never depend on a user-installed agent CLI. The regression tests
// below drive the same stop code with test-owned sleep helpers and a guard
// whose answers are controlled per call, covering query failure, per-signal
// re-query, SIGKILL escalation refusal, and idempotent repeated stop.

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestProcessCleanupAfterStartupFailure(t *testing.T) {
	root := t.TempDir()
	// A caller that wants durable evidence can point this at a real directory;
	// the default keeps the usual t.TempDir cleanup.
	artifacts := os.Getenv("MULTICA_LIFECYCLE_ARTIFACTS")
	if artifacts == "" {
		artifacts = filepath.Join(root, "artifacts")
	}
	home := filepath.Join(root, "home")
	tmp := filepath.Join(root, "tmp")
	for _, dir := range []string{artifacts, home, tmp} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	logPath := filepath.Join(artifacts, "harness.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	harness := exec.Command(os.Args[0],
		"-test.run=^TestProcessCleanupStartupFailureHarness$",
		"-test.v",
		"-test.count=1",
	)
	harness.Dir = root
	harness.Env = []string{
		"PATH=/usr/bin:/bin",
		"HOME=" + home,
		"TMPDIR=" + tmp,
		"MULTICA_LIFECYCLE_HARNESS=1",
		"MULTICA_LIFECYCLE_ROOT=" + root,
		"MULTICA_LIFECYCLE_ARTIFACTS=" + artifacts,
	}
	harness.Stdout = logFile
	harness.Stderr = logFile
	harness.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	runErr := harness.Run()
	logFile.Close()

	harnessLog, _ := os.ReadFile(logPath)
	if runErr == nil {
		t.Fatalf("harness must fail at daemon start to exercise cleanup; log:\n%s", harnessLog)
	}
	if !strings.Contains(string(harnessLog), "server-stub started pid=") {
		t.Fatalf("harness did not start the server stub before failing; log:\n%s", harnessLog)
	}
	if !strings.Contains(string(harnessLog), "start daemon-stub:") {
		t.Fatalf("harness did not fail at daemon start; log:\n%s", harnessLog)
	}
	if !strings.Contains(string(harnessLog), "no such file or directory") && !strings.Contains(string(harnessLog), "executable file not found") {
		t.Fatalf("harness did not report the missing daemon binary; log:\n%s", harnessLog)
	}

	var started struct {
		ServerPID int `json:"server_pid"`
	}
	readArtifact(t, artifacts, "failure-start.json", &started)
	if started.ServerPID <= 0 {
		t.Fatalf("harness recorded no server pid: %+v", started)
	}

	var cleanup struct {
		PID           int            `json:"pid"`
		AliveBefore   bool           `json:"alive_before_second_stop"`
		Alive         bool           `json:"alive_at_report"`
		StatusQueries int            `json:"status_queries"`
		Second        map[string]any `json:"second_stop"`
	}
	readArtifact(t, artifacts, "failure-cleanup.json", &cleanup)
	if cleanup.PID != started.ServerPID {
		t.Fatalf("cleanup recorded pid %d, started pid %d", cleanup.PID, started.ServerPID)
	}
	// startChild's own cleanup must have stopped the stub before this later
	// reporter ran. If it had not, the reporter's stop below is what killed the
	// process and the failure window was still leaking.
	if cleanup.AliveBefore {
		t.Fatalf("server stub %d was still running when the post-start reporter ran; the start-time cleanup did not stop it", cleanup.PID)
	}
	if cleanup.Alive {
		t.Fatalf("server stub %d was still alive when the cleanup report was written", cleanup.PID)
	}
	if got := cleanup.Second["stopped_by"]; got != "SIGTERM" {
		t.Fatalf("cleanup stopped the server stub by %v, want SIGTERM", got)
	}
	// The start-registered cleanup sent the only signal; the later explicit
	// stop reused that result instead of signalling the process again.
	if got := cleanup.Second["signals_sent"]; got != float64(1) {
		t.Fatalf("cleanup sent %v signals, want exactly 1", got)
	}
	// The start-registered cleanup queried the stable daemon once before its
	// SIGTERM; the later explicit stop only reused the cached per-process
	// result and must not query (or signal) again.
	if cleanup.StatusQueries != 1 {
		t.Fatalf("status guard answered %d queries, want exactly 1 (the one before the start cleanup's SIGTERM)", cleanup.StatusQueries)
	}
	if checks, ok := cleanup.Second["stop_time_checks"].([]any); !ok || len(checks) != 1 {
		t.Fatalf("second stop recorded stop_time_checks=%v, want the one SIGTERM query", cleanup.Second["stop_time_checks"])
	}
	if processAlive(started.ServerPID) {
		// A regression here would otherwise orphan the stub; stop exactly the
		// pid the harness reported before failing the test.
		_ = syscall.Kill(-started.ServerPID, syscall.SIGKILL)
		t.Fatalf("server stub pid %d survived the harness cleanup", started.ServerPID)
	}
	if _, err := os.Stat(filepath.Join(artifacts, "daemon-stub.log")); err != nil {
		t.Fatalf("daemon log was not created before the start failure: %v", err)
	}
	t.Logf("startup failure cleaned up: server stub pid=%d stopped_by=SIGTERM signals_sent=1 daemon_start_error=%v", started.ServerPID, runErr)
}

// TestProcessCleanupStartupFailureHarness is the deliberately failing child. It
// only runs when the parent above launches it.
func TestProcessCleanupStartupFailureHarness(t *testing.T) {
	if os.Getenv("MULTICA_LIFECYCLE_HARNESS") != "1" {
		t.Skip("harness runs only as a child of TestProcessCleanupAfterStartupFailure")
	}
	root := os.Getenv("MULTICA_LIFECYCLE_ROOT")
	artifacts := os.Getenv("MULTICA_LIFECYCLE_ARTIFACTS")
	if root == "" || artifacts == "" {
		t.Fatal("harness requires MULTICA_LIFECYCLE_ROOT and MULTICA_LIFECYCLE_ARTIFACTS")
	}
	env := []string{
		"PATH=/usr/bin:/bin",
		"HOME=" + filepath.Join(root, "home"),
		"TMPDIR=" + filepath.Join(root, "tmp"),
		"MULTICA_LIFECYCLE_SLEEP_HELPER=1",
		"MULTICA_LIFECYCLE_READY_FILE=" + filepath.Join(root, "stub.ready"),
	}

	var server *childProc
	// Test-owned scripted answers: no runtime CLI is executed, and the answer
	// is not the stub's pid, so the stop proceeds only through the exact
	// process group the harness started.
	guard := &scriptedGuard{respond: func(int) map[string]any { return verifiedDaemon(930001) }}
	// Registered before startChild, so this report runs after the cleanup that
	// startChild installed. The recorded pre-stop liveness distinguishes an
	// already-cleaned stub from one that only this reporter would stop.
	t.Cleanup(func() {
		aliveBefore := processAlive(server.cmd.Process.Pid)
		second := server.stop(t, 5*time.Second)
		writeArtifact(t, artifacts, "failure-cleanup.json", map[string]any{
			"pid":                      server.cmd.Process.Pid,
			"alive_before_second_stop": aliveBefore,
			"alive_at_report":          processAlive(server.cmd.Process.Pid),
			"status_queries":           guard.count(),
			"second_stop":              second,
		})
	})

	server = startChild(t, "server-stub", root, os.Args[0],
		[]string{"-test.run=^TestProcessCleanupSleepHelper$"},
		env, filepath.Join(artifacts, "server-stub.log"), guard.guard())
	writeArtifact(t, artifacts, "failure-start.json", map[string]any{"server_pid": server.cmd.Process.Pid})

	waitFor(t, 20*time.Second, "server stub ready", func() (bool, string) {
		_, err := os.Stat(filepath.Join(root, "stub.ready"))
		if err != nil {
			return false, err.Error()
		}
		return true, ""
	})

	// Fail exactly where the chain fails when the daemon binary cannot start:
	// the daemon log is created and cmd.Start fails, before any daemon cleanup
	// could be registered. Only the server's start-time cleanup remains, and it
	// must stop the server.
	startChild(t, "daemon-stub", root, filepath.Join(root, "missing-daemon-binary"),
		nil, env, filepath.Join(artifacts, "daemon-stub.log"), nil)
	t.Fatalf("daemon-stub started unexpectedly")
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

// scriptedGuard is the test-owned replacement for the runtime CLI. It answers
// each call independently, records how many answers were produced, and never
// resolves or executes an installed agent CLI.
type scriptedGuard struct {
	mu      sync.Mutex
	calls   int
	respond func(call int) map[string]any
}

func (s *scriptedGuard) guard() *stableDaemonGuard {
	return &stableDaemonGuard{query: func(context.Context) map[string]any {
		s.mu.Lock()
		s.calls++
		call := s.calls
		s.mu.Unlock()
		return s.respond(call)
	}}
}

func (s *scriptedGuard) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func verifiedDaemon(pid int) map[string]any {
	return map[string]any{"stable_daemon_pid": pid, "stable_daemon_status": "running"}
}

func stubEnv(root, marker string) []string {
	return []string{
		"PATH=/usr/bin:/bin",
		"HOME=" + filepath.Join(root, "home"),
		"TMPDIR=" + filepath.Join(root, "tmp"),
		marker + "=1",
	}
}

// startStub starts a test-owned process that ignores nothing and sleeps until
// it receives SIGTERM. A kill of the exact group is registered as a safety net:
// when a test deliberately refuses to signal, the stub must still not outlive
// the test run.
func startStub(t *testing.T, root, name string, guard *stableDaemonGuard) *childProc {
	t.Helper()
	p := startChild(t, name, root, os.Args[0],
		[]string{"-test.run=^TestProcessCleanupSleepHelper$"},
		stubEnv(root, "MULTICA_LIFECYCLE_SLEEP_HELPER"), filepath.Join(root, name+".log"), guard)
	killGroupOnCleanup(t, p.cmd.Process.Pid)
	return p
}

// startIgnoreTermStub starts a test-owned process that ignores SIGTERM, so the
// stop code reaches its SIGKILL escalation gate. The helper announces itself
// through a ready file only after it has installed the ignore handler, which
// removes the race between exec and the first signal.
func startIgnoreTermStub(t *testing.T, root, name string, guard *stableDaemonGuard) *childProc {
	t.Helper()
	ready := filepath.Join(root, name+".ready")
	env := append(stubEnv(root, "MULTICA_LIFECYCLE_IGNORE_TERM"), "MULTICA_LIFECYCLE_READY_FILE="+ready)
	p := startChild(t, name, root, os.Args[0],
		[]string{"-test.run=^TestProcessCleanupIgnoreTermHelper$"},
		env, filepath.Join(root, name+".log"), guard)
	waitFor(t, 10*time.Second, name+" ready", func() (bool, string) {
		if _, err := os.Stat(ready); err != nil {
			return false, err.Error()
		}
		return true, ""
	})
	killGroupOnCleanup(t, p.cmd.Process.Pid)
	return p
}

// killGroupOnCleanup removes a deliberately unstopped test fixture. It is a
// fixture teardown, not a stop path: it only ever targets the exact process
// group this test started, and only after the stop under test has finished.
func killGroupOnCleanup(t *testing.T, pid int) {
	t.Cleanup(func() {
		if processAlive(pid) {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}
	})
}

// TestStopRefusesWithoutVerifiedStableDaemon covers both ways the stop gate can
// lack an answer: no guard at all, and a guard whose query fails. Neither may
// send a signal, and both must report the exact unstopped pid, process group,
// log and reason.
func TestStopRefusesWithoutVerifiedStableDaemon(t *testing.T) {
	t.Run("no guard", func(t *testing.T) {
		root := t.TempDir()
		for _, dir := range []string{filepath.Join(root, "home"), filepath.Join(root, "tmp")} {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		p := startStub(t, root, "no-guard-stub", nil)
		p.expectRefusal = true
		res := p.stop(t, 2*time.Second)
		assertRefusedStop(t, res, "refused-unverified-SIGTERM", 0, "no runtime CLI guard configured", p.cmd.Process.Pid)
		if !processAlive(p.cmd.Process.Pid) {
			t.Fatalf("stub pid %d died even though no signal was allowed", p.cmd.Process.Pid)
		}
	})

	t.Run("query failure", func(t *testing.T) {
		root := t.TempDir()
		for _, dir := range []string{filepath.Join(root, "home"), filepath.Join(root, "tmp")} {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		g := &scriptedGuard{respond: func(int) map[string]any {
			return map[string]any{"error": "daemon status query timed out"}
		}}
		p := startStub(t, root, "query-error-stub", g.guard())
		p.expectRefusal = true
		res := p.stop(t, 2*time.Second)
		assertRefusedStop(t, res, "refused-unverified-SIGTERM", 0, "daemon status query timed out", p.cmd.Process.Pid)
		if got := g.count(); got != 1 {
			t.Fatalf("guard answered %d queries, want exactly 1 for the refused SIGTERM", got)
		}
		if !processAlive(p.cmd.Process.Pid) {
			t.Fatalf("stub pid %d died even though the query failed", p.cmd.Process.Pid)
		}
	})
}

func assertRefusedStop(t *testing.T, res map[string]any, wantStoppedBy string, wantSignals int, wantReason string, wantPID int) {
	t.Helper()
	if got := res["stopped_by"]; got != wantStoppedBy {
		t.Fatalf("stopped_by = %v, want %s (result %+v)", got, wantStoppedBy, res)
	}
	if got := res["signals_sent"]; got != wantSignals {
		t.Fatalf("signals_sent = %v, want %d (result %+v)", got, wantSignals, res)
	}
	reason, _ := res["refusal"].(string)
	if !strings.Contains(reason, wantReason) {
		t.Fatalf("refusal %q does not explain %q", reason, wantReason)
	}
	if got := res["pid"]; got != wantPID {
		t.Fatalf("recorded pid = %v, want %d", got, wantPID)
	}
	if got := res["unstopped_pid"]; got != wantPID {
		t.Fatalf("recorded unstopped_pid = %v, want %d", got, wantPID)
	}
	if got := res["process_group"]; got != -wantPID {
		t.Fatalf("recorded process_group = %v, want %d", got, -wantPID)
	}
	if logPath, _ := res["log"].(string); logPath == "" {
		t.Fatal("refused stop did not record its log path")
	}
}

// TestStopQueriesFreshlyBeforeEachSignal starts two targets under one scripted
// source, changes the answer between their stops, and repeats a stop. Each
// target must get its own query, each must see the then-current answer, and a
// repeated stop must neither query nor signal again.
func TestStopQueriesFreshlyBeforeEachSignal(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{filepath.Join(root, "home"), filepath.Join(root, "tmp")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	g := &scriptedGuard{respond: func(call int) map[string]any {
		return verifiedDaemon(920000 + call)
	}}
	first := startStub(t, root, "stub-a", g.guard())
	second := startStub(t, root, "stub-b", g.guard())

	firstRes := first.stop(t, 3*time.Second)
	secondRes := second.stop(t, 3*time.Second)
	if got := g.count(); got != 2 {
		t.Fatalf("guard answered %d queries, want one before each of the two SIGTERMs", got)
	}
	if got := singleCheckPID(t, firstRes); got != 920001 {
		t.Fatalf("first target verified against pid %d, want the first answer 920001", got)
	}
	if got := singleCheckPID(t, secondRes); got != 920002 {
		t.Fatalf("second target verified against pid %d, want the changed answer 920002", got)
	}

	// A third answer would prove a cached result was reused; the guard must not
	// be asked again for an already stopped target.
	repeated := first.stop(t, 3*time.Second)
	if got := g.count(); got != 2 {
		t.Fatalf("repeated stop produced a new query (total %d); the per-process result must be reused", got)
	}
	if got := repeated["signals_sent"]; got != 1 {
		t.Fatalf("repeated stop sent %v signals, want the original 1", got)
	}
	if got := repeated["stopped_by"]; got != "SIGTERM" {
		t.Fatalf("repeated stop reported %v, want the original SIGTERM", got)
	}
}

func singleCheckPID(t *testing.T, res map[string]any) int {
	t.Helper()
	checks, ok := res["stop_time_checks"].([]map[string]any)
	if !ok || len(checks) != 1 {
		t.Fatalf("stop_time_checks = %v, want exactly one SIGTERM query", res["stop_time_checks"])
	}
	pid, _ := checks[0]["stable_daemon_pid"].(int)
	return pid
}

// TestStopRefusesSIGKILLWhenEscalationQueryFails makes the first query succeed
// and the second fail. SIGTERM may be sent (verified), but the escalation query
// fails, so no SIGKILL may follow and the process must be reported as still
// running.
func TestStopRefusesSIGKILLWhenEscalationQueryFails(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{filepath.Join(root, "home"), filepath.Join(root, "tmp")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	g := &scriptedGuard{respond: func(call int) map[string]any {
		if call == 1 {
			return verifiedDaemon(910001)
		}
		return map[string]any{"error": "daemon status query failed before SIGKILL"}
	}}
	p := startIgnoreTermStub(t, root, "ignore-term-stub", g.guard())
	p.expectRefusal = true

	res := p.stop(t, 400*time.Millisecond)
	if got := res["stopped_by"]; got != "refused-unverified-SIGKILL" {
		t.Fatalf("stopped_by = %v, want refused-unverified-SIGKILL (result %+v)", got, res)
	}
	if got := res["signals_sent"]; got != 1 {
		t.Fatalf("signals_sent = %v, want only the verified SIGTERM", got)
	}
	if got := g.count(); got != 2 {
		t.Fatalf("guard answered %d queries, want one before SIGTERM and one before SIGKILL", got)
	}
	checks, ok := res["stop_time_checks"].([]map[string]any)
	if !ok || len(checks) != 2 {
		t.Fatalf("stop_time_checks = %v, want both signal gates", res["stop_time_checks"])
	}
	if got := checks[1]["signal"]; got != "before SIGKILL" {
		t.Fatalf("second gate is %v, want before SIGKILL", got)
	}
	if msg, _ := checks[1]["error"].(string); !strings.Contains(msg, "before SIGKILL") {
		t.Fatalf("second gate error %q does not explain the failed re-query", msg)
	}
	if !processAlive(p.cmd.Process.Pid) {
		t.Fatalf("stub pid %d exited although SIGKILL was refused", p.cmd.Process.Pid)
	}
	if got := res["unstopped_pid"]; got != p.cmd.Process.Pid {
		t.Fatalf("unstopped_pid = %v, want %d", got, p.cmd.Process.Pid)
	}
}

// TestDaemonStatusDecodeRejectsUnverifiableAnswers covers the runtime CLI
// output gate: invalid JSON, a missing pid, and a non-running status must all
// surface as an error, which the stop gate turns into a refusal.
func TestDaemonStatusDecodeRejectsUnverifiableAnswers(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{"running", `{"pid":2673688,"status":"running"}`, false},
		{"invalid json", `{"pid":`, true},
		{"missing pid", `{"status":"running"}`, true},
		{"stopped", `{"pid":0,"status":"stopped"}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := decodeDaemonStatus([]byte(tc.raw))
			msg, _ := out["error"].(string)
			if tc.wantErr && msg == "" {
				t.Fatalf("decode %s accepted as verified: %+v", tc.raw, out)
			}
			if !tc.wantErr && msg != "" {
				t.Fatalf("decode %s refused: %s", tc.raw, msg)
			}
		})
	}
}

// TestProcessCleanupSleepHelper is the test-owned stub process. It is never
// resolved through PATH; the harness runs this same test binary.
func TestProcessCleanupSleepHelper(t *testing.T) {
	if os.Getenv("MULTICA_LIFECYCLE_SLEEP_HELPER") != "1" {
		t.Skip("helper runs only as a child of the lifecycle harness")
	}
	if ready := os.Getenv("MULTICA_LIFECYCLE_READY_FILE"); ready != "" {
		if err := os.WriteFile(ready, []byte("ready\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for {
		time.Sleep(time.Hour)
	}
}

// TestProcessCleanupIgnoreTermHelper is the test-owned stub that survives
// SIGTERM so the escalation gate can be exercised.
func TestProcessCleanupIgnoreTermHelper(t *testing.T) {
	if os.Getenv("MULTICA_LIFECYCLE_IGNORE_TERM") != "1" {
		t.Skip("helper runs only as a child of TestStopRefusesSIGKILLWhenEscalationQueryFails")
	}
	signal.Ignore(syscall.SIGTERM)
	if ready := os.Getenv("MULTICA_LIFECYCLE_READY_FILE"); ready != "" {
		if err := os.WriteFile(ready, []byte("ready\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for {
		time.Sleep(time.Hour)
	}
}

func readArtifact(t *testing.T, dir, name string, out any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
}
