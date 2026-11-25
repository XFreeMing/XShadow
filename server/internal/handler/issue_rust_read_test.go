package handler

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestIssueReadBridgeFromEnvAndPilotMatching(t *testing.T) {
	t.Run("default env has no pilot scopes", func(t *testing.T) {
		t.Setenv("XSHADOW_ISSUES_RUST_READ_PILOT", "")
		t.Setenv("XSHADOW_ISSUES_UPSTREAM", "")
		t.Setenv("XSHADOW_TABLES_UPSTREAM", "")
		t.Setenv("XSHADOW_ISSUES_INTERNAL_TOKEN", "")
		t.Setenv("XSHADOW_TABLES_INTERNAL_TOKEN", "")

		bridge := NewIssueReadBridgeFromEnv(nil)
		if bridge.HasPilotScopes() {
			t.Fatalf("expected no pilot scopes by default, got %v", bridge.PilotScopes)
		}
		if bridge.Enabled() {
			t.Fatalf("expected bridge disabled when unset")
		}
	})

	t.Run("rejects non-loopback upstream when pilot configured", func(t *testing.T) {
		t.Setenv("XSHADOW_ISSUES_RUST_READ_PILOT", "pilot-ws")
		t.Setenv("XSHADOW_ISSUES_UPSTREAM", "http://example.com:8080")
		t.Setenv("XSHADOW_ISSUES_INTERNAL_TOKEN", "secret")

		bridge := NewIssueReadBridgeFromEnv(nil)
		if !bridge.HasPilotScopes() {
			t.Fatalf("expected pilot scope retained so scoped requests fail closed")
		}
		if bridge.Enabled() {
			t.Fatalf("expected non-loopback upstream to be disabled")
		}
	})

	t.Run("assign pilot scopes are independent of other pilots", func(t *testing.T) {
		t.Setenv("XSHADOW_ISSUES_RUST_ASSIGN_PILOT", "assign-pilot")
		t.Setenv("XSHADOW_ISSUES_UPSTREAM", "http://127.0.0.1:7")
		t.Setenv("XSHADOW_ISSUES_INTERNAL_TOKEN", "secret")
		bridge := NewIssueReadBridgeFromEnv(nil)
		if !reflect.DeepEqual(bridge.AssignPilotScopes, []string{"assign-pilot"}) {
			t.Fatalf("AssignPilotScopes = %v, want [assign-pilot]", bridge.AssignPilotScopes)
		}
	})

	t.Run("batch pilot scopes are independent of write pilot", func(t *testing.T) {
		t.Setenv("XSHADOW_ISSUES_RUST_BATCH_PILOT", "batch-pilot")
		t.Setenv("XSHADOW_ISSUES_RUST_WRITE_PILOT", "write-pilot")
		t.Setenv("XSHADOW_TABLES_UPSTREAM", "http://127.0.0.1:18080")
		t.Setenv("XSHADOW_TABLES_INTERNAL_TOKEN", "shared-secret")

		bridge := NewIssueReadBridgeFromEnv(nil)
		if !bridge.Enabled() {
			t.Fatal("expected bridge enabled with loopback upstream")
		}
		if !reflect.DeepEqual(bridge.BatchPilotScopes, []string{"batch-pilot"}) {
			t.Fatalf("BatchPilotScopes = %v, want [batch-pilot]", bridge.BatchPilotScopes)
		}
		if !reflect.DeepEqual(bridge.WritePilotScopes, []string{"write-pilot"}) {
			t.Fatalf("WritePilotScopes = %v, want [write-pilot]", bridge.WritePilotScopes)
		}
	})

	t.Run("terminal status pilot scopes are independent", func(t *testing.T) {
		t.Setenv("XSHADOW_ISSUES_RUST_STATUS_PILOT", "status-pilot")
		t.Setenv("XSHADOW_ISSUES_RUST_TERMINAL_STATUS_PILOT", "terminal-pilot")
		t.Setenv("XSHADOW_TABLES_UPSTREAM", "http://127.0.0.1:18080")
		t.Setenv("XSHADOW_TABLES_INTERNAL_TOKEN", "shared-secret")

		bridge := NewIssueReadBridgeFromEnv(nil)
		if !reflect.DeepEqual(bridge.TerminalStatusPilotScopes, []string{"terminal-pilot"}) {
			t.Fatalf("TerminalStatusPilotScopes = %v, want [terminal-pilot]", bridge.TerminalStatusPilotScopes)
		}
	})

	t.Run("status pilot scopes are independent of other pilots", func(t *testing.T) {
		t.Setenv("XSHADOW_ISSUES_RUST_STATUS_PILOT", "status-pilot")
		t.Setenv("XSHADOW_ISSUES_RUST_WRITE_PILOT", "")
		t.Setenv("XSHADOW_TABLES_UPSTREAM", "http://127.0.0.1:18080")
		t.Setenv("XSHADOW_TABLES_INTERNAL_TOKEN", "shared-secret")

		bridge := NewIssueReadBridgeFromEnv(nil)
		if !bridge.Enabled() {
			t.Fatal("expected bridge enabled with loopback upstream")
		}
		if !reflect.DeepEqual(bridge.StatusPilotScopes, []string{"status-pilot"}) {
			t.Fatalf("StatusPilotScopes = %v, want [status-pilot]", bridge.StatusPilotScopes)
		}
		if len(bridge.WritePilotScopes) != 0 {
			t.Fatalf("WritePilotScopes = %v, want empty", bridge.WritePilotScopes)
		}
	})

	t.Run("acceptance pilot scopes are independent of write pilot", func(t *testing.T) {
		t.Setenv("XSHADOW_ISSUES_RUST_WRITE_PILOT", "")
		t.Setenv("XSHADOW_ISSUES_RUST_ACCEPTANCE_PILOT", "acc-pilot ")
		t.Setenv("XSHADOW_TABLES_UPSTREAM", "http://127.0.0.1:18080")
		t.Setenv("XSHADOW_TABLES_INTERNAL_TOKEN", "shared-secret")

		bridge := NewIssueReadBridgeFromEnv(nil)
		if !bridge.Enabled() {
			t.Fatal("expected bridge enabled with loopback upstream")
		}
		if !reflect.DeepEqual(bridge.AcceptancePilotScopes, []string{"acc-pilot"}) {
			t.Fatalf("AcceptancePilotScopes = %v, want [acc-pilot]", bridge.AcceptancePilotScopes)
		}
		if len(bridge.WritePilotScopes) != 0 {
			t.Fatalf("WritePilotScopes = %v, want empty", bridge.WritePilotScopes)
		}
	})

	t.Run("falls back to tables upstream and token when issue-specific env unset", func(t *testing.T) {
		t.Setenv("XSHADOW_ISSUES_RUST_READ_PILOT", "pilot-a, PILOT-B ")
		t.Setenv("XSHADOW_ISSUES_UPSTREAM", "")
		t.Setenv("XSHADOW_TABLES_UPSTREAM", "http://127.0.0.1:18080/")
		t.Setenv("XSHADOW_ISSUES_INTERNAL_TOKEN", "")
		t.Setenv("XSHADOW_TABLES_INTERNAL_TOKEN", "shared-secret")

		bridge := NewIssueReadBridgeFromEnv(nil)
		if !bridge.Enabled() {
			t.Fatalf("expected loopback upstream with token to be enabled")
		}
		if bridge.Upstream != "http://127.0.0.1:18080" {
			t.Fatalf("Upstream = %q, want trimmed loopback URL", bridge.Upstream)
		}
		if !reflect.DeepEqual(bridge.PilotScopes, []string{"pilot-a", "pilot-b"}) {
			t.Fatalf("PilotScopes = %v, want [pilot-a pilot-b]", bridge.PilotScopes)
		}
	})
}

func TestGetIssueRouteDefaultGoVsPilotRustAndNoSilentFallback(t *testing.T) {
	ctx := context.Background()
	issue := createIssueWithPropertyBody(t, map[string]any{
		"title": "Pilot routing test " + uuid.NewString()[:8],
	})

	var upstreamHits atomic.Int64
	var lastToken, lastWorkspace, lastActorSource, lastActorID, lastUserID string
	var redirectLocation string
	upstreamStatus := http.StatusOK
	upstreamBody := `{"id":"` + issue.ID + `","workspace_id":"` + testWorkspaceID + `","number":` + "1" + `,"identifier":"` + issue.Identifier + `","title":"From Rust Mock","description":null,"status":"todo","status_category":"todo","status_name":"","priority":"none","assignee_type":null,"assignee_id":null,"creator_type":"member","creator_id":"` + testUserID + `","requester_id":null,"parent_issue_id":null,"duplicate_of":null,"project_id":null,"position":0,"stage":null,"start_date":null,"due_date":null,"created_at":"2026-11-25T09:00:00Z","updated_at":"2026-11-25T09:00:00Z","revision":1,"last_activity_at":null,"metadata":{},"properties":{},"issue_type_id":null,"labels":[]}`

	mockRust := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits.Add(1)
		lastToken = r.Header.Get("X-XShadow-Internal-Token")
		lastWorkspace = r.Header.Get("X-XShadow-Workspace-Id")
		lastActorSource = r.Header.Get("X-XShadow-Actor-Source")
		lastActorID = r.Header.Get("X-XShadow-Actor-Id")
		lastUserID = r.Header.Get("X-XShadow-User-Id")
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-XShadow-Issue-Backend", "rust")
		if redirectLocation != "" {
			w.Header().Set("Location", redirectLocation)
		}
		w.WriteHeader(upstreamStatus)
		_, _ = w.Write([]byte(upstreamBody))
	}))
	defer mockRust.Close()

	h := *testHandler
	callRoute := func(id string) *httptest.ResponseRecorder {
		req := newRequest(http.MethodGet, "/api/issues/"+id, nil)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", id)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		w := httptest.NewRecorder()
		h.GetIssueRoute(w, req)
		return w
	}

	// 1. Default (nil bridge): Go serves the request, Rust is never called.
	h.IssueReadBridge = nil
	w := callRoute(issue.ID)
	if w.Code != http.StatusOK {
		t.Fatalf("default Go status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if upstreamHits.Load() != 0 {
		t.Fatalf("default Go path called Rust %d times, want 0", upstreamHits.Load())
	}
	var goResp IssueResponse
	if err := json.Unmarshal(w.Body.Bytes(), &goResp); err != nil {
		t.Fatalf("decode Go response: %v", err)
	}
	if goResp.Title != issue.Title {
		t.Fatalf("default Go title = %q, want %q", goResp.Title, issue.Title)
	}

	// 2. Pilot configured for a DIFFERENT workspace: still uses Go.
	h.IssueReadBridge = &IssueReadBridge{
		Upstream:    mockRust.URL,
		Token:       "pilot-secret",
		PilotScopes: []string{"some-other-workspace-slug"},
	}
	w = callRoute(issue.ID)
	if w.Code != http.StatusOK {
		t.Fatalf("non-pilot workspace status = %d, want 200", w.Code)
	}
	if upstreamHits.Load() != 0 {
		t.Fatalf("non-pilot workspace called Rust %d times, want 0", upstreamHits.Load())
	}

	// 3. Pilot configured for this workspace's slug: Rust serves the request.
	ws, err := testHandler.Queries.GetWorkspace(ctx, util.MustParseUUID(testWorkspaceID))
	if err != nil {
		t.Fatalf("GetWorkspace: %v", err)
	}
	h.IssueReadBridge = &IssueReadBridge{
		Upstream:    mockRust.URL,
		Token:       "pilot-secret",
		PilotScopes: []string{strings.ToLower(ws.Slug)},
	}
	w = callRoute(issue.ID)
	if w.Code != http.StatusOK {
		t.Fatalf("pilot Rust status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if upstreamHits.Load() != 1 {
		t.Fatalf("pilot Rust hits = %d, want 1", upstreamHits.Load())
	}
	if lastToken != "pilot-secret" || lastWorkspace != testWorkspaceID || lastActorSource != "member" || lastActorID != testUserID || lastUserID != testUserID {
		t.Fatalf("unexpected trusted headers forwarded to Rust: token=%q ws=%q source=%q actor=%q user=%q",
			lastToken, lastWorkspace, lastActorSource, lastActorID, lastUserID)
	}
	var rustResp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &rustResp); err != nil {
		t.Fatalf("decode Rust response: %v", err)
	}
	if rustResp["title"] != "From Rust Mock" {
		t.Fatalf("expected mock Rust payload, got title=%v", rustResp["title"])
	}

	// 4. Scoped request when Rust returns 500: must NOT silently fall back to Go.
	upstreamStatus = http.StatusInternalServerError
	upstreamBody = `{"error":"failed to load issue"}`
	w = callRoute(issue.ID)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("Rust 500 status = %d, want 500 (no silent fallback); body=%s", w.Code, w.Body.String())
	}
	if upstreamHits.Load() != 2 {
		t.Fatalf("expected second Rust hit, got %d", upstreamHits.Load())
	}

	var redirectedHits atomic.Int64
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirectedHits.Add(1)
	}))
	defer redirectTarget.Close()
	redirectLocation = redirectTarget.URL
	upstreamStatus = http.StatusTemporaryRedirect
	w = callRoute(issue.ID)
	if w.Code != http.StatusBadGateway || redirectedHits.Load() != 0 {
		t.Fatalf("upstream redirect status = %d, redirect hits = %d", w.Code, redirectedHits.Load())
	}

	// 5. Scoped request when Rust bridge is misconfigured: returns 503, no fallback.
	h.IssueReadBridge = &IssueReadBridge{
		Upstream:    "",
		Token:       "",
		PilotScopes: []string{strings.ToLower(testWorkspaceID)},
	}
	w = callRoute(issue.ID)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("misconfigured pilot status = %d, want 503 (no silent fallback); body=%s", w.Code, w.Body.String())
	}

	// 6. Scoped request when Rust upstream is unreachable: returns 503, no fallback.
	mockRust.Close()
	h.IssueReadBridge = &IssueReadBridge{
		Upstream:    mockRust.URL,
		Token:       "pilot-secret",
		PilotScopes: []string{strings.ToLower(testWorkspaceID)},
	}
	w = callRoute(issue.ID)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("unreachable Rust status = %d, want 503 (no silent fallback); body=%s", w.Code, w.Body.String())
	}
}

func startRustCandidateForTest(t *testing.T, token string) (string, *atomic.Int64) {
	t.Helper()
	bin := filepath.Join("..", "..", "..", "rust", "app", "target", "debug", "xshadow-server")
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("Rust binary %s not built; skipping live Rust parity test", bin)
	}
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://multica:multica@localhost:5432/multica_test?sslmode=disable"
	}

	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(),
		"PORT=0",
		"DATABASE_URL="+dbURL,
		"XSHADOW_ISSUES_INTERNAL_TOKEN="+token,
		"XSHADOW_TABLES_INTERNAL_TOKEN="+token,
	)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatalf("stderr pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start xshadow-server: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	addrCh := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := scanner.Text()
			if idx := strings.Index(line, "listening on "); idx >= 0 {
				addrCh <- "http://" + strings.TrimSpace(line[idx+len("listening on "):])
				return
			}
		}
	}()

	var rustAddr string
	select {
	case rustAddr = <-addrCh:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for xshadow-server to listen")
	}

	parsed, err := url.Parse(rustAddr)
	if err != nil {
		t.Fatalf("parse rust addr %q: %v", rustAddr, err)
	}
	var hits atomic.Int64
	proxy := httputil.NewSingleHostReverseProxy(parsed)
	countingProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(countingProxy.Close)
	return countingProxy.URL, &hits
}

func TestLiveRustIssueReadParityVisibilityAndEnrichment(t *testing.T) {
	token := "live-parity-token-" + uuid.NewString()[:8]
	rustUpstream, rustHits := startRustCandidateForTest(t, token)

	ctx := context.Background()
	wsUUID := util.MustParseUUID(testWorkspaceID)
	suffix := uuid.NewString()[:8]

	// Create a regular non-admin member user to test private project visibility.
	memberUserID := uuid.NewString()
	_, err := testPool.Exec(ctx,
		`INSERT INTO "user" (id, name, email) VALUES ($1, $2, $3)`,
		memberUserID, "Member "+suffix, "member-"+suffix+"@example.com",
	)
	if err != nil {
		t.Fatalf("insert member user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM "user" WHERE id = $1`, memberUserID)
	})
	_, err = testPool.Exec(ctx,
		`INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'member')`,
		testWorkspaceID, memberUserID,
	)
	if err != nil {
		t.Fatalf("insert member: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM member WHERE workspace_id = $1 AND user_id = $2`, testWorkspaceID, memberUserID)
	})

	// Create a private project (not shared with memberUserID).
	privateProjectID := uuid.NewString()
	_, err = testPool.Exec(ctx,
		`INSERT INTO project (id, workspace_id, title, visibility) VALUES ($1, $2, $3, 'private')`,
		privateProjectID, testWorkspaceID, "Private Project "+suffix,
	)
	if err != nil {
		t.Fatalf("insert private project: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM project WHERE id = $1`, privateProjectID)
	})

	// Create custom properties + task type (TODO 4 contract) and a custom status.
	readCount := createTestProperty(t, map[string]any{"name": "Reads " + suffix, "type": "number"})
	confirmed := createTestProperty(t, map[string]any{"name": "Confirmed " + suffix, "type": "checkbox"})
	taskType := createTestIssueType(t, map[string]any{
		"name": "ParityType " + suffix,
		"fields": []map[string]any{
			{"property_id": readCount.ID, "required": false, "default_value": 0},
			{"property_id": confirmed.ID, "required": false, "default_value": false},
		},
	})

	customStatusKey := "qa_" + strings.ToLower(suffix[:6])
	_, err = testPool.Exec(ctx,
		`INSERT INTO issue_status (workspace_id, key, name, description, category, color, is_system, position)
		 VALUES ($1, $2, $3, '', 'started', '#22c55e', FALSE, 99)`,
		testWorkspaceID, customStatusKey, "QA Gate "+suffix,
	)
	if err != nil {
		t.Fatalf("insert custom status: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM issue_status WHERE workspace_id = $1 AND key = $2`, testWorkspaceID, customStatusKey)
	})

	// Create rich visible issue with custom status, task type, properties (0, false),
	// label, reaction, attachment, and acceptance trace.
	richIssue := createIssueWithPropertyBody(t, map[string]any{
		"title":         "Rich Parity Issue " + suffix,
		"description":   "Detailed body for parity verification",
		"issue_type_id": taskType.ID,
		"properties": map[string]any{
			readCount.ID: 0,
			confirmed.ID: false,
		},
	})
	_, err = testPool.Exec(ctx,
		`UPDATE issue
		 SET status = $1,
		     stage = 2,
		     start_date = '2026-11-01',
		     due_date = '2026-11-30',
		     metadata = '{"env":"pilot","retries":0}'::jsonb,
		     acceptance_criteria = '{"revision":1,"criteria":[{"id":"ac1","versions":["Must match Go"],"cases":[],"implementations":[]}]}'::jsonb
		 WHERE id = $2`,
		customStatusKey, richIssue.ID,
	)
	if err != nil {
		t.Fatalf("update rich issue fields: %v", err)
	}

	// Add label, reaction, and attachment to richIssue.
	labelID := uuid.NewString()
	_, err = testPool.Exec(ctx,
		`INSERT INTO issue_label (id, workspace_id, resource_type, name, description, color)
		 VALUES ($1, $2, 'issue', $3, 'desc', '#ff0000')`,
		labelID, testWorkspaceID, "parity-"+suffix,
	)
	if err != nil {
		t.Fatalf("insert label: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM issue_to_label WHERE label_id = $1`, labelID)
		_, _ = testPool.Exec(ctx, `DELETE FROM issue_label WHERE id = $1`, labelID)
	})
	_, err = testPool.Exec(ctx, `INSERT INTO issue_to_label (issue_id, label_id) VALUES ($1, $2)`, richIssue.ID, labelID)
	if err != nil {
		t.Fatalf("link label: %v", err)
	}

	reactionID := uuid.NewString()
	_, err = testPool.Exec(ctx,
		`INSERT INTO issue_reaction (id, workspace_id, issue_id, actor_type, actor_id, emoji)
		 VALUES ($1, $2, $3, 'member', $4, '👍')`,
		reactionID, testWorkspaceID, richIssue.ID, testUserID,
	)
	if err != nil {
		t.Fatalf("insert reaction: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM issue_reaction WHERE id = $1`, reactionID)
	})

	attID := uuid.NewString()
	_, err = testPool.Exec(ctx,
		`INSERT INTO attachment (id, workspace_id, issue_id, uploader_type, uploader_id, filename, url, content_type, size_bytes)
		 VALUES ($1, $2, $3, 'member', $4, 'spec.txt', 'https://cdn.example.com/spec.txt', 'text/plain', 128)`,
		attID, testWorkspaceID, richIssue.ID, testUserID,
	)
	if err != nil {
		t.Fatalf("insert attachment: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM attachment WHERE id = $1`, attID)
	})

	// Create private issue and a cancelled duplicate pointing to it.
	privateIssue := createIssueWithPropertyBody(t, map[string]any{
		"title":      "Private Parity Issue " + suffix,
		"project_id": privateProjectID,
	})
	dupIssue := createIssueWithPropertyBody(t, map[string]any{
		"title": "Duplicate of Private " + suffix,
	})
	_, err = testPool.Exec(ctx,
		`UPDATE issue SET status = 'cancelled', duplicate_of_issue_id = $1 WHERE id = $2`,
		privateIssue.ID, dupIssue.ID,
	)
	if err != nil {
		t.Fatalf("mark duplicate: %v", err)
	}

	// Build a handler wired to the real Rust candidate for this workspace.
	rustHandler := *testHandler
	rustHandler.IssueReadBridge = &IssueReadBridge{
		Upstream:    rustUpstream,
		Token:       token,
		PilotScopes: []string{strings.ToLower(testWorkspaceID)},
	}

	callAs := func(h *Handler, fn func(http.ResponseWriter, *http.Request), id, userID, role string, headers map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/issues/"+id, nil)
		req.Header.Set("X-User-ID", userID)
		req.Header.Set("X-Workspace-ID", testWorkspaceID)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		member := db.Member{
			WorkspaceID: wsUUID,
			UserID:      util.MustParseUUID(userID),
			Role:        role,
		}
		ctx := middleware.SetMemberContext(req.Context(), testWorkspaceID, member)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", id)
		ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
		req = req.WithContext(ctx)
		w := httptest.NewRecorder()
		fn(w, req)
		return w
	}

	decodeMap := func(t *testing.T, body []byte) map[string]any {
		t.Helper()
		var out map[string]any
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatalf("unmarshal json: %v\nbody=%s", err, string(body))
		}
		return out
	}

	// 1. Exact Go-vs-Rust parity on rich issue by UUID and lowercase short identifier.
	for _, lookupID := range []string{richIssue.ID, strings.ToLower(richIssue.Identifier)} {
		goW := callAs(testHandler, testHandler.GetIssue, lookupID, testUserID, "owner", map[string]string{
			"X-Client-Capabilities": ClientCapabilityStableAttachmentURLs,
		})
		if goW.Code != http.StatusOK {
			t.Fatalf("Go GetIssue(%q) status = %d, want 200; body=%s", lookupID, goW.Code, goW.Body.String())
		}
		beforeHits := rustHits.Load()
		rustW := callAs(&rustHandler, rustHandler.GetIssueRoute, lookupID, testUserID, "owner", map[string]string{
			"X-Client-Capabilities": ClientCapabilityStableAttachmentURLs,
		})
		if rustW.Code != http.StatusOK {
			t.Fatalf("Rust GetIssueRoute(%q) status = %d, want 200; body=%s", lookupID, rustW.Code, rustW.Body.String())
		}
		if rustHits.Load() != beforeHits+1 {
			t.Fatalf("expected Rust upstream hit for %q", lookupID)
		}
		goJSON := decodeMap(t, goW.Body.Bytes())
		rustJSON := decodeMap(t, rustW.Body.Bytes())
		if !reflect.DeepEqual(goJSON, rustJSON) {
			t.Fatalf("Go vs Rust payload mismatch for %q:\nGo:   %s\nRust: %s", lookupID, goW.Body.String(), rustW.Body.String())
		}
		// Spot-check critical fields.
		if rustJSON["status_category"] != "in_progress" || rustJSON["status_name"] != "QA Gate "+suffix {
			t.Fatalf("unexpected custom status resolution: category=%v name=%v", rustJSON["status_category"], rustJSON["status_name"])
		}
		if rustJSON["issue_type_id"] != taskType.ID {
			t.Fatalf("issue_type_id = %v, want %s", rustJSON["issue_type_id"], taskType.ID)
		}
		props := rustJSON["properties"].(map[string]any)
		if props[readCount.ID] != float64(0) || props[confirmed.ID] != false {
			t.Fatalf("properties zero/false lost: %#v", props)
		}
		atts, ok := rustJSON["attachments"].([]any)
		if !ok || len(atts) != 1 {
			t.Fatalf("expected 1 enriched attachment, got %#v", rustJSON["attachments"])
		}
	}

	// 2. Private project visibility: owner sees it, non-member gets 404 matching Go.
	goOwnerPriv := callAs(testHandler, testHandler.GetIssue, privateIssue.ID, testUserID, "owner", nil)
	rustOwnerPriv := callAs(&rustHandler, rustHandler.GetIssueRoute, privateIssue.ID, testUserID, "owner", nil)
	if goOwnerPriv.Code != http.StatusOK || rustOwnerPriv.Code != http.StatusOK {
		t.Fatalf("owner private read codes: go=%d rust=%d", goOwnerPriv.Code, rustOwnerPriv.Code)
	}
	if !reflect.DeepEqual(decodeMap(t, goOwnerPriv.Body.Bytes()), decodeMap(t, rustOwnerPriv.Body.Bytes())) {
		t.Fatalf("owner private issue mismatch:\nGo:   %s\nRust: %s", goOwnerPriv.Body.String(), rustOwnerPriv.Body.String())
	}

	goMemberPriv := callAs(testHandler, testHandler.GetIssue, privateIssue.Identifier, memberUserID, "member", nil)
	rustMemberPriv := callAs(&rustHandler, rustHandler.GetIssueRoute, privateIssue.Identifier, memberUserID, "member", nil)
	if goMemberPriv.Code != http.StatusNotFound || rustMemberPriv.Code != http.StatusNotFound {
		t.Fatalf("member private read codes: go=%d rust=%d, want 404", goMemberPriv.Code, rustMemberPriv.Code)
	}
	if !reflect.DeepEqual(decodeMap(t, goMemberPriv.Body.Bytes()), decodeMap(t, rustMemberPriv.Body.Bytes())) {
		t.Fatalf("member private 404 body mismatch:\nGo:   %s\nRust: %s", goMemberPriv.Body.String(), rustMemberPriv.Body.String())
	}

	// 3. Duplicate-of redaction for hidden private original: owner sees duplicate_of, member sees null.
	goOwnerDup := callAs(testHandler, testHandler.GetIssue, dupIssue.ID, testUserID, "owner", nil)
	rustOwnerDup := callAs(&rustHandler, rustHandler.GetIssueRoute, dupIssue.ID, testUserID, "owner", nil)
	if !reflect.DeepEqual(decodeMap(t, goOwnerDup.Body.Bytes()), decodeMap(t, rustOwnerDup.Body.Bytes())) {
		t.Fatalf("owner duplicate_of mismatch:\nGo:   %s\nRust: %s", goOwnerDup.Body.String(), rustOwnerDup.Body.String())
	}
	if decodeMap(t, rustOwnerDup.Body.Bytes())["duplicate_of"] == nil {
		t.Fatalf("expected owner to see duplicate_of")
	}

	goMemberDup := callAs(testHandler, testHandler.GetIssue, dupIssue.ID, memberUserID, "member", nil)
	rustMemberDup := callAs(&rustHandler, rustHandler.GetIssueRoute, dupIssue.ID, memberUserID, "member", nil)
	if !reflect.DeepEqual(decodeMap(t, goMemberDup.Body.Bytes()), decodeMap(t, rustMemberDup.Body.Bytes())) {
		t.Fatalf("member duplicate_of mismatch:\nGo:   %s\nRust: %s", goMemberDup.Body.String(), rustMemberDup.Body.String())
	}
	if decodeMap(t, rustMemberDup.Body.Bytes())["duplicate_of"] != nil {
		t.Fatalf("expected member duplicate_of to be redacted to null")
	}

	// 4. Task-scoped agent caller: bounded by originator + agent owner.
	agentID := uuid.NewString()
	_, err = testPool.Exec(ctx,
		`INSERT INTO agent (id, workspace_id, name, owner_id, runtime_mode, runtime_id)
		 VALUES ($1, $2, $3, $4, 'cloud', $5)`,
		agentID, testWorkspaceID, "ParityAgent-"+suffix, memberUserID, testRuntimeID,
	)
	if err != nil {
		t.Fatalf("insert agent: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM agent WHERE id = $1`, agentID)
	})
	taskID := uuid.NewString()
	_, err = testPool.Exec(ctx,
		`INSERT INTO agent_task_queue (id, agent_id, issue_id, status, originator_user_id, accountable_user_id, runtime_id)
		 VALUES ($1, $2, $3, 'running', $4, $4, $5)`,
		taskID, agentID, richIssue.ID, testUserID, testRuntimeID,
	)
	if err != nil {
		t.Fatalf("insert agent task: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE id = $1`, taskID)
	})

	agentHeaders := map[string]string{
		"X-Actor-Source": "task_token",
		"X-Agent-ID":     agentID,
		"X-Task-ID":      taskID,
	}
	// Agent owner is memberUserID who cannot see privateProjectID -> 404.
	goAgentPriv := callAs(testHandler, testHandler.GetIssue, privateIssue.ID, testUserID, "owner", agentHeaders)
	rustAgentPriv := callAs(&rustHandler, rustHandler.GetIssueRoute, privateIssue.ID, testUserID, "owner", agentHeaders)
	if goAgentPriv.Code != http.StatusNotFound || rustAgentPriv.Code != http.StatusNotFound {
		t.Fatalf("agent private read codes: go=%d rust=%d, want 404", goAgentPriv.Code, rustAgentPriv.Code)
	}
	// Agent CAN see public/workspace richIssue.
	goAgentPublic := callAs(testHandler, testHandler.GetIssue, richIssue.ID, testUserID, "owner", agentHeaders)
	rustAgentPublic := callAs(&rustHandler, rustHandler.GetIssueRoute, richIssue.ID, testUserID, "owner", agentHeaders)
	if goAgentPublic.Code != http.StatusOK || rustAgentPublic.Code != http.StatusOK {
		t.Fatalf("agent public read codes: go=%d rust=%d, want 200", goAgentPublic.Code, rustAgentPublic.Code)
	}
	if !reflect.DeepEqual(decodeMap(t, goAgentPublic.Body.Bytes()), decodeMap(t, rustAgentPublic.Body.Bytes())) {
		t.Fatalf("agent public issue mismatch:\nGo:   %s\nRust: %s", goAgentPublic.Body.String(), rustAgentPublic.Body.String())
	}

	// 5. Missing issue UUID and invalid identifier return identical 404.
	for _, missing := range []string{uuid.NewString(), "NOT-A-VALID-ID", "WRONGPREFIX-999999"} {
		goMiss := callAs(testHandler, testHandler.GetIssue, missing, testUserID, "owner", nil)
		rustMiss := callAs(&rustHandler, rustHandler.GetIssueRoute, missing, testUserID, "owner", nil)
		if goMiss.Code != http.StatusNotFound || rustMiss.Code != http.StatusNotFound {
			t.Fatalf("missing(%q) codes: go=%d rust=%d, want 404", missing, goMiss.Code, rustMiss.Code)
		}
		if !reflect.DeepEqual(decodeMap(t, goMiss.Body.Bytes()), decodeMap(t, rustMiss.Body.Bytes())) {
			t.Fatalf("missing(%q) body mismatch:\nGo:   %s\nRust: %s", missing, goMiss.Body.String(), rustMiss.Body.String())
		}
	}

	// Verify non-member rejection in Rust directly.
	nonMemberID := uuid.NewString()
	rustNonMember := callAs(&rustHandler, rustHandler.GetIssueRoute, richIssue.ID, nonMemberID, "member", nil)
	if rustNonMember.Code != http.StatusNotFound {
		t.Fatalf("non-member direct Rust check status = %d, want 404; body=%s", rustNonMember.Code, rustNonMember.Body.String())
	}
}
