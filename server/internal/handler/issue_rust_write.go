package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/logger"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/workmethod"
)

func (h *Handler) ordinaryIssueEditUsesRust(r *http.Request, workspaceID pgtype.UUID, fields map[string]json.RawMessage, params db.UpdateIssueParams, attachmentIDs []pgtype.UUID) (bool, error) {
	if h.IssueReadBridge == nil || len(h.IssueReadBridge.WritePilotScopes) == 0 {
		return false, nil
	}
	_, startDate := fields["start_date"]
	_, dueDate := fields["due_date"]
	if !params.Title.Valid && !params.Description.Valid && len(attachmentIDs) == 0 &&
		!params.Priority.Valid && !params.Position.Valid && !startDate && !dueDate {
		return false, nil
	}
	for key := range fields {
		switch key {
		case "title", "title_base", "description", "description_base", "attachment_ids",
			"priority", "position", "start_date", "due_date", "expected_revision":
		default:
			return false, nil
		}
	}
	return matchesIssuePilot(r.Context(), h.Queries, h.IssueReadBridge.WritePilotScopes, uuidToString(workspaceID), workspaceID)
}

type ordinaryIssueEdit struct {
	ExpectedRevision *int64                     `json:"expected_revision,omitempty"`
	Title            *string                    `json:"title,omitempty"`
	TitleBase        *string                    `json:"title_base,omitempty"`
	Description      *string                    `json:"description,omitempty"`
	DescriptionBase  *string                    `json:"description_base,omitempty"`
	AttachmentIDs    []string                   `json:"attachment_ids,omitempty"`
	MethodCatalog    *ordinaryWorkMethodCatalog `json:"method_catalog,omitempty"`
	Priority         *string                    `json:"priority,omitempty"`
	Position         *float64                   `json:"position,omitempty"`
	StartDate        *ordinaryDateEdit          `json:"start_date,omitempty"`
	DueDate          *ordinaryDateEdit          `json:"due_date,omitempty"`
	SourceTaskID     *string                    `json:"source_task_id,omitempty"`
}

type ordinaryWorkMethodCatalog struct {
	Builtins    []workmethod.Template `json:"builtins"`
	LegacyAgile []workmethod.Template `json:"legacy_agile"`
}

type ordinaryDateEdit struct {
	Day *int32 `json:"day"`
}

const postgresEpochSeconds = 946684800 // 2000-01-01 UTC
const secondsPerDay = 86400

func ordinaryDateChange(date pgtype.Date) *ordinaryDateEdit {
	if !date.Valid {
		return &ordinaryDateEdit{}
	}
	day := int32((date.Time.Unix() - postgresEpochSeconds) / secondsPerDay)
	return &ordinaryDateEdit{Day: &day}
}

// Snapshot dates are signed PostgreSQL days. Explicit JSON fields shadow the
// generated []byte columns, which would otherwise expect base64 strings.
type ordinaryIssueSnapshot struct {
	db.Issue
	StartDate          *int32          `json:"start_date"`
	DueDate            *int32          `json:"due_date"`
	AcceptanceCriteria json.RawMessage `json:"acceptance_criteria"`
	ContextRefs        json.RawMessage `json:"context_refs"`
	Metadata           json.RawMessage `json:"metadata"`
	Properties         json.RawMessage `json:"properties"`
	WorkMethodRun      json.RawMessage `json:"work_method_run"`
}

func snapshotDate(day *int32) pgtype.Date {
	if day == nil {
		return pgtype.Date{}
	}
	switch *day {
	case 2147483647:
		return pgtype.Date{Valid: true, InfinityModifier: pgtype.Infinity}
	case -2147483648:
		return pgtype.Date{Valid: true, InfinityModifier: pgtype.NegativeInfinity}
	default:
		return pgtype.Date{Valid: true, Time: time.Unix(postgresEpochSeconds+int64(*day)*secondsPerDay, 0).UTC()}
	}
}

func (s ordinaryIssueSnapshot) issue() db.Issue {
	i := s.Issue
	i.StartDate = snapshotDate(s.StartDate)
	i.DueDate = snapshotDate(s.DueDate)
	i.AcceptanceCriteria = s.AcceptanceCriteria
	i.ContextRefs = s.ContextRefs
	i.Metadata = s.Metadata
	i.Properties = s.Properties
	if !bytes.Equal(s.WorkMethodRun, []byte("null")) {
		i.WorkMethodRun = s.WorkMethodRun
	}
	// Match pgx's default location when decoding timestamptz from PostgreSQL.
	for _, ts := range []*pgtype.Timestamptz{&i.CreatedAt, &i.UpdatedAt, &i.LastActivityAt, &i.FirstExecutedAt} {
		if ts.Valid && ts.InfinityModifier == pgtype.Finite {
			ts.Time = ts.Time.In(time.Local)
		}
	}
	return i
}

func (h *Handler) updateOrdinaryIssueViaRust(w http.ResponseWriter, r *http.Request, userID string, current db.Issue, params db.UpdateIssueParams, req UpdateIssueRequest, fields map[string]json.RawMessage, attachmentIDs []pgtype.UUID) (after, before db.Issue, attachmentsChanged, ok bool) {
	edit := ordinaryIssueEdit{
		Title:           textToPtr(params.Title),
		TitleBase:       req.TitleBase,
		Description:     textToPtr(params.Description),
		DescriptionBase: req.DescriptionBase,
		Priority:        textToPtr(params.Priority),
		SourceTaskID:    uuidToPtr(params.SourceTaskID),
	}
	for _, id := range attachmentIDs {
		edit.AttachmentIDs = append(edit.AttachmentIDs, uuidToString(id))
	}
	if edit.Title != nil || edit.Description != nil {
		edit.MethodCatalog = &ordinaryWorkMethodCatalog{
			Builtins: workmethod.Builtins(), LegacyAgile: workmethod.LegacyAgileTemplates(),
		}
	}
	if params.Position.Valid {
		edit.Position = &params.Position.Float64
	}
	if params.ExpectedRevision.Valid {
		edit.ExpectedRevision = &params.ExpectedRevision.Int64
	}
	if _, touched := fields["start_date"]; touched {
		edit.StartDate = ordinaryDateChange(params.StartDate)
	}
	if _, touched := fields["due_date"]; touched {
		edit.DueDate = ordinaryDateChange(params.DueDate)
	}
	payload, err := json.Marshal(edit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode issue edit")
		return after, before, false, false
	}
	resp, requested := h.requestRustIssue(w, r, http.MethodPut, "/internal/issues/"+uuidToString(current.ID), uuidToString(current.WorkspaceID), userID, payload)
	if !requested {
		return after, before, false, false
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		if !writeQueryCancellation(w, r, err) {
			writeError(w, http.StatusBadGateway, "failed to read rust issue edit response")
		}
		return after, before, false, false
	}
	if resp.StatusCode == http.StatusConflict {
		var conflict struct {
			ActualRevision int64 `json:"actual_revision"`
			FieldConflict  bool  `json:"field_conflict"`
		}
		if json.Unmarshal(body, &conflict) == nil {
			if conflict.FieldConflict {
				writeEditConflict(w, "issue", current.ID)
				return after, before, false, false
			}
			if edit.ExpectedRevision != nil && conflict.ActualRevision > 0 {
				writeRevisionConflict(w, "issue", current.ID, *edit.ExpectedRevision, conflict.ActualRevision)
				return after, before, false, false
			}
		}
		writeError(w, http.StatusBadGateway, "invalid rust issue revision conflict")
		return after, before, false, false
	}
	if resp.StatusCode != http.StatusOK {
		writeRustIssueResponse(w, resp, bytes.NewReader(body))
		return after, before, false, false
	}
	var snapshots struct {
		Before             ordinaryIssueSnapshot `json:"before"`
		After              ordinaryIssueSnapshot `json:"after"`
		AttachmentsChanged bool                  `json:"attachments_changed"`
	}
	if json.Unmarshal(body, &snapshots) != nil ||
		snapshots.Before.ID != current.ID || snapshots.Before.WorkspaceID != current.WorkspaceID ||
		snapshots.After.ID != current.ID || snapshots.After.WorkspaceID != current.WorkspaceID {
		writeError(w, http.StatusBadGateway, "invalid rust issue edit snapshots")
		return after, before, false, false
	}
	return snapshots.After.issue(), snapshots.Before.issue(), snapshots.AttachmentsChanged, true
}

func (h *Handler) rustIssueWritePilot(w http.ResponseWriter, r *http.Request, workspaceID pgtype.UUID) (bool, bool) {
	if h.IssueReadBridge == nil || len(h.IssueReadBridge.WritePilotScopes) == 0 {
		return false, true
	}
	selected, err := matchesIssuePilot(r.Context(), h.Queries, h.IssueReadBridge.WritePilotScopes, uuidToString(workspaceID), workspaceID)
	if err != nil {
		if !writeQueryCancellation(w, r, err) {
			slog.Warn("resolve rust issue write pilot failed", append(logger.RequestAttrs(r), "error", err)...)
			writeError(w, http.StatusInternalServerError, "failed to resolve issue write route")
		}
		return false, false
	}
	return selected, true
}
