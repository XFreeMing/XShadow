// Package tablesbridge forwards workspace-scoped generic-table requests to the
// Rust service. Membership is decided by the surrounding middleware. The
// workspace id sent onward is the one that middleware stored, never the
// client's X-Workspace-ID header.
package tablesbridge

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/util"
)

const maxBody = 1 << 20

// Bridge is disabled when upstream or token is empty, or when upstream is not
// a loopback URL. A disabled bridge answers only the table routes.
type Bridge struct {
	Upstream string
	Token    string
	Client   *http.Client
	// AgentLabel returns the agent name when it belongs to this workspace.
	// A miss or another workspace yields an empty label. The actor id still
	// comes from the stamped task token, never from this lookup.
	AgentLabel func(ctx context.Context, workspaceID, agentID string) string
}

func FromEnv(logger *slog.Logger) *Bridge {
	upstream := strings.TrimRight(strings.TrimSpace(os.Getenv("XSHADOW_TABLES_UPSTREAM")), "/")
	token := strings.TrimSpace(os.Getenv("XSHADOW_TABLES_INTERNAL_TOKEN"))
	bridge := &Bridge{
		Upstream: upstream,
		Token:    token,
		Client:   &http.Client{Timeout: 15 * time.Second},
	}
	if upstream == "" && token == "" {
		return bridge
	}
	if !loopback(upstream) || token == "" {
		logger.Error("generic tables bridge disabled: upstream must be loopback and the internal token must be set")
		bridge.Upstream = ""
		bridge.Token = ""
	}
	return bridge
}

func (b *Bridge) Enabled() bool {
	return b != nil && b.Upstream != "" && b.Token != "" && loopback(b.Upstream)
}

func (b *Bridge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !b.Enabled() {
		writeJSON(w, http.StatusServiceUnavailable, `{"error":"表格服务未启用","code":"tables_unavailable"}`)
		return
	}
	workspaceID := middleware.WorkspaceIDFromContext(r.Context())
	if _, err := util.ParseUUID(workspaceID); err != nil {
		writeJSON(w, http.StatusBadRequest, `{"error":"工作区标识无效","code":"invalid_workspace"}`)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, `{"error":"请求过大或无法读取","code":"invalid_value"}`)
		return
	}
	target := b.Upstream + collectionPath(r.URL.Path)
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	upstream, err := http.NewRequestWithContext(ctx, r.Method, target, bytes.NewReader(body))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, `{"error":"请求无法转发","code":"invalid_value"}`)
		return
	}
	if contentType := r.Header.Get("Content-Type"); contentType != "" {
		upstream.Header.Set("Content-Type", contentType)
	}
	if requestID := r.Header.Get("X-Request-ID"); requestID != "" {
		upstream.Header.Set("X-Request-ID", requestID)
	}
	upstream.Header.Set("X-XShadow-Internal-Token", b.Token)
	upstream.Header.Set("X-XShadow-Workspace-Id", workspaceID)
	actor, ok := trustedActor(r)
	mutating := r.Method != http.MethodGet && r.Method != http.MethodHead
	if r.Header.Get("X-Actor-Source") == "task_token" && !ok {
		writeJSON(w, http.StatusUnauthorized, `{"error":"协作者身份无效","code":"unauthorized"}`)
		return
	}
	if !ok && mutating {
		writeJSON(w, http.StatusUnauthorized, `{"error":"协作者身份无效","code":"unauthorized"}`)
		return
	}
	if ok {
		if actor.source == "agent" && b.AgentLabel != nil {
			actor.label = sanitizeLabel(b.AgentLabel(r.Context(), workspaceID, actor.id))
		}
		upstream.Header.Set("X-XShadow-Actor-Source", actor.source)
		upstream.Header.Set("X-XShadow-Actor-Id", actor.id)
		if actor.taskID != "" {
			upstream.Header.Set("X-XShadow-Task-Id", actor.taskID)
		}
		if actor.label != "" {
			upstream.Header.Set("X-XShadow-Actor-Label", actor.label)
		}
	}
	response, err := b.client().Do(upstream)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, `{"error":"表格服务暂时不可用","code":"tables_unavailable"}`)
		return
	}
	defer response.Body.Close()
	if contentType := response.Header.Get("Content-Type"); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, response.Body)
}

// collectionPath maps the public collection root, with or without a trailing
// slash, onto the slash-free Rust route. Axum does not treat those as the
// same path, and a forced slash makes both list requests 404.
func collectionPath(requestPath string) string {
	rest := strings.TrimPrefix(requestPath, "/api/tables")
	if rest == "" || rest == "/" {
		return "/internal/tables"
	}
	return "/internal/tables" + rest
}

type trusted struct {
	source string
	id     string
	taskID string
	label  string
}

// trustedActor uses the middleware-stamped task token or the verified member.
// Client actor headers are not a source: task_token is the only agent signal,
// and a member id comes from the membership context.
func trustedActor(r *http.Request) (trusted, bool) {
	if r.Header.Get("X-Actor-Source") == "task_token" {
		agentID := strings.TrimSpace(r.Header.Get("X-Agent-ID"))
		taskID := strings.TrimSpace(r.Header.Get("X-Task-ID"))
		if _, err := util.ParseUUID(agentID); err != nil {
			return trusted{}, false
		}
		if _, err := util.ParseUUID(taskID); err != nil {
			return trusted{}, false
		}
		return trusted{source: "agent", id: agentID, taskID: taskID}, true
	}
	member, ok := middleware.MemberFromContext(r.Context())
	if !ok {
		return trusted{}, false
	}
	userID := util.UUIDToString(member.UserID)
	if _, err := util.ParseUUID(userID); err != nil {
		return trusted{}, false
	}
	return trusted{source: "member", id: userID}, true
}

func sanitizeLabel(raw string) string {
	raw = strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, raw))
	runes := []rune(raw)
	if len(runes) > 40 {
		raw = string(runes[:40])
	}
	return strings.TrimSpace(raw)
}

func (b *Bridge) client() *http.Client {
	if b.Client != nil {
		return b.Client
	}
	return http.DefaultClient
}

func loopback(raw string) bool {
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

func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}
