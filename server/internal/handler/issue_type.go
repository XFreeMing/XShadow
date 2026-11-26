package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/issueproperty"
	"github.com/multica-ai/multica/server/internal/issuetype"
	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	maxActiveIssueTypesPerWorkspace = 50
	maxIssueTypeNameLen             = 64
	maxIssueTypeFields              = 20
)

type IssueTypeResponse struct {
	ID          string            `json:"id"`
	WorkspaceID string            `json:"workspace_id"`
	Name        string            `json:"name"`
	Fields      []issuetype.Field `json:"fields"`
	Position    float64           `json:"position"`
	Archived    bool              `json:"archived"`
	ArchivedAt  *string           `json:"archived_at"`
	CreatedAt   string            `json:"created_at"`
	UpdatedAt   string            `json:"updated_at"`
}

type CreateIssueTypeRequest struct {
	Name   string            `json:"name"`
	Fields []issuetype.Field `json:"fields"`
}

type UpdateIssueTypeRequest struct {
	Name     *string            `json:"name"`
	Fields   *[]issuetype.Field `json:"fields"`
	Archived *bool              `json:"archived"`
}

func issueTypeToResponse(t db.IssueType) IssueTypeResponse {
	fields, err := issuetype.ParseFields(t.Fields)
	if err != nil {
		fields = []issuetype.Field{}
	}
	resp := IssueTypeResponse{
		ID:          uuidToString(t.ID),
		WorkspaceID: uuidToString(t.WorkspaceID),
		Name:        t.Name,
		Fields:      fields,
		Position:    t.Position,
		Archived:    t.ArchivedAt.Valid,
		CreatedAt:   timestampToString(t.CreatedAt),
		UpdatedAt:   timestampToString(t.UpdatedAt),
	}
	if t.ArchivedAt.Valid {
		s := timestampToString(t.ArchivedAt)
		resp.ArchivedAt = &s
	}
	return resp
}

func validateIssueTypeName(raw string) (string, error) {
	for _, r := range raw {
		if unicode.IsControl(r) {
			return "", errors.New("name cannot contain tabs, newlines, or control characters")
		}
	}
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", errors.New("name is required")
	}
	if utf8.RuneCountInString(name) > maxIssueTypeNameLen {
		return "", fmt.Errorf("name must be %d characters or fewer", maxIssueTypeNameLen)
	}
	return name, nil
}

// validateIssueTypeFields checks every referenced property belongs to this
// workspace, is active, is unique within the type, and that any configured
// default passes the canonical property validator (including actor resolution).
func (h *Handler) validateIssueTypeFields(
	r *http.Request,
	q *db.Queries,
	workspaceID string,
	wsUUID pgtype.UUID,
	rawFields []issuetype.Field,
) ([]byte, int, string, error) {
	if len(rawFields) > maxIssueTypeFields {
		return nil, http.StatusBadRequest, fmt.Sprintf("an issue type cannot include more than %d fields", maxIssueTypeFields), nil
	}
	seen := make(map[string]struct{}, len(rawFields))
	canonical := make([]issuetype.Field, 0, len(rawFields))
	for _, field := range rawFields {
		propUUID, err := util.ParseUUID(strings.TrimSpace(field.PropertyID))
		if err != nil {
			return nil, http.StatusBadRequest, fmt.Sprintf("invalid property_id %q: must be a UUID", field.PropertyID), nil
		}
		propID := uuidToString(propUUID)
		if _, dup := seen[propID]; dup {
			return nil, http.StatusBadRequest, fmt.Sprintf("duplicate property_id %q in issue type fields", propID), nil
		}
		seen[propID] = struct{}{}

		def, err := q.GetIssueProperty(r.Context(), db.GetIssuePropertyParams{
			ID:          propUUID,
			WorkspaceID: wsUUID,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, http.StatusBadRequest, fmt.Sprintf("property %q not found in this workspace", propID), nil
			}
			return nil, 0, "", err
		}
		if def.ArchivedAt.Valid {
			return nil, http.StatusBadRequest, fmt.Sprintf("property %q is archived and cannot be used in an issue type", def.Name), nil
		}
		if issueproperty.IsComputed(def.Type) && (field.Required || field.HasDefault()) {
			return nil, http.StatusBadRequest, fmt.Sprintf("computed property %q cannot be required or have a default", def.Name), nil
		}

		entry := issuetype.Field{
			PropertyID: propID,
			Required:   field.Required,
		}
		if field.HasDefault() {
			validatedDefault, err := issueproperty.ValidateValue(def, field.DefaultValue)
			if err != nil {
				return nil, http.StatusBadRequest, fmt.Sprintf("invalid default_value for property %q: %s", def.Name, err.Error()), nil
			}
			if issueproperty.IsActor(def.Type) {
				refs, err := issueproperty.ActorRefsInValue(def.Type, validatedDefault)
				if err != nil {
					return nil, http.StatusBadRequest, fmt.Sprintf("invalid default_value for property %q: %s", def.Name, err.Error()), nil
				}
				if status, msg := h.resolveActorRefs(r, workspaceID, refs); status != 0 {
					return nil, status, fmt.Sprintf("invalid default_value for property %q: %s", def.Name, msg), nil
				}
			}
			if issueproperty.IsRecordRelation(def.Type) {
				relCfg, err := issueproperty.ParseRecordRelationConfig(def.Config)
				if err != nil {
					return nil, http.StatusBadRequest, fmt.Sprintf("invalid default_value for property %q: %s", def.Name, err.Error()), nil
				}
				recordIDs, err := issueproperty.RecordIDsInValue(validatedDefault)
				if err != nil {
					return nil, http.StatusBadRequest, fmt.Sprintf("invalid default_value for property %q: %s", def.Name, err.Error()), nil
				}
				if err := issueproperty.VerifyRecordRelationRefs(
					r.Context(), h.DB, workspaceID, relCfg.TargetTableID, recordIDs, false,
				); err != nil {
					if status, msg := recordRelationRefsHTTPError(err); status != 0 {
						return nil, status, fmt.Sprintf("invalid default_value for property %q: %s", def.Name, msg), nil
					}
				}
			}
			entry.DefaultValue = validatedDefault
		}
		canonical = append(canonical, entry)
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return nil, 0, "", err
	}
	return encoded, 0, "", nil
}

func issueTypeLockKeys(workspaceID string, fields []issuetype.Field) []string {
	keys := []string{"issue_types:" + workspaceID}
	propKeys := make([]string, 0, len(fields))
	seen := make(map[string]struct{}, len(fields))
	for _, f := range fields {
		id, err := util.ParseUUID(strings.TrimSpace(f.PropertyID))
		if err != nil {
			continue
		}
		k := "prop:" + uuidToString(id)
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		propKeys = append(propKeys, k)
	}
	sort.Strings(propKeys)
	return append(keys, propKeys...)
}

func (h *Handler) ListIssueTypes(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	includeArchived := r.URL.Query().Get("include_archived") == "true"
	rows, err := h.Queries.ListIssueTypes(r.Context(), db.ListIssueTypesParams{
		WorkspaceID:     wsUUID,
		IncludeArchived: includeArchived,
	})
	if err != nil {
		slog.Warn("ListIssueTypes failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to list issue types")
		return
	}
	resp := make([]IssueTypeResponse, len(rows))
	for i, row := range rows {
		resp[i] = issueTypeToResponse(row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"issue_types": resp, "total": len(resp)})
}

func (h *Handler) GetIssueType(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	idUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "issue type id")
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	issueType, err := h.Queries.GetIssueType(r.Context(), db.GetIssueTypeParams{
		ID:          idUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "issue type not found")
			return
		}
		slog.Warn("GetIssueType failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to get issue type")
		return
	}
	writeJSON(w, http.StatusOK, issueTypeToResponse(issueType))
}

func (h *Handler) CreateIssueType(w http.ResponseWriter, r *http.Request) {
	workspaceID, userID, ok := h.requirePropertyAdmin(w, r)
	if !ok {
		return
	}
	var req CreateIssueTypeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	name, err := validateIssueTypeName(req.Name)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}

	var created db.IssueType
	var httpStatus int
	var httpMsg string
	fail := func(status int, msg string) error {
		httpStatus, httpMsg = status, msg
		return errClientRejected
	}
	err = h.withPropertyLock(r, issueTypeLockKeys(workspaceID, req.Fields), func(_ pgx.Tx, q *db.Queries) error {
		if _, err := q.LockWorkspaceForIssueTypeCreate(r.Context(), wsUUID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fail(http.StatusNotFound, "workspace not found")
			}
			return err
		}
		active, err := q.CountActiveIssueTypes(r.Context(), wsUUID)
		if err != nil {
			return err
		}
		if active >= maxActiveIssueTypesPerWorkspace {
			return fail(http.StatusBadRequest, fmt.Sprintf("a workspace cannot have more than %d active issue types; archive unused ones first", maxActiveIssueTypesPerWorkspace))
		}
		fieldsJSON, status, msg, err := h.validateIssueTypeFields(r, q, workspaceID, wsUUID, req.Fields)
		if err != nil {
			return err
		}
		if status != 0 {
			return fail(status, msg)
		}
		created, err = q.CreateIssueType(r.Context(), db.CreateIssueTypeParams{
			WorkspaceID: wsUUID,
			Name:        name,
			Fields:      fieldsJSON,
		})
		if err != nil {
			if isUniqueViolation(err) {
				return fail(http.StatusConflict, "an issue type with that name already exists")
			}
			return err
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, errClientRejected) {
			writeError(w, httpStatus, httpMsg)
			return
		}
		slog.Warn("CreateIssueType failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to create issue type")
		return
	}
	resp := issueTypeToResponse(created)
	h.publish(protocol.EventIssueTypeChanged, workspaceID, "member", userID, map[string]any{"action": "created", "issue_type": resp})
	writeJSON(w, http.StatusCreated, resp)
}

func (h *Handler) UpdateIssueType(w http.ResponseWriter, r *http.Request) {
	workspaceID, userID, ok := h.requirePropertyAdmin(w, r)
	if !ok {
		return
	}
	idUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "issue type id")
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	var req UpdateIssueTypeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	var lockFields []issuetype.Field
	if req.Fields != nil {
		lockFields = *req.Fields
	}

	var updated db.IssueType
	var httpStatus int
	var httpMsg string
	fail := func(status int, msg string) error {
		httpStatus, httpMsg = status, msg
		return errClientRejected
	}
	err := h.withPropertyLock(r, issueTypeLockKeys(workspaceID, lockFields), func(_ pgx.Tx, q *db.Queries) error {
		existing, err := q.GetIssueType(r.Context(), db.GetIssueTypeParams{
			ID:          idUUID,
			WorkspaceID: wsUUID,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fail(http.StatusNotFound, "issue type not found")
			}
			return err
		}

		params := db.UpdateIssueTypeParams{ID: idUUID, WorkspaceID: wsUUID}
		if req.Name != nil {
			name, err := validateIssueTypeName(*req.Name)
			if err != nil {
				return fail(http.StatusBadRequest, err.Error())
			}
			params.Name = pgtype.Text{String: name, Valid: true}
		}
		if req.Fields != nil {
			fieldsJSON, status, msg, err := h.validateIssueTypeFields(r, q, workspaceID, wsUUID, *req.Fields)
			if err != nil {
				return err
			}
			if status != 0 {
				return fail(status, msg)
			}
			params.Fields = fieldsJSON
		}
		if req.Archived != nil {
			params.ArchivedSet = true
			if *req.Archived {
				params.ArchivedAt = pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
			} else if existing.ArchivedAt.Valid {
				active, err := q.CountActiveIssueTypes(r.Context(), wsUUID)
				if err != nil {
					return err
				}
				if active >= maxActiveIssueTypesPerWorkspace {
					return fail(http.StatusBadRequest, fmt.Sprintf("a workspace cannot have more than %d active issue types; archive unused ones first", maxActiveIssueTypesPerWorkspace))
				}
			}
		}

		updated, err = q.UpdateIssueType(r.Context(), params)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fail(http.StatusNotFound, "issue type not found")
			}
			if isUniqueViolation(err) {
				return fail(http.StatusConflict, "an issue type with that name already exists")
			}
			return err
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, errClientRejected) {
			writeError(w, httpStatus, httpMsg)
			return
		}
		slog.Warn("UpdateIssueType failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to update issue type")
		return
	}
	resp := issueTypeToResponse(updated)
	h.publish(protocol.EventIssueTypeChanged, workspaceID, "member", userID, map[string]any{"action": "updated", "issue_type": resp})
	writeJSON(w, http.StatusOK, resp)
}
