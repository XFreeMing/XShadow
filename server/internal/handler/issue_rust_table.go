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
	// The first pilot handles ungrouped row queries, including hierarchy.
	filters := request.Query.Filters
	if request.Group.Kind != "none" || request.Query.Scope.Kind == "my" ||
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

func (h *Handler) queryRustIssueTableRows(w http.ResponseWriter, r *http.Request, compiled issueTableSQL, request issueTableRowsRequest, sort resolvedIssueTableSort, cursor *issueTableCursor, limit int, branch, cursorTimeZone string) (issueTableRowsResponse, bool) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return issueTableRowsResponse{}, false
	}
	query := rustIssueTableQuery{Query: compiled.native, Hierarchy: request.Hierarchy.Enabled, CursorTimeZone: cursorTimeZone}
	if request.ParentID != nil {
		query.ParentID = uuidToPtr(util.MustParseUUID(*request.ParentID))
	}
	if cursor != nil {
		query.Cursor = &rustIssueTablePosition{
			RowID: uuidToString(util.MustParseUUID(cursor.RowID)), CreatedAt: cursor.RowCreatedAt, SortValue: cursor.SortValue,
		}
	}
	query.Query.Sort = sort.nativeField
	query.Query.Descending = sort.direction == "desc"
	query.Query.StatusOrder = sort.statusOrder
	query.Query.PropertySort = sort.property
	query.Query.Limit = limit
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
		QueryFingerprint: compiled.fingerprint, ParentID: request.ParentID,
		Total: page.Total, Rows: page.Rows, BranchTotal: int64(len(page.Rows)),
	}
	if page.Next != nil {
		result.NextCursor = encodeIssueTableCursor(issueTableCursor{
			Version: 1, QueryFingerprint: compiled.fingerprint, ParentID: request.ParentID,
			BranchIdentity: branch, SortValue: page.Next.SortValue, SortIsNull: page.Next.SortValue == nil,
			RowCreatedAt: page.Next.CreatedAt, RowID: page.Next.RowID,
		})
	}
	return result, true
}
