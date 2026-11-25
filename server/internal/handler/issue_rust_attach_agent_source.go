package handler

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type rustPendingAgentOriginTrust struct {
	ExpectedOriginTaskID      string                        `json:"expected_origin_task_id"`
	ExpectedAgentID           string                        `json:"expected_agent_id"`
	ExpectedRuntimeID         string                        `json:"expected_runtime_id"`
	ExpectedRuntimeCLIVersion string                        `json:"expected_runtime_cli_version"`
	ExpectedContextID         string                        `json:"expected_context_id"`
	ExpectedCaptureDigest     string                        `json:"expected_capture_digest"`
	ExpectedSourceIssueID     string                        `json:"expected_source_issue_id"`
	ExpectedRequesterID       string                        `json:"expected_requester_id"`
	ExpectedClones            []rustSourceContextCloneTrust `json:"expected_clones,omitempty"`
}

type preparedAgentQuickCreateAttach struct {
	WorkspaceID   pgtype.UUID
	IssueID       pgtype.UUID
	ContextID     pgtype.UUID
	SourceIssueID pgtype.UUID
	RequesterID   pgtype.UUID
	AgentID       pgtype.UUID
	OriginTaskID  pgtype.UUID
	CaptureDigest string
	FilePlan      rustAgentSourceContextFilePlan
	Trust         rustPendingAgentOriginTrust
}

func taskHeaderMatchesOrigin(r *http.Request, originID pgtype.UUID) bool {
	hdr := r.Header.Get("X-Task-ID")
	if hdr == "" {
		return false
	}
	parsed, err := util.ParseUUID(hdr)
	return err == nil && parsed == originID
}

func agentQuickCreateAttachShape(
	req CreateIssueRequest,
	assigneeType pgtype.Text,
	parentIssueID, projectID, issueTypeID pgtype.UUID,
	attachmentCount, labelCount int,
	status string,
) bool {
	if assigneeType.Valid || projectID.Valid || issueTypeID.Valid {
		return false
	}
	if attachmentCount > 0 || labelCount > 0 {
		return false
	}
	if req.Stage != nil || req.PRAutoCompleteDisabled {
		return false
	}
	if status != "todo" {
		return false
	}
	if req.Properties != nil && len(req.Properties) > 0 {
		return false
	}
	return parentIssueID.Valid
}

func (h *Handler) prepareAgentQuickCreateAttachRust(
	r *http.Request,
	wsUUID pgtype.UUID,
	creatorType, actualCreatorID string,
	originType pgtype.Text,
	originID pgtype.UUID,
	req CreateIssueRequest,
	parentIssueID, projectID, issueTypeID pgtype.UUID,
	assigneeType pgtype.Text,
	attachmentIDs []pgtype.UUID,
	labelIDs []pgtype.UUID,
	status string,
	workflowNeedsGo bool,
) (bool, preparedAgentQuickCreateAttach, error) {
	var none preparedAgentQuickCreateAttach
	if h.IssueReadBridge == nil ||
		len(h.IssueReadBridge.CreatePilotScopes) == 0 ||
		len(h.IssueReadBridge.AttachAgentSourcePilotScopes) == 0 {
		return false, none, nil
	}
	if creatorType != "agent" || !originType.Valid || originType.String != "quick_create" || !originID.Valid {
		return false, none, nil
	}
	if !taskHeaderMatchesOrigin(r, originID) {
		return false, none, nil
	}
	if workflowNeedsGo || !agentQuickCreateAttachShape(req, assigneeType, parentIssueID, projectID, issueTypeID, len(attachmentIDs), len(labelIDs), status) {
		return false, none, nil
	}
	ctx := r.Context()
	inCreate, err := matchesIssuePilot(ctx, h.Queries, h.IssueReadBridge.CreatePilotScopes, uuidToString(wsUUID), wsUUID)
	if err != nil || !inCreate {
		return false, none, err
	}
	inAttach, err := matchesIssuePilot(ctx, h.Queries, h.IssueReadBridge.AttachAgentSourcePilotScopes, uuidToString(wsUUID), wsUUID)
	if err != nil || !inAttach {
		return false, none, err
	}
	agentID, err := util.ParseUUID(actualCreatorID)
	if err != nil {
		return false, none, nil
	}
	task, err := h.Queries.GetAgentTaskInWorkspace(ctx, db.GetAgentTaskInWorkspaceParams{
		ID: originID, WorkspaceID: wsUUID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, none, nil
		}
		return false, none, err
	}
	if task.AgentID != agentID || task.IssueID.Valid || task.SquadID.Valid {
		return false, none, nil
	}
	if task.Status != "running" {
		return false, none, nil
	}
	if !task.RuntimeID.Valid {
		return false, none, nil
	}
	qc, ok := quickCreateContextFromTask(task)
	if !ok || qc.SourceContextID == "" {
		return false, none, nil
	}
	if qc.ProjectID != "" || qc.SquadID != "" || len(qc.AttachmentIDs) > 0 {
		return false, none, nil
	}
	contextID, err := util.ParseUUID(qc.SourceContextID)
	if err != nil {
		return false, none, nil
	}
	requesterID, err := util.ParseUUID(qc.RequesterID)
	if err != nil || requesterID != task.OriginatorUserID ||
		!task.OriginatorSource.Valid || task.OriginatorSource.String != "direct_human" ||
		uuidToString(requesterID) != requestUserID(r) {
		return false, none, nil
	}
	sourceIssueID, err := util.ParseUUID(qc.ParentIssueID)
	if err != nil || sourceIssueID != parentIssueID {
		return false, none, nil
	}
	if _, err := h.Queries.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
		UserID: requesterID, WorkspaceID: wsUUID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, none, nil
		}
		return false, none, err
	}
	pending, err := h.Queries.GetIssueSourceContextByID(ctx, db.GetIssueSourceContextByIDParams{
		WorkspaceID: wsUUID, ID: contextID,
	})
	if err != nil || pending.State != "pending" || pending.IssueID.Valid ||
		pending.OriginTaskID != originID || pending.CapturedByUserID != requesterID ||
		pending.SourceIssueID != sourceIssueID {
		return false, none, nil
	}
	clones, err := h.Queries.ListAttachmentsBySourceContext(ctx, db.ListAttachmentsBySourceContextParams{
		WorkspaceID: wsUUID, SourceContextID: contextID,
	})
	if err != nil {
		return false, none, err
	}
	filePlan, planOK := h.buildAgentSourceContextFilePlan(wsUUID, contextID, requesterID, pending.Snapshot, clones)
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
		ID: sourceIssueID, WorkspaceID: wsUUID,
	})
	if err != nil || sourceIssue.ProjectID.Valid {
		return false, none, nil
	}
	ag, err := h.Queries.GetAgent(ctx, agentID)
	if err != nil || ag.ArchivedAt.Valid || ag.OwnerID != requesterID || ag.RuntimeMode != "local" ||
		!ag.RuntimeID.Valid || ag.RuntimeID != task.RuntimeID || task.AgentID != ag.ID {
		return false, none, nil
	}
	rt, err := h.getAgentRuntime(ctx, obsmetrics.RuntimeLookupSourceSourceContext, task.RuntimeID)
	if err != nil || !agentSourceQuickCreateLocalRuntimeOwned(requesterID, rt) {
		return false, none, nil
	}
	if !runtimeHasCapability(rt.Metadata, protocol.DaemonCapabilitySourceContextQuickCreateV1) {
		return false, none, nil
	}
	cliVersion := readRuntimeCLIVersion(rt.Metadata)
	if cliVersion == "" {
		return false, none, nil
	}
	trust := rustPendingAgentOriginTrust{
		ExpectedOriginTaskID:      uuidToString(originID),
		ExpectedAgentID:           uuidToString(agentID),
		ExpectedRuntimeID:         uuidToString(task.RuntimeID),
		ExpectedRuntimeCLIVersion: cliVersion,
		ExpectedContextID:         uuidToString(contextID),
		ExpectedCaptureDigest:     pending.CaptureDigest,
		ExpectedSourceIssueID:     uuidToString(sourceIssueID),
		ExpectedRequesterID:       uuidToString(requesterID),
		ExpectedClones:            filePlan.Clones,
	}
	return true, preparedAgentQuickCreateAttach{
		WorkspaceID:   wsUUID,
		IssueID:       dbid.NewV7(),
		ContextID:     contextID,
		SourceIssueID: sourceIssueID,
		RequesterID:   requesterID,
		AgentID:       agentID,
		OriginTaskID:  originID,
		CaptureDigest: pending.CaptureDigest,
		FilePlan:      filePlan,
		Trust:         trust,
	}, nil
}

func (h *Handler) agentQuickCreateAttachCommitted(
	ctx context.Context,
	plan preparedAgentQuickCreateAttach,
	receipt rustIssueCreatePayload,
) bool {
	if receipt.Issue.ID != uuidToString(plan.IssueID) {
		return false
	}
	if receipt.Issue.CreatorType != "agent" || receipt.Issue.CreatorID != uuidToString(plan.AgentID) {
		return false
	}
	if receipt.Issue.RequesterID == nil || *receipt.Issue.RequesterID != uuidToString(plan.RequesterID) {
		return false
	}
	if receipt.Issue.ParentIssueID == nil || *receipt.Issue.ParentIssueID != uuidToString(plan.SourceIssueID) {
		return false
	}
	issue, err := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{
		WorkspaceID: plan.WorkspaceID, ID: plan.IssueID,
	})
	if err != nil || !issue.OriginType.Valid || issue.OriginType.String != "quick_create" ||
		issue.OriginID != plan.OriginTaskID || issue.CreatorType != "agent" || issue.CreatorID != plan.AgentID ||
		issue.RequesterUserID != plan.RequesterID || issue.Revision < receipt.Issue.Revision {
		return false
	}
	if issue.Revision == receipt.Issue.Revision &&
		(issue.ParentIssueID != plan.SourceIssueID || issue.ProjectID.Valid ||
			receipt.Issue.Title != issue.Title || receipt.Issue.Priority != issue.Priority || receipt.Issue.Status != issue.Status) {
		return false
	}
	pending, err := h.Queries.GetIssueSourceContextByID(ctx, db.GetIssueSourceContextByIDParams{
		WorkspaceID: plan.WorkspaceID, ID: plan.ContextID,
	})
	if err != nil || pending.State != "attached" || pending.IssueID != plan.IssueID ||
		pending.SourceIssueID != plan.SourceIssueID || pending.CaptureDigest != plan.CaptureDigest ||
		pending.OriginTaskID != plan.OriginTaskID || pending.CapturedByUserID != plan.RequesterID {
		return false
	}
	return h.verifyAgentSourceContextFilePlanCommitted(ctx, plan.FilePlan, pending.Snapshot)
}
