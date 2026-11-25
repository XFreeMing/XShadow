package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestStatusIssueEditUsesRustRejectsMixedPayloads(t *testing.T) {
	t.Parallel()
	h := &Handler{
		IssueReadBridge: &IssueReadBridge{
			StatusPilotScopes: []string{"*"},
		},
	}
	rev := int64(3)
	issue := db.Issue{
		Status: issuestatus.Todo,
	}
	mustMarshal := func(m map[string]any) map[string]json.RawMessage {
		t.Helper()
		out := make(map[string]json.RawMessage, len(m))
		for k, v := range m {
			b, err := json.Marshal(v)
			if err != nil {
				t.Fatalf("marshal %q: %v", k, err)
			}
			out[k] = b
		}
		return out
	}
	req := httptest.NewRequest(http.MethodPut, "/api/issues/1", nil)
	gridOK := mustMarshal(map[string]any{
		"status":            issuestatus.InProgress,
		"expected_revision": rev,
		"position":          1200.0,
		"suppress_run":      false,
	})
	ok, err := h.statusIssueEditUsesRust(
		req,
		issue.WorkspaceID,
		issue,
		gridOK,
		UpdateIssueRequest{
			ExpectedRevision: &rev,
			Status:           strPtr(issuestatus.InProgress),
		},
		nil,
		issuestatus.InProgress,
		nil,
	)
	if err != nil || !ok {
		t.Fatalf("grid payload uses rust = %v err=%v, want true", ok, err)
	}

	withTitle := mustMarshal(map[string]any{
		"status":            issuestatus.InProgress,
		"expected_revision": rev,
		"title":             "nope",
	})
	ok, err = h.statusIssueEditUsesRust(req, issue.WorkspaceID, issue, withTitle, UpdateIssueRequest{
		ExpectedRevision: &rev,
		Status:           strPtr(issuestatus.InProgress),
	}, nil, issuestatus.InProgress, nil)
	if err != nil || ok {
		t.Fatalf("title mixed payload uses rust = %v err=%v, want false", ok, err)
	}

	terminalTarget := mustMarshal(map[string]any{
		"status":            issuestatus.Done,
		"expected_revision": rev,
	})
	ok, err = h.statusIssueEditUsesRust(req, issue.WorkspaceID, issue, terminalTarget, UpdateIssueRequest{
		ExpectedRevision: &rev,
		Status:           strPtr(issuestatus.Done),
	}, nil, issuestatus.Done, nil)
	if err != nil || ok {
		t.Fatalf("done target uses rust = %v err=%v, want false", ok, err)
	}

	customSource := issue
	customSource.Status = "qa_custom"
	ok, err = h.statusIssueEditUsesRust(req, issue.WorkspaceID, customSource, gridOK, UpdateIssueRequest{
		ExpectedRevision: &rev,
		Status:           strPtr(issuestatus.InProgress),
	}, nil, issuestatus.InProgress, nil)
	if err != nil || ok {
		t.Fatalf("custom source uses rust = %v err=%v, want false", ok, err)
	}
}

func TestStatusIssueEditUsesRustRejectsDispatchSensitivePromotion(t *testing.T) {
	t.Parallel()
	h := &Handler{IssueReadBridge: &IssueReadBridge{StatusPilotScopes: []string{"*"}}}
	rev := int64(1)
	agentID := util.MustParseUUID("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	backlog := db.Issue{
		Status:       issuestatus.Backlog,
		AssigneeType: pgtype.Text{String: "agent", Valid: true},
		AssigneeID:   agentID,
	}
	raw := map[string]json.RawMessage{
		"status":            statusPilotRawJSON(t, issuestatus.Todo),
		"expected_revision": statusPilotRawJSON(t, rev),
	}
	req := httptest.NewRequest(http.MethodPut, "/api/issues/1", nil)
	ok, err := h.statusIssueEditUsesRust(
		req,
		backlog.WorkspaceID,
		backlog,
		raw,
		UpdateIssueRequest{ExpectedRevision: &rev, Status: strPtr(issuestatus.Todo)},
		nil,
		issuestatus.Todo,
		nil,
	)
	if err != nil || !ok {
		t.Fatalf("backlog promotion with agent uses rust = %v err=%v, want true", ok, err)
	}

	squadBacklog := db.Issue{
		Status:       issuestatus.Backlog,
		AssigneeType: pgtype.Text{String: "squad", Valid: true},
		AssigneeID:   util.MustParseUUID("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"),
	}
	ok, err = h.statusIssueEditUsesRust(
		req,
		squadBacklog.WorkspaceID,
		squadBacklog,
		raw,
		UpdateIssueRequest{ExpectedRevision: &rev, Status: strPtr(issuestatus.Todo)},
		nil,
		issuestatus.Todo,
		nil,
	)
	if err != nil || !ok {
		t.Fatalf("backlog promotion with squad uses rust = %v err=%v, want true", ok, err)
	}

	run := &db.AgentTaskQueue{WorkflowStep: pgtype.Text{String: "todo", Valid: true}}
	ok, err = h.statusIssueEditUsesRust(
		req,
		backlog.WorkspaceID,
		db.Issue{Status: issuestatus.Todo},
		raw,
		UpdateIssueRequest{ExpectedRevision: &rev, Status: strPtr(issuestatus.InProgress)},
		nil,
		issuestatus.InProgress,
		run,
	)
	if err != nil || ok {
		t.Fatalf("workflow step advance uses rust = %v err=%v, want false", ok, err)
	}

	dupTarget := util.MustParseUUID("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
	duplicateReopen := db.Issue{
		Status:             issuestatus.Cancelled,
		DuplicateOfIssueID: dupTarget,
	}
	ok, err = h.statusIssueEditUsesRust(
		req,
		duplicateReopen.WorkspaceID,
		duplicateReopen,
		raw,
		UpdateIssueRequest{ExpectedRevision: &rev, Status: strPtr(issuestatus.Todo)},
		nil,
		issuestatus.Todo,
		nil,
	)
	if err != nil || ok {
		t.Fatalf("duplicate reopen uses rust = %v err=%v, want false", ok, err)
	}
}

func statusPilotRawJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRustStatusWriteSnapshotsValid(t *testing.T) {
	t.Parallel()
	rev := int64(5)
	base := ordinaryIssueSnapshot{
		Issue: db.Issue{
			Status:   issuestatus.Todo,
			Revision: rev,
			Position: 100,
		},
	}
	same := base
	if !rustStatusWriteSnapshotsValid(rev, issuestatus.Todo, nil, base, same) {
		t.Fatal("identical no-op snapshot rejected")
	}
	bumped := base
	bumped.Revision = rev + 1
	bumped.Status = issuestatus.InProgress
	bumped.LastActivityAt = pgtype.Timestamptz{Valid: true}
	bumped.UpdatedAt = pgtype.Timestamptz{Valid: true}
	if !rustStatusWriteSnapshotsValid(rev, issuestatus.InProgress, nil, base, bumped) {
		t.Fatal("status change snapshot rejected")
	}
	badRev := bumped
	badRev.Revision = rev + 2
	if rustStatusWriteSnapshotsValid(rev, issuestatus.InProgress, nil, base, badRev) {
		t.Fatal("expected revision mismatch accepted")
	}
	posOnly := base
	posOnly.Position = 200
	posOnly.Revision = rev + 1
	posOnly.UpdatedAt = pgtype.Timestamptz{Valid: true}
	if !rustStatusWriteSnapshotsValid(rev, issuestatus.Todo, floatPtr(200), base, posOnly) {
		t.Fatal("position-only snapshot rejected")
	}
}

func floatPtr(v float64) *float64 { return &v }
