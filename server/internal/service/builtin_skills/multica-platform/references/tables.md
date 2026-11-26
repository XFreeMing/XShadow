# Tables / Multi-Dimensional Sheets (APITable-style)

Use the injected XShadow CLI (`"$MULTICA_CLI_PATH" table ...`) for both **task tables** (read-only structured views and computed projections over issues) and **generic multi-dimensional tables** (APITable-style workspace sheets with typed fields, views, forms, comments, optimistic collaboration, and presence).

Always invoke the injected CLI via `"$MULTICA_CLI_PATH"` (PowerShell on Windows: `& "$env:MULTICA_CLI_PATH"`), not an unqualified `multica` from `PATH`. Run `"$MULTICA_CLI_PATH" table --help` (and `--help` on any subcommand) to inspect the exact command tree and flags. All `table` commands authenticate with the normal CLI / task token via `newAPIClient` and emit JSON on stdout. Never bypass the CLI with `curl`, `wget`, or alternate credentials.

## Boundaries

- **Task writes stay on `issue`.** `"$MULTICA_CLI_PATH" table tasks ...` is read-only query/projection over workspace issues. Create, update, assign, comment, or transition issues with `"$MULTICA_CLI_PATH" issue ...`, and set custom properties (including `record_relation`) with `"$MULTICA_CLI_PATH" issue property set`.
- **Generic tables are workspace-scoped.** Table, field, record, view, and comment IDs are server-issued UUIDs. Invalid UUIDs fail locally before sending HTTP requests.
- **JSON payloads via `--input`.** Pass `--input <path>` (a file inside the current working directory; symlinks escaping workdir and files over 1 MiB are rejected) or `--input -` for stdin. Every payload must be a single JSON object.
- **Structural deletes require workspace owner/admin authority.** Deleting a table, field, or view requires workspace owner/admin privileges — and for task-token runs, the run's human originator must also be a current workspace owner/admin. Deleting a record uses normal granted record access plus `expected_revision`.
- **Protected references return `409 Conflict`.** Deleting a table, field, or record that is still referenced (by issue `record_relation`/`lookup` properties, cross-table relations, lookups, formulas, view definitions, or other records) fails with `409` instead of silently leaving dangling references.

## Task Tables (`"$MULTICA_CLI_PATH" table tasks`)

Query workspace issues in table form without mutating them:

- `"$MULTICA_CLI_PATH" table tasks rows --input <file|->` — `POST /api/issues/table/rows`. Returns `query_fingerprint`, `group_key`, `parent_id`, `total`, `branch_total`, `next_cursor`, and `rows`. For ungrouped queries, `"group": {"kind": "none"}` is required.
- `"$MULTICA_CLI_PATH" table tasks groups --input <file|->` — `POST /api/issues/table/groups`. Returns grouped counts and `next_cursor`.
- `"$MULTICA_CLI_PATH" table tasks facets --input <file|->` — `POST /api/issues/table/facets`. Returns facet counts for the query.
- `"$MULTICA_CLI_PATH" table tasks computed --input <file|->` — `POST /api/issues/properties/project`. Evaluates lookup/formula computed properties for up to 200 issues: `{"issue_ids": ["<uuid>", ...]}`.

A `409` (`cursor_query_mismatch`) means the paging cursor does not match the query; do not retry blindly — restart pagination from the current query.

### Linking issues to generic table records

- `"$MULTICA_CLI_PATH" property list --output json` includes `record_relation`, `lookup`, and `formula` definitions with their full `config` (`target_table_id`, `relation_property_id`, `target_field_id`, `expression`).
- `"$MULTICA_CLI_PATH" issue property set <issue-id> --name "<prop>" --value "<uuid1>,<uuid2>"` (or a JSON array `'["<uuid1>"]'`) sets a `record_relation` property.
- `lookup` and `formula` issue properties are server-computed and read-only; attempting to set them fails fast locally.

## Generic Tables (`"$MULTICA_CLI_PATH" table`)

### Tables

- `"$MULTICA_CLI_PATH" table list` — `GET /api/tables`
- `"$MULTICA_CLI_PATH" table get <table-id> [--view <view-id>]` — `GET /api/tables/<table-id>` (returns `table`, `fields`, `views`, `records`, `active_view_id`)
- `"$MULTICA_CLI_PATH" table create --input <file|->` — `POST /api/tables` (`{"name": "..."}`)
- `"$MULTICA_CLI_PATH" table rename <table-id> --input <file|->` — `PATCH /api/tables/<table-id>` (`{"name": "..."}`)
- `"$MULTICA_CLI_PATH" table delete <table-id>` — `DELETE /api/tables/<table-id>` (owner/admin; blocked with `409` if referenced by other tables or issue properties)

### Fields (`"$MULTICA_CLI_PATH" table field`)

Supported kinds: `text`, `number`, `date`, `single_select`, `checkbox`, `relation`, `lookup`, `formula`.

- `"$MULTICA_CLI_PATH" table field list <table-id>` — `GET /api/tables/<table-id>/fields`
- `"$MULTICA_CLI_PATH" table field get <table-id> <field-id>` — `GET /api/tables/<table-id>/fields/<field-id>`
- `"$MULTICA_CLI_PATH" table field create <table-id> --input <file|->` — `POST /api/tables/<table-id>/fields` (`{"name": "...", "kind": "...", "config": {...}}`)
- `"$MULTICA_CLI_PATH" table field update <table-id> <field-id> --input <file|->` — `PATCH /api/tables/<table-id>/fields/<field-id>` (`{"name": "...", "config": {...}}`)
- `"$MULTICA_CLI_PATH" table field delete <table-id> <field-id>` — `DELETE /api/tables/<table-id>/fields/<field-id>` (owner/admin; blocked with `409` if referenced by views, formulas, lookups, bidirectional relations, or issue lookups)

### Records (`"$MULTICA_CLI_PATH" table record`)

- `"$MULTICA_CLI_PATH" table record list <table-id> [--view <view-id>] [--limit N] [--offset N]` — `GET /api/tables/<table-id>/records` (applies view filter/sort when `--view` is provided and returns `records`, `total`, `limit`, `offset`, `has_more`, `next_offset`)
- `"$MULTICA_CLI_PATH" table record get <table-id> <record-id>` — `GET /api/tables/<table-id>/records/<record-id>`
- `"$MULTICA_CLI_PATH" table record options <table-id>` — `GET /api/tables/<table-id>/record-options` (id + display label pairs for relation pickers)
- `"$MULTICA_CLI_PATH" table record create <table-id> --input <file|->` — `POST /api/tables/<table-id>/records` (`{"values": {"<field-id>": ...}}`)
- `"$MULTICA_CLI_PATH" table record update <table-id> <record-id> --input <file|->` — `PATCH /api/tables/<table-id>/records/<record-id>` (`{"expected_revision": 1, "values": {"<field-id>": ...}}`)
- `"$MULTICA_CLI_PATH" table record delete <table-id> <record-id> (--expected-revision N | --input <file|->)` — `DELETE /api/tables/<table-id>/records/<record-id>` (requires positive integer `expected_revision`; returns `409` on stale revision or active references)
- `"$MULTICA_CLI_PATH" table record batch <table-id> --input <file|->` — `POST /api/tables/<table-id>/records/batch` (`{"idempotency_key": "<uuid>", "operation": "create|update|delete", "records": [...]}` with `1..10` items). Runs atomically in one transaction: any invalid, missing, referenced, or conflicting row rolls back the entire batch and returns the error with zero-based `failed_index` (plus `record_id` and current `record` on `revision_conflict`). Retrying with the same actor, workspace, table, `idempotency_key`, and payload replays the exact committed result without writing duplicate events; reusing `idempotency_key` with a different payload returns `409` (`idempotency_conflict`). Duplicate record IDs in `update`/`delete` are rejected before mutation. Results are returned in submitted order with a shared `batch_id` that also groups the committed events in `changes`.

#### Optimistic concurrency & collaboration

- Every record carries a monotonically increasing positive integer `revision`.
- Concurrent updates on **disjoint fields** from the same base revision merge cleanly and advance `revision`.
- Concurrent updates touching the **same field**, or updates/deletes with a stale `expected_revision`, fail with `409` (`revision_conflict`) and include the current server `record`. Re-read or inspect the returned record before deciding whether to retry. Never blindly overwrite.

### Record Comments (`"$MULTICA_CLI_PATH" table record comment`)

- `"$MULTICA_CLI_PATH" table record comment list <table-id> <record-id> [--after N] [--limit N]` — `GET /api/tables/<table-id>/records/<record-id>/comments`
- `"$MULTICA_CLI_PATH" table record comment create <table-id> <record-id> --input <file|->` — `POST /api/tables/<table-id>/records/<record-id>/comments` (`{"body": "...", "parent_id": "<uuid>"}`)

### Views & Forms (`"$MULTICA_CLI_PATH" table view`)

Supported view kinds: `grid`, `kanban`, `gallery`, `form`.

- `"$MULTICA_CLI_PATH" table view list <table-id>` — `GET /api/tables/<table-id>/views`
- `"$MULTICA_CLI_PATH" table view get <table-id> <view-id>` — `GET /api/tables/<table-id>/views/<view-id>`
- `"$MULTICA_CLI_PATH" table view create <table-id> --input <file|->` — `POST /api/tables/<table-id>/views`
- `"$MULTICA_CLI_PATH" table view update <table-id> <view-id> --input <file|->` — `PATCH /api/tables/<table-id>/views/<view-id>`
- `"$MULTICA_CLI_PATH" table view delete <table-id> <view-id>` — `DELETE /api/tables/<table-id>/views/<view-id>` (owner/admin; cannot delete the table's last remaining grid view)
- `"$MULTICA_CLI_PATH" table view submit <table-id> <view-id> --input <file|->` — `POST /api/tables/<table-id>/views/<view-id>/submit` (`{"values": {...}}`)

### Changes & Presence

- `"$MULTICA_CLI_PATH" table changes <table-id> [--since N]` — `GET /api/tables/<table-id>/changes` (returns `latest`, `next_since`, ordered typed `changes` with `event_type`, `topic`, `entity_id`, `actor`, and active `presence`)
- `"$MULTICA_CLI_PATH" table presence <table-id> [--record-id UUID] [--field-id UUID] [--input <file|->]` — `POST /api/tables/<table-id>/presence` (refreshes this actor's presence lease and returns the current snapshot)
