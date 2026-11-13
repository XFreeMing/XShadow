package handler

// DEV-7 closure contract assembly. The deterministic judgement lives in
// internal/issueclosure; this file only loads the persisted facts, records
// independent reviews and authorizations, and exposes the decision to the
// status-write gate and to clients.
//
// Invariants:
//   - Reviewer and author identity/run/model are derived by the server. A
//     client-supplied reviewer name is never trusted.
//   - A grant is persisted only by a human caller, is scoped to one subject and
//     action, and is never inferred from a project or a parent relation.
//   - The success-terminal write (issue done / goal completed) shares the same
//     evaluation as the closure view; no separate "auto close" switch exists.

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

	"github.com/multica-ai/multica/server/internal/issueclosure"
	"github.com/multica-ai/multica/server/internal/issueworkflow"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	closureMaxTextRunes   = 2000
	closureMaxCriteria    = 50
	closureMaxPlatforms   = 20
	closureMaxMissing     = 20
	closureMaxIdempotency = 128
)

// ClosureReviewResponse is one stored verdict. Reviewed identity is echoed for
// audit; clients never submit it.
type ClosureReviewResponse struct {
	ID               string                         `json:"id"`
	SubjectType      string                         `json:"subject_type"`
	SubjectID        string                         `json:"subject_id"`
	Action           string                         `json:"action"`
	DeliveryVersion  string                         `json:"delivery_version"`
	DeliveryTree     string                         `json:"delivery_tree"`
	CriteriaRevision string                         `json:"criteria_revision"`
	AuthorType       string                         `json:"author_type"`
	AuthorID         *string                        `json:"author_id,omitempty"`
	AuthorModel      string                         `json:"author_model,omitempty"`
	ReviewerType     string                         `json:"reviewer_type"`
	ReviewerID       *string                        `json:"reviewer_id,omitempty"`
	ReviewerModel    string                         `json:"reviewer_model,omitempty"`
	Verdict          string                         `json:"verdict"`
	Criteria         []issueclosure.CriterionResult `json:"criteria"`
	Platforms        []string                       `json:"platforms"`
	Missing          []string                       `json:"missing"`
	Reason           string                         `json:"reason,omitempty"`
	SupersededBy     *string                        `json:"superseded_by,omitempty"`
	AcceptedAt       *string                        `json:"accepted_at,omitempty"`
	AcceptedByType   string                         `json:"accepted_by_type,omitempty"`
	AcceptedByID     *string                        `json:"accepted_by_id,omitempty"`
	RecordedAt       string                         `json:"recorded_at"`
}

// ClosureAuthorizationResponse is the readable grant fact.
type ClosureAuthorizationResponse struct {
	ID               string   `json:"id"`
	SubjectType      string   `json:"subject_type"`
	SubjectID        string   `json:"subject_id"`
	Actions          []string `json:"actions"`
	CoversChildren   bool     `json:"covers_children"`
	CriteriaRevision string   `json:"criteria_revision,omitempty"`
	State            string   `json:"state"`
	Revision         int32    `json:"revision"`
	SourceRef        string   `json:"source_ref,omitempty"`
	GrantedByType    string   `json:"granted_by_type,omitempty"`
	GrantedByID      *string  `json:"granted_by_id,omitempty"`
	ExpiresAt        *string  `json:"expires_at,omitempty"`
	RevokedAt        *string  `json:"revoked_at,omitempty"`
}

// ClosureResponse is the single read model shared by Web, Desktop and the
// status-write gate.
type ClosureResponse struct {
	SubjectType      string                        `json:"subject_type"`
	SubjectID        string                        `json:"subject_id"`
	Status           string                        `json:"status"`
	Managed          bool                          `json:"managed"`
	DeliveryVersion  string                        `json:"delivery_version,omitempty"`
	DeliveryTree     string                        `json:"delivery_tree,omitempty"`
	CriteriaRevision string                        `json:"criteria_revision,omitempty"`
	Outcome          string                        `json:"outcome"`
	Reason           string                        `json:"reason,omitempty"`
	Detail           string                        `json:"detail,omitempty"`
	NextActor        string                        `json:"next_actor,omitempty"`
	AlreadyAccepted  bool                          `json:"already_accepted"`
	Authorization    *ClosureAuthorizationResponse `json:"authorization,omitempty"`
	Reviews          []ClosureReviewResponse       `json:"reviews"`
}

// CreateClosureReviewRequest is what a reviewer submits. Delivery identity and
// verdict are required; identity of the reviewer is taken from the caller.
type CreateClosureReviewRequest struct {
	DeliveryVersion  string                         `json:"delivery_version"`
	DeliveryTree     string                         `json:"delivery_tree"`
	CriteriaRevision string                         `json:"criteria_revision"`
	Verdict          string                         `json:"verdict"`
	Criteria         []issueclosure.CriterionResult `json:"criteria"`
	Platforms        []string                       `json:"platforms"`
	Missing          []string                       `json:"missing"`
	Reason           string                         `json:"reason"`
	IdempotencyKey   string                         `json:"idempotency_key"`
}

// CreateClosureAuthorizationRequest grants a scoped closure action. Actions,
// scope and revision are explicit; nothing is implied.
type CreateClosureAuthorizationRequest struct {
	Actions          []string `json:"actions"`
	CoversChildren   bool     `json:"covers_children"`
	CriteriaRevision string   `json:"criteria_revision"`
	SourceRef        string   `json:"source_ref"`
	ExpiresAt        *string  `json:"expires_at"`
}

type closureSubject struct {
	kind         issueclosure.SubjectKind
	id           pgtype.UUID
	workspaceID  pgtype.UUID
	projectID    pgtype.UUID
	status       string
	managed      bool
	linkedGoalID pgtype.UUID
	author       issueclosure.Principal
	ownerType    string
	ownerID      pgtype.UUID
}

func (h *Handler) closureAgentPrincipal(ctx context.Context, actorType, actorID string) issueclosure.Principal {
	p := issueclosure.Principal{Type: actorType, ID: actorID}
	if actorType != "agent" || actorID == "" {
		return p
	}
	id, err := util.ParseUUID(actorID)
	if err != nil {
		return p
	}
	agent, err := h.Queries.GetAgent(ctx, id)
	if err != nil {
		return p
	}
	if agent.Model.Valid {
		p.Model = agent.Model.String
	}
	return p
}

func (h *Handler) closureCallerPrincipal(ctx context.Context, w http.ResponseWriter, r *http.Request, subject issueclosure.SubjectKind, subjectID pgtype.UUID) (issueclosure.Principal, bool) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return issueclosure.Principal{}, false
	}
	workspaceID := h.resolveWorkspaceID(r)
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	p := h.closureAgentPrincipal(ctx, actorType, actorID)
	if actorType == "agent" {
		if taskID, err := util.ParseUUID(r.Header.Get("X-Task-ID")); err == nil {
			if task, err := h.Queries.GetAgentTask(ctx, taskID); err == nil && task.IssueID == subjectID && subject == issueclosure.SubjectIssue {
				p.RunID = uuidToString(task.ID)
			}
		}
	}
	return p, true
}

func (h *Handler) loadIssueClosureSubject(ctx context.Context, issue db.Issue) (closureSubject, error) {
	s := closureSubject{
		kind:        issueclosure.SubjectIssue,
		id:          issue.ID,
		workspaceID: issue.WorkspaceID,
		projectID:   issue.ProjectID,
		status:      issue.Status,
		ownerType:   issue.AssigneeType.String,
		ownerID:     issue.AssigneeID,
	}
	link, err := h.Queries.GetGoalIssueLink(ctx, db.GetGoalIssueLinkParams{
		WorkspaceID: issue.WorkspaceID,
		IssueID:     issue.ID,
	})
	if err == nil {
		s.managed = true
		s.linkedGoalID = link.GoalID
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return s, err
	}
	if author, err := h.Queries.GetLatestIssueAuthor(ctx, issue.ID); err == nil {
		s.author = issueclosure.Principal{
			Type:  "agent",
			ID:    uuidToString(author.AgentID),
			RunID: uuidToString(author.TaskID),
			Model: author.Model,
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return s, err
	}
	return s, nil
}

func (h *Handler) loadGoalClosureSubject(ctx context.Context, goal db.Goal) (closureSubject, error) {
	s := closureSubject{
		kind:        issueclosure.SubjectGoal,
		id:          goal.ID,
		workspaceID: goal.WorkspaceID,
		projectID:   goal.ProjectID,
		status:      goal.Status,
		managed:     true,
		ownerType:   goal.OwnerType.String,
		ownerID:     goal.OwnerID,
	}
	if author, err := h.Queries.GetLatestGoalAuthor(ctx, db.GetLatestGoalAuthorParams{
		WorkspaceID: goal.WorkspaceID,
		GoalID:      goal.ID,
	}); err == nil {
		s.author = issueclosure.Principal{
			Type:  "agent",
			ID:    uuidToString(author.AgentID),
			RunID: uuidToString(author.TaskID),
			Model: author.Model,
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return s, err
	}
	return s, nil
}

// closureEnrolled reports whether the subject is under the closure contract.
// Issues are enrolled by an explicit goal link; goals by a persisted grant
// (a revoked grant still enrolls it, so it cannot fall back to the legacy path).
func (h *Handler) closureEnrolled(ctx context.Context, s closureSubject) (bool, error) {
	if s.kind == issueclosure.SubjectIssue {
		return s.managed, nil
	}
	_, err := h.Queries.GetLatestClosureAuthorization(ctx, db.GetLatestClosureAuthorizationParams{
		WorkspaceID: s.workspaceID,
		SubjectKind: string(s.kind),
		SubjectID:   s.id,
	})
	if err == nil {
		return true, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return false, err
}

func (h *Handler) closureReviews(ctx context.Context, s closureSubject) ([]db.ClosureReview, error) {
	rows, err := h.Queries.ListClosureReviews(ctx, db.ListClosureReviewsParams{
		WorkspaceID: s.workspaceID,
		SubjectKind: string(s.kind),
		SubjectID:   s.id,
	})
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func (h *Handler) closureAuthorization(ctx context.Context, s closureSubject) (*db.ClosureAuthorization, error) {
	row, err := h.Queries.GetLatestClosureAuthorization(ctx, db.GetLatestClosureAuthorizationParams{
		WorkspaceID: s.workspaceID,
		SubjectKind: string(s.kind),
		SubjectID:   s.id,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &row, nil
}

// closureWorkflowFacts walks the resolved workflow from the issue's current
// step. A step that hands off to a member (or resolves to one) owns a human
// decision; none is never treated as human approval.
func (h *Handler) closureWorkflowFacts(ctx context.Context, issue db.Issue) issueclosure.WorkflowFacts {
	def, project, err := issueworkflow.ForProject(ctx, h.Queries, issue.WorkspaceID, issue.ProjectID)
	if err != nil || def == nil || def.IsDefault {
		return issueclosure.WorkflowFacts{}
	}
	seen := map[string]bool{}
	for at := issue.Status; at != "" && !seen[at]; {
		seen[at] = true
		step, ok := def.Step(at)
		if !ok {
			break
		}
		if handlerType, handlerID, ok := issueworkflow.ResolveHandler(step, project, issue.CreatorType, issue.CreatorID); ok && handlerType == "member" {
			return issueclosure.WorkflowFacts{
				HumanStepPending: true,
				HumanActor:       uuidToString(handlerID),
			}
		}
		at = def.NextStatus(at)
	}
	return issueclosure.WorkflowFacts{}
}

func closureDelivery(reviews []db.ClosureReview) issueclosure.DeliveryVersion {
	for _, r := range reviews {
		if r.SupersededBy.Valid {
			continue
		}
		return issueclosure.DeliveryVersion{Version: r.DeliveryVersion, Tree: r.DeliveryTree}
	}
	return issueclosure.DeliveryVersion{}
}

func closureAcceptance(reviews []db.ClosureReview) *issueclosure.Acceptance {
	for _, r := range reviews {
		if !r.AcceptedAt.Valid {
			continue
		}
		return &issueclosure.Acceptance{
			ReviewID:        uuidToString(r.ID),
			AuthorizationID: uuidToString(r.AuthorizationID),
			Delivery:        issueclosure.DeliveryVersion{Version: r.DeliveryVersion, Tree: r.DeliveryTree},
			AcceptedBy: issueclosure.Principal{
				Type: r.AcceptedByType,
				ID:   uuidToString(r.AcceptedByID),
			},
			AcceptedAt: r.AcceptedAt.Time,
		}
	}
	return nil
}

func closureReviewInputs(reviews []db.ClosureReview) []issueclosure.Review {
	out := make([]issueclosure.Review, 0, len(reviews))
	for _, r := range reviews {
		out = append(out, issueclosure.Review{
			ID:               uuidToString(r.ID),
			SubjectKind:      issueclosure.SubjectKind(r.SubjectKind),
			SubjectID:        uuidToString(r.SubjectID),
			Action:           issueclosure.Action(r.Action),
			Delivery:         issueclosure.DeliveryVersion{Version: r.DeliveryVersion, Tree: r.DeliveryTree},
			CriteriaRevision: r.CriteriaRevision,
			Author: issueclosure.Principal{
				Type:  r.AuthorType,
				ID:    uuidToString(r.AuthorID),
				RunID: uuidToString(r.AuthorRunID),
				Model: r.AuthorModel,
			},
			Reviewer: issueclosure.Principal{
				Type:  r.ReviewerType,
				ID:    uuidToString(r.ReviewerID),
				RunID: uuidToString(r.ReviewerRunID),
				Model: r.ReviewerModel,
			},
			Verdict:    issueclosure.ReviewVerdict(r.Verdict),
			Criteria:   decodeCriterionResults(r.Criteria),
			Platforms:  decodeStrings(r.Platforms),
			Missing:    decodeStrings(r.Missing),
			Reason:     r.Reason,
			RecordedAt: r.RecordedAt.Time,
			SupersededBy: func() string {
				if r.SupersededBy.Valid {
					return uuidToString(r.SupersededBy)
				}
				return ""
			}(),
		})
	}
	return out
}

func closureAuthorizationInput(row *db.ClosureAuthorization) *issueclosure.Authorization {
	if row == nil {
		return nil
	}
	actions := make([]issueclosure.Action, 0, len(row.Actions))
	for _, a := range row.Actions {
		actions = append(actions, issueclosure.Action(a))
	}
	var expires *time.Time
	if row.ExpiresAt.Valid {
		t := row.ExpiresAt.Time
		expires = &t
	}
	return &issueclosure.Authorization{
		ID:               uuidToString(row.ID),
		WorkspaceID:      uuidToString(row.WorkspaceID),
		ProjectID:        uuidToString(row.ProjectID),
		SubjectKind:      issueclosure.SubjectKind(row.SubjectKind),
		SubjectID:        uuidToString(row.SubjectID),
		Actions:          actions,
		CoversChildren:   row.CoversChildren,
		CriteriaRevision: row.CriteriaRevision,
		State:            row.State,
		Revision:         int(row.Revision),
		ExpiresAt:        expires,
	}
}

func (h *Handler) evaluateClosure(ctx context.Context, s closureSubject, delivery issueclosure.DeliveryVersion, criteriaRevision string, caller issueclosure.Principal, callerCanClose bool) (issueclosure.Decision, *db.ClosureAuthorization, []db.ClosureReview, error) {
	reviews, err := h.closureReviews(ctx, s)
	if err != nil {
		return issueclosure.Decision{}, nil, nil, err
	}
	authRow, err := h.closureAuthorization(ctx, s)
	if err != nil {
		return issueclosure.Decision{}, nil, nil, err
	}
	if !delivery.Valid() {
		delivery = closureDelivery(reviews)
	}
	if criteriaRevision == "" {
		for _, r := range reviews {
			if !r.SupersededBy.Valid {
				criteriaRevision = r.CriteriaRevision
				break
			}
		}
	}
	in := issueclosure.Input{
		Now:              time.Now(),
		SubjectKind:      s.kind,
		SubjectID:        uuidToString(s.id),
		Action:           actionForSubject(s.kind),
		Status:           s.status,
		Managed:          s.managed,
		Delivery:         delivery,
		CriteriaRevision: criteriaRevision,
		Author:           s.author,
		Caller:           caller,
		CallerCanClose:   callerCanClose,
		Authorization:    closureAuthorizationInput(authRow),
		Acceptance:       closureAcceptance(reviews),
		Reviews:          closureReviewInputs(reviews),
	}
	if s.kind == issueclosure.SubjectIssue {
		in.Required, in.RequirementsKnown = h.issueRequiredDeliverables(ctx, s)
		issue, err := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: s.id, WorkspaceID: s.workspaceID})
		if err != nil {
			return issueclosure.Decision{}, nil, nil, err
		}
		in.Workflow = h.closureWorkflowFacts(ctx, issue)
	} else {
		// Goal acceptance has no additional delivery-condition source of its
		// own: its conditions are the goal's review criteria, carried by the
		// independent goal review. That is an explicit empty answer, not an
		// unread one.
		in.RequirementsKnown = true
	}
	return issueclosure.Evaluate(in), authRow, reviews, nil
}

// issueRequiredDeliverables reads the concrete delivery conditions recorded
// for one issue. A linked pull request is required until it is merged; a read
// failure is reported as unknown so the caller refuses rather than accepting
// an empty list. An issue with no linked pull request has no PR condition to
// satisfy (known, empty).
func (h *Handler) issueRequiredDeliverables(ctx context.Context, s closureSubject) ([]issueclosure.RequiredDeliverable, bool) {
	prs, err := h.Queries.ListIssueLinkedPullRequestStates(ctx, s.id)
	if err != nil {
		slog.Warn("closure: linked pull request states unreadable; treating conditions as unknown",
			"workspace_id", uuidToString(s.workspaceID),
			"issue_id", uuidToString(s.id),
			"error", err,
		)
		return nil, false
	}
	required := make([]issueclosure.RequiredDeliverable, 0, len(prs))
	for _, pr := range prs {
		required = append(required, issueclosure.RequiredDeliverable{
			Kind:      "pull_request",
			Ref:       fmt.Sprintf("%s #%d", pr.Provider, pr.PrNumber),
			Satisfied: pr.State == "merged",
		})
	}
	return required, true
}

func actionForSubject(kind issueclosure.SubjectKind) issueclosure.Action {
	if kind == issueclosure.SubjectGoal {
		return issueclosure.ActionGoalAccept
	}
	return issueclosure.ActionIssueClose
}

func (h *Handler) closureCloserAllowed(s closureSubject, p issueclosure.Principal) bool {
	if p.Type == "member" {
		return true
	}
	if p.Type != "agent" || p.ID == "" {
		return false
	}
	return s.ownerType == "agent" && uuidToString(s.ownerID) == p.ID
}

func (h *Handler) mustDecodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	return true
}

func validClosureVerdict(v string) bool {
	switch issueclosure.ReviewVerdict(v) {
	case issueclosure.VerdictPass, issueclosure.VerdictFail, issueclosure.VerdictNeedsChanges:
		return true
	}
	return false
}

func clampText(s string) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= closureMaxTextRunes {
		return s
	}
	return string([]rune(s)[:closureMaxTextRunes])
}

func (h *Handler) CreateIssueClosureReview(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	h.createClosureReview(w, r, issueclosure.SubjectIssue, issue.WorkspaceID, issue.ProjectID, issue.ID)
}

func (h *Handler) CreateGoalClosureReview(w http.ResponseWriter, r *http.Request) {
	goal, ok := h.loadVisibleGoal(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	h.createClosureReview(w, r, issueclosure.SubjectGoal, goal.WorkspaceID, goal.ProjectID, goal.ID)
}

func (h *Handler) createClosureReview(w http.ResponseWriter, r *http.Request, kind issueclosure.SubjectKind, workspaceID, projectID, subjectID pgtype.UUID) {
	if _, ok := requireUserID(w, r); !ok {
		return
	}
	var req CreateClosureReviewRequest
	if !h.mustDecodeJSON(w, r, &req) {
		return
	}
	req.DeliveryVersion = strings.TrimSpace(req.DeliveryVersion)
	req.DeliveryTree = strings.TrimSpace(req.DeliveryTree)
	if req.DeliveryVersion == "" || utf8.RuneCountInString(req.DeliveryVersion) > 200 {
		writeError(w, http.StatusBadRequest, "delivery_version is required")
		return
	}
	if !validClosureVerdict(req.Verdict) {
		writeError(w, http.StatusBadRequest, "verdict must be pass, fail or needs_changes")
		return
	}
	if len(req.Criteria) > closureMaxCriteria || len(req.Platforms) > closureMaxPlatforms || len(req.Missing) > closureMaxMissing {
		writeError(w, http.StatusBadRequest, "too many criteria, platforms or missing entries")
		return
	}
	if utf8.RuneCountInString(req.IdempotencyKey) > closureMaxIdempotency {
		writeError(w, http.StatusBadRequest, "idempotency_key is too long")
		return
	}

	var subject closureSubject
	var err error
	switch kind {
	case issueclosure.SubjectIssue:
		issue, loadErr := h.Queries.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{ID: subjectID, WorkspaceID: workspaceID})
		if loadErr != nil {
			if errors.Is(loadErr, pgx.ErrNoRows) {
				writeError(w, http.StatusNotFound, "issue not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "failed to load issue")
			return
		}
		subject, err = h.loadIssueClosureSubject(r.Context(), issue)
	case issueclosure.SubjectGoal:
		goal, loadErr := h.Queries.GetGoalInWorkspace(r.Context(), db.GetGoalInWorkspaceParams{ID: subjectID, WorkspaceID: workspaceID})
		if loadErr != nil {
			if errors.Is(loadErr, pgx.ErrNoRows) {
				writeError(w, http.StatusNotFound, "goal not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "failed to load goal")
			return
		}
		subject, err = h.loadGoalClosureSubject(r.Context(), goal)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to resolve closure subject")
		return
	}

	reviewer, ok := h.closureCallerPrincipal(r.Context(), w, r, kind, subjectID)
	if !ok {
		return
	}
	if !reviewer.Valid() {
		writeError(w, http.StatusForbidden, "reviewer identity is unavailable")
		return
	}
	criteriaJSON, _ := json.Marshal(nonNilCriteria(req.Criteria))
	platformsJSON, _ := json.Marshal(nonNilStrings(req.Platforms))
	missingJSON, _ := json.Marshal(nonNilStrings(req.Missing))

	if req.IdempotencyKey != "" {
		existing, err := h.Queries.GetClosureReviewByIdempotencyKey(r.Context(), db.GetClosureReviewByIdempotencyKeyParams{
			WorkspaceID:    workspaceID,
			IdempotencyKey: req.IdempotencyKey,
		})
		if err == nil {
			writeJSON(w, http.StatusOK, closureReviewResponse(existing))
			return
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusInternalServerError, "failed to check review replay")
			return
		}
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	created, err := qtx.CreateClosureReview(r.Context(), db.CreateClosureReviewParams{
		WorkspaceID:      workspaceID,
		ProjectID:        projectID,
		SubjectKind:      string(kind),
		SubjectID:        subjectID,
		Action:           string(actionForSubject(kind)),
		DeliveryVersion:  req.DeliveryVersion,
		DeliveryTree:     req.DeliveryTree,
		CriteriaRevision: clampText(req.CriteriaRevision),
		AuthorType:       subject.author.Type,
		AuthorID:         subject.authorID(),
		AuthorRunID:      subject.authorRunID(),
		AuthorModel:      subject.author.Model,
		ReviewerType:     reviewer.Type,
		ReviewerID:       subject.parseOrEmpty(reviewer.ID),
		ReviewerRunID:    subject.parseOrEmpty(reviewer.RunID),
		ReviewerModel:    reviewer.Model,
		Verdict:          req.Verdict,
		Criteria:         criteriaJSON,
		Platforms:        platformsJSON,
		Missing:          missingJSON,
		Reason:           clampText(req.Reason),
		IdempotencyKey:   req.IdempotencyKey,
	})
	if err != nil {
		if isUniqueViolation(err) {
			writeErrorCode(w, http.StatusConflict, "closure_review_duplicate", "this review was already recorded")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to record review")
		return
	}
	if err := qtx.SupersedeClosureReviews(r.Context(), db.SupersedeClosureReviewsParams{
		SupersededBy:    created.ID,
		WorkspaceID:     workspaceID,
		SubjectKind:     string(kind),
		SubjectID:       subjectID,
		DeliveryVersion: req.DeliveryVersion,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to supersede older reviews")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record review")
		return
	}
	writeJSON(w, http.StatusCreated, closureReviewResponse(created))
}

func (h *Handler) GetIssueClosure(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	subject, err := h.loadIssueClosureSubject(r.Context(), issue)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load closure state")
		return
	}
	h.writeClosureState(w, r, subject)
}

func (h *Handler) GetGoalClosure(w http.ResponseWriter, r *http.Request) {
	goal, ok := h.loadVisibleGoal(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	subject, err := h.loadGoalClosureSubject(r.Context(), goal)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load closure state")
		return
	}
	h.writeClosureState(w, r, subject)
}

func (h *Handler) writeClosureState(w http.ResponseWriter, r *http.Request, subject closureSubject) {
	caller, ok := h.closureCallerPrincipal(r.Context(), w, r, subject.kind, subject.id)
	if !ok {
		return
	}
	delivery := issueclosure.DeliveryVersion{
		Version: strings.TrimSpace(r.URL.Query().Get("delivery_version")),
		Tree:    strings.TrimSpace(r.URL.Query().Get("delivery_tree")),
	}
	decision, authRow, reviews, err := h.evaluateClosure(r.Context(), subject, delivery, strings.TrimSpace(r.URL.Query().Get("criteria_revision")), caller, h.closureCloserAllowed(subject, caller))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to evaluate closure")
		return
	}
	resp := ClosureResponse{
		SubjectType:      string(subject.kind),
		SubjectID:        uuidToString(subject.id),
		Status:           subject.status,
		Managed:          subject.managed,
		DeliveryVersion:  decision.Delivery.Version,
		DeliveryTree:     decision.Delivery.Tree,
		CriteriaRevision: strings.TrimSpace(r.URL.Query().Get("criteria_revision")),
		Outcome:          string(decision.Outcome),
		Reason:           string(decision.Reason),
		Detail:           decision.Detail,
		NextActor:        decision.NextActor,
		AlreadyAccepted:  decision.AlreadyAccepted,
		Authorization:    closureAuthorizationResponse(authRow),
		Reviews:          make([]ClosureReviewResponse, 0, len(reviews)),
	}
	for _, review := range reviews {
		resp.Reviews = append(resp.Reviews, closureReviewResponse(review))
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) GrantIssueClosureAuthorization(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	h.grantClosureAuthorization(w, r, issueclosure.SubjectIssue, issue.WorkspaceID, issue.ProjectID, issue.ID)
}

func (h *Handler) GrantGoalClosureAuthorization(w http.ResponseWriter, r *http.Request) {
	goal, ok := h.loadVisibleGoal(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	h.grantClosureAuthorization(w, r, issueclosure.SubjectGoal, goal.WorkspaceID, goal.ProjectID, goal.ID)
}

func (h *Handler) grantClosureAuthorization(w http.ResponseWriter, r *http.Request, kind issueclosure.SubjectKind, workspaceID, projectID, subjectID pgtype.UUID) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var req CreateClosureAuthorizationRequest
	if !h.mustDecodeJSON(w, r, &req) {
		return
	}
	if len(req.Actions) == 0 {
		writeError(w, http.StatusBadRequest, "actions is required")
		return
	}
	actions := make([]string, 0, len(req.Actions))
	seen := map[string]bool{}
	for _, a := range req.Actions {
		action := issueclosure.Action(strings.TrimSpace(a))
		switch action {
		case issueclosure.ActionIssueClose:
			if kind == issueclosure.SubjectGoal {
				writeError(w, http.StatusBadRequest, "a goal grant cannot include issue_close; grant issue closure on the issue or its goal scope")
				return
			}
		case issueclosure.ActionGoalAccept:
			if kind != issueclosure.SubjectGoal {
				writeError(w, http.StatusBadRequest, "goal_accept is only valid on a goal")
				return
			}
		default:
			writeError(w, http.StatusBadRequest, "unknown action")
			return
		}
		if !seen[string(action)] {
			seen[string(action)] = true
			actions = append(actions, string(action))
		}
	}
	if !seen[string(issueclosure.ActionIssueClose)] && !seen[string(issueclosure.ActionGoalAccept)] {
		writeError(w, http.StatusBadRequest, "actions is required")
		return
	}
	var expires *time.Time
	if req.ExpiresAt != nil && strings.TrimSpace(*req.ExpiresAt) != "" {
		t, err := time.Parse(time.RFC3339, strings.TrimSpace(*req.ExpiresAt))
		if err != nil {
			writeError(w, http.StatusBadRequest, "expires_at must be RFC3339")
			return
		}
		expires = &t
	}
	actorType, actorID := h.resolveActor(r, userID, uuidToString(workspaceID))
	grantedBy, _ := util.ParseUUID(actorID)
	auth := db.CreateClosureAuthorizationParams{
		WorkspaceID:      workspaceID,
		ProjectID:        projectID,
		SubjectKind:      string(kind),
		SubjectID:        subjectID,
		Actions:          actions,
		CoversChildren:   req.CoversChildren,
		CriteriaRevision: clampText(req.CriteriaRevision),
		SourceRef:        clampText(req.SourceRef),
		GrantedByType:    actorType,
		GrantedByID:      grantedBy,
		ExpiresAt:        pgtype.Timestamptz{},
	}
	if expires != nil {
		auth.ExpiresAt = pgtype.Timestamptz{Time: *expires, Valid: true}
	}
	created, err := h.Queries.CreateClosureAuthorization(r.Context(), auth)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to grant authorization")
		return
	}
	writeJSON(w, http.StatusCreated, closureAuthorizationResponse(&created))
}

// closureReviewResponse maps a stored verdict for clients.
func closureReviewResponse(r db.ClosureReview) ClosureReviewResponse {
	var superseded, acceptedBy *string
	if r.SupersededBy.Valid {
		s := uuidToString(r.SupersededBy)
		superseded = &s
	}
	if r.AcceptedByID.Valid {
		s := uuidToString(r.AcceptedByID)
		acceptedBy = &s
	}
	var acceptedAt *string
	if r.AcceptedAt.Valid {
		s := timestampToString(r.AcceptedAt)
		acceptedAt = &s
	}
	return ClosureReviewResponse{
		ID:               uuidToString(r.ID),
		SubjectType:      r.SubjectKind,
		SubjectID:        uuidToString(r.SubjectID),
		Action:           r.Action,
		DeliveryVersion:  r.DeliveryVersion,
		DeliveryTree:     r.DeliveryTree,
		CriteriaRevision: r.CriteriaRevision,
		AuthorType:       r.AuthorType,
		AuthorID:         uuidToPtr(r.AuthorID),
		AuthorModel:      r.AuthorModel,
		ReviewerType:     r.ReviewerType,
		ReviewerID:       uuidToPtr(r.ReviewerID),
		ReviewerModel:    r.ReviewerModel,
		Verdict:          r.Verdict,
		Criteria:         decodeCriterionResults(r.Criteria),
		Platforms:        nonNilStrings(decodeStrings(r.Platforms)),
		Missing:          nonNilStrings(decodeStrings(r.Missing)),
		Reason:           r.Reason,
		SupersededBy:     superseded,
		AcceptedAt:       acceptedAt,
		AcceptedByType:   r.AcceptedByType,
		AcceptedByID:     acceptedBy,
		RecordedAt:       timestampToString(r.RecordedAt),
	}
}

func closureAuthorizationResponse(a *db.ClosureAuthorization) *ClosureAuthorizationResponse {
	if a == nil {
		return nil
	}
	var grantedBy *string
	if a.GrantedByID.Valid {
		s := uuidToString(a.GrantedByID)
		grantedBy = &s
	}
	var expires, revoked *string
	if a.ExpiresAt.Valid {
		s := timestampToString(a.ExpiresAt)
		expires = &s
	}
	if a.RevokedAt.Valid {
		s := timestampToString(a.RevokedAt)
		revoked = &s
	}
	return &ClosureAuthorizationResponse{
		ID:               uuidToString(a.ID),
		SubjectType:      a.SubjectKind,
		SubjectID:        uuidToString(a.SubjectID),
		Actions:          a.Actions,
		CoversChildren:   a.CoversChildren,
		CriteriaRevision: a.CriteriaRevision,
		State:            a.State,
		Revision:         a.Revision,
		SourceRef:        a.SourceRef,
		GrantedByType:    a.GrantedByType,
		GrantedByID:      grantedBy,
		ExpiresAt:        expires,
		RevokedAt:        revoked,
	}
}

// ensureClosureOnSuccessTerminal is the shared gate for the success status
// write. It runs the same evaluation the read model exposes and refuses the
// write unless the decision is can_close; on can_close it persists the
// acceptance on the review row so downstream work can see it.
func (h *Handler) ensureClosureOnIssueDone(ctx context.Context, issue db.Issue, caller issueclosure.Principal, canClose bool) (issueclosure.Decision, error) {
	subject, err := h.loadIssueClosureSubject(ctx, issue)
	if err != nil {
		return issueclosure.Decision{}, err
	}
	enrolled, err := h.closureEnrolled(ctx, subject)
	if err != nil {
		return issueclosure.Decision{}, err
	}
	if !enrolled {
		return issueclosure.Decision{Outcome: issueclosure.CanClose}, nil
	}
	decision, _, reviews, err := h.evaluateClosure(ctx, subject, issueclosure.DeliveryVersion{}, "", caller, canClose)
	if err != nil {
		return issueclosure.Decision{}, err
	}
	if decision.Outcome != issueclosure.CanClose {
		return decision, nil
	}
	if !decision.AlreadyAccepted {
		if err := h.persistClosureAcceptance(ctx, reviews, decision, caller); err != nil {
			return issueclosure.Decision{}, err
		}
	}
	return decision, nil
}

func (h *Handler) ensureClosureOnGoalCompleted(ctx context.Context, goal db.Goal, caller issueclosure.Principal, canClose bool) (issueclosure.Decision, error) {
	subject, err := h.loadGoalClosureSubject(ctx, goal)
	if err != nil {
		return issueclosure.Decision{}, err
	}
	enrolled, err := h.closureEnrolled(ctx, subject)
	if err != nil {
		return issueclosure.Decision{}, err
	}
	if !enrolled {
		return issueclosure.Decision{Outcome: issueclosure.CanClose}, nil
	}
	decision, _, reviews, err := h.evaluateClosure(ctx, subject, issueclosure.DeliveryVersion{}, "", caller, canClose)
	if err != nil {
		return issueclosure.Decision{}, err
	}
	if decision.Outcome != issueclosure.CanClose {
		return decision, nil
	}
	if !decision.AlreadyAccepted {
		if err := h.persistClosureAcceptance(ctx, reviews, decision, caller); err != nil {
			return issueclosure.Decision{}, err
		}
	}
	return decision, nil
}

func (h *Handler) persistClosureAcceptance(ctx context.Context, reviews []db.ClosureReview, decision issueclosure.Decision, caller issueclosure.Principal) error {
	if decision.ReviewID == "" {
		return fmt.Errorf("closure decision has no review to accept")
	}
	reviewID, err := util.ParseUUID(decision.ReviewID)
	if err != nil {
		return err
	}
	authorizationID, err := util.ParseUUID(decision.AuthorizationID)
	if err != nil {
		return fmt.Errorf("closure decision has no authorization to record: %w", err)
	}
	callerID, err := util.ParseUUID(caller.ID)
	if err != nil {
		return err
	}
	var workspaceID pgtype.UUID
	for _, r := range reviews {
		if uuidToString(r.ID) == decision.ReviewID {
			workspaceID = r.WorkspaceID
			break
		}
	}
	if !workspaceID.Valid {
		return fmt.Errorf("closure review not found for acceptance")
	}
	_, err = h.Queries.AcceptClosureReview(ctx, db.AcceptClosureReviewParams{
		ID:              reviewID,
		WorkspaceID:     workspaceID,
		AcceptedByType:  caller.Type,
		AcceptedByID:    callerID,
		AuthorizationID: authorizationID,
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	return nil
}

func (s closureSubject) authorID() pgtype.UUID {
	id, _ := util.ParseUUID(s.author.ID)
	return id
}

func (s closureSubject) authorRunID() pgtype.UUID {
	id, _ := util.ParseUUID(s.author.RunID)
	return id
}

func (s closureSubject) parseOrEmpty(value string) pgtype.UUID {
	id, err := util.ParseUUID(value)
	if err != nil {
		return pgtype.UUID{}
	}
	return id
}

func decodeStrings(raw []byte) []string {
	if len(raw) == 0 {
		return nil
	}
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

func decodeCriterionResults(raw []byte) []issueclosure.CriterionResult {
	if len(raw) == 0 {
		return nil
	}
	var out []issueclosure.CriterionResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

func nonNilStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

func nonNilCriteria(in []issueclosure.CriterionResult) []issueclosure.CriterionResult {
	if in == nil {
		return []issueclosure.CriterionResult{}
	}
	return in
}

// closureRefusal is returned by the status-write gate when the prospective
// success-terminal write has not earned a close. It carries the same decision
// the read model exposes so the caller can see what is missing.
type closureRefusal struct {
	decision issueclosure.Decision
}

func (e *closureRefusal) Error() string {
	if e.decision.Detail != "" {
		return e.decision.Detail
	}
	return "closure is not permitted: " + string(e.decision.Reason)
}

// closureGateIssueDone blocks an agent's own done write on an enrolled issue
// until the contract allows it. A member write is a human decision and is not
// gated. A run with no authoritative identity cannot close.
func (h *Handler) closureGateIssueDone(ctx context.Context, issue db.Issue, run *db.AgentTaskQueue) error {
	if run == nil {
		return nil
	}
	caller := issueclosure.Principal{
		Type:  "agent",
		ID:    uuidToString(run.AgentID),
		RunID: uuidToString(run.ID),
	}
	if agent, err := h.Queries.GetAgent(ctx, run.AgentID); err == nil && agent.Model.Valid {
		caller.Model = agent.Model.String
	}
	subject, err := h.loadIssueClosureSubject(ctx, issue)
	if err != nil {
		return err
	}
	decision, err := h.ensureClosureOnIssueDone(ctx, issue, caller, h.closureCloserAllowed(subject, caller))
	if err != nil {
		return err
	}
	if decision.Outcome != issueclosure.CanClose {
		return &closureRefusal{decision: decision}
	}
	return nil
}

// writeClosureRefusal answers a refused success-terminal write with the same
// decision the closure read model exposes.
func writeClosureRefusal(w http.ResponseWriter, decision issueclosure.Decision) {
	writeJSON(w, http.StatusConflict, map[string]any{
		"error":      (&closureRefusal{decision: decision}).Error(),
		"code":       "closure_not_allowed",
		"outcome":    string(decision.Outcome),
		"reason":     string(decision.Reason),
		"detail":     decision.Detail,
		"next_actor": decision.NextActor,
	})
}

// closureGateGoalCompleted is the goal-side counterpart of
// closureGateIssueDone. Goals enroll by an explicit grant; without one the
// legacy update path is unchanged.
func (h *Handler) closureGateGoalCompleted(ctx context.Context, goal db.Goal, caller issueclosure.Principal, canClose bool) error {
	subject, err := h.loadGoalClosureSubject(ctx, goal)
	if err != nil {
		return err
	}
	enrolled, err := h.closureEnrolled(ctx, subject)
	if err != nil {
		return err
	}
	if !enrolled {
		return nil
	}
	decision, err := h.ensureClosureOnGoalCompleted(ctx, goal, caller, canClose)
	if err != nil {
		return err
	}
	if decision.Outcome != issueclosure.CanClose {
		return &closureRefusal{decision: decision}
	}
	return nil
}

// closureRequestAgentPrincipal resolves an agent caller for a handler request.
// The run id is only attached when the token's task actually belongs to the
// agent, so a stale header cannot manufacture a different run identity.
func (h *Handler) closureRequestAgentPrincipal(ctx context.Context, r *http.Request, actorID string) issueclosure.Principal {
	p := h.closureAgentPrincipal(ctx, "agent", actorID)
	agentID, err := util.ParseUUID(actorID)
	if err != nil {
		return p
	}
	taskID, err := util.ParseUUID(r.Header.Get("X-Task-ID"))
	if err != nil {
		return p
	}
	if task, err := h.Queries.GetAgentTask(ctx, taskID); err == nil && task.AgentID == agentID {
		p.RunID = uuidToString(task.ID)
	}
	return p
}
