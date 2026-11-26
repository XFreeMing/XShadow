package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/goalproblem"
)

func TestGoalProblemSolvingChangesCharterOnlyThroughAnAuthorizedDecision(t *testing.T) {
	project := dbfx.Project(t, "Library")
	goal := createGoalViaAPI(t, testUserID, map[string]any{
		"title": "Find the right note", "intent": "Recover the right note quickly",
		"acceptance_criteria": "Find the note within three minutes", "project_id": project,
	})
	t.Cleanup(func() {
		dbfx.Exec(t, "DELETE FROM goal_plan WHERE goal_id=$1", goal.ID)
		dbfx.Exec(t, "DELETE FROM goal_problem_solving WHERE goal_id=$1", goal.ID)
	})
	reviewer := dbfx.User(t, "Independent reviewer", uuid.NewString()+"@example.test")
	dbfx.Member(t, testWorkspaceID, reviewer, "member")
	issue := dbfx.Issue(t, "Compare keyword results and original-note lookup", testutil.Cols{"project_id": project})
	planItemID := uuid.NewString()
	plan := map[string]any{"expected_revision": 0, "items": []GoalPlanItem{{
		ID: planItemID, Title: "Follow the original", Outcome: "Find the right note", AcceptanceCriteria: "Replay the current search",
		Binding: GoalPlanItemBinding{Type: "new"}, EvidenceRefs: []GoalPlanEvidenceRef{{Kind: "contract", Ref: goal.ID}},
	}}, "edges": []GoalPlanEdge{}}
	base := "/api/goals/" + goal.ID
	params := map[string]string{"id": goal.ID}
	testutil.Call(t, testHandler.PutGoalPlan, goalRequest("PUT", base+"/plan", testUserID, plan, params)).Want(200)

	read := func() goalProblemResponse {
		t.Helper()
		var response goalProblemResponse
		testutil.Call(t, testHandler.GetGoalProblemSolving, goalRequest("GET", base+"/problem-solving", testUserID, nil, params)).Want(200).JSON(&response)
		return response
	}
	current := read()
	if current.Revision != 0 || current.State.Current().Charter.Title != goal.Title || !current.CanDecide {
		t.Fatalf("incorrect baseline: %+v", current)
	}
	submit := func(actor, action string, payload any, status int, newTaskTitle ...string) {
		t.Helper()
		request := map[string]any{"expected_revision": current.Revision, "command": map[string]any{"action": action, action: payload}}
		if len(newTaskTitle) != 0 {
			request["new_task_title"] = newTaskTitle[0]
		}
		result := testutil.Call(t, testHandler.ChangeGoalProblemSolving, goalRequest("POST", base+"/problem-solving/commands", actor, request, params)).Want(status)
		if status == 200 {
			previous := current.Revision
			result.JSON(&current)
			if current.Revision != previous+1 {
				t.Fatalf("%s returned revision %d after %d", action, current.Revision, previous)
			}
		}
	}
	ref := func(id string) goalproblem.RevisionRef { return goalproblem.RevisionRef{ID: id, Revision: 1} }
	problem, branch, hypothesis, protocol := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	batchID, resultID, synthesisID, proposalID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	submit(testUserID, "define_problem", goalproblem.DefineProblemInput{
		ProblemID: problem, Statement: "Readers cannot recover a note in time", Baseline: "Five minutes on a frozen corpus",
		AffectedUsers: []string{"Library users"}, Constraints: []string{"Same corpus"},
		SuccessCriterion: "Open the correct note within three minutes",
	}, 200)
	submit(reviewer, "confirm_problem", goalproblem.ConfirmProblemInput{ProblemRef: ref(problem)}, 200)
	submit(testUserID, "create_branch", goalproblem.CreateBranchInput{
		BranchID: branch, ProblemRef: ref(problem), Title: "Old keyword index", Scope: "Search latency",
		Boundary: "Does not include transcription", Rationale: "Keyword lookup precedes source confirmation",
	}, 200)
	submit(testUserID, "create_hypothesis", goalproblem.CreateHypothesisInput{
		HypothesisID: hypothesis, BranchRef: ref(branch), Statement: "Better keyword matches reduce time to find a note",
		FalsificationCriterion: "Median lookup remains over three minutes even with better matches",
	}, 200)
	submit(testUserID, "record_prioritization", goalproblem.RecordPrioritizationInput{
		PrioritizationID: uuid.NewString(), BranchRef: ref(branch), Disposition: goalproblem.DispositionPrioritize, Rank: 1,
		Impact: goalproblem.LevelHigh, Controllability: goalproblem.LevelHigh, Cost: goalproblem.LevelLow,
		Rationale: "Can test on the existing corpus",
	}, 200)
	submit(testUserID, "record_protocol", goalproblem.RecordProtocolInput{
		ProtocolID: protocol, HypothesisRef: ref(hypothesis), PredictedResult: "Three minutes or less",
		SampleSource: "Frozen set of twelve readers' notes", ControlOrComparison: "Existing keyboard search",
		DecisionThreshold: "Median at most 180 seconds", StopCondition: "All twelve observations collected",
	}, 200)
	submit(reviewer, "approve_protocol", goalproblem.ApproveProtocolInput{ProtocolRef: ref(protocol)}, 200)

	createdBatch := goalproblem.StartValidationBatchInput{
		BatchID: uuid.NewString(), HypothesisRef: ref(hypothesis), ProtocolRef: ref(protocol),
	}
	submit(testUserID, "start_validation_batch", createdBatch, 200, "Try the same samples with the original index")
	created, ok := current.State.ValidationBatch(createdBatch.BatchID)
	if !ok || created.Task.IssueID == "" || goalLinkRows(t, goal.ID) != 1 {
		t.Fatalf("verification batch did not create and link its task: %+v", created)
	}
	t.Cleanup(func() { dbfx.Exec(t, "DELETE FROM issue WHERE id=$1", created.Task.IssueID) })
	submit(testUserID, "start_validation_batch", createdBatch, 409, "Must not create a second task")
	if goalLinkRows(t, goal.ID) != 1 {
		t.Fatal("duplicate validation batch created another task")
	}

	batch := goalproblem.StartValidationBatchInput{
		BatchID: batchID, HypothesisRef: ref(hypothesis), ProtocolRef: ref(protocol),
		Task: goalproblem.TaskRef{IssueID: issue, SubmitterID: "untrusted-client-value"},
	}
	submit(testUserID, "start_validation_batch", batch, 409)
	if status := linkGoalIssueViaAPI(t, testUserID, goal.ID, issue); status != 204 {
		t.Fatalf("link verification issue: %d", status)
	}
	submit(testUserID, "start_validation_batch", batch, 200)
	storedBatch, ok := current.State.ValidationBatch(batchID)
	if !ok || storedBatch.Task.IssueID != issue || storedBatch.Task.SubmitterID == "untrusted-client-value" {
		t.Fatalf("incorrect verification task binding: %+v", storedBatch)
	}
	submit(testUserID, "record_validation_result", goalproblem.RecordValidationResultInput{
		ResultID: resultID, BatchID: batchID, Observations: []string{"Accuracy improved but median lookup remained four minutes"},
		Evidence: []goalproblem.EvidenceRef{{ID: uuid.NewString(), Kind: goalproblem.EvidenceKindIssue, IssueID: issue, Summary: "Independent input capture"}},
	}, 200)
	review := goalproblem.ReviewValidationResultInput{ResultID: resultID, Verdict: goalproblem.VerdictRefuted, Rationale: "The frozen threshold was missed"}
	submit(testUserID, "review_validation_result", review, 403)
	submit(reviewer, "review_validation_result", review, 200)
	currentBranch, _ := current.State.Branch(branch)
	if currentBranch.Status != goalproblem.BranchStatusNeedsReassessment || current.State.Current().Number != 1 {
		t.Fatalf("refutation did not reopen the affected branch independently: %+v", current)
	}
	submit(testUserID, "record_synthesis", goalproblem.RecordSynthesisInput{
		SynthesisID: synthesisID, BaseGoalVersion: 1, Recommendation: goalproblem.DecisionRevise,
		Claims:    []goalproblem.SynthesisClaim{{ID: uuid.NewString(), Statement: "Source confirmation sets the effective time", ResultIDs: []string{resultID}}},
		Tradeoffs: []string{"One more explicit acceptance action"}, Limitations: []string{"One frozen corpus"},
		Rationale: "Participants still checked the original before accepting a match",
	}, 200)
	submit(reviewer, "review_synthesis", goalproblem.ReviewSynthesisInput{SynthesisRef: ref(synthesisID), ReviewNotes: "Replayed the linked issue"}, 200)
	submit(testUserID, "propose_charter_revision", goalproblem.ProposeCharterRevisionInput{
		ProposalID: proposalID, BaseGoalVersion: 1, SynthesisRef: ref(synthesisID), ChangeSummary: "Measure opening the correct original",
		ProposedCharter: goalproblem.Charter{Title: "Verify the original note", Intent: goal.Intent, Scope: "Search and source confirmation",
			AcceptanceCriteria: []string{"Open the correct original within three minutes"}},
	}, 200)
	apply := goalproblem.ApplyCharterRevisionInput{ProposalID: proposalID}
	submit(reviewer, "apply_charter_revision", apply, 403)
	if code, _ := updateGoalViaAPI(t, testUserID, goal.ID, map[string]any{"title": "Bypass the decision"}); code != 409 {
		t.Fatalf("direct edit of the managed charter returned %d", code)
	}
	submit(testUserID, "apply_charter_revision", apply, 200)
	submit(testUserID, "apply_charter_revision", apply, 409)

	persisted := read()
	if persisted.Revision != current.Revision || persisted.State.Current().Number != 2 {
		t.Fatalf("incorrect persisted version: %+v", persisted)
	}
	old, _ := persisted.State.GoalVersionAt(1)
	if old.Charter.Title != goal.Title {
		t.Fatalf("original charter changed: %+v", old)
	}
	if code, updated := getGoalViaAPI(t, testUserID, goal.ID); code != http.StatusOK || updated.Title != "Verify the original note" {
		t.Fatalf("goal detail did not reflect the decision: %d %+v", code, updated)
	}
	var updatedPlan GoalPlanResponse
	testutil.Call(t, testHandler.GetGoalPlan, goalRequest("GET", base+"/plan", testUserID, nil, params)).Want(200).JSON(&updatedPlan)
	if updatedPlan.Revision != 2 || len(updatedPlan.Items) != 1 || updatedPlan.Items[0].ID != planItemID || goalLinkRows(t, goal.ID) != 2 {
		t.Fatalf("plan lost prior scope or task identity: %+v", updatedPlan)
	}
	lockedGoal, err := testHandler.Queries.GetGoalInWorkspace(context.Background(), db.GetGoalInWorkspaceParams{ID: parseUUID(goal.ID), WorkspaceID: parseUUID(testWorkspaceID)})
	if err != nil {
		t.Fatal(err)
	}
	storedPlan, err := testHandler.Queries.GetGoalPlan(context.Background(), db.GetGoalPlanParams{GoalID: lockedGoal.ID, WorkspaceID: lockedGoal.WorkspaceID})
	if err != nil {
		t.Fatal(err)
	}
	if allowed, err := testHandler.goalReviewAllows(context.Background(), testHandler.Queries, lockedGoal, storedPlan, false); err != nil || allowed {
		t.Fatalf("the revised charter bypassed plan review: allowed=%v, err=%v", allowed, err)
	}
	if err := testHandler.Queries.DeleteGoal(context.Background(), db.DeleteGoalParams{ID: parseUUID(goal.ID), WorkspaceID: parseUUID(testWorkspaceID)}); err != nil {
		t.Fatal(err)
	}
	var remaining int
	dbfx.QueryRow(t, "SELECT count(*) FROM goal_problem_solving WHERE goal_id=$1", goal.ID).Scan(&remaining)
	if remaining != 0 {
		t.Fatal("deleting the goal left its problem-solving record")
	}
}
