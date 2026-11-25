-- One optional grouping field. Existing views stay ungrouped.
ALTER TABLE generic_view ADD COLUMN IF NOT EXISTS group_field TEXT;
