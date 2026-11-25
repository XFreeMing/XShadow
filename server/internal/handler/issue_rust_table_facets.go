package handler

import (
	"encoding/json"
	"io"
	"net/http"
)

const rustIssueTableFacetsPath = "/internal/issues/table/facets"

type rustIssueFacet struct {
	issueTableFacetSpec
	PropertyKey  string `json:"property_key,omitempty"`
	PropertyType string `json:"property_type,omitempty"`
	Batch        bool   `json:"batch"`
}

type rustIssueTableFacetsQuery struct {
	Query           rustIssueListQuery `json:"query"`
	Facets          []rustIssueFacet   `json:"facets"`
	IncludeTotal    bool               `json:"include_total"`
	WorkingAgentIDs []string           `json:"working_agent_ids,omitempty"`
}

func (h *Handler) prepareRustIssueTableFacets(w http.ResponseWriter, r *http.Request, compiled issueTableSQL, publicQuery issueTableQuerySpec, facets []issueTableFacetSpec, includeTotal bool) (rustIssueTableFacetsQuery, bool) {
	request := rustIssueTableFacetsQuery{
		Query: compiled.native, IncludeTotal: includeTotal, Facets: make([]rustIssueFacet, 0, len(facets)),
	}
	for _, spec := range facets {
		_, batch := issueTableBaseFacetExpression(publicQuery, spec)
		facet := rustIssueFacet{issueTableFacetSpec: spec, Batch: batch}
		switch spec.Kind {
		case "status", "priority", "assignee", "creator", "project", "label":
		case "property":
			property, ok := h.resolveIssueTableFacetProperty(w, r, compiled.workspaceID, spec.PropertyID)
			if !ok {
				return request, false
			}
			switch property.Type {
			case "select", "actor", "checkbox", "text", "url", "date", "number", "multi_text", "multi_url", "multi_select", "multi_actor":
				facet.PropertyType = property.Type
			default:
				writeIssueTableUnsupportedGroup(w, "property_type_unsupported", "This property type cannot be used as a filter facet.")
				return request, false
			}
			facet.PropertyKey = uuidToString(property.ID)
		case "working_agents":
			ids, ok := h.issueTableWorkingAgentIDs(w, r, compiled.workspaceID)
			if !ok {
				return request, false
			}
			request.WorkingAgentIDs = make([]string, 0, len(ids))
			for _, id := range ids {
				request.WorkingAgentIDs = append(request.WorkingAgentIDs, uuidToString(id))
			}
		default:
			writeError(w, http.StatusBadRequest, "invalid facets.kind")
			return request, false
		}
		request.Facets = append(request.Facets, facet)
	}
	return request, true
}

func (h *Handler) writeRustIssueTableFacets(w http.ResponseWriter, r *http.Request, compiled issueTableSQL, query rustIssueTableFacetsQuery) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	body, err := json.Marshal(query)
	if err != nil {
		writeIssueTableQueryFailure(w, r, "failed to prepare table facets query")
		return
	}
	upstream, ok := h.requestRustIssue(w, r, http.MethodPost, rustIssueTableFacetsPath, uuidToString(compiled.workspaceID), userID, body)
	if !ok {
		return
	}
	defer upstream.Body.Close()
	if upstream.StatusCode != http.StatusOK {
		writeRustIssueResponse(w, upstream, upstream.Body)
		return
	}
	var result issueTableFacetsResponse
	if err := json.NewDecoder(io.LimitReader(upstream.Body, maxRustIssueReadBody)).Decode(&result); err != nil || len(result.Facets) != len(query.Facets) {
		writeIssueTableQueryFailure(w, r, "invalid Rust table facets response")
		return
	}
	result.QueryFingerprint = compiled.fingerprint
	writeJSON(w, http.StatusOK, result)
}
