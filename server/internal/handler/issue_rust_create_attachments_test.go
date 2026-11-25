package handler

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestAttachmentIsFreeIssueUpload(t *testing.T) {
	t.Parallel()
	free := db.Attachment{}
	if !attachmentIsFreeIssueUpload(free) {
		t.Fatal("expected ownerless upload to be free")
	}
	withIssue := db.Attachment{IssueID: util.MustParseUUID("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")}
	if attachmentIsFreeIssueUpload(withIssue) {
		t.Fatal("expected issue-bound upload to be unavailable")
	}
}

func TestRustIssueCreateLinkedAttachmentsValid(t *testing.T) {
	t.Parallel()
	requested := []string{"11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"}
	if !rustIssueCreateLinkedAttachmentsValid(requested, nil) {
		t.Fatal("expected empty linked set to be valid")
	}
	if !rustIssueCreateLinkedAttachmentsValid(requested, []string{requested[0]}) {
		t.Fatal("expected subset linked set to be valid")
	}
	if rustIssueCreateLinkedAttachmentsValid(requested, []string{requested[0], requested[0]}) {
		t.Fatal("expected duplicate linked ids to be rejected")
	}
	if rustIssueCreateLinkedAttachmentsValid(requested, []string{"33333333-3333-4333-8333-333333333333"}) {
		t.Fatal("expected foreign linked id to be rejected")
	}
}

func TestCanonicalAttachmentIDStringsDedupesAndOrders(t *testing.T) {
	t.Parallel()
	id1 := util.MustParseUUID("11111111-1111-4111-8111-111111111111")
	id2 := util.MustParseUUID("22222222-2222-4222-8222-222222222222")
	got := canonicalAttachmentIDStrings([]pgtype.UUID{id2, id1, id2})
	want := []string{id1.String(), id2.String()}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("canonical ids = %#v, want %#v", got, want)
	}
}

func TestIssueReadBridgeCreateAttachmentsPilotEnv(t *testing.T) {
	t.Setenv("XSHADOW_ISSUES_RUST_CREATE_ATTACHMENTS_PILOT", "create-attachments")
	t.Setenv("XSHADOW_ISSUES_UPSTREAM", "http://127.0.0.1:7")
	t.Setenv("XSHADOW_ISSUES_INTERNAL_TOKEN", "secret")
	bridge := NewIssueReadBridgeFromEnv(nil)
	if bridge.CreateAttachPilotScopes == nil || bridge.CreateAttachPilotScopes[0] != "create-attachments" {
		t.Fatalf("CreateAttachPilotScopes = %#v, want [create-attachments]", bridge.CreateAttachPilotScopes)
	}
}
