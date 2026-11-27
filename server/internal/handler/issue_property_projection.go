package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/issueproperty"
	"github.com/multica-ai/multica/server/internal/projectaccess"
	"github.com/multica-ai/multica/server/internal/tables"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const maxIssuePropertyProjectBatch = tables.MaxIssueProjectionBatch

type ProjectIssuePropertiesRequest struct {
	IssueIDs []string `json:"issue_ids"`
}

type ProjectIssuePropertiesResponse struct {
	Properties map[string]map[string]json.RawMessage `json:"properties"`
}

func (h *Handler) tablesPool() tables.Pool {
	if h == nil {
		return nil
	}
	if p, ok := h.TxStarter.(tables.Pool); ok {
		return p
	}
	if p, ok := h.DB.(tables.Pool); ok {
		return p
	}
	return nil
}

// ProjectTaskFormula evaluates a single read-only derived property for an issue
// while enforcing the caller's project visibility.
func (h *Handler) ProjectTaskFormula(ctx context.Context, workspaceID, memberID, issueID pgtype.UUID, propertyID string) (json.RawMessage, error) {
	pool := h.tablesPool()
	if pool == nil {
		return nil, errors.New("table engine unavailable")
	}
	issue, err := h.Queries.GetIssue(ctx, issueID)
	if err != nil {
		return nil, err
	}
	if issue.WorkspaceID != workspaceID {
		return nil, nil
	}
	if memberID.Valid {
		hidden, err := projectaccess.ForPrincipals(ctx, h.Queries, workspaceID, memberID)
		if err != nil {
			return nil, err
		}
		if !hidden.CanSeeProject(issue.ProjectID) {
			return nil, nil
		}
	}
	return tables.ProjectTaskFormula(ctx, pool, uuidToString(workspaceID), uuidToString(issueID), propertyID)
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

	if _, ok := requireUserID(w, r); !ok {
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

	pool := h.tablesPool()
	if pool == nil {
		writeError(w, http.StatusServiceUnavailable, "derived property projection unavailable")
		return
	}

	projected, err := tables.ProjectIssueProperties(r.Context(), pool, workspaceID, authorized)
	if writeQueryCancellation(w, r, err) {
		return
	}
	if err != nil {
		var se *tables.StoreError
		if errors.As(err, &se) && se.Kind == tables.ErrKindInvalid {
			writeError(w, http.StatusBadRequest, se.Message)
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to read property projection")
		return
	}

	filtered := filterProjectedComputedProperties(projected, allowed, computedIDs)
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

func (h *Handler) compileIssuePropertyConfig(
	w http.ResponseWriter,
	r *http.Request,
	tx pgx.Tx,
	workspaceID, kind string,
	propertyID *string,
	rawConfig json.RawMessage,
) (json.RawMessage, bool) {
	compiled, err := tables.CompileIssuePropertyConfigInTx(r.Context(), tx, workspaceID, propertyID, kind, rawConfig)
	if writeQueryCancellation(w, r, err) {
		return nil, false
	}
	if err != nil {
		var se *tables.StoreError
		if errors.As(err, &se) && se.Kind == tables.ErrKindInvalid {
			writeError(w, http.StatusBadRequest, se.Message)
			return nil, false
		}
		writeError(w, http.StatusInternalServerError, "failed to compile property config")
		return nil, false
	}
	return compiled, true
}
