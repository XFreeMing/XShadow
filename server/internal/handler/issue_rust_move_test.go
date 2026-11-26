package handler

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestIssueMoveRustEligibleDetectsMixedFields(t *testing.T) {
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
			name: "with parent",
			body: map[string]any{
				"before_id": nil, "after_id": nil,
				"parent_issue_id": "00000000-0000-4000-8000-000000000002",
			},
			want: true,
		},
		{
			name: "clear parent",
			body: map[string]any{"before_id": nil, "after_id": nil, "parent_issue_id": nil},
			want: true,
		},
		{
			name: "status move",
			body: map[string]any{"before_id": nil, "after_id": nil, "status": "in_progress"},
			want: false,
		},
		{
			name: "assignee move",
			body: map[string]any{
				"before_id": nil, "after_id": nil,
				"assignee_type": "agent", "assignee_id": "00000000-0000-4000-8000-000000000003",
			},
			want: false,
		},
		{
			name: "project move",
			body: map[string]any{"before_id": nil, "after_id": nil, "project_id": "00000000-0000-4000-8000-000000000001"},
			want: false,
		},
		{
			name: "stop previous runs",
			body: map[string]any{"before_id": nil, "after_id": nil, "stop_previous_assignee_runs": true},
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
			if got := issueMoveRustEligible(fields); got != tc.want {
				t.Fatalf("issueMoveRustEligible = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDecodeIssueMoveParentIntentCanonicalizesUUID(t *testing.T) {
	fields := map[string]json.RawMessage{
		"parent_issue_id": json.RawMessage(`"AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA"`),
	}
	w := httptest.NewRecorder()
	intent, ok := decodeIssueMoveParentIntent(w, fields)
	if !ok || !intent.touched || intent.clear {
		t.Fatalf("intent = %+v ok=%v", intent, ok)
	}
	want := util.MustParseUUID("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	if intent.parent != want {
		t.Fatalf("parent = %v want %v", intent.parent, want)
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
	parent := util.MustParseUUID("00000000-0000-4000-8000-000000000012")
	emptyIntent := issueMoveParentIntent{}

	changed := rustIssueRelativeMoveWrite{
		Before:  ordinaryIssueSnapshot{Issue: dbIssue(issueID, wsID, 1, 10)},
		After:   ordinaryIssueSnapshot{Issue: dbIssue(issueID, wsID, 2, 15)},
		Changed: true,
	}
	if !rustIssueRelativeMoveWriteSnapshotsValid(nil, emptyIntent, issueID, wsID, changed) {
		t.Fatal("expected changed write to validate")
	}

	noop := rustIssueRelativeMoveWrite{
		Before:  ordinaryIssueSnapshot{Issue: dbIssue(issueID, wsID, 2, 15)},
		After:   ordinaryIssueSnapshot{Issue: dbIssue(issueID, wsID, 2, 15)},
		Changed: false,
	}
	if !rustIssueRelativeMoveWriteSnapshotsValid(nil, emptyIntent, issueID, wsID, noop) {
		t.Fatal("expected noop write to validate")
	}
	noopSignedZero := rustIssueRelativeMoveWrite{
		Before:  ordinaryIssueSnapshot{Issue: dbIssue(issueID, wsID, 3, 0)},
		After:   ordinaryIssueSnapshot{Issue: dbIssue(issueID, wsID, 3, math.Copysign(0, -1))},
		Changed: false,
	}
	if !rustIssueRelativeMoveWriteSnapshotsValid(nil, emptyIntent, issueID, wsID, noopSignedZero) {
		t.Fatal("expected ±0 noop to validate")
	}

	if rustIssueRelativeMoveWriteSnapshotsValid(nil, emptyIntent, util.MustParseUUID("00000000-0000-4000-8000-000000000099"), wsID, changed) {
		t.Fatal("wrong issue id accepted")
	}
	ambiguous := rustIssueRelativeMoveWrite{
		Before:  ordinaryIssueSnapshot{Issue: dbIssue(issueID, wsID, 1, 10)},
		After:   ordinaryIssueSnapshot{Issue: dbIssue(issueID, wsID, 3, 15)},
		Changed: true,
	}
	if rustIssueRelativeMoveWriteSnapshotsValid(nil, emptyIntent, issueID, wsID, ambiguous) {
		t.Fatal("revision jump accepted")
	}
	revisionOnly := rustIssueRelativeMoveWrite{
		Before:  ordinaryIssueSnapshot{Issue: dbIssue(issueID, wsID, 1, 10)},
		After:   ordinaryIssueSnapshot{Issue: dbIssue(issueID, wsID, 2, 10)},
		Changed: false,
	}
	if rustIssueRelativeMoveWriteSnapshotsValid(nil, emptyIntent, issueID, wsID, revisionOnly) {
		t.Fatal("changed flag disagreed with revision")
	}
	lyingNoop := rustIssueRelativeMoveWrite{
		Before:  ordinaryIssueSnapshot{Issue: dbIssue(issueID, wsID, 1, 10)},
		After:   ordinaryIssueSnapshot{Issue: dbIssue(issueID, wsID, 2, 15)},
		Changed: false,
	}
	if rustIssueRelativeMoveWriteSnapshotsValid(nil, emptyIntent, issueID, wsID, lyingNoop) {
		t.Fatal("noop flag with changed revision accepted")
	}

	setParent := issueMoveParentIntent{touched: true, parent: parent}
	parentBefore := ordinaryIssueSnapshot{Issue: dbIssue(issueID, wsID, 4, 20)}
	parentBefore.ParentIssueID = pgtype.UUID{Valid: false}
	parentAfter := ordinaryIssueSnapshot{Issue: dbIssue(issueID, wsID, 5, 20)}
	parentAfter.ParentIssueID = parent
	parentAfter.LastActivityAt = pgtype.Timestamptz{Valid: true}
	parentMove := rustIssueRelativeMoveWrite{
		Before: parentBefore, After: parentAfter, Changed: true,
	}
	if !rustIssueRelativeMoveWriteSnapshotsValid(nil, setParent, issueID, wsID, parentMove) {
		t.Fatal("expected parent-only revision bump to validate")
	}
	wrongParent := issueMoveParentIntent{touched: true, parent: util.MustParseUUID("00000000-0000-4000-8000-000000000099")}
	if rustIssueRelativeMoveWriteSnapshotsValid(nil, wrongParent, issueID, wsID, parentMove) {
		t.Fatal("wrong parent id in receipt should fail")
	}

	clearParent := issueMoveParentIntent{touched: true, clear: true}
	clearAfter := ordinaryIssueSnapshot{Issue: dbIssue(issueID, wsID, 6, 20)}
	clearAfter.ParentIssueID = pgtype.UUID{Valid: false}
	clearAfter.LastActivityAt = pgtype.Timestamptz{Valid: true}
	clearMove := rustIssueRelativeMoveWrite{
		Before: parentAfter, After: clearAfter, Changed: true,
	}
	if !rustIssueRelativeMoveWriteSnapshotsValid(nil, clearParent, issueID, wsID, clearMove) {
		t.Fatal("expected parent clear receipt to validate")
	}

	combined := rustIssueRelativeMoveWrite{
		Before: ordinaryIssueSnapshot{Issue: dbIssue(issueID, wsID, 2, 10)},
		After: ordinaryIssueSnapshot{
			Issue: dbIssue(issueID, wsID, 3, 25),
		},
		Changed: true,
	}
	combined.After.ParentIssueID = parent
	combined.After.LastActivityAt = pgtype.Timestamptz{Valid: true}
	if !rustIssueRelativeMoveWriteSnapshotsValid(nil, setParent, issueID, wsID, combined) {
		t.Fatal("expected combined parent+position write with +1 revision to validate")
	}
	combinedBadRev := combined
	combinedBadRev.After.Revision = 5
	if rustIssueRelativeMoveWriteSnapshotsValid(nil, setParent, issueID, wsID, combinedBadRev) {
		t.Fatal("revision jump on combined move should fail")
	}

	corruptTitle := combined
	corruptTitle.After.Title = "mutated"
	if rustIssueRelativeMoveWriteSnapshotsValid(nil, setParent, issueID, wsID, corruptTitle) {
		t.Fatal("corrupt title should fail receipt validation")
	}
	missingActivity := parentMove
	missingActivity.After.LastActivityAt = pgtype.Timestamptz{Valid: false}
	if rustIssueRelativeMoveWriteSnapshotsValid(nil, setParent, issueID, wsID, missingActivity) {
		t.Fatal("parent change without last_activity_at should fail")
	}

	staleParentIntent := issueMoveParentIntent{touched: true, parent: parent}
	unchangedParent := rustIssueRelativeMoveWrite{
		Before: parentBefore, After: parentBefore, Changed: false,
	}
	if !rustIssueRelativeMoveWriteSnapshotsValid(nil, emptyIntent, issueID, wsID, unchangedParent) {
		t.Fatal("noop with omitted parent should keep parent stable")
	}
	parentDrift := unchangedParent
	parentDrift.After.ParentIssueID = parent
	if rustIssueRelativeMoveWriteSnapshotsValid(nil, emptyIntent, issueID, wsID, parentDrift) {
		t.Fatal("noop with parent drift should fail when parent omitted")
	}
	if rustIssueRelativeMoveWriteSnapshotsValid(nil, staleParentIntent, issueID, wsID, unchangedParent) {
		t.Fatal("set parent intent with unchanged row should fail receipt binding")
	}
}

func dbIssue(id, workspaceID pgtype.UUID, revision int64, position float64) db.Issue {
	return db.Issue{ID: id, WorkspaceID: workspaceID, Revision: revision, Position: position}
}

func int64Ptr(v int64) *int64 { return &v }
