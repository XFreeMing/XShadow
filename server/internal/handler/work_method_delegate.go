package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/workmethod"
)

func (h *Handler) dispatchGoalCandidate(w http.ResponseWriter, r *http.Request, issue db.Issue, source db.AgentTaskQueue, change workmethod.Change) {
	if r.Header.Get("X-Actor-Source") != "task_token" || source.Status != "running" ||
		source.AgentID != parseUUIDOrInvalid(r.Header.Get("X-Agent-ID")) || !service.ReadGoalExecutionTask(source).ID.Valid {
		writeError(w, http.StatusForbidden, "candidate actions require the current authorized run")
		return
	}
	brief := strings.TrimSpace(change.Evidence)
	if change.BaseRevision == nil || *change.BaseRevision < 1 || brief == "" || len(brief) > 12000 ||
		change.MethodID != "" || change.MethodRevision != nil || change.TargetStep != nil || (len(change.CheckedItems) != 0 || len(change.Findings) != 0) {
		writeError(w, http.StatusBadRequest, "candidate actions require base_revision and evidence")
		return
	}
	workerID := source.AgentID
	if change.Action == workmethod.ClaimCandidate {
		if change.DelegateAgentID != "" {
			writeError(w, http.StatusBadRequest, "claim_candidate uses the authenticated agent identity")
			return
		}
	} else {
		var ok bool
		workerID, ok = parseUUIDOrBadRequest(w, change.DelegateAgentID, "delegate_agent_id")
		if !ok {
			return
		}
	}
	var run workmethod.Run
	if err := json.Unmarshal(issue.WorkMethodRun, &run); err != nil || !run.CanDelegateCandidate() {
		writeError(w, http.StatusForbidden, "only the authorized squad's current candidate step can be delegated")
		return
	}
	review := run
	review.State, review.CurrentActor = "review", nil
	recipient, err := h.methodRecipient(r.Context(), issue, review)
	if err != nil || recipient == nil {
		writeError(w, http.StatusConflict, "candidate requires an independent reviewer")
		return
	}
	var reviewerID pgtype.UUID
	switch recipient.Kind {
	case "agent":
		reviewerID = parseUUIDOrInvalid(recipient.ID)
	case "squad":
		squad, err := h.Queries.GetSquadInWorkspace(r.Context(), db.GetSquadInWorkspaceParams{
			ID: parseUUIDOrInvalid(recipient.ID), WorkspaceID: issue.WorkspaceID,
		})
		if err != nil || squad.ArchivedAt.Valid || !squad.LeaderID.Valid {
			writeError(w, http.StatusConflict, "candidate requires an independent reviewer")
			return
		}
		reviewerID = squad.LeaderID
	}
	task, err := h.TaskService.DispatchGoalCandidate(r.Context(), issue, source, change.Action, *change.BaseRevision, workerID, reviewerID, brief)
	switch {
	case errors.Is(err, service.ErrWakeupForbidden):
		writeErrorCode(w, http.StatusForbidden, "goal_execution_forbidden", "current goal authorization does not permit this candidate action")
	case errors.Is(err, service.ErrGoalCandidateConflict):
		writeError(w, http.StatusConflict, err.Error())
	case err != nil:
		slog.WarnContext(r.Context(), "candidate delegation failed", "issue_id", uuidToString(issue.ID), "error", err)
		writeError(w, http.StatusInternalServerError, "failed to dispatch candidate implementation")
	default:
		if err := h.TaskService.PublishWorkMethodHandoff(r.Context(), task); err != nil {
			slog.WarnContext(r.Context(), "candidate dispatch notification failed", "task_id", uuidToString(task.ID), "error", err)
		}
		if current, err := h.Queries.GetAgentTask(r.Context(), task.ID); err == nil {
			task = current
		}
		writeJSON(w, http.StatusOK, map[string]string{"task_id": uuidToString(task.ID), "agent_id": uuidToString(task.AgentID), "status": task.Status})
	}
}

func checkGoalCandidateReview(ctx context.Context, q *db.Queries, issue db.Issue, executionID, reviewerID pgtype.UUID) error {
	var before workmethod.Run
	if err := json.Unmarshal(issue.WorkMethodRun, &before); err != nil {
		return err
	}
	if !before.CanDelegateCandidate() {
		return nil
	}
	worker, err := service.GoalCandidateWorker(ctx, q, issue, before, executionID)
	if errors.Is(err, pgx.ErrNoRows) {
		if before.UsesCandidateSelfClaim() {
			return errors.New("the candidate requires a self-claimed implementation before submission")
		}
		return nil
	}
	if err != nil {
		return err
	}
	if !isTerminalTaskStatus(worker.Status) || (before.UsesCandidateSelfClaim() && worker.Status != "completed") {
		return errors.New("the candidate implementation run must finish before submission")
	}
	if worker.AgentID == reviewerID {
		return errors.New("the candidate implementer cannot independently review their own work")
	}
	return nil
}
