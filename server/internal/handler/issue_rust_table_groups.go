package handler

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/multica-ai/multica/server/internal/logger"
)

const rustIssueTableGroupsPath = "/internal/issues/table/groups"

type rustIssueTableGroupDimension struct {
	Kind       string            `json:"kind"`
	ID         string            `json:"id,omitempty"`
	Options    []string          `json:"options"`
	Order      []string          `json:"order"`
	Categories map[string]string `json:"categories,omitempty"`
	Lifecycle  bool              `json:"lifecycle"`
	Prefix     string            `json:"prefix"`
}

type rustIssueTableGroupSecondary struct {
	Categories             map[string]string `json:"categories,omitempty"`
	Visible                *[]string         `json:"visible,omitempty"`
	PromotedParentStatuses *[]string         `json:"promoted_parent_statuses,omitempty"`
}

type rustIssueTableGroupPosition struct {
	Order int    `json:"order"`
	Sort  string `json:"sort"`
	Value string `json:"value"`
}

type rustIssueTableGroupsQuery struct {
	Query        rustIssueListQuery            `json:"query"`
	Dimension    rustIssueTableGroupDimension  `json:"dimension"`
	Secondary    *rustIssueTableGroupSecondary `json:"secondary,omitempty"`
	IncludeEmpty bool                          `json:"include_empty"`
	Limit        int                           `json:"limit"`
	Cursor       *rustIssueTableGroupPosition  `json:"cursor,omitempty"`
}

type rustIssueTableGroupsPage struct {
	Rows []struct {
		rustIssueTableGroupPosition
		Count           int64                  `json:"count"`
		SecondaryCounts map[string]int64       `json:"secondary_counts"`
		Context         issueTableGroupContext `json:"context"`
	} `json:"rows"`
	Total int64 `json:"total"`
}

func rustIssueGroupStatusCategories(group resolvedIssueTableGroup) map[string]string {
	categories := make(map[string]string)
	for category, keys := range group.categoryKeys {
		for _, key := range keys {
			categories[key] = category
		}
	}
	return categories
}

func prepareRustIssueTableGroups(compiled issueTableSQL, request issueTableGroupsRequest, group resolvedIssueTableGroup, limit int, cursor *issueTableCursor, prefix string) rustIssueTableGroupsQuery {
	query := rustIssueTableGroupsQuery{Query: compiled.native, IncludeEmpty: request.Group.IncludeEmpty, Limit: limit}
	primary := group
	if group.primary != nil {
		primary = *group.primary
		secondary := &rustIssueTableGroupSecondary{}
		if group.secondaryCategory {
			secondary.Categories = rustIssueGroupStatusCategories(group)
		}
		if group.secondaryFiltered {
			visible := append([]string{}, group.secondaryValues...)
			secondary.Visible = &visible
			if primary.kind == "parent" {
				statuses := visible
				if group.secondaryCategory {
					statuses = []string{}
					for _, value := range visible {
						statuses = append(statuses, group.categoryKeysFor(value)...)
					}
				}
				secondary.PromotedParentStatuses = &statuses
			}
		}
		query.Secondary = secondary
	}

	dimension := rustIssueTableGroupDimension{Kind: primary.kind}
	switch primary.kind {
	case "status":
		dimension.Order = primary.statusOrder
	case "status_category":
		dimension.Categories = rustIssueGroupStatusCategories(primary)
		dimension.Order = primary.categoryValues()
		dimension.Lifecycle = !primary.legacyCategories
	case "parent":
		dimension.Prefix = prefix
	case "property":
		dimension.ID = primary.propertyID
		dimension.Kind = primary.propertyType + "_property"
		if primary.propertyType == "select" {
			dimension.Options = make([]string, 0, len(primary.activeOptionOrder))
			for _, option := range primary.activeOptionOrder {
				dimension.Options = append(dimension.Options, strings.TrimPrefix(option, "value:"))
			}
		}
	}
	query.Dimension = dimension
	if cursor != nil {
		query.Cursor = &rustIssueTableGroupPosition{Order: *cursor.GroupOrder, Sort: *cursor.GroupSortKey, Value: *cursor.GroupCursorKey}
	}
	return query
}

func (h *Handler) writeRustIssueTableGroups(w http.ResponseWriter, r *http.Request, compiled issueTableSQL, group resolvedIssueTableGroup, identity string, query rustIssueTableGroupsQuery) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	body, err := json.Marshal(query)
	if err != nil {
		writeIssueTableQueryFailure(w, r, "failed to prepare table group query")
		return
	}
	upstream, ok := h.requestRustIssue(w, r, http.MethodPost, rustIssueTableGroupsPath, uuidToString(compiled.workspaceID), userID, body)
	if !ok {
		return
	}
	defer upstream.Body.Close()
	if upstream.StatusCode != http.StatusOK {
		writeRustIssueResponse(w, upstream, upstream.Body)
		return
	}
	var page rustIssueTableGroupsPage
	if err := json.NewDecoder(io.LimitReader(upstream.Body, maxRustIssueReadBody)).Decode(&page); err != nil || page.Rows == nil || len(page.Rows) > query.Limit+1 {
		writeIssueTableQueryFailure(w, r, "invalid Rust table group response")
		return
	}
	result := issueTableGroupsResponse{
		QueryFingerprint: compiled.fingerprint,
		Total:            page.Total, Groups: make([]issueTableGroupDescriptorResponse, 0, len(page.Rows)),
	}
	for _, row := range page.Rows {
		descriptor, err := group.descriptor(row.Value, row.Count, row.Context, row.SecondaryCounts)
		if err != nil {
			slog.Warn("ListIssueTableGroups descriptor failed", append(logger.RequestAttrs(r), "error", err)...)
			writeError(w, http.StatusInternalServerError, "failed to resolve table group")
			return
		}
		result.Groups = append(result.Groups, descriptor)
	}
	if len(page.Rows) > query.Limit {
		last := page.Rows[query.Limit-1]
		result.Groups = result.Groups[:query.Limit]
		result.NextCursor = encodeIssueTableCursor(issueTableCursor{
			Version: 1, QueryFingerprint: compiled.fingerprint, GroupKey: &identity,
			GroupOrder: &last.Order, GroupSortKey: &last.Sort, GroupCursorKey: &last.Value,
		})
	}
	writeJSON(w, http.StatusOK, result)
}
