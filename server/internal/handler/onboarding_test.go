package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
)

// newWaitlistTestUser inserts a fresh user row, returns its id, and
// registers a cleanup. Uses the test pool directly so we don't depend
// on the handler-under-test for fixture setup.
func newWaitlistTestUser(t *testing.T, email string) string {
	t.Helper()
	ctx := context.Background()

	var userID string
	if err := testPool.QueryRow(ctx,
		`INSERT INTO "user" (name, email) VALUES ($1, $2) RETURNING id`,
		"Waitlist Test", email,
	).Scan(&userID); err != nil {
		t.Fatalf("insert test user: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(ctx, `DELETE FROM "user" WHERE id = $1`, userID)
	})
	return userID
}

// counterValue sums a named Prometheus counter across all label
// combinations on the given BusinessMetrics. Events are metrics-only
// since MUL-4127, so PatchOnboarding's emissions are observable ONLY
// through these counters — there is no PostHog capture to record.
func counterValue(t *testing.T, m *obsmetrics.BusinessMetrics, name string) float64 {
	t.Helper()
	reg := prometheus.NewRegistry()
	for _, c := range m.Collectors() {
		if err := reg.Register(c); err != nil {
			t.Fatalf("register collector: %v", err)
		}
	}
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	for _, mf := range families {
		if mf.GetName() != name {
			continue
		}
		var sum float64
		for _, mm := range mf.GetMetric() {
			sum += mm.GetCounter().GetValue()
		}
		return sum
	}
	return 0
}

func patchOnboardingAs(t *testing.T, h *Handler, userID, questionnaire string) {
	t.Helper()
	body := `{"questionnaire": ` + questionnaire + `}`
	req := httptest.NewRequest("PATCH", "/api/me/onboarding", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", userID)
	w := httptest.NewRecorder()
	h.PatchOnboarding(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("patch onboarding: expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

// The in-flow questionnaire is role + use_case only (MUL-5159): its
// funnel counter must move without source, and source's own counter
// must move exactly once when source resolves later via the workspace
// backfill prompt.
func TestPatchOnboardingSplitsQuestionnaireAndSourceEvents(t *testing.T) {
	userID := newWaitlistTestUser(t, "onboarding-event-split@multica.ai")

	m := obsmetrics.NewBusinessMetrics()
	h := *testHandler
	h.Metrics = m

	const questionnaireCounter = "multica_onboarding_questionnaire_submitted_total"
	const sourceCounter = "multica_onboarding_source_submitted_total"

	const inFlow = `{"source":[],"source_other":null,"source_skipped":false,` +
		`"role":"engineer","role_other":null,"role_skipped":false,` +
		`"use_case":["ship_code"],"use_case_other":null,"use_case_skipped":false,"version":2}`

	// Role + use_case resolved, source untouched → questionnaire counter
	// moves now; source counter must NOT.
	patchOnboardingAs(t, &h, userID, inFlow)
	if got := counterValue(t, m, questionnaireCounter); got != 1 {
		t.Fatalf("expected questionnaire counter 1 after role+use_case resolve, got %v", got)
	}
	if got := counterValue(t, m, sourceCounter); got != 0 {
		t.Fatalf("expected source counter 0 while source unresolved, got %v", got)
	}

	// Idempotency: replaying the same answers re-emits nothing.
	patchOnboardingAs(t, &h, userID, inFlow)
	if got := counterValue(t, m, questionnaireCounter); got != 1 {
		t.Fatalf("expected questionnaire counter to stay at 1 after replay, got %v", got)
	}

	// Source lands later (the backfill prompt merges it into the row).
	const withSource = `{"source":["search"],"source_other":null,"source_skipped":false,` +
		`"role":"engineer","role_other":null,"role_skipped":false,` +
		`"use_case":["ship_code"],"use_case_other":null,"use_case_skipped":false,"version":2}`
	patchOnboardingAs(t, &h, userID, withSource)
	if got := counterValue(t, m, sourceCounter); got != 1 {
		t.Fatalf("expected source counter 1 after source resolves, got %v", got)
	}
	if got := counterValue(t, m, questionnaireCounter); got != 1 {
		t.Fatalf("expected questionnaire counter to stay at 1 after source lands, got %v", got)
	}

	// Replaying the source answer re-emits nothing.
	patchOnboardingAs(t, &h, userID, withSource)
	if got := counterValue(t, m, sourceCounter); got != 1 {
		t.Fatalf("expected source counter to stay at 1 after replay, got %v", got)
	}
}

// Declining every question at once (the About-you Skip writes both
// role and use_case skip markers; a backfill Skip writes source's) is
// a resolution too: both counters move exactly once.
func TestPatchOnboardingAllSkippedResolvesBothCounters(t *testing.T) {
	userID := newWaitlistTestUser(t, "onboarding-source-skip@multica.ai")

	m := obsmetrics.NewBusinessMetrics()
	h := *testHandler
	h.Metrics = m

	const skipped = `{"source":[],"source_other":null,"source_skipped":true,` +
		`"role":null,"role_other":null,"role_skipped":true,` +
		`"use_case":[],"use_case_other":null,"use_case_skipped":true,"version":2}`
	patchOnboardingAs(t, &h, userID, skipped)

	if got := counterValue(t, m, "multica_onboarding_questionnaire_submitted_total"); got != 1 {
		t.Fatalf("expected questionnaire counter 1 for all-skip, got %v", got)
	}
	if got := counterValue(t, m, "multica_onboarding_source_submitted_total"); got != 1 {
		t.Fatalf("expected source counter 1 for an explicit decline, got %v", got)
	}
}
