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

func TestAssignIssueEditUsesRustRejectsMixedPayloads(t *testing.T) {
	t.Parallel()
	h := &Handler{
		IssueReadBridge: &IssueReadBridge{
			AssignPilotScopes: []string{"*"},
		},
	}
	rev := int64(2)
	agentID := util.MustParseUUID("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	prev := db.Issue{Status: issuestatus.Todo}
	req := httptest.NewRequest(http.MethodPut, "/api/issues/1", nil)
	gridOK := assignPilotRaw(t, map[string]any{
		"assignee_type":     "agent",
		"assignee_id":       "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		"expected_revision": rev,
	})
	params := db.UpdateIssueParams{
		AssigneeType: pgtype.Text{String: "agent", Valid: true},
		AssigneeID:   agentID,
	}
	ok, err := h.assignIssueEditUsesRust(req, prev.WorkspaceID, prev, gridOK, UpdateIssueRequest{
		ExpectedRevision: &rev,
		AssigneeType:     strPtr("agent"),
		AssigneeID:       strPtr("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"),
	}, nil, params)
	if err != nil || !ok {
		t.Fatalf("assign payload uses rust = %v err=%v, want true", ok, err)
	}

	withStatus := assignPilotRaw(t, map[string]any{
		"assignee_type":     "agent",
		"assignee_id":       "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		"expected_revision": rev,
		"status":            issuestatus.InProgress,
	})
	ok, err = h.assignIssueEditUsesRust(req, prev.WorkspaceID, prev, withStatus, UpdateIssueRequest{
		ExpectedRevision: &rev,
		AssigneeType:     strPtr("agent"),
		AssigneeID:       strPtr("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"),
		Status:           strPtr(issuestatus.InProgress),
	}, nil, params)
	if err != nil || ok {
		t.Fatalf("status mixed payload uses rust = %v err=%v, want false", ok, err)
	}
}

func TestAssignIssueEditUsesRustRejectsSquadSourcesAndTargets(t *testing.T) {
	t.Parallel()
	h := &Handler{IssueReadBridge: &IssueReadBridge{AssignPilotScopes: []string{"*"}}}
	rev := int64(1)
	agent := util.MustParseUUID("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	squad := util.MustParseUUID("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
	raw := assignPilotRaw(t, map[string]any{
		"assignee_type":     "agent",
		"assignee_id":       "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		"expected_revision": rev,
	})
	req := httptest.NewRequest(http.MethodPut, "/api/issues/1", nil)
	agentParams := db.UpdateIssueParams{
		AssigneeType: pgtype.Text{String: "agent", Valid: true},
		AssigneeID:   agent,
	}
	squadParams := db.UpdateIssueParams{
		AssigneeType: pgtype.Text{String: "squad", Valid: true},
		AssigneeID:   squad,
	}

	ok, err := h.assignIssueEditUsesRust(req, pgtype.UUID{}, db.Issue{Status: issuestatus.Todo}, raw,
		UpdateIssueRequest{ExpectedRevision: &rev, AssigneeType: strPtr("agent"), AssigneeID: strPtr(agent.String())},
		nil, agentParams)
	if err != nil || !ok {
		t.Fatalf("unassigned->agent uses rust = %v err=%v, want true", ok, err)
	}

	squadPrev := db.Issue{
		Status:       issuestatus.Todo,
		AssigneeType: pgtype.Text{String: "squad", Valid: true},
		AssigneeID:   squad,
	}
	ok, err = h.assignIssueEditUsesRust(req, squadPrev.WorkspaceID, squadPrev, raw,
		UpdateIssueRequest{ExpectedRevision: &rev, AssigneeType: strPtr("agent"), AssigneeID: strPtr(agent.String())},
		nil, agentParams)
	if err != nil || ok {
		t.Fatalf("squad source uses rust = %v err=%v, want false", ok, err)
	}

	ok, err = h.assignIssueEditUsesRust(req, pgtype.UUID{}, db.Issue{Status: issuestatus.Todo}, raw,
		UpdateIssueRequest{ExpectedRevision: &rev, AssigneeType: strPtr("squad"), AssigneeID: strPtr(squad.String())},
		nil, squadParams)
	if err != nil || ok {
		t.Fatalf("squad target uses rust = %v err=%v, want false", ok, err)
	}
}

func TestRustAssignWriteSnapshotsValid(t *testing.T) {
	t.Parallel()
	rev := int64(5)
	agent := util.MustParseUUID("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	base := ordinaryIssueSnapshot{
		Issue: db.Issue{
			Status:   issuestatus.Todo,
			Revision: rev,
			Position: 42,
		},
	}
	sameAssignee := base
	sameAssignee.AssigneeType = pgtype.Text{String: "agent", Valid: true}
	sameAssignee.AssigneeID = agent
	after := sameAssignee
	after.Revision = rev + 1
	after.AssigneeType = pgtype.Text{String: "agent", Valid: true}
	after.AssigneeID = agent
	after.LastActivityAt = pgtype.Timestamptz{Valid: true}
	after.UpdatedAt = pgtype.Timestamptz{Valid: true}
	if !rustAssignWriteSnapshotsValid(rev, pgtype.Text{String: "agent", Valid: true}, agent, base, after, false) {
		t.Fatal("assign change snapshot rejected")
	}
	movedStatus := after
	movedStatus.Status = issuestatus.InProgress
	if rustAssignWriteSnapshotsValid(rev, pgtype.Text{String: "agent", Valid: true}, agent, base, movedStatus, false) {
		t.Fatal("status drift accepted")
	}
	dupBefore := base
	dupBefore.DuplicateOfIssueID = agent
	dupAfter := after
	dupAfter.DuplicateOfIssueID = pgtype.UUID{}
	dupAfter.Revision = rev + 1
	dupAfter.LastActivityAt = pgtype.Timestamptz{Valid: true}
	if !rustAssignWriteSnapshotsValid(rev, pgtype.Text{String: "agent", Valid: true}, agent, dupBefore, dupAfter, true) {
		t.Fatal("stale duplicate cleanup snapshot rejected")
	}
}

func assignPilotRaw(t *testing.T, m map[string]any) map[string]json.RawMessage {
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
