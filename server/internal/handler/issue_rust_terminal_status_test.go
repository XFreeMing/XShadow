package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/issuestatus"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestTerminalStatusIssueEditUsesRustRequiresBothPilots(t *testing.T) {
	t.Parallel()
	h := &Handler{
		IssueReadBridge: &IssueReadBridge{
			StatusPilotScopes:         []string{"*"},
			TerminalStatusPilotScopes: []string{"*"},
		},
	}
	rev := int64(2)
	issue := db.Issue{Status: issuestatus.Todo}
	raw := map[string]json.RawMessage{
		"status":            statusPilotRawJSON(t, issuestatus.Done),
		"expected_revision": statusPilotRawJSON(t, rev),
	}
	req := httptest.NewRequest(http.MethodPut, "/api/issues/1", nil)
	ok, err := h.terminalStatusIssueEditUsesRust(
		req,
		issue.WorkspaceID,
		issue,
		raw,
		UpdateIssueRequest{ExpectedRevision: &rev, Status: strPtr(issuestatus.Done)},
		nil,
		issuestatus.Done,
		nil,
	)
	if err != nil || !ok {
		t.Fatalf("done target uses rust terminal = %v err=%v, want true", ok, err)
	}

	h.IssueReadBridge.TerminalStatusPilotScopes = nil
	ok, err = h.terminalStatusIssueEditUsesRust(
		req, issue.WorkspaceID, issue, raw,
		UpdateIssueRequest{ExpectedRevision: &rev, Status: strPtr(issuestatus.Done)},
		nil, issuestatus.Done, nil,
	)
	if err != nil || ok {
		t.Fatalf("missing terminal pilot uses rust = %v err=%v, want false", ok, err)
	}
}

func TestTerminalStatusIssueEditUsesRustRejectsWorkMethodAndActiveTargets(t *testing.T) {
	t.Parallel()
	h := &Handler{
		IssueReadBridge: &IssueReadBridge{
			StatusPilotScopes:         []string{"*"},
			TerminalStatusPilotScopes: []string{"*"},
		},
	}
	rev := int64(1)
	raw := map[string]json.RawMessage{
		"status":            statusPilotRawJSON(t, issuestatus.InProgress),
		"expected_revision": statusPilotRawJSON(t, rev),
	}
	req := httptest.NewRequest(http.MethodPut, "/api/issues/1", nil)
	ok, err := h.terminalStatusIssueEditUsesRust(
		req,
		db.Issue{}.WorkspaceID,
		db.Issue{Status: issuestatus.Todo},
		raw,
		UpdateIssueRequest{ExpectedRevision: &rev, Status: strPtr(issuestatus.InProgress)},
		nil,
		issuestatus.InProgress,
		nil,
	)
	if err != nil || ok {
		t.Fatalf("active target uses rust terminal = %v err=%v, want false", ok, err)
	}

	withMethod := db.Issue{
		Status:        issuestatus.Todo,
		WorkMethodRun: []byte(`{"state":"ready","revision":0}`),
	}
	doneRaw := map[string]json.RawMessage{
		"status":            statusPilotRawJSON(t, issuestatus.Done),
		"expected_revision": statusPilotRawJSON(t, rev),
	}
	ok, err = h.terminalStatusIssueEditUsesRust(
		req, withMethod.WorkspaceID, withMethod, doneRaw,
		UpdateIssueRequest{ExpectedRevision: &rev, Status: strPtr(issuestatus.Done)},
		nil, issuestatus.Done, nil,
	)
	if err != nil || ok {
		t.Fatalf("work method row uses rust terminal = %v err=%v, want false", ok, err)
	}

	duplicateReopen := db.Issue{
		Status:             issuestatus.Cancelled,
		DuplicateOfIssueID: pgtype.UUID{Valid: true},
	}
	ok, err = h.terminalStatusIssueEditUsesRust(
		req, duplicateReopen.WorkspaceID, duplicateReopen, doneRaw,
		UpdateIssueRequest{ExpectedRevision: &rev, Status: strPtr(issuestatus.Done)},
		nil, issuestatus.Done, nil,
	)
	if err != nil || ok {
		t.Fatalf("marked duplicate reopen uses rust terminal = %v err=%v, want false", ok, err)
	}

	markedDone := db.Issue{
		Status:             issuestatus.Cancelled,
		DuplicateOfIssueID: pgtype.UUID{Valid: true},
	}
	ok, err = h.terminalStatusIssueEditUsesRust(
		req, markedDone.WorkspaceID, markedDone, doneRaw,
		UpdateIssueRequest{ExpectedRevision: &rev, Status: strPtr(issuestatus.Done)},
		nil, issuestatus.Done, nil,
	)
	if err != nil || ok {
		t.Fatalf("duplicate marked terminal done uses rust = %v err=%v, want false", ok, err)
	}
}
