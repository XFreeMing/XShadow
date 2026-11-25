package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/logger"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const maxRustIssueReadBody = 16 << 20

var rustIssueReadClient = &http.Client{
	Timeout: 15 * time.Second,
	CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// IssueReadBridge routes GET /api/issues/{id} for explicitly configured pilot
// workspaces to the Rust service over trusted loopback HTTP. Workspaces outside
// the pilot stay on the default Go handler; workspaces inside the pilot never
// silently fall back to Go if the Rust service is unconfigured or errors.
type IssueReadBridge struct {
	Upstream    string
	Token       string
	PilotScopes []string
}

// NewIssueReadBridgeFromEnv configures the Rust issue detail read pilot from
// environment variables:
//   - XSHADOW_ISSUES_RUST_READ_PILOT: comma-separated workspace slugs, UUIDs,
//     or "*" / "all". Empty disables the pilot (default Go path).
//   - XSHADOW_ISSUES_UPSTREAM (fallback: XSHADOW_TABLES_UPSTREAM): loopback URL
//     of the Rust service.
//   - XSHADOW_ISSUES_INTERNAL_TOKEN (fallback: XSHADOW_TABLES_INTERNAL_TOKEN):
//     shared internal token verified by the Rust service.
func NewIssueReadBridgeFromEnv(log *slog.Logger) *IssueReadBridge {
	scopes := parsePilotScopes(os.Getenv("XSHADOW_ISSUES_RUST_READ_PILOT"))
	upstream := strings.TrimRight(strings.TrimSpace(os.Getenv("XSHADOW_ISSUES_UPSTREAM")), "/")
	if upstream == "" {
		upstream = strings.TrimRight(strings.TrimSpace(os.Getenv("XSHADOW_TABLES_UPSTREAM")), "/")
	}
	token := strings.TrimSpace(os.Getenv("XSHADOW_ISSUES_INTERNAL_TOKEN"))
	if token == "" {
		token = strings.TrimSpace(os.Getenv("XSHADOW_TABLES_INTERNAL_TOKEN"))
	}
	bridge := &IssueReadBridge{
		Upstream:    upstream,
		Token:       token,
		PilotScopes: scopes,
	}
	if len(scopes) > 0 && (!isLoopbackURL(upstream) || token == "") {
		if log != nil {
			log.Error("rust issue read pilot configured without a valid loopback upstream and internal token")
		}
		bridge.Upstream = ""
		bridge.Token = ""
	}
	return bridge
}

func parsePilotScopes(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	seen := make(map[string]struct{}, len(parts))
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		scope := strings.ToLower(strings.TrimSpace(part))
		if scope == "" {
			continue
		}
		if _, ok := seen[scope]; ok {
			continue
		}
		seen[scope] = struct{}{}
		out = append(out, scope)
	}
	return out
}

func (b *IssueReadBridge) HasPilotScopes() bool {
	return b != nil && len(b.PilotScopes) > 0
}

func (b *IssueReadBridge) Enabled() bool {
	return b != nil && b.Upstream != "" && b.Token != "" && isLoopbackURL(b.Upstream)
}

func (b *IssueReadBridge) MatchesWorkspace(ctx context.Context, queries *db.Queries, workspaceID string, wsUUID pgtype.UUID) (bool, error) {
	if !b.HasPilotScopes() {
		return false, nil
	}
	wsLower := strings.ToLower(strings.TrimSpace(workspaceID))
	needsSlugLookup := false
	for _, scope := range b.PilotScopes {
		if scope == "*" || scope == "all" {
			return true, nil
		}
		if scope == wsLower {
			return true, nil
		}
		if _, err := util.ParseUUID(scope); err != nil {
			needsSlugLookup = true
		}
	}
	if !needsSlugLookup {
		return false, nil
	}
	ws, err := queries.GetWorkspace(ctx, wsUUID)
	if err != nil {
		return false, err
	}
	slugLower := strings.ToLower(strings.TrimSpace(ws.Slug))
	for _, scope := range b.PilotScopes {
		if scope == slugLower {
			return true, nil
		}
	}
	return false, nil
}

func isLoopbackURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return false
	}
	host := parsed.Hostname()
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// GetIssueRoute dispatches GET /api/issues/{id} to the default Go handler
// unless the workspace is explicitly selected for the Rust read pilot.
func (h *Handler) GetIssueRoute(w http.ResponseWriter, r *http.Request) {
	if h.IssueReadBridge == nil || !h.IssueReadBridge.HasPilotScopes() {
		h.GetIssue(w, r)
		return
	}

	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := h.resolveWorkspaceID(r)
	if workspaceID == "" {
		writeError(w, http.StatusBadRequest, "workspace_id is required")
		return
	}
	wsUUID, err := util.ParseUUID(workspaceID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid workspace_id")
		return
	}

	inPilot, err := h.IssueReadBridge.MatchesWorkspace(r.Context(), h.Queries, workspaceID, wsUUID)
	if err != nil {
		if writeQueryCancellation(w, r, err) {
			return
		}
		slog.Warn("resolve rust issue read pilot failed", append(logger.RequestAttrs(r), "workspace_id", workspaceID, "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to resolve issue read route")
		return
	}
	if !inPilot {
		h.GetIssue(w, r)
		return
	}

	h.getIssueViaRust(w, r, workspaceID, wsUUID, userID)
}

func (h *Handler) getIssueViaRust(w http.ResponseWriter, r *http.Request, workspaceID string, wsUUID pgtype.UUID, userID string) {
	if !h.IssueReadBridge.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "rust issue read service unavailable")
		return
	}

	id := chi.URLParam(r, "id")
	actorType, actorID := h.resolveActor(r, userID, workspaceID)

	target := h.IssueReadBridge.Upstream + "/internal/issues/" + url.PathEscape(id)
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	upstreamReq, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid issue request")
		return
	}
	if requestID := r.Header.Get("X-Request-ID"); requestID != "" {
		upstreamReq.Header.Set("X-Request-ID", requestID)
	}
	upstreamReq.Header.Set("X-XShadow-Internal-Token", h.IssueReadBridge.Token)
	upstreamReq.Header.Set("X-XShadow-Workspace-Id", workspaceID)
	upstreamReq.Header.Set("X-XShadow-Actor-Source", actorType)
	upstreamReq.Header.Set("X-XShadow-Actor-Id", actorID)
	upstreamReq.Header.Set("X-XShadow-User-Id", userID)
	if actorType == "agent" {
		if taskID := strings.TrimSpace(r.Header.Get("X-Task-ID")); taskID != "" {
			upstreamReq.Header.Set("X-XShadow-Task-Id", taskID)
		}
	}

	resp, err := rustIssueReadClient.Do(upstreamReq)
	if err != nil {
		if writeQueryCancellation(w, r, err) {
			return
		}
		slog.Warn("rust issue read upstream failed", append(logger.RequestAttrs(r), "workspace_id", workspaceID, "error", err)...)
		writeError(w, http.StatusServiceUnavailable, "rust issue read service unavailable")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		writeError(w, http.StatusBadGateway, "unexpected rust issue redirect")
		return
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRustIssueReadBody+1))
	if err != nil {
		if writeQueryCancellation(w, r, err) {
			return
		}
		writeError(w, http.StatusBadGateway, "failed to read rust issue response")
		return
	}
	if len(body) > maxRustIssueReadBody {
		writeError(w, http.StatusBadGateway, "rust issue response too large")
		return
	}

	if resp.StatusCode != http.StatusOK {
		contentType := resp.Header.Get("Content-Type")
		if contentType == "" {
			contentType = "application/json"
		}
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(body)
		return
	}

	enrichAttachments := resp.Header.Get("X-XShadow-Enrich-Attachments") == "1"
	enrichSourceContext := resp.Header.Get("X-XShadow-Enrich-Source-Context") == "1"
	if !enrichAttachments && !enrichSourceContext {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
		return
	}

	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		writeError(w, http.StatusBadGateway, "invalid rust issue response")
		return
	}
	var issueIDStr string
	if rawID, ok := payload["id"]; !ok || json.Unmarshal(rawID, &issueIDStr) != nil {
		writeError(w, http.StatusBadGateway, "invalid rust issue response")
		return
	}
	issueUUID, err := util.ParseUUID(issueIDStr)
	if err != nil {
		writeError(w, http.StatusBadGateway, "invalid rust issue response")
		return
	}

	if enrichAttachments {
		attachments, err := h.Queries.ListAttachmentsByIssue(r.Context(), db.ListAttachmentsByIssueParams{
			IssueID:     issueUUID,
			WorkspaceID: wsUUID,
		})
		if err != nil {
			if writeQueryCancellation(w, r, err) {
				return
			}
			slog.Warn("enrich rust issue attachments failed", append(logger.RequestAttrs(r), "issue_id", issueIDStr, "error", err)...)
			writeError(w, http.StatusInternalServerError, "failed to load issue attachments")
			return
		}
		if len(attachments) > 0 {
			mode := attachmentURLModeFromRequest(r)
			rendered := make([]AttachmentResponse, len(attachments))
			for i, a := range attachments {
				rendered[i] = h.attachmentToResponse(a, mode)
			}
			encoded, err := json.Marshal(rendered)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "failed to encode issue attachments")
				return
			}
			payload["attachments"] = encoded
		}
	}

	if enrichSourceContext {
		hidden, ok := h.requestHiddenProjects(w, r, workspaceID)
		if !ok {
			return
		}
		sourceContext, err := h.issueSourceContextDetail(r.Context(), db.Issue{
			ID:          issueUUID,
			WorkspaceID: wsUUID,
		}, hidden)
		if err == nil {
			encoded, marshalErr := json.Marshal(sourceContext)
			if marshalErr != nil {
				writeError(w, http.StatusInternalServerError, "failed to encode issue source context")
				return
			}
			payload["source_context"] = encoded
		} else if !errors.Is(err, pgx.ErrNoRows) {
			if writeQueryCancellation(w, r, err) {
				return
			}
			slog.Warn("load issue source context failed", append(logger.RequestAttrs(r), "issue_id", issueIDStr, "error", err)...)
			writeError(w, http.StatusInternalServerError, "failed to load issue source context")
			return
		}
	}

	if writeQueryCancellation(w, r, r.Context().Err()) {
		return
	}
	writeJSON(w, http.StatusOK, payload)
}
