package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/issueworkflow"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/pkg/acceptance"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/multica-ai/multica/server/pkg/workmethod"
)

func (h *Handler) bindProjectDelivery(ctx context.Context, issue db.Issue, run *workmethod.Run) error {
	run.ProjectDelivery = nil
	if !issue.ProjectID.Valid || run.Method == nil || run.Method.Kind != "development" {
		return nil
	}
	rows, err := h.Queries.ListProjectResources(ctx, issue.ProjectID)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.ResourceType != "local_directory" || row.WorkspaceID != issue.WorkspaceID {
			continue
		}
		var ref localDirectoryRef
		if err := json.Unmarshal(row.ResourceRef, &ref); err != nil || ref.ActionsDelivery == nil {
			continue
		}
		if run.ProjectDelivery != nil {
			return errors.New("项目须指定唯一的 GitHub Actions 交付工作目录")
		}
		run.ProjectDelivery = &workmethod.ProjectDelivery{ResourceID: uuidToString(row.ID), DaemonID: ref.DaemonID, Directory: ref.LocalPath}
		method := *run.Method
		method.GitHubDelivery = ref.ActionsDelivery
		run.Method = &method
	}
	return nil
}

func projectDeliveryJob(issue db.Issue, run workmethod.Run) (workmethod.DeliveryJob, error) {
	approval := run.DeliveryApproval()
	if approval == nil {
		return workmethod.DeliveryJob{}, errMethodReviewOpen
	}
	job := workmethod.DeliveryJob{
		IssueID: uuidToString(issue.ID), Revision: run.Revision, Target: *run.Method.GitHubDelivery,
		Directory: run.ProjectDelivery.Directory, Head: approval.Closeout.Criteria[0].Candidate,
	}
	if approval.DeliveryReceipt != nil {
		job.Receipt = *approval.DeliveryReceipt
	}
	trace, err := acceptance.Decode(issue.AcceptanceCriteria)
	if err != nil {
		return job, err
	}
	var exact acceptance.Implementation
	for _, criterion := range trace.Criteria {
		var link acceptance.Implementation
		for _, item := range criterion.Implementations {
			if item.Version == len(criterion.Versions) {
				link = item
			}
		}
		if link.Commit != job.Head || !job.MatchesRepository(link.Repository) ||
			(exact.Branch != "" && exact.Branch != link.Branch) {
			return job, errors.New("交付须关联项目仓库中的同一候选分支和完整提交")
		}
		exact = link
	}
	job.Branch = exact.Branch
	if job.Branch == "" {
		return job, errMethodReviewOpen
	}
	return job, nil
}

func (h *Handler) projectDeliveryStillBound(ctx context.Context, issue db.Issue, run workmethod.Run) bool {
	row, err := h.Queries.GetProjectResourceInWorkspace(ctx, db.GetProjectResourceInWorkspaceParams{
		ID: parseUUIDOrInvalid(run.ProjectDelivery.ResourceID), WorkspaceID: issue.WorkspaceID,
	})
	var ref localDirectoryRef
	return err == nil && row.ProjectID == issue.ProjectID && row.ResourceType == "local_directory" &&
		json.Unmarshal(row.ResourceRef, &ref) == nil && ref.ActionsDelivery != nil &&
		*ref.ActionsDelivery == *run.Method.GitHubDelivery && ref.LocalPath == run.ProjectDelivery.Directory &&
		ref.DaemonID == run.ProjectDelivery.DaemonID
}

func (h *Handler) projectDeliveryDaemon(w http.ResponseWriter, r *http.Request) (pgtype.UUID, string, bool) {
	runtime, ok := h.requireDaemonRuntimeAccess(w, r, chi.URLParam(r, "runtimeId"))
	if ok && !runtime.DaemonID.Valid {
		writeError(w, http.StatusForbidden, "project delivery requires a bound daemon")
		return pgtype.UUID{}, "", false
	}
	return runtime.WorkspaceID, runtime.DaemonID.String, ok
}

func (h *Handler) ListProjectDeliveryJobs(w http.ResponseWriter, r *http.Request) {
	workspace, daemon, ok := h.projectDeliveryDaemon(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		writeError(w, 500, "could not read pending delivery")
		return
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT id, project_id, number, work_method_run, acceptance_criteria
FROM issue WHERE workspace_id=$1 AND status='in_review' AND work_method_run->>'state'='closed'
  AND work_method_run#>>'{project_delivery,daemon_id}'=$2
ORDER BY updated_at, id LIMIT 32`, workspace, daemon)
	if err != nil {
		writeError(w, 500, "could not read pending delivery")
		return
	}
	issues, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (db.Issue, error) {
		issue := db.Issue{WorkspaceID: workspace}
		err := row.Scan(&issue.ID, &issue.ProjectID, &issue.Number, &issue.WorkMethodRun, &issue.AcceptanceCriteria)
		return issue, err
	})
	if err != nil || tx.Commit(ctx) != nil {
		writeError(w, 500, "could not read pending delivery")
		return
	}
	jobs := make([]workmethod.DeliveryJob, 0, len(issues))
	prefix := h.getIssuePrefix(ctx, workspace)
	for _, issue := range issues {
		var run workmethod.Run
		if json.Unmarshal(issue.WorkMethodRun, &run) != nil {
			continue
		}
		job, err := projectDeliveryJob(issue, run)
		if err == nil && job.Receipt.State != workmethod.DeliveryDone && h.projectDeliveryStillBound(ctx, issue, run) {
			job.Identifier = fmt.Sprintf("%s-%d", prefix, issue.Number)
			jobs = append(jobs, job)
		}
	}
	writeJSON(w, 200, jobs)
}

func (h *Handler) ReportProjectDelivery(w http.ResponseWriter, r *http.Request) {
	workspace, daemon, ok := h.projectDeliveryDaemon(w, r)
	if !ok {
		return
	}
	id, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "issueId"), "issue_id")
	if !ok {
		return
	}
	var result struct {
		Revision int                        `json:"revision"`
		Head     string                     `json:"head"`
		Receipt  workmethod.DeliveryReceipt `json:"receipt"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&result) != nil || !result.Receipt.Valid() {
		writeError(w, 400, "invalid project delivery receipt")
		return
	}
	ctx := r.Context()
	issue, err := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{ID: id, WorkspaceID: workspace})
	var run workmethod.Run
	if err != nil || json.Unmarshal(issue.WorkMethodRun, &run) != nil ||
		run.ProjectDelivery == nil || run.ProjectDelivery.DaemonID != daemon {
		writeError(w, 404, "project delivery not found")
		return
	}
	job, err := projectDeliveryJob(issue, run)
	if err != nil || result.Revision != job.Revision || result.Head != job.Head || !h.projectDeliveryStillBound(ctx, issue, run) {
		writeError(w, 409, "the approved delivery changed")
		return
	}
	if job.Receipt == result.Receipt {
		w.WriteHeader(204)
		return
	}
	if issue.Status != issuestatus.InReview ||
		(job.Receipt.Merge != "" && job.Receipt.Merge != result.Receipt.Merge) ||
		(job.Receipt.PullRequest != 0 && job.Receipt.PullRequest != result.Receipt.PullRequest) {
		writeError(w, 409, "the delivery receipt is no longer current")
		return
	}
	var board pgtype.Text
	if result.Receipt.State == workmethod.DeliveryDone {
		def, _, err := issueworkflow.ForProject(ctx, h.Queries, workspace, issue.ProjectID)
		if err != nil || issueworkflow.CheckStatus(def, issuestatus.Done) != nil {
			writeError(w, 409, "project workflow cannot close this delivery")
			return
		}
		board = pgtype.Text{String: issuestatus.Done, Valid: true}
	}
	run.DeliveryApproval().DeliveryReceipt = &result.Receipt
	raw, err := json.Marshal(run)
	if err != nil {
		writeError(w, 500, "could not encode delivery")
		return
	}
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		writeError(w, 500, "could not save project delivery")
		return
	}
	defer tx.Rollback(ctx)
	q := h.Queries.WithTx(tx)
	updated, err := q.SaveIssueWorkMethodRun(ctx, db.SaveIssueWorkMethodRunParams{
		ID: id, WorkspaceID: workspace, IssueRevision: issue.Revision, Run: raw, BoardStatus: board,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 409, "the delivery changed while saving the receipt")
		return
	}
	var cancelled []db.AgentTaskQueue
	if err == nil && board.Valid {
		cancelled, err = service.StopClosedIssueWakeups(ctx, q, updated)
	}
	if err != nil || tx.Commit(ctx) != nil {
		writeError(w, 500, "could not save project delivery")
		return
	}
	h.broadcastCancelledWakeups(ctx, workspace, cancelled)
	if board.Valid && updated.ParentIssueID.Valid {
		h.processChildEvents(ctx, updated.ParentIssueID)
	}
	resp := issueToResponse(updated, h.getIssuePrefix(ctx, workspace))
	h.publish(protocol.EventIssueUpdated, uuidToString(workspace), "system", "", map[string]any{
		"issue": resp, "status_changed": updated.Status != issue.Status, "prev_status": issue.Status,
	})
	w.WriteHeader(http.StatusNoContent)
}
