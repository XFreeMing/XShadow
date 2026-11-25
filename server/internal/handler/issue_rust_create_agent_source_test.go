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

func TestIssueReadBridgeCreateAgentSourcePilotEnv(t *testing.T) {
	t.Setenv("XSHADOW_ISSUES_RUST_CREATE_AGENT_SOURCE_PILOT", "agent-source")
	t.Setenv("XSHADOW_ISSUES_UPSTREAM", "http://127.0.0.1:7")
	t.Setenv("XSHADOW_ISSUES_INTERNAL_TOKEN", "secret")
	bridge := NewIssueReadBridgeFromEnv(nil)
	if !reflect.DeepEqual(bridge.CreateAgentSourcePilotScopes, []string{"agent-source"}) {
		t.Fatalf("CreateAgentSourcePilotScopes = %v, want [agent-source]", bridge.CreateAgentSourcePilotScopes)
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

	useRust, err := h.selectRustAgentSourceCommentSubIssue(req, ws, user, build, base)
	if err != nil || useRust {
		t.Fatalf("no bridge queries uses rust = %v err=%v, want false", useRust, err)
	}

	withAttach := base
	withAttach.attachmentIDs = []pgtype.UUID{{Valid: true}}
	useRust, err = h.selectRustAgentSourceCommentSubIssue(req, ws, user, build, withAttach)
	if err != nil || useRust {
		t.Fatalf("attachments uses rust = %v err=%v, want false", useRust, err)
	}

	withSquad := base
	withSquad.squadID = util.MustParseUUID("11111111-1111-4111-8111-111111111111")
	useRust, err = h.selectRustAgentSourceCommentSubIssue(req, ws, user, build, withSquad)
	if err != nil || useRust {
		t.Fatalf("squad uses rust = %v err=%v, want false", useRust, err)
	}

	withProject := base
	withProject.projectID = util.MustParseUUID("cccccccc-cccc-4ccc-8ccc-cccccccccccc")
	useRust, err = h.selectRustAgentSourceCommentSubIssue(req, ws, user, build, withProject)
	if err != nil || useRust {
		t.Fatalf("explicit project uses rust = %v err=%v, want false", useRust, err)
	}

	withRows := build
	withRows.Rows = []db.Attachment{{}}
	useRust, err = h.selectRustAgentSourceCommentSubIssue(req, ws, user, withRows, base)
	if err != nil || useRust {
		t.Fatalf("source attachment rows uses rust = %v err=%v, want false", useRust, err)
	}

	hMissing := &Handler{IssueReadBridge: &IssueReadBridge{}}
	useRust, err = hMissing.selectRustAgentSourceCommentSubIssue(req, ws, user, build, base)
	if err != nil || useRust {
		t.Fatalf("missing pilot uses rust = %v err=%v, want false", useRust, err)
	}
}
