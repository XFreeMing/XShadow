-- One stored value. SQL NULL means the cell was cleared.
-- Checkbox false is a real jsonb false, not NULL.
-- No foreign keys. The primary key is attached in 699.
CREATE TABLE IF NOT EXISTS generic_cell (
    workspace_id UUID NOT NULL,
    record_id UUID NOT NULL,
    field_id UUID NOT NULL,
    value JSONB
);
