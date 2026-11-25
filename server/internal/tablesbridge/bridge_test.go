package tablesbridge

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	db "github.com/multica-ai/multica/server/pkg/db/generated"

	"github.com/multica-ai/multica/server/internal/middleware"
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
