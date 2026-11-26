package issueparentlink

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/util"
)

func TestLockIDsForParentWriteOrdersDeterministically(t *testing.T) {
	t.Parallel()
	child := util.MustParseUUID("cccccccc-cccc-4ccc-8ccc-cccccccccccc")
	old := util.MustParseUUID("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
	newp := util.MustParseUUID("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	ids := lockIDsForParentWrite(child, old, newp)
	if len(ids) != 3 {
		t.Fatalf("ids len=%d want 3", len(ids))
	}
	if uuidToString(ids[0]) != uuidToString(newp) {
		t.Fatalf("first lock=%s want new parent first in sort order", uuidToString(ids[0]))
	}
	if ids[0] == ids[1] || ids[1] == ids[2] {
		t.Fatal("duplicate lock ids")
	}
}

func TestHierarchyLockSuffixConstant(t *testing.T) {
	t.Parallel()
	if HierarchyLockSuffix != ":issue_parent_hierarchy" {
		t.Fatalf("suffix=%q", HierarchyLockSuffix)
	}
}

func TestLockIDsForParentWriteSkipsInvalid(t *testing.T) {
	t.Parallel()
	child := util.MustParseUUID("cccccccc-cccc-4ccc-8ccc-cccccccccccc")
	ids := lockIDsForParentWrite(child, pgtype.UUID{}, pgtype.UUID{})
	if len(ids) != 1 || ids[0] != child {
		t.Fatalf("ids=%v", ids)
	}
}
