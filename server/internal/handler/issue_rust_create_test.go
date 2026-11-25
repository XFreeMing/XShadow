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
	planUsesRust := func(handler *Handler, issue CreateIssueRequest, creatorType string,
		assigneeType pgtype.Text, assigneeID pgtype.UUID, status string, originType pgtype.Text,
		attachmentIDs []pgtype.UUID, workflowNeedsGo bool,
	) (bool, error) {
		plan, err := handler.planIssueCreateRust(
			req, pgtype.UUID{}, issue, creatorType, "", "", assigneeType, assigneeID, status,
			originType, attachmentIDs, workflowNeedsGo, pgtype.UUID{},
		)
		return plan.useRust, err
	}

	ok, err := planUsesRust(h, CreateIssueRequest{
		Title:  "plain",
		Status: issuestatus.Todo,
	}, "member", pgtype.Text{}, pgtype.UUID{}, issuestatus.Todo, pgtype.Text{}, nil, false)
	if err != nil || !ok {
		t.Fatalf("plain member todo uses rust = %v err=%v, want true", ok, err)
	}

	ok, err = planUsesRust(h, CreateIssueRequest{
		Title:  "agent",
		Status: issuestatus.Todo,
	}, "agent", pgtype.Text{}, pgtype.UUID{}, issuestatus.Todo, pgtype.Text{}, nil, false)
	if err != nil || ok {
		t.Fatalf("agent author uses rust = %v err=%v, want false", ok, err)
	}

	origin := "quick_create"
	ok, err = planUsesRust(h, CreateIssueRequest{
		Title:      "origin",
		Status:     issuestatus.Todo,
		OriginType: &origin,
	}, "member", pgtype.Text{}, pgtype.UUID{}, issuestatus.Todo, pgtype.Text{String: "quick_create", Valid: true}, nil, false)
	if err != nil || ok {
		t.Fatalf("explicit origin uses rust = %v err=%v, want false", ok, err)
	}

	agentID := util.MustParseUUID("cccccccc-cccc-4ccc-8ccc-cccccccccccc")
	ok, err = planUsesRust(h, CreateIssueRequest{
		Title:  "assigned agent backlog",
		Status: issuestatus.Backlog,
	}, "member", pgtype.Text{String: "agent", Valid: true}, agentID, issuestatus.Backlog, pgtype.Text{}, nil, false)
	if err != nil || ok {
		t.Fatalf("agent assignee without dispatch pilot uses rust = %v err=%v, want false", ok, err)
	}

	hBoth := &Handler{IssueReadBridge: &IssueReadBridge{
		CreatePilotScopes:         []string{"*"},
		CreateDispatchPilotScopes: []string{"*"},
	}}
	ok, err = planUsesRust(hBoth, CreateIssueRequest{
		Title:  "assigned agent backlog",
		Status: issuestatus.Backlog,
	}, "member", pgtype.Text{String: "agent", Valid: true}, agentID, issuestatus.Backlog, pgtype.Text{}, nil, false)
	if err != nil || ok {
		t.Fatalf("agent assignee without workspace DB uses rust = %v err=%v, want false", ok, err)
	}

	ok, err = planUsesRust(hBoth, CreateIssueRequest{
		Title:  "workflow handoff",
		Status: issuestatus.Todo,
	}, "member", pgtype.Text{String: "agent", Valid: true}, agentID, issuestatus.Todo, pgtype.Text{}, nil, true)
	if err != nil || ok {
		t.Fatalf("workflow handoff uses rust = %v err=%v, want false", ok, err)
	}

	squadID := util.MustParseUUID("dddddddd-dddd-4ddd-8ddd-dddddddddddd")
	ok, err = planUsesRust(h, CreateIssueRequest{
		Title:  "squad without squad pilot",
		Status: issuestatus.Todo,
	}, "member", pgtype.Text{String: "squad", Valid: true}, squadID, issuestatus.Todo, pgtype.Text{}, nil, false)
	if err != nil || ok {
		t.Fatalf("squad assignee without squad pilot uses rust = %v err=%v, want false", ok, err)
	}

	hSquad := &Handler{IssueReadBridge: &IssueReadBridge{
		CreatePilotScopes:      []string{"*"},
		CreateSquadPilotScopes: []string{"*"},
	}}
	ok, err = planUsesRust(hSquad, CreateIssueRequest{
		Title:  "squad backlog",
		Status: issuestatus.Backlog,
	}, "member", pgtype.Text{String: "squad", Valid: true}, squadID, issuestatus.Backlog, pgtype.Text{}, nil, false)
	if err != nil || ok {
		t.Fatalf("squad assignee without workspace DB uses rust = %v err=%v, want false", ok, err)
	}
}

func TestIssueReadBridgeCreatePilotEnv(t *testing.T) {
	t.Setenv("XSHADOW_ISSUES_RUST_CREATE_PILOT", "create-pilot")
	t.Setenv("XSHADOW_ISSUES_RUST_CREATE_DISPATCH_PILOT", "create-dispatch")
	t.Setenv("XSHADOW_ISSUES_RUST_CREATE_SQUAD_PILOT", "create-squad")
	t.Setenv("XSHADOW_ISSUES_UPSTREAM", "http://127.0.0.1:7")
	t.Setenv("XSHADOW_ISSUES_INTERNAL_TOKEN", "secret")
	bridge := NewIssueReadBridgeFromEnv(nil)
	if !reflect.DeepEqual(bridge.CreatePilotScopes, []string{"create-pilot"}) {
		t.Fatalf("CreatePilotScopes = %v, want [create-pilot]", bridge.CreatePilotScopes)
	}
	if !reflect.DeepEqual(bridge.CreateDispatchPilotScopes, []string{"create-dispatch"}) {
		t.Fatalf("CreateDispatchPilotScopes = %v, want [create-dispatch]", bridge.CreateDispatchPilotScopes)
	}
	if !reflect.DeepEqual(bridge.CreateSquadPilotScopes, []string{"create-squad"}) {
		t.Fatalf("CreateSquadPilotScopes = %v, want [create-squad]", bridge.CreateSquadPilotScopes)
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
