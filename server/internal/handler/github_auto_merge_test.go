package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestGitHubDeliveryVerifiesMergedHeadAndBase(t *testing.T) {
	head := strings.Repeat("a", 40)
	for _, test := range []struct {
		name   string
		head   string
		base   string
		merged bool
		valid  bool
	}{
		{name: "approved merge", head: head, base: "dev", merged: true, valid: true},
		{name: "open PR", head: head, base: "dev"},
		{name: "retargeted PR", head: head, base: "main", merged: true},
		{name: "unapproved head", head: strings.Repeat("b", 40), base: "dev", merged: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"head": map[string]string{"sha": test.head}, "base": map[string]string{"ref": test.base},
					"merged": test.merged, "merge_commit_sha": strings.Repeat("c", 40),
				})
			}))
			defer server.Close()
			err := verifyMergedGitHubPR(t.Context(), server.Client(), server.URL, "test", "/pulls/1", gitHubAutoMergeRequest{HeadSHA: head, BaseRef: "dev"})
			if (err == nil) != test.valid {
				t.Fatalf("merge verification returned %v, valid=%v", err, test.valid)
			}
		})
	}
}

func TestGitHubAutoMergeFixedHeadAndRetry(t *testing.T) {
	head := strings.Repeat("a", 40)
	request := gitHubAutoMergeRequest{HeadSHA: head, BaseRef: "dev"}
	var pending atomic.Bool
	var writes atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/app/pulls/3":
			payload := map[string]any{
				"head": map[string]string{"sha": head}, "base": map[string]string{"ref": "dev"},
				"node_id": "PR_node", "state": "open", "mergeable_state": "blocked",
			}
			if pending.Load() {
				payload["auto_merge"] = map[string]string{"merge_method": "squash"}
			}
			_ = json.NewEncoder(w).Encode(payload)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/app/rules/branches/dev":
			_, _ = w.Write([]byte(`[{"type":"required_status_checks","parameters":{"required_status_checks":[{"context":"XShadow Quality Review"}]}}]`))
		case r.Method == http.MethodPost && r.URL.Path == "/graphql":
			var body struct {
				Query     string
				Variables map[string]string
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Variables["head"] != head ||
				!strings.Contains(body.Query, "expectedHeadOid: $head") {
				t.Errorf("mutation lacks the reviewed head: %+v, %v", body, err)
			}
			pending.Store(true)
			writes.Add(1)
			w.WriteHeader(http.StatusBadGateway)
		default:
			t.Errorf("unexpected GitHub request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	for i := 0; i < 2; i++ {
		result, err := requestGitHubAutoMerge(t.Context(), srv.Client(), srv.URL, "fixture", "/repos/acme/app", 3, request)
		if err != nil || result.State != "waiting_for_github" || result.MergeSHA != "" {
			t.Fatalf("attempt %d: %+v %v", i, result, err)
		}
	}
	if writes.Load() != 1 {
		t.Fatalf("retried remote mutation despite GitHub confirmation: %d", writes.Load())
	}
	request.HeadSHA = strings.Repeat("b", 40)
	if _, err := requestGitHubAutoMerge(t.Context(), srv.Client(), srv.URL, "fixture", "/repos/acme/app", 3, request); !errors.Is(err, errGitHubMergeCandidateChanged) {
		t.Fatalf("mismatched head returned %v", err)
	}
}

func TestGitHubAutoMergeReadyReturnsConfirmedMerge(t *testing.T) {
	head := strings.Repeat("c", 40)
	merge := strings.Repeat("d", 40)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/app/pulls/3":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"head": map[string]string{"sha": head}, "base": map[string]string{"ref": "dev"},
				"node_id": "PR_node", "state": "open", "mergeable_state": "clean",
			})
		case r.Method == http.MethodPut && r.URL.Path == "/repos/acme/app/pulls/3/merge":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["sha"] != head || body["merge_method"] != "squash" {
				t.Errorf("unfenced merge request: %v %v", body, err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"merged": true, "sha": merge})
		default:
			t.Errorf("unexpected GitHub request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	result, err := requestGitHubAutoMerge(t.Context(), srv.Client(), srv.URL, "fixture", "/repos/acme/app", 3,
		gitHubAutoMergeRequest{HeadSHA: head, BaseRef: "dev"})
	if err != nil || result.State != "merged" || result.MergeSHA != merge {
		t.Fatalf("got %+v %v", result, err)
	}
}
