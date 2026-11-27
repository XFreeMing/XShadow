package service

import (
	"context"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
)

func TestResolveAutopilotNotificationRecipientRejectsFormerMember(t *testing.T) {
	fx, ownerID := newPrincipalFixture(t)
	ctx := context.Background()
	agentID := fx.privateAgentOwnedBy(t, ownerID, "recipient")
	autopilotID := fx.Insert(t, "autopilot", testutil.Cols{
		"workspace_id":    fx.WorkspaceID,
		"title":           "recipient check",
		"assignee_type":   "agent",
		"assignee_id":     agentID,
		"status":          "active",
		"execution_mode":  "run_only",
		"created_by_type": "member",
		"created_by_id":   ownerID,
	})
	autopilot, err := fx.q.GetAutopilot(ctx, util.MustParseUUID(autopilotID))
	if err != nil {
		t.Fatalf("load autopilot: %v", err)
	}
	if _, err := fx.Pool.Exec(ctx, `
		DELETE FROM member WHERE workspace_id = $1 AND user_id = $2`,
		util.MustParseUUID(fx.WorkspaceID), util.MustParseUUID(ownerID),
	); err != nil {
		t.Fatalf("remove autopilot creator from workspace: %v", err)
	}

	recipient, ok, err := ResolveAutopilotNotificationRecipient(ctx, fx.q, autopilot)
	if err != nil {
		t.Fatalf("resolve former member: %v", err)
	}
	if ok {
		t.Fatalf("former member resolved as recipient %+v", recipient)
	}
}
