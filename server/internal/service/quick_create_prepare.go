package service

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/attribution"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

// PrepareQuickCreateTaskWrite resolves attribution, project access, runtime
// overlay, and the queued-row shape without writing. Callers must still run
// issue-capacity checks before enqueue.
func (s *TaskService) PrepareQuickCreateTaskWrite(
	ctx context.Context,
	workspaceID, requesterID pgtype.UUID,
	agent db.Agent,
	squadID pgtype.UUID,
	prompt, priority, dueDate string,
	projectID, parentIssueID pgtype.UUID,
	attachmentIDs []pgtype.UUID,
	capture *SourceContextCapture,
) (db.CreateQuickCreateTaskParams, error) {
	var none db.CreateQuickCreateTaskParams
	if agent.ArchivedAt.Valid {
		return none, fmt.Errorf("agent is archived")
	}
	if !agent.RuntimeID.Valid {
		return none, fmt.Errorf("agent has no runtime")
	}

	payload := QuickCreateContext{
		Type:        QuickCreateContextType,
		Prompt:      prompt,
		RequesterID: util.UUIDToString(requesterID),
		WorkspaceID: util.UUIDToString(workspaceID),
		Priority:    priority,
		DueDate:     dueDate,
	}
	if projectID.Valid {
		payload.ProjectID = util.UUIDToString(projectID)
	}
	if squadID.Valid {
		payload.SquadID = util.UUIDToString(squadID)
	}
	if parentIssueID.Valid {
		payload.ParentIssueID = util.UUIDToString(parentIssueID)
	}
	if capture != nil {
		payload.SourceContextID = util.UUIDToString(capture.ID)
	}
	if len(attachmentIDs) > 0 {
		payload.AttachmentIDs = make([]string, 0, len(attachmentIDs))
		for _, id := range attachmentIDs {
			if id.Valid {
				payload.AttachmentIDs = append(payload.AttachmentIDs, util.UUIDToString(id))
			}
		}
	}
	contextJSON, err := json.Marshal(payload)
	if err != nil {
		return none, fmt.Errorf("marshal quick-create context: %w", err)
	}

	// The human is accountable for this enqueue before the agent creates an issue.
	attr := attribution.DirectHumanRun(requesterID, "", pgtype.UUID{})
	attr, err = s.applyAttributionFallback(ctx, attr, agent)
	if err != nil {
		return none, err
	}
	attrSource, _, attrEvidenceKind, attrEvidenceRef := attributionCreateParams(attr)
	if err := s.guardQuickCreateProjectAccess(ctx, workspaceID, agent, requesterID, attr.AccountableUserID, projectID, parentIssueID); err != nil {
		return none, err
	}
	runtimeMCPOverlay := s.buildRuntimeMCPOverlay(ctx, requesterID, agent)
	return db.CreateQuickCreateTaskParams{
		ID:                   dbid.NewV7(),
		AgentID:              agent.ID,
		RuntimeID:            agent.RuntimeID,
		Priority:             priorityToInt("high"),
		Context:              contextJSON,
		OriginatorUserID:     requesterID,
		AccountableUserID:    attr.AccountableUserID,
		RuntimeMcpOverlay:    runtimeMCPOverlay.Overlay,
		RuntimeConnectedApps: runtimeMCPOverlay.ConnectedApps,
		OriginatorSource:     attrSource,
		TriggerEvidenceKind:  attrEvidenceKind,
		TriggerEvidenceRefID: attrEvidenceRef,
	}, nil
}
