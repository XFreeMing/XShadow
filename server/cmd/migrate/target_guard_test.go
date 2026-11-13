package main

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestParseDatabaseTarget(t *testing.T) {
	cases := []struct {
		name     string
		raw      string
		wantDB   string
		wantHost string
		wantPort int
		wantErr  bool
	}{
		{
			name:     "URL path database",
			raw:      "postgres://user:pass@db.example.com:6543/appdb?sslmode=disable",
			wantDB:   "appdb",
			wantHost: "db.example.com",
			wantPort: 6543,
		},
		{
			name:     "dbname query overrides URL path",
			raw:      "postgres://user:pass@db.example.com:6543/appdb?dbname=override&sslmode=disable",
			wantDB:   "override",
			wantHost: "db.example.com",
			wantPort: 6543,
		},
		{
			name:     "keyword/value DSN",
			raw:      "host=db.example.com port=6543 user=user password=pass dbname=appdb sslmode=disable",
			wantDB:   "appdb",
			wantHost: "db.example.com",
			wantPort: 6543,
		},
		{
			name:     "default port",
			raw:      "postgres://user:pass@db.example.com/appdb",
			wantDB:   "appdb",
			wantHost: "db.example.com",
			wantPort: 5432,
		},
		{
			name:    "malformed input",
			raw:     "postgres://user:pass@db.example.com:not-a-port/appdb",
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseDatabaseTarget(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected parse error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parse %q: %v", tc.raw, err)
			}
			if got.Database != tc.wantDB || got.Host != tc.wantHost || got.Port != tc.wantPort {
				t.Fatalf("parse %q = %+v, want database=%s host=%s port=%d", tc.raw, got, tc.wantDB, tc.wantHost, tc.wantPort)
			}
		})
	}
}

func TestNormalizeDatabaseHost(t *testing.T) {
	for _, host := range []string{"localhost", "127.0.0.1", "::1", "[::1]"} {
		if got := normalizeDatabaseHost(host); got != "127.0.0.1" {
			t.Fatalf("normalizeDatabaseHost(%q) = %q, want 127.0.0.1", host, got)
		}
	}
	for _, host := range []string{"db.example.com", "10.0.0.5", "", "LOCALHOST"} {
		if got := normalizeDatabaseHost(host); got != host {
			t.Fatalf("normalizeDatabaseHost(%q) = %q, want unchanged", host, got)
		}
	}
}

func TestDatabaseTargetSameServer(t *testing.T) {
	cases := []struct {
		name  string
		left  databaseTarget
		right databaseTarget
		want  bool
	}{
		{
			name:  "same database with different loopback spellings",
			left:  databaseTarget{Database: "appdb", Host: "localhost", Port: 5432},
			right: databaseTarget{Database: "appdb", Host: "127.0.0.1", Port: 5432},
			want:  true,
		},
		{
			name:  "different database",
			left:  databaseTarget{Database: "appdb", Host: "127.0.0.1", Port: 5432},
			right: databaseTarget{Database: "other", Host: "127.0.0.1", Port: 5432},
			want:  false,
		},
		{
			name:  "different port while both hosts are loopback",
			left:  databaseTarget{Database: "appdb", Host: "127.0.0.1", Port: 5432},
			right: databaseTarget{Database: "appdb", Host: "127.0.0.1", Port: 5433},
			want:  false,
		},
		{
			// A Docker-published port makes the server report its container
			// address and internal port, so the endpoints are not comparable.
			name:  "published port versus container address with same database",
			left:  databaseTarget{Database: "appdb", Host: "localhost", Port: 15432},
			right: databaseTarget{Database: "appdb", Host: "172.22.0.2", Port: 5432},
			want:  true,
		},
		{
			// Remote hosts are not comparable across NAT; the database name is
			// the contract.
			name:  "different remote hosts with same database",
			left:  databaseTarget{Database: "appdb", Host: "db.example.com", Port: 5432},
			right: databaseTarget{Database: "appdb", Host: "other.example.com", Port: 5432},
			want:  true,
		},
		{
			name:  "empty actual host skips host comparison",
			left:  databaseTarget{Database: "appdb", Host: "", Port: 5432},
			right: databaseTarget{Database: "appdb", Host: "db.example.com", Port: 5432},
			want:  true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.left.sameServer(tc.right); got != tc.want {
				t.Fatalf("%+v.sameServer(%+v) = %v, want %v", tc.left, tc.right, got, tc.want)
			}
		})
	}
}

func TestDatabaseGuardActive(t *testing.T) {
	if (databaseGuard{}).active() {
		t.Fatal("empty guard must be inactive")
	}
	for name, guard := range map[string]databaseGuard{
		"expected target": {Expected: "postgres://user:pass@db.example.com:5432/appdb"},
		"selected env":    {SelectedEnv: "dev-xshadow"},
		"linked worktree": {LinkedWorktree: true},
		"protected list":  {Protected: map[string]bool{"stable": true}},
	} {
		if !guard.active() {
			t.Fatalf("%s guard must be active", name)
		}
	}
}

func TestEvaluateDatabaseTarget(t *testing.T) {
	const password = "sup3rs3cret"
	actual := databaseTarget{Database: "appdb", Host: "127.0.0.1", Port: 5432}

	cases := []struct {
		name          string
		guard         databaseGuard
		actual        databaseTarget
		wantErr       bool
		wantContains  []string
		forbidSubstrs []string
	}{
		{
			name:          "protected database",
			guard:         databaseGuard{Protected: map[string]bool{"stable": true}},
			actual:        databaseTarget{Database: "stable", Host: "127.0.0.1", Port: 5432},
			wantErr:       true,
			wantContains:  []string{"protected database", "database=stable"},
			forbidSubstrs: []string{password},
		},
		{
			name:          "expected database mismatch",
			guard:         databaseGuard{Expected: "postgres://user:" + password + "@127.0.0.1:5432/other"},
			actual:        actual,
			wantErr:       true,
			wantContains:  []string{"database=appdb", "database=other"},
			forbidSubstrs: []string{password},
		},
		{
			name:          "missing expected while an environment is selected",
			guard:         databaseGuard{SelectedEnv: "dev-xshadow"},
			actual:        actual,
			wantErr:       true,
			wantContains:  []string{"dev-xshadow", "has no verified target"},
			forbidSubstrs: []string{password},
		},
		{
			name:          "missing expected in a linked worktree",
			guard:         databaseGuard{LinkedWorktree: true},
			actual:        actual,
			wantErr:       true,
			wantContains:  []string{"make up", "make env-exec"},
			forbidSubstrs: []string{password},
		},
		{
			name:          "invalid expected URL",
			guard:         databaseGuard{Expected: "postgres://user:" + password + "@127.0.0.1:not-a-port/appdb"},
			actual:        actual,
			wantErr:       true,
			wantContains:  []string{"invalid MULTICA_EXPECTED_DATABASE_URL"},
			forbidSubstrs: []string{password},
		},
		{
			name:   "legacy run without a declared target",
			guard:  databaseGuard{},
			actual: actual,
		},
		{
			name:   "matching verified target",
			guard:  databaseGuard{Expected: "postgres://user:" + password + "@localhost:5432/appdb"},
			actual: actual,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := evaluateDatabaseTarget(tc.actual, tc.guard)
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("expected acceptance, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected refusal, got nil")
			}
			msg := err.Error()
			for _, want := range tc.wantContains {
				if !strings.Contains(msg, want) {
					t.Fatalf("refusal %q does not contain %q", msg, want)
				}
			}
			for _, forbid := range tc.forbidSubstrs {
				if strings.Contains(msg, forbid) {
					t.Fatalf("refusal %q leaks %q", msg, forbid)
				}
			}
		})
	}
}

func TestDatabaseGuardFromEnvLinkedWorktree(t *testing.T) {
	env := map[string]string{
		expectedDatabaseURLEnv: "postgres://user:pass@localhost:5432/appdb",
		protectedDatabasesEnv:  "multica, stable ,,xshadow",
		databaseGuardEnvEnv:    "dev-xshadow",
	}
	getenv := func(key string) string { return env[key] }

	guard := databaseGuardFromEnv(getenv, t.TempDir())
	if guard.Expected != env[expectedDatabaseURLEnv] {
		t.Fatalf("expected = %q, want %q", guard.Expected, env[expectedDatabaseURLEnv])
	}
	if guard.SelectedEnv != "dev-xshadow" {
		t.Fatalf("selected env = %q", guard.SelectedEnv)
	}
	for _, name := range []string{"multica", "stable", "xshadow"} {
		if !guard.Protected[name] {
			t.Fatalf("protected set is missing %q: %v", name, guard.Protected)
		}
	}
	if len(guard.Protected) != 3 {
		t.Fatalf("protected set = %v, want 3 entries", guard.Protected)
	}

	t.Run("no git directory", func(t *testing.T) {
		if databaseGuardFromEnv(getenv, t.TempDir()).LinkedWorktree {
			t.Fatal("directory without .git reported as a linked worktree")
		}
	})

	t.Run("git directory", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		if databaseGuardFromEnv(getenv, dir).LinkedWorktree {
			t.Fatal("main checkout with a .git directory reported as a linked worktree")
		}
	})

	t.Run("git file", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: /elsewhere\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if !databaseGuardFromEnv(getenv, dir).LinkedWorktree {
			t.Fatal(".git file reported as a main checkout")
		}
	})

	t.Run("walk up to nested .git file", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: /elsewhere\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		nested := filepath.Join(dir, "server", "cmd", "migrate")
		if err := os.MkdirAll(nested, 0o755); err != nil {
			t.Fatal(err)
		}
		if !databaseGuardFromEnv(getenv, nested).LinkedWorktree {
			t.Fatal("nested cwd did not walk up to the linked-worktree .git file")
		}
	})
}

// TestDatabaseTargetReadIdentityLive is the only test in this file that touches
// a database. It only reads the live identity, and it skips when no disposable
// DATABASE_URL is exported.
func TestDatabaseTargetReadIdentityLive(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("explicit disposable DATABASE_URL required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	defer pool.Close()

	actual, err := readDatabaseIdentity(ctx, pool)
	if err != nil {
		t.Skipf("live database identity unavailable: %v", err)
	}

	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse DATABASE_URL: %v", err)
	}
	if actual.Database != config.Database {
		t.Fatalf("identity database = %q, want %q", actual.Database, config.Database)
	}

	if err := evaluateDatabaseTarget(actual, databaseGuard{Expected: dsn}); err != nil {
		t.Fatalf("expected DATABASE_URL to match the live identity: %v", err)
	}

	otherDSN := testDatabaseDSNWithName(t, dsn, config.Database+"_other")
	if err := evaluateDatabaseTarget(actual, databaseGuard{Expected: otherDSN}); err == nil {
		t.Fatal("expected a database-name mismatch to be refused")
	}
}

// testDatabaseDSNWithName rewrites the database name in either URL or
// keyword/value form, mirroring how the isolated-DB fixture builds its child DSN.
func testDatabaseDSNWithName(t *testing.T, dsn, name string) string {
	t.Helper()
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		u.Path, u.RawPath = "/"+name, ""
		q := u.Query()
		q.Del("database")
		q.Del("dbname")
		u.RawQuery = q.Encode()
		return u.String()
	}
	return dsn + " dbname=" + name
}
