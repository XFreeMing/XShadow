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
	"github.com/multica-ai/multica/server/pkg/acceptance"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/multica-ai/multica/server/pkg/workmethod"
)

var ErrGoalCandidateConflict = errors.New("the current candidate already has an implementation run or the method has changed")
var ErrSquadContextChanged = errors.New("交接后验收标准、用例或样本发生变更，请退回对应步骤重新评审，再发出交接")

func checkCandidateSquadContext(issue db.Issue, run workmethod.Run) error {
	handoff := run.CurrentSquadHandoff()
	if handoff != nil && handoff.ContextBrief != nil && !handoff.ContextBrief.MatchesAcceptance(issue.AcceptanceCriteria) {
		return ErrSquadContextChanged
	}
	return nil
}

func GoalCandidateWorker(ctx context.Context, q *db.Queries, issue db.Issue, run workmethod.Run, executionID pgtype.UUID) (db.AgentTaskQueue, error) {
	return q.GetGoalCandidateWorker(ctx, db.GetGoalCandidateWorkerParams{
		IssueID: issue.ID, SquadID: parseGoalCandidateSquad(run),
		ExecutionID: util.UUIDToString(executionID), SlotRevision: fmt.Sprint(run.SlotRevision()),
	})
}

func GoalCandidateFailureWorker(ctx context.Context, q *db.Queries, issue db.Issue, run workmethod.Run, executionID pgtype.UUID, failure workmethod.CandidateFailure) (db.AgentTaskQueue, error) {
	worker, err := GoalCandidateWorker(ctx, q, issue, run, executionID)
	if errors.Is(err, pgx.ErrNoRows) {
		// A renewed grant may route evidence from the same slot for rework.
		// The old run has no authority under the renewed grant.
		id, parseErr := util.ParseUUID(failure.WorkerTaskID)
		if parseErr != nil {
			return db.AgentTaskQueue{}, ErrGoalCandidateConflict
		}
		worker, err = q.GetAgentTask(ctx, id)
	}
	if err != nil {
		return db.AgentTaskQueue{}, err
	}
	binding := ReadGoalExecutionTask(worker)
	if util.UUIDToString(worker.ID) != failure.WorkerTaskID || worker.IssueID != issue.ID ||
		worker.SquadID != parseGoalCandidateSquad(run) || worker.IsLeaderTask || !worker.DelegatedFromTaskID.Valid ||
		binding.CandidateOffer || !binding.ID.Valid || binding.Revision != run.SlotRevision() || worker.Status != "completed" {
		return db.AgentTaskQueue{}, ErrGoalCandidateConflict
	}
	return worker, nil
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

func (s *TaskService) DispatchGoalCandidate(ctx context.Context, issue db.Issue, source db.AgentTaskQueue, action string, revision int, workerID, reviewerID pgtype.UUID, brief string) (db.AgentTaskQueue, error) {
	var empty db.AgentTaskQueue
	var run workmethod.Run
	if err := json.Unmarshal(issue.WorkMethodRun, &run); err != nil || !run.CanDelegateCandidate() ||
		!source.SquadID.Valid || source.SquadID != parseGoalCandidateSquad(run) || workerID == reviewerID {
		return empty, ErrWakeupForbidden
	}
	switch action {
	case workmethod.ClaimCandidate:
		if !run.UsesCandidateSelfClaim() || workerID != source.AgentID || !IsGoalCandidateOffer(source) {
			return empty, ErrWakeupForbidden
		}
	case workmethod.OfferCandidate, workmethod.Delegate:
		if !source.IsLeaderTask || workerID == source.AgentID || run.UsesCandidateSelfClaim() != (action == workmethod.OfferCandidate) {
			return empty, ErrWakeupForbidden
		}
	default:
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
	if err := checkCandidateSquadContext(issue, run); err != nil {
		return empty, err
	}
	if action == workmethod.OfferCandidate {
		offer, err := q.GetGoalCandidateOffer(ctx, db.GetGoalCandidateOfferParams{
			IssueID: issue.ID, SquadID: source.SquadID, AgentID: workerID,
			ExecutionID: util.UUIDToString(execution.ID), SlotRevision: fmt.Sprint(run.SlotRevision()),
		})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return empty, err
		}
		if err == nil {
			switch offer.Status {
			case "completed", "failed", "cancelled":
				if offer.TriggerEvidenceRefID == source.ID {
					return offer, nil
				}
				// A scheduler retry alone does not authorize another invitation.
				var reopened bool
				err := tx.QueryRow(ctx, `SELECT EXISTS (
					SELECT 1 FROM activity_log
					WHERE workspace_id=$1 AND issue_id=$2 AND action='status_changed'
					  AND details->>'from'='blocked' AND details->>'to'=$3 AND created_at>$4
				)`, issue.WorkspaceID, issue.ID, issue.Status, offer.CreatedAt).Scan(&reopened)
				if err != nil {
					return empty, err
				}
				if !reopened {
					return offer, nil
				}
			default:
				return offer, nil
			}
		}
	}
	prior, err := GoalCandidateWorker(ctx, q, issue, run, execution.ID)
	if err == nil {
		if action == workmethod.OfferCandidate || prior.AgentID != workerID {
			return empty, ErrGoalCandidateConflict
		}
		return prior, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return empty, err
	}
	if action != workmethod.OfferCandidate {
		active, err := q.ListActiveTasksByIssue(ctx, issue.ID)
		if err != nil {
			return empty, err
		}
		for _, task := range active {
			if task.ID == source.ID {
				continue
			}
			binding := ReadGoalExecutionTask(task)
			if action == workmethod.ClaimCandidate && binding.ID == execution.ID &&
				run.AcceptsTaskRevision(binding.Revision) && task.SquadID == source.SquadID &&
				(task.IsLeaderTask || IsGoalCandidateOffer(task)) {
				continue
			}
			return empty, ErrGoalCandidateConflict
		}
	}
	scoped := &TaskService{Queries: q, Composio: s.Composio, FeatureFlags: s.FeatureFlags}
	attr := attribution.DelegatedIssueRun(issue.ID, source.ID, pgtype.UUID{}, source.AccountableUserID)
	note := "Implement the current candidate on the original issue. Read its work method and acceptance ledger. Record evidence or blockers there. The workflow will wake the squad leader after this run.\n\n" + brief
	summary := "Work method candidate delegation"
	switch action {
	case workmethod.OfferCandidate:
		attr.EvidenceKind, attr.EvidenceRefID = attribution.EvidenceCandidateOffer, source.ID
		summary = "Candidate self-claim invitation"
		note = fmt.Sprintf("Assess the current candidate using the original issue and acceptance ledger. This run is for assessment only. Do not edit the project or start implementation. If ready, use \"$MULTICA_CLI_PATH\" issue method <issue> --change-stdin with {\"action\":\"claim_candidate\",\"base_revision\":%d,\"evidence\":\"Why you are ready\"}, using the latest revision. After receiving the receipt, end this run. If you need information or cannot take the work, explain what is missing in your result.\n\n%s", revision, brief)
	case workmethod.ClaimCandidate:
		summary = "Self-claimed candidate implementation"
		note = "You claimed this implementation. Read the original issue and the assessment run linked to this implementation for context.\n\n" + note
	}
	if handoff := run.CurrentSquadHandoff(); handoff != nil && handoff.ContextBrief != nil {
		note = handoff.Brief() + "\n\n" + note
	}
	var checkout *protocol.CandidateCheckout
	if action != workmethod.OfferCandidate {
		checkout = reworkCandidateCheckout(issue, run)
		if checkout != nil {
			note = fmt.Sprintf("Resume the recorded candidate %s@%s. The assigned worktree can use a different branch name. Verify that HEAD contains that commit and the frozen cases, then record the actual assignment's branch and new commit.\n\n", checkout.Repository, checkout.Commit) + note
		}
	}
	params, err := scoped.prepareMentionTask(ctx, issue, workerID, pgtype.UUID{}, nil, false, source.SquadID, action != workmethod.Delegate, note, attr, pgtype.UUID{}, OriginDerived, false)
	if err != nil {
		return empty, err
	}
	params.GoalExecutionID = execution.ID
	params.GoalCandidateOffer = pgtype.Bool{Bool: action == workmethod.OfferCandidate, Valid: action == workmethod.OfferCandidate}
	params.WorkMethodRevision = pgtype.Int8{Int64: int64(run.SlotRevision()), Valid: true}
	params.TriggerSummary = pgtype.Text{String: summary, Valid: true}
	params.FireAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
	task, err := q.CreateAgentTask(ctx, params)
	if err != nil {
		return empty, err
	}
	if checkout != nil {
		raw, err := json.Marshal(checkout)
		if err != nil {
			return empty, err
		}
		if err := tx.QueryRow(ctx, `UPDATE agent_task_queue SET context=coalesce(context,'{}')||jsonb_build_object('candidate_checkout',$2::jsonb) WHERE id=$1 RETURNING context`, task.ID, raw).Scan(&task.Context); err != nil {
			return empty, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return empty, err
	}
	return task, nil
}

func IsGoalCandidateOffer(task db.AgentTaskQueue) bool {
	return ReadGoalExecutionTask(task).CandidateOffer
}

func reworkCandidateCheckout(issue db.Issue, run workmethod.Run) *protocol.CandidateCheckout {
	transition := len(run.History) - 1 - (run.Revision - run.SlotRevision())
	if transition < 0 || run.History[transition].Action != "rework" {
		return nil
	}
	trace, err := acceptance.Decode(issue.AcceptanceCriteria)
	if err != nil {
		return nil
	}
	var checkout *protocol.CandidateCheckout
	for _, criterion := range trace.Criteria {
		for index := len(criterion.Implementations) - 1; index >= 0; index-- {
			link := criterion.Implementations[index]
			if link.Version != len(criterion.Versions) {
				continue
			}
			ref := protocol.CandidateCheckout{Repository: link.Repository, Branch: link.Branch, Commit: link.Commit}
			if checkout != nil && *checkout != ref {
				return nil
			}
			checkout = &ref
			break
		}
	}
	return checkout
}
