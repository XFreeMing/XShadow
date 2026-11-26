package issueparentlink

// HierarchyLockSuffix is appended to a workspace UUID for pg_advisory_xact_lock.
// Rust parent writes use the same suffix.
const HierarchyLockSuffix = ":issue_parent_hierarchy"
