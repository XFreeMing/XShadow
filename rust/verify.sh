#!/usr/bin/env bash
set -euo pipefail
umask 077

cd "$(dirname "${BASH_SOURCE[0]}")/.."
source_root="$PWD"
mkdir -p "$1"
evidence="$(mktemp -d "$1/run-XXXXXX")"
docker_cli="${XSHADOW_RUST_DOCKER:-docker}"
database_container=
candidate_pid=

cleanup() {
  if [[ -n "$candidate_pid" ]]; then
    kill -TERM "$candidate_pid" 2>/dev/null || true
    wait "$candidate_pid" || true
  fi
  if [[ -n "$database_container" ]]; then
    "$docker_cli" stop --time 2 "$database_container" >/dev/null
  fi
  for binary in go-server go-migrate; do
    if [[ -f "$evidence/$binary" ]]; then
      unlink "$evidence/$binary"
    fi
  done
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

export XSHADOW_BUILD_COMMIT="$(git rev-parse --short HEAD)"
git rev-parse HEAD > "$evidence/revision"
git status --porcelain > "$evidence/working-tree-status"
rust-anything --version > "$evidence/toolchain"
rustc --version >> "$evidence/toolchain"

manifest=rust/app/Cargo.toml
cargo fmt --manifest-path "$manifest" --check
cargo clippy --manifest-path "$manifest" --locked --all-targets -- -D warnings
cargo test --manifest-path "$manifest" --locked
CARGO_TARGET_DIR="$source_root/rust/app/target" cargo build --manifest-path "$manifest" --locked
candidate="$source_root/rust/app/target/debug/xshadow-server"

(
  cd server
  go test ./cmd/server -run '^TestServerHealth' -count=1
  go build -ldflags "-X main.commit=$XSHADOW_BUILD_COMMIT" -o "$evidence/go-server" ./cmd/server
  go build -o "$evidence/go-migrate" ./cmd/migrate
)

password="$(python3 -c 'import secrets; print(secrets.token_hex(24))')"
database_container="$("$docker_cli" run --rm --detach --publish 127.0.0.1::5432 \
  --env POSTGRES_USER=xshadow_rust --env POSTGRES_DB=xshadow_rust \
  --env "POSTGRES_PASSWORD=$password" pgvector/pgvector:pg17)"
db_address="$("$docker_cli" port "$database_container" 5432/tcp)"
[[ "$db_address" =~ ^127\.0\.0\.1:[0-9]+$ ]]
export DATABASE_URL="postgresql://xshadow_rust:$password@$db_address/xshadow_rust?sslmode=disable"
unset password

query() {
  psql "$DATABASE_URL" -XAtq -v ON_ERROR_STOP=1 -c "$1"
}
for ((attempt=0; attempt<40; attempt++)); do
  if query 'SELECT 1' >/dev/null 2>&1; then break; fi
  sleep 1
done
query 'SELECT 1' >/dev/null
(cd server && "$evidence/go-migrate" up) > "$evidence/migrate.log" 2>&1

export XSHADOW_GO_REFERENCE="$evidence/go-server"
export XSHADOW_MIGRATION_SANDBOX="$evidence/go-sandbox"
mkdir -p "$XSHADOW_MIGRATION_SANDBOX"
ln -s "$source_root/server/migrations" "$XSHADOW_MIGRATION_SANDBOX/migrations"

replay() {
  local case_root="$1" label="$2"
  shift 2
  rust-anything app replay "$case_root" --json "$@" > "$case_root/$label.json" || {
    cat "$case_root/$label.json"
    return 1
  }
  jq -er --arg label "$label" '"\($label) \(.passed)/\(.total)"' "$case_root/$label.json"
}

mkdir -p "$evidence/ready/rust"
cp -R rust/app-cases "$evidence/ready/rust/"
replay "$evidence/ready" go
replay "$evidence/ready" rust --build "$candidate"

# Leave later versions present to distinguish checking every required migration
# from checking only the highest version in the table.
query 'DELETE FROM schema_migrations WHERE version = (SELECT MIN(version) FROM schema_migrations)'
printf '%s\n' '{"method":"GET","path":"/readyz"}' > "$evidence/readiness.jsonl"

verify_readiness_error() {
  local state="$1" case_root="$evidence/$1"
  mkdir -p "$case_root"
  rust-anything app record "$case_root" --name xshadow-server --http --no-traps \
    --inputs "$evidence/readiness.jsonl" --timeout 20 -- \
    bash -c 'cd "$XSHADOW_MIGRATION_SANDBOX" && exec env -i PATH="$PATH" PORT="$PORT" DATABASE_URL="$DATABASE_URL" "$XSHADOW_GO_REFERENCE"'
  jq -e --arg state "$state" \
    '.status == "not_ready" and .checks.db == "ok" and .checks.migrations == $state' \
    "$case_root/rust/app-cases/http/input-001-get-readyz.body" >/dev/null
  replay "$case_root" "rust-$state" --build "$candidate"
}
verify_readiness_error out_of_date
query 'ALTER TABLE schema_migrations RENAME TO schema_migrations_fixture'
verify_readiness_error error

PORT=0 "$candidate" > "$evidence/candidate.log" 2>&1 &
candidate_pid=$!
address=
for ((attempt=0; attempt<50; attempt++)); do
  address="$(sed -n 's/^XShadow Rust API candidate listening on //p' "$evidence/candidate.log")"
  if [[ -n "$address" ]]; then break; fi
  kill -0 "$candidate_pid"
  sleep 0.1
done
[[ "$address" =~ ^127\.0\.0\.1:[0-9]+$ ]]
curl --noproxy '*' --max-time 5 --fail --silent --show-error \
  "http://$address/health" > "$evidence/liveness.json"
jq -e --argjson pid "$candidate_pid" --arg commit "$XSHADOW_BUILD_COMMIT" \
  '.status == "ok" and .pid == $pid and .commit == $commit and (.started_at | fromdateiso8601 > 0)' \
  "$evidence/liveness.json" >/dev/null

"$docker_cli" stop --time 2 "$database_container" >/dev/null
database_container=
status="$(curl --noproxy '*' --max-time 5 --silent --show-error --write-out '%{http_code}' \
  --output "$evidence/database-unavailable.json" "http://$address/readyz")"
[[ "$status" = 503 ]]
jq -e '.status == "not_ready" and .checks == {db: "error", migrations: "unknown"}' \
  "$evidence/database-unavailable.json" >/dev/null

printf 'Rust 迁移验证回执：%s\n' "$evidence"
