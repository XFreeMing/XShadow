package handler

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
)

func TestContinuousGoalReviewSeparatesExecutionIssuesAndPlanningGaps(t *testing.T) {
	ctx := context.Background()
	seedTestCatalog(t)
	runtime := dbfx.Runtime(t, "Continuous review runtime")
	reviewer := dbfx.Agent(t, "Independent goal reviewer", runtime)
	project := deliveryProject(t, "Reviewed goal project", reviewer)
	goal := createGoalViaAPI(t, testUserID, map[string]any{
		"title": "Recover the same book and read its original excerpt", "status": "in_progress",
		"project_id": project, "acceptance_criteria": "Follow the same job through to its fixed original.",
	})
	goalID := parseUUID(goal.ID)
	workspace := parseUUID(testWorkspaceID)
	t.Cleanup(func() {
		dbfx.Exec(t, "DELETE FROM goal_plan_generation WHERE goal_id=$1", goalID)
		dbfx.Exec(t, "DELETE FROM goal_plan WHERE goal_id=$1", goalID)
	})
	item := func(title string) GoalPlanItem {
		return GoalPlanItem{
			ID: uuidToString(dbid.NewV7()), Title: title, Outcome: title, AcceptanceCriteria: "Replay the user's flow",
			Binding:      GoalPlanItemBinding{Type: "new"},
			EvidenceRefs: []GoalPlanEvidenceRef{{Kind: "contract", Ref: goal.ID}},
			Delivery: &GoalPlanDelivery{Type: "feature", MethodID: "agile", Criteria: []GoalPlanCriterion{
				{ID: "C1", Expected: "The same book", Verification: "Replay the path from the signed receipt"},
			}},
		}
	}
	first, next, memory := item("Recover the job"), item("Open an excerpt"), item("Browse personal memory")
	held := item("Compare a saved memory with the original")
	stageOne, stageTwo := 1, 2
	first.Stage, next.Stage, memory.Stage, held.Stage = &stageOne, &stageOne, &stageTwo, &stageTwo
	t.Cleanup(func() {
		for _, node := range []GoalPlanItem{next, memory, held} {
			dbfx.Exec(t, "DELETE FROM issue WHERE id=$1", service.GoalPreparationIssueID(goalID, node.ID))
		}
	})
	var receipt IssueResponse
	testutil.Call(t, testHandler.CreateIssue, newRequest("POST", "/api/issues?workspace_id="+testWorkspaceID,
		map[string]any{"title": "Book receipt", "status": "done", "project_id": project})).Want(201).JSON(&receipt)
	issue := receipt.ID
	t.Cleanup(func() { dbfx.Exec(t, "DELETE FROM issue WHERE id=$1", issue) })
	first.Binding = GoalPlanItemBinding{Type: "existing", IssueID: &issue}
	path := "/api/goals/" + goal.ID
	params := map[string]string{"id": goal.ID}
	testutil.Call(t, testHandler.LinkGoalIssue, goalRequest("PUT", path+"/issues/"+issue, testUserID, nil,
		map[string]string{"id": goal.ID, "issueId": issue})).Want(204)
	var saved GoalPlanResponse
	testutil.Call(t, testHandler.PutGoalPlan, goalRequest("PUT", path+"/plan", testUserID, map[string]any{
		"expected_revision": 0, "items": []GoalPlanItem{first, next, memory, held},
		"edges": []GoalPlanEdge{
			{DependentItemID: next.ID, PrerequisiteItemID: first.ID, Reason: "Reuse the accepted job ID"},
			{DependentItemID: held.ID, PrerequisiteItemID: memory.ID, Reason: "Requires a saved memory"},
		},
	}, params)).Want(200).JSON(&saved)
	if len(saved.Progress.ReadyToPrepare) != 2 || saved.Progress.ReadyToPrepare[0].ItemID != next.ID || saved.Progress.ReadyToPrepare[1].ItemID != memory.ID {
		t.Fatalf("expected both independent preparation lines: %+v", saved.Progress)
	}
	start := func() GoalPlanGenerationResponse {
		t.Helper()
		var run GoalPlanGenerationResponse
		testutil.Call(t, testHandler.StartGoalPlanReview, goalRequest("POST", path+"/plan/review", testUserID,
			map[string]any{"expected_revision": 1, "agent_id": reviewer}, params)).Want(200).JSON(&run)
		return run
	}
	preflight := start()
	if again := start(); again.ID != preflight.ID {
		t.Fatal("an identical review enrolled more than one run")
	}
	release := func(status int) {
		t.Helper()
		testutil.Call(t, testHandler.PutGoalExecution, goalRequest("PUT", path+"/plan/execution", testUserID,
			map[string]any{"enabled": true, "expected_revision": 1}, params)).Want(status)
	}
	revoke := func() {
		t.Helper()
		testutil.Call(t, testHandler.PutGoalExecution, goalRequest("PUT", path+"/plan/execution", testUserID,
			map[string]any{"enabled": false, "expected_revision": 1}, params)).Want(200)
	}
	release(409)
	if err := testHandler.TickGoalPlanReviews(ctx); err != nil {
		t.Fatal(err)
	}
	if _, after := getPlan(t, goal.ID); after.Items[1].Binding.IssueID != nil {
		t.Fatal("preparation started before the review finished")
	}
	submit := func(run, task, decision, result string, items []GoalPlanItem, want int) GoalPlanGenerationResponse {
		t.Helper()
		body := map[string]any{
			"review": GoalReviewAssessment{Decision: decision, Summary: "Independent review of the approved outcome",
				Checks: []GoalReviewCheck{{Outcome: "Follow the same job", Result: result, ItemIDs: []string{first.ID, next.ID}, Reason: "Compare the receipt and fixed original"}}},
			"items": items, "edges": []GoalPlanEdge{}, "context_snapshot": map[string]any{"plan_revision": 1},
		}
		code, response := submitPlanResultAsRun(t, mintGoalPlanTaskToken(t, testWorkspaceID, task, reviewer), goal.ID, run, body)
		if code != want {
			t.Fatalf("review %s returned %d, want %d", decision, code, want)
		}
		return response
	}
	submit(preflight.ID, *preflight.TaskID, "covered", "sufficient", nil, 200)
	release(200)

	tick := func() db.GoalPlanGeneration {
		t.Helper()
		if err := testHandler.TickGoalPlanReviews(ctx); err != nil {
			t.Fatal(err)
		}
		run, err := testHandler.Queries.GetLatestGoalPlanReview(ctx, db.GetLatestGoalPlanReviewParams{WorkspaceID: workspace, GoalID: goalID})
		if err != nil {
			t.Fatal(err)
		}
		return run
	}
	ticks := make(chan error, 2)
	for range 2 {
		go func() { ticks <- testHandler.TickGoalPlanReviews(ctx) }()
	}
	for range 2 {
		if err := <-ticks; err != nil {
			t.Fatal(err)
		}
	}
	if uuidToString(tick().ID) != preflight.ID {
		t.Fatal("card creation should preserve the completed review")
	}
	_, prepared := getPlan(t, goal.ID)
	if prepared.Revision != 1 || len(prepared.Progress.ReadyToPrepare) != 2 || prepared.Items[3].Binding.IssueID != nil {
		t.Fatalf("unexpected card readiness: %+v", prepared)
	}
	cards, err := testHandler.Queries.ListGoalIssues(ctx, db.ListGoalIssuesParams{WorkspaceID: workspace, GoalID: goalID, Limit: 20})
	if err != nil || len(cards) != 3 {
		t.Fatalf("expected exactly two new linked cards: %v %v", cards, err)
	}
	for _, index := range []int{1, 2} {
		node := prepared.Items[index]
		id := service.GoalPreparationIssueID(goalID, node.ID)
		card, err := testHandler.Queries.GetIssue(ctx, id)
		if err != nil || node.Binding.IssueID == nil || *node.Binding.IssueID != uuidToString(id) ||
			card.Status != "backlog" || card.AssigneeID.Valid || card.FirstExecutedAt.Valid {
			t.Fatalf("expected an unassigned backlog card for %s: %v %v", node.ID, card, err)
		}
		contract, err := testHandler.issuePlanDelivery(ctx, card, goal.ID)
		if err != nil || contract == nil || contract.Delivery.MethodID != "agile" || len(contract.Delivery.Criteria) != 1 {
			t.Fatalf("the card lost its planned delivery contract: %+v %v", contract, err)
		}
	}
	dbfx.Exec(t, "UPDATE issue SET status='blocked', revision=revision+1 WHERE id=$1", *prepared.Items[1].Binding.IssueID)
	blocked := tick()
	if uuidToString(blocked.ID) == preflight.ID || tick().ID != blocked.ID {
		t.Fatal("blocked issue must trigger exactly one new review")
	}
	submit(uuidToString(blocked.ID), uuidToString(blocked.TaskID), "execution_issue", "unverified", []GoalPlanItem{item("Unrelated rewrite")}, 400)
	submit(uuidToString(blocked.ID), uuidToString(blocked.TaskID), "execution_issue", "unverified", nil, 200)

	revoke()
	dbfx.Exec(t, "UPDATE issue SET status='in_progress', revision=revision+1 WHERE id=$1", *prepared.Items[1].Binding.IssueID)
	dbfx.Exec(t, "UPDATE issue SET revision=revision+1 WHERE id=$1", issue)
	resumed := tick()
	submit(uuidToString(resumed.ID), uuidToString(resumed.TaskID), "insufficient_evidence", "unverified", nil, 200)
	release(409)
	resumePreflight := start()
	if resumePreflight.ID == uuidToString(resumed.ID) || start().ID != resumePreflight.ID {
		t.Fatal("reauthorization needs exactly one distinct preflight review of the same facts")
	}
	if _, plan := getPlan(t, goal.ID); plan.Review.Phase != "preflight" {
		t.Fatalf("reauthorization reviewed delivered results instead of plan coverage: %+v", plan.Review)
	}
	submit(resumePreflight.ID, *resumePreflight.TaskID, "execution_issue", "sufficient", nil, 200)
	testutil.Call(t, testHandler.PutGoalExecution, goalRequest("PUT", path+"/plan/execution", testUserID,
		map[string]any{"enabled": true, "expected_revision": 1, "stage": 3}, params)).Want(400)
	var authorized GoalPlanResponse
	testutil.Call(t, testHandler.PutGoalExecution, goalRequest("PUT", path+"/plan/execution", testUserID,
		map[string]any{"enabled": true, "expected_revision": 1, "stage": 1}, params)).Want(200).JSON(&authorized)
	if authorized.ExecutionState != "active" || authorized.ExecutionStage != 1 || len(authorized.Progress.ReadyToPrepare) != 0 {
		t.Fatalf("stage-specific reauthorization left later work eligible: %+v", authorized)
	}
	revoke()

	dbfx.Exec(t, "UPDATE issue SET revision=revision+1 WHERE id=$1", issue)
	adjustment := tick()
	updated := submit(uuidToString(adjustment.ID), uuidToString(adjustment.TaskID), "planning_gap", "gap", []GoalPlanItem{item("Replay the integrated journey")}, 200)
	var candidate GoalPlanCandidate
	if err := json.Unmarshal(updated.Candidate, &candidate); err != nil || len(candidate.Items) != 5 ||
		candidate.Items[0].ID != first.ID || candidate.Items[0].Binding.IssueID == nil ||
		*candidate.Items[0].Binding.IssueID != issue || candidate.Items[4].Title != "Replay the integrated journey" {
		t.Fatalf("not a scoped candidate: %+v %v", candidate, err)
	}
	if _, after := getPlan(t, goal.ID); after.Revision != 1 || len(after.Items) != 4 || after.Review.Assessment.Decision != "planning_gap" {
		t.Fatalf("candidate unexpectedly changed the authorized plan: %+v", after)
	}
	release(409)
	var presented GoalPlanGenerationResponse
	testutil.Call(t, testHandler.GetGoalPlanGeneration, goalRequest("GET", path+"/plan/generations/"+updated.ID, testUserID, nil,
		map[string]string{"id": goal.ID, "generationId": updated.ID})).Want(200).JSON(&presented)
	if err := json.Unmarshal(presented.Candidate, &candidate); err != nil || candidate.Items[1].Binding.IssueID == nil ||
		*candidate.Items[1].Binding.IssueID != *prepared.Items[1].Binding.IssueID {
		t.Fatalf("candidate view lost the automatically linked card: %+v %v", candidate, err)
	}
	testutil.Call(t, testHandler.PutGoalPlan, goalRequest("PUT", path+"/plan", testUserID, map[string]any{
		"expected_revision": 1, "generation_id": uuidToString(adjustment.ID), "items": candidate.Items,
		"edges": candidate.Edges, "context_snapshot": candidate.ContextSnapshot,
	}, params)).Want(200)
	recheck := tick()
	if recheck.ID == adjustment.ID || recheck.BaseRevision != 2 {
		t.Fatalf("an applied adjustment must trigger a fresh review: %+v", recheck)
	}
}
