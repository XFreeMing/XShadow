package handler

// Goal plans (DEV-6) are independent, editable drafts produced from one
// sentence of intent. They reuse the existing chat -> agent task -> daemon
// chain to run the planning model, but they never create, assign or wake a
// product issue: a draft node only becomes executable in DEV-7.
//
// Invariants this file maintains:
//   - A plan belongs to exactly one goal and one project. A goal without a
//     project cannot save a plan or start a generation; the project's own
//     resources are the only evidence base (never the workspace default repo).
//   - Writes are compare-and-swap on goal_plan.revision, serialized per goal by
//     an advisory lock. A stale expected_revision answers 409 and keeps the
//     caller's input untouched.
//   - A generation is deduplicated by (workspace, goal, request_key). A replay
//     returns the original generation instead of starting a second run.
//   - A finished planning run always produces a candidate, even when the draft
//     moved on while it ran. The candidate is never auto-applied, so newer
//     edits are not overwritten; the client merges and saves explicitly.
//   - A result can only be submitted by the task token of the run that owns the
//     generation.
//   - Deleting the goal, its project or the workspace removes the plan and its
//     generations; there are no foreign keys (repo rule).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

const (
	goalPlanMaxItems        = 100
	goalPlanMaxEdges        = 300
	goalPlanMaxTitleRunes   = 500
	goalPlanTextFieldRunes  = 4000
	goalPlanMaxEvidenceRefs = 20
	goalPlanMaxSnapshotSize = 64 << 10
	goalPlanMaxContentSize  = 512 << 10
	goalPlanMaxIntentRunes  = 4000
	goalPlanMaxRequestKey   = 128
)

var validGoalPlanEvidenceKinds = []string{"resource", "file", "task", "comment", "contract", "other"}
var validGoalPlanBindingTypes = []string{"new", "existing"}

// GoalPlanItem is one draft node. Its ID is stable across generations and
// edits; the title is never a dedupe key.
type GoalPlanItem struct {
	ID                 string                 `json:"id"`
	Title              string                 `json:"title"`
	Outcome            string                 `json:"outcome"`
	AcceptanceCriteria string                 `json:"acceptance_criteria"`
	EvidenceRefs       []GoalPlanEvidenceRef  `json:"evidence_refs"`
	Binding            GoalPlanItemBinding    `json:"binding"`
	ProposedOwner      *GoalPlanProposedOwner `json:"proposed_owner"`
	Stage              *int                   `json:"stage"`
	Coordination       *GoalPlanCoordination  `json:"coordination"`
}

// GoalPlanEvidenceRef records one actual read: a project resource, a file and
// line range, an issue at a revision, or a contract comment. Version carries
// the concrete revision (repo SHA, "rev 3", comment revision) when known.
type GoalPlanEvidenceRef struct {
	Kind    string `json:"kind"`
	Ref     string `json:"ref"`
	Note    string `json:"note,omitempty"`
	Version string `json:"version,omitempty"`
}

// GoalPlanItemBinding is "new" for a node that will become a new issue in
// DEV-7, or "existing" with the issue it reuses. Binding to an issue never
// assigns it and never changes that issue.
type GoalPlanItemBinding struct {
	Type    string  `json:"type"`
	IssueID *string `json:"issue_id,omitempty"`
}

// GoalPlanProposedOwner is a responsibility suggestion only. It is not
// issue.assignee and it never enqueues a run.
type GoalPlanProposedOwner struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// GoalPlanCoordination holds the per-node reasons the contract requires:
// why the node may run in parallel, what it writes exclusively, who integrates
// it, and which fact should trigger the next readiness review.
type GoalPlanCoordination struct {
	ParallelReason     string   `json:"parallel_reason,omitempty"`
	WriteScope         []string `json:"write_scope,omitempty"`
	ExclusiveResources []string `json:"exclusive_resources,omitempty"`
	IntegrationOwner   string   `json:"integration_owner,omitempty"`
	NextEvaluation     string   `json:"next_evaluation,omitempty"`
}

// GoalPlanEdge is a strong prerequisite: dependent_item_id waits for
// prerequisite_item_id, and reason states why.
type GoalPlanEdge struct {
	DependentItemID    string `json:"dependent_item_id"`
	PrerequisiteItemID string `json:"prerequisite_item_id"`
	Reason             string `json:"reason"`
}

type GoalPlanContent struct {
	Items []GoalPlanItem `json:"items"`
	Edges []GoalPlanEdge `json:"edges"`
}

type GoalPlanResponse struct {
	ID              *string         `json:"id"`
	GoalID          string          `json:"goal_id"`
	ProjectID       *string         `json:"project_id"`
	Revision        int32           `json:"revision"`
	State           string          `json:"state"`
	Items           []GoalPlanItem  `json:"items"`
	Edges           []GoalPlanEdge  `json:"edges"`
	ContextSnapshot json.RawMessage `json:"context_snapshot"`
	CreatedAt       *string         `json:"created_at"`
	UpdatedAt       *string         `json:"updated_at"`
}

type PutGoalPlanRequest struct {
	ExpectedRevision int32           `json:"expected_revision"`
	Items            []GoalPlanItem  `json:"items"`
	Edges            []GoalPlanEdge  `json:"edges"`
	ContextSnapshot  json.RawMessage `json:"context_snapshot"`
	GenerationID     *string         `json:"generation_id"`
}

type CreateGoalPlanGenerationRequest struct {
	Intent           string `json:"intent"`
	RequestKey       string `json:"request_key"`
	ExpectedRevision *int32 `json:"expected_revision"`
	AgentID          string `json:"agent_id"`
}

type GoalPlanGenerationResponse struct {
	ID               string          `json:"id"`
	GoalID           string          `json:"goal_id"`
	ProjectID        string          `json:"project_id"`
	RequestKey       string          `json:"request_key"`
	Intent           string          `json:"intent"`
	AgentID          string          `json:"agent_id"`
	BaseRevision     int32           `json:"base_revision"`
	State            string          `json:"state"`
	ChatSessionID    *string         `json:"chat_session_id"`
	TaskID           *string         `json:"task_id"`
	InputContext     json.RawMessage `json:"input_context"`
	Candidate        json.RawMessage `json:"candidate"`
	Applied          bool            `json:"applied"`
	AppliedRevision  *int32          `json:"applied_revision"`
	ObservedRevision *int32          `json:"observed_revision"`
	CurrentRevision  int32           `json:"current_revision"`
	Stale            bool            `json:"stale"`
	Error            string          `json:"error"`
	CreatedAt        string          `json:"created_at"`
	UpdatedAt        string          `json:"updated_at"`
}

type GoalPlanResultRequest struct {
	Items           []GoalPlanItem  `json:"items"`
	Edges           []GoalPlanEdge  `json:"edges"`
	ContextSnapshot json.RawMessage `json:"context_snapshot"`
	Error           string          `json:"error"`
}

// GoalPlanCandidate is the stored generation result: a complete draft plus the
// evidence the run actually read. It is a candidate, not a saved revision.
type GoalPlanCandidate struct {
	Items           []GoalPlanItem  `json:"items"`
	Edges           []GoalPlanEdge  `json:"edges"`
	ContextSnapshot json.RawMessage `json:"context_snapshot"`
}

func emptyGoalPlanResponse(goal db.Goal) GoalPlanResponse {
	return GoalPlanResponse{
		GoalID:          uuidToString(goal.ID),
		ProjectID:       uuidToPtr(goal.ProjectID),
		Revision:        0,
		State:           "draft",
		Items:           []GoalPlanItem{},
		Edges:           []GoalPlanEdge{},
		ContextSnapshot: json.RawMessage("{}"),
	}
}

func goalPlanToResponse(goal db.Goal, plan db.GoalPlan) (GoalPlanResponse, error) {
	var content GoalPlanContent
	if len(plan.Content) > 0 {
		if err := json.Unmarshal(plan.Content, &content); err != nil {
			return GoalPlanResponse{}, fmt.Errorf("decode goal plan content: %w", err)
		}
	}
	if content.Items == nil {
		content.Items = []GoalPlanItem{}
	}
	if content.Edges == nil {
		content.Edges = []GoalPlanEdge{}
	}
	snapshot := json.RawMessage(plan.ContextSnapshot)
	if len(snapshot) == 0 {
		snapshot = json.RawMessage("{}")
	}
	id := uuidToString(plan.ID)
	createdAt := timestampToString(plan.CreatedAt)
	updatedAt := timestampToString(plan.UpdatedAt)
	return GoalPlanResponse{
		ID:              &id,
		GoalID:          uuidToString(goal.ID),
		ProjectID:       uuidToPtr(goal.ProjectID),
		Revision:        plan.Revision,
		State:           plan.State,
		Items:           content.Items,
		Edges:           content.Edges,
		ContextSnapshot: snapshot,
		CreatedAt:       &createdAt,
		UpdatedAt:       &updatedAt,
	}, nil
}

func goalPlanGenerationToResponse(gen db.GoalPlanGeneration, state string, currentRevision int32) GoalPlanGenerationResponse {
	input := json.RawMessage(gen.InputContext)
	if len(input) == 0 {
		input = json.RawMessage("{}")
	}
	candidate := json.RawMessage(gen.Candidate)
	if len(candidate) == 0 {
		candidate = nil
	}
	resp := GoalPlanGenerationResponse{
		ID:               uuidToString(gen.ID),
		GoalID:           uuidToString(gen.GoalID),
		ProjectID:        uuidToString(gen.ProjectID),
		RequestKey:       gen.RequestKey,
		Intent:           gen.Intent,
		AgentID:          uuidToString(gen.AgentID),
		BaseRevision:     gen.BaseRevision,
		State:            state,
		ChatSessionID:    uuidToPtr(gen.ChatSessionID),
		TaskID:           uuidToPtr(gen.TaskID),
		InputContext:     input,
		Candidate:        candidate,
		Applied:          gen.Applied,
		AppliedRevision:  int32ToPtr(gen.AppliedRevision),
		ObservedRevision: int32ToPtr(gen.ObservedRevision),
		CurrentRevision:  currentRevision,
		Error:            gen.Error,
		CreatedAt:        timestampToString(gen.CreatedAt),
		UpdatedAt:        timestampToString(gen.UpdatedAt),
	}
	resp.Stale = candidate != nil && !gen.Applied &&
		(currentRevision > gen.BaseRevision || (gen.ObservedRevision.Valid && gen.ObservedRevision.Int32 > gen.BaseRevision))
	return resp
}

func int32ToPtr(v pgtype.Int4) *int32 {
	if !v.Valid {
		return nil
	}
	out := v.Int32
	return &out
}

// effectiveGoalPlanGenerationState derives the run state from the task row for
// generations that have not reported a result yet. The daemon never parses the
// plan; the server therefore also recognises "task finished without a result"
// as a failure instead of leaving the generation queued forever.
func (h *Handler) effectiveGoalPlanGenerationState(ctx context.Context, gen db.GoalPlanGeneration) string {
	switch gen.State {
	case "succeeded", "failed", "preparing":
		return gen.State
	}
	if !gen.TaskID.Valid {
		return "queued"
	}
	task, err := h.Queries.GetAgentTask(ctx, gen.TaskID)
	if err != nil {
		return "queued"
	}
	switch task.Status {
	case "queued", "dispatched", "waiting_local_directory":
		return "queued"
	case "running":
		return "running"
	case "completed":
		if len(gen.Candidate) > 0 {
			return "succeeded"
		}
		return "failed"
	default:
		return "failed"
	}
}

// generationFailureText fills in the reason shown for a generation that failed
// without submitting one.
func (h *Handler) generationFailureText(ctx context.Context, gen db.GoalPlanGeneration, state string) string {
	if gen.Error != "" || state != "failed" {
		return gen.Error
	}
	if len(gen.Candidate) > 0 {
		return ""
	}
	if !gen.TaskID.Valid {
		return "the planning run could not be started"
	}
	task, err := h.Queries.GetAgentTask(ctx, gen.TaskID)
	if err == nil && task.Status == "completed" {
		return "the planning run finished without submitting a plan"
	}
	return "the planning run ended without a plan"
}

func (h *Handler) goalPlanRevision(ctx context.Context, q *db.Queries, goal db.Goal) (int32, bool, error) {
	plan, err := q.GetGoalPlan(ctx, db.GetGoalPlanParams{
		WorkspaceID: goal.WorkspaceID,
		GoalID:      goal.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return plan.Revision, true, nil
}

// GetGoalPlan returns the current draft, or a synthetic empty draft at
// revision 0 when the goal has never been saved.
func (h *Handler) GetGoalPlan(w http.ResponseWriter, r *http.Request) {
	goal, ok := h.loadVisibleGoal(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	plan, err := h.Queries.GetGoalPlan(r.Context(), db.GetGoalPlanParams{
		WorkspaceID: goal.WorkspaceID,
		GoalID:      goal.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeJSON(w, http.StatusOK, emptyGoalPlanResponse(goal))
		return
	}
	if err != nil {
		slog.Error("load goal plan failed",
			append(logger.RequestAttrs(r), "goal_id", uuidToString(goal.ID), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to load plan")
		return
	}
	// The goal's project may have become invisible since the plan was saved;
	// the loaded goal already answered 404 in that case.
	resp, err := goalPlanToResponse(goal, plan)
	if err != nil {
		slog.Error("decode goal plan failed",
			append(logger.RequestAttrs(r), "goal_id", uuidToString(goal.ID), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to load plan")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// PutGoalPlan saves the full edited draft under expected_revision. It never
// dispatches or assigns anything.
func (h *Handler) PutGoalPlan(w http.ResponseWriter, r *http.Request) {
	goal, ok := h.loadVisibleGoal(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var req PutGoalPlanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.ExpectedRevision < 0 {
		writeError(w, http.StatusBadRequest, "expected_revision must be zero or greater")
		return
	}
	snapshot, err := normalizeGoalPlanSnapshot(req.ContextSnapshot)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	locked, err := qtx.LockGoalForUpdate(r.Context(), db.LockGoalForUpdateParams{
		ID:          goal.ID,
		WorkspaceID: goal.WorkspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "goal not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to save plan")
		return
	}
	if !locked.ProjectID.Valid {
		writeErrorCode(w, http.StatusBadRequest, "goal_project_required",
			"link this goal to a project before saving a plan")
		return
	}
	if err := qtx.LockGoalPlanAdvisory(r.Context(), uuidToString(locked.ID)); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save plan")
		return
	}

	current, exists, err := h.goalPlanRevision(r.Context(), qtx, locked)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save plan")
		return
	}
	if req.ExpectedRevision != current {
		writeGoalPlanRevisionConflict(w, current)
		return
	}

	content, err := h.validateGoalPlanContent(r.Context(), qtx, locked.WorkspaceID, locked.ProjectID, req.Items, req.Edges, false)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	contentJSON, err := json.Marshal(content)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save plan")
		return
	}
	if len(contentJSON) > goalPlanMaxContentSize {
		writeError(w, http.StatusBadRequest, "plan is too large")
		return
	}

	var saved db.GoalPlan
	if exists {
		saved, err = qtx.UpdateGoalPlanContent(r.Context(), db.UpdateGoalPlanContentParams{
			Content:          contentJSON,
			ContextSnapshot:  snapshot,
			WorkspaceID:      locked.WorkspaceID,
			GoalID:           locked.ID,
			ExpectedRevision: req.ExpectedRevision,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			// Another writer moved the revision between the read and the CAS;
			// re-read it so the client can retry with the real value.
			current, _, readErr := h.goalPlanRevision(r.Context(), qtx, locked)
			if readErr != nil {
				writeError(w, http.StatusInternalServerError, "failed to save plan")
				return
			}
			writeGoalPlanRevisionConflict(w, current)
			return
		}
	} else {
		if req.ExpectedRevision != 0 {
			writeGoalPlanRevisionConflict(w, 0)
			return
		}
		saved, err = qtx.CreateGoalPlan(r.Context(), db.CreateGoalPlanParams{
			WorkspaceID:     locked.WorkspaceID,
			GoalID:          locked.ID,
			ProjectID:       locked.ProjectID,
			Content:         contentJSON,
			ContextSnapshot: snapshot,
		})
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save plan")
		return
	}

	if req.GenerationID != nil && strings.TrimSpace(*req.GenerationID) != "" {
		generationID, ok := parseUUIDOrBadRequest(w, strings.TrimSpace(*req.GenerationID), "generation_id")
		if !ok {
			return
		}
		generation, err := qtx.GetGoalPlanGeneration(r.Context(), db.GetGoalPlanGenerationParams{
			ID:          generationID,
			WorkspaceID: locked.WorkspaceID,
		})
		if err != nil || uuidToString(generation.GoalID) != uuidToString(locked.ID) {
			writeError(w, http.StatusNotFound, "generation not found")
			return
		}
		// A goal can move once it has no saved plan and no in-flight run, but a
		// finished candidate from its old project must never be applied to the
		// new one. It stays readable nowhere; the user starts a new run.
		if uuidToString(generation.ProjectID) != uuidToString(locked.ProjectID) {
			writeGoalPlanGenerationProjectConflict(w)
			return
		}
		if len(generation.Candidate) == 0 {
			writeErrorCode(w, http.StatusConflict, "generation_not_ready",
				"this generation has not submitted a plan yet")
			return
		}
		if generation.Applied {
			writeErrorCode(w, http.StatusConflict, "generation_already_applied",
				"this generation has already been applied to a saved revision")
			return
		}
		if err := qtx.MarkGoalPlanGenerationApplied(r.Context(), db.MarkGoalPlanGenerationAppliedParams{
			ID:              generation.ID,
			WorkspaceID:     generation.WorkspaceID,
			AppliedRevision: pgtype.Int4{Int32: saved.Revision, Valid: true},
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to save plan")
			return
		}
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit plan save")
		return
	}
	actorType, actorID := h.resolveActor(r, userID, uuidToString(locked.WorkspaceID))
	h.publishGoalChanged(uuidToString(locked.WorkspaceID), actorType, actorID)

	resp, err := goalPlanToResponse(locked, saved)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load plan")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func writeGoalPlanRevisionConflict(w http.ResponseWriter, current int32) {
	writeJSON(w, http.StatusConflict, map[string]any{
		"error":            "the plan has changed since it was loaded; re-read it and retry",
		"code":             "plan_revision_conflict",
		"current_revision": current,
	})
}

// writeGoalPlanProjectConflict refuses a generation whose goal changed project
// between this request's read and the protected transaction. Nothing from the
// old project is written or returned; the caller re-reads the goal and starts a
// new request.
func writeGoalPlanProjectConflict(w http.ResponseWriter) {
	writeErrorCode(w, http.StatusConflict, "goal_project_conflict",
		"the goal's project changed while this request was being prepared; re-read the goal and retry")
}

// writeGoalPlanGenerationProjectConflict refuses to read, apply, replay or
// accept the result of a run created for a project the goal no longer belongs
// to. The body carries no old-project input context or candidate.
func writeGoalPlanGenerationProjectConflict(w http.ResponseWriter) {
	writeErrorCode(w, http.StatusConflict, "generation_project_conflict",
		"this planning run belongs to a project the goal no longer uses; start a new run")
}

// CreateGoalPlanGeneration starts one planning run. It reuses the existing
// chat session -> agent task chain and records the request before anything is
// enqueued, so a retried request_key resolves to the original run.
func (h *Handler) CreateGoalPlanGeneration(w http.ResponseWriter, r *http.Request) {
	goal, ok := h.loadVisibleGoal(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := h.resolveWorkspaceID(r)
	var req CreateGoalPlanGenerationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	intent := strings.TrimSpace(req.Intent)
	if intent == "" {
		writeError(w, http.StatusBadRequest, "intent is required")
		return
	}
	if utf8.RuneCountInString(intent) > goalPlanMaxIntentRunes {
		writeError(w, http.StatusBadRequest, "intent is too long")
		return
	}
	requestKey := strings.TrimSpace(req.RequestKey)
	if requestKey == "" {
		writeError(w, http.StatusBadRequest, "request_key is required")
		return
	}
	if len(requestKey) > goalPlanMaxRequestKey {
		writeError(w, http.StatusBadRequest, "request_key is too long")
		return
	}
	if req.ExpectedRevision == nil || *req.ExpectedRevision < 0 {
		writeError(w, http.StatusBadRequest, "expected_revision is required")
		return
	}
	agentID, ok := parseUUIDOrBadRequest(w, req.AgentID, "agent_id")
	if !ok {
		return
	}
	if !goal.ProjectID.Valid {
		writeErrorCode(w, http.StatusBadRequest, "goal_project_required",
			"link this goal to a project before generating a plan")
		return
	}

	agent, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
		ID:          agentID,
		WorkspaceID: goal.WorkspaceID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}
	// The agent's archived/no-runtime state is checked during preparation below
	// instead of here: a replay of an already accepted request_key must keep
	// resolving to the run it created even when the agent became unavailable
	// afterwards. A genuinely new request still refuses with 409.
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	if !h.canInvokeAgent(r.Context(), agent, actorType, actorID, h.invokeOriginatorFromRequest(r, actorType, actorID), workspaceID) {
		writeError(w, http.StatusForbidden, "you do not have access to this agent")
		return
	}
	if !h.requireChatProjectVisible(w, r, workspaceID, goal.ProjectID, agent.ID) {
		return
	}

	// The whole run — the fixed input record, its chat session, the task, the
	// user message that carries the prompt and the generation -> task binding —
	// is written in one transaction with a single commit. A crash therefore
	// leaves either a complete queued run or nothing at all: no generation
	// stuck in 'preparing' without a task, and no task whose generation row
	// never learns it exists.
	//
	// Preparation does network I/O (the Composio overlay) and reads the
	// attribution policy, so it runs before the transaction. Its error is
	// stashed and only reported for a genuinely new request, after the in-lock
	// replay check: an accepted request keeps resolving to its original run
	// even when a later precondition would refuse a new one.
	sessionID := dbid.NewV7()
	prepared, prepErr := h.TaskService.PrepareDirectChatSend(
		r.Context(), sessionID, agent, parseUUID(userID),
	)

	// The locks are taken in the order the delete paths take them — workspace
	// delete locks the workspace first, project delete locks the project before
	// its goals — so this path cannot deadlock with either: a generation never
	// waits for a project while holding a goal.
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	if _, err := qtx.LockWorkspaceForChatSessionCreate(r.Context(), goal.WorkspaceID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "workspace not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to start planning run")
		return
	}
	// The project is locked from the project the goal had when this request was
	// read. The goal lock comes afterwards and the two are compared below: if
	// the goal moved in between, this run is refused rather than mixing the old
	// project's resources with a session created in the new one.
	if _, err := qtx.LockProjectForChatSessionCreate(r.Context(), db.LockProjectForChatSessionCreateParams{
		ID:          goal.ProjectID,
		WorkspaceID: goal.WorkspaceID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeGoalPlanProjectConflict(w)
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to start planning run")
		return
	}
	lockedGoal, err := qtx.LockGoalForUpdate(r.Context(), db.LockGoalForUpdateParams{
		ID:          goal.ID,
		WorkspaceID: goal.WorkspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "goal not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to start planning run")
		return
	}
	if !lockedGoal.ProjectID.Valid {
		writeErrorCode(w, http.StatusBadRequest, "goal_project_required",
			"link this goal to a project before generating a plan")
		return
	}
	if uuidToString(lockedGoal.ProjectID) != uuidToString(goal.ProjectID) {
		writeGoalPlanProjectConflict(w)
		return
	}
	if err := qtx.LockGoalPlanAdvisory(r.Context(), uuidToString(lockedGoal.ID)); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start planning run")
		return
	}

	existing, err := qtx.GetGoalPlanGenerationByRequestKey(r.Context(), db.GetGoalPlanGenerationByRequestKeyParams{
		WorkspaceID: lockedGoal.WorkspaceID,
		GoalID:      lockedGoal.ID,
		RequestKey:  requestKey,
	})
	if err == nil {
		if existing.Intent != intent || uuidToString(existing.AgentID) != uuidToString(agentID) ||
			existing.BaseRevision != *req.ExpectedRevision {
			writeErrorCode(w, http.StatusConflict, "generation_request_conflict",
				"request_key was already used for a different generation request")
			return
		}
		// A replay may only hand back a run of the goal's current project. The
		// goal can move once its plan and every in-flight run are gone, so an
		// old run is still readable by id; replaying its request_key must not
		// resurrect its old-project input or candidate.
		if uuidToString(existing.ProjectID) != uuidToString(lockedGoal.ProjectID) {
			writeGoalPlanGenerationProjectConflict(w)
			return
		}
		if err := tx.Commit(r.Context()); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to load generation")
			return
		}
		h.writeGoalPlanGenerationReplay(w, r, lockedGoal, existing)
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, "failed to start planning run")
		return
	}

	// Only a genuinely new request needs the preparation work; a failure here is
	// the user-facing refusal, and nothing has been written yet.
	if prepErr != nil {
		switch {
		case errors.Is(prepErr, service.ErrChatTaskAgentArchived):
			writeError(w, http.StatusConflict, "agent is archived")
		case errors.Is(prepErr, service.ErrChatTaskAgentNoRuntime):
			writeError(w, http.StatusConflict, "agent has no runtime")
		default:
			slog.Warn("prepare goal plan run failed",
				append(logger.RequestAttrs(r), "goal_id", uuidToString(lockedGoal.ID), "error", prepErr)...)
			writeError(w, http.StatusInternalServerError, "failed to start planning run")
		}
		return
	}

	// The draft is read under the goal lock and the advisory lock, so the
	// revision compared here is the one the run is fixed to. Everything the run
	// receives — base revision, input context, session project and prompt — is
	// derived from this one read and never refreshed afterwards.
	plan, hasPlan, err := loadGoalPlanDraft(r.Context(), qtx, lockedGoal)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start planning run")
		return
	}
	if plan.Revision != *req.ExpectedRevision {
		writeGoalPlanRevisionConflict(w, plan.Revision)
		return
	}
	input, err := h.buildGoalPlanGenerationInput(r.Context(), qtx, lockedGoal, plan, hasPlan)
	if err != nil {
		slog.Error("build goal plan input context failed",
			append(logger.RequestAttrs(r), "goal_id", uuidToString(lockedGoal.ID), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to read project context")
		return
	}

	generation, err := qtx.CreateGoalPlanGeneration(r.Context(), db.CreateGoalPlanGenerationParams{
		WorkspaceID:  lockedGoal.WorkspaceID,
		GoalID:       lockedGoal.ID,
		ProjectID:    input.Project.ID,
		RequestKey:   requestKey,
		Intent:       intent,
		AgentID:      agentID,
		BaseRevision: input.PlanRevision,
		InputContext: input.Snapshot,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start planning run")
		return
	}

	// The session id was preallocated before the transaction so the attribution
	// evidence ref written with the task names the session this transaction
	// creates.
	session, err := qtx.CreateChatSession(r.Context(), db.CreateChatSessionParams{
		ID:          sessionID,
		WorkspaceID: lockedGoal.WorkspaceID,
		AgentID:     agentID,
		CreatorID:   parseUUID(userID),
		Title:       lockedGoal.Title,
		ProjectID:   input.Project.ID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start planning run")
		return
	}
	if _, err := qtx.MarkChatSessionExplicitlyCreated(r.Context(), session.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start planning run")
		return
	}

	// The prompt is built from the same in-memory read that produced the stored
	// input; it performs no database access. The task, its self-owned input
	// batch and the prompt message are written into this transaction, not a
	// nested one.
	prompt := buildGoalPlanPrompt(lockedGoal, generation, input)
	sent, sendErr := h.TaskService.SendPreparedDirectChatMessageInTx(
		r.Context(), tx, session, parseUUID(userID), prompt, nil,
		actorType, parseUUID(actorID), prepared,
	)
	if sendErr != nil {
		// The whole transaction rolls back: a preparation or enqueue failure
		// leaves no accepted request, so a retry with the same request_key may
		// start a fresh run.
		switch {
		case errors.Is(sendErr, service.ErrChatTaskAgentArchived):
			writeError(w, http.StatusConflict, "agent is archived")
		case errors.Is(sendErr, service.ErrChatTaskAgentNoRuntime):
			writeError(w, http.StatusConflict, "agent has no runtime")
		case errors.Is(sendErr, service.ErrChatSessionArchived):
			writeError(w, http.StatusConflict, "chat session is archived")
		default:
			slog.Warn("enqueue goal plan run failed",
				append(logger.RequestAttrs(r), "generation_id", uuidToString(generation.ID), "error", sendErr)...)
			writeError(w, http.StatusInternalServerError, "failed to start planning run")
		}
		return
	}

	started, err := qtx.SetGoalPlanGenerationRun(r.Context(), db.SetGoalPlanGenerationRunParams{
		ChatSessionID: session.ID,
		TaskID:        sent.Task.ID,
		ID:            generation.ID,
		WorkspaceID:   generation.WorkspaceID,
	})
	if err != nil {
		// Zero rows means the row left 'preparing' (or was already bound) while
		// this transaction held the goal lock. Never commit a task no generation
		// points at, and never revive a finished run: the whole transaction
		// fails.
		slog.Warn("bind goal plan generation failed",
			append(logger.RequestAttrs(r), "generation_id", uuidToString(generation.ID), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to record the planning run")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		// The commit outcome is unknown. Never write a failure or delete the
		// session here: re-read the request key on a new connection, which only
		// finds the row if the commit actually landed, and let a retry with the
		// same key resolve to that run.
		if committed, ok := h.goalPlanGenerationCommittedByKey(lockedGoal, requestKey, intent, agentID, *req.ExpectedRevision); ok {
			h.writeGoalPlanGenerationReplay(w, r, lockedGoal, committed)
			return
		}
		slog.Warn("commit goal plan run unconfirmed",
			append(logger.RequestAttrs(r), "generation_id", uuidToString(generation.ID), "error", err)...)
		writeErrorCode(w, http.StatusServiceUnavailable, "goal_plan_generation_retryable",
			"the planning run could not be confirmed; retry with the same request_key")
		return
	}

	// Only after the single commit: the run is durable, so the queued task can
	// be announced.
	h.TaskService.FinalizeDirectChatSend(r.Context(), session, sent)
	writeJSON(w, http.StatusAccepted, goalPlanGenerationToResponse(started, "queued", generation.BaseRevision))
}

// writeGoalPlanGenerationReplay answers a request whose generation already
// exists — either a saved request_key replay or a commit that landed despite a
// failed commit call — without writing anything.
func (h *Handler) writeGoalPlanGenerationReplay(w http.ResponseWriter, r *http.Request, goal db.Goal, generation db.GoalPlanGeneration) {
	current, _, revErr := h.goalPlanRevision(r.Context(), h.Queries, goal)
	if revErr != nil {
		current = generation.BaseRevision
	}
	state := h.effectiveGoalPlanGenerationState(r.Context(), generation)
	generation.Error = h.generationFailureText(r.Context(), generation, state)
	writeJSON(w, http.StatusOK, goalPlanGenerationToResponse(generation, state, current))
}

// goalPlanGenerationCommittedByKey re-reads the request key after an uncertain
// commit. A matching row that already carries its task and session is proof
// that the transaction committed; a legacy unbound 'preparing' row (written by
// the pre-single-transaction code) is not.
func (h *Handler) goalPlanGenerationCommittedByKey(
	goal db.Goal, requestKey, intent string, agentID pgtype.UUID, baseRevision int32,
) (db.GoalPlanGeneration, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	existing, err := h.Queries.GetGoalPlanGenerationByRequestKey(ctx, db.GetGoalPlanGenerationByRequestKeyParams{
		WorkspaceID: goal.WorkspaceID,
		GoalID:      goal.ID,
		RequestKey:  requestKey,
	})
	if err != nil {
		return db.GoalPlanGeneration{}, false
	}
	if existing.Intent != intent || uuidToString(existing.AgentID) != uuidToString(agentID) ||
		existing.BaseRevision != baseRevision {
		return db.GoalPlanGeneration{}, false
	}
	if uuidToString(existing.ProjectID) != uuidToString(goal.ProjectID) {
		return db.GoalPlanGeneration{}, false
	}
	if existing.State == "preparing" || !existing.TaskID.Valid || !existing.ChatSessionID.Valid {
		return db.GoalPlanGeneration{}, false
	}
	return existing, true
}

func (h *Handler) GetGoalPlanGeneration(w http.ResponseWriter, r *http.Request) {
	goal, ok := h.loadVisibleGoal(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	generationID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "generationId"), "generation_id")
	if !ok {
		return
	}
	generation, err := h.Queries.GetGoalPlanGeneration(r.Context(), db.GetGoalPlanGenerationParams{
		ID:          generationID,
		WorkspaceID: goal.WorkspaceID,
	})
	if err != nil || uuidToString(generation.GoalID) != uuidToString(goal.ID) {
		writeError(w, http.StatusNotFound, "generation not found")
		return
	}
	// The goal may have moved after this run finished. Its candidate and input
	// evidence describe the old project, so they are not served for the new one.
	if uuidToString(generation.ProjectID) != uuidToString(goal.ProjectID) {
		writeGoalPlanGenerationProjectConflict(w)
		return
	}
	state := h.effectiveGoalPlanGenerationState(r.Context(), generation)
	generation.Error = h.generationFailureText(r.Context(), generation, state)
	current, _, err := h.goalPlanRevision(r.Context(), h.Queries, goal)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load plan")
		return
	}
	writeJSON(w, http.StatusOK, goalPlanGenerationToResponse(generation, state, current))
}

// SubmitGoalPlanGenerationResult is the dedicated result entry for a planning
// run. Only the task token minted for that run may call it, so an arbitrary run
// cannot write another goal's plan.
func (h *Handler) SubmitGoalPlanGenerationResult(w http.ResponseWriter, r *http.Request) {
	if !isGoalPlanRunActor(r) {
		writeErrorCode(w, http.StatusForbidden, "task_token_required",
			"only the planning run that owns this generation can submit its result")
		return
	}
	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	goalID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "goal id")
	if !ok {
		return
	}
	generationID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "generationId"), "generation_id")
	if !ok {
		return
	}
	taskID, ok := parseUUIDOrBadRequest(w, r.Header.Get("X-Task-ID"), "task_id")
	if !ok {
		return
	}

	var req GoalPlanResultRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	generation, err := h.Queries.GetGoalPlanGenerationByTask(r.Context(), db.GetGoalPlanGenerationByTaskParams{
		WorkspaceID: wsUUID,
		TaskID:      taskID,
	})
	// A missing generation and a mismatched path answer the same 404 so one
	// run cannot probe other goals' generations.
	if err != nil || uuidToString(generation.ID) != uuidToString(generationID) ||
		uuidToString(generation.GoalID) != uuidToString(goalID) {
		writeError(w, http.StatusNotFound, "generation not found")
		return
	}
	if agentID := r.Header.Get("X-Agent-ID"); agentID != "" && agentID != uuidToString(generation.AgentID) {
		writeError(w, http.StatusNotFound, "generation not found")
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record plan result")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	// Same lock order as every other plan writer: the goal, then the per-goal
	// advisory lock. The goal is re-read under its lock so the project compared
	// below is the current one, not the one this run was started with.
	goal, err := qtx.LockGoalForUpdate(r.Context(), db.LockGoalForUpdateParams{
		ID:          generation.GoalID,
		WorkspaceID: generation.WorkspaceID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "generation not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to record plan result")
		return
	}
	// Ownership already answered 404 above. A goal that moved to another
	// project while this run was out is a conflict: the candidate was produced
	// from the old project, so it is neither stored nor returned.
	if uuidToString(generation.ProjectID) != uuidToString(goal.ProjectID) {
		writeGoalPlanGenerationProjectConflict(w)
		return
	}
	if err := qtx.LockGoalPlanAdvisory(r.Context(), uuidToString(goal.ID)); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record plan result")
		return
	}
	current, _, err := h.goalPlanRevision(r.Context(), qtx, goal)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record plan result")
		return
	}

	var candidateJSON []byte
	errorText := strings.TrimSpace(req.Error)
	if errorText != "" {
		if utf8.RuneCountInString(errorText) > goalPlanTextFieldRunes {
			writeError(w, http.StatusBadRequest, "error is too long")
			return
		}
	} else {
		content, err := h.validateGoalPlanContent(r.Context(), qtx, goal.WorkspaceID, generation.ProjectID, req.Items, req.Edges, true)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if len(content.Items) == 0 {
			writeError(w, http.StatusBadRequest, "a generated plan needs at least one item")
			return
		}
		snapshot, err := normalizeGoalPlanSnapshot(req.ContextSnapshot)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		candidateJSON, err = json.Marshal(GoalPlanCandidate{
			Items:           content.Items,
			Edges:           content.Edges,
			ContextSnapshot: snapshot,
		})
		if err != nil || len(candidateJSON) > goalPlanMaxContentSize {
			writeError(w, http.StatusBadRequest, "plan is too large")
			return
		}
	}

	updated, err := qtx.CompleteGoalPlanGeneration(r.Context(), db.CompleteGoalPlanGenerationParams{
		Candidate:        candidateJSON,
		ObservedRevision: pgtype.Int4{Int32: current, Valid: true},
		Error:            errorText,
		ID:               generation.ID,
		WorkspaceID:      generation.WorkspaceID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// First result wins: an idempotent retry returns what was stored.
		stored, readErr := qtx.GetGoalPlanGeneration(r.Context(), db.GetGoalPlanGenerationParams{
			ID:          generation.ID,
			WorkspaceID: generation.WorkspaceID,
		})
		if readErr != nil {
			writeError(w, http.StatusInternalServerError, "failed to record plan result")
			return
		}
		if err := tx.Commit(r.Context()); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to record plan result")
			return
		}
		state := h.effectiveGoalPlanGenerationState(r.Context(), stored)
		stored.Error = h.generationFailureText(r.Context(), stored, state)
		writeJSON(w, http.StatusOK, goalPlanGenerationToResponse(stored, state, current))
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record plan result")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit plan result")
		return
	}

	state := h.effectiveGoalPlanGenerationState(r.Context(), updated)
	updated.Error = h.generationFailureText(r.Context(), updated, state)
	actorType, actorID := h.resolveActor(r, requestUserID(r), uuidToString(updated.WorkspaceID))
	h.publishGoalChanged(uuidToString(updated.WorkspaceID), actorType, actorID)
	resp := goalPlanGenerationToResponse(updated, state, current)
	if state == "succeeded" && current > updated.BaseRevision {
		// Explicit late-candidate signal: the stored candidate is kept, never
		// applied, and the client is told the draft has moved on.
		resp.Stale = true
	}
	writeJSON(w, http.StatusOK, resp)
}

// isGoalPlanRunActor reports whether the request authenticated as a task token.
// The Auth middleware sets X-Actor-Source only on that path and strips any
// client-supplied value, so the header is trustworthy here.
func isGoalPlanRunActor(r *http.Request) bool {
	return r.Header.Get("X-Actor-Source") == "task_token"
}

// normalizeGoalPlanSnapshot validates the stored context snapshot as a
// non-empty JSON object within the size budget.
func normalizeGoalPlanSnapshot(raw json.RawMessage) (json.RawMessage, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return json.RawMessage("{}"), nil
	}
	if len(trimmed) > goalPlanMaxSnapshotSize {
		return nil, errors.New("context_snapshot is too large")
	}
	var object map[string]any
	if err := json.Unmarshal([]byte(trimmed), &object); err != nil || object == nil {
		return nil, errors.New("context_snapshot must be a JSON object")
	}
	return json.RawMessage(trimmed), nil
}

// validateGoalPlanContent normalizes and validates a draft. assignMissingIDs is
// true only for a generation result: the model may omit item ids, and the
// server mints stable ones so the client can merge against them. A manual save
// must carry real ids, which keeps node identity stable across edits.
func (h *Handler) validateGoalPlanContent(
	ctx context.Context,
	q *db.Queries,
	workspaceID, projectID pgtype.UUID,
	items []GoalPlanItem,
	edges []GoalPlanEdge,
	assignMissingIDs bool,
) (GoalPlanContent, error) {
	if len(items) > goalPlanMaxItems {
		return GoalPlanContent{}, fmt.Errorf("a plan can have at most %d items", goalPlanMaxItems)
	}
	if len(edges) > goalPlanMaxEdges {
		return GoalPlanContent{}, fmt.Errorf("a plan can have at most %d edges", goalPlanMaxEdges)
	}
	out := GoalPlanContent{Items: make([]GoalPlanItem, 0, len(items)), Edges: make([]GoalPlanEdge, 0, len(edges))}
	seen := make(map[string]bool, len(items))
	for i, item := range items {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			if !assignMissingIDs {
				return GoalPlanContent{}, fmt.Errorf("items[%d].id is required", i)
			}
			id = uuidToString(dbid.NewV7())
		} else if _, err := parseUUIDString(id); err != nil {
			return GoalPlanContent{}, fmt.Errorf("items[%d].id must be a UUID", i)
		}
		if seen[id] {
			return GoalPlanContent{}, fmt.Errorf("items[%d].id is duplicated", i)
		}
		seen[id] = true

		title := strings.TrimSpace(item.Title)
		if title == "" {
			return GoalPlanContent{}, fmt.Errorf("items[%d].title is required", i)
		}
		if utf8.RuneCountInString(title) > goalPlanMaxTitleRunes {
			return GoalPlanContent{}, fmt.Errorf("items[%d].title is too long", i)
		}
		if err := checkGoalPlanText(item.Outcome, goalPlanTextFieldRunes); err != nil {
			return GoalPlanContent{}, fmt.Errorf("items[%d].outcome %s", i, err)
		}
		if err := checkGoalPlanText(item.AcceptanceCriteria, goalPlanTextFieldRunes); err != nil {
			return GoalPlanContent{}, fmt.Errorf("items[%d].acceptance_criteria %s", i, err)
		}

		evidence := make([]GoalPlanEvidenceRef, 0, len(item.EvidenceRefs))
		if len(item.EvidenceRefs) > goalPlanMaxEvidenceRefs {
			return GoalPlanContent{}, fmt.Errorf("items[%d] has too many evidence references", i)
		}
		for j, ref := range item.EvidenceRefs {
			kind := strings.TrimSpace(ref.Kind)
			if !isGoalEnumValue(kind, validGoalPlanEvidenceKinds) {
				return GoalPlanContent{}, fmt.Errorf("items[%d].evidence_refs[%d].kind is invalid", i, j)
			}
			value := strings.TrimSpace(ref.Ref)
			if value == "" {
				return GoalPlanContent{}, fmt.Errorf("items[%d].evidence_refs[%d].ref is required", i, j)
			}
			if err := checkGoalPlanText(value, goalPlanTextFieldRunes); err != nil {
				return GoalPlanContent{}, fmt.Errorf("items[%d].evidence_refs[%d].ref %s", i, j, err)
			}
			if err := checkGoalPlanText(ref.Note, goalPlanTextFieldRunes); err != nil {
				return GoalPlanContent{}, fmt.Errorf("items[%d].evidence_refs[%d].note %s", i, j, err)
			}
			if err := checkGoalPlanText(ref.Version, 200); err != nil {
				return GoalPlanContent{}, fmt.Errorf("items[%d].evidence_refs[%d].version %s", i, j, err)
			}
			evidence = append(evidence, GoalPlanEvidenceRef{
				Kind: kind, Ref: value, Note: strings.TrimSpace(ref.Note), Version: strings.TrimSpace(ref.Version),
			})
		}

		binding, err := h.validateGoalPlanBinding(ctx, q, workspaceID, projectID, item.Binding, i)
		if err != nil {
			return GoalPlanContent{}, err
		}

		var owner *GoalPlanProposedOwner
		if item.ProposedOwner != nil {
			ownerType := strings.TrimSpace(item.ProposedOwner.Type)
			if !isGoalEnumValue(ownerType, validGoalOwnerTypes) {
				return GoalPlanContent{}, fmt.Errorf("items[%d].proposed_owner.type is invalid", i)
			}
			ownerID, err := parseUUIDString(strings.TrimSpace(item.ProposedOwner.ID))
			if err != nil {
				return GoalPlanContent{}, fmt.Errorf("items[%d].proposed_owner.id must be a UUID", i)
			}
			owner = &GoalPlanProposedOwner{Type: ownerType, ID: uuidToString(ownerID)}
		}

		if item.Stage != nil && *item.Stage < 0 {
			return GoalPlanContent{}, fmt.Errorf("items[%d].stage must be zero or greater", i)
		}

		var coordination *GoalPlanCoordination
		if item.Coordination != nil {
			coordination, err = validateGoalPlanCoordination(item.Coordination, i)
			if err != nil {
				return GoalPlanContent{}, err
			}
		}

		out.Items = append(out.Items, GoalPlanItem{
			ID:                 id,
			Title:              title,
			Outcome:            strings.TrimSpace(item.Outcome),
			AcceptanceCriteria: strings.TrimSpace(item.AcceptanceCriteria),
			EvidenceRefs:       evidence,
			Binding:            binding,
			ProposedOwner:      owner,
			Stage:              item.Stage,
			Coordination:       coordination,
		})
	}

	seenEdges := make(map[string]bool, len(edges))
	for i, edge := range edges {
		dependent := strings.TrimSpace(edge.DependentItemID)
		prerequisite := strings.TrimSpace(edge.PrerequisiteItemID)
		if !seen[dependent] {
			return GoalPlanContent{}, fmt.Errorf("edges[%d].dependent_item_id does not match an item", i)
		}
		if !seen[prerequisite] {
			return GoalPlanContent{}, fmt.Errorf("edges[%d].prerequisite_item_id does not match an item", i)
		}
		if dependent == prerequisite {
			return GoalPlanContent{}, fmt.Errorf("edges[%d] is a self-dependency", i)
		}
		key := dependent + "\x00" + prerequisite
		if seenEdges[key] {
			return GoalPlanContent{}, fmt.Errorf("edges[%d] duplicates an existing dependency", i)
		}
		seenEdges[key] = true
		reason := strings.TrimSpace(edge.Reason)
		if reason == "" {
			return GoalPlanContent{}, fmt.Errorf("edges[%d].reason is required", i)
		}
		if err := checkGoalPlanText(reason, goalPlanTextFieldRunes); err != nil {
			return GoalPlanContent{}, fmt.Errorf("edges[%d].reason %s", i, err)
		}
		out.Edges = append(out.Edges, GoalPlanEdge{
			DependentItemID: dependent, PrerequisiteItemID: prerequisite, Reason: reason,
		})
	}
	if err := detectGoalPlanCycle(out.Edges); err != nil {
		return GoalPlanContent{}, err
	}
	return out, nil
}

func (h *Handler) validateGoalPlanBinding(
	ctx context.Context,
	q *db.Queries,
	workspaceID, projectID pgtype.UUID,
	binding GoalPlanItemBinding,
	index int,
) (GoalPlanItemBinding, error) {
	bindingType := strings.TrimSpace(binding.Type)
	if bindingType == "" {
		bindingType = "new"
	}
	if !isGoalEnumValue(bindingType, validGoalPlanBindingTypes) {
		return GoalPlanItemBinding{}, fmt.Errorf("items[%d].binding.type is invalid", index)
	}
	if bindingType == "new" {
		return GoalPlanItemBinding{Type: "new"}, nil
	}
	if binding.IssueID == nil || strings.TrimSpace(*binding.IssueID) == "" {
		return GoalPlanItemBinding{}, fmt.Errorf("items[%d].binding.issue_id is required for an existing binding", index)
	}
	issueID, err := parseUUIDString(strings.TrimSpace(*binding.IssueID))
	if err != nil {
		return GoalPlanItemBinding{}, fmt.Errorf("items[%d].binding.issue_id must be a UUID", index)
	}
	issue, err := q.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{
		ID:          issueID,
		WorkspaceID: workspaceID,
	})
	if err != nil || uuidToString(issue.ProjectID) != uuidToString(projectID) {
		return GoalPlanItemBinding{}, fmt.Errorf("items[%d].binding.issue_id is not an issue of this goal's project", index)
	}
	return GoalPlanItemBinding{Type: "existing", IssueID: uuidToPtr(issue.ID)}, nil
}

func validateGoalPlanCoordination(in *GoalPlanCoordination, index int) (*GoalPlanCoordination, error) {
	out := &GoalPlanCoordination{
		ParallelReason:   strings.TrimSpace(in.ParallelReason),
		IntegrationOwner: strings.TrimSpace(in.IntegrationOwner),
		NextEvaluation:   strings.TrimSpace(in.NextEvaluation),
	}
	for field, value := range map[string]string{
		"parallel_reason":   out.ParallelReason,
		"integration_owner": out.IntegrationOwner,
		"next_evaluation":   out.NextEvaluation,
	} {
		if err := checkGoalPlanText(value, goalPlanTextFieldRunes); err != nil {
			return nil, fmt.Errorf("items[%d].coordination.%s %s", index, field, err)
		}
	}
	var err error
	if out.WriteScope, err = normalizeGoalPlanStringList(in.WriteScope, "write_scope", index); err != nil {
		return nil, err
	}
	if out.ExclusiveResources, err = normalizeGoalPlanStringList(in.ExclusiveResources, "exclusive_resources", index); err != nil {
		return nil, err
	}
	return out, nil
}

func normalizeGoalPlanStringList(values []string, field string, index int) ([]string, error) {
	if len(values) > 20 {
		return nil, fmt.Errorf("items[%d].coordination.%s has too many entries", index, field)
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if err := checkGoalPlanText(trimmed, goalPlanMaxTitleRunes); err != nil {
			return nil, fmt.Errorf("items[%d].coordination.%s %s", index, field, err)
		}
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out, nil
}

func checkGoalPlanText(value string, maxRunes int) error {
	if utf8.RuneCountInString(value) > maxRunes {
		return fmt.Errorf("is too long")
	}
	return nil
}

// detectGoalPlanCycle rejects any prerequisite cycle. A draft that says A
// waits for B and B waits for A can never start, so saving it would only defer
// the failure.
func detectGoalPlanCycle(edges []GoalPlanEdge) error {
	next := make(map[string][]string, len(edges))
	nodes := make(map[string]bool, len(edges))
	for _, edge := range edges {
		next[edge.PrerequisiteItemID] = append(next[edge.PrerequisiteItemID], edge.DependentItemID)
		nodes[edge.PrerequisiteItemID] = true
		nodes[edge.DependentItemID] = true
	}
	const (
		unvisited = 0
		visiting  = 1
		done      = 2
	)
	state := make(map[string]int, len(nodes))
	var visit func(string) error
	visit = func(node string) error {
		switch state[node] {
		case visiting:
			return errors.New("plan dependencies contain a cycle")
		case done:
			return nil
		}
		state[node] = visiting
		for _, dependent := range next[node] {
			if err := visit(dependent); err != nil {
				return err
			}
		}
		state[node] = done
		return nil
	}
	for node := range nodes {
		if err := visit(node); err != nil {
			return err
		}
	}
	return nil
}

// goalPlanGenerationInput is everything one planning run is allowed to be based
// on. It is read inside the transaction that creates the generation, after the
// workspace, project and goal locks, so the stored input_context, the base
// revision, the chat session's project and the prompt all describe the same
// project and the same draft revision. Nothing re-reads the draft afterwards.
type goalPlanGenerationInput struct {
	Project      db.Project
	Resources    []db.ProjectResource
	PlanRevision int32
	PlanContent  json.RawMessage
	Snapshot     json.RawMessage
}

// loadGoalPlanDraft reads the saved draft inside the caller's transaction. The
// second result is false when the goal has never been saved; the draft is then
// the synthetic revision 0 with no content.
func loadGoalPlanDraft(ctx context.Context, q *db.Queries, goal db.Goal) (db.GoalPlan, bool, error) {
	plan, err := q.GetGoalPlan(ctx, db.GetGoalPlanParams{
		WorkspaceID: goal.WorkspaceID,
		GoalID:      goal.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.GoalPlan{}, false, nil
	}
	if err != nil {
		return db.GoalPlan{}, false, err
	}
	return plan, true, nil
}

// buildGoalPlanGenerationInput fixes the project, its own resources and the
// draft revision of one run. It must run inside the generation transaction
// after the project and goal locks; the result is the only source the caller
// uses for the stored input context, the base revision, the session project
// and the prompt. The workspace default repo is deliberately not consulted.
func (h *Handler) buildGoalPlanGenerationInput(
	ctx context.Context,
	q *db.Queries,
	goal db.Goal,
	plan db.GoalPlan,
	hasPlan bool,
) (goalPlanGenerationInput, error) {
	project, err := q.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{
		ID:          goal.ProjectID,
		WorkspaceID: goal.WorkspaceID,
	})
	if err != nil {
		return goalPlanGenerationInput{}, err
	}
	resources, err := q.ListProjectResourcesInWorkspace(ctx, db.ListProjectResourcesInWorkspaceParams{
		ProjectID:   project.ID,
		WorkspaceID: goal.WorkspaceID,
	})
	if err != nil {
		return goalPlanGenerationInput{}, err
	}
	input := goalPlanGenerationInput{Project: project, Resources: resources}
	if hasPlan {
		input.PlanRevision = plan.Revision
		if len(plan.Content) > 0 {
			input.PlanContent = json.RawMessage(plan.Content)
		}
	}
	snapshot, err := buildGoalPlanContextSnapshot(project, resources, input.PlanRevision, time.Now().UTC())
	if err != nil {
		return goalPlanGenerationInput{}, err
	}
	input.Snapshot = snapshot
	return input, nil
}

// buildGoalPlanContextSnapshot records what the request is based on: the
// project, its own resources and the draft revision the run is fixed to. A
// resource is a pointer, not proof of reading, so `read` starts false and the
// run's result fills in what it actually read.
func buildGoalPlanContextSnapshot(
	project db.Project,
	resources []db.ProjectResource,
	planRevision int32,
	readAt time.Time,
) (json.RawMessage, error) {
	resourceEntries := make([]map[string]any, 0, len(resources))
	missing := []string{}
	for _, resource := range resources {
		ref := json.RawMessage(resource.ResourceRef)
		if len(ref) == 0 {
			ref = json.RawMessage("{}")
		}
		entry := map[string]any{
			"id":   uuidToString(resource.ID),
			"type": resource.ResourceType,
			"ref":  ref,
			"read": false,
		}
		if label := textToPtr(resource.Label); label != nil {
			entry["label"] = *label
		}
		resourceEntries = append(resourceEntries, entry)
	}
	if len(resourceEntries) == 0 {
		missing = append(missing, "project_resources")
	}
	snapshot := map[string]any{
		"project_id":          uuidToString(project.ID),
		"project_title":       project.Title,
		"project_description": project.Description.String,
		"resources":           resourceEntries,
		"plan_revision":       planRevision,
		"missing":             missing,
		"read_at":             readAt.Format(time.RFC3339),
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return nil, fmt.Errorf("encode goal plan context snapshot: %w", err)
	}
	return encoded, nil
}

func buildGoalPlanPrompt(goal db.Goal, generation db.GoalPlanGeneration, input goalPlanGenerationInput) string {
	currentPlan := "（空：这是第一份计划草稿）"
	if len(input.PlanContent) > 0 {
		currentPlan = fmt.Sprintf("revision %d\n%s", input.PlanRevision, string(input.PlanContent))
	} else if input.PlanRevision > 0 {
		currentPlan = fmt.Sprintf("revision %d\n（空计划内容）", input.PlanRevision)
	}
	resourceLines := make([]string, 0, len(input.Resources))
	for _, resource := range input.Resources {
		ref := string(resource.ResourceRef)
		if ref == "" {
			ref = "{}"
		}
		label := ""
		if resource.Label.Valid {
			label = resource.Label.String
		}
		resourceLines = append(resourceLines, fmt.Sprintf(
			"- id=%s type=%s label=%s ref=%s", uuidToString(resource.ID), resource.ResourceType, label, ref))
	}
	if len(resourceLines) == 0 {
		resourceLines = []string{"（该项目还没有登记资源；请在 context_snapshot.missing 中如实记录缺失项，不要改用工作区默认仓库）"}
	}

	var b strings.Builder
	b.WriteString("你负责为下面这个目标生成一份可调整的项目计划草稿。只读分析，不要创建、指派或修改任何 issue，不要修改目标和项目，也不要开始实现。\n\n")
	fmt.Fprintf(&b, "## 目标\n- goal_id: %s\n- 标题: %s\n- 意图: %s\n", uuidToString(goal.ID), goal.Title, goal.Intent)
	if goal.Scope != "" {
		fmt.Fprintf(&b, "- 范围: %s\n", goal.Scope)
	}
	if goal.AcceptanceCriteria != "" {
		fmt.Fprintf(&b, "- 验收标准: %s\n", goal.AcceptanceCriteria)
	}
	fmt.Fprintf(&b, "\n## 本次请求\n- intent: %s\n- base_revision: %d\n- project_id: %s\n",
		generation.Intent, input.PlanRevision, uuidToString(input.Project.ID))
	if input.Project.Title != "" {
		fmt.Fprintf(&b, "- 项目: %s\n", input.Project.Title)
	}
	if input.Project.Description.Valid && input.Project.Description.String != "" {
		fmt.Fprintf(&b, "- 项目描述: %s\n", input.Project.Description.String)
	}
	b.WriteString("- 项目资源（只使用这些资源，不要改用工作区默认仓库）:\n")
	for _, line := range resourceLines {
		b.WriteString("  " + line + "\n")
	}
	fmt.Fprintf(&b, "\n## 当前计划\n%s\n\n", currentPlan)
	b.WriteString("## 输出要求\n")
	b.WriteString("1. 先实际阅读本项目资源的代码、AGENTS.md、README、相关文档和已有任务，再提出 1 到 8 个可独立验收的交付节点。\n")
	b.WriteString("2. 每个节点包含 title、outcome、acceptance_criteria；evidence_refs 至少一条，记录实际读到的依据：kind=resource|file|task|comment|contract，ref 写资源 ID/文件路径与行段/任务 UUID/评论 ID，version 写实际版本（仓库 SHA、任务 revision、评论 revision 等），note 写摘要。只有真正读到内容才算依据。\n")
	b.WriteString("3. 依赖写在 edges：dependent_item_id 依赖 prerequisite_item_id，reason 说明为什么必须等待；没有依赖表示可以并行。不能自依赖、不能成环、不能重复。\n")
	b.WriteString("4. 已有节点必须沿用原 id；新增节点请自行生成新的 UUID；删除节点直接从 items 去掉。标题不能作为去重键，不要复制已有事项。\n")
	b.WriteString("5. 复用已有任务时 binding 写 {\"type\":\"existing\",\"issue_id\":\"<uuid>\"}，且该 issue 必须属于本项目；新节点写 {\"type\":\"new\"}。proposed_owner 只是建议责任人，不会指派，格式 {\"type\":\"member|agent|squad\",\"id\":\"<uuid>\"}。\n")
	b.WriteString("6. 可选 coordination 记录：parallel_reason（为什么可以并行）、write_scope（将写入的路径/分支）、exclusive_resources（需要串行的独占资源）、integration_owner（集成负责人）、next_evaluation（下次重新评估触发的事件）。等待原因写在该边的 reason。\n")
	b.WriteString("7. context_snapshot 记录实际读取依据：repo_sha、files（path/lines/summary）、tasks（issue_id/revision/read_at）、resources（对应输入资源的 id 和 read=true）、missing（未读到的缺失项）、read_at。缺失项如实填写，不要猜。\n\n")
	b.WriteString("## 提交方式\n")
	b.WriteString("把结果写成一个 UTF-8 JSON 文件，结构为：\n")
	b.WriteString(`{"items":[...],"edges":[...],"context_snapshot":{...}}` + "\n")
	b.WriteString("然后运行（不要用 curl，也不要把计划贴进评论）：\n")
	fmt.Fprintf(&b, "  \"$MULTICA_CLI_PATH\" goal plan-result --goal %s --generation %s --file <上面的文件路径>\n", uuidToString(goal.ID), uuidToString(generation.ID))
	b.WriteString("提交成功即结束。若确实无法完成，用同一条命令加 --error \"<原因>\" 报告失败，不要编造依据。\n")
	return b.String()
}
