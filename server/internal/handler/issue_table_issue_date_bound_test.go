package handler

import (
	"fmt"
	"net/http"
	"sort"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestIssueTableRowsIssueDateBound(t *testing.T) {
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	issue := func(title string, due any) string {
		return dbfx.Issue(t, fmt.Sprintf("bound %s %s", title, suffix), testutil.Cols{"due_date": due})
	}

	before := issue("before", "2026-10-31")
	onStart := issue("on-start", "2026-11-01")
	inside := issue("inside", "2026-11-15")
	lastDay := issue("last-day", "2026-11-30")
	onEnd := issue("on-end-exclusive", "2026-12-01")
	after := issue("after", "2026-12-05")
	undated := issue("undated", nil)

	fixture := map[string]struct{}{
		before: {}, onStart: {}, inside: {}, lastDay: {}, onEnd: {}, after: {}, undated: {},
	}

	got := issueTableFixtureRows(t, fixture, issueTableFiltersRequest{
		IssueDateBound: &issueTableIssueDateBoundRequest{
			Field: "due_date",
			Start: "2026-11-01",
			End:   "2026-12-01",
		},
	})
	want := []string{onStart, inside, lastDay}
	sort.Strings(want)
	sort.Strings(got)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("window rows = %v, want %v", got, want)
	}

	testutil.Call(t, testHandler.ListIssueTableRows, newRequest(http.MethodPost, "/api/issues/table/rows", issueTableRowsRequest{
		Query: issueTableQuerySpec{
			Scope: issueTableScope{Kind: "workspace"},
			Filters: issueTableFiltersRequest{
				IssueDateBound: &issueTableIssueDateBoundRequest{
					Field: "due_date",
					Start: "2026-12-01",
					End:   "2026-11-01",
				},
			},
			Sort: issueTableSortRequest{Field: "position", Direction: "asc"},
		},
		Group: issueTableGroupSpec{Kind: "none"},
	})).Want(http.StatusBadRequest)
}
