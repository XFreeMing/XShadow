-- Extend generic_view with view kind and typed JSON config.
-- Existing views default to kind='grid' and config='{}'.
ALTER TABLE generic_view ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'grid';
ALTER TABLE generic_view ADD COLUMN IF NOT EXISTS config JSONB NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE generic_view DROP CONSTRAINT IF EXISTS generic_view_kind_check;
ALTER TABLE generic_view
    ADD CONSTRAINT generic_view_kind_check
    CHECK (kind IN ('grid', 'kanban', 'gallery', 'calendar', 'gantt', 'org_chart', 'form'));
ALTER TABLE generic_view DROP CONSTRAINT IF EXISTS generic_view_config_object_check;
ALTER TABLE generic_view
    ADD CONSTRAINT generic_view_config_object_check
    CHECK (jsonb_typeof(config) = 'object');
