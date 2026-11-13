package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// expectedDatabaseURLEnv carries the connection string the caller verified
	// against the checkout's environment record. Without it, a research task
	// worktree can silently inherit a stale DATABASE_URL for a protected
	// database and apply DDL there.
	expectedDatabaseURLEnv = "MULTICA_EXPECTED_DATABASE_URL"
	// protectedDatabasesEnv lists database names no guarded run may ever write.
	protectedDatabasesEnv = "MULTICA_PROTECTED_DATABASES"
	// databaseGuardEnvEnv names the selected dev environment. When it is set but
	// no expected URL accompanies it, the caller's configuration is broken and
	// the run must refuse rather than fall back to whatever DATABASE_URL leaked
	// into the process.
	databaseGuardEnvEnv = "MULTICA_DB_GUARD_ENV"
)

// databaseTarget is the subset of a live database identity that can safely
// appear in logs. User and password are deliberately absent so a refusal never
// leaks credentials.
type databaseTarget struct {
	Database string
	Host     string
	Port     int
}

// redacted returns "database=<name> server=<host>:<port>" and never includes
// user or password.
func (t databaseTarget) redacted() string {
	return fmt.Sprintf("database=%s server=%s:%d", t.Database, t.Host, t.Port)
}

// parseDatabaseTarget extracts the database, host and port from a connection
// string using pgx's parser so URL and keyword/value DSNs behave exactly as they
// will when the pool connects.
func parseDatabaseTarget(raw string) (databaseTarget, error) {
	config, err := pgx.ParseConfig(raw)
	if err != nil {
		return databaseTarget{}, fmt.Errorf("parse database target: %w", err)
	}
	return databaseTarget{
		Database: config.Database,
		Host:     config.Host,
		Port:     int(config.Port),
	}, nil
}

// normalizeDatabaseHost collapses loopback spellings so a caller that verified
// "localhost" matches a server that reports "127.0.0.1". Anything that is not a
// known loopback spelling is returned untouched.
func normalizeDatabaseHost(host string) string {
	switch host {
	case "localhost", "127.0.0.1", "::1", "[::1]":
		return "127.0.0.1"
	default:
		return host
	}
}

// sameServer reports whether two targets identify the same database. The
// database name is the contract: a stale DATABASE_URL pointing at a different or
// protected database is what the guard exists to catch. Host and port are only
// comparable for direct local connections — Docker publishes a host port while
// the server reports its container-internal address and port, so comparing them
// there would reject the verified database itself. Whenever either host is not a
// loopback spelling (container address, remote host, NAT, empty host), host and
// port are ignored.
func (t databaseTarget) sameServer(other databaseTarget) bool {
	if t.Database != other.Database {
		return false
	}
	if isLoopbackHost(t.Host) && isLoopbackHost(other.Host) {
		if t.Port != 0 && other.Port != 0 && t.Port != other.Port {
			return false
		}
	}
	return true
}

// isLoopbackHost reports whether host is one of the loopback spellings a DSN or
// a server may report for a direct local connection.
func isLoopbackHost(host string) bool {
	return normalizeDatabaseHost(host) == "127.0.0.1"
}

// readDatabaseIdentity asks the server which database the connection actually
// reached. It is strictly read-only: the trusted DATABASE_URL may point at a
// protected database, so any write here would be the very incident this guard
// exists to prevent.
func readDatabaseIdentity(ctx context.Context, pool *pgxpool.Pool) (databaseTarget, error) {
	var target databaseTarget
	err := pool.QueryRow(ctx, `SELECT current_database(), COALESCE(host(inet_server_addr()), ''), COALESCE(inet_server_port(), 0)`).Scan(&target.Database, &target.Host, &target.Port)
	if err != nil {
		return databaseTarget{}, fmt.Errorf("read database identity: %w", err)
	}
	return target, nil
}

// databaseGuard captures everything the caller declared about the database a
// run is allowed to touch. It is plain data so every refusal can be tested
// without a live database.
type databaseGuard struct {
	Expected       string
	Protected      map[string]bool
	SelectedEnv    string
	LinkedWorktree bool
}

// active reports whether any verification input exists. Inactive runs keep
// their historical behavior and must not pay for an extra database round trip.
func (g databaseGuard) active() bool {
	return g.Expected != "" || g.SelectedEnv != "" || g.LinkedWorktree || len(g.Protected) > 0
}

// databaseGuardFromEnv reads the caller's declaration and detects whether the
// working directory sits inside a linked git worktree, which is how a task
// worktree differs from the main checkout or a container.
func databaseGuardFromEnv(getenv func(string) string, cwd string) databaseGuard {
	guard := databaseGuard{
		Expected:    strings.TrimSpace(getenv(expectedDatabaseURLEnv)),
		Protected:   make(map[string]bool),
		SelectedEnv: strings.TrimSpace(getenv(databaseGuardEnvEnv)),
	}
	for _, name := range strings.Split(getenv(protectedDatabasesEnv), ",") {
		name = strings.TrimSpace(name)
		if name != "" {
			guard.Protected[name] = true
		}
	}
	guard.LinkedWorktree = isLinkedWorktree(cwd)
	return guard
}

// isLinkedWorktree reports whether the nearest .git above cwd is a file. A
// linked worktree stores a "gitdir:" pointer there instead of a .git directory,
// and that is exactly the checkout shape whose inherited DATABASE_URL caused the
// incident.
func isLinkedWorktree(cwd string) bool {
	dir := cwd
	for {
		info, err := os.Stat(filepath.Join(dir, ".git"))
		if err == nil {
			return !info.IsDir()
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

// evaluateDatabaseTarget is pure so every refusal can be unit-tested without a
// database. It returns a redacted error for every refusal.
func evaluateDatabaseTarget(actual databaseTarget, guard databaseGuard) error {
	if guard.Protected[actual.Database] {
		return fmt.Errorf("refusing to migrate protected database: %s", actual.redacted())
	}
	if guard.Expected == "" {
		if guard.SelectedEnv != "" {
			return fmt.Errorf("refusing to migrate: environment %q has no verified target (%s)", guard.SelectedEnv, actual.redacted())
		}
		if guard.LinkedWorktree {
			return fmt.Errorf("refusing to migrate from a task worktree without a verified target; run through make up or make env-exec (%s)", actual.redacted())
		}
		// Legacy main checkout, CI, and containers have no declaration to check
		// against. Keep them working unchanged.
		return nil
	}

	expected, err := parseDatabaseTarget(guard.Expected)
	if err != nil {
		// The raw URL is never echoed: it can contain a password.
		return fmt.Errorf("invalid MULTICA_EXPECTED_DATABASE_URL: the verified target cannot be parsed (%s)", actual.redacted())
	}
	if !expected.sameServer(actual) {
		return fmt.Errorf("refusing to migrate: connected to %s but the verified target is %s", actual.redacted(), expected.redacted())
	}
	return nil
}

// verifyDatabaseTarget reads the live identity and applies the guard. It is
// called before migrations run, so a refusal always precedes any DDL.
func verifyDatabaseTarget(ctx context.Context, pool *pgxpool.Pool, rawDatabaseURL string) (databaseTarget, error) {
	guard := databaseGuardFromEnv(os.Getenv, currentDirectory())
	// The binary falls back to a built-in localhost URL when DATABASE_URL is
	// empty. Under a guard that fallback would silently migrate a database the
	// caller never verified, so refuse before connecting.
	if guard.active() && rawDatabaseURL == "" {
		return databaseTarget{}, emptyDatabaseURLError(guard)
	}

	actual, err := readDatabaseIdentity(ctx, pool)
	if err != nil {
		return databaseTarget{}, err
	}
	if err := evaluateDatabaseTarget(actual, guard); err != nil {
		return actual, err
	}
	return actual, nil
}

// emptyDatabaseURLError explains why a guarded run cannot use the built-in
// fallback URL. A declared target that did not resolve is a configuration
// error; a task worktree or selected environment without any target means the
// caller must enter the managed entry point instead.
func emptyDatabaseURLError(guard databaseGuard) error {
	if guard.Expected != "" {
		return errors.New("DATABASE_URL is empty but MULTICA_EXPECTED_DATABASE_URL is set")
	}
	return errors.New("DATABASE_URL is empty; run through make up or make env-exec")
}

// currentDirectory returns the process working directory, falling back to "." so
// worktree detection still has a starting point when getwd fails.
func currentDirectory() string {
	cwd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return cwd
}
