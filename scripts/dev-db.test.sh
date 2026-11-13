#!/usr/bin/env bash
set -euo pipefail

# The verified database target must come from the selected environment record,
# never from the connection it is supposed to verify. A Makefile export derived
# from DATABASE_URL approved whatever database the env file or a parent shell
# named — including a linked worktree with no selected environment, where the
# guard exists precisely to refuse (DEV-54 review finding 1).
#
# Nothing here needs a database: a Makefile evaluation and the script's own
# configuration checks refuse before any connection or compilation.

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

fail() {
  echo "$1" >&2
  exit 1
}

require_contains() {
  grep -Fq -- "$2" <<<"$1" || fail "$3 (output: $1)"
}

tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT

env_file="$tmp_dir/env"
cat >"$env_file" <<'EOF'
DATABASE_URL=postgres://user:secret@127.0.0.1:5432/dev54_make_protected
EOF

recorded="postgres://user:secret@127.0.0.1:5432/dev54_make_dev"

# A target that prints what a recipe would receive. `make` runs it with the
# environment the parent exported plus whatever the Makefile adds.
print_guard=(
  make -s -f Makefile ENV_FILE="$env_file"
  --eval='print-guard: ; @printf "%s|%s\n" "$$MULTICA_EXPECTED_DATABASE_URL" "$$DATABASE_URL"'
  print-guard
)

# 1. The recorded target survives; the env file's DATABASE_URL does not replace it.
output="$(
  env -u DATABASE_URL -u MULTICA_DB_GUARD_ENV \
    MULTICA_EXPECTED_DATABASE_URL="$recorded" \
    "${print_guard[@]}"
)" || fail "make print-guard failed"
[ "$output" = "$recorded|postgres://user:secret@127.0.0.1:5432/dev54_make_protected" ] ||
  fail "the Makefile rewrote the recorded target (got: $output)"

# 2. Without a record the Makefile must not invent one from DATABASE_URL.
output="$(
  env -u DATABASE_URL -u MULTICA_DB_GUARD_ENV -u MULTICA_EXPECTED_DATABASE_URL \
    "${print_guard[@]}"
)" || fail "make print-guard failed without a recorded target"
[ "$output" = "|postgres://user:secret@127.0.0.1:5432/dev54_make_protected" ] ||
  fail "the Makefile synthesized a verified target from DATABASE_URL (got: $output)"

# 3. dev-db.sh is only usable through the environment entry that exports the
#    record. Run outside it, the script refuses before touching a database.
status=0
output="$(env -u MULTICA_EXPECTED_DATABASE_URL -u MULTICA_DB_GUARD_ENV -u DATABASE_URL \
  bash scripts/dev-db.sh verify 2>&1)" || status=$?
[ "$status" -ne 0 ] || fail "dev-db.sh ran without a verified target"
require_contains "$output" "No verified database target" \
  "dev-db.sh did not explain the missing entry"

# 4. A selected environment without its recorded target is a configuration
#    error, not permission to use whatever DATABASE_URL is exported.
status=0
output="$(env -u MULTICA_EXPECTED_DATABASE_URL \
  MULTICA_DB_GUARD_ENV=dev54-entry \
  DATABASE_URL="$recorded" \
  bash scripts/dev-db.sh verify 2>&1)" || status=$?
[ "$status" -ne 0 ] || fail "dev-db.sh ran with an environment but no recorded target"
require_contains "$output" "No verified database target" \
  "dev-db.sh did not refuse an environment without a recorded target"

# 5. A recorded target with no connection refuses before the binary starts.
status=0
output="$(env -u DATABASE_URL \
  MULTICA_EXPECTED_DATABASE_URL="$recorded" \
  MULTICA_DB_GUARD_ENV=dev54-entry \
  bash scripts/dev-db.sh verify 2>&1)" || status=$?
[ "$status" -ne 0 ] || fail "dev-db.sh ran with an empty DATABASE_URL"
require_contains "$output" "DATABASE_URL is empty" \
  "dev-db.sh did not refuse an unresolved connection"

echo "dev-db entry checks passed"