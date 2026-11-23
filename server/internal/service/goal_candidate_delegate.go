package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/attribution"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/workmethod"
)

var ErrGoalCandidateConflict = errors.New("the current candidate already has an implementation run or the method has changed")

func GoalCandidateWorker(ctx context.Context, q *db.Queries, issue db.Issue, run workmethod.Run, executionID pgtype.UUID) (db.AgentTaskQueue, error) {
	return q.GetGoalCandidateWorker(ctx, db.GetGoalCandidateWorkerParams{
		IssueID: issue.ID, SquadID: parseGoalCandidateSquad(run),
		ExecutionID: util.UUIDToString(executionID), SlotRevision: fmt.Sprint(run.SlotRevision()),
	})
}

func parseGoalCandidateSquad(run workmethod.Run) pgtype.UUID {
	if !run.CanDelegateCandidate() {
		return pgtype.UUID{}
	}
	id, _ := util.ParseUUID(run.CurrentActor.ID)
	return id
}

func checkGoalCandidateWorker(ctx context.Context, q *db.Queries, squadID, workerID pgtype.UUID) error {
	member, err := q.IsSquadMember(ctx, db.IsSquadMemberParams{SquadID: squadID, MemberType: "agent", MemberID: workerID})
	if err != nil {
		return err
	}
	if !member {
		return ErrWakeupForbidden
	}
	return nil
}

func (s *TaskService) DelegateGoalCandidate(ctx context.Context, issue db.Issue, source db.AgentTaskQueue, revision int, workerID, reviewerID pgtype.UUID, brief string) (db.AgentTaskQueue, error) {
	var empty db.AgentTaskQueue
	var run workmethod.Run
	if err := json.Unmarshal(issue.WorkMethodRun, &run); err != nil || !run.CanDelegateCandidate() ||
		!source.IsLeaderTask || !source.SquadID.Valid || source.SquadID != parseGoalCandidateSquad(run) ||
		workerID == source.AgentID || workerID == reviewerID {
		return empty, ErrWakeupForbidden
	}
	if revision != run.Revision {
		return empty, ErrGoalCandidateConflict
	}
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback(ctx)
	q := s.Queries.WithTx(tx)
	execution, err := CheckGoalExecutionTask(ctx, q, issue, source, true)
	if err != nil {
		return empty, err
	}
	current, err := q.GetAgentTask(ctx, source.ID)
	if err != nil {
		return empty, err
	}
	if current.Status != "running" {
		return empty, ErrWakeupForbidden
	}
	if err := checkGoalCandidateWorker(ctx, q, source.SquadID, workerID); err != nil {
		return empty, err
	}
	if err := CheckGoalExecutionRecipient(ctx, q, issue, workerID, execution.By); err != nil {
		return empty, err
	}
	prior, err := GoalCandidateWorker(ctx, q, issue, run, execution.ID)
	if err == nil {
		if prior.AgentID != workerID {
			return empty, ErrGoalCandidateConflict
		}
		return prior, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return empty, err
	}
	active, err := q.ListActiveTasksByIssue(ctx, issue.ID)
	if err != nil {
		return empty, err
	}
	for _, task := range active {
		if task.ID != source.ID {
			return empty, ErrGoalCandidateConflict
		}
	}
	scoped := &TaskService{Queries: q, Composio: s.Composio, FeatureFlags: s.FeatureFlags}
	attr := attribution.DelegatedIssueRun(issue.ID, source.ID, pgtype.UUID{}, source.AccountableUserID)
	note := "Implement the current candidate on the original issue. Read its work method and acceptance ledger. Record evidence there for the squad leader to verify and submit for independent review.\n\n" + brief
	params, err := scoped.prepareMentionTask(ctx, issue, workerID, pgtype.UUID{}, nil, false, source.SquadID, false, note, attr, pgtype.UUID{}, OriginDerived, false)
	if err != nil {
		return empty, err
	}
	params.GoalExecutionID = execution.ID
	params.WorkMethodRevision = pgtype.Int8{Int64: int64(run.SlotRevision()), Valid: true}
	params.TriggerSummary = pgtype.Text{String: "Work method candidate delegation", Valid: true}
	params.FireAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
	task, err := q.CreateAgentTask(ctx, params)
	if err != nil {
		return empty, err
	}
	if err := tx.Commit(ctx); err != nil {
		return empty, err
	}
	return task, nil
}
