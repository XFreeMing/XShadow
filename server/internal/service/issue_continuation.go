package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/attribution"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/workmethod"
)

// An operator opts one workspace into recovery by pinning its coordinating agent.
// Native handoffs have the first opportunity to dispatch after a transition.
type IssueContinuation struct {
	Tasks     *TaskService
	ManagerID pgtype.UUID
}

const continuationCandidates = `
 FROM issue i
 JOIN issue_status st ON st.workspace_id=i.workspace_id AND st.key=i.status
 WHERE i.workspace_id=$1 AND i.triage_state IS NULL AND st.category NOT IN ('done','closed')
 AND i.updated_at < now()-interval '30 seconds'
 AND NOT EXISTS (SELECT FROM agent_task_queue t WHERE t.issue_id=i.id
   AND (t.status NOT IN ('completed','cancelled','failed') OR t.completed_at > now()-interval '30 seconds'))
 AND NOT EXISTS (SELECT FROM issue_wakeup w JOIN agent a ON a.id=w.agent_id AND a.workspace_id=i.workspace_id
   WHERE w.issue_id=i.id AND w.enabled AND a.archived_at IS NULL AND a.runtime_id IS NOT NULL
   AND w.paused_reason IS NULL AND (w.expires_at IS NULL OR w.expires_at>now())
   AND (w.filter_task_id IS NULL OR EXISTS (SELECT FROM agent_task_queue ft WHERE ft.issue_id=i.id AND ft.id=w.filter_task_id
     AND (ft.status NOT IN ('completed','cancelled','failed') OR ft.completed_at>now()-interval '2 minutes')))
   AND (w.condition->>'type' IS DISTINCT FROM 'other_issue' OR EXISTS (
     SELECT FROM issue dep JOIN issue_status ds ON ds.workspace_id=dep.workspace_id AND ds.key=dep.status
     WHERE dep.workspace_id=i.workspace_id AND dep.id::text=w.condition->>'issue_id'
     AND (ds.category NOT IN ('closed','done') OR (ds.category='done' AND w.condition->>'member_option' IS NOT NULL) OR (dep.updated_at > now()-interval '2 minutes' AND
          (w.condition->>'state'='ended' OR (ds.category='done' AND w.condition->>'state'='done'))))))
   AND (w.next_fire_at IS NOT NULL OR w.expires_at IS NOT NULL OR w.condition->>'type'='other_issue'
     OR w.updated_at>now()-interval '24 hours'))
 AND NOT EXISTS (SELECT FROM issue child JOIN issue_status cs ON cs.workspace_id=child.workspace_id AND cs.key=child.status
   WHERE child.parent_issue_id=i.id AND child.workspace_id=i.workspace_id AND cs.category NOT IN ('closed','done')
   AND EXISTS (SELECT FROM issue_wakeup w WHERE w.issue_id=i.id AND w.enabled AND w.system_rule='child_done'))
`

func (s *IssueContinuation) Tick(ctx context.Context) error {
	manager, err := s.Tasks.Queries.GetAgent(ctx, s.ManagerID)
	if err != nil {
		return err
	}
	tx, err := s.Tasks.TxStarter.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, "SELECT i.id "+continuationCandidates+" ORDER BY i.id", manager.WorkspaceID)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[pgtype.UUID])
	if err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	var failures []error
	for _, id := range ids {
		if ctx.Err() != nil {
			return errors.Join(ctx.Err(), errors.Join(failures...))
		}
		if err := s.recoverIssue(ctx, manager, id); err != nil {
			failures = append(failures, fmt.Errorf("issue %s: %w", util.UUIDToString(id), err))
		}
	}
	return errors.Join(failures...)
}

const continuationBrief = "请读取原事项、当前工作方法、最近回执与已登记的等待。接回当前步骤并核实下一位实际接单。若原目标或授权已明确撤销、事项重复或已被另一交付替代且无需再做，在原卡记录依据或替代事项，直接将状态设为 cancelled（已取消），并读回。超时、运行失败、缺资料或关联卡找不到，单独都不足以取消。已完成须满足原验收要求。还要等待时，在原卡登记真实条件、接回人和可用唤醒；没有可订阅事件则登记定时复核。保留已有授权、独立评审和成员决定。"
const coordinationBrief = "你负责协调这个未接续的事项。核对上一轮为什么没有形成结果；仍需执行或审核时交给有权的负责人，并验证接单。待规划、成员答复和显式阻塞只做规划、核查或解除阻塞；不得代成员答复或自行扩大实施范围。"

func (s *IssueContinuation) recoverIssue(ctx context.Context, manager db.Agent, id pgtype.UUID) error {
	tx, err := s.Tasks.TxStarter.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	q := s.Tasks.Queries.WithTx(tx)
	if _, err = q.LockWorkspaceForChatSessionCreate(ctx, manager.WorkspaceID); err != nil {
		return err
	}
	issue, err := q.LockWakeupIssue(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var eligible bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 "+continuationCandidates+" AND i.id=$2)", manager.WorkspaceID, id).Scan(&eligible); err != nil || !eligible {
		return err
	}

	// A recorded wait stays quiet until its observed facts change.
	var dependencyChange string
	if issue.Status == issuestatus.Backlog {
		var waiting bool
		waiting, dependencyChange, err = backlogPlanWait(ctx, tx, issue)
		if err != nil || waiting {
			return err
		}
	}
	if issue.AssigneeType.String == "member" {
		var todo struct {
			Key    string `json:"member_todo_key"`
			Source string `json:"member_todo_source"`
		}
		_ = json.Unmarshal(issue.Metadata, &todo)
		if (todo.Key != "" && todo.Source != "") || time.Since(issue.UpdatedAt.Time) < 24*time.Hour {
			return nil
		}
	}

	var method workmethod.Run
	if len(issue.WorkMethodRun) > 0 {
		if err = json.Unmarshal(issue.WorkMethodRun, &method); err != nil {
			return err
		}
	}
	var ordinary, latestStatus, transition string
	if err = tx.QueryRow(ctx, `SELECT coalesce((SELECT id::text FROM agent_task_queue WHERE issue_id=$1
   AND trigger_evidence_kind IS DISTINCT FROM 'issue_continuation' ORDER BY created_at DESC,id DESC LIMIT 1),''),
   coalesce((SELECT status FROM agent_task_queue WHERE issue_id=$1 ORDER BY created_at DESC,id DESC LIMIT 1),''),
   coalesce((SELECT id::text FROM activity_log WHERE issue_id=$1 AND action='status_changed' ORDER BY created_at DESC,id DESC LIMIT 1),'')`, id).Scan(&ordinary, &latestStatus, &transition); err != nil {
		return err
	}
	gap := fmt.Sprintf("%s/%s/%s/%d/%s/%s", issue.Status, issue.AssigneeType.String, util.UUIDToString(issue.AssigneeID), method.Revision, ordinary, transition)
	if dependencyChange != "" {
		gap += "/" + dependencyChange
	}
	var attempted, waitingForRecheck bool
	if err = tx.QueryRow(ctx, `SELECT count(*)>0,coalesce(max(coalesce(completed_at,created_at))
 FILTER (WHERE context->>'continuation_role'='coordinator') > now()-interval '24 hours', false)
 OR ($3 AND coalesce(bool_or(status='completed' AND context->>'continuation_role'='coordinator'),false))
 FROM agent_task_queue WHERE issue_id=$1 AND context->>'continuation_gap'=$2`, id, gap, dependencyChange != "").Scan(&attempted, &waitingForRecheck); err != nil || waitingForRecheck {
		return err
	}
	target, err := continuationOwner(ctx, q, issue, method)
	if err != nil {
		return err
	}
	coord := attempted || !target.Agent.ID.Valid || issue.Status == issuestatus.Blocked || issue.Status == issuestatus.Backlog || latestStatus == "cancelled"
	if !coord && !CanMemberInvokeAgent(ctx, q, target.Agent, manager.OwnerID, issue.WorkspaceID) {
		coord = true
	}
	role, note := "owner", continuationBrief
	if coord {
		target = wakeTarget{Type: "agent", Agent: manager}
		role = "coordinator"
		note = coordinationBrief + "\n" + note
	}
	if err = (&IssueWakeupService{Tasks: s.Tasks}).authorize(ctx, q, issue.WorkspaceID, manager.OwnerID, target.Agent); err != nil {
		return err
	}
	scoped := &TaskService{Queries: q, Composio: s.Tasks.Composio, FeatureFlags: s.Tasks.FeatureFlags}
	if err = scoped.guardRunProjectAccess(ctx, issue, target.Agent, manager.OwnerID, manager.OwnerID); err != nil {
		return err
	}
	attr := attribution.Unattributed("issue_continuation", issue.ID)
	params, err := scoped.prepareMentionTask(ctx, issue, target.Agent.ID, pgtype.UUID{}, nil, target.SquadID.Valid, target.SquadID, true, note, attr, pgtype.UUID{}, OriginDerived, false)
	if err != nil {
		return err
	}
	params.TriggerSummary = pgtype.Text{String: "Issue continuation (" + role + ")", Valid: true}
	if !coord && (method.State == "working" || method.State == "review") {
		params.WorkMethodRevision = pgtype.Int8{Int64: int64(method.Revision), Valid: method.Revision > 0}
	}
	params.FireAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
	task, err := q.CreateAgentTask(ctx, params)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE agent_task_queue SET context=coalesce(context,'{}')||jsonb_build_object('continuation_gap',$2::text,'continuation_role',$3::text,'continuation_manager',$4::text) WHERE id=$1`, task.ID, gap, role, util.UUIDToString(manager.ID)); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	return s.Tasks.PublishWorkMethodHandoff(ctx, task)
}

func backlogPlanWait(ctx context.Context, tx pgx.Tx, issue db.Issue) (bool, string, error) {
	var meta struct {
		Plan json.RawMessage `json:"backlog_plan"`
	}
	if len(issue.Metadata) == 0 || json.Unmarshal(issue.Metadata, &meta) != nil {
		return false, "", nil
	}
	raw := meta.Plan
	var encoded string
	if json.Unmarshal(raw, &encoded) == nil {
		raw = []byte(encoded)
	}
	var plan struct {
		State        string
		Next         string
		Dependencies []struct{ Issue, Status string }
	}
	if json.Unmarshal(raw, &plan) != nil || plan.Next == "" {
		return false, "", nil
	}
	switch plan.State {
	case "waiting_scope", "needs_story", "waiting_dependency":
	default:
		return false, "", nil
	}
	var changes []string
	for _, dep := range plan.Dependencies {
		if dep.Issue == "" || dep.Status == "" {
			return false, "", nil
		}
		var status, transition string
		err := tx.QueryRow(ctx, `SELECT i.status, CASE WHEN i.status=$3 THEN '' ELSE coalesce((SELECT a.id::text FROM activity_log a WHERE a.issue_id=i.id AND a.action='status_changed' ORDER BY a.created_at DESC,a.id DESC LIMIT 1),'') END
    FROM issue i JOIN workspace w ON w.id=i.workspace_id WHERE w.id=$1 AND upper(w.issue_prefix||'-'||i.number)=upper($2)`, issue.WorkspaceID, dep.Issue, dep.Status).Scan(&status, &transition)
		if errors.Is(err, pgx.ErrNoRows) {
			status = "missing"
		} else if err != nil {
			return false, "", err
		}
		if status != dep.Status {
			changes = append(changes, fmt.Sprintf("%s:%s>%s:%s", strings.ToUpper(dep.Issue), dep.Status, status, transition))
		}
	}
	sort.Strings(changes)
	return len(changes) == 0, strings.Join(changes, ","), nil
}

func continuationOwner(ctx context.Context, q *db.Queries, issue db.Issue, method workmethod.Run) (wakeTarget, error) {
	if method.CurrentActor != nil && (method.State == "review" || method.State == "working") {
		id, err := util.ParseUUID(method.CurrentActor.ID)
		if err != nil {
			return wakeTarget{}, err
		}
		issue.AssigneeID = id
		issue.AssigneeType = pgtype.Text{String: method.CurrentActor.Kind, Valid: true}
	}
	return resolveWakeTarget(ctx, q, issue)
}

func (s *IssueWakeupService) checkContinuationClaim(ctx context.Context, task db.AgentTaskQueue) error {
	var binding struct {
		Manager string `json:"continuation_manager"`
		Role    string `json:"continuation_role"`
	}
	if err := json.Unmarshal(task.Context, &binding); err != nil {
		return ErrWakeupForbidden
	}
	managerID, err := util.ParseUUID(binding.Manager)
	if err != nil {
		return ErrWakeupForbidden
	}
	q := s.Tasks.Queries
	issue, err := q.GetIssue(ctx, task.IssueID)
	if err != nil {
		return err
	}
	active, err := wakeupIssueActive(ctx, q, issue)
	if err != nil {
		return err
	}
	if !active || issue.TriageState.Valid || issue.Status != task.WorkflowStep.String {
		return ErrWakeupForbidden
	}
	manager, err := q.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{ID: managerID, WorkspaceID: issue.WorkspaceID})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrWakeupForbidden
	}
	if err != nil {
		return err
	}
	target := manager
	switch binding.Role {
	case "owner":
		var method workmethod.Run
		if len(issue.WorkMethodRun) > 0 {
			if err = json.Unmarshal(issue.WorkMethodRun, &method); err != nil {
				return err
			}
		}
		owner, err := continuationOwner(ctx, q, issue, method)
		if err != nil {
			return err
		}
		target = owner.Agent
	case "coordinator":
	default:
		return ErrWakeupForbidden
	}
	if manager.ArchivedAt.Valid || target.ID != task.AgentID {
		return ErrWakeupForbidden
	}
	if err := s.authorize(ctx, q, issue.WorkspaceID, manager.OwnerID, target); err != nil {
		return err
	}
	err = s.Tasks.guardRunProjectAccess(ctx, issue, target, manager.OwnerID, manager.OwnerID)
	if errors.Is(err, ErrProjectAccessDenied) {
		return ErrWakeupForbidden
	}
	return err
}
