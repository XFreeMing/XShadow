package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/logger"
)

// rustIssueListQuery carries values validated by the public Go API. Rust
// applies project access and constructs the database query locally.
type rustIssueListQuery struct {
	OpenOnly           bool                   `json:"open_only"`
	CreatedByUser      string                 `json:"created_by_user_id"`
	CreatedByID        *string                `json:"created_by_id,omitempty"`
	TerminalStatusKeys []string               `json:"terminal_status_keys,omitempty"`
	Statuses           []string               `json:"statuses,omitempty"`
	CategoryStatusKeys *[]string              `json:"category_status_keys,omitempty"`
	Priorities         []string               `json:"priorities,omitempty"`
	AssigneeID         *string                `json:"assignee_id,omitempty"`
	AssigneeIDs        []pgtype.UUID          `json:"assignee_ids,omitempty"`
	AssigneeTypes      []string               `json:"assignee_types,omitempty"`
	ScopeAssignee      *rustIssueActor        `json:"scope_assignee,omitempty"`
	AssigneeFilters    []rustIssueActor       `json:"assignee_filters,omitempty"`
	IncludeNoAssignee  bool                   `json:"include_no_assignee,omitempty"`
	CreatorID          *string                `json:"creator_id,omitempty"`
	ScopeCreator       *rustIssueActor        `json:"scope_creator,omitempty"`
	CreatorFilters     []rustIssueActor       `json:"creator_filters,omitempty"`
	ProjectID          *string                `json:"project_id,omitempty"`
	ProjectIDs         []pgtype.UUID          `json:"project_ids,omitempty"`
	ProjectStatuses    []string               `json:"project_statuses,omitempty"`
	IncludeNoProject   bool                   `json:"include_no_project,omitempty"`
	LabelIDs           []pgtype.UUID          `json:"label_ids,omitempty"`
	IDs                *[]pgtype.UUID         `json:"ids,omitempty"`
	WorkingOnly        bool                   `json:"working_only,omitempty"`
	WorkingIssueIDs    *[]pgtype.UUID         `json:"working_issue_ids,omitempty"`
	UnreadIssueIDs     *[]pgtype.UUID         `json:"unread_issue_ids,omitempty"`
	TopLevelOnly       bool                   `json:"top_level_only,omitempty"`
	SearchPatterns     []string               `json:"search_patterns,omitempty"`
	SearchNumber       *int32                 `json:"search_number,omitempty"`
	Scheduled          bool                   `json:"scheduled,omitempty"`
	Metadata           json.RawMessage        `json:"metadata,omitempty"`
	Properties         [][]json.RawMessage    `json:"properties,omitempty"`
	Date               *rustIssueDate         `json:"date,omitempty"`
	InvolvesUserID     *string                `json:"involves_user_id,omitempty"`
	Sort               string                 `json:"sort"`
	PropertySort       *rustIssuePropertySort `json:"property_sort,omitempty"`
	Descending         bool                   `json:"descending"`
	StatusOrder        []string               `json:"status_order,omitempty"`
	Limit              int                    `json:"limit"`
	Offset             int                    `json:"offset"`
}

type rustIssuePropertySort struct {
	ID      string   `json:"id"`
	Kind    string   `json:"kind"`
	Options []string `json:"options,omitempty"`
}

type rustIssueActor struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type rustIssueDate struct {
	Column string    `json:"column"`
	Start  time.Time `json:"start"`
	End    time.Time `json:"end"`
}

func rustIssueActors(filters []issueActorFilter) []rustIssueActor {
	actors := make([]rustIssueActor, len(filters))
	for i, filter := range filters {
		actors[i] = rustIssueActor{Type: filter.actorType, ID: uuidToString(filter.actorID)}
	}
	return actors
}

func (q *rustIssueListQuery) setSearch(raw string) {
	query := strings.TrimSpace(raw)
	for _, word := range splitSearchTerms(strings.ToLower(query)) {
		q.SearchPatterns = append(q.SearchPatterns, "%"+escapeLike(word)+"%")
	}
	if number, ok := parseQueryNumber(query); ok {
		parsed := int32(number)
		q.SearchNumber = &parsed
	}
}

func (h *Handler) isRustIssueListPilot(w http.ResponseWriter, r *http.Request, workspaceID string, wsUUID pgtype.UUID) (pilot, ok bool) {
	if h.IssueReadBridge == nil || len(h.IssueReadBridge.ListPilotScopes) == 0 {
		return false, true
	}
	matches, err := matchesIssuePilot(r.Context(), h.Queries, h.IssueReadBridge.ListPilotScopes, workspaceID, wsUUID)
	if err != nil {
		if !writeQueryCancellation(w, r, err) {
			slog.Warn("resolve rust issue list pilot failed", append(logger.RequestAttrs(r), "workspace_id", workspaceID, "error", err)...)
			writeError(w, http.StatusInternalServerError, "failed to resolve issue read route")
		}
		return false, false
	}
	return matches, true
}

func (h *Handler) listIssuesViaRust(w http.ResponseWriter, r *http.Request, workspaceID string, query rustIssueListQuery) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	body, err := json.Marshal(query)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list issues")
		return
	}
	resp, ok := h.requestRustIssue(w, r, http.MethodPost, "/internal/issues", workspaceID, userID, body)
	if ok {
		defer resp.Body.Close()
		writeRustIssueResponse(w, resp, resp.Body)
	}
}
