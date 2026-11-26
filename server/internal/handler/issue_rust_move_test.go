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
			name: "with project",
			body: map[string]any{
				"before_id": nil, "after_id": nil,
				"project_id": "00000000-0000-4000-8000-000000000001",
			},
			want: true,
		},
		{
			name: "clear project",
			body: map[string]any{"before_id": nil, "after_id": nil, "project_id": nil},
			want: true,
		},
		{
			name: "parent and project together",
			body: map[string]any{
				"before_id": nil, "after_id": nil,
				"parent_issue_id": "00000000-0000-4000-8000-000000000002",
				"project_id":      "00000000-0000-4000-8000-000000000001",
			},
			want: false,
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

func TestDecodeIssueMoveProjectIntent(t *testing.T) {
	unchanged, ok := decodeIssueMoveProjectIntent(httptest.NewRecorder(), map[string]json.RawMessage{})
	if !ok || unchanged.touched {
		t.Fatalf("missing project = %+v ok=%v", unchanged, ok)
	}

	clearFields := map[string]json.RawMessage{"project_id": json.RawMessage(`null`)}
	clear, ok := decodeIssueMoveProjectIntent(httptest.NewRecorder(), clearFields)
	if !ok || !clear.touched || !clear.clear {
		t.Fatalf("clear project = %+v ok=%v", clear, ok)
	}

	setFields := map[string]json.RawMessage{
		"project_id": json.RawMessage(`"cccccccc-cccc-4ccc-8ccc-cccccccccccc"`),
	}
	set, ok := decodeIssueMoveProjectIntent(httptest.NewRecorder(), setFields)
	if !ok || !set.touched || set.clear {
		t.Fatalf("set project = %+v ok=%v", set, ok)
	}
	want := util.MustParseUUID("cccccccc-cccc-4ccc-8ccc-cccccccccccc")
	if set.project != want {
		t.Fatalf("project = %v want %v", set.project, want)
	}
}

func TestIssueMoveProjectRustAllowed(t *testing.T) {
	if !issueMoveProjectRustAllowed(issueMoveProjectIntent{}, false) {
		t.Fatal("untouched project should allow rust")
	}
	if !issueMoveProjectRustAllowed(issueMoveProjectIntent{touched: true, clear: true}, true) {
		t.Fatal("clear should allow rust even if target metadata says named")
	}
	if issueMoveProjectRustAllowed(issueMoveProjectIntent{touched: true, project: util.MustParseUUID("00000000-0000-4000-8000-000000000001")}, true) {
		t.Fatal("named workflow target should stay on Go")
	}
	if !issueMoveProjectRustAllowed(issueMoveProjectIntent{touched: true, project: util.MustParseUUID("00000000-0000-4000-8000-000000000001")}, false) {
		t.Fatal("plain target should allow rust")
	}
}

func TestEffectiveMoveProjectID(t *testing.T) {
	current := util.MustParseUUID("dddddddd-dddd-4ddd-8ddd-dddddddddddd")
	unchanged := effectiveMoveProjectID(issueMoveProjectIntent{}, current)
	if unchanged != current {
		t.Fatalf("unchanged = %v want %v", unchanged, current)
	}
	clear := effectiveMoveProjectID(issueMoveProjectIntent{touched: true, clear: true}, current)
	if clear.Valid {
		t.Fatal("clear should drop project")
	}
	target := util.MustParseUUID("eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee")
	set := effectiveMoveProjectID(issueMoveProjectIntent{touched: true, project: target}, current)
	if set != target {
		t.Fatalf("set = %v want %v", set, target)
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
	project := util.MustParseUUID("00000000-0000-4000-8000-000000000013")
	emptyParent := issueMoveParentIntent{}
	emptyProject := issueMoveProjectIntent{}

	changed := rustIssueRelativeMoveWrite{
		Before:  ordinaryIssueSnapshot{Issue: dbIssue(issueID, wsID, 1, 10)},
		After:   ordinaryIssueSnapshot{Issue: dbIssue(issueID, wsID, 2, 15)},
		Changed: true,
	}
	if !rustIssueRelativeMoveWriteSnapshotsValid(nil, emptyParent, emptyProject, issueID, wsID, changed) {
		t.Fatal("expected changed write to validate")
	}

	noop := rustIssueRelativeMoveWrite{
		Before:  ordinaryIssueSnapshot{Issue: dbIssue(issueID, wsID, 2, 15)},
		After:   ordinaryIssueSnapshot{Issue: dbIssue(issueID, wsID, 2, 15)},
		Changed: false,
	}
	if !rustIssueRelativeMoveWriteSnapshotsValid(nil, emptyParent, emptyProject, issueID, wsID, noop) {
		t.Fatal("expected noop write to validate")
	}
	noopSignedZero := rustIssueRelativeMoveWrite{
		Before:  ordinaryIssueSnapshot{Issue: dbIssue(issueID, wsID, 3, 0)},
		After:   ordinaryIssueSnapshot{Issue: dbIssue(issueID, wsID, 3, math.Copysign(0, -1))},
		Changed: false,
	}
	if !rustIssueRelativeMoveWriteSnapshotsValid(nil, emptyParent, emptyProject, issueID, wsID, noopSignedZero) {
		t.Fatal("expected ±0 noop to validate")
	}

	if rustIssueRelativeMoveWriteSnapshotsValid(nil, emptyParent, emptyProject, util.MustParseUUID("00000000-0000-4000-8000-000000000099"), wsID, changed) {
		t.Fatal("wrong issue id accepted")
	}
	ambiguous := rustIssueRelativeMoveWrite{
		Before:  ordinaryIssueSnapshot{Issue: dbIssue(issueID, wsID, 1, 10)},
		After:   ordinaryIssueSnapshot{Issue: dbIssue(issueID, wsID, 3, 15)},
		Changed: true,
	}
	if rustIssueRelativeMoveWriteSnapshotsValid(nil, emptyParent, emptyProject, issueID, wsID, ambiguous) {
		t.Fatal("revision jump accepted")
	}
	revisionOnly := rustIssueRelativeMoveWrite{
		Before:  ordinaryIssueSnapshot{Issue: dbIssue(issueID, wsID, 1, 10)},
		After:   ordinaryIssueSnapshot{Issue: dbIssue(issueID, wsID, 2, 10)},
		Changed: false,
	}
	if rustIssueRelativeMoveWriteSnapshotsValid(nil, emptyParent, emptyProject, issueID, wsID, revisionOnly) {
		t.Fatal("changed flag disagreed with revision")
	}
	lyingNoop := rustIssueRelativeMoveWrite{
		Before:  ordinaryIssueSnapshot{Issue: dbIssue(issueID, wsID, 1, 10)},
		After:   ordinaryIssueSnapshot{Issue: dbIssue(issueID, wsID, 2, 15)},
		Changed: false,
	}
	if rustIssueRelativeMoveWriteSnapshotsValid(nil, emptyParent, emptyProject, issueID, wsID, lyingNoop) {
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
	if !rustIssueRelativeMoveWriteSnapshotsValid(nil, setParent, emptyProject, issueID, wsID, parentMove) {
		t.Fatal("expected parent-only revision bump to validate")
	}
	wrongParent := issueMoveParentIntent{touched: true, parent: util.MustParseUUID("00000000-0000-4000-8000-000000000099")}
	if rustIssueRelativeMoveWriteSnapshotsValid(nil, wrongParent, emptyProject, issueID, wsID, parentMove) {
		t.Fatal("wrong parent id in receipt should fail")
	}

	clearParent := issueMoveParentIntent{touched: true, clear: true}
	clearAfter := ordinaryIssueSnapshot{Issue: dbIssue(issueID, wsID, 6, 20)}
	clearAfter.ParentIssueID = pgtype.UUID{Valid: false}
	clearAfter.LastActivityAt = pgtype.Timestamptz{Valid: true}
	clearMove := rustIssueRelativeMoveWrite{
		Before: parentAfter, After: clearAfter, Changed: true,
	}
	if !rustIssueRelativeMoveWriteSnapshotsValid(nil, clearParent, emptyProject, issueID, wsID, clearMove) {
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
	if !rustIssueRelativeMoveWriteSnapshotsValid(nil, setParent, emptyProject, issueID, wsID, combined) {
		t.Fatal("expected combined parent+position write with +1 revision to validate")
	}
	combinedBadRev := combined
	combinedBadRev.After.Revision = 5
	if rustIssueRelativeMoveWriteSnapshotsValid(nil, setParent, emptyProject, issueID, wsID, combinedBadRev) {
		t.Fatal("revision jump on combined move should fail")
	}

	corruptTitle := combined
	corruptTitle.After.Title = "mutated"
	if rustIssueRelativeMoveWriteSnapshotsValid(nil, setParent, emptyProject, issueID, wsID, corruptTitle) {
		t.Fatal("corrupt title should fail receipt validation")
	}
	missingActivity := parentMove
	missingActivity.After.LastActivityAt = pgtype.Timestamptz{Valid: false}
	if rustIssueRelativeMoveWriteSnapshotsValid(nil, setParent, emptyProject, issueID, wsID, missingActivity) {
		t.Fatal("parent change without last_activity_at should fail")
	}

	staleParentIntent := issueMoveParentIntent{touched: true, parent: parent}
	unchangedParent := rustIssueRelativeMoveWrite{
		Before: parentBefore, After: parentBefore, Changed: false,
	}
	if !rustIssueRelativeMoveWriteSnapshotsValid(nil, emptyParent, emptyProject, issueID, wsID, unchangedParent) {
		t.Fatal("noop with omitted parent should keep parent stable")
	}
	parentDrift := unchangedParent
	parentDrift.After.ParentIssueID = parent
	if rustIssueRelativeMoveWriteSnapshotsValid(nil, emptyParent, emptyProject, issueID, wsID, parentDrift) {
		t.Fatal("noop with parent drift should fail when parent omitted")
	}
	if rustIssueRelativeMoveWriteSnapshotsValid(nil, staleParentIntent, emptyProject, issueID, wsID, unchangedParent) {
		t.Fatal("set parent intent with unchanged row should fail receipt binding")
	}

	setProject := issueMoveProjectIntent{touched: true, project: project}
	projectBefore := ordinaryIssueSnapshot{Issue: dbIssue(issueID, wsID, 7, 30)}
	projectBefore.ProjectID = pgtype.UUID{Valid: false}
	projectAfter := ordinaryIssueSnapshot{Issue: dbIssue(issueID, wsID, 8, 30)}
	projectAfter.ProjectID = project
	projectMove := rustIssueRelativeMoveWrite{
		Before: projectBefore, After: projectAfter, Changed: true,
	}
	if !rustIssueRelativeMoveWriteSnapshotsValid(nil, emptyParent, setProject, issueID, wsID, projectMove) {
		t.Fatal("expected project-only revision bump to validate")
	}

	clearProject := issueMoveProjectIntent{touched: true, clear: true}
	clearProjectAfter := ordinaryIssueSnapshot{Issue: dbIssue(issueID, wsID, 9, 30)}
	clearProjectAfter.ProjectID = pgtype.UUID{Valid: false}
	clearProjectMove := rustIssueRelativeMoveWrite{
		Before: projectAfter, After: clearProjectAfter, Changed: true,
	}
	if !rustIssueRelativeMoveWriteSnapshotsValid(nil, emptyParent, clearProject, issueID, wsID, clearProjectMove) {
		t.Fatal("expected project clear receipt to validate")
	}

	unchangedProject := rustIssueRelativeMoveWrite{
		Before: projectAfter, After: projectAfter, Changed: false,
	}
	if !rustIssueRelativeMoveWriteSnapshotsValid(nil, emptyParent, emptyProject, issueID, wsID, unchangedProject) {
		t.Fatal("noop with omitted project should keep project stable")
	}
	staleProjectIntent := issueMoveProjectIntent{touched: true, project: project}
	if rustIssueRelativeMoveWriteSnapshotsValid(nil, emptyParent, staleProjectIntent, issueID, wsID, unchangedProject) {
		t.Fatal("set project intent with unchanged row should fail receipt binding")
	}
	wrongProject := issueMoveProjectIntent{touched: true, project: util.MustParseUUID("00000000-0000-4000-8000-000000000099")}
	if rustIssueRelativeMoveWriteSnapshotsValid(nil, emptyParent, wrongProject, issueID, wsID, projectMove) {
		t.Fatal("wrong project id in receipt should fail")
	}
	combinedProject := rustIssueRelativeMoveWrite{
		Before: ordinaryIssueSnapshot{Issue: dbIssue(issueID, wsID, 4, 10)},
		After: ordinaryIssueSnapshot{
			Issue: dbIssue(issueID, wsID, 5, 25),
		},
		Changed: true,
	}
	combinedProject.After.ProjectID = project
	if !rustIssueRelativeMoveWriteSnapshotsValid(nil, emptyParent, setProject, issueID, wsID, combinedProject) {
		t.Fatal("expected combined project+position write with +1 revision to validate")
	}
}

func dbIssue(id, workspaceID pgtype.UUID, revision int64, position float64) db.Issue {
	return db.Issue{ID: id, WorkspaceID: workspaceID, Revision: revision, Position: position}
}

func int64Ptr(v int64) *int64 { return &v }
