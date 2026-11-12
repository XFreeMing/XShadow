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

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestProcessCleanupAfterStartupFailure(t *testing.T) {
	root := t.TempDir()
	artifacts := filepath.Join(root, "artifacts")
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
		PID         int            `json:"pid"`
		AliveBefore bool           `json:"alive_before_second_stop"`
		Alive       bool           `json:"alive_at_report"`
		Second      map[string]any `json:"second_stop"`
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
			"second_stop":              second,
		})
	})

	server = startChild(t, "server-stub", root, os.Args[0],
		[]string{"-test.run=^TestProcessCleanupSleepHelper$"},
		env, filepath.Join(artifacts, "server-stub.log"))
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
		nil, env, filepath.Join(artifacts, "daemon-stub.log"))
	t.Fatalf("daemon-stub started unexpectedly")
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

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
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
