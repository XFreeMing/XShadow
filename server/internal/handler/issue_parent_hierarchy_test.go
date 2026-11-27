package handler

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
)

// Codex fixture: run with managed PostgreSQL (testHandler non-nil).
// Ordered scenarios: legacy Go parent writes, depth >10, disjoint concurrent cycles.

func TestIssueParentHierarchyDeepChainLegacyGo(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	parent := dbfx.Issue(t, "depth root")
	child := parent
	for i := 0; i < 12; i++ {
		child = dbfx.Issue(t, "depth child", testutil.Cols{"parent_issue_id": child})
	}
	leaf := dbfx.Issue(t, "depth leaf", testutil.Cols{"parent_issue_id": child})
	newParent := dbfx.Issue(t, "depth new parent")
	leafIssue, _ := testHandler.Queries.GetIssue(context.Background(), util.MustParseUUID(leaf))
	testutil.Call(t, testHandler.UpdateIssue, updateIssueRequest(leaf, map[string]any{
		"parent_issue_id":   newParent,
		"expected_revision": leafIssue.Revision,
	})).Want(http.StatusOK)
}

func TestIssueParentDisjointConcurrentCycleRejected(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	b := dbfx.Issue(t, "cycle B")
	c := dbfx.Issue(t, "cycle C")
	a := dbfx.Issue(t, "cycle A")
	d := dbfx.Issue(t, "cycle D")
	bIssue, _ := testHandler.Queries.GetIssue(context.Background(), util.MustParseUUID(b))
	dIssue, _ := testHandler.Queries.GetIssue(context.Background(), util.MustParseUUID(d))
	testutil.Call(t, testHandler.UpdateIssue, updateIssueRequest(b, map[string]any{
		"parent_issue_id": c, "expected_revision": bIssue.Revision,
	})).Want(http.StatusOK)
	testutil.Call(t, testHandler.UpdateIssue, updateIssueRequest(d, map[string]any{
		"parent_issue_id": a, "expected_revision": dIssue.Revision,
	})).Want(http.StatusOK)

	aIssue, _ := testHandler.Queries.GetIssue(context.Background(), util.MustParseUUID(a))
	cIssue, _ := testHandler.Queries.GetIssue(context.Background(), util.MustParseUUID(c))
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		testutil.Call(t, testHandler.UpdateIssue, updateIssueRequest(a, map[string]any{
			"parent_issue_id": b, "expected_revision": aIssue.Revision,
		}))
	}()
	go func() {
		defer wg.Done()
		testutil.Call(t, testHandler.UpdateIssue, updateIssueRequest(c, map[string]any{
			"parent_issue_id": d, "expected_revision": cIssue.Revision,
		}))
	}()
	wg.Wait()
	aAfter, _ := testHandler.Queries.GetIssue(context.Background(), util.MustParseUUID(a))
	cAfter, _ := testHandler.Queries.GetIssue(context.Background(), util.MustParseUUID(c))
	aParent := uuidToString(aAfter.ParentIssueID)
	cParent := uuidToString(cAfter.ParentIssueID)
	if (aParent == b) == (cParent == d) {
		t.Fatalf("expected exactly one committed parent link: A points to %q, C points to %q", aParent, cParent)
	}
}

func TestIssueParentDirectCycleLegacyGo(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	a := dbfx.Issue(t, "direct cycle A")
	b := dbfx.Issue(t, "direct cycle B", testutil.Cols{"parent_issue_id": a})
	bIssue, _ := testHandler.Queries.GetIssue(context.Background(), util.MustParseUUID(b))
	testutil.Call(t, testHandler.UpdateIssue, updateIssueRequest(b, map[string]any{
		"parent_issue_id": a, "expected_revision": bIssue.Revision,
	})).Want(http.StatusOK)
	aIssue, _ := testHandler.Queries.GetIssue(context.Background(), util.MustParseUUID(a))
	w := testutil.Call(t, testHandler.UpdateIssue, updateIssueRequest(a, map[string]any{
		"parent_issue_id": b, "expected_revision": aIssue.Revision,
	}))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("A→B after B→A: status=%d want 400", w.Code)
	}
}

func TestIssueParentConcurrentDirectSwapLegacyGo(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	a := dbfx.Issue(t, "swap A")
	b := dbfx.Issue(t, "swap B")
	aIssue, _ := testHandler.Queries.GetIssue(context.Background(), util.MustParseUUID(a))
	bIssue, _ := testHandler.Queries.GetIssue(context.Background(), util.MustParseUUID(b))
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		testutil.Call(t, testHandler.UpdateIssue, updateIssueRequest(a, map[string]any{
			"parent_issue_id": b, "expected_revision": aIssue.Revision,
		}))
	}()
	go func() {
		defer wg.Done()
		testutil.Call(t, testHandler.UpdateIssue, updateIssueRequest(b, map[string]any{
			"parent_issue_id": a, "expected_revision": bIssue.Revision,
		}))
	}()
	wg.Wait()
	aAfter, _ := testHandler.Queries.GetIssue(context.Background(), util.MustParseUUID(a))
	bAfter, _ := testHandler.Queries.GetIssue(context.Background(), util.MustParseUUID(b))
	aParent := uuidToString(aAfter.ParentIssueID)
	bParent := uuidToString(bAfter.ParentIssueID)
	if (aParent == b) == (bParent == a) {
		t.Fatalf("expected exactly one committed parent link: A points to %q, B points to %q", aParent, bParent)
	}
}
