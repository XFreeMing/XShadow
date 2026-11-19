package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/pkg/acceptance"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/multica-ai/multica/server/pkg/workmethod"
)

func (h *Handler) ChangeIssueAcceptance(w http.ResponseWriter, r *http.Request) {
	r = h.withWakeupActor(r)
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var change acceptance.Change
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&change); err != nil {
		writeError(w, 400, "invalid acceptance change")
		return
	}
	trace, err := acceptance.Decode(issue.AcceptanceCriteria)
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	before := trace.Revision
	if err := trace.Apply(change); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, acceptance.ErrConflict) {
			status = http.StatusConflict
		}
		writeError(w, status, err.Error())
		return
	}
	if before == trace.Revision {
		writeJSON(w, 200, trace)
		return
	}
	raw, err := json.Marshal(trace)
	if err != nil {
		writeError(w, 500, "failed to encode acceptance records")
		return
	}
	ws := uuidToString(issue.WorkspaceID)
	actorType, actorID := h.resolveActor(r, userID, ws)
	var run workmethod.Run
	redispatch := false
	if len(issue.WorkMethodRun) > 0 {
		if err := json.Unmarshal(issue.WorkMethodRun, &run); err != nil {
			writeError(w, 500, "failed to read method run")
			return
		}
		if task, hasTask := h.taskFromRequestHeader(r); hasTask && methodBoundRevision(&task) > 0 {
			if task.IssueID != issue.ID || methodBoundRevision(&task) != run.Revision || isTerminalTaskStatus(task.Status) {
				writeError(w, 409, "来源运行已结束或工作方法已推进，请读取当前步骤")
				return
			}
		}
		if run.EvidenceGate() != "" && run.State != "ready" {
			var changed acceptance.Criterion
			for _, c := range trace.Criteria {
				if c.ID == change.CriterionID {
					changed = c
					break
				}
			}
			if step, ok := agileReworkStep(run, change.Action, changed); ok {
				from := run.Step
				run.State, run.Step = "working", step
				run.Revision++
				run.History = append(run.History, workmethod.Event{Action: "rework", FromStep: &from, Step: step, Evidence: fmt.Sprintf("%s（标准版本 v%d，验收记录修订 %d）发生 %s 变更，请核查受影响用例及候选提交。", change.CriterionID, len(changed.Versions), trace.Revision, change.Action), Actor: actorType + "/" + actorID, At: time.Now().UTC().Format(time.RFC3339Nano)})
				redispatch = true
			}
		} else if run.State == "closed" && run.Method != nil && run.Method.Kind == "development" {
			if issuestatus.Effective(r.Context(), h.Queries, issue.WorkspaceID, issue.Status) == "done" {
				writeError(w, 409, "请先重新打开已完成的事项，再修改研发验收记录")
				return
			}
			run.State = "review"
			run.Revision++
			run.History = append(run.History, workmethod.Event{Action: "review_reopened", Step: run.Step, Evidence: change.CriterionID + " 的验收记录已更新", Actor: actorType + "/" + actorID, At: time.Now().UTC().Format(time.RFC3339Nano)})
			redispatch = true
		}
	}
	var updated db.Issue
	if redispatch {
		updated, ok = h.persistMethodTransition(w, r, issue, &run, raw)
		if !ok {
			return
		}
	} else {
		updated, err = wakeupWrite(h, r, func(q *db.Queries) (db.Issue, error) {
			return q.UpdateIssueAcceptanceTrace(r.Context(), db.UpdateIssueAcceptanceTraceParams{ID: issue.ID, WorkspaceID: issue.WorkspaceID, Trace: raw, IssueRevision: issue.Revision})
		})
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, 409, acceptance.ErrConflict.Error())
			return
		}
		if err != nil {
			writeError(w, 500, "failed to save acceptance change")
			return
		}
	}
	resp := issueToResponse(updated, h.getIssuePrefix(r.Context(), issue.WorkspaceID))
	resp.AcceptanceTrace = &trace
	h.publish(protocol.EventIssueUpdated, ws, actorType, actorID, map[string]any{
		"issue":          resp,
		"status_changed": issue.Status != updated.Status, "prev_status": issue.Status,
		"assignee_changed":   issue.AssigneeType != updated.AssigneeType || issue.AssigneeID != updated.AssigneeID,
		"prev_assignee_type": textToPtr(issue.AssigneeType), "prev_assignee_id": uuidToPtr(issue.AssigneeID),
	})
	writeJSON(w, http.StatusOK, trace)
}

// Each changed artifact goes back to its own author for independent review.
// Edits during the current working step do not dispatch additional tasks.
func agileReworkStep(run workmethod.Run, action string, criterion acceptance.Criterion) (int, bool) {
	targetGate := "candidate"
	switch action {
	case "criterion":
		targetGate = "criteria"
	case "case":
		targetGate = "coverage"
	case "implementation":
	case "run", "case_retire":
		impact := criterion.Impact()
		if impact.CurrentImplementation && !impact.Uncovered && len(impact.Retest)+len(impact.Failed)+len(impact.Unrun) == 0 {
			return 0, false
		}
	default:
		return 0, false
	}
	for i, s := range run.Method.Steps {
		if s.Gate == targetGate && (i < run.Step || (i == run.Step && run.State != "working")) {
			return i, true
		}
	}
	return 0, false
}
