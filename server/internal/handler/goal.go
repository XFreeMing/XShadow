package handler

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Goals are a first-class deliverable object (DEV-5): they own their intent,
// scope, acceptance criteria, owner and lifecycle. A goal is not an issue and
// does not inherit issue status.
//
// Invariants this file maintains:
//   - Only an explicit goal write changes goal.status. No task transition
//     (done, cancelled, in_review) is read here.
//   - Assigning a member/agent/squad owner records responsibility only. It
//     never grants access and never enqueues a task, so the issue assignee /
//     invoke gate is deliberately not reused.
//   - Visibility follows the project: a goal in a hidden private project
//     answers exactly like a missing one, and a page total never counts it.

var validGoalStatuses = []string{"planned", "in_progress", "paused", "completed", "cancelled"}
var validGoalOwnerTypes = []string{"member", "agent", "squad"}

const (
	goalListDefaultLimit = 50
	goalListMaxLimit     = 100
)

type GoalResponse struct {
	ID                 string  `json:"id"`
	WorkspaceID        string  `json:"workspace_id"`
	ProjectID          *string `json:"project_id"`
	Title              string  `json:"title"`
	Intent             string  `json:"intent"`
	Scope              string  `json:"scope"`
	AcceptanceCriteria string  `json:"acceptance_criteria"`
	OwnerType          *string `json:"owner_type"`
	OwnerID            *string `json:"owner_id"`
	Status             string  `json:"status"`
	CreatorType        string  `json:"creator_type"`
	CreatorID          string  `json:"creator_id"`
	CreatedAt          string  `json:"created_at"`
	UpdatedAt          string  `json:"updated_at"`
}

func goalToResponse(g db.Goal) GoalResponse {
	return GoalResponse{
		ID:                 uuidToString(g.ID),
		WorkspaceID:        uuidToString(g.WorkspaceID),
		ProjectID:          uuidToPtr(g.ProjectID),
		Title:              g.Title,
		Intent:             g.Intent,
		Scope:              g.Scope,
		AcceptanceCriteria: g.AcceptanceCriteria,
		OwnerType:          textToPtr(g.OwnerType),
		OwnerID:            uuidToPtr(g.OwnerID),
		Status:             g.Status,
		CreatorType:        g.CreatorType,
		CreatorID:          uuidToString(g.CreatorID),
		CreatedAt:          timestampToString(g.CreatedAt),
		UpdatedAt:          timestampToString(g.UpdatedAt),
	}
}

type CreateGoalRequest struct {
	Title              string  `json:"title"`
	Intent             *string `json:"intent"`
	Scope              *string `json:"scope"`
	AcceptanceCriteria *string `json:"acceptance_criteria"`
	ProjectID          *string `json:"project_id"`
	OwnerType          *string `json:"owner_type"`
	OwnerID            *string `json:"owner_id"`
	Status             *string `json:"status"`
}

type UpdateGoalRequest struct {
	Title              *string `json:"title"`
	Intent             *string `json:"intent"`
	Scope              *string `json:"scope"`
	AcceptanceCriteria *string `json:"acceptance_criteria"`
	ProjectID          *string `json:"project_id"`
	OwnerType          *string `json:"owner_type"`
	OwnerID            *string `json:"owner_id"`
	Status             *string `json:"status"`
}

// publishGoalChanged tells every client that this workspace's goal queries
// need a refresh. The payload is deliberately empty: goals may sit in private
// projects, and a workspace broadcast is not where per-viewer filtering
// happens. Clients re-read through the authorized HTTP API.
func (h *Handler) publishGoalChanged(workspaceID, actorType, actorID string) {
	h.publish(protocol.EventGoalChanged, workspaceID, actorType, actorID, map[string]any{})
}

func isGoalEnumValue(value string, allowed []string) bool {
	for _, a := range allowed {
		if value == a {
			return true
		}
	}
	return false
}

func goalEnumError(w http.ResponseWriter, field, value string, allowed []string) {
	writeError(w, http.StatusBadRequest,
		"invalid "+field+" "+strconv.Quote(value)+"; valid values: "+strings.Join(allowed, ", "))
}

// loadVisibleGoal resolves a goal by UUID inside the request's workspace and
// applies project visibility. A goal in a hidden private project answers 404
// exactly like a missing one.
func (h *Handler) loadVisibleGoal(w http.ResponseWriter, r *http.Request, goalID string) (db.Goal, bool) {
	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return db.Goal{}, false
	}
	goalUUID, ok := parseUUIDOrBadRequest(w, goalID, "goal id")
	if !ok {
		return db.Goal{}, false
	}
	goal, err := h.Queries.GetGoalInWorkspace(r.Context(), db.GetGoalInWorkspaceParams{
		ID:          goalUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		if !isNotFound(err) {
			slog.Error("load goal failed", append(logger.RequestAttrs(r), "goal_id", goalID, "error", err)...)
		}
		writeError(w, http.StatusNotFound, "goal not found")
		return db.Goal{}, false
	}
	if !h.requireProjectVisible(w, r, workspaceID, goal.ProjectID, http.StatusNotFound, "goal not found") {
		return db.Goal{}, false
	}
	return goal, true
}

// goalPagination parses limit/offset the way the issue list does: malformed
// or out-of-range values fall back to the defaults instead of failing.
func goalPagination(r *http.Request) (int32, int32) {
	limit := goalListDefaultLimit
	if l := r.URL.Query().Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 {
			limit = v
		}
	}
	if limit > goalListMaxLimit {
		limit = goalListMaxLimit
	}
	offset := 0
	if o := r.URL.Query().Get("offset"); o != "" {
		if v, err := strconv.Atoi(o); err == nil && v >= 0 {
			offset = v
		}
	}
	return int32(limit), int32(offset)
}

// validateGoalProjectRef resolves a project reference from a request body.
// A missing or empty reference means "no project". A project outside the
// workspace, or a private project the caller cannot see, answers 404 — the
// same answer a missing project gets.
func (h *Handler) validateGoalProjectRef(w http.ResponseWriter, r *http.Request, wsUUID pgtype.UUID, raw *string) (pgtype.UUID, bool) {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return pgtype.UUID{}, true
	}
	projectUUID, ok := parseUUIDOrBadRequest(w, strings.TrimSpace(*raw), "project_id")
	if !ok {
		return pgtype.UUID{}, false
	}
	if _, err := h.Queries.GetProjectInWorkspace(r.Context(), db.GetProjectInWorkspaceParams{
		ID:          projectUUID,
		WorkspaceID: wsUUID,
	}); err != nil {
		if !isNotFound(err) {
			slog.Error("validate goal project failed",
				append(logger.RequestAttrs(r), "project_id", uuidToString(projectUUID), "error", err)...)
		}
		writeError(w, http.StatusNotFound, "project not found")
		return pgtype.UUID{}, false
	}
	if !h.requireProjectVisible(w, r, uuidToString(wsUUID), projectUUID, http.StatusNotFound, "project not found") {
		return pgtype.UUID{}, false
	}
	return projectUUID, true
}

// validateGoalOwnerPair checks the resulting (owner_type, owner_id) pair. The
// owner is responsibility only: this never grants access and never starts a
// run. A reference outside the workspace, an archived target, or an agent the
// caller cannot see all get the same 400, so the response does not disclose
// that a hidden target exists.
func (h *Handler) validateGoalOwnerPair(w http.ResponseWriter, r *http.Request, workspaceID string, ownerType pgtype.Text, ownerID pgtype.UUID) bool {
	if !ownerType.Valid && !ownerID.Valid {
		return true
	}
	if ownerType.Valid != ownerID.Valid {
		writeError(w, http.StatusBadRequest, "owner_type and owner_id must be provided together")
		return false
	}
	if !isGoalEnumValue(ownerType.String, validGoalOwnerTypes) {
		goalEnumError(w, "owner_type", ownerType.String, validGoalOwnerTypes)
		return false
	}
	wsUUID, err := util.ParseUUID(workspaceID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid workspace_id")
		return false
	}
	switch ownerType.String {
	case "member":
		if _, err := h.Queries.GetMemberByUserAndWorkspace(r.Context(), db.GetMemberByUserAndWorkspaceParams{
			UserID:      ownerID,
			WorkspaceID: wsUUID,
		}); err != nil {
			writeError(w, http.StatusBadRequest, "owner_id does not refer to a member of this workspace")
			return false
		}
	case "agent":
		agent, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
			ID:          ownerID,
			WorkspaceID: wsUUID,
		})
		if err != nil || agent.ArchivedAt.Valid {
			writeError(w, http.StatusBadRequest, "owner_id does not refer to an available agent of this workspace")
			return false
		}
		actorType, actorID := h.resolveActor(r, requestUserID(r), workspaceID)
		if !h.canAccessPrivateAgent(r.Context(), agent, actorType, actorID, workspaceID) {
			writeError(w, http.StatusBadRequest, "owner_id does not refer to an available agent of this workspace")
			return false
		}
	case "squad":
		squad, err := h.Queries.GetSquadInWorkspace(r.Context(), db.GetSquadInWorkspaceParams{
			ID:          ownerID,
			WorkspaceID: wsUUID,
		})
		if err != nil || squad.ArchivedAt.Valid {
			writeError(w, http.StatusBadRequest, "owner_id does not refer to an available squad of this workspace")
			return false
		}
	}
	return true
}

func (h *Handler) ListGoals(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}

	var projectFilter pgtype.UUID
	standaloneOnly := false
	if p := r.URL.Query().Get("project_id"); p != "" {
		if p == "none" {
			standaloneOnly = true
		} else {
			id, ok := parseUUIDOrBadRequest(w, p, "project_id")
			if !ok {
				return
			}
			projectFilter = id
		}
	}
	var statusFilter pgtype.Text
	if s := r.URL.Query().Get("status"); s != "" {
		if !isGoalEnumValue(s, validGoalStatuses) {
			goalEnumError(w, "status", s, validGoalStatuses)
			return
		}
		statusFilter = pgtype.Text{String: s, Valid: true}
	}
	limit, offset := goalPagination(r)

	hidden, ok := h.requestHiddenProjects(w, r, workspaceID)
	if !ok {
		return
	}
	goals, err := h.Queries.ListGoals(r.Context(), db.ListGoalsParams{
		WorkspaceID:      wsUUID,
		ProjectID:        projectFilter,
		StandaloneOnly:   pgtype.Bool{Bool: standaloneOnly, Valid: true},
		Status:           statusFilter,
		HiddenProjectIds: hidden.Param(),
		Limit:            limit,
		Offset:           offset,
	})
	if err != nil {
		slog.Error("list goals failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to list goals")
		return
	}
	total, err := h.Queries.CountGoals(r.Context(), db.CountGoalsParams{
		WorkspaceID:      wsUUID,
		ProjectID:        projectFilter,
		StandaloneOnly:   pgtype.Bool{Bool: standaloneOnly, Valid: true},
		Status:           statusFilter,
		HiddenProjectIds: hidden.Param(),
	})
	if err != nil {
		slog.Error("count goals failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to list goals")
		return
	}
	resp := make([]GoalResponse, len(goals))
	for i, g := range goals {
		resp[i] = goalToResponse(g)
	}
	writeJSON(w, http.StatusOK, map[string]any{"goals": resp, "total": total})
}

func (h *Handler) CreateGoal(w http.ResponseWriter, r *http.Request) {
	var req CreateGoalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		writeError(w, http.StatusBadRequest, "title is required")
		return
	}
	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	status := "planned"
	if req.Status != nil && *req.Status != "" {
		if !isGoalEnumValue(*req.Status, validGoalStatuses) {
			goalEnumError(w, "status", *req.Status, validGoalStatuses)
			return
		}
		status = *req.Status
	}
	projectID, ok := h.validateGoalProjectRef(w, r, wsUUID, req.ProjectID)
	if !ok {
		return
	}
	var ownerType pgtype.Text
	var ownerID pgtype.UUID
	if req.OwnerType != nil {
		ownerType = pgtype.Text{String: *req.OwnerType, Valid: true}
	}
	if req.OwnerID != nil && *req.OwnerID != "" {
		id, ok := parseUUIDOrBadRequest(w, *req.OwnerID, "owner_id")
		if !ok {
			return
		}
		ownerID = id
	}
	if req.OwnerType == nil && req.OwnerID != nil {
		writeError(w, http.StatusBadRequest, "owner_type and owner_id must be provided together")
		return
	}
	if !h.validateGoalOwnerPair(w, r, workspaceID, ownerType, ownerID) {
		return
	}
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	creatorID, ok := parseUUIDOrBadRequest(w, actorID, "creator_id")
	if !ok {
		return
	}

	goal, err := h.Queries.CreateGoal(r.Context(), db.CreateGoalParams{
		WorkspaceID:        wsUUID,
		ProjectID:          projectID,
		Title:              title,
		Intent:             ptrToTextOrEmpty(req.Intent),
		Scope:              ptrToTextOrEmpty(req.Scope),
		AcceptanceCriteria: ptrToTextOrEmpty(req.AcceptanceCriteria),
		OwnerType:          ownerType,
		OwnerID:            ownerID,
		Status:             status,
		CreatorType:        actorType,
		CreatorID:          creatorID,
	})
	if err != nil {
		h.writeGoalWriteError(w, r, err, "create")
		return
	}
	h.publishGoalChanged(workspaceID, actorType, actorID)
	writeJSON(w, http.StatusCreated, goalToResponse(goal))
}

func (h *Handler) GetGoal(w http.ResponseWriter, r *http.Request) {
	goal, ok := h.loadVisibleGoal(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, goalToResponse(goal))
}

func (h *Handler) UpdateGoal(w http.ResponseWriter, r *http.Request) {
	prev, ok := h.loadVisibleGoal(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	workspaceID := uuidToString(prev.WorkspaceID)
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	var req UpdateGoalRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	var rawFields map[string]json.RawMessage
	json.Unmarshal(body, &rawFields)

	params := db.UpdateGoalParams{ID: prev.ID, WorkspaceID: prev.WorkspaceID}
	if req.Title != nil {
		title := strings.TrimSpace(*req.Title)
		if title == "" {
			writeError(w, http.StatusBadRequest, "title is required")
			return
		}
		params.Title = pgtype.Text{String: title, Valid: true}
	}
	if _, touched := rawFields["intent"]; touched {
		params.IntentSet = true
		params.Intent = pgtype.Text{String: ptrToTextOrEmpty(req.Intent), Valid: true}
	}
	if _, touched := rawFields["scope"]; touched {
		params.ScopeSet = true
		params.Scope = pgtype.Text{String: ptrToTextOrEmpty(req.Scope), Valid: true}
	}
	if _, touched := rawFields["acceptance_criteria"]; touched {
		params.AcceptanceCriteriaSet = true
		params.AcceptanceCriteria = pgtype.Text{String: ptrToTextOrEmpty(req.AcceptanceCriteria), Valid: true}
	}
	if req.Status != nil {
		if !isGoalEnumValue(*req.Status, validGoalStatuses) {
			goalEnumError(w, "status", *req.Status, validGoalStatuses)
			return
		}
		params.Status = pgtype.Text{String: *req.Status, Valid: true}
	}

	// Owner: a partial update must not turn an omitted half of the pair into
	// an implicit clear. Both keys absent keeps the owner; both keys present
	// replaces it (both null clears) or is rejected as a mixed/empty pair; a
	// lone key, including a lone null, is a malformed request.
	_, touchedOwnerType := rawFields["owner_type"]
	_, touchedOwnerID := rawFields["owner_id"]
	if touchedOwnerType != touchedOwnerID {
		writeError(w, http.StatusBadRequest, "owner_type and owner_id must be provided together")
		return
	}
	if touchedOwnerType {
		var newOwnerType pgtype.Text
		var newOwnerID pgtype.UUID
		if req.OwnerType != nil && *req.OwnerType != "" {
			newOwnerType = pgtype.Text{String: *req.OwnerType, Valid: true}
		}
		if req.OwnerID != nil && *req.OwnerID != "" {
			id, ok := parseUUIDOrBadRequest(w, *req.OwnerID, "owner_id")
			if !ok {
				return
			}
			newOwnerID = id
		}
		// Both sides must agree: one null/empty alongside a value is neither
		// a replacement nor the paired clear the API accepts.
		if newOwnerType.Valid != newOwnerID.Valid {
			writeError(w, http.StatusBadRequest, "owner_type and owner_id must be provided together")
			return
		}
		if !h.validateGoalOwnerPair(w, r, workspaceID, newOwnerType, newOwnerID) {
			return
		}
		params.OwnerSet = true
		params.OwnerType = newOwnerType
		params.OwnerID = newOwnerID
	}

	// Project: a present key with null/"" clears it, an absent key keeps it.
	if _, touched := rawFields["project_id"]; touched {
		newProject, ok := h.validateGoalProjectRef(w, r, prev.WorkspaceID, req.ProjectID)
		if !ok {
			return
		}
		params.ProjectIDSet = true
		params.ProjectID = newProject
	}

	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	// Lock first and compare against the locked row, so a concurrent link or
	// project move cannot slip between the read above and this write.
	locked, err := qtx.LockGoalForUpdate(r.Context(), db.LockGoalForUpdateParams{
		ID:          prev.ID,
		WorkspaceID: prev.WorkspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "goal not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to update goal")
		return
	}
	if !h.requireProjectVisible(w, r, workspaceID, locked.ProjectID, http.StatusNotFound, "goal not found") {
		return
	}
	if params.ProjectIDSet && uuidToString(locked.ProjectID) != uuidToString(params.ProjectID) {
		links, err := qtx.CountGoalIssues(r.Context(), db.CountGoalIssuesParams{
			WorkspaceID: locked.WorkspaceID,
			GoalID:      locked.ID,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to update goal")
			return
		}
		if links > 0 {
			writeErrorCode(w, http.StatusConflict, "goal_has_linked_issues",
				"this goal still has linked issues; unlink them before changing its project")
			return
		}
		// A plan is read and generated against the goal's project. Moving the
		// goal would leave the saved draft and any in-flight run describing the
		// old project, so the move waits until the plan is deleted and no
		// generation is still running.
		hasPlan, err := qtx.GoalPlanExists(r.Context(), db.GoalPlanExistsParams{
			WorkspaceID: locked.WorkspaceID,
			GoalID:      locked.ID,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to update goal")
			return
		}
		if hasPlan {
			writeErrorCode(w, http.StatusConflict, "goal_has_plan",
				"this goal has a plan saved against its current project; delete the plan before changing its project")
			return
		}
		active, err := qtx.CountActiveGoalPlanGenerations(r.Context(), db.CountActiveGoalPlanGenerationsParams{
			WorkspaceID: locked.WorkspaceID,
			GoalID:      locked.ID,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to update goal")
			return
		}
		if active > 0 {
			writeErrorCode(w, http.StatusConflict, "goal_generation_in_progress",
				"a plan generation is still running for this goal; wait for it before changing its project")
			return
		}
	}
	// DEV-7: completing a goal that is under the closure contract is the
	// accepted close, not an arbitrary status write. A member's write is the
	// human endpoint and is not gated.
	if params.Status.Valid && params.Status.String == "completed" && locked.Status != "completed" && actorType == "agent" {
		caller := h.closureRequestAgentPrincipal(r.Context(), r, actorID)
		if err := h.closureGateGoalCompleted(r.Context(), locked, caller, locked.OwnerType.String == "agent" && uuidToString(locked.OwnerID) == caller.ID); err != nil {
			if writeIssueWorkflowError(w, err) {
				return
			}
			if isNotFound(err) {
				writeError(w, http.StatusNotFound, "goal not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "failed to evaluate goal closure")
			return
		}
	}
	goal, err := qtx.UpdateGoal(r.Context(), params)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "goal not found")
			return
		}
		h.writeGoalWriteError(w, r, err, "update")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit goal update")
		return
	}
	h.publishGoalChanged(workspaceID, actorType, actorID)
	writeJSON(w, http.StatusOK, goalToResponse(goal))
}

func (h *Handler) DeleteGoal(w http.ResponseWriter, r *http.Request) {
	goal, ok := h.loadVisibleGoal(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	if !h.requireGoalDeletion(w, r, goal) {
		return
	}
	workspaceID := uuidToString(goal.WorkspaceID)
	userID := requestUserID(r)
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	// Serialize with relation writes: a link takes FOR SHARE on the goal, so
	// after this lock no new link can commit before the goal is gone.
	if _, err := qtx.LockGoalForUpdate(r.Context(), db.LockGoalForUpdateParams{
		ID:          goal.ID,
		WorkspaceID: goal.WorkspaceID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "goal not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to delete goal")
		return
	}
	// Links go first; the tasks themselves are never touched.
	if err := qtx.DeleteGoalIssuesByGoal(r.Context(), db.DeleteGoalIssuesByGoalParams{
		WorkspaceID: goal.WorkspaceID,
		GoalID:      goal.ID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete goal")
		return
	}
	// Plan data is goal-scoped with no foreign key to enforce it (repo rule),
	// so it is removed explicitly. A generation row that is still running keeps
	// its task (the task is never deleted) but loses its goal; a later result
	// submission then answers 404.
	if err := qtx.DeleteGoalPlanGenerationsByGoal(r.Context(), db.DeleteGoalPlanGenerationsByGoalParams{
		WorkspaceID: goal.WorkspaceID,
		GoalID:      goal.ID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete goal")
		return
	}
	if err := qtx.DeleteGoalPlansByGoal(r.Context(), db.DeleteGoalPlansByGoalParams{
		WorkspaceID: goal.WorkspaceID,
		GoalID:      goal.ID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete goal")
		return
	}
	// Closure evidence hangs off the goal by value only (no foreign keys by
	// project rule), so it is removed with the goal.
	if err := qtx.DeleteClosureReviewsBySubject(r.Context(), db.DeleteClosureReviewsBySubjectParams{
		WorkspaceID: goal.WorkspaceID, SubjectKind: "goal", SubjectID: goal.ID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete goal")
		return
	}
	if err := qtx.DeleteClosureAuthorizationsBySubject(r.Context(), db.DeleteClosureAuthorizationsBySubjectParams{
		WorkspaceID: goal.WorkspaceID, SubjectKind: "goal", SubjectID: goal.ID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete goal")
		return
	}
	if err := qtx.DeleteGoal(r.Context(), db.DeleteGoalParams{
		ID:          goal.ID,
		WorkspaceID: goal.WorkspaceID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete goal")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit goal delete")
		return
	}
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	h.publishGoalChanged(workspaceID, actorType, actorID)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) ListGoalIssues(w http.ResponseWriter, r *http.Request) {
	goal, ok := h.loadVisibleGoal(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	hidden, ok := h.requestHiddenProjects(w, r, uuidToString(goal.WorkspaceID))
	if !ok {
		return
	}
	limit, offset := goalPagination(r)
	issues, err := h.Queries.ListGoalIssues(r.Context(), db.ListGoalIssuesParams{
		WorkspaceID:      goal.WorkspaceID,
		GoalID:           goal.ID,
		HiddenProjectIds: hidden.Param(),
		Limit:            limit,
		Offset:           offset,
	})
	if err != nil {
		slog.Error("list goal issues failed",
			append(logger.RequestAttrs(r), "goal_id", uuidToString(goal.ID), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to list goal issues")
		return
	}
	total, err := h.Queries.CountGoalIssuesVisible(r.Context(), db.CountGoalIssuesVisibleParams{
		WorkspaceID:      goal.WorkspaceID,
		GoalID:           goal.ID,
		HiddenProjectIds: hidden.Param(),
	})
	if err != nil {
		slog.Error("count goal issues failed",
			append(logger.RequestAttrs(r), "goal_id", uuidToString(goal.ID), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to list goal issues")
		return
	}
	prefix := h.getIssuePrefix(r.Context(), goal.WorkspaceID)
	resp := make([]IssueResponse, len(issues))
	for i, issue := range issues {
		resp[i] = issueToResponse(issue, prefix)
	}
	h.fillStatusCategories(r.Context(), goal.WorkspaceID, hidden, resp)
	writeJSON(w, http.StatusOK, map[string]any{"issues": resp, "total": total})
}

// LinkGoalIssue — PUT /api/goals/{id}/issues/{issueId}. Idempotent for the
// same pair; a task already linked to another goal is a conflict, never an
// implicit move.
func (h *Handler) LinkGoalIssue(w http.ResponseWriter, r *http.Request) {
	goal, ok := h.loadVisibleGoal(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	// loadIssueForUser resolves UUID/identifier and applies project visibility.
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "issueId"))
	if !ok {
		return
	}
	workspaceID := uuidToString(goal.WorkspaceID)
	userID := requestUserID(r)
	hidden, ok := h.requestHiddenProjects(w, r, workspaceID)
	if !ok {
		return
	}
	if !hidden.CanSeeProject(issue.ProjectID) {
		writeError(w, http.StatusNotFound, "issue not found")
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	// Re-read both rows under lock: either may have been moved, deleted, or
	// relinked between the reads above and this write.
	lockedGoal, err := qtx.LockGoalForLink(r.Context(), db.LockGoalForLinkParams{
		ID:          goal.ID,
		WorkspaceID: goal.WorkspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "goal not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to link issue")
		return
	}
	lockedIssue, err := qtx.LockIssueForGoalLink(r.Context(), db.LockIssueForGoalLinkParams{
		ID:          issue.ID,
		WorkspaceID: goal.WorkspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "issue not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to link issue")
		return
	}
	if !hidden.CanSeeProject(lockedGoal.ProjectID) || !hidden.CanSeeProject(lockedIssue.ProjectID) {
		writeError(w, http.StatusNotFound, "issue not found")
		return
	}
	// Same workspace and same project, including "neither has a project".
	if uuidToString(lockedGoal.ProjectID) != uuidToString(lockedIssue.ProjectID) {
		writeErrorCode(w, http.StatusConflict, "goal_project_mismatch",
			"the issue and the goal must belong to the same project")
		return
	}

	inserted := true
	if _, err := qtx.LinkGoalIssue(r.Context(), db.LinkGoalIssueParams{
		WorkspaceID: goal.WorkspaceID,
		GoalID:      goal.ID,
		IssueID:     issue.ID,
	}); err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusInternalServerError, "failed to link issue")
			return
		}
		inserted = false
	}
	if !inserted {
		existing, err := qtx.GetGoalIssueLink(r.Context(), db.GetGoalIssueLinkParams{
			WorkspaceID: goal.WorkspaceID,
			IssueID:     issue.ID,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to link issue")
			return
		}
		if existing.GoalID != goal.ID {
			writeErrorCode(w, http.StatusConflict, "issue_already_linked",
				"the issue is already linked to another goal")
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit issue link")
		return
	}
	if inserted {
		actorType, actorID := h.resolveActor(r, userID, workspaceID)
		h.publishGoalChanged(workspaceID, actorType, actorID)
	}
	w.WriteHeader(http.StatusNoContent)
}

// UnlinkGoalIssue — DELETE /api/goals/{id}/issues/{issueId}. Removing a pair
// that is no longer linked is success; the task and the goal both survive.
func (h *Handler) UnlinkGoalIssue(w http.ResponseWriter, r *http.Request) {
	goal, ok := h.loadVisibleGoal(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "issueId"))
	if !ok {
		return
	}
	removed, err := h.Queries.UnlinkGoalIssue(r.Context(), db.UnlinkGoalIssueParams{
		WorkspaceID: goal.WorkspaceID,
		GoalID:      goal.ID,
		IssueID:     issue.ID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to unlink issue")
		return
	}
	if removed > 0 {
		workspaceID := uuidToString(goal.WorkspaceID)
		actorType, actorID := h.resolveActor(r, requestUserID(r), workspaceID)
		h.publishGoalChanged(workspaceID, actorType, actorID)
	}
	w.WriteHeader(http.StatusNoContent)
}

// requireGoalDeletion mirrors requireProjectDeletion: the goal's creator (or
// the owner of the agent that created it), the creator of the goal's project
// on the same rule, and workspace owners/admins. Agent requests never inherit
// their token user's role or agent ownership.
func (h *Handler) requireGoalDeletion(w http.ResponseWriter, r *http.Request, goal db.Goal) bool {
	if isMachineCredentialActor(r) && r.Header.Get("X-Actor-Source") != "task_token" {
		writeError(w, http.StatusForbidden, "goal deletion requires a human or a bound agent identity")
		return false
	}
	wsID := uuidToString(goal.WorkspaceID)
	actorType, actorID := h.resolveActor(r, requestUserID(r), wsID)
	if actorType == "member" {
		member, ok := h.workspaceMember(w, r, wsID)
		if !ok {
			return false
		}
		if roleAllowed(member.Role, "owner", "admin") {
			return true
		}
	}
	if h.actorOwnsCreator(r, actorType, actorID, goal.CreatorType, goal.CreatorID, goal.WorkspaceID) {
		return true
	}
	if goal.ProjectID.Valid {
		project, err := h.Queries.GetProjectInWorkspace(r.Context(), db.GetProjectInWorkspaceParams{
			ID:          goal.ProjectID,
			WorkspaceID: goal.WorkspaceID,
		})
		if err == nil && project.CreatorType.Valid && project.CreatorID.Valid {
			if h.actorOwnsCreator(r, actorType, actorID, project.CreatorType.String, project.CreatorID, goal.WorkspaceID) {
				return true
			}
		}
	}
	writeError(w, http.StatusForbidden,
		"goal deletion is restricted to its creator, the creating agent's owner, the project creator, and workspace owners or admins")
	return false
}

// actorOwnsCreator reports whether the request's actor is the creator itself,
// or the member who owns the agent that created the object.
func (h *Handler) actorOwnsCreator(r *http.Request, actorType, actorID, creatorType string, creatorID pgtype.UUID, workspaceID pgtype.UUID) bool {
	if !creatorID.Valid {
		return false
	}
	if creatorType == actorType && uuidToString(creatorID) == actorID {
		return true
	}
	if actorType == "member" && creatorType == "agent" {
		agent, err := h.Queries.GetAgent(r.Context(), creatorID)
		if err == nil && agent.WorkspaceID == workspaceID && uuidToString(agent.OwnerID) == actorID {
			return true
		}
	}
	return false
}

// writeGoalWriteError maps a failed goal INSERT/UPDATE the way project writes
// are mapped: a CHECK violation is a client error, anything else is a server
// fault with the underlying error logged.
func (h *Handler) writeGoalWriteError(w http.ResponseWriter, r *http.Request, err error, action string) {
	if isCheckViolation(err) {
		writeError(w, http.StatusBadRequest, "goal "+action+" rejected: a field value failed a database constraint")
		return
	}
	slog.Error("goal "+action+" failed", append(logger.RequestAttrs(r), "error", err)...)
	writeError(w, http.StatusInternalServerError, "failed to "+action+" goal")
}

// ptrToTextOrEmpty treats a nil optional body as the column's empty default.
func ptrToTextOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
