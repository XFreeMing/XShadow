package handler

import (
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestRustWorkflowPreparedTaskMatchesPromotedRow(t *testing.T) {
	t.Parallel()
	taskID := util.MustParseUUID("11111111-1111-4111-8111-111111111111")
	agentID := util.MustParseUUID("22222222-2222-4222-8222-222222222222")
	issueID := util.MustParseUUID("33333333-3333-4333-8333-333333333333")
	runtimeID := util.MustParseUUID("44444444-4444-4444-8444-444444444444")
	squadID := util.MustParseUUID("55555555-5555-4555-8555-555555555555")
	prep := service.IssuePreparedWorkflowHandoffEnqueue{
		WorkflowHandoffRevision: 4,
		QueueKeyHeadSha:         "sha",
		Task: db.CreateAgentTaskParams{
			ID: taskID, AgentID: agentID, IssueID: issueID, RuntimeID: runtimeID, SquadID: squadID,
			IsLeaderTask: pgtype.Bool{Bool: true, Valid: true},
			WorkflowStep: pgtype.Text{String: "in_progress", Valid: true},
			HeadSha:      pgtype.Text{String: "sha", Valid: true},
		},
	}
	row := db.AgentTaskQueue{
		ID: taskID, AgentID: agentID, IssueID: issueID, RuntimeID: runtimeID, SquadID: squadID,
		Status: "queued", IsLeaderTask: true,
		WorkflowStep: pgtype.Text{String: "in_progress", Valid: true},
		Context:      json.RawMessage(`{"workflow_handoff_revision":4,"head_sha":"sha"}`),
	}
	if !rustWorkflowPreparedTaskMatches(row, prep) {
		t.Fatal("expected promoted workflow handoff row to match prepared identity")
	}
}
