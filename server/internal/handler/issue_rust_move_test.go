package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/internal/util"
)

func TestIssueMovePositionOnlyDetectsMixedFields(t *testing.T) {
	tests := []struct {
		name string
		body map[string]any
		want bool
	}{
		{
			name: "position only",
			body: map[string]any{"before_id": nil, "after_id": nil},
			want: true,
		},
		{
			name: "with revision",
			body: map[string]any{"before_id": nil, "after_id": nil, "expected_revision": 3},
			want: true,
		},
		{
			name: "status move",
			body: map[string]any{"before_id": nil, "after_id": nil, "status": "in_progress"},
			want: false,
		},
		{
			name: "project move",
			body: map[string]any{"before_id": nil, "after_id": nil, "project_id": "00000000-0000-4000-8000-000000000001"},
			want: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.body)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(raw, &fields); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got := issueMovePositionOnly(fields); got != tc.want {
				t.Fatalf("issueMovePositionOnly = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParseMoveExpectedRevision(t *testing.T) {
	tests := []struct {
		name       string
		body       map[string]any
		wantCode   int
		wantValue  *int64
		wantParsed bool
	}{
		{name: "missing", body: map[string]any{"before_id": nil, "after_id": nil}, wantParsed: true},
		{name: "null", body: map[string]any{"before_id": nil, "after_id": nil, "expected_revision": nil}, wantParsed: true},
		{name: "valid", body: map[string]any{"before_id": nil, "after_id": nil, "expected_revision": 2}, wantParsed: true, wantValue: int64Ptr(2)},
		{name: "invalid type", body: map[string]any{"before_id": nil, "after_id": nil, "expected_revision": "x"}, wantParsed: false, wantCode: http.StatusBadRequest},
		{name: "non positive", body: map[string]any{"before_id": nil, "after_id": nil, "expected_revision": 0}, wantParsed: false, wantCode: http.StatusBadRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.body)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(raw, &fields); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			w := httptest.NewRecorder()
			got, ok := parseMoveExpectedRevision(w, fields)
			if ok != tc.wantParsed {
				t.Fatalf("parse ok = %v, want %v body=%s", ok, tc.wantParsed, w.Body.String())
			}
			if !ok {
				if w.Code != tc.wantCode {
					t.Fatalf("status = %d, want %d body=%s", w.Code, tc.wantCode, w.Body.String())
				}
				return
			}
			if tc.wantValue == nil {
				if got != nil {
					t.Fatalf("revision = %v, want nil", *got)
				}
				return
			}
			if got == nil || *got != *tc.wantValue {
				t.Fatalf("revision = %v, want %v", got, tc.wantValue)
			}
		})
	}
}

func TestIssueMoveUsesRustRequiresWritePilot(t *testing.T) {
	handler := &Handler{IssueReadBridge: &IssueReadBridge{}}
	r := httptest.NewRequest("POST", "/api/issues/x/move", nil)
	selected, err := handler.issueMoveUsesRust(r, pgtype.UUID{Bytes: [16]byte{1}, Valid: true})
	if err != nil || selected {
		t.Fatalf("empty pilot: selected=%v err=%v", selected, err)
	}
}

func TestRustIssueRelativeMoveWriteValid(t *testing.T) {
	issueID := util.MustParseUUID("00000000-0000-4000-8000-000000000010")
	wsID := util.MustParseUUID("00000000-0000-4000-8000-000000000011")
	changed := rustIssueRelativeMoveWrite{
		Before:  ordinaryIssueSnapshot{Issue: dbIssue(issueID, wsID, 1, 10)},
		After:   ordinaryIssueSnapshot{Issue: dbIssue(issueID, wsID, 2, 15)},
		Changed: true,
	}
	if !rustIssueRelativeMoveWriteValid(issueID, wsID, changed) {
		t.Fatal("expected changed write to validate")
	}
	noop := rustIssueRelativeMoveWrite{
		Before:  ordinaryIssueSnapshot{Issue: dbIssue(issueID, wsID, 2, 15)},
		After:   ordinaryIssueSnapshot{Issue: dbIssue(issueID, wsID, 2, 15)},
		Changed: false,
	}
	if !rustIssueRelativeMoveWriteValid(issueID, wsID, noop) {
		t.Fatal("expected noop write to validate")
	}
	if rustIssueRelativeMoveWriteValid(util.MustParseUUID("00000000-0000-4000-8000-000000000099"), wsID, changed) {
		t.Fatal("wrong issue id accepted")
	}
	ambiguous := rustIssueRelativeMoveWrite{
		Before:  ordinaryIssueSnapshot{Issue: dbIssue(issueID, wsID, 1, 10)},
		After:   ordinaryIssueSnapshot{Issue: dbIssue(issueID, wsID, 3, 15)},
		Changed: true,
	}
	if rustIssueRelativeMoveWriteValid(issueID, wsID, ambiguous) {
		t.Fatal("revision jump accepted")
	}
	revisionOnly := rustIssueRelativeMoveWrite{
		Before:  ordinaryIssueSnapshot{Issue: dbIssue(issueID, wsID, 1, 10)},
		After:   ordinaryIssueSnapshot{Issue: dbIssue(issueID, wsID, 2, 10)},
		Changed: false,
	}
	if rustIssueRelativeMoveWriteValid(issueID, wsID, revisionOnly) {
		t.Fatal("changed flag disagreed with revision")
	}
	lyingNoop := rustIssueRelativeMoveWrite{
		Before:  ordinaryIssueSnapshot{Issue: dbIssue(issueID, wsID, 1, 10)},
		After:   ordinaryIssueSnapshot{Issue: dbIssue(issueID, wsID, 2, 15)},
		Changed: false,
	}
	if rustIssueRelativeMoveWriteValid(issueID, wsID, lyingNoop) {
		t.Fatal("noop flag with changed revision accepted")
	}
}

func dbIssue(id, workspaceID pgtype.UUID, revision int64, position float64) db.Issue {
	return db.Issue{ID: id, WorkspaceID: workspaceID, Revision: revision, Position: position}
}

func int64Ptr(v int64) *int64 { return &v }