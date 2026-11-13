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

// databaseTarget is the subset of a database identity that can safely appear in
// logs. User and password are deliberately absent so a refusal never leaks
// credentials.
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

// normalizeDatabaseHost collapses loopback spellings so a caller that wrote
// "localhost" matches a record that wrote "127.0.0.1". Anything that is not a
// known loopback spelling is returned untouched.
func normalizeDatabaseHost(host string) string {
	switch host {
	case "localhost", "127.0.0.1", "::1", "[::1]":
		return "127.0.0.1"
	default:
		return host
	}
}

// sameEndpoint reports whether two connection targets name the same database on
// the same server. Both sides are parsed from connection strings written by the
// caller, so host and port are directly comparable. A server-reported
// inet_server_addr is not: behind Docker's published port it is the container's
// internal address and never equals the host port in the DSN, which is why the
// live identity check compares only the database name.
func (t databaseTarget) sameEndpoint(other databaseTarget) bool {
	if t.Database != other.Database {
		return false
	}
	if !strings.EqualFold(normalizeDatabaseHost(t.Host), normalizeDatabaseHost(other.Host)) {
		return false
	}
	return t.Port == other.Port
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

// evaluateConnectionTarget checks a parsed connection string before the process
// opens a connection. Both the recorded target and the connection come from the
// caller's configuration, so their endpoints must agree exactly; nothing is
// learned from a live server here. Refusing here costs no network round trip
// and cannot touch any database.
func evaluateConnectionTarget(connection databaseTarget, guard databaseGuard) error {
	if guard.Protected[connection.Database] {
		return fmt.Errorf("refusing to migrate protected database: %s", connection.redacted())
	}
	if guard.Expected == "" {
		if guard.SelectedEnv != "" {
			return fmt.Errorf("refusing to migrate: environment %q has no verified target (%s)", guard.SelectedEnv, connection.redacted())
		}
		if guard.LinkedWorktree {
			return fmt.Errorf("refusing to migrate from a task worktree without a verified target; run through make up or make env-exec (%s)", connection.redacted())
		}
		// Legacy main checkout, CI, and containers have no declaration to check
		// against. Keep them working unchanged.
		return nil
	}

	expected, err := parseDatabaseTarget(guard.Expected)
	if err != nil {
		// The raw URL is never echoed: it can contain a password.
		return fmt.Errorf("invalid MULTICA_EXPECTED_DATABASE_URL: the verified target cannot be parsed (%s)", connection.redacted())
	}
	if !connection.sameEndpoint(expected) {
		return fmt.Errorf("refusing to migrate: the connection targets %s but the verified target is %s", connection.redacted(), expected.redacted())
	}
	return nil
}

// evaluateLiveIdentity checks what the server reports after connecting. The
// endpoint was already compared against the record from the connection strings;
// here only the database the server actually reached can be trusted, because a
// published container port reports an internal server address.
func evaluateLiveIdentity(live databaseTarget, guard databaseGuard) error {
	if guard.Protected[live.Database] {
		return fmt.Errorf("refusing to migrate protected database: %s", live.redacted())
	}
	if guard.Expected == "" {
		return nil
	}
	expected, err := parseDatabaseTarget(guard.Expected)
	if err != nil {
		return fmt.Errorf("invalid MULTICA_EXPECTED_DATABASE_URL: the verified target cannot be parsed (%s)", live.redacted())
	}
	if live.Database != expected.Database {
		return fmt.Errorf("refusing to migrate: connected to %s but the verified target is %s", live.redacted(), expected.redacted())
	}
	return nil
}

// verifyConnectionTarget parses DATABASE_URL and applies the pre-connect guard.
// It runs before the pool is created so a polluted connection never reaches a
// server.
func verifyConnectionTarget(rawDatabaseURL string, guard databaseGuard) error {
	connection, err := parseDatabaseTarget(rawDatabaseURL)
	if err != nil {
		return errors.New("invalid DATABASE_URL: the connection target cannot be parsed")
	}
	return evaluateConnectionTarget(connection, guard)
}

// verifyDatabaseTarget reads the live identity through the pool and applies the
// guard. It is called before migrations run, so a refusal always precedes any
// DDL.
func verifyDatabaseTarget(ctx context.Context, pool *pgxpool.Pool, guard databaseGuard) (databaseTarget, error) {
	live, err := readDatabaseIdentity(ctx, pool)
	if err != nil {
		return databaseTarget{}, err
	}
	if err := evaluateLiveIdentity(live, guard); err != nil {
		return live, err
	}
	return live, nil
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
