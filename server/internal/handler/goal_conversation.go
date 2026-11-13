package handler

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const goalConversationInstructions = `## Goal conversation with the chief of staff

Use this dedicated conversation to help the user define goals. Discuss the desired outcome, scope and observable acceptance criteria in natural language. Apply the normal coordination rules if the user separately asks you to carry out work. When the user clearly wants to pursue a concrete new outcome and you have enough information, save it immediately with "$MULTICA_CLI_PATH" goal record --file <path> (use the equivalent syntax for your shell). Ask only about ambiguities that materially affect the outcome. Exploratory ideas, questions, system notices and updates to a previously recorded goal do not create a new goal. Consult multica chat history if you need to recover the discussion or check whether a goal was already recorded.

Before saving, identify the project from the conversation and workspace project list. Independently search existing tasks using multica issue search <query> --project <project-id> --output json, or inspect multica issue list --project <project-id> --output json. Read likely matches and compare their deliverables and acceptance criteria with the goal. Include every task that directly contributes to that outcome. One goal can own many tasks, and each task can belong to at most one goal. The user only needs to describe the outcome. Do not ask for task identifiers or require the user to select tasks. When no task matches, save the goal without links.

Write a UTF-8 JSON object with title, intent, scope, acceptance_criteria, optional project_id, and issue_ids containing the verified issue UUIDs or identifiers you found. If the project is omitted and issues are supplied, the server uses their common project. Do not guess, move tasks between projects or goals, or create or dispatch work just to record a goal.

Only say the goal was saved after the command succeeds. Reply with the returned Markdown link and identify the linked tasks. If the command reports a conflict, explain it and clarify the relationship before trying again.`

// OpenGoalConversation resolves the personal team's chief through the existing
// squad leadership binding. Mika and ordinary chats with the chief are separate.
func (h *Handler) OpenGoalConversation(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	wsID := parseUUID(workspaceID)
	ctx := r.Context()

	squads, err := h.Queries.ListSquads(ctx, wsID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to resolve chief of staff")
		return
	}
	var chiefID pgtype.UUID
	for _, squad := range squads {
		if squad.Name != "军团统筹" {
			continue
		}
		if chiefID.Valid {
			writeChiefUnavailable(w)
			return
		}
		chiefID = squad.LeaderID
	}
	if !chiefID.Valid {
		writeChiefUnavailable(w)
		return
	}

	chief, err := h.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{
		ID: chiefID, WorkspaceID: wsID,
	})
	if errors.Is(err, pgx.ErrNoRows) || chief.ArchivedAt.Valid {
		writeChiefUnavailable(w)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load chief of staff")
		return
	}
	if !h.canInvokeAgent(ctx, chief, "member", userID, "", workspaceID) {
		writeChiefUnavailable(w)
		return
	}

	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to open goal conversation")
		return
	}
	defer tx.Rollback(ctx)
	qtx := h.Queries.WithTx(tx)
	if _, err := qtx.LockWorkspaceForChatSessionCreate(ctx, wsID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to lock workspace")
		return
	}
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))",
		"goal-conversation:"+workspaceID+":"+userID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to lock goal conversation")
		return
	}
	lookup := db.GetGoalConversationParams{
		WorkspaceID: wsID, CreatorID: parseUUID(userID), AgentID: chief.ID,
	}
	session, err := qtx.GetGoalConversation(ctx, lookup)
	reopened := err == nil && session.Status == "archived"
	if errors.Is(err, pgx.ErrNoRows) {
		session, err = qtx.CreateChatSession(ctx, db.CreateChatSessionParams{
			ID: dbid.NewV7(), WorkspaceID: wsID, CreatorID: lookup.CreatorID,
			AgentID: chief.ID, Title: "目标 · " + chief.Name, IsGoalConversation: true,
		})
		if err == nil {
			session, err = qtx.MarkChatSessionExplicitlyCreated(ctx, session.ID)
		}
	} else if reopened {
		session, err = qtx.SetChatSessionArchived(ctx, db.SetChatSessionArchivedParams{ID: session.ID})
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to open goal conversation")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit goal conversation")
		return
	}
	if reopened {
		id := uuidToString(session.ID)
		h.publishChat(protocol.EventChatSessionUpdated, workspaceID, "member", userID, id, protocol.ChatSessionUpdatedPayload{
			ChatSessionID: id, Title: session.Title, Status: &session.Status, UpdatedAt: timestampToString(session.UpdatedAt),
		})
	}
	writeJSON(w, http.StatusOK, chatSessionToResponse(session))
}

func writeChiefUnavailable(w http.ResponseWriter) {
	writeErrorCode(w, http.StatusConflict, "goal_chief_unavailable", "no accessible chief of staff is configured")
}

func (h *Handler) CreateGoal(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Actor-Source") != "task_token" {
		writeGoalConversationRequired(w)
		return
	}
	taskID, ok := parseUUIDOrBadRequest(w, r.Header.Get("X-Task-ID"), "task_id")
	if !ok {
		return
	}
	ctx := r.Context()
	task, err := h.Queries.GetAgentTask(ctx, taskID)
	if err != nil || !task.ChatSessionID.Valid || !task.ChatInputTaskID.Valid ||
		task.OriginatorSource.String != "direct_human" || uuidToString(task.AgentID) != r.Header.Get("X-Agent-ID") {
		writeGoalConversationRequired(w)
		return
	}
	session, err := h.Queries.GetChatSession(ctx, task.ChatSessionID)
	if err != nil || !session.IsGoalConversation || session.Status != "active" ||
		session.AgentID != task.AgentID || session.CreatorID != task.OriginatorUserID ||
		uuidToString(session.WorkspaceID) != h.resolveWorkspaceID(r) {
		writeGoalConversationRequired(w)
		return
	}
	h.createGoal(w, r, task.ChatInputTaskID)
}

func writeGoalConversationRequired(w http.ResponseWriter) {
	writeErrorCode(w, http.StatusForbidden, "goal_conversation_required", "create goals through the chief of staff goal conversation")
}
