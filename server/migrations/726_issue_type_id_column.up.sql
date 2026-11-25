-- Nullable reference to the workspace issue_type definition; untyped legacy
-- issues remain NULL. No foreign key per repository migration rules.
ALTER TABLE issue
    ADD COLUMN IF NOT EXISTS issue_type_id UUID;
