package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/entitlement"
	"github.com/multica-ai/multica/server/internal/issueguard"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type rustIssueCreateLimit struct {
	Enforce        bool  `json:"enforce"`
	Limit          int64 `json:"limit"`
	PolicyRevision int64 `json:"policy_revision"`
}

type rustIssueCreate struct {
	ID                     string                `json:"id"`
	Title                  string                `json:"title"`
	Description            *string               `json:"description,omitempty"`
	Status                 string                `json:"status"`
	NormalizedTitle        string                `json:"normalized_title"`
	Priority               string                `json:"priority"`
	AssigneeType           *string               `json:"assignee_type,omitempty"`
	AssigneeID             *string               `json:"assignee_id,omitempty"`
	CreatorType            string                `json:"creator_type"`
	CreatorID              string                `json:"creator_id"`
	ParentIssueID          *string               `json:"parent_issue_id,omitempty"`
	ProjectID              *string               `json:"project_id,omitempty"`
	StartDate              *string               `json:"start_date,omitempty"`
	DueDate                *string               `json:"due_date,omitempty"`
	LabelIDs               []string              `json:"label_ids,omitempty"`
	Properties             json.RawMessage       `json:"properties"`
	PropertyVersions       map[string]string     `json:"property_versions"`
	IssueTypeID            *string               `json:"issue_type_id,omitempty"`
	IssueTypeVersion       string                `json:"issue_type_version,omitempty"`
	AllowDuplicate         bool                  `json:"allow_duplicate"`
	WorkMethodRun          json.RawMessage       `json:"work_method_run,omitempty"`
	IssueLimit             rustIssueCreateLimit  `json:"issue_limit"`
	PRAutoCompleteDisabled bool                  `json:"pr_auto_complete_disabled,omitempty"`
	PreparedEnqueue        *json.RawMessage      `json:"prepared_enqueue,omitempty"`
	SquadCreateTrust       *rustSquadCreateTrust `json:"squad_create_trust,omitempty"`
	AttachmentIDs          []string              `json:"attachment_ids,omitempty"`
}

type rustSquadCreateTrust struct {
	SquadID          string `json:"squad_id"`
	ExpectedLeaderID string `json:"expected_leader_id"`
}

type rustIssueCreatePayload struct {
	Issue                 IssueResponse   `json:"issue"`
	Labels                []LabelResponse `json:"labels"`
	QueuedTaskID          *string         `json:"queued_task_id,omitempty"`
	CoalescedQueuedTaskID *string         `json:"coalesced_queued_task_id,omitempty"`
	LinkedAttachmentIDs   *[]string       `json:"linked_attachment_ids"`
}

type issueCreateRustPlan struct {
	useRust             bool
	issueID             pgtype.UUID
	dispatchPrep        *service.IssuePreparedAgentEnqueue
	squadTrust          *rustSquadCreateTrust
	nativeAttachmentIDs []string
}

func (h *Handler) loadOwnedSquadForIssueCreate(
	ctx context.Context,
	wsUUID, squadID pgtype.UUID,
	memberUserID string,
	requireReady bool,
) (db.Squad, bool) {
	squad, err := h.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{
		ID: squadID, WorkspaceID: wsUUID,
	})
	if err != nil || squad.ArchivedAt.Valid {
		return db.Squad{}, false
	}
	leader, err := h.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
		ID: squad.LeaderID, WorkspaceID: wsUUID,
	})
	if err != nil || leader.ArchivedAt.Valid || uuidToString(leader.OwnerID) != memberUserID {
		return db.Squad{}, false
	}
	if requireReady {
		verdict, err := service.AgentReadiness(ctx, h.runtimeLookup(uuidToString(wsUUID)), leader)
		if err != nil || !verdict.Ready() {
			return db.Squad{}, false
		}
	}
	return squad, true
}

func projectedIssueForCreate(
	issueID, wsUUID pgtype.UUID,
	req CreateIssueRequest,
	status, priority string,
	assigneeType pgtype.Text,
	assigneeID, projectID pgtype.UUID,
) db.Issue {
	return db.Issue{
		ID: issueID, WorkspaceID: wsUUID,
		Title: req.Title, Status: status, Priority: priority,
		AssigneeType: assigneeType, AssigneeID: assigneeID, ProjectID: projectID,
	}
}

func (h *Handler) plainIssueCreateRustEligible(
	req CreateIssueRequest,
	creatorType string,
	assigneeType pgtype.Text,
	status string,
	originType pgtype.Text,
	attachmentIDs []pgtype.UUID,
	attachmentsNative bool,
	workflowNeedsGo bool,
) bool {
	if creatorType != "member" {
		return false
	}
	if originType.Valid || req.OriginType != nil || req.OriginID != nil {
		return false
	}
	if len(attachmentIDs) > 0 && !attachmentsNative {
		return false
	}
	if req.Stage != nil {
		return false
	}
	if workflowNeedsGo {
		return false
	}
	if !isActiveBuiltInIssueStatus(status) {
		return false
	}
	if assigneeType.Valid && assigneeType.String != "member" {
		return false
	}
	return true
}

// nativeCreateDirectAgentOwnerOnly reports whether the acting member is the
// agent's owner — the only direct-agent shape the native create pilot writes.
func (h *Handler) nativeCreateDirectAgentOwnerOnly(
	ctx context.Context,
	wsUUID, assigneeID pgtype.UUID,
	memberUserID string,
) bool {
	agent, err := h.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
		ID: assigneeID, WorkspaceID: wsUUID,
	})
	if err != nil || !agent.OwnerID.Valid {
		return false
	}
	return uuidToString(agent.OwnerID) == memberUserID
}

// planIssueCreateRust chooses at most one native create plan per request.
// Direct-agent shapes need CREATE and CREATE_DISPATCH pilots; owned-squad
// shapes need CREATE and CREATE_SQUAD pilots, with leader ownership re-checked
// at selection time.
func (h *Handler) planIssueCreateRust(
	r *http.Request,
	wsUUID pgtype.UUID,
	req CreateIssueRequest,
	creatorType, actualCreatorID, priority string,
	assigneeType pgtype.Text,
	assigneeID pgtype.UUID,
	status string,
	originType pgtype.Text,
	attachmentIDs []pgtype.UUID,
	workflowNeedsGo bool,
	projectID pgtype.UUID,
) (issueCreateRustPlan, error) {
	plan := issueCreateRustPlan{}
	if h.IssueReadBridge == nil || len(h.IssueReadBridge.CreatePilotScopes) == 0 {
		return plan, nil
	}
	inCreatePilot, err := matchesIssuePilot(r.Context(), h.Queries, h.IssueReadBridge.CreatePilotScopes, uuidToString(wsUUID), wsUUID)
	if err != nil || !inCreatePilot {
		return plan, err
	}

	if len(attachmentIDs) > 0 && assigneeType.Valid && assigneeType.String != "member" {
		return plan, nil
	}

	if assigneeType.Valid && assigneeType.String == "agent" {
		if len(h.IssueReadBridge.CreateDispatchPilotScopes) == 0 {
			return plan, nil
		}
		inDispatchPilot, err := matchesIssuePilot(r.Context(), h.Queries, h.IssueReadBridge.CreateDispatchPilotScopes, uuidToString(wsUUID), wsUUID)
		if err != nil || !inDispatchPilot {
			return plan, err
		}
		if !h.plainIssueCreateRustEligible(req, creatorType, pgtype.Text{}, status, originType, attachmentIDs, false, workflowNeedsGo) {
			return plan, nil
		}
		if !wsUUID.Valid || actualCreatorID == "" || !assigneeID.Valid || !h.nativeCreateDirectAgentOwnerOnly(r.Context(), wsUUID, assigneeID, actualCreatorID) {
			return plan, nil
		}
		plan.issueID = dbid.NewV7()
		plan.useRust = true
		active := status != issuestatus.Backlog &&
			issuestatus.Effective(r.Context(), h.Queries, wsUUID, status) != issuestatus.Backlog
		if !active {
			return plan, nil
		}
		projected := projectedIssueForCreate(
			plan.issueID, wsUUID, req, status, priority, assigneeType, assigneeID, projectID,
		)
		_, admitted := service.AgentAssigneeVerdict(r.Context(), h.runtimeLookup(uuidToString(wsUUID)), projected)
		if !admitted {
			plan.useRust = false
			return plan, nil
		}
		prep, err := h.TaskService.PrepareIssueAgentEnqueue(
			r.Context(), projected, "", memberActorUserID(creatorType, actualCreatorID),
		)
		if err != nil {
			plan.useRust = false
			return plan, nil
		}
		plan.dispatchPrep = &prep
		return plan, nil
	}

	if assigneeType.Valid && assigneeType.String == "squad" {
		if len(h.IssueReadBridge.CreateSquadPilotScopes) == 0 {
			return plan, nil
		}
		inSquadPilot, err := matchesIssuePilot(r.Context(), h.Queries, h.IssueReadBridge.CreateSquadPilotScopes, uuidToString(wsUUID), wsUUID)
		if err != nil || !inSquadPilot {
			return plan, err
		}
		if !h.plainIssueCreateRustEligible(req, creatorType, pgtype.Text{}, status, originType, attachmentIDs, false, workflowNeedsGo) {
			return plan, nil
		}
		if !wsUUID.Valid || actualCreatorID == "" || !assigneeID.Valid {
			return plan, nil
		}
		active := status != issuestatus.Backlog &&
			issuestatus.Effective(r.Context(), h.Queries, wsUUID, status) != issuestatus.Backlog
		squad, ok := h.loadOwnedSquadForIssueCreate(r.Context(), wsUUID, assigneeID, actualCreatorID, active)
		if !ok {
			return plan, nil
		}
		plan.issueID = dbid.NewV7()
		plan.useRust = true
		plan.squadTrust = &rustSquadCreateTrust{
			SquadID:          uuidToString(assigneeID),
			ExpectedLeaderID: uuidToString(squad.LeaderID),
		}
		if !active {
			return plan, nil
		}
		projected := projectedIssueForCreate(
			plan.issueID, wsUUID, req, status, priority, assigneeType, assigneeID, projectID,
		)
		prep, err := h.TaskService.PrepareIssueSquadLeaderEnqueue(
			r.Context(), projected, squad, "", memberActorUserID(creatorType, actualCreatorID),
		)
		if err != nil {
			plan.useRust = false
			return plan, nil
		}
		plan.dispatchPrep = &prep
		return plan, nil
	}

	attachmentsNative := false
	if len(attachmentIDs) > 0 {
		if len(h.IssueReadBridge.CreateAttachPilotScopes) == 0 {
			return plan, nil
		}
		inAttachmentsPilot, err := matchesIssuePilot(
			r.Context(), h.Queries, h.IssueReadBridge.CreateAttachPilotScopes, uuidToString(wsUUID), wsUUID,
		)
		if err != nil || !inAttachmentsPilot {
			return plan, err
		}
		ok, err := h.issueCreateAttachmentsPreflight(r.Context(), wsUUID, attachmentIDs)
		if err != nil {
			return plan, err
		}
		if !ok {
			return plan, nil
		}
		attachmentsNative = true
		plan.nativeAttachmentIDs = canonicalAttachmentIDStrings(attachmentIDs)
	}

	if !h.plainIssueCreateRustEligible(req, creatorType, assigneeType, status, originType, attachmentIDs, attachmentsNative, workflowNeedsGo) {
		return plan, nil
	}
	plan.useRust = true
	plan.issueID = dbid.NewV7()
	return plan, nil
}

func (h *Handler) createIssueViaRust(
	w http.ResponseWriter,
	r *http.Request,
	workspaceID string,
	wsUUID pgtype.UUID,
	userID, creatorType, actualCreatorID string,
	req CreateIssueRequest,
	status, priority string,
	assigneeType pgtype.Text,
	assigneeID pgtype.UUID,
	parentIssueID, projectID pgtype.UUID,
	startDate, dueDate pgtype.Date,
	issueTypeID pgtype.UUID,
	properties map[pgtype.UUID]json.RawMessage,
	labelIDs []pgtype.UUID,
	fillCreated func(*IssueResponse),
	plan issueCreateRustPlan,
) {
	if !plan.useRust {
		writeError(w, http.StatusInternalServerError, "failed to create issue")
		return
	}
	issueID := plan.issueID
	dispatchPrepared := plan.dispatchPrep

	prepared, err := h.IssueService.PrepareCreateProperties(r.Context(), wsUUID, issueTypeID, properties)
	if err != nil {
		if writeIssueCreatePropertyError(w, err) {
			return
		}
		if errors.Is(err, service.ErrIssueTypeNotFound) || errors.Is(err, service.ErrIssueTypeArchived) {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"code":  "invalid_issue_type",
				"error": err.Error(),
			})
			return
		}
		slog.Warn("prepare create properties failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to create issue")
		return
	}
	workMethod, err := service.ReadyWorkMethodJSON(r.Context(), h.Queries, wsUUID, status, req.Title, ptrToTextOrEmpty(req.Description), "")
	if err != nil {
		slog.Warn("prepare create work method failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to create issue")
		return
	}
	policy := service.ResolveIssueCountPolicy(r.Context(), h.Entitlements, wsUUID)

	payload := rustIssueCreate{
		ID:                     uuidToString(issueID),
		Title:                  req.Title,
		Status:                 status,
		NormalizedTitle:        issueguard.NormalizeTitle(req.Title),
		Priority:               priority,
		CreatorType:            creatorType,
		CreatorID:              actualCreatorID,
		Properties:             prepared.Values,
		PropertyVersions:       prepared.PropertyVersions,
		IssueTypeVersion:       prepared.TypeVersion,
		AllowDuplicate:         req.AllowDuplicate,
		PRAutoCompleteDisabled: req.PRAutoCompleteDisabled,
		IssueLimit: rustIssueCreateLimit{
			Enforce:        policy.Action == entitlement.ActionEnforce,
			Limit:          policy.Limit,
			PolicyRevision: policy.PolicyRevision,
		},
	}
	if req.Description != nil {
		payload.Description = req.Description
	}
	if assigneeType.Valid {
		payload.AssigneeType = &assigneeType.String
		payload.AssigneeID = uuidToPtr(assigneeID)
	}
	if parentIssueID.Valid {
		payload.ParentIssueID = uuidToPtr(parentIssueID)
	}
	if projectID.Valid {
		payload.ProjectID = uuidToPtr(projectID)
	}
	payload.StartDate = dateToPtr(startDate)
	payload.DueDate = dateToPtr(dueDate)
	if issueTypeID.Valid {
		payload.IssueTypeID = uuidToPtr(issueTypeID)
	}
	for _, id := range labelIDs {
		if id.Valid {
			payload.LabelIDs = append(payload.LabelIDs, uuidToString(id))
		}
	}
	if len(workMethod) > 0 {
		payload.WorkMethodRun = workMethod
	}
	if dispatchPrepared != nil {
		raw, err := marshalIssueStatusPreparedEnqueue(*dispatchPrepared)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to encode issue create enqueue")
			return
		}
		rawMsg := json.RawMessage(raw)
		payload.PreparedEnqueue = &rawMsg
	}
	if plan.squadTrust != nil {
		payload.SquadCreateTrust = plan.squadTrust
	}
	if len(plan.nativeAttachmentIDs) > 0 {
		payload.AttachmentIDs = append([]string(nil), plan.nativeAttachmentIDs...)
	}

	body, err := json.Marshal(payload)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode issue create")
		return
	}
	resp, requested := h.requestRustIssue(w, r, http.MethodPost, "/internal/issues/create", workspaceID, userID, body)
	if !requested {
		return
	}
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, maxRustIssueReadBody+1))
	if err != nil || int64(len(bodyBytes)) > maxRustIssueReadBody {
		if !writeQueryCancellation(w, r, err) {
			writeError(w, http.StatusBadGateway, "failed to read rust issue create response")
		}
		return
	}
	if resp.StatusCode == http.StatusConflict {
		var changed struct {
			Code string `json:"code"`
		}
		if json.Unmarshal(bodyBytes, &changed) == nil && changed.Code == "issue_create_configuration_changed" {
			writeJSON(w, http.StatusConflict, changed)
			return
		}
		var conflict struct {
			Code  string        `json:"code"`
			Error string        `json:"error"`
			Issue IssueResponse `json:"issue"`
		}
		if json.Unmarshal(bodyBytes, &conflict) == nil && conflict.Code == "active_duplicate_issue" {
			conflict.Issue.Labels = nil
			fillCreated(&conflict.Issue)
			writeJSON(w, http.StatusConflict, conflict)
			return
		}
		writeRustIssueResponse(w, resp, bytes.NewReader(bodyBytes))
		return
	}
	if resp.StatusCode != http.StatusCreated {
		writeRustIssueResponse(w, resp, bytes.NewReader(bodyBytes))
		return
	}
	var receipt rustIssueCreatePayload
	if json.Unmarshal(bodyBytes, &receipt) != nil || !rustIssueCreateReceiptValid(issueID, wsUUID, receipt) {
		writeError(w, http.StatusBadGateway, "invalid rust issue create response")
		return
	}
	if len(plan.nativeAttachmentIDs) > 0 {
		if receipt.LinkedAttachmentIDs == nil {
			writeError(w, http.StatusBadGateway, "invalid rust issue create attachment receipt")
			return
		}
		linkedReceipt := *receipt.LinkedAttachmentIDs
		if !rustIssueCreateLinkedAttachmentsValid(plan.nativeAttachmentIDs, linkedReceipt) {
			writeError(w, http.StatusBadGateway, "invalid rust issue create attachment receipt")
			return
		}
	} else if receipt.LinkedAttachmentIDs != nil {
		writeError(w, http.StatusBadGateway, "unexpected rust issue create attachment receipt")
		return
	}
	taskReceipt := derefString(receipt.QueuedTaskID)
	coalescedReceipt := derefString(receipt.CoalescedQueuedTaskID)
	if dispatchPrepared != nil {
		switch {
		case taskReceipt != "" && coalescedReceipt != "":
			writeError(w, http.StatusBadGateway, "invalid rust issue create queue receipt")
			return
		case taskReceipt != "":
			if !rustStatusQueuedTaskValid(r.Context(), h.Queries, taskReceipt, *dispatchPrepared) {
				writeError(w, http.StatusBadGateway, "invalid rust issue create queue receipt")
				return
			}
		case coalescedReceipt != "":
			if !rustStatusCoalescedTaskValid(r.Context(), h.Queries, coalescedReceipt, *dispatchPrepared) {
				writeError(w, http.StatusBadGateway, "invalid rust issue create queue receipt")
				return
			}
		default:
			writeError(w, http.StatusBadGateway, "invalid rust issue create queue receipt")
			return
		}
	} else if taskReceipt != "" || coalescedReceipt != "" {
		writeError(w, http.StatusBadGateway, "unexpected rust issue create queue receipt")
		return
	}

	issue := receipt.Issue
	slog.Info("issue created", append(logger.RequestAttrs(r), "issue_id", issue.ID, "title", issue.Title, "status", issue.Status, "workspace_id", workspaceID)...)

	var linkedAttachments []db.Attachment
	if len(plan.nativeAttachmentIDs) > 0 {
		var err error
		linkedAttachments, err = h.Queries.ListAttachmentsByIssue(r.Context(), db.ListAttachmentsByIssueParams{
			IssueID: issueID, WorkspaceID: wsUUID,
		})
		if err != nil {
			slog.Warn("list native create attachments failed", append(logger.RequestAttrs(r), "error", err)...)
			writeError(w, http.StatusBadGateway, "failed to resolve created issue attachments")
			return
		}
		if !attachmentRowsMatchLinkedIDs(linkedAttachments, *receipt.LinkedAttachmentIDs) {
			writeError(w, http.StatusBadGateway, "invalid rust issue create attachment receipt")
			return
		}
	}

	attachmentMode := attachmentURLModeFromRequest(r)
	buildAttachmentResponses := func(atts []db.Attachment) []AttachmentResponse {
		if len(atts) == 0 {
			return nil
		}
		out := make([]AttachmentResponse, len(atts))
		for i, a := range atts {
			out[i] = h.attachmentToResponse(a, attachmentMode)
		}
		return out
	}

	respIssue := issue
	fillCreated(&respIssue)
	respIssue.Attachments = buildAttachmentResponses(linkedAttachments)
	labelResponses := receipt.Labels
	if labelResponses == nil {
		labelResponses = []LabelResponse{}
	}
	respIssue.Labels = &labelResponses
	if req.PRAutoCompleteDisabled {
		h.recordPRAutoCompleteActivity(r.Context(), db.Issue{
			ID: issueID, WorkspaceID: wsUUID, Title: issue.Title,
		}, creatorType, actualCreatorID, true)
	}

	platform, _, _ := middleware.ClientMetadataFromContext(r.Context())
	h.IssueService.CapturePlainCreateAnalytics(db.Issue{
		ID: issueID, WorkspaceID: wsUUID, Title: issue.Title, Status: issue.Status, Number: issue.Number,
		AssigneeType: assigneeType, AssigneeID: assigneeID,
	}, creatorType, actualCreatorID, platform)
	h.publish(protocol.EventIssueCreated, workspaceID, creatorType, actualCreatorID, map[string]any{
		"issue": respIssue,
	})
	if taskReceipt != "" {
		if taskID, err := util.ParseUUID(taskReceipt); err == nil {
			h.TaskService.CompleteIssueStatusPromotionAfterNativeWrite(r.Context(), taskID)
		}
	}
	writeJSON(w, http.StatusCreated, CreateIssueResponse{IssueResponse: respIssue, PRAutoCompleteDisabled: req.PRAutoCompleteDisabled})
}

func rustIssueCreateReceiptValid(plannedID pgtype.UUID, workspaceID pgtype.UUID, receipt rustIssueCreatePayload) bool {
	if receipt.Issue.ID != uuidToString(plannedID) {
		return false
	}
	if receipt.Issue.WorkspaceID != uuidToString(workspaceID) {
		return false
	}
	if receipt.Issue.Title == "" || receipt.Issue.Status == "" || receipt.Issue.Number <= 0 {
		return false
	}
	if receipt.Issue.Revision < 1 {
		return false
	}
	return true
}
