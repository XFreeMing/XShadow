package handler

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestManualSourceContextCreateRustRejectsOutOfScopeShapes(t *testing.T) {
	t.Parallel()
	h := &Handler{IssueReadBridge: &IssueReadBridge{
		CreatePilotScopes:              []string{"*"},
		CreateSourceContextPilotScopes: []string{"*"},
	}}
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	ws := util.MustParseUUID("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	sourceIssue := util.MustParseUUID("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
	capture := service.SourceContextCapture{
		SourceIssueID: sourceIssue,
		Snapshot: service.SourceContextSnapshot{
			SourceIssue: service.SourceContextIssueSnapshot{ID: util.UUIDToString(sourceIssue)},
		},
	}
	assignee := "member"
	plan, err := h.manualSourceContextCreateRustEligible(
		req, ws,
		CreateIssueRequest{Title: "child", Status: issuestatus.Todo, AssigneeType: &assignee},
		capture,
		service.SourceContextBuild{},
		issuestatus.Todo,
	)
	if err != nil || plan.useRust {
		t.Fatalf("assignee uses rust = %v err=%v, want false", plan.useRust, err)
	}

	plan, err = h.manualSourceContextCreateRustEligible(
		req, ws,
		CreateIssueRequest{Title: "child", Status: issuestatus.Todo},
		capture,
		service.SourceContextBuild{Rows: []db.Attachment{{}}},
		issuestatus.Todo,
	)
	if err != nil || plan.useRust {
		t.Fatalf("attachment rows uses rust = %v err=%v, want false", plan.useRust, err)
	}

	hMissing := &Handler{IssueReadBridge: &IssueReadBridge{CreatePilotScopes: []string{"*"}}}
	plan, err = hMissing.manualSourceContextCreateRustEligible(
		req, ws,
		CreateIssueRequest{Title: "child", Status: issuestatus.Todo},
		capture,
		service.SourceContextBuild{},
		issuestatus.Todo,
	)
	if err != nil || plan.useRust {
		t.Fatalf("missing source-context pilot uses rust = %v err=%v, want false", plan.useRust, err)
	}
}

func TestIssueReadBridgeCreateSourceContextPilotEnv(t *testing.T) {
	t.Setenv("XSHADOW_ISSUES_RUST_CREATE_PILOT", "create-pilot")
	t.Setenv("XSHADOW_ISSUES_RUST_CREATE_SOURCE_CONTEXT_PILOT", "create-source-context")
	t.Setenv("XSHADOW_ISSUES_RUST_CREATE_SOURCE_PROJECT_PILOT", "create-source-project")
	t.Setenv("XSHADOW_ISSUES_RUST_CREATE_SOURCE_FILES_PILOT", "create-source-files")
	t.Setenv("XSHADOW_ISSUES_UPSTREAM", "http://127.0.0.1:7")
	t.Setenv("XSHADOW_ISSUES_INTERNAL_TOKEN", "secret")
	bridge := NewIssueReadBridgeFromEnv(nil)
	if !reflect.DeepEqual(bridge.CreateSourceContextPilotScopes, []string{"create-source-context"}) {
		t.Fatalf("CreateSourceContextPilotScopes = %v, want [create-source-context]", bridge.CreateSourceContextPilotScopes)
	}
	if !reflect.DeepEqual(bridge.CreateSourceProjectPilotScopes, []string{"create-source-project"}) {
		t.Fatalf("CreateSourceProjectPilotScopes = %v, want [create-source-project]", bridge.CreateSourceProjectPilotScopes)
	}
	if !reflect.DeepEqual(bridge.CreateSourceFilesPilotScopes, []string{"create-source-files"}) {
		t.Fatalf("CreateSourceFilesPilotScopes = %v, want [create-source-files]", bridge.CreateSourceFilesPilotScopes)
	}
}

func TestManualSourceContextCreateRustRejectsExplicitDestinationProject(t *testing.T) {
	t.Parallel()
	h := &Handler{IssueReadBridge: &IssueReadBridge{
		CreatePilotScopes:              []string{"*"},
		CreateSourceContextPilotScopes: []string{"*"},
		CreateSourceProjectPilotScopes: []string{"*"},
	}}
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	ws := util.MustParseUUID("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	sourceIssue := util.MustParseUUID("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
	project := util.UUIDToString(util.MustParseUUID("cccccccc-cccc-4ccc-8ccc-cccccccccccc"))
	capture := service.SourceContextCapture{SourceIssueID: sourceIssue}
	plan, err := h.manualSourceContextCreateRustEligible(
		req, ws,
		CreateIssueRequest{Title: "child", Status: issuestatus.Todo, ProjectID: &project},
		capture,
		service.SourceContextBuild{},
		issuestatus.Todo,
	)
	if err != nil || plan.useRust {
		t.Fatalf("explicit project uses rust = %v err=%v, want false", plan.useRust, err)
	}
}

func TestRustSourceContextClonePlanMatchesCaptureIgnoresRowTraversalOrder(t *testing.T) {
	t.Parallel()
	sourceA := util.MustParseUUID("11111111-1111-4111-8111-111111111111")
	sourceB := util.MustParseUUID("22222222-2222-4222-8222-222222222222")
	cloneA := util.MustParseUUID("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	cloneB := util.MustParseUUID("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
	build := service.SourceContextBuild{
		Rows: []db.Attachment{
			{ID: sourceB, Filename: "comment.txt", ContentType: "text/plain", SizeBytes: 2},
			{ID: sourceA, Filename: "issue.txt", ContentType: "text/plain", SizeBytes: 1},
		},
		Snapshot: service.SourceContextSnapshot{
			SourceIssue: service.SourceContextIssueSnapshot{
				Attachments: []service.SourceContextAttachment{{
					ID: util.UUIDToString(sourceA), OwnerType: "issue", OwnerID: "issue-1",
					Filename: "issue.txt", ContentType: "text/plain", SizeBytes: 1,
				}},
			},
			CommentThread: []service.SourceContextCommentSnapshot{{
				ID: "comment-1",
				Attachments: []service.SourceContextAttachment{{
					ID: util.UUIDToString(sourceB), OwnerType: "comment", OwnerID: "comment-1",
					Filename: "comment.txt", ContentType: "text/plain", SizeBytes: 2,
				}},
			}},
		},
	}
	capture := service.SourceContextCapture{
		Clones: []service.SourceContextClone{
			{ID: cloneB, Filename: "comment.txt", ContentType: "text/plain", SizeBytes: 2},
			{ID: cloneA, Filename: "issue.txt", ContentType: "text/plain", SizeBytes: 1},
		},
		Snapshot: service.SourceContextSnapshot{
			SourceIssue: service.SourceContextIssueSnapshot{
				Attachments: []service.SourceContextAttachment{{
					ID: util.UUIDToString(cloneA), SourceAttachmentID: util.UUIDToString(sourceA),
					OwnerType: "issue", OwnerID: "issue-1",
					Filename: "issue.txt", ContentType: "text/plain", SizeBytes: 1,
				}},
			},
			CommentThread: []service.SourceContextCommentSnapshot{{
				ID: "comment-1",
				Attachments: []service.SourceContextAttachment{{
					ID: util.UUIDToString(cloneB), SourceAttachmentID: util.UUIDToString(sourceB),
					OwnerType: "comment", OwnerID: "comment-1",
					Filename: "comment.txt", ContentType: "text/plain", SizeBytes: 2,
				}},
			}},
		},
	}
	if !rustSourceContextClonePlanMatchesCapture(build, capture) {
		t.Fatal("expected reverse row traversal order to still match by source id")
	}
}

func TestManualSourceContextCreateRustFilesNeedDedicatedPilot(t *testing.T) {
	t.Parallel()
	h := &Handler{IssueReadBridge: &IssueReadBridge{
		CreatePilotScopes:              []string{"*"},
		CreateSourceContextPilotScopes: []string{"*"},
	}}
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	ws := util.MustParseUUID("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	sourceIssue := util.MustParseUUID("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
	cloneID := util.MustParseUUID("cccccccc-cccc-4ccc-8ccc-cccccccccccc")
	sourceFile := util.MustParseUUID("dddddddd-dddd-4ddd-8ddd-dddddddddddd")
	capture := service.SourceContextCapture{
		SourceIssueID: sourceIssue,
		Clones:        []service.SourceContextClone{{ID: cloneID, Filename: "a.txt", URL: "/x", ContentType: "text/plain", SizeBytes: 1}},
		Snapshot: service.SourceContextSnapshot{
			SourceIssue: service.SourceContextIssueSnapshot{
				ID:          util.UUIDToString(sourceIssue),
				Attachments: []service.SourceContextAttachment{{ID: util.UUIDToString(cloneID), SourceAttachmentID: util.UUIDToString(sourceFile)}},
			},
			AnchorCommentID: util.UUIDToString(util.MustParseUUID("eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee")),
			CommentThread: []service.SourceContextCommentSnapshot{{
				ID: util.UUIDToString(util.MustParseUUID("eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee")),
			}},
		},
	}
	build := service.SourceContextBuild{
		Rows:     []db.Attachment{{ID: sourceFile}},
		Snapshot: service.SourceContextSnapshot{SourceIssue: service.SourceContextIssueSnapshot{Attachments: []service.SourceContextAttachment{{ID: util.UUIDToString(sourceFile)}}}},
	}
	plan, err := h.manualSourceContextCreateRustEligible(
		req, ws,
		CreateIssueRequest{Title: "child", Status: issuestatus.Todo},
		capture, build, issuestatus.Todo,
	)
	if err != nil || plan.useRust {
		t.Fatalf("missing files pilot uses rust = %v err=%v, want false", plan.useRust, err)
	}
}
