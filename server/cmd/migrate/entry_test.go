package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestMigrateEntryRefusesBeforeConnecting runs the real migrate binary in a
// subprocess with a deliberately minimal environment. Every refusal case is
// decided from the connection string, so no database is created or reached:
// the DATABASE_URL in each case points at a closed local port, and the
// expected target points at an endpoint that is never dialed. That is stronger
// than asserting the target database was left unchanged — a refusal here cannot
// have issued any statement at all.
//
// The cases mirror the reviewed gaps: a Make chain must not rewrite the
// recorded target, and the connection endpoint must agree with it even when the
// database name matches and the endpoint is not loopback.
func TestMigrateEntryRefusesBeforeConnecting(t *testing.T) {
	binary := buildMigrateBinary(t)
	worktree := linkedWorktreeDir(t)

	const password = "sup3rsecret"
	usable := "postgres://guard:" + password + "@127.0.0.1:1/dev54_entry_dev"
	guardEnv := map[string]string{
		"MULTICA_DB_GUARD_ENV":          "dev54-entry",
		"MULTICA_EXPECTED_DATABASE_URL": usable,
		"DATABASE_URL":                  usable,
	}

	cases := []struct {
		name          string
		dir           string
		env           map[string]string
		args          []string
		wantContains  []string
		forbidSubstrs []string
	}{
		{
			name: "bare up on a connection to another remote host",
			dir:  worktree,
			env: map[string]string{
				"MULTICA_DB_GUARD_ENV":          "dev54-entry",
				"MULTICA_EXPECTED_DATABASE_URL": "postgres://guard:" + password + "@192.0.2.1:6543/dev54_entry_dev",
				"DATABASE_URL":                  usable,
			},
			args:          []string{"up"},
			wantContains:  []string{"the connection targets", "server=127.0.0.1:1", "server=192.0.2.1:6543"},
			forbidSubstrs: []string{password},
		},
		{
			name: "bare up on the same database at another port",
			dir:  worktree,
			env: map[string]string{
				"MULTICA_DB_GUARD_ENV":          "dev54-entry",
				"MULTICA_EXPECTED_DATABASE_URL": "postgres://guard:" + password + "@127.0.0.1:2/dev54_entry_dev",
				"DATABASE_URL":                  usable,
			},
			args:          []string{"up"},
			wantContains:  []string{"the connection targets", "server=127.0.0.1:1", "server=127.0.0.1:2"},
			forbidSubstrs: []string{password},
		},
		{
			name: "the connection reaches a protected database",
			dir:  worktree,
			env: map[string]string{
				"MULTICA_DB_GUARD_ENV":          "dev54-entry",
				"MULTICA_EXPECTED_DATABASE_URL": "postgres://guard:" + password + "@127.0.0.1:1/dev54_entry_protected",
				"DATABASE_URL":                  "postgres://guard:" + password + "@127.0.0.1:1/dev54_entry_protected",
				"MULTICA_PROTECTED_DATABASES":   "dev54_entry_protected",
			},
			args:          []string{"up"},
			wantContains:  []string{"protected database", "database=dev54_entry_protected"},
			forbidSubstrs: []string{password},
		},
		{
			name:          "a recorded target with an empty DATABASE_URL",
			dir:           worktree,
			env:           map[string]string{"MULTICA_EXPECTED_DATABASE_URL": usable, "MULTICA_DB_GUARD_ENV": "dev54-entry"},
			args:          []string{"up"},
			wantContains:  []string{"DATABASE_URL is empty but MULTICA_EXPECTED_DATABASE_URL is set"},
			forbidSubstrs: []string{password},
		},
		{
			name:          "a task worktree with no declared environment",
			dir:           worktree,
			env:           map[string]string{},
			args:          []string{"up"},
			wantContains:  []string{"run through make up or make env-exec"},
			forbidSubstrs: []string{password},
		},
		{
			name:          "verify uses the same pre-connect refusal",
			dir:           worktree,
			env:           map[string]string{"MULTICA_EXPECTED_DATABASE_URL": "postgres://guard:" + password + "@192.0.2.1:6543/dev54_entry_dev", "MULTICA_DB_GUARD_ENV": "dev54-entry", "DATABASE_URL": usable},
			args:          []string{"verify"},
			wantContains:  []string{"the connection targets"},
			forbidSubstrs: []string{password},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exit, _, stderr := runMigrate(t, binary, tc.dir, tc.env, tc.args...)
			if exit != 2 {
				t.Fatalf("exit = %d, want 2 (refused); stderr:\n%s", exit, stderr)
			}
			if !strings.Contains(stderr, "database target check failed") {
				t.Fatalf("stderr does not report a target refusal:\n%s", stderr)
			}
			for _, want := range tc.wantContains {
				if !strings.Contains(stderr, want) {
					t.Fatalf("refusal does not contain %q:\n%s", want, stderr)
				}
			}
			for _, forbid := range tc.forbidSubstrs {
				if strings.Contains(stderr, forbid) {
					t.Fatalf("refusal leaks %q:\n%s", forbid, stderr)
				}
			}
		})
	}

	// Control: a matching target passes the pre-connect check and fails at the
	// dial. Without this, a guard that refused everything would look correct.
	t.Run("matching target is not refused before connecting", func(t *testing.T) {
		exit, _, stderr := runMigrate(t, binary, worktree, guardEnv, "verify")
		if exit == 0 {
			t.Fatalf("verify on a closed port exited 0; stderr:\n%s", stderr)
		}
		if strings.Contains(stderr, "refusing to migrate") {
			t.Fatalf("a matching target was refused instead of dialed:\n%s", stderr)
		}
		if !strings.Contains(stderr, "read database identity") {
			t.Fatalf("expected a dial failure after the target check:\n%s", stderr)
		}
	})
}

// buildMigrateBinary compiles the package under test once per test binary so
// the refusal cases exercise the real entry point, not the functions directly.
func buildMigrateBinary(t *testing.T) string {
	t.Helper()
	serverRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve server root: %v", err)
	}
	binary := filepath.Join(t.TempDir(), "migrate-entry")
	build := exec.Command("go", "build", "-o", binary, "./cmd/migrate")
	build.Dir = serverRoot
	build.Env = databaseFreeEnv()
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build migrate binary: %v\n%s", err, output)
	}
	return binary
}

// linkedWorktreeDir returns a temporary directory whose nearest .git is a file,
// the shape a linked worktree has.
func linkedWorktreeDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: /elsewhere\n"), 0o644); err != nil {
		t.Fatalf("write .git pointer: %v", err)
	}
	return dir
}

// runMigrate executes the built binary with the inherited environment minus
// every database declaration. No DATABASE_URL or MULTICA_* value from the test
// process reaches the child, so an inherited connection can never become the
// target of a default test.
func runMigrate(t *testing.T, binary, dir string, env map[string]string, args ...string) (int, string, string) {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Dir = dir
	cmd.Env = databaseFreeEnv()
	for key, value := range env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	exit := 0
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("run migrate: %v", err)
		}
		exit = exitErr.ExitCode()
	}
	return exit, stdout.String(), stderr.String()
}

// databaseFreeEnv keeps HOME and the module cache so the build and the binary
// work, but drops every database or guard declaration inherited from the shell.
func databaseFreeEnv() []string {
	env := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if key == "DATABASE_URL" || strings.HasPrefix(key, "MULTICA_") {
			continue
		}
		env = append(env, entry)
	}
	return env
}
