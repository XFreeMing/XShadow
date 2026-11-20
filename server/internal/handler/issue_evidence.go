package handler

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/projectaccess"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type evidenceInput struct {
	AttachmentID string `json:"attachment_id"`
	SHA256       string `json:"sha256"`
}
type evidenceRequest struct {
	RequestID string          `json:"request_id"`
	AgentID   string          `json:"agent_id"`
	Question  string          `json:"question"`
	Inputs    []evidenceInput `json:"inputs"`
}

func evidenceRequestError(w http.ResponseWriter, err error) {
	slog.Error("issue evidence run failed", "error", err)
	writeError(w, http.StatusInternalServerError, "failed to record issue evidence run")
}

// Starting the dedicated chat, sealing its sole input, and binding it to this
// issue share a commit. No client can publish an existing private chat this way.
func (h *Handler) StartIssueEvidenceRun(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var req evidenceRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&req) != nil || decoder.Decode(&struct{}{}) != io.EOF || strings.TrimSpace(req.Question) == "" || len(req.Inputs) == 0 || len(req.Inputs) > 32 {
		writeError(w, 400, "provide a question and 1 to 32 frozen issue attachments")
		return
	}
	requestKey, ok := parseUUIDOrBadRequest(w, req.RequestID, "request_id")
	if !ok {
		return
	}
	agentID, ok := parseUUIDOrBadRequest(w, req.AgentID, "agent_id")
	if !ok {
		return
	}
	req.AgentID = uuidToString(agentID)
	req.RequestID = uuidToString(requestKey)
	ws := uuidToString(issue.WorkspaceID)
	kind, actor := h.resolveActor(r, userID, ws)
	origin := h.invokeOriginatorFromRequest(r, kind, actor)
	principal := parseUUIDOrInvalid(origin)
	delegated := r.Header.Get("X-Actor-Source") == "task_token"
	if !principal.Valid || (delegated && kind != "agent") || (!delegated && (kind != "member" || isMachineCredentialActor(r))) {
		writeError(w, 403, "evidence requests require a member or their active issue run")
		return
	}
	if issue.TriageState.Valid {
		h.writeDispatchBlocked(w, 403, ReasonIssueInTriage)
		return
	}
	agent, err := h.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{WorkspaceID: issue.WorkspaceID, ID: agentID})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 404, "agent not found")
		return
	}
	if err != nil {
		evidenceRequestError(w, err)
		return
	}
	if !h.canInvokeAgent(ctx, agent, kind, actor, origin, ws) {
		h.writeDispatchBlocked(w, 403, ReasonInvocationNotAllowed)
		return
	}
	runtime, err := h.Queries.GetAgentRuntime(ctx, agent.RuntimeID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !evidenceRuntimeSupported(runtime)) {
		writeError(w, 409, "evidence collection requires a local Codex or Claude CLI runtime")
		return
	}
	if err != nil {
		evidenceRequestError(w, err)
		return
	}
	if agent.Kind == "system" {
		writeError(w, 409, "choose an individual workspace agent for evidence collection")
		return
	}
	if !h.requireChatProjectVisible(w, r, ws, issue.ProjectID, agentID) {
		return
	}
	sessionID := pgtype.UUID{Bytes: uuid.NewSHA1(uuid.UUID(issue.ID.Bytes), []byte("evidence/"+kind+"/"+actor+"/"+req.RequestID)), Valid: true}
	var source db.AgentTaskQueue
	var prepared service.DirectChatSendPreparation
	if delegated {
		source, ok = h.taskFromRequestHeader(r)
		if !ok || source.AgentID != parseUUIDOrInvalid(actor) || source.IssueID != issue.ID || source.Status != "running" || source.OriginatorUserID != principal {
			writeError(w, 403, "only the active issue owner can delegate evidence")
			return
		}
		prepared, err = h.TaskService.PrepareDelegatedEvidenceSend(ctx, issue, agent, source)
	} else {
		prepared, err = h.TaskService.PrepareDirectChatSend(ctx, sessionID, agent, principal)
	}
	if errors.Is(err, service.ErrProjectAccessDenied) {
		h.writeDispatchBlocked(w, 403, ReasonProjectAccessBlocked)
		return
	}
	if errors.Is(err, service.ErrChatTaskAgentArchived) || errors.Is(err, service.ErrChatTaskAgentNoRuntime) {
		writeError(w, 409, "target agent is unavailable")
		return
	}
	if err != nil {
		evidenceRequestError(w, err)
		return
	}
	var prompt strings.Builder
	fmt.Fprintf(&prompt, "Independent evidence request. Include the full findings in your final answer for the issue. Use only the following frozen issue attachments. Download each with multica attachment download and verify its SHA-256 before using it. Report a missing attachment or digest mismatch. This run cannot approve or change the work method.\nQuestion: %s\n", req.Question)
	for index, input := range req.Inputs {
		attID, ok := parseUUIDOrBadRequest(w, input.AttachmentID, "attachment_id")
		if !ok {
			return
		}
		digest, err := hex.DecodeString(input.SHA256)
		if err != nil || len(digest) != 32 {
			writeError(w, 400, "each attachment needs a SHA-256 digest")
			return
		}
		input.AttachmentID = uuidToString(attID)
		input.SHA256 = hex.EncodeToString(digest)
		req.Inputs[index] = input
		fmt.Fprintf(&prompt, "\nAttachment %s SHA-256 %s", input.AttachmentID, input.SHA256)
	}
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		evidenceRequestError(w, err)
		return
	}
	defer tx.Rollback(ctx)
	q := h.Queries.WithTx(tx)
	if _, err = q.LockWorkspaceForChatSessionCreate(ctx, issue.WorkspaceID); err != nil {
		evidenceRequestError(w, err)
		return
	}
	if issue.ProjectID.Valid {
		if _, err = q.LockProjectForChatSessionCreate(ctx, db.LockProjectForChatSessionCreateParams{ID: issue.ProjectID, WorkspaceID: issue.WorkspaceID}); err != nil {
			writeError(w, 409, "project changed before dispatch")
			return
		}
	}
	if _, err = q.LockIssueForAttachmentWrite(ctx, db.LockIssueForAttachmentWriteParams{ID: issue.ID, WorkspaceID: issue.WorkspaceID}); err != nil {
		writeError(w, 409, "issue changed before dispatch")
		return
	}
	current, err := q.GetIssue(ctx, issue.ID)
	if err != nil {
		evidenceRequestError(w, err)
		return
	}
	if current.ProjectID != issue.ProjectID || current.TriageState.Valid {
		writeError(w, 409, "issue changed before dispatch")
		return
	}
	if delegated {
		var active bool
		err = tx.QueryRow(ctx, "SELECT status='running' FROM agent_task_queue WHERE id=$1 FOR SHARE NOWAIT", source.ID).Scan(&active)
		if err != nil || !active || !h.canDelegateEvidence(r, current, source) {
			writeError(w, 403, "only the active issue or method step owner can delegate evidence")
			return
		}
	}
	prev, err := q.GetIssueEvidenceRequest(ctx, db.GetIssueEvidenceRequestParams{IssueID: issue.ID, SessionID: sessionID})
	if err == nil {
		var prior evidenceRequest
		if json.Unmarshal(prev.Input, &prior) != nil || !reflect.DeepEqual(prior, req) {
			writeError(w, 409, "request_id already belongs to a different evidence request")
			return
		}
		writeJSON(w, 200, map[string]string{"task_id": uuidToString(prev.ID)})
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		evidenceRequestError(w, err)
		return
	}
	for _, input := range req.Inputs {
		att, err := q.GetAttachment(ctx, db.GetAttachmentParams{ID: parseUUID(input.AttachmentID), WorkspaceID: issue.WorkspaceID})
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && (att.IssueID != issue.ID || att.ChatSessionID.Valid || att.SourceContextID.Valid)) {
			writeError(w, 404, "attachment not found on this issue")
			return
		}
		if err != nil {
			evidenceRequestError(w, err)
			return
		}
	}
	session, err := q.CreateChatSession(ctx, db.CreateChatSessionParams{ID: sessionID, AgentID: agentID, WorkspaceID: issue.WorkspaceID, CreatorID: principal, ProjectID: issue.ProjectID, Title: "Issue evidence"})
	if err != nil {
		evidenceRequestError(w, err)
		return
	}
	initiator := principal
	if delegated {
		initiator = pgtype.UUID{}
	}
	sent, err := h.TaskService.SendPreparedDirectChatMessageInTx(ctx, tx, session, initiator, prompt.String(), nil, kind, parseUUID(actor), prepared)
	if err != nil {
		evidenceRequestError(w, err)
		return
	}
	raw, _ := json.Marshal(req)
	rows, err := q.BindIssueEvidenceTask(ctx, db.BindIssueEvidenceTaskParams{TaskID: sent.Task.ID, SessionID: sessionID, IssueID: issue.ID, Input: raw})
	if err != nil || rows != 1 {
		evidenceRequestError(w, fmt.Errorf("bind evidence task: rows=%d error=%v", rows, err))
		return
	}
	if err = tx.Commit(ctx); err != nil {
		evidenceRequestError(w, err)
		return
	}
	h.TaskService.FinalizeDirectChatSend(ctx, session, sent)
	writeJSON(w, 201, map[string]string{"task_id": uuidToString(sent.Task.ID)})
}

func (h *Handler) canDelegateEvidence(r *http.Request, issue db.Issue, task db.AgentTaskQueue) bool {
	run, _, err := h.issueWorkMethod(r.Context(), issue, nil)
	if err != nil {
		return false
	}
	if run.Method == nil {
		return taskRunsForAssignee(issue, &task)
	}
	if run.State != "working" && run.State != "review" {
		return false
	}
	recipient, err := h.methodRecipient(r.Context(), issue, run)
	if err != nil {
		return false
	}
	if recipient == nil && !taskRunsForAssignee(issue, &task) {
		return false
	}
	return h.canActOnMethod(r, issue, run, recipient)
}

// Remote gateways may execute in an existing server-side workspace.
func evidenceRuntimeSupported(runtime db.AgentRuntime) bool {
	return runtime.RuntimeMode == "local" && (runtime.Provider == "codex" || runtime.Provider == "claude")
}

// Permissions and the issue's project may change while the helper is queued.
func (h *Handler) evidenceClaimAllowed(ctx context.Context, issueID string, session db.ChatSession, agent db.Agent, task db.AgentTaskQueue, runtime db.AgentRuntime) (bool, error) {
	if !evidenceRuntimeSupported(runtime) {
		return false, nil
	}
	issue, err := h.Queries.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{WorkspaceID: session.WorkspaceID, ID: parseUUIDOrInvalid(issueID)})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if issue.ProjectID != session.ProjectID {
		return false, nil
	}
	origin := uuidToString(task.OriginatorUserID)
	ws := uuidToString(session.WorkspaceID)
	if _, err := h.getWorkspaceMember(ctx, origin, ws); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	if !h.canInvokeAgent(ctx, agent, "member", origin, origin, ws) {
		return false, nil
	}
	hidden, err := projectaccess.ForPrincipals(ctx, h.Queries, session.WorkspaceID, task.OriginatorUserID, agent.OwnerID, runtime.OwnerID)
	return err == nil && hidden.CanSeeProject(issue.ProjectID), err
}

type issueEvidenceReceipt struct {
	db.ListIssueEvidenceTasksRow
	Request json.RawMessage `json:"request"`
	Usage   []TaskUsageData `json:"usage"`
}

func (h *Handler) ListIssueEvidenceRuns(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	rows, err := h.Queries.ListIssueEvidenceTasks(r.Context(), db.ListIssueEvidenceTasksParams{IssueID: issue.ID, WorkspaceID: issue.WorkspaceID})
	if err != nil {
		evidenceRequestError(w, err)
		return
	}
	ids := make([]pgtype.UUID, len(rows))
	for i, row := range rows {
		ids[i] = row.ID
	}
	usage, err := h.Queries.ListTaskUsageByIDs(r.Context(), ids)
	if err != nil {
		evidenceRequestError(w, err)
		return
	}
	byID := map[string][]TaskUsageData{}
	for _, u := range usage {
		appendTaskUsage(byID, u.TaskID, u.Provider, u.Model, u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheWriteTokens, u.CostUsdTicks, u.CreditsMicros)
	}
	receipts := make([]issueEvidenceReceipt, len(rows))
	for i, row := range rows {
		receipts[i] = issueEvidenceReceipt{row, json.RawMessage(row.Request), byID[uuidToString(row.ID)]}
	}
	writeJSON(w, 200, receipts)
}
