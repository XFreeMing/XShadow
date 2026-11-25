package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestLiveRustIssueCreatePilotMemberShapes(t *testing.T) {
	token := "live-create-token-" + uuid.NewString()[:8]
	rustUpstream, rustHits := startRustCandidateForTest(t, token)

	ctx := context.Background()
	suffix := uuid.NewString()[:8]
	title := "SH-PM-SYNTH-01 Rust create pilot " + suffix

	var projectID, parentID, firstID string
	defer func() {
		for _, id := range []string{firstID} {
			if id != "" {
				_, _ = testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1`, id)
			}
		}
		if parentID != "" {
			_, _ = testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1`, parentID)
		}
		if projectID != "" {
			_, _ = testPool.Exec(ctx, `DELETE FROM project WHERE id = $1`, projectID)
		}
	}()

	err := testPool.QueryRow(ctx, `
		INSERT INTO project (workspace_id, title) VALUES ($1, $2) RETURNING id
	`, testWorkspaceID, "Rust create pilot "+suffix).Scan(&projectID)
	if err != nil {
		t.Fatalf("insert project: %v", err)
	}

	rustHandler := *testHandler
	rustHandler.IssueReadBridge = &IssueReadBridge{
		Upstream:          rustUpstream,
		Token:             token,
		CreatePilotScopes: []string{strings.ToLower(testWorkspaceID)},
	}

	parentReq := newRequest(http.MethodPost, "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title": "Rust create parent " + suffix,
	})
	parentW := testutil.Call(t, rustHandler.CreateIssue, parentReq).Want(http.StatusCreated)
	var parent IssueResponse
	parentW.JSON(&parent)
	parentID = parent.ID

	createReq := newRequest(http.MethodPost, "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title":           title,
		"status":          issuestatus.InProgress,
		"parent_issue_id": parentID,
		"project_id":      projectID,
	})
	beforeHits := rustHits.Load()
	w := testutil.Call(t, rustHandler.CreateIssue, createReq).Want(http.StatusCreated)
	if rustHits.Load() != beforeHits+1 {
		t.Fatalf("expected one Rust upstream create, got %d hits", rustHits.Load()-beforeHits)
	}
	var created IssueResponse
	w.JSON(&created)
	firstID = created.ID
	if created.Status != issuestatus.InProgress || created.Identifier == "" || created.Revision < 1 {
		t.Fatalf("unexpected created issue: %#v", created)
	}
	if created.Labels == nil {
		t.Fatal("expected non-nil labels field on create response")
	}

	dupBody := map[string]any{
		"title":           "  " + strings.ReplaceAll(strings.ToUpper(title), " ", "   ") + "  ",
		"parent_issue_id": parentID,
		"project_id":      projectID,
	}
	dupReq := newRequest(http.MethodPost, "/api/issues?workspace_id="+testWorkspaceID, dupBody)
	dupW := testutil.Call(t, rustHandler.CreateIssue, dupReq).Want(http.StatusConflict)
	var conflict struct {
		Code  string        `json:"code"`
		Issue IssueResponse `json:"issue"`
	}
	dupW.JSON(&conflict)
	if conflict.Code != "active_duplicate_issue" || conflict.Issue.ID != firstID {
		t.Fatalf("duplicate conflict = %#v, want active_duplicate_issue for %s", conflict, firstID)
	}

	var afterCount int
	if err := testPool.QueryRow(ctx,
		`SELECT COUNT(*) FROM issue WHERE workspace_id = $1 AND lower(btrim(regexp_replace(title, '[[:space:]]+', ' ', 'g'))) = $2`,
		testWorkspaceID, strings.ToLower(strings.Join(strings.Fields(title), " ")),
	).Scan(&afterCount); err != nil {
		t.Fatalf("count duplicates: %v", err)
	}
	if afterCount != 1 {
		t.Fatalf("active duplicate count = %d, want 1", afterCount)
	}

	goHandler := *testHandler
	goHandler.IssueReadBridge = &IssueReadBridge{CreatePilotScopes: []string{"other-ws"}}
	w = testutil.Call(t, goHandler.CreateIssue,
		newRequest(http.MethodPost, "/api/issues?workspace_id="+testWorkspaceID, dupBody)).Want(http.StatusConflict)
	w.JSON(&conflict)
	if conflict.Issue.ID != firstID {
		t.Fatalf("Go path duplicate id = %s, want %s", conflict.Issue.ID, firstID)
	}
}

func TestLiveRustIssueCreateConcurrentSameTitleSingleRow(t *testing.T) {
	token := "live-create-race-" + uuid.NewString()[:8]
	rustUpstream, _ := startRustCandidateForTest(t, token)

	ctx := context.Background()
	title := "Rust concurrent create " + uuid.NewString()[:8]
	var winnerID string
	defer func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM issue WHERE workspace_id = $1 AND title = $2`, testWorkspaceID, title)
	}()

	rustHandler := *testHandler
	rustHandler.IssueReadBridge = &IssueReadBridge{
		Upstream:          rustUpstream,
		Token:             token,
		CreatePilotScopes: []string{strings.ToLower(testWorkspaceID)},
	}

	const workers = 8
	var wg sync.WaitGroup
	wg.Add(workers)
	statuses := make([]int, workers)
	var mu sync.Mutex
	for i := 0; i < workers; i++ {
		go func(idx int) {
			defer wg.Done()
			req := newRequest(http.MethodPost, "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
				"title":  title,
				"status": issuestatus.Todo,
			})
			w := httptest.NewRecorder()
			rustHandler.CreateIssue(w, req)
			statuses[idx] = w.Code
			if w.Code == http.StatusCreated {
				var out IssueResponse
				_ = json.Unmarshal(w.Body.Bytes(), &out)
				mu.Lock()
				winnerID = out.ID
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()

	created := 0
	conflicts := 0
	for _, code := range statuses {
		switch code {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
			conflicts++
		default:
			t.Fatalf("unexpected status %d in %#v", code, statuses)
		}
	}
	if created != 1 {
		t.Fatalf("created count = %d, want 1; statuses=%v", created, statuses)
	}
	if conflicts != workers-1 {
		t.Fatalf("conflict count = %d, want %d", conflicts, workers-1)
	}

	var count int
	normalized := strings.ToLower(strings.Join(strings.Fields(title), " "))
	if err := testPool.QueryRow(ctx,
		`SELECT COUNT(*) FROM issue WHERE workspace_id = $1 AND lower(btrim(regexp_replace(title, '[[:space:]]+', ' ', 'g'))) = $2`,
		testWorkspaceID, normalized,
	).Scan(&count); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("row count = %d, want 1", count)
	}
	if winnerID == "" {
		t.Fatal("missing winner issue id")
	}
}

func TestLiveRustIssueCreateMalformedBodyNoRow(t *testing.T) {
	token := "live-create-bad-" + uuid.NewString()[:8]
	rustUpstream, _ := startRustCandidateForTest(t, token)

	ctx := context.Background()
	before := time.Now()
	var countBefore int
	_ = testPool.QueryRow(ctx, `SELECT COUNT(*) FROM issue WHERE workspace_id = $1 AND created_at >= $2`,
		testWorkspaceID, before.Add(-time.Minute)).Scan(&countBefore)

	rustHandler := *testHandler
	rustHandler.IssueReadBridge = &IssueReadBridge{
		Upstream:          rustUpstream,
		Token:             token,
		CreatePilotScopes: []string{strings.ToLower(testWorkspaceID)},
	}

	req := httptest.NewRequest(http.MethodPost, "/api/issues?workspace_id="+testWorkspaceID, strings.NewReader(`{"title":""}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", testUserID)
	req.Header.Set("X-Workspace-ID", testWorkspaceID)
	w := httptest.NewRecorder()
	rustHandler.CreateIssue(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}

	var countAfter int
	if err := testPool.QueryRow(ctx, `SELECT COUNT(*) FROM issue WHERE workspace_id = $1 AND created_at >= $2`,
		testWorkspaceID, before.Add(-time.Minute)).Scan(&countAfter); err != nil {
		t.Fatalf("count after: %v", err)
	}
	if countAfter != countBefore {
		t.Fatalf("issue rows changed from %d to %d on malformed create", countBefore, countAfter)
	}
}

func TestLiveRustIssueCreateTypedPropertiesDefaults(t *testing.T) {
	token := "live-create-props-" + uuid.NewString()[:8]
	rustUpstream, _ := startRustCandidateForTest(t, token)
	suffix := uuid.NewString()[:8]

	number := createTestProperty(t, map[string]any{"name": "RustCreateNum" + suffix, "type": "number"})
	checkbox := createTestProperty(t, map[string]any{"name": "RustCreateCheck" + suffix, "type": "checkbox"})

	rustHandler := *testHandler
	rustHandler.IssueReadBridge = &IssueReadBridge{
		Upstream:          rustUpstream,
		Token:             token,
		CreatePilotScopes: []string{strings.ToLower(testWorkspaceID)},
	}

	req := newRequest(http.MethodPost, "/api/issues?workspace_id="+testWorkspaceID, map[string]any{
		"title": "typed defaults " + suffix,
		"properties": map[string]any{
			number.ID:   0,
			checkbox.ID: false,
		},
	})
	w := testutil.Call(t, rustHandler.CreateIssue, req).Want(http.StatusCreated)
	var created IssueResponse
	w.JSON(&created)
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE id = $1`, created.ID)
	})
	if fmt.Sprint(created.Properties[number.ID]) != "0" {
		t.Fatalf("number default = %#v, want 0", created.Properties[number.ID])
	}
	if created.Properties[checkbox.ID] != false {
		t.Fatalf("checkbox default = %#v, want false", created.Properties[checkbox.ID])
	}
}
