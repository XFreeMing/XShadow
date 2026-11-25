package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/entitlement"
	"github.com/multica-ai/multica/server/internal/issueguard"
	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/service"
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
	ID                     string               `json:"id"`
	Title                  string               `json:"title"`
	Description            *string              `json:"description,omitempty"`
	Status                 string               `json:"status"`
	NormalizedTitle        string               `json:"normalized_title"`
	Priority               string               `json:"priority"`
	AssigneeType           *string              `json:"assignee_type,omitempty"`
	AssigneeID             *string              `json:"assignee_id,omitempty"`
	CreatorType            string               `json:"creator_type"`
	CreatorID              string               `json:"creator_id"`
	ParentIssueID          *string              `json:"parent_issue_id,omitempty"`
	ProjectID              *string              `json:"project_id,omitempty"`
	StartDate              *string              `json:"start_date,omitempty"`
	DueDate                *string              `json:"due_date,omitempty"`
	LabelIDs               []string             `json:"label_ids,omitempty"`
	Properties             json.RawMessage      `json:"properties"`
	PropertyVersions       map[string]string    `json:"property_versions"`
	IssueTypeID            *string              `json:"issue_type_id,omitempty"`
	IssueTypeVersion       string               `json:"issue_type_version,omitempty"`
	AllowDuplicate         bool                 `json:"allow_duplicate"`
	WorkMethodRun          json.RawMessage      `json:"work_method_run,omitempty"`
	IssueLimit             rustIssueCreateLimit `json:"issue_limit"`
	PRAutoCompleteDisabled bool                 `json:"pr_auto_complete_disabled,omitempty"`
}

type rustIssueCreatePayload struct {
	Issue  IssueResponse   `json:"issue"`
	Labels []LabelResponse `json:"labels"`
}

// createIssueUsesRust selects plain member-authored creates on built-in active
// statuses without attachments, origin stamping, workflow handoffs, or runtime
// assignees.
func (h *Handler) createIssueUsesRust(
	r *http.Request,
	workspaceID pgtype.UUID,
	req CreateIssueRequest,
	creatorType string,
	assigneeType pgtype.Text,
	status string,
	originType pgtype.Text,
	attachmentIDs []pgtype.UUID,
	workflowNeedsGo bool,
) (bool, error) {
	if h.IssueReadBridge == nil || len(h.IssueReadBridge.CreatePilotScopes) == 0 {
		return false, nil
	}
	if creatorType != "member" {
		return false, nil
	}
	if originType.Valid || req.OriginType != nil || req.OriginID != nil {
		return false, nil
	}
	if len(attachmentIDs) > 0 {
		return false, nil
	}
	if req.Stage != nil {
		return false, nil
	}
	if workflowNeedsGo {
		return false, nil
	}
	if !isActiveBuiltInIssueStatus(status) {
		return false, nil
	}
	if assigneeType.Valid && assigneeType.String != "member" {
		return false, nil
	}
	return matchesIssuePilot(r.Context(), h.Queries, h.IssueReadBridge.CreatePilotScopes, uuidToString(workspaceID), workspaceID)
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
) {
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

	issueID := dbid.NewV7()
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

	issue := receipt.Issue
	slog.Info("issue created", append(logger.RequestAttrs(r), "issue_id", issue.ID, "title", issue.Title, "status", issue.Status, "workspace_id", workspaceID)...)

	respIssue := issue
	fillCreated(&respIssue)
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
	}, creatorType, actualCreatorID, platform)
	h.publish(protocol.EventIssueCreated, workspaceID, creatorType, actualCreatorID, map[string]any{
		"issue": respIssue,
	})
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
