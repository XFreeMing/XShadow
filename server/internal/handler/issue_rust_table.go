package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/util"
)

const rustIssueTablePath = "/internal/issues/table/rows"

type rustIssueTablePosition struct {
	RowID     string  `json:"row_id"`
	CreatedAt string  `json:"created_at"`
	SortValue *string `json:"sort_value"`
}

type rustIssueTableQuery struct {
	Query          rustIssueListQuery      `json:"query"`
	Hierarchy      bool                    `json:"hierarchy"`
	ParentID       *string                 `json:"parent_id"`
	Cursor         *rustIssueTablePosition `json:"cursor"`
	CursorTimeZone string                  `json:"cursor_time_zone,omitempty"`
	Group          *rustIssueTableGroup    `json:"group,omitempty"`
}

type rustIssueTableGroup struct {
	Statuses               *[]string               `json:"statuses,omitempty"`
	Assignee               *rustIssueActor         `json:"assignee,omitempty"`
	Unassigned             bool                    `json:"unassigned,omitempty"`
	ProjectID              *string                 `json:"project_id,omitempty"`
	NoProject              bool                    `json:"no_project,omitempty"`
	ParentID               *string                 `json:"parent_id,omitempty"`
	NoParent               bool                    `json:"no_parent,omitempty"`
	Property               *rustIssueGroupProperty `json:"property,omitempty"`
	PromotedParentStatuses *[]string               `json:"promoted_parent_statuses,omitempty"`
}

type rustIssueGroupProperty struct {
	ID       string `json:"id"`
	State    string `json:"state"`
	JSONType string `json:"json_type"`
	Value    string `json:"value"`
}

type rustIssueTablePage struct {
	Total int64                   `json:"total"`
	Rows  []issueTableRowResponse `json:"rows"`
	Next  *rustIssueTablePosition `json:"next"`
}

func (h *Handler) rustIssueTablePilot(w http.ResponseWriter, r *http.Request, workspaceID pgtype.UUID, request issueTableRowsRequest) (bool, bool) {
	if h.IssueReadBridge == nil || len(h.IssueReadBridge.TablePilotScopes) == 0 {
		return false, true
	}
	filters := request.Query.Filters
	if request.Query.Scope.Kind == "my" ||
		len(filters.Relations) != 0 || len(filters.DueDates) != 0 || len(filters.Negate) != 0 || filters.Advanced != nil {
		return false, true
	}
	matches, err := matchesIssuePilot(r.Context(), h.Queries, h.IssueReadBridge.TablePilotScopes, uuidToString(workspaceID), workspaceID)
	if err != nil {
		slog.Warn("resolve rust issue table pilot failed", append(logger.RequestAttrs(r), "error", err)...)
		writeIssueTableQueryFailure(w, r, "failed to resolve issue read route")
		return false, false
	}
	return matches, true
}

func (h *Handler) queryRustIssueTableRows(w http.ResponseWriter, r *http.Request, compiled issueTableSQL, request issueTableRowsRequest, query rustIssueTableQuery, groupKey *string, branch string) (issueTableRowsResponse, bool) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return issueTableRowsResponse{}, false
	}
	body, err := json.Marshal(query)
	if err != nil {
		writeIssueTableQueryFailure(w, r, "failed to prepare table query")
		return issueTableRowsResponse{}, false
	}
	upstream, ok := h.requestRustIssue(w, r, http.MethodPost, rustIssueTablePath, uuidToString(compiled.workspaceID), userID, body)
	if !ok {
		return issueTableRowsResponse{}, false
	}
	defer upstream.Body.Close()
	if upstream.StatusCode != http.StatusOK {
		writeRustIssueResponse(w, upstream, upstream.Body)
		return issueTableRowsResponse{}, false
	}
	var page rustIssueTablePage
	if err := json.NewDecoder(upstream.Body).Decode(&page); err != nil || page.Rows == nil {
		writeIssueTableQueryFailure(w, r, "invalid Rust table response")
		return issueTableRowsResponse{}, false
	}
	result := issueTableRowsResponse{
		QueryFingerprint: compiled.fingerprint, GroupKey: groupKey, ParentID: request.ParentID,
		Total: page.Total, Rows: page.Rows, BranchTotal: int64(len(page.Rows)),
	}
	if page.Next != nil {
		result.NextCursor = encodeIssueTableCursor(issueTableCursor{
			Version: 1, QueryFingerprint: compiled.fingerprint, GroupKey: groupKey, ParentID: request.ParentID,
			BranchIdentity: branch, SortValue: page.Next.SortValue, SortIsNull: page.Next.SortValue == nil,
			RowCreatedAt: page.Next.CreatedAt, RowID: page.Next.RowID,
		})
	}
	return result, true
}

func prepareRustIssueTableQuery(native rustIssueListQuery, request issueTableRowsRequest, sort resolvedIssueTableSort, cursor *issueTableCursor, limit int, group *rustIssueTableGroup) rustIssueTableQuery {
	native.Sort = sort.nativeField
	native.Descending = sort.direction == "desc"
	native.StatusOrder = sort.statusOrder
	native.PropertySort = sort.property
	native.Limit = limit
	query := rustIssueTableQuery{Query: native, Hierarchy: request.Hierarchy.Enabled, Group: group}
	if request.ParentID != nil {
		query.ParentID = uuidToPtr(util.MustParseUUID(*request.ParentID))
	}
	if cursor != nil {
		query.Cursor = &rustIssueTablePosition{
			RowID: uuidToString(util.MustParseUUID(cursor.RowID)), CreatedAt: cursor.RowCreatedAt, SortValue: cursor.SortValue,
		}
	}
	return query
}
