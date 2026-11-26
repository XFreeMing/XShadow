package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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

func TestIssueTableIssueDateBoundNativeRoundTrip(t *testing.T) {
	rec := httptest.NewRecorder()
	spec := issueTableQuerySpec{
		Scope: issueTableScope{Kind: "workspace"},
		Filters: issueTableFiltersRequest{
			IssueDateBound: &issueTableIssueDateBoundRequest{
				Field: "due_date",
				Start: "2026-11-01",
				End:   "2026-12-01",
			},
		},
		Sort: issueTableSortRequest{Field: "due_date", Direction: "asc"},
	}
	compiled, ok := testHandler.compileIssueTableQuery(rec, newRequest(http.MethodPost, "/", nil), spec)
	if !ok {
		t.Fatalf("compile failed: %s", rec.Body.String())
	}
	if compiled.native.IssueDateBound == nil {
		t.Fatal("native issue_date_bound missing")
	}
	if compiled.native.IssueDateBound.Field != "due_date" ||
		compiled.native.IssueDateBound.Start != "2026-11-01" ||
		compiled.native.IssueDateBound.End != "2026-12-01" {
		t.Fatalf("native=%+v", compiled.native.IssueDateBound)
	}
	body, err := json.Marshal(compiled.native)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, part := range []string{`"issue_date_bound"`, `"due_date"`, `"2026-11-01"`, `"2026-12-01"`} {
		if !containsSubstr(text, part) {
			t.Fatalf("rust payload missing %q in %s", part, text)
		}
	}
}

func containsSubstr(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	}())
}
