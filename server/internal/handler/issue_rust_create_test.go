package handler

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/util"
)

func TestCreateIssueUsesRustRejectsOutOfScopeShapes(t *testing.T) {
	t.Parallel()
	h := &Handler{IssueReadBridge: &IssueReadBridge{CreatePilotScopes: []string{"*"}}}
	req := httptest.NewRequest(http.MethodPost, "/api/issues", nil)

	ok, err := h.createIssueUsesRust(req, pgtype.UUID{}, CreateIssueRequest{
		Title:  "plain",
		Status: issuestatus.Todo,
	}, "member", pgtype.Text{}, issuestatus.Todo, pgtype.Text{}, nil, false)
	if err != nil || !ok {
		t.Fatalf("plain member todo uses rust = %v err=%v, want true", ok, err)
	}

	ok, err = h.createIssueUsesRust(req, pgtype.UUID{}, CreateIssueRequest{
		Title:  "agent",
		Status: issuestatus.Todo,
	}, "agent", pgtype.Text{}, issuestatus.Todo, pgtype.Text{}, nil, false)
	if err != nil || ok {
		t.Fatalf("agent author uses rust = %v err=%v, want false", ok, err)
	}

	origin := "quick_create"
	ok, err = h.createIssueUsesRust(req, pgtype.UUID{}, CreateIssueRequest{
		Title:      "origin",
		Status:     issuestatus.Todo,
		OriginType: &origin,
	}, "member", pgtype.Text{}, issuestatus.Todo, pgtype.Text{String: "quick_create", Valid: true}, nil, false)
	if err != nil || ok {
		t.Fatalf("explicit origin uses rust = %v err=%v, want false", ok, err)
	}

	ok, err = h.createIssueUsesRust(req, pgtype.UUID{}, CreateIssueRequest{
		Title:  "assigned agent",
		Status: issuestatus.Todo,
	}, "member", pgtype.Text{String: "agent", Valid: true}, issuestatus.Todo, pgtype.Text{}, nil, false)
	if err != nil || ok {
		t.Fatalf("agent assignee uses rust = %v err=%v, want false", ok, err)
	}

	ok, err = h.createIssueUsesRust(req, pgtype.UUID{}, CreateIssueRequest{
		Title:  "workflow handoff",
		Status: issuestatus.Todo,
	}, "member", pgtype.Text{String: "agent", Valid: true}, issuestatus.Todo, pgtype.Text{}, nil, true)
	if err != nil || ok {
		t.Fatalf("workflow handoff uses rust = %v err=%v, want false", ok, err)
	}
}

func TestIssueReadBridgeCreatePilotEnv(t *testing.T) {
	t.Setenv("XSHADOW_ISSUES_RUST_CREATE_PILOT", "create-pilot")
	t.Setenv("XSHADOW_ISSUES_UPSTREAM", "http://127.0.0.1:7")
	t.Setenv("XSHADOW_ISSUES_INTERNAL_TOKEN", "secret")
	bridge := NewIssueReadBridgeFromEnv(nil)
	if !reflect.DeepEqual(bridge.CreatePilotScopes, []string{"create-pilot"}) {
		t.Fatalf("CreatePilotScopes = %v, want [create-pilot]", bridge.CreatePilotScopes)
	}
}

func TestRustIssueCreateReceiptValidRequiresCoreFields(t *testing.T) {
	t.Parallel()
	issueID := util.MustParseUUID("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	ws := util.MustParseUUID("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
	receipt := rustIssueCreatePayload{
		Issue: IssueResponse{
			ID: issueID.String(), WorkspaceID: ws.String(), Title: "Title",
			Status: issuestatus.Todo, Number: 9, Revision: 1,
		},
	}
	if !rustIssueCreateReceiptValid(issueID, ws, receipt) {
		t.Fatal("expected valid create receipt")
	}
	receipt.Issue.Number = 0
	if rustIssueCreateReceiptValid(issueID, ws, receipt) {
		t.Fatal("expected invalid receipt without number")
	}
}
