package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/issueproperty"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	rustIssuePropertyProjectPath = "/internal/issues/properties/project"
	rustIssuePropertyCompilePath = "/internal/issues/properties/compile-config"
	maxIssuePropertyProjectBatch = 200
)

type ProjectIssuePropertiesRequest struct {
	IssueIDs []string `json:"issue_ids"`
}

type ProjectIssuePropertiesResponse struct {
	Properties map[string]map[string]json.RawMessage `json:"properties"`
}

type rustIssuePropertyProjectRequest struct {
	IssueIDs []string `json:"issue_ids"`
}

type rustIssuePropertyProjectResponse struct {
	Properties map[string]map[string]json.RawMessage `json:"properties"`
}

type rustIssuePropertyCompileRequest struct {
	PropertyID *string         `json:"property_id,omitempty"`
	Kind       string          `json:"kind"`
	Config     json.RawMessage `json:"config"`
}

type rustIssuePropertyCompileResponse struct {
	Config json.RawMessage `json:"config"`
}

func (b *IssueReadBridge) ProjectTaskFormula(ctx context.Context, workspaceID, memberID, issueID pgtype.UUID, propertyID string) (json.RawMessage, error) {
	if !b.Enabled() {
		return nil, errors.New("table engine unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 750*time.Millisecond)
	defer cancel()
	issue, member := uuidToString(issueID), uuidToString(memberID)
	payload, err := json.Marshal(rustIssuePropertyProjectRequest{IssueIDs: []string{issue}})
	if err != nil {
		return nil, err
	}
	req, err := b.internalRequest(ctx, http.MethodPost, rustIssuePropertyProjectPath, uuidToString(workspaceID), member, "member", member, payload)
	if err != nil {
		return nil, err
	}
	resp, err := rustIssueReadClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("task formula projection was rejected")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRustIssueReadBody+1))
	if err != nil {
		return nil, err
	}
	var projection rustIssuePropertyProjectResponse
	if len(body) > maxRustIssueReadBody || json.Unmarshal(body, &projection) != nil {
		return nil, errors.New("invalid task formula projection")
	}
	return projection.Properties[issue][propertyID], nil
}

func (h *Handler) computedIssuePropertyIDs(ctx context.Context, wsUUID pgtype.UUID) (map[string]struct{}, error) {
	props, err := h.Queries.ListIssueProperties(ctx, db.ListIssuePropertiesParams{
		WorkspaceID:     wsUUID,
		IncludeArchived: false,
	})
	if err != nil {
		return nil, err
	}
	out := make(map[string]struct{})
	for _, property := range props {
		if issueproperty.IsComputed(property.Type) {
			out[uuidToString(property.ID)] = struct{}{}
		}
	}
	return out, nil
}

// ProjectIssueProperties returns read-only computed lookup/formula values for
// authorized, visible issues. Editable property bags are unaffected.
func (h *Handler) ProjectIssueProperties(w http.ResponseWriter, r *http.Request) {
	var req ProjectIssuePropertiesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.IssueIDs) == 0 {
		writeError(w, http.StatusBadRequest, "issue_ids is required")
		return
	}
	if len(req.IssueIDs) > maxIssuePropertyProjectBatch {
		writeError(w, http.StatusBadRequest, "too many issue ids")
		return
	}

	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}

	hidden, ok := h.requestHiddenProjects(w, r, workspaceID)
	if !ok {
		return
	}

	computedIDs, err := h.computedIssuePropertyIDs(r.Context(), wsUUID)
	if writeQueryCancellation(w, r, err) {
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list properties")
		return
	}
	if len(computedIDs) == 0 {
		writeJSON(w, http.StatusOK, ProjectIssuePropertiesResponse{
			Properties: map[string]map[string]json.RawMessage{},
		})
		return
	}

	seen := make(map[pgtype.UUID]struct{}, len(req.IssueIDs))
	ids := make([]pgtype.UUID, 0, len(req.IssueIDs))
	for _, raw := range req.IssueIDs {
		issueUUID, err := util.ParseUUID(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "issue_ids must contain UUIDs")
			return
		}
		if _, duplicate := seen[issueUUID]; duplicate {
			continue
		}
		seen[issueUUID] = struct{}{}
		ids = append(ids, issueUUID)
	}
	issues, err := h.Queries.ListIssueRefsInWorkspace(r.Context(), db.ListIssueRefsInWorkspaceParams{
		WorkspaceID: wsUUID, Ids: ids,
	})
	if writeQueryCancellation(w, r, err) {
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list issues")
		return
	}
	authorized := make([]string, 0, len(issues))
	allowed := make(map[string]struct{}, len(issues))
	for _, issue := range issues {
		if !hidden.CanSeeProject(issue.ProjectID) {
			continue
		}
		id := uuidToString(issue.ID)
		allowed[id] = struct{}{}
		authorized = append(authorized, id)
	}

	if len(authorized) == 0 {
		writeJSON(w, http.StatusOK, ProjectIssuePropertiesResponse{
			Properties: map[string]map[string]json.RawMessage{},
		})
		return
	}

	if h.IssueReadBridge == nil || !h.IssueReadBridge.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "derived property projection requires rust")
		return
	}

	body, err := json.Marshal(rustIssuePropertyProjectRequest{IssueIDs: authorized})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode property projection")
		return
	}
	resp, ok := h.requestRustIssue(w, r, http.MethodPost, rustIssuePropertyProjectPath, workspaceID, userID, body)
	if !ok {
		return
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxRustIssueReadBody+1))
	if err != nil || len(raw) > maxRustIssueReadBody || resp.StatusCode != http.StatusOK {
		if !writeQueryCancellation(w, r, err) {
			writeError(w, http.StatusBadGateway, "failed to read property projection")
		}
		return
	}
	var reply rustIssuePropertyProjectResponse
	if err := json.Unmarshal(raw, &reply); err != nil {
		writeError(w, http.StatusBadGateway, "invalid property projection response")
		return
	}
	if reply.Properties == nil {
		reply.Properties = map[string]map[string]json.RawMessage{}
	}

	filtered := filterProjectedComputedProperties(reply.Properties, allowed, computedIDs)
	writeJSON(w, http.StatusOK, ProjectIssuePropertiesResponse{Properties: filtered})
}

func filterProjectedComputedProperties(
	properties map[string]map[string]json.RawMessage,
	allowedIssues map[string]struct{},
	computedIDs map[string]struct{},
) map[string]map[string]json.RawMessage {
	out := make(map[string]map[string]json.RawMessage)
	for issueID, bag := range properties {
		if _, ok := allowedIssues[issueID]; !ok {
			continue
		}
		filtered := make(map[string]json.RawMessage)
		for propertyID, value := range bag {
			if _, ok := computedIDs[propertyID]; ok {
				filtered[propertyID] = value
			}
		}
		if len(filtered) > 0 {
			out[issueID] = filtered
		}
	}
	return out
}

func (h *Handler) compileIssuePropertyConfigViaRust(
	w http.ResponseWriter,
	r *http.Request,
	workspaceID, userID, kind string,
	propertyID *string,
	rawConfig json.RawMessage,
) (json.RawMessage, bool) {
	if h.IssueReadBridge == nil || !h.IssueReadBridge.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "derived property compile requires rust")
		return nil, false
	}
	payload, err := json.Marshal(rustIssuePropertyCompileRequest{
		PropertyID: propertyID,
		Kind:       kind,
		Config:     rawConfig,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode compile request")
		return nil, false
	}
	resp, ok := h.requestRustIssue(w, r, http.MethodPost, rustIssuePropertyCompilePath, workspaceID, userID, payload)
	if !ok {
		return nil, false
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20+1))
	if err != nil || len(body) > 1<<20 {
		if !writeQueryCancellation(w, r, err) {
			writeError(w, http.StatusBadGateway, "failed to read compile response")
		}
		return nil, false
	}
	if resp.StatusCode == http.StatusBadRequest {
		writeRustIssueResponse(w, resp, bytes.NewReader(body))
		return nil, false
	}
	if resp.StatusCode != http.StatusOK {
		writeRustIssueResponse(w, resp, bytes.NewReader(body))
		return nil, false
	}
	var reply rustIssuePropertyCompileResponse
	if err := json.Unmarshal(body, &reply); err != nil || len(reply.Config) == 0 {
		writeError(w, http.StatusBadGateway, "invalid compile response")
		return nil, false
	}
	return reply.Config, true
}
