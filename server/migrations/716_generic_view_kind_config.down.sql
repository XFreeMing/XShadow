ALTER TABLE generic_view DROP CONSTRAINT IF EXISTS generic_view_config_object_check;
ALTER TABLE generic_view DROP CONSTRAINT IF EXISTS generic_view_kind_check;
ALTER TABLE generic_view DROP COLUMN IF EXISTS config;
ALTER TABLE generic_view DROP COLUMN IF EXISTS kind;
