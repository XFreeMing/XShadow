package handler

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestIssueReadBridgeCreateAgentSourceFilesPilotEnv(t *testing.T) {
	t.Setenv("XSHADOW_ISSUES_RUST_CREATE_AGENT_SOURCE_FILES_PILOT", "agent-source-files")
	t.Setenv("XSHADOW_ISSUES_UPSTREAM", "http://127.0.0.1:7")
	t.Setenv("XSHADOW_ISSUES_INTERNAL_TOKEN", "secret")
	bridge := NewIssueReadBridgeFromEnv(nil)
	if !reflect.DeepEqual(bridge.CreateAgentSourceFilesPilotScopes, []string{"agent-source-files"}) {
		t.Fatalf("CreateAgentSourceFilesPilotScopes = %v, want [agent-source-files]", bridge.CreateAgentSourceFilesPilotScopes)
	}
}

func TestIssueReadBridgeCreateAgentSourcePilotEnv(t *testing.T) {
	t.Setenv("XSHADOW_ISSUES_RUST_CREATE_AGENT_SOURCE_PILOT", "agent-source")
	t.Setenv("XSHADOW_ISSUES_UPSTREAM", "http://127.0.0.1:7")
	t.Setenv("XSHADOW_ISSUES_INTERNAL_TOKEN", "secret")
	bridge := NewIssueReadBridgeFromEnv(nil)
	if !reflect.DeepEqual(bridge.CreateAgentSourcePilotScopes, []string{"agent-source"}) {
		t.Fatalf("CreateAgentSourcePilotScopes = %v, want [agent-source]", bridge.CreateAgentSourcePilotScopes)
	}
}

func TestIssueReadBridgeCreateAgentSourceProjectPilotEnv(t *testing.T) {
	t.Setenv("XSHADOW_ISSUES_RUST_CREATE_AGENT_SOURCE_PROJECT_PILOT", "agent-source-project")
	t.Setenv("XSHADOW_ISSUES_UPSTREAM", "http://127.0.0.1:7")
	t.Setenv("XSHADOW_ISSUES_INTERNAL_TOKEN", "secret")
	bridge := NewIssueReadBridgeFromEnv(nil)
	if !reflect.DeepEqual(bridge.CreateAgentSourceProjectPilotScopes, []string{"agent-source-project"}) {
		t.Fatalf("CreateAgentSourceProjectPilotScopes = %v, want [agent-source-project]", bridge.CreateAgentSourceProjectPilotScopes)
	}
}

func TestAgentSourceQuickCreateReceiptProjectOK(t *testing.T) {
	t.Parallel()
	project := util.MustParseUUID("cccccccc-cccc-4ccc-8ccc-cccccccccccc")
	projectStr := util.UUIDToString(project)
	if !agentSourceQuickCreateReceiptProjectOK(
		agentSourceRustSelection{expectedSourceProjectID: project},
		rustAgentSourceQuickCreateResult{SourceProjectID: &projectStr},
	) {
		t.Fatal("matching project receipt should qualify")
	}
	if agentSourceQuickCreateReceiptProjectOK(
		agentSourceRustSelection{expectedSourceProjectID: project},
		rustAgentSourceQuickCreateResult{},
	) {
		t.Fatal("missing receipt project should fail")
	}
	wrong := "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
	if agentSourceQuickCreateReceiptProjectOK(
		agentSourceRustSelection{expectedSourceProjectID: project},
		rustAgentSourceQuickCreateResult{SourceProjectID: &wrong},
	) {
		t.Fatal("wrong receipt project should fail")
	}
	if !agentSourceQuickCreateReceiptProjectOK(
		agentSourceRustSelection{},
		rustAgentSourceQuickCreateResult{},
	) {
		t.Fatal("projectless receipt should omit source_project_id")
	}
}

func TestSelectRustAgentSourceCommentSubIssueRejectsOutOfScopeShapes(t *testing.T) {
	t.Parallel()
	h := &Handler{IssueReadBridge: &IssueReadBridge{CreateAgentSourcePilotScopes: []string{"*"}}}
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	ws := util.MustParseUUID("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	user := util.MustParseUUID("dddddddd-dddd-4ddd-8ddd-dddddddddddd")
	agent := util.MustParseUUID("eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee")
	build := service.SourceContextBuild{}
	base := preparedAgentCommentSubIssue{
		agentID: agent, runtimeID: util.MustParseUUID("ffffffff-ffff-4fff-8fff-ffffffffffff"),
		prompt: "go",
	}

	sel, err := h.selectRustAgentSourceCommentSubIssue(req, ws, user, build, service.SourceContextCapture{}, base)
	if err != nil || sel.useRust {
		t.Fatalf("no bridge queries uses rust = %v err=%v, want false", sel.useRust, err)
	}

	withAttach := base
	withAttach.attachmentIDs = []pgtype.UUID{{Valid: true}}
	sel, err = h.selectRustAgentSourceCommentSubIssue(req, ws, user, build, service.SourceContextCapture{}, withAttach)
	if err != nil || sel.useRust {
		t.Fatalf("attachments uses rust = %v err=%v, want false", sel.useRust, err)
	}

	withSquad := base
	withSquad.squadID = util.MustParseUUID("11111111-1111-4111-8111-111111111111")
	sel, err = h.selectRustAgentSourceCommentSubIssue(req, ws, user, build, service.SourceContextCapture{}, withSquad)
	if err != nil || sel.useRust {
		t.Fatalf("squad uses rust = %v err=%v, want false", sel.useRust, err)
	}

	withProject := base
	withProject.projectID = util.MustParseUUID("cccccccc-cccc-4ccc-8ccc-cccccccccccc")
	sel, err = h.selectRustAgentSourceCommentSubIssue(req, ws, user, build, service.SourceContextCapture{}, withProject)
	if err != nil || sel.useRust {
		t.Fatalf("explicit project uses rust = %v err=%v, want false", sel.useRust, err)
	}

	withRows := build
	withRows.Rows = []db.Attachment{{}}
	sel, err = h.selectRustAgentSourceCommentSubIssue(req, ws, user, withRows, service.SourceContextCapture{}, base)
	if err != nil || sel.useRust {
		t.Fatalf("source attachment rows without files pilot uses rust = %v err=%v, want false", sel.useRust, err)
	}

	hFiles := &Handler{IssueReadBridge: &IssueReadBridge{
		CreateAgentSourcePilotScopes:      []string{"*"},
		CreateAgentSourceFilesPilotScopes: []string{"*"},
	}}
	sel, err = hFiles.selectRustAgentSourceCommentSubIssue(req, ws, user, withRows, service.SourceContextCapture{}, base)
	if err != nil || sel.useRust {
		t.Fatalf("source attachment rows with both pilots still needs live agent/runtime: useRust=%v err=%v", sel.useRust, err)
	}

	hMissing := &Handler{IssueReadBridge: &IssueReadBridge{}}
	sel, err = hMissing.selectRustAgentSourceCommentSubIssue(req, ws, user, build, service.SourceContextCapture{}, base)
	if err != nil || sel.useRust {
		t.Fatalf("missing pilot uses rust = %v err=%v, want false", sel.useRust, err)
	}
}
