package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type rustAgentSourceQuickCreateWrite struct {
	TaskID               string                       `json:"task_id"`
	AgentID              string                       `json:"agent_id"`
	RuntimeID            string                       `json:"runtime_id"`
	RuntimeCLIVersion    string                       `json:"runtime_cli_version"`
	Priority             int32                        `json:"priority"`
	Context              json.RawMessage              `json:"context"`
	OriginatorUserID     string                       `json:"originator_user_id"`
	AccountableUserID    string                       `json:"accountable_user_id"`
	RuntimeMcpOverlay    json.RawMessage              `json:"runtime_mcp_overlay,omitempty"`
	RuntimeConnectedApps json.RawMessage              `json:"runtime_connected_apps,omitempty"`
	OriginatorSource     *string                      `json:"originator_source,omitempty"`
	TriggerEvidenceKind  *string                      `json:"trigger_evidence_kind,omitempty"`
	TriggerEvidenceRefID *string                      `json:"trigger_evidence_ref_id,omitempty"`
	SourceContext        rustSourceContextCreateTrust `json:"source_context"`
}

type rustAgentSourceQuickCreateResult struct {
	TaskID          string `json:"task_id"`
	SourceContextID string `json:"source_context_id"`
}

func agentSourceQuickCreateLocalRuntimeOwned(userID pgtype.UUID, rt db.AgentRuntime) bool {
	if rt.Status != "online" || rt.RuntimeMode != "local" {
		return false
	}
	return rt.OwnerID.Valid && rt.OwnerID == userID
}

func (h *Handler) selectRustAgentSourceCommentSubIssue(
	r *http.Request,
	wsUUID pgtype.UUID,
	userUUID pgtype.UUID,
	build service.SourceContextBuild,
	prepared preparedAgentCommentSubIssue,
) (bool, error) {
	if h.IssueReadBridge == nil || len(h.IssueReadBridge.CreateAgentSourcePilotScopes) == 0 {
		return false, nil
	}
	if len(build.Rows) > 0 || len(prepared.attachmentIDs) > 0 || prepared.squadID.Valid || prepared.projectID.Valid {
		return false, nil
	}
	ctx := r.Context()
	inPilot, err := matchesIssuePilot(ctx, h.Queries, h.IssueReadBridge.CreateAgentSourcePilotScopes, uuidToString(wsUUID), wsUUID)
	if err != nil || !inPilot {
		return false, err
	}
	sourceIssueID, err := util.ParseUUID(build.Snapshot.SourceIssue.ID)
	if err != nil {
		return false, nil
	}
	sourceIssue, err := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{
		ID: sourceIssueID, WorkspaceID: wsUUID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	if sourceIssue.ProjectID.Valid {
		return false, nil
	}
	agent, err := h.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
		ID: prepared.agentID, WorkspaceID: wsUUID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	if agent.OwnerID != userUUID || agent.RuntimeMode != "local" {
		return false, nil
	}
	rt, err := h.getAgentRuntime(ctx, obsmetrics.RuntimeLookupSourceSourceContext, prepared.runtimeID)
	if err != nil {
		return false, nil
	}
	if !agentSourceQuickCreateLocalRuntimeOwned(userUUID, rt) {
		return false, nil
	}
	if !runtimeHasCapability(rt.Metadata, protocol.DaemonCapabilitySourceContextQuickCreateV1) {
		return false, nil
	}
	return true, nil
}

func (h *Handler) agentSourceQuickCreateCommitted(
	ctx context.Context,
	workspaceID pgtype.UUID,
	expected db.CreateQuickCreateTaskParams,
	capture service.SourceContextCapture,
) (db.AgentTaskQueue, bool) {
	var none db.AgentTaskQueue
	row, err := h.Queries.GetAgentTaskInWorkspace(ctx, db.GetAgentTaskInWorkspaceParams{
		ID: expected.ID, WorkspaceID: workspaceID,
	})
	if err != nil || row.AgentID != expected.AgentID || row.RuntimeID != expected.RuntimeID ||
		row.OriginatorUserID != expected.OriginatorUserID || row.AccountableUserID != expected.AccountableUserID {
		return none, false
	}
	source, err := h.Queries.GetIssueSourceContextByID(ctx, db.GetIssueSourceContextByIDParams{
		WorkspaceID: workspaceID, ID: capture.ID,
	})
	if err != nil || source.OriginTaskID != expected.ID || source.SourceIssueID != capture.SourceIssueID ||
		source.AnchorCommentID != capture.AnchorCommentID || source.CapturedByUserID != capture.CapturedByUserID ||
		source.CaptureDigest != capture.Digest {
		return none, false
	}
	switch source.State {
	case "pending":
		return row, !source.IssueID.Valid && !row.IssueID.Valid
	case "attached":
		return row, source.IssueID.Valid && (!row.IssueID.Valid || row.IssueID == source.IssueID)
	default:
		return none, false
	}
}

func (h *Handler) createAgentCommentSubIssueViaRust(
	w http.ResponseWriter,
	r *http.Request,
	workspaceID, userID pgtype.UUID,
	prepared preparedAgentCommentSubIssue,
	capture service.SourceContextCapture,
	limits service.SourceContextLimitUsage,
) error {
	ctx := r.Context()
	if err := service.CheckIssueCreateCapacity(ctx, h.Queries, h.Entitlements, workspaceID); err != nil {
		if !writeIssueLimitReached(w, err) {
			writeError(w, http.StatusInternalServerError, "failed to check issue capacity")
		}
		return errSourceContextResponseWritten
	}
	runtime, err := h.getAgentRuntime(ctx, obsmetrics.RuntimeLookupSourceSourceContext, prepared.runtimeID)
	if err != nil || !runtimeHasCapability(runtime.Metadata, protocol.DaemonCapabilitySourceContextQuickCreateV1) {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"code": "source_context_quick_create_unsupported", "error": "selected agent runtime must be updated before using captured context"})
		return errSourceContextResponseWritten
	}
	agent, err := h.Queries.GetAgent(ctx, prepared.agentID)
	if err != nil {
		return err
	}
	createParams, err := h.TaskService.PrepareQuickCreateTaskWrite(
		ctx, workspaceID, userID, agent, prepared.squadID,
		prepared.prompt, prepared.priority, prepared.dueDate,
		prepared.projectID, capture.SourceIssueID, prepared.attachmentIDs, &capture,
	)
	if err != nil {
		return err
	}
	snapshotJSON, err := json.Marshal(capture.Snapshot)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode source context snapshot")
		return errSourceContextResponseWritten
	}
	payload := rustAgentSourceQuickCreateWrite{
		TaskID:               uuidToString(createParams.ID),
		AgentID:              uuidToString(createParams.AgentID),
		RuntimeID:            uuidToString(prepared.runtimeID),
		RuntimeCLIVersion:    readRuntimeCLIVersion(runtime.Metadata),
		Priority:             createParams.Priority,
		Context:              createParams.Context,
		OriginatorUserID:     uuidToString(createParams.OriginatorUserID),
		AccountableUserID:    uuidToString(createParams.AccountableUserID),
		RuntimeMcpOverlay:    createParams.RuntimeMcpOverlay,
		RuntimeConnectedApps: createParams.RuntimeConnectedApps,
		SourceContext: rustSourceContextCreateTrust{
			ContextID:        uuidToString(capture.ID),
			SourceIssueID:    uuidToString(capture.SourceIssueID),
			AnchorCommentID:  uuidToString(capture.AnchorCommentID),
			CaptureDigest:    capture.Digest,
			CapturedByUserID: uuidToString(capture.CapturedByUserID),
			CapturedAt:       capture.CapturedAt.UTC().Format(time.RFC3339Nano),
			Snapshot:         snapshotJSON,
		},
	}
	if createParams.OriginatorSource.Valid {
		payload.OriginatorSource = &createParams.OriginatorSource.String
	}
	if createParams.TriggerEvidenceKind.Valid {
		payload.TriggerEvidenceKind = &createParams.TriggerEvidenceKind.String
	}
	if createParams.TriggerEvidenceRefID.Valid {
		ref := uuidToString(createParams.TriggerEvidenceRefID)
		payload.TriggerEvidenceRefID = &ref
	}
	body, err := json.Marshal(payload)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode agent source quick-create")
		return errSourceContextResponseWritten
	}
	resp, routed := h.requestRustIssue(w, r, http.MethodPost, "/internal/issues/agent-source-quick-create", uuidToString(workspaceID), uuidToString(userID), body)
	if !routed {
		return errSourceContextResponseWritten
	}
	defer resp.Body.Close()
	const maxBody = 1 << 20
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil || int64(len(raw)) > maxBody {
		writeError(w, http.StatusBadGateway, "failed to read agent source quick-create response")
		return errSourceContextResponseWritten
	}
	if resp.StatusCode != http.StatusOK {
		writeRustIssueResponse(w, resp, bytes.NewReader(raw))
		return errSourceContextResponseWritten
	}
	var reply rustAgentSourceQuickCreateResult
	if json.Unmarshal(raw, &reply) != nil ||
		reply.TaskID != uuidToString(createParams.ID) ||
		reply.SourceContextID != uuidToString(capture.ID) {
		writeError(w, http.StatusBadGateway, "invalid agent source quick-create response")
		return errSourceContextResponseWritten
	}
	task, confirmed := h.agentSourceQuickCreateCommitted(ctx, workspaceID, createParams, capture)
	if !confirmed {
		writeError(w, http.StatusBadGateway, "invalid agent source quick-create response")
		return errSourceContextResponseWritten
	}
	h.TaskService.CompleteQuickCreateAfterNativeWrite(ctx, task)
	slog.Info("sub-issue quick-create enqueued from captured comment", append(sourceContextAuditAttrs(r, capture.ID, capture.Snapshot.SourceIssue.ID, capture.Snapshot.AnchorCommentID, userID, "agent", limits, "success"), "task_id", uuidToString(task.ID), "backend", "rust")...)
	writeJSON(w, http.StatusAccepted, QuickCreateIssueResponse{TaskID: uuidToString(task.ID)})
	return nil
}
