package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/entitlement"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/issueworkflow"
	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/projectaccess"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type rustSourceContextCreateTrust struct {
	ContextID               string                        `json:"context_id"`
	SourceIssueID           string                        `json:"source_issue_id"`
	AnchorCommentID         string                        `json:"anchor_comment_id"`
	CaptureDigest           string                        `json:"capture_digest"`
	CapturedByUserID        string                        `json:"captured_by_user_id"`
	CapturedAt              string                        `json:"captured_at"`
	Snapshot                json.RawMessage               `json:"snapshot"`
	SourceCompareSnapshot   json.RawMessage               `json:"source_compare_snapshot,omitempty"`
	ExpectedSourceProjectID *string                       `json:"expected_source_project_id,omitempty"`
	Clones                  []rustSourceContextCloneTrust `json:"clones,omitempty"`
}

type rustSourceContextCloneTrust struct {
	CloneID            string `json:"clone_id"`
	SourceAttachmentID string `json:"source_attachment_id"`
	Filename           string `json:"filename"`
	URL                string `json:"url"`
	ContentType        string `json:"content_type"`
	SizeBytes          int64  `json:"size_bytes"`
	StorageKey         string `json:"storage_key"`
}

type manualSourceContextCreateRustPlan struct {
	useRust                 bool
	issueID                 pgtype.UUID
	expectedSourceProjectID pgtype.UUID
}

func rustSourceContextCreateTrustFromCapture(
	wsUUID pgtype.UUID,
	capture service.SourceContextCapture,
	build service.SourceContextBuild,
) (rustSourceContextCreateTrust, error) {
	snapshotJSON, err := json.Marshal(capture.Snapshot)
	if err != nil {
		return rustSourceContextCreateTrust{}, err
	}
	trust := rustSourceContextCreateTrust{
		ContextID:        util.UUIDToString(capture.ID),
		SourceIssueID:    util.UUIDToString(capture.SourceIssueID),
		AnchorCommentID:  util.UUIDToString(capture.AnchorCommentID),
		CaptureDigest:    capture.Digest,
		CapturedByUserID: util.UUIDToString(capture.CapturedByUserID),
		CapturedAt:       capture.CapturedAt.UTC().Format(time.RFC3339Nano),
		Snapshot:         snapshotJSON,
	}
	if len(capture.Clones) == 0 {
		return trust, nil
	}
	compareSnapshotJSON, err := json.Marshal(build.Snapshot)
	if err != nil {
		return rustSourceContextCreateTrust{}, err
	}
	if !rustSourceContextClonePlanMatchesCapture(build, capture) {
		return rustSourceContextCreateTrust{}, fmt.Errorf("source context clone plan mismatch")
	}
	trust.SourceCompareSnapshot = compareSnapshotJSON
	workspaceKey := util.UUIDToString(wsUUID)
	trust.Clones = make([]rustSourceContextCloneTrust, len(capture.Clones))
	for i, clone := range capture.Clones {
		row := build.Rows[i]
		sourceID := util.UUIDToString(row.ID)
		if sourceID == "" {
			return rustSourceContextCreateTrust{}, fmt.Errorf("invalid source context clone plan")
		}
		cloneID := util.UUIDToString(clone.ID)
		trust.Clones[i] = rustSourceContextCloneTrust{
			CloneID:            cloneID,
			SourceAttachmentID: sourceID,
			Filename:           clone.Filename,
			URL:                clone.URL,
			ContentType:        clone.ContentType,
			SizeBytes:          clone.SizeBytes,
			StorageKey: fmt.Sprintf(
				"workspaces/%s/source-context/%s%s",
				workspaceKey, cloneID, path.Ext(clone.Filename),
			),
		}
	}
	return trust, nil
}

func rustSourceContextClonePlanMatchesCapture(build service.SourceContextBuild, capture service.SourceContextCapture) bool {
	if len(build.Rows) != len(capture.Clones) || len(build.Rows) == 0 {
		return false
	}
	frozenBySource := sourceContextAttachmentsByOriginalID(capture.Snapshot, true)
	compareBySource := sourceContextAttachmentsByOriginalID(build.Snapshot, false)
	if len(frozenBySource) != len(build.Rows) || len(compareBySource) != len(build.Rows) {
		return false
	}
	for i, row := range build.Rows {
		sourceID := util.UUIDToString(row.ID)
		clone := capture.Clones[i]
		frozen, frozenOK := frozenBySource[sourceID]
		compare, compareOK := compareBySource[sourceID]
		if !frozenOK || !compareOK || frozen.ID != util.UUIDToString(clone.ID) ||
			frozen.Filename != clone.Filename || frozen.ContentType != clone.ContentType || frozen.SizeBytes != clone.SizeBytes ||
			compare.Filename != row.Filename || compare.ContentType != row.ContentType || compare.SizeBytes != row.SizeBytes ||
			compare.OwnerType != frozen.OwnerType || compare.OwnerID != frozen.OwnerID || compare.CreatedAt != frozen.CreatedAt {
			return false
		}
		delete(frozenBySource, sourceID)
	}
	return true
}

func sourceContextAttachmentsByOriginalID(snapshot service.SourceContextSnapshot, frozen bool) map[string]service.SourceContextAttachment {
	out := make(map[string]service.SourceContextAttachment)
	record := func(items []service.SourceContextAttachment) bool {
		for _, item := range items {
			sourceID := item.ID
			if frozen {
				sourceID = item.SourceAttachmentID
			}
			if sourceID == "" || item.ID == "" {
				return false
			}
			if _, exists := out[sourceID]; exists {
				return false
			}
			out[sourceID] = item
		}
		return true
	}
	if !record(snapshot.SourceIssue.Attachments) {
		return nil
	}
	for _, comment := range snapshot.CommentThread {
		if !record(comment.Attachments) {
			return nil
		}
	}
	return out
}

func snapshotHasSourceContextAttachments(snapshot service.SourceContextSnapshot) bool {
	if len(snapshot.SourceIssue.Attachments) > 0 {
		return true
	}
	for _, comment := range snapshot.CommentThread {
		if len(comment.Attachments) > 0 {
			return true
		}
	}
	return false
}

func (h *Handler) manualSourceContextCreateRustEligible(
	r *http.Request,
	wsUUID pgtype.UUID,
	input CreateIssueRequest,
	capture service.SourceContextCapture,
	build service.SourceContextBuild,
	status string,
) (manualSourceContextCreateRustPlan, error) {
	plan := manualSourceContextCreateRustPlan{}
	if h.IssueReadBridge == nil || len(h.IssueReadBridge.CreatePilotScopes) == 0 ||
		len(h.IssueReadBridge.CreateSourceContextPilotScopes) == 0 {
		return plan, nil
	}
	inCreatePilot, err := matchesIssuePilot(r.Context(), h.Queries, h.IssueReadBridge.CreatePilotScopes, uuidToString(wsUUID), wsUUID)
	if err != nil || !inCreatePilot {
		return plan, err
	}
	inSourceContextPilot, err := matchesIssuePilot(r.Context(), h.Queries, h.IssueReadBridge.CreateSourceContextPilotScopes, uuidToString(wsUUID), wsUUID)
	if err != nil || !inSourceContextPilot {
		return plan, err
	}
	hasSourceFiles := len(build.Rows) > 0 || len(capture.Clones) > 0 || snapshotHasSourceContextAttachments(capture.Snapshot)
	if hasSourceFiles {
		if len(h.IssueReadBridge.CreateSourceFilesPilotScopes) == 0 {
			return plan, nil
		}
		inSourceFilesPilot, pilotErr := matchesIssuePilot(r.Context(), h.Queries, h.IssueReadBridge.CreateSourceFilesPilotScopes, uuidToString(wsUUID), wsUUID)
		if pilotErr != nil || !inSourceFilesPilot {
			return plan, pilotErr
		}
		if len(build.Rows) != len(capture.Clones) || len(build.Rows) == 0 {
			return plan, nil
		}
		if !rustSourceContextClonePlanMatchesCapture(build, capture) {
			return plan, nil
		}
	} else if len(build.Rows) > 0 || len(capture.Clones) > 0 {
		return plan, nil
	}
	if status != issuestatus.Todo {
		return plan, nil
	}
	if input.AssigneeType != nil && strings.TrimSpace(*input.AssigneeType) != "" {
		return plan, nil
	}
	if input.AssigneeID != nil && strings.TrimSpace(*input.AssigneeID) != "" {
		return plan, nil
	}
	if input.ProjectID != nil && strings.TrimSpace(*input.ProjectID) != "" {
		return plan, nil
	}
	if input.Stage != nil || len(input.AttachmentIDs) > 0 {
		return plan, nil
	}
	if !h.plainIssueCreateRustEligible(input, "member", pgtype.Text{}, status, pgtype.Text{}, nil, false, false) {
		return plan, nil
	}
	sourceIssue, err := h.Queries.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{
		ID: capture.SourceIssueID, WorkspaceID: wsUUID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return plan, nil
		}
		return plan, err
	}
	if sourceIssue.ProjectID.Valid {
		if len(h.IssueReadBridge.CreateSourceProjectPilotScopes) == 0 {
			return plan, nil
		}
		inSourceProjectPilot, pilotErr := matchesIssuePilot(
			r.Context(), h.Queries, h.IssueReadBridge.CreateSourceProjectPilotScopes, uuidToString(wsUUID), wsUUID,
		)
		if pilotErr != nil || !inSourceProjectPilot {
			return plan, pilotErr
		}
		hidden, hiddenErr := h.hiddenProjects(r, uuidToString(wsUUID))
		if hiddenErr != nil {
			return plan, hiddenErr
		}
		if !hidden.CanSeeProject(sourceIssue.ProjectID) {
			return plan, service.ErrAnchorCommentDeleted
		}
		workflow, project, err := issueworkflow.ForProject(r.Context(), h.Queries, wsUUID, sourceIssue.ProjectID)
		if err != nil || project.WorkflowID.Valid {
			return plan, nil
		}
		if workflow != nil {
			step, exists := workflow.Step(status)
			if !exists || step.HandsOff() {
				return plan, nil
			}
		}
		plan.expectedSourceProjectID = sourceIssue.ProjectID
	}
	plan.useRust = true
	plan.issueID = dbid.NewV7()
	return plan, nil
}

func (h *Handler) createManualCommentSubIssueViaRust(
	w http.ResponseWriter,
	r *http.Request,
	workspaceID string,
	wsUUID, userUUID pgtype.UUID,
	input CreateIssueRequest,
	capture service.SourceContextCapture,
	build service.SourceContextBuild,
	limits service.SourceContextLimitUsage,
	status, priority string,
	labelIDs []pgtype.UUID,
	properties map[pgtype.UUID]json.RawMessage,
	issueTypeID pgtype.UUID,
	startDate, dueDate pgtype.Date,
	plan manualSourceContextCreateRustPlan,
) error {
	if !plan.useRust {
		return errors.New("manual source context rust plan inactive")
	}
	issueID := plan.issueID
	memberID := util.UUIDToString(userUUID)

	prepared, err := h.IssueService.PrepareCreateProperties(r.Context(), wsUUID, issueTypeID, properties)
	if err != nil {
		if writeIssueCreatePropertyError(w, err) {
			return errSourceContextResponseWritten
		}
		if errors.Is(err, service.ErrIssueTypeNotFound) || errors.Is(err, service.ErrIssueTypeArchived) {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"code":  "invalid_issue_type",
				"error": err.Error(),
			})
			return errSourceContextResponseWritten
		}
		slog.Warn("prepare source context create properties failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to create issue")
		return errSourceContextResponseWritten
	}
	workMethod, err := service.ReadyWorkMethodJSON(r.Context(), h.Queries, wsUUID, status, input.Title, ptrToTextOrEmpty(input.Description), "")
	if err != nil {
		slog.Warn("prepare source context create work method failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to create issue")
		return errSourceContextResponseWritten
	}
	policy := service.ResolveIssueCountPolicy(r.Context(), h.Entitlements, wsUUID)
	contextTrust, err := rustSourceContextCreateTrustFromCapture(wsUUID, capture, build)
	if err != nil {
		writeError(w, http.StatusBadGateway, "invalid source context clone plan")
		return errSourceContextResponseWritten
	}

	payload := assembleRustIssueCreate(
		issueID, input, "member", memberID, status, priority,
		capture.SourceIssueID, pgtype.UUID{}, issueTypeID,
		startDate, dueDate, labelIDs,
		prepared.Values, prepared.PropertyVersions, prepared.TypeVersion,
		workMethod, rustIssueCreateLimit{
			Enforce:        policy.Action == entitlement.ActionEnforce,
			Limit:          policy.Limit,
			PolicyRevision: policy.PolicyRevision,
		},
	)
	if plan.expectedSourceProjectID.Valid {
		projectID := uuidToString(plan.expectedSourceProjectID)
		contextTrust.ExpectedSourceProjectID = &projectID
	}
	payload.SourceContextCreateTrust = &contextTrust

	body, err := json.Marshal(payload)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode issue create")
		return errSourceContextResponseWritten
	}
	resp, requested := h.requestRustIssue(w, r, http.MethodPost, "/internal/issues/create", workspaceID, memberID, body)
	if !requested {
		return errSourceContextResponseWritten
	}
	defer resp.Body.Close()
	bodyBytes, ok := readRustIssueCreateResponseBody(w, r, resp)
	if !ok {
		return errSourceContextResponseWritten
	}
	if resp.StatusCode == http.StatusConflict {
		if writeRustIssueCreateConflictForSourceContext(w, bodyBytes) {
			return errSourceContextResponseWritten
		}
		writeRustIssueResponse(w, resp, bytes.NewReader(bodyBytes))
		return errSourceContextResponseWritten
	}
	if resp.StatusCode != http.StatusCreated {
		writeRustIssueResponse(w, resp, bytes.NewReader(bodyBytes))
		return errSourceContextResponseWritten
	}
	var receipt rustIssueCreatePayload
	if json.Unmarshal(bodyBytes, &receipt) != nil || !rustIssueCreateReceiptValid(issueID, wsUUID, receipt) {
		writeError(w, http.StatusBadGateway, "invalid rust issue create response")
		return errSourceContextResponseWritten
	}
	respIssue := receipt.Issue
	projectOK := !plan.expectedSourceProjectID.Valid && respIssue.ProjectID == nil
	if plan.expectedSourceProjectID.Valid {
		projectOK = derefString(respIssue.ProjectID) == uuidToString(plan.expectedSourceProjectID)
	}
	if derefString(respIssue.ParentIssueID) != uuidToString(capture.SourceIssueID) || !projectOK ||
		respIssue.CreatorType != "member" || respIssue.CreatorID != memberID ||
		respIssue.Title != payload.Title || respIssue.Status != status || respIssue.Priority != priority ||
		respIssue.AssigneeType != nil || respIssue.AssigneeID != nil ||
		receipt.LinkedAttachmentIDs != nil || receipt.QueuedTaskID != nil || receipt.CoalescedQueuedTaskID != nil {
		writeError(w, http.StatusBadGateway, "invalid rust issue create response")
		return errSourceContextResponseWritten
	}
	if !rustIssueCreateSourceContextReceiptValid(r.Context(), h.Queries, wsUUID, issueID, capture, plan.expectedSourceProjectID) {
		writeError(w, http.StatusBadGateway, "invalid rust source context create response")
		return errSourceContextResponseWritten
	}

	h.fillStatusCategory(r.Context(), wsUUID, projectaccess.Hidden{}, &respIssue)
	labelResponses := receipt.Labels
	if labelResponses == nil {
		labelResponses = []LabelResponse{}
	}
	respIssue.Labels = &labelResponses
	if input.PRAutoCompleteDisabled {
		h.recordPRAutoCompleteActivity(r.Context(), db.Issue{
			ID: issueID, WorkspaceID: wsUUID, Title: respIssue.Title,
		}, "member", memberID, true)
	}
	slog.Info("sub-issue created from captured comment", append(sourceContextAuditAttrs(r, capture.ID, capture.Snapshot.SourceIssue.ID, capture.Snapshot.AnchorCommentID, userUUID, "manual", limits, "success"), "target_issue_id", uuidToString(issueID))...)
	h.publish(protocol.EventIssueCreated, workspaceID, "member", memberID, map[string]any{
		"issue": respIssue,
	})
	writeJSON(w, http.StatusCreated, CreateIssueResponse{IssueResponse: respIssue, PRAutoCompleteDisabled: input.PRAutoCompleteDisabled})
	return nil
}

func rustIssueCreateSourceContextReceiptValid(
	ctx context.Context,
	queries *db.Queries,
	wsUUID, issueID pgtype.UUID,
	capture service.SourceContextCapture,
	expectedProjectID pgtype.UUID,
) bool {
	issue, err := queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{
		ID: issueID, WorkspaceID: wsUUID,
	})
	if err != nil {
		return false
	}
	if expectedProjectID.Valid {
		if issue.ProjectID != expectedProjectID {
			return false
		}
	} else if issue.ProjectID.Valid {
		return false
	}
	row, err := queries.GetIssueSourceContextByIssue(ctx, db.GetIssueSourceContextByIssueParams{
		WorkspaceID: wsUUID,
		IssueID:     issueID,
	})
	if err != nil {
		return false
	}
	if row.ID != capture.ID ||
		row.SourceIssueID != capture.SourceIssueID ||
		row.AnchorCommentID != capture.AnchorCommentID ||
		row.CapturedByUserID != capture.CapturedByUserID ||
		row.CaptureDigest != capture.Digest {
		return false
	}
	if len(capture.Clones) == 0 {
		return true
	}
	attachments, err := queries.ListAttachmentsBySourceContext(ctx, db.ListAttachmentsBySourceContextParams{
		WorkspaceID: wsUUID, SourceContextID: capture.ID,
	})
	if err != nil || len(attachments) != len(capture.Clones) {
		return false
	}
	byCloneID := make(map[string]db.Attachment, len(attachments))
	for _, attachment := range attachments {
		byCloneID[util.UUIDToString(attachment.ID)] = attachment
	}
	for _, clone := range capture.Clones {
		stored, ok := byCloneID[util.UUIDToString(clone.ID)]
		if !ok || stored.Filename != clone.Filename || stored.ContentType != clone.ContentType ||
			stored.SizeBytes != clone.SizeBytes || stored.Url != clone.URL ||
			stored.UploaderType != "member" || stored.UploaderID != capture.CapturedByUserID {
			return false
		}
	}
	intentCount, err := queries.CountSourceContextObjectIntents(ctx, db.CountSourceContextObjectIntentsParams{
		WorkspaceID: wsUUID, SourceContextID: capture.ID,
	})
	if err != nil || intentCount != 0 {
		return false
	}
	return true
}
