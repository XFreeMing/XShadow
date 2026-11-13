#!/usr/bin/env bash
# Migrations and database tests through the one connection this checkout's
# environment record names.
#
#   make env-exec ARGS="-- bash scripts/dev-db.sh verify"
#   make env-exec ARGS="-- bash scripts/dev-db.sh migrate"
#   make env-exec ARGS="-- bash scripts/dev-db.sh test"
#   make env-exec ARGS="-- bash scripts/dev-db.sh migrate-test"   # default
#
# scripts/dev-env.sh exec resolves the environment record and exports
# MULTICA_EXPECTED_DATABASE_URL (the recorded target) plus
# MULTICA_PROTECTED_DATABASES (other checkouts' databases). cmd/migrate reads
# the live identity through DATABASE_URL and refuses before any DDL when it is
# not the recorded database. Running this script outside that entry has no
# verified target and is refused here.
#
# A refusal stops the run. Nothing in this script stops, resets or rolls back
# the environment, so a wrong target leaves the database untouched.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

die() { printf '\n✗ %s\n' "$1" >&2; exit 1; }

mode="${1:-migrate-test}"
[ $# -eq 0 ] || shift

case "$mode" in
  verify|migrate|test|migrate-test) ;;
  *) die "Unknown mode '$mode'. Use verify, migrate, test or migrate-test." ;;
esac

[ -n "${MULTICA_EXPECTED_DATABASE_URL:-}" ] \
  || die "No verified database target. Run this through the environment entry: make env-exec ARGS=\"-- bash scripts/dev-db.sh $mode\""
[ -n "${MULTICA_DB_GUARD_ENV:-}" ] \
  || die "No environment selected. Run this through the environment entry: make env-exec ARGS=\"-- bash scripts/dev-db.sh $mode\""
[ -n "${DATABASE_URL:-}" ] \
  || die "DATABASE_URL is empty; the environment record could not resolve a connection."

cd "$REPO_ROOT/server"

# Tests write fixtures, so their target is verified first even when the schema
# is already migrated. `migrate` verifies inside the same process before DDL.
case "$mode" in
  verify)
    exec go run ./cmd/migrate verify
    ;;
  migrate)
    exec go run ./cmd/migrate up
    ;;
  test)
    go run ./cmd/migrate verify
    exec go test ./internal/testutil -count=1 "$@"
    ;;
  migrate-test)
    go run ./cmd/migrate up
    exec go test ./internal/testutil -count=1 "$@"
    ;;
esac