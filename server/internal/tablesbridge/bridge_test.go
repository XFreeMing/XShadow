package tablesbridge

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	db "github.com/multica-ai/multica/server/pkg/db/generated"

	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/util"
)

func TestBridgeUsesAuthenticatedWorkspaceInsteadOfClientHeader(t *testing.T) {
	const authenticated = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	var gotWorkspace, gotClientWorkspace, gotToken, gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotWorkspace = r.Header.Get("X-XShadow-Workspace-Id")
		gotClientWorkspace = r.Header.Get("X-Workspace-ID")
		gotToken = r.Header.Get("X-XShadow-Internal-Token")
		gotPath = r.URL.Path
		if r.Header.Get("X-XShadow-Internal-Token") == "client-token" {
			t.Errorf("client token was forwarded")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"tables":[]}`)
	}))
	t.Cleanup(upstream.Close)

	bridge := &Bridge{Upstream: upstream.URL, Token: "internal-token", Client: upstream.Client()}
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := middleware.SetMemberContext(r.Context(), authenticated, db.Member{})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	})
	router.Route("/api/tables", func(r chi.Router) {
		r.HandleFunc("/", bridge.ServeHTTP)
		r.HandleFunc("/*", bridge.ServeHTTP)
	})

	request := httptest.NewRequest(http.MethodGet, "/api/tables/11111111-1111-4111-8111-111111111111?view=22222222-2222-4222-8222-222222222222", nil)
	request.Header.Set("X-Workspace-ID", "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
	request.Header.Set("X-XShadow-Internal-Token", "client-token")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d body %s", recorder.Code, recorder.Body.String())
	}
	if gotWorkspace != authenticated {
		t.Fatalf("upstream workspace = %q", gotWorkspace)
	}
	if gotClientWorkspace != "" {
		t.Fatalf("client workspace header reached upstream: %q", gotClientWorkspace)
	}
	if gotToken != "internal-token" {
		t.Fatalf("token = %q", gotToken)
	}
	if gotPath != "/internal/tables/11111111-1111-4111-8111-111111111111" {
		t.Fatalf("path = %q", gotPath)
	}
}

func TestCollectionRootOmitsTrailingSlash(t *testing.T) {
	const authenticated = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	var paths []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.RequestURI())
		if r.URL.Path != "/internal/tables" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"tables":[]}`)
	}))
	t.Cleanup(upstream.Close)

	bridge := &Bridge{Upstream: upstream.URL, Token: "internal-token", Client: upstream.Client()}
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := middleware.SetMemberContext(r.Context(), authenticated, db.Member{})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	})
	router.Route("/api/tables", func(r chi.Router) {
		r.HandleFunc("/", bridge.ServeHTTP)
		r.HandleFunc("/*", bridge.ServeHTTP)
	})

	for _, path := range []string{"/api/tables", "/api/tables/"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s status %d body %s upstream %#v", path, recorder.Code, recorder.Body.String(), paths)
		}
	}
	if len(paths) != 2 || paths[0] != "/internal/tables" || paths[1] != "/internal/tables" {
		t.Fatalf("upstream paths = %#v", paths)
	}
}

func TestTrustedActorIgnoresForgedIdentity(t *testing.T) {
	const (
		workspace = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
		userID    = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
		agentID   = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
		taskID    = "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
	)
	parsedUser, err := util.ParseUUID(userID)
	if err != nil {
		t.Fatal(err)
	}
	var gotSource, gotID, gotTask, gotLabel, gotForged string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSource = r.Header.Get("X-XShadow-Actor-Source")
		gotID = r.Header.Get("X-XShadow-Actor-Id")
		gotTask = r.Header.Get("X-XShadow-Task-Id")
		gotLabel = r.Header.Get("X-XShadow-Actor-Label")
		gotForged = r.Header.Get("X-Actor-Source")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"record":{}}`)
	}))
	t.Cleanup(upstream.Close)
	bridge := &Bridge{
		Upstream: upstream.URL,
		Token:    "internal-token",
		Client:   upstream.Client(),
		AgentLabel: func(_ context.Context, gotWorkspace, gotAgent string) string {
			if gotWorkspace != workspace || gotAgent != agentID {
				t.Fatalf("label lookup %s %s", gotWorkspace, gotAgent)
			}
			return "Review Agent A"
		},
	}
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := middleware.SetMemberContext(r.Context(), workspace, db.Member{UserID: parsedUser})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	})
	router.HandleFunc("/api/tables/*", bridge.ServeHTTP)

	agentReq := httptest.NewRequest(http.MethodPatch, "/api/tables/11111111-1111-4111-8111-111111111111/records/22222222-2222-4222-8222-222222222222", strings.NewReader(`{"actor_id":"forged","expected_revision":1,"values":{}}`))
	agentReq.Header.Set("X-Actor-Source", "task_token")
	agentReq.Header.Set("X-Agent-ID", agentID)
	agentReq.Header.Set("X-Task-ID", taskID)
	agentReq.Header.Set("X-XShadow-Actor-Id", "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
	agentReq.Header.Set("X-XShadow-Actor-Source", "member")
	agentRec := httptest.NewRecorder()
	router.ServeHTTP(agentRec, agentReq)
	if agentRec.Code != http.StatusOK || gotSource != "agent" || gotID != agentID || gotTask != taskID || gotLabel != "Review Agent A" || gotForged != "" {
		t.Fatalf("agent forward status %d source %q id %q task %q label %q leaked %q", agentRec.Code, gotSource, gotID, gotTask, gotLabel, gotForged)
	}

	memberReq := httptest.NewRequest(http.MethodPatch, "/api/tables/11111111-1111-4111-8111-111111111111/records/22222222-2222-4222-8222-222222222222", strings.NewReader(`{"actor_id":"forged"}`))
	memberReq.Header.Set("X-Actor-Source", "member")
	memberReq.Header.Set("X-Agent-ID", agentID)
	memberReq.Header.Set("X-Task-ID", taskID)
	memberRec := httptest.NewRecorder()
	router.ServeHTTP(memberRec, memberReq)
	if memberRec.Code != http.StatusOK || gotSource != "member" || gotID != userID || gotTask != "" || gotLabel != "" {
		t.Fatalf("member forward status %d source %q id %q task %q label %q", memberRec.Code, gotSource, gotID, gotTask, gotLabel)
	}
}

func TestDisabledBridgeDoesNotCallUpstream(t *testing.T) {
	bridge := &Bridge{}
	request := httptest.NewRequest(http.MethodGet, "/api/tables", nil)
	recorder := httptest.NewRecorder()
	bridge.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d", recorder.Code)
	}
}

func TestFromEnvRejectsNonLoopbackUpstream(t *testing.T) {
	t.Setenv("XSHADOW_TABLES_UPSTREAM", "http://example.com")
	t.Setenv("XSHADOW_TABLES_INTERNAL_TOKEN", "secret")
	bridge := FromEnv(slog.Default())
	if bridge.Enabled() {
		t.Fatal("public upstream was enabled")
	}
}
