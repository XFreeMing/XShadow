package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/pkg/agent"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type rustAgentSourceRetryWrite struct {
	SourceTaskID          string                        `json:"source_task_id"`
	NewTaskID             string                        `json:"new_task_id"`
	ActorUserID           string                        `json:"actor_user_id"`
	ExpectedContextID     string                        `json:"expected_context_id"`
	ExpectedSourceIssueID string                        `json:"expected_source_issue_id"`
	ExpectedAgentID       string                        `json:"expected_agent_id"`
	ExpectedRuntimeID     string                        `json:"expected_runtime_id"`
	ExpectedTaskContext   json.RawMessage               `json:"expected_task_context"`
	ExpectedCaptureDigest string                        `json:"expected_capture_digest"`
	ExpectedClones        []rustSourceContextCloneTrust `json:"expected_clones,omitempty"`
	RuntimeMcpOverlay     json.RawMessage               `json:"runtime_mcp_overlay,omitempty"`
	RuntimeConnectedApps  json.RawMessage               `json:"runtime_connected_apps,omitempty"`
	RuntimeCLIVersion     string                        `json:"runtime_cli_version"`
}

type rustAgentSourceRetryResult struct {
	TaskID          string `json:"task_id"`
	SourceContextID string `json:"source_context_id"`
}

type preparedRustAgentSourceRetry struct {
	WorkspaceID       pgtype.UUID
	RequesterID       pgtype.UUID
	SourceTaskID      pgtype.UUID
	NewTaskID         pgtype.UUID
	ContextID         pgtype.UUID
	SourceIssueID     pgtype.UUID
	AgentID           pgtype.UUID
	RuntimeID         pgtype.UUID
	RuntimeCLIVersion string
	ParentContext     json.RawMessage
	CaptureDigest     string
	FilePlan          rustAgentSourceContextFilePlan
	Agent             db.Agent
}

func quickCreateContextFromTask(task db.AgentTaskQueue) (service.QuickCreateContext, bool) {
	if task.IssueID.Valid || task.ChatSessionID.Valid || task.AutopilotRunID.Valid || len(task.Context) == 0 {
		return service.QuickCreateContext{}, false
	}
	var qc service.QuickCreateContext
	if json.Unmarshal(task.Context, &qc) != nil || qc.Type != service.QuickCreateContextType {
		return service.QuickCreateContext{}, false
	}
	return qc, true
}

func (h *Handler) prepareRustRetryAgentSourceQuickCreate(
	ctx context.Context,
	wsUUID, userUUID, sourceTaskID pgtype.UUID,
) (bool, preparedRustAgentSourceRetry, error) {
	var none preparedRustAgentSourceRetry
	if h.IssueReadBridge == nil || len(h.IssueReadBridge.RetryAgentSourcePilotScopes) == 0 {
		return false, none, nil
	}
	inPilot, err := matchesIssuePilot(ctx, h.Queries, h.IssueReadBridge.RetryAgentSourcePilotScopes, uuidToString(wsUUID), wsUUID)
	if err != nil || !inPilot {
		return false, none, err
	}
	parent, err := h.Queries.GetAgentTaskInWorkspace(ctx, db.GetAgentTaskInWorkspaceParams{
		ID: sourceTaskID, WorkspaceID: wsUUID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, none, nil
		}
		return false, none, err
	}
	if parent.Status != "failed" || parent.SquadID.Valid {
		return false, none, nil
	}
	qc, ok := quickCreateContextFromTask(parent)
	if !ok || qc.SourceContextID == "" || qc.RequesterID != uuidToString(userUUID) {
		return false, none, nil
	}
	if qc.ProjectID != "" || qc.SquadID != "" || len(qc.AttachmentIDs) > 0 {
		return false, none, nil
	}
	contextID, err := util.ParseUUID(qc.SourceContextID)
	if err != nil {
		return false, none, nil
	}
	pending, err := h.Queries.GetIssueSourceContextByID(ctx, db.GetIssueSourceContextByIDParams{
		WorkspaceID: wsUUID, ID: contextID,
	})
	if err != nil || pending.State != "pending" || pending.IssueID.Valid ||
		pending.OriginTaskID != sourceTaskID || pending.CapturedByUserID != userUUID ||
		qc.ParentIssueID != uuidToString(pending.SourceIssueID) {
		return false, none, nil
	}
	clones, err := h.Queries.ListAttachmentsBySourceContext(ctx, db.ListAttachmentsBySourceContextParams{
		WorkspaceID: wsUUID, SourceContextID: contextID,
	})
	if err != nil {
		return false, none, err
	}
	filePlan, planOK := h.buildAgentSourceContextFilePlan(wsUUID, contextID, userUUID, pending.Snapshot, clones)
	if !planOK {
		return false, none, nil
	}
	if filePlan.HasFiles {
		if len(h.IssueReadBridge.AgentSourceFilesLifecyclePilotScopes) == 0 {
			return false, none, nil
		}
		inLifecycle, lifeErr := matchesIssuePilot(
			ctx, h.Queries, h.IssueReadBridge.AgentSourceFilesLifecyclePilotScopes, uuidToString(wsUUID), wsUUID,
		)
		if lifeErr != nil || !inLifecycle {
			return false, none, lifeErr
		}
	}
	sourceIssue, err := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{
		ID: pending.SourceIssueID, WorkspaceID: wsUUID,
	})
	if err != nil || sourceIssue.ProjectID.Valid {
		return false, none, nil
	}
	ag, err := h.Queries.GetAgent(ctx, parent.AgentID)
	if err != nil || ag.ArchivedAt.Valid || ag.OwnerID != userUUID || ag.RuntimeMode != "local" ||
		!ag.RuntimeID.Valid || ag.RuntimeID != parent.RuntimeID || parent.AgentID != ag.ID {
		return false, none, nil
	}
	rt, err := h.getAgentRuntime(ctx, obsmetrics.RuntimeLookupSourceSourceContext, parent.RuntimeID)
	if err != nil || !agentSourceQuickCreateLocalRuntimeOwned(userUUID, rt) {
		return false, none, nil
	}
	if !runtimeHasCapability(rt.Metadata, protocol.DaemonCapabilitySourceContextQuickCreateV1) {
		return false, none, nil
	}
	minVersion := agent.MinQuickCreateCLIVersion
	if qc.Priority != "" || qc.DueDate != "" {
		minVersion = agent.MinQuickCreateFieldsCLIVersion
	}
	if st, _ := h.checkQuickCreateDaemonVersionAtLeast(ctx, obsmetrics.RuntimeLookupSourceSourceContext, parent.RuntimeID, minVersion); st != 0 {
		return false, none, nil
	}
	parentContext := append(json.RawMessage(nil), parent.Context...)
	return true, preparedRustAgentSourceRetry{
		WorkspaceID:       wsUUID,
		RequesterID:       userUUID,
		SourceTaskID:      sourceTaskID,
		NewTaskID:         dbid.NewV7(),
		ContextID:         contextID,
		SourceIssueID:     pending.SourceIssueID,
		AgentID:           parent.AgentID,
		RuntimeID:         parent.RuntimeID,
		RuntimeCLIVersion: readRuntimeCLIVersion(rt.Metadata),
		ParentContext:     parentContext,
		CaptureDigest:     pending.CaptureDigest,
		FilePlan:          filePlan,
		Agent:             ag,
	}, nil
}

func (h *Handler) agentSourceRetryCommitted(ctx context.Context, plan preparedRustAgentSourceRetry) (db.AgentTaskQueue, bool) {
	var none db.AgentTaskQueue
	child, err := h.Queries.GetAgentTaskInWorkspace(ctx, db.GetAgentTaskInWorkspaceParams{
		ID: plan.NewTaskID, WorkspaceID: plan.WorkspaceID,
	})
	if err != nil || child.RerunOfTaskID != plan.SourceTaskID ||
		child.AgentID != plan.AgentID || child.RuntimeID != plan.RuntimeID ||
		child.OriginatorUserID != plan.RequesterID || child.AccountableUserID != plan.RequesterID ||
		!child.OriginatorSource.Valid || child.OriginatorSource.String != "direct_human" ||
		child.RetryOfTaskID.Valid || !child.ForceFreshSession {
		return none, false
	}
	if !bytes.Equal(child.Context, plan.ParentContext) {
		return none, false
	}
	pending, err := h.Queries.GetIssueSourceContextByID(ctx, db.GetIssueSourceContextByIDParams{
		WorkspaceID: plan.WorkspaceID, ID: plan.ContextID,
	})
	if err != nil || pending.OriginTaskID != plan.NewTaskID || pending.CapturedByUserID != plan.RequesterID ||
		pending.SourceIssueID != plan.SourceIssueID || pending.CaptureDigest != plan.CaptureDigest {
		return none, false
	}
	if !h.verifyAgentSourceContextFilePlanCommitted(ctx, plan.FilePlan, pending.Snapshot) {
		return none, false
	}
	switch pending.State {
	case "pending":
		return child, !pending.IssueID.Valid && !child.IssueID.Valid
	case "attached":
		return child, pending.IssueID.Valid && (!child.IssueID.Valid || child.IssueID == pending.IssueID)
	default:
		return none, false
	}
}

func (h *Handler) retrySourceContextQuickCreateViaRust(
	w http.ResponseWriter,
	r *http.Request,
	plan preparedRustAgentSourceRetry,
) (*db.AgentTaskQueue, error) {
	ctx := r.Context()
	if err := service.CheckIssueCreateCapacity(ctx, h.Queries, h.Entitlements, plan.WorkspaceID); err != nil {
		return nil, err
	}
	overlay := h.TaskService.BuildRuntimeMCPOverlay(ctx, plan.RequesterID, plan.Agent)
	payload := rustAgentSourceRetryWrite{
		SourceTaskID:          uuidToString(plan.SourceTaskID),
		NewTaskID:             uuidToString(plan.NewTaskID),
		ActorUserID:           uuidToString(plan.RequesterID),
		ExpectedContextID:     uuidToString(plan.ContextID),
		ExpectedSourceIssueID: uuidToString(plan.SourceIssueID),
		ExpectedAgentID:       uuidToString(plan.AgentID),
		ExpectedRuntimeID:     uuidToString(plan.RuntimeID),
		ExpectedTaskContext:   plan.ParentContext,
		ExpectedCaptureDigest: plan.CaptureDigest,
		ExpectedClones:        plan.FilePlan.Clones,
		RuntimeMcpOverlay:     overlay.Overlay,
		RuntimeConnectedApps:  overlay.ConnectedApps,
		RuntimeCLIVersion:     plan.RuntimeCLIVersion,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode agent source retry")
		return nil, errSourceContextResponseWritten
	}
	resp, routed := h.requestRustIssue(w, r, http.MethodPost, "/internal/issues/agent-source-retry", uuidToString(plan.WorkspaceID), uuidToString(plan.RequesterID), body)
	if !routed {
		return nil, errSourceContextResponseWritten
	}
	defer resp.Body.Close()
	const maxBody = 1 << 20
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil || int64(len(raw)) > maxBody {
		writeError(w, http.StatusBadGateway, "failed to read agent source retry response")
		return nil, errSourceContextResponseWritten
	}
	if resp.StatusCode != http.StatusOK {
		writeRustIssueResponse(w, resp, bytes.NewReader(raw))
		return nil, errSourceContextResponseWritten
	}
	var reply rustAgentSourceRetryResult
	if json.Unmarshal(raw, &reply) != nil ||
		reply.TaskID != uuidToString(plan.NewTaskID) ||
		reply.SourceContextID != uuidToString(plan.ContextID) {
		writeError(w, http.StatusBadGateway, "invalid agent source retry response")
		return nil, errSourceContextResponseWritten
	}
	child, confirmed := h.agentSourceRetryCommitted(ctx, plan)
	if !confirmed {
		writeError(w, http.StatusBadGateway, "invalid agent source retry response")
		return nil, errSourceContextResponseWritten
	}
	h.TaskService.CompleteManualSourceContextRetryAfterNativeWrite(ctx, child)
	slog.Info("source context quick-create manually retried", "source_task_id", uuidToString(plan.SourceTaskID), "task_id", uuidToString(child.ID), "backend", "rust")
	return &child, nil
}
