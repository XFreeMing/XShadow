package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/multica-ai/multica/server/pkg/workmethod"
)

func (h *Handler) adoptIssueMethod(w http.ResponseWriter, r *http.Request, issue db.Issue, userID string, change workmethod.Change) {
	ctx := r.Context()
	ws := uuidToString(issue.WorkspaceID)
	kind, actor := h.resolveActor(r, userID, ws)
	if kind != "member" {
		writeError(w, 403, "只有成员能主动更换正在执行的方法版本")
		return
	}
	_, delivery, err := h.issueWorkMethod(ctx, issue, nil)
	if err != nil {
		writeError(w, 500, "failed to read the issue delivery contract")
		return
	}
	h.adoptIssueMethodViaGo(w, r, issue, kind, actor, ws, delivery, change)
}

func (h *Handler) adoptIssueMethodViaGo(
	w http.ResponseWriter,
	r *http.Request,
	issue db.Issue,
	kind, actor, ws string,
	delivery *issuePlanContract,
	change workmethod.Change,
) {
	ctx := r.Context()
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		writeError(w, 500, "failed to start method adoption")
		return
	}
	defer tx.Rollback(ctx)
	q := h.Queries.WithTx(tx)
	if _, err := q.LockWorkMethodTemplates(ctx, issue.WorkspaceID); err != nil {
		writeError(w, 500, "failed to lock the method catalog")
		return
	}
	catalog, err := service.WorkMethodCatalog(ctx, q, issue.WorkspaceID)
	if err != nil {
		writeError(w, 500, "failed to read the method catalog")
		return
	}
	locked, err := q.LockWakeupIssue(ctx, issue.ID)
	if err != nil || locked.WorkspaceID != issue.WorkspaceID || locked.Revision != issue.Revision {
		writeError(w, 409, "事项已变化，请重新读取")
		return
	}
	var run workmethod.Run
	if len(locked.WorkMethodRun) > 0 {
		if err = json.Unmarshal(locked.WorkMethodRun, &run); err != nil {
			writeError(w, 500, "failed to read the method snapshot")
			return
		}
	}
	if run.CurrentActor == nil {
		run.CurrentActor, err = h.methodRecipient(ctx, locked, run)
		if err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
	}
	before := run.Revision
	if err := run.Adopt(change, catalog, "member/"+actor, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, workmethod.ErrConflict) {
			status = http.StatusConflict
		}
		writeError(w, status, err.Error())
		return
	}
	changed := before != run.Revision
	if changed {
		raw, err := json.Marshal(run)
		if err != nil {
			writeError(w, 500, "failed to encode the method snapshot")
			return
		}
		locked, err = q.SaveIssueWorkMethodRun(ctx, db.SaveIssueWorkMethodRunParams{ID: issue.ID, WorkspaceID: issue.WorkspaceID, IssueRevision: locked.Revision, Run: raw})
		if err != nil {
			writeError(w, 500, "failed to save the method revision")
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		writeError(w, 500, "failed to commit method adoption")
		return
	}
	if changed {
		h.publish(protocol.EventIssueUpdated, ws, kind, actor, map[string]any{"issue": issueToResponse(locked, h.getIssuePrefix(ctx, issue.WorkspaceID))})
	}
	h.writeIssueMethodView(w, r, locked, run, delivery)
}
