-- Generic-table time machine and point-in-time snapshot history.
-- Stores one revision per (workspace_id, table_id, transaction_id) and typed
-- entity versions only for changed rows (table, field, view, record, cell),
-- with NULL payload tombstones for deletions. No foreign keys; whole-table
-- deletes clean up scoped versions and revisions atomically in the trigger.

CREATE TABLE IF NOT EXISTS generic_history_revision (
    revision BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id UUID NOT NULL,
    table_id UUID NOT NULL,
    transaction_id BIGINT NOT NULL,
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    baseline BOOLEAN NOT NULL DEFAULT FALSE,
    UNIQUE (workspace_id, table_id, transaction_id)
);

CREATE TABLE IF NOT EXISTS generic_history_version (
    revision BIGINT NOT NULL,
    kind TEXT NOT NULL,
    entity_id UUID NOT NULL,
    secondary_id UUID NOT NULL DEFAULT '00000000-0000-0000-0000-000000000000'::uuid,
    payload JSONB,
    PRIMARY KEY (revision, kind, entity_id, secondary_id),
    CONSTRAINT generic_history_version_kind_check CHECK (
        kind IN ('table', 'field', 'view', 'record', 'cell')
    )
);

-- Backfill one explicit baseline revision per preexisting table before
-- installing mutation triggers.
WITH seeded AS (
    INSERT INTO generic_history_revision (workspace_id, table_id, transaction_id, recorded_at, baseline)
    SELECT t.workspace_id, t.id, txid_current(), clock_timestamp(), TRUE
    FROM generic_table t
    WHERE NOT EXISTS (
        SELECT 1
        FROM generic_history_revision r
        WHERE r.workspace_id = t.workspace_id
          AND r.table_id = t.id
    )
    RETURNING revision, workspace_id, table_id
),
ins_table AS (
    INSERT INTO generic_history_version (revision, kind, entity_id, secondary_id, payload)
    SELECT s.revision, 'table', t.id, '00000000-0000-0000-0000-000000000000'::uuid, to_jsonb(t)
    FROM seeded s
    JOIN generic_table t
      ON t.id = s.table_id AND t.workspace_id = s.workspace_id
),
ins_field AS (
    INSERT INTO generic_history_version (revision, kind, entity_id, secondary_id, payload)
    SELECT s.revision, 'field', f.id, '00000000-0000-0000-0000-000000000000'::uuid, to_jsonb(f)
    FROM seeded s
    JOIN generic_field f
      ON f.table_id = s.table_id AND f.workspace_id = s.workspace_id
),
ins_view AS (
    INSERT INTO generic_history_version (revision, kind, entity_id, secondary_id, payload)
    SELECT s.revision, 'view', v.id, '00000000-0000-0000-0000-000000000000'::uuid, to_jsonb(v)
    FROM seeded s
    JOIN generic_view v
      ON v.table_id = s.table_id AND v.workspace_id = s.workspace_id
),
ins_record AS (
    INSERT INTO generic_history_version (revision, kind, entity_id, secondary_id, payload)
    SELECT s.revision, 'record', r.id, '00000000-0000-0000-0000-000000000000'::uuid, to_jsonb(r)
    FROM seeded s
    JOIN generic_record r
      ON r.table_id = s.table_id AND r.workspace_id = s.workspace_id
)
INSERT INTO generic_history_version (revision, kind, entity_id, secondary_id, payload)
SELECT s.revision, 'cell', c.record_id, c.field_id, to_jsonb(c)
FROM seeded s
JOIN generic_record r
  ON r.table_id = s.table_id AND r.workspace_id = s.workspace_id
JOIN generic_cell c
  ON c.record_id = r.id AND c.workspace_id = s.workspace_id;

CREATE OR REPLACE FUNCTION generic_history_capture()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
DECLARE
    v_zero CONSTANT UUID := '00000000-0000-0000-0000-000000000000'::uuid;
    v_row JSONB;
    v_workspace_id UUID;
    v_table_id UUID;
    v_kind TEXT;
    v_entity_id UUID;
    v_secondary_id UUID := v_zero;
    v_revision BIGINT;
    v_payload JSONB;
    v_is_baseline BOOLEAN := FALSE;
BEGIN
    IF TG_OP = 'DELETE' THEN
        v_row := to_jsonb(OLD);
        v_payload := NULL;
    ELSE
        v_row := to_jsonb(NEW);
        v_payload := v_row;
    END IF;

    v_workspace_id := (v_row->>'workspace_id')::uuid;

    IF TG_TABLE_NAME = 'generic_table' THEN
        v_kind := 'table';
        v_table_id := (v_row->>'id')::uuid;
        v_entity_id := v_table_id;

        IF TG_OP = 'DELETE' THEN
            DELETE FROM generic_history_version
            WHERE revision IN (
                SELECT revision
                FROM generic_history_revision
                WHERE workspace_id = v_workspace_id
                  AND table_id = v_table_id
            );
            DELETE FROM generic_history_revision
            WHERE workspace_id = v_workspace_id
              AND table_id = v_table_id;
            RETURN OLD;
        END IF;

        IF TG_OP = 'INSERT' THEN
            v_is_baseline := TRUE;
        END IF;
    ELSIF TG_TABLE_NAME = 'generic_field' THEN
        v_kind := 'field';
        v_table_id := (v_row->>'table_id')::uuid;
        v_entity_id := (v_row->>'id')::uuid;
    ELSIF TG_TABLE_NAME = 'generic_view' THEN
        v_kind := 'view';
        v_table_id := (v_row->>'table_id')::uuid;
        v_entity_id := (v_row->>'id')::uuid;
    ELSIF TG_TABLE_NAME = 'generic_record' THEN
        v_kind := 'record';
        v_table_id := (v_row->>'table_id')::uuid;
        v_entity_id := (v_row->>'id')::uuid;
    ELSIF TG_TABLE_NAME = 'generic_cell' THEN
        v_kind := 'cell';
        v_entity_id := (v_row->>'record_id')::uuid;
        v_secondary_id := (v_row->>'field_id')::uuid;

        SELECT table_id INTO v_table_id
        FROM generic_record
        WHERE id = v_entity_id
          AND workspace_id = v_workspace_id;

        IF v_table_id IS NULL THEN
            SELECT table_id INTO v_table_id
            FROM generic_field
            WHERE id = v_secondary_id
              AND workspace_id = v_workspace_id;
        END IF;

        IF v_table_id IS NULL THEN
            RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
        END IF;
    ELSE
        RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
    END IF;

    IF TG_TABLE_NAME <> 'generic_table' AND TG_OP = 'DELETE' THEN
        IF NOT EXISTS (
            SELECT 1
            FROM generic_table
            WHERE id = v_table_id
              AND workspace_id = v_workspace_id
        ) THEN
            RETURN OLD;
        END IF;
    END IF;

    INSERT INTO generic_history_revision (
        workspace_id,
        table_id,
        transaction_id,
        recorded_at,
        baseline
    ) VALUES (
        v_workspace_id,
        v_table_id,
        txid_current(),
        clock_timestamp(),
        v_is_baseline
    )
    ON CONFLICT (workspace_id, table_id, transaction_id)
    DO UPDATE SET recorded_at = EXCLUDED.recorded_at
    RETURNING revision INTO v_revision;

    INSERT INTO generic_history_version (
        revision,
        kind,
        entity_id,
        secondary_id,
        payload
    ) VALUES (
        v_revision,
        v_kind,
        v_entity_id,
        v_secondary_id,
        v_payload
    )
    ON CONFLICT (revision, kind, entity_id, secondary_id)
    DO UPDATE SET payload = EXCLUDED.payload;

    RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
END;
$$;

DROP TRIGGER IF EXISTS generic_table_history_trg ON generic_table;
CREATE TRIGGER generic_table_history_trg
    AFTER INSERT OR UPDATE OR DELETE ON generic_table
    FOR EACH ROW EXECUTE FUNCTION generic_history_capture();

DROP TRIGGER IF EXISTS generic_field_history_trg ON generic_field;
CREATE TRIGGER generic_field_history_trg
    AFTER INSERT OR UPDATE OR DELETE ON generic_field
    FOR EACH ROW EXECUTE FUNCTION generic_history_capture();

DROP TRIGGER IF EXISTS generic_view_history_trg ON generic_view;
CREATE TRIGGER generic_view_history_trg
    AFTER INSERT OR UPDATE OR DELETE ON generic_view
    FOR EACH ROW EXECUTE FUNCTION generic_history_capture();

DROP TRIGGER IF EXISTS generic_record_history_trg ON generic_record;
CREATE TRIGGER generic_record_history_trg
    AFTER INSERT OR UPDATE OR DELETE ON generic_record
    FOR EACH ROW EXECUTE FUNCTION generic_history_capture();

DROP TRIGGER IF EXISTS generic_cell_history_trg ON generic_cell;
CREATE TRIGGER generic_cell_history_trg
    AFTER INSERT OR UPDATE OR DELETE ON generic_cell
    FOR EACH ROW EXECUTE FUNCTION generic_history_capture();
