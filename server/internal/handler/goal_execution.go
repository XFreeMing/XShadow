package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

func (h *Handler) canManageGoalExecution(r *http.Request, goal db.Goal) bool {
	if isMachineCredentialActor(r) {
		return false
	}
	kind, _ := h.resolveActor(r, requestUserID(r), uuidToString(goal.WorkspaceID))
	if kind != "member" {
		return false
	}
	member, err := h.Queries.GetMemberByUserAndWorkspace(r.Context(), db.GetMemberByUserAndWorkspaceParams{
		UserID: parseUUIDOrInvalid(requestUserID(r)), WorkspaceID: goal.WorkspaceID,
	})
	return err == nil && service.CanManageGoalExecution(member, goal)
}

func (h *Handler) PutGoalExecution(w http.ResponseWriter, r *http.Request) {
	goal, ok := h.loadVisibleGoal(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	member, ok := h.workspaceMember(w, r, uuidToString(goal.WorkspaceID))
	if !ok {
		return
	}
	kind, _ := h.resolveActor(r, requestUserID(r), uuidToString(goal.WorkspaceID))
	if isMachineCredentialActor(r) || kind != "member" || !service.CanManageGoalExecution(member, goal) {
		writeError(w, http.StatusForbidden, "goal execution requires a human goal creator or workspace owner or admin")
		return
	}
	var mutation struct {
		Enabled          *bool `json:"enabled"`
		ExpectedRevision int32 `json:"expected_revision"`
		Stage            *int  `json:"stage"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&mutation); err != nil || mutation.Enabled == nil || mutation.ExpectedRevision <= 0 {
		writeError(w, http.StatusBadRequest, "enabled and a saved expected_revision are required")
		return
	}
	if mutation.Stage != nil && (!*mutation.Enabled || *mutation.Stage <= 0) {
		writeError(w, http.StatusBadRequest, "stage requires an enabled grant and a positive stage number")
		return
	}
	ctx := r.Context()
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start goal execution update")
		return
	}
	defer tx.Rollback(ctx)
	q := h.Queries.WithTx(tx)
	goal, err = q.LockGoalForUpdate(ctx, db.LockGoalForUpdateParams{ID: goal.ID, WorkspaceID: goal.WorkspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "goal not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to lock goal")
		return
	}
	plan, err := q.GetGoalPlan(ctx, db.GetGoalPlanParams{WorkspaceID: goal.WorkspaceID, GoalID: goal.ID})
	if errors.Is(err, pgx.ErrNoRows) {
		writeGoalPlanRevisionConflict(w, 0)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read goal plan")
		return
	}
	if plan.Revision != mutation.ExpectedRevision {
		writeGoalPlanRevisionConflict(w, plan.Revision)
		return
	}
	params := db.SetGoalPlanExecutionParams{
		WorkspaceID: goal.WorkspaceID, GoalID: goal.ID, ExpectedRevision: plan.Revision,
	}
	if *mutation.Enabled {
		stage := 0
		if mutation.Stage != nil {
			stage = *mutation.Stage
			var content GoalPlanContent
			if err := json.Unmarshal(plan.Content, &content); err != nil {
				writeError(w, http.StatusInternalServerError, "failed to read goal plan items")
				return
			}
			if !slices.ContainsFunc(content.Items, func(item GoalPlanItem) bool { return item.Stage != nil && *item.Stage == stage }) {
				writeError(w, http.StatusBadRequest, "stage is not part of the saved goal plan")
				return
			}
		}
		if ok, err := h.goalReviewAllows(r.Context(), q, goal, plan, false); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to verify goal review")
			return
		} else if !ok {
			writeErrorCode(w, http.StatusConflict, "goal_review_required", "the current goal plan needs a verified coverage review before execution")
			return
		}
		progress, err := service.ReadGoalWorkProgress(ctx, q, goal, plan)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to read goal progress")
			return
		}
		if goal.Status != "in_progress" || progress.Total == 0 || progress.Completed == progress.Total || plan.ProjectID != goal.ProjectID {
			writeError(w, http.StatusConflict, "an in-progress goal with outstanding planned work is required")
			return
		}
		scope, err := service.GoalExecutionScope(ctx, q, goal, plan, stage)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to read goal execution scope")
			return
		}
		params.ExecutionID = plan.ExecutionID
		if !params.ExecutionID.Valid || plan.ExecutionRevision.Int32 != plan.Revision || plan.ExecutionScope.String != scope || plan.ExecutionBy != member.UserID {
			params.ExecutionID = dbid.NewV7()
		}
		params.ExecutionRevision = pgtype.Int4{Int32: plan.Revision, Valid: true}
		params.ExecutionScope = pgtype.Text{String: scope, Valid: true}
		params.ExecutionBy = member.UserID
	}
	plan, err = q.SetGoalPlanExecution(ctx, params)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save goal execution")
		return
	}
	resp, err := goalPlanToResponse(ctx, q, goal, plan)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read goal execution")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit goal execution")
		return
	}
	h.publishGoalChanged(uuidToString(goal.WorkspaceID), "member", uuidToString(member.UserID))
	resp.CanManageExecution = true
	writeJSON(w, http.StatusOK, resp)
}
