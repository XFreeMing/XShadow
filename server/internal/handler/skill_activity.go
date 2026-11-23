package handler

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func (h *Handler) recordSkillRunAvailability(ctx context.Context, task db.AgentTaskQueue, workspaceID pgtype.UUID, versions []protocol.PreparedSkill) {
	if !task.IssueID.Valid || task.ChatSessionID.Valid || len(versions) == 0 || len(versions) > 1000 {
		return
	}
	params := db.RecordSkillRunAvailabilityParams{
		WorkspaceID:   workspaceID,
		TaskID:        task.ID,
		RuntimeID:     task.RuntimeID,
		DispatchedAt:  task.DispatchedAt,
		SkillIds:      make([]pgtype.UUID, 0, len(versions)),
		ContentHashes: make([]string, 0, len(versions)),
	}
	seen := make(map[pgtype.UUID]struct{}, len(versions))
	for _, version := range versions {
		id, err := util.ParseUUID(version.ID)
		hash, hashErr := hex.DecodeString(version.ContentHash)
		if _, duplicate := seen[id]; err != nil || hashErr != nil || len(hash) != 32 || duplicate {
			continue
		}
		seen[id] = struct{}{}
		params.SkillIds = append(params.SkillIds, id)
		params.ContentHashes = append(params.ContentHashes, hex.EncodeToString(hash))
	}
	if len(params.SkillIds) == 0 {
		return
	}
	if err := h.Queries.RecordSkillRunAvailability(ctx, params); err != nil {
		slog.Warn("record skill availability for run", "task_id", uuidToString(task.ID), "error", err)
	}
}

type skillActivityCounts struct {
	Available  int `json:"available"`
	Reviewed   int `json:"reviewed"`
	Adopted    int `json:"adopted"`
	Helped     int `json:"helped"`
	Neutral    int `json:"neutral"`
	Regressed  int `json:"regressed"`
	NotApplied int `json:"not_applied"`
}

func (c *skillActivityCounts) record(outcome string) {
	c.Available++
	switch outcome {
	case "helped":
		c.Helped++
	case "neutral":
		c.Neutral++
	case "regressed":
		c.Regressed++
	case "not_applied":
		c.NotApplied++
	default:
		return
	}
	c.Reviewed++
	c.Adopted = c.Helped + c.Neutral + c.Regressed
}

type skillActivityVersion struct {
	ContentHash string `json:"content_hash"`
	Version     *int32 `json:"version"`
	skillActivityCounts
}

type skillActivityRun struct {
	ID          string `json:"id"`
	IssueID     string `json:"issue_id"`
	IssueNumber int32  `json:"issue_number"`
	ContentHash string `json:"content_hash"`
	Version     *int32 `json:"version"`
	Status      string `json:"status"`
	ProvidedAt  string `json:"provided_at"`
	Outcome     string `json:"outcome,omitempty"`
	Evidence    string `json:"evidence,omitempty"`
	ReviewedAt  string `json:"reviewed_at,omitempty"`
}

type skillActivityResponse struct {
	Days     int                    `json:"days"`
	Counts   skillActivityCounts    `json:"counts"`
	Versions []skillActivityVersion `json:"versions"`
	Runs     []skillActivityRun     `json:"runs"`
}

const skillActivityRecentLimit = 20

func (h *Handler) GetSkillActivity(w http.ResponseWriter, r *http.Request) {
	skill, ok := h.loadSkillForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	days := 30
	if raw := r.URL.Query().Get("days"); raw != "" {
		days, _ = strconv.Atoi(raw)
		if days != 7 && days != 30 && days != 90 {
			writeError(w, http.StatusBadRequest, "days must be 7, 30 or 90")
			return
		}
	}
	hidden, ok := h.requestHiddenProjects(w, r, uuidToString(skill.WorkspaceID))
	if !ok {
		return
	}
	rows, err := h.Queries.ListSkillRunObservations(r.Context(), db.ListSkillRunObservationsParams{
		WorkspaceID: skill.WorkspaceID,
		SkillID:     skill.ID,
		Since:       pgtype.Timestamptz{Time: time.Now().AddDate(0, 0, -days), Valid: true},
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load skill activity")
		return
	}
	result := skillActivityResponse{Days: days, Versions: []skillActivityVersion{}, Runs: []skillActivityRun{}}
	type revision struct {
		hash    string
		version int32
	}
	positions := map[revision]int{}
	for _, row := range rows {
		if !hidden.CanSeeProject(row.ProjectID) {
			continue
		}
		observation := row.SkillRunObservation
		result.Counts.record(observation.Outcome.String)
		var version *int32
		if observation.Version.Valid {
			version = &observation.Version.Int32
		}
		key := revision{observation.ContentHash, observation.Version.Int32}
		position, exists := positions[key]
		if !exists {
			position = len(result.Versions)
			positions[key] = position
			result.Versions = append(result.Versions, skillActivityVersion{ContentHash: key.hash, Version: version})
		}
		result.Versions[position].record(observation.Outcome.String)
		if len(result.Runs) < skillActivityRecentLimit {
			run := skillActivityRun{
				ID: uuidToString(observation.TaskID), IssueID: uuidToString(row.IssueID), IssueNumber: row.IssueNumber,
				ContentHash: key.hash, Version: version, Status: row.Status,
				ProvidedAt: observation.ProvidedAt.Time.Format(time.RFC3339Nano),
				Outcome:    observation.Outcome.String, Evidence: observation.Evidence.String,
			}
			if observation.ReviewedAt.Valid {
				run.ReviewedAt = observation.ReviewedAt.Time.Format(time.RFC3339Nano)
			}
			result.Runs = append(result.Runs, run)
		}
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) ReviewSkillActivity(w http.ResponseWriter, r *http.Request) {
	skill, ok := h.loadSkillForUser(w, r, chi.URLParam(r, "id"))
	if !ok || !h.canManageSkill(w, r, skill) {
		return
	}
	var req struct {
		RunID       string `json:"run_id"`
		ContentHash string `json:"content_hash"`
		Outcome     string `json:"outcome"`
		Evidence    string `json:"evidence"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid skill review")
		return
	}
	taskID, ok := parseUUIDOrBadRequest(w, req.RunID, "run_id")
	if !ok {
		return
	}
	switch req.Outcome {
	case "not_applied", "helped", "neutral", "regressed":
	default:
		writeError(w, http.StatusBadRequest, "invalid skill outcome")
		return
	}
	evidence := strings.TrimSpace(req.Evidence)
	if evidence == "" || utf8.RuneCountInString(evidence) > 1000 || strings.ContainsRune(evidence, 0) || len(req.ContentHash) != 64 {
		writeError(w, http.StatusBadRequest, "skill version and evidence of 1 to 1000 characters are required")
		return
	}
	task, err := h.Queries.GetAgentTask(r.Context(), taskID)
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, "skill run not found")
		} else {
			writeError(w, http.StatusInternalServerError, "failed to load skill run")
		}
		return
	}
	workspaceID := uuidToString(skill.WorkspaceID)
	if !task.IssueID.Valid || task.ChatSessionID.Valid {
		writeError(w, http.StatusNotFound, "skill run not found")
		return
	}
	issue, err := h.Queries.GetIssue(r.Context(), task.IssueID)
	if err != nil {
		if isNotFound(err) {
			writeError(w, http.StatusNotFound, "skill run not found")
		} else {
			writeError(w, http.StatusInternalServerError, "failed to load skill run issue")
		}
		return
	}
	if issue.WorkspaceID != skill.WorkspaceID {
		writeError(w, http.StatusNotFound, "skill run not found")
		return
	}
	if !h.requireTaskVisible(w, r, workspaceID, task, http.StatusNotFound, "skill run not found") {
		return
	}
	reviewer, ok := parseUUIDOrBadRequest(w, requestUserID(r), "reviewer")
	if !ok {
		return
	}
	updated, err := h.Queries.ReviewSkillRunObservation(r.Context(), db.ReviewSkillRunObservationParams{
		WorkspaceID: skill.WorkspaceID, SkillID: skill.ID, TaskID: taskID, ContentHash: req.ContentHash,
		Outcome: pgtype.Text{String: req.Outcome, Valid: true}, Evidence: pgtype.Text{String: evidence, Valid: true},
		ReviewedBy: reviewer,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record skill review")
		return
	}
	if updated == 0 {
		writeError(w, http.StatusConflict, "the skill version was not available on this finished run")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
