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
	"github.com/multica-ai/multica/server/pkg/acceptance"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/taskfailure"
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
const backlogPlanningTrigger = "Issue continuation (planning)"
const backlogPlanningBrief = `在原事项接续规划和设计，把 TDD 准备作为本轮首要产物。读取目标、前置、原讨论和已有证据，推进当前信息足以支持的部分，与获准参与的成员核对不同方案和反例。
1. 先把敏捷用户故事的使用者、情境、用户结果和范围写清楚，给出编号的可观察验收标准，并用正反例消除歧义。
按一个史诗一个目录、一个短 US 编号一份 Markdown 故事整理草案，例如 epics/<史诗>/US-01.md。
2. 读取项目统一语言表及领域源码。将业务词、含义与源码标识对齐，在项目语言的接口和数据结构中表达相关聚合根、聚合、实体和值对象。明确影响验收的 DDD 领域术语、模型边界、归属、状态转换、业务不变量和权限。从上下游交接、失败或重试中挑出能区分正确与错误实现的例子。
3. 把验收标准和领域规则逐条映射到测试用例、可复用且获准使用的测试数据、固定输入和独立预期。将测试数据准备代码和测试用例代码归入故事旁唯一的 US-01-tdd/，故事链接该目录并给出复跑命令。写明测试入口、当前基线、红灯应因哪条业务断言失败、绿灯应看到什么。已有隔离基线时可先复演红灯。尚未运行的部分保留为待验证。
4. 将草案、夹具或复现步骤、原件和复核入口留在原事项，记录缺口、接回人和复核条件。正式启动工作方法、修改正在使用的代码检出、派工实施或改写现行已授权计划均须经正式交付流程。`

func IsBacklogPlanningTask(task db.AgentTaskQueue) bool {
	return task.TriggerEvidenceKind.String == "issue_continuation" && task.TriggerSummary.String == backlogPlanningTrigger
}

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

	var dependencyChange string
	var goalGate goalBacklogGate
	isBacklog := issuestatus.Effective(ctx, q, issue.WorkspaceID, issue.Status) == issuestatus.Backlog
	if isBacklog {
		goalGate, err = goalBacklogPosition(ctx, q, issue)
		if err != nil || (goalGate.managed && !goalGate.ready && !goalGate.planning) {
			return err
		}
		switch {
		case goalGate.planning:
			var facts string
			if plan := readBacklogPlan(issue); plan != nil {
				_, facts, err = backlogPlanDependencies(ctx, tx, issue, plan)
				if err != nil {
					return err
				}
			}
			// Agent receipts do not reopen their own planning run. A member's
			// new input or a changed dependency does.
			var input string
			if err := tx.QueryRow(ctx, `SELECT coalesce((SELECT id::text||':'||updated_at::text FROM comment
 WHERE workspace_id=$1 AND issue_id=$2 AND author_type='member'
 ORDER BY updated_at DESC, id DESC LIMIT 1),'')`, issue.WorkspaceID, id).Scan(&input); err != nil {
				return err
			}
			dependencyChange = goalGate.key + "/" + facts + "/" + input
		case goalGate.ready:
			dependencyChange = goalGate.key
		default:
			if plan := readBacklogPlan(issue); plan != nil {
				dependencyChange, _, err = backlogPlanDependencies(ctx, tx, issue, plan)
				if err != nil || dependencyChange == "" {
					return err
				}
			}
		}
	}
	if issue.AssigneeType.String == "member" &&
		(IsMemberTodo(issue) || time.Since(issue.UpdatedAt.Time) < 24*time.Hour) {
		return nil
	}

	var method workmethod.Run
	if len(issue.WorkMethodRun) > 0 {
		if err = json.Unmarshal(issue.WorkMethodRun, &method); err != nil {
			return err
		}
		if method.State == "closed" && method.Method != nil && method.Method.GitHubDelivery != nil {
			return nil
		}
		if goalGate.planning && method.GoalExecution {
			return nil
		}
	}
	var execution GoalExecution
	if isBacklog || method.State == "" || method.State == "ready" || method.GoalExecution {
		execution, err = goalExecutionForIssue(ctx, q, issue, false)
		if err != nil || (execution.ReviewHold && !goalGate.planning) || (method.GoalExecution && !execution.ID.Valid) {
			return err
		}
	}
	startGoal := !goalGate.planning && execution.ID.Valid && execution.Progress.Phase == "planning" && !method.GoalExecution
	if !startGoal && !method.GoalExecution {
		execution = GoalExecution{}
	}
	if execution.ID.Valid {
		dependencyChange += "/execution/" + util.UUIDToString(execution.ID)
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
	target, err := continuationOwner(ctx, q, issue, method)
	if err != nil {
		return err
	}
	var recoveryRuntime pgtype.UUID
	if method.GoalExecution {
		if !target.Agent.ID.Valid {
			return nil
		}
		recoveryRuntime = target.Agent.RuntimeID
		// An unavailable provider must not be retried through the same runtime.
		// A different runtime or a new explicit grant starts a fresh attempt
		// for the same frozen role, preserving the independent reviewer.
		var status, priorGap string
		var reason, detail pgtype.Text
		var ended pgtype.Timestamptz
		err := tx.QueryRow(ctx, `
SELECT status, failure_reason, error, coalesce(completed_at,created_at), coalesce(context->>'continuation_gap','')
FROM agent_task_queue
WHERE issue_id=$1 AND agent_id=$2 AND runtime_id=$3
  AND context->>'goal_execution_id'=$4 AND context->>'work_method_revision'=$5
ORDER BY created_at DESC, id DESC LIMIT 1`,
			id, target.Agent.ID, recoveryRuntime, util.UUIDToString(execution.ID), fmt.Sprint(method.Revision),
		).Scan(&status, &reason, &detail, &ended, &priorGap)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if status == "failed" {
			switch taskfailure.NormalizeDaemonReason(reason.String, detail.String) {
			case taskfailure.ReasonAgentProviderAuthOrAccess:
				return nil
			case taskfailure.ReasonAgentProviderQuotaLimit:
				// Kimi reports a five-hour window without a reset timestamp.
				// Replay this slot once after the full window can have elapsed.
				gap += "/quota-reset"
				if !strings.Contains(strings.ToLower(detail.String), "5-hour usage limit") ||
					!ended.Valid || time.Since(ended.Time) < 5*time.Hour || priorGap == gap {
					return nil
				}
			}
		}
	}
	var attempted, waitingForRecheck bool
	if err = tx.QueryRow(ctx, `SELECT count(*)>0,coalesce(max(coalesce(completed_at,created_at))
 FILTER (WHERE context->>'continuation_role' IN ('coordinator','planning')) > now()-interval '24 hours', false)
 OR ($3 AND coalesce(bool_or(status='completed' AND context->>'continuation_role' IN ('coordinator','planning')),false))
 FROM agent_task_queue WHERE issue_id=$1 AND context->>'continuation_gap'=$2
 AND ($4::uuid IS NULL OR runtime_id=$4)`, id, gap, dependencyChange != "", recoveryRuntime).Scan(&attempted, &waitingForRecheck); err != nil || waitingForRecheck {
		return err
	}
	coord := attempted || !target.Agent.ID.Valid || issue.Status == issuestatus.Blocked || isBacklog || startGoal || latestStatus == "cancelled"
	invoker := manager.OwnerID
	if execution.ID.Valid {
		invoker = execution.By
		if method.GoalExecution {
			// Only the frozen current actor may resume a granted step.
			if attempted {
				return nil
			}
			coord = false
		}
	}
	if !coord && !execution.ID.Valid && !CanMemberInvokeAgent(ctx, q, target.Agent, invoker, issue.WorkspaceID) {
		coord = true
	}
	role, note := "owner", continuationBrief
	if coord {
		target = wakeTarget{Type: "agent", Agent: manager}
		role = "coordinator"
		note = coordinationBrief + "\n" + note
	}
	if goalGate.ready {
		note = goalGate.note + "\n" + note
	}
	if method.GoalExecution {
		note = "在已授权的原事项接续当前步骤。先读当前方法、当前候选和最新交接。沿用已裁定的标准、用例和回执，只补新证据、差异及待决定事项。仅在这些入口不足时回查相关历史。平台按正式方法派送下一位。"
		if failure := method.CandidateFailureReview(); failure != nil {
			note += "\n" + failure.ReviewBrief()
		}
		if method.CanDelegateCandidate() {
			worker, err := GoalCandidateWorker(ctx, q, issue, method, execution.ID)
			if err == nil {
				checkout := ReadGoalExecutionTask(worker).CandidateCheckout
				if worker.Status == "failed" && worker.FailureReason.String == taskfailure.ReasonCandidateCheckoutUnavailable.String() && checkout != nil {
					note = fmt.Sprintf("候选工作树准备失败。实现运行 %s 未启动，固定候选为 %s@%s（分支 %s）。核对该运行的环境失败详情，并在原卡记录候选缺失或运行时版本阻塞。保留候选和冻结用例，恢复可验证的工作树后再续派。此运行由平台交给当前小队负责人，无需在评论中 @ 任何人。", util.UUIDToString(worker.ID), checkout.Repository, checkout.Commit, checkout.Branch)
				} else {
					note = fmt.Sprintf("本步骤的实现运行 %s 已结束，状态为 %s。先读取该运行交付回执和验收台账的当前候选，核对 SHA、已运行用例与待审核差异。引用已有证据后送独立评审；把复跑和新反例留给独立评审者。", util.UUIDToString(worker.ID), worker.Status)
				}
			} else if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			trace, _ := acceptance.Decode(issue.AcceptanceCriteria)
			if failure := workmethod.FailedCandidate(trace); failure != nil {
				_, err := GoalCandidateFailureWorker(ctx, q, issue, method, execution.ID, *failure)
				if err == nil {
					note = failure.Summary() + " 用 issue method 的 action=advance 和当前 base_revision 提交这份固定失败回执。平台会正式派送独立裁定；本运行无需复跑或发起新的实现。"
				} else if !errors.Is(err, ErrGoalCandidateConflict) && !errors.Is(err, pgx.ErrNoRows) {
					return err
				}
			}
		}
		if handoff := method.CurrentSquadHandoff(); handoff != nil && handoff.ContextBrief != nil && handoff.ToSquadID == util.UUIDToString(target.SquadID) {
			note += "\n" + handoff.Brief()
		}
	}
	if startGoal {
		note = fmt.Sprintf("目标已授权启动本卡的正式工作方法。用 \"$MULTICA_CLI_PATH\" issue method <本事项编号> 读取当前步骤，再用同一命令加 --change-stdin 提交 action=start、method_id=%q、当前 base_revision。平台将自动交给方法指定角色。", execution.Progress.MethodID)
	}
	summary := "Issue continuation (" + role + ")"
	if goalGate.planning {
		role, note, summary = "planning", backlogPlanningBrief, backlogPlanningTrigger
	}
	if err = (&IssueWakeupService{Tasks: s.Tasks}).authorize(ctx, q, issue.WorkspaceID, invoker, target.Agent); err != nil {
		return err
	}
	scoped := &TaskService{Queries: q, Composio: s.Tasks.Composio, FeatureFlags: s.Tasks.FeatureFlags}
	if err = scoped.guardRunProjectAccess(ctx, issue, target.Agent, invoker, invoker); err != nil {
		return err
	}
	attr := attribution.Unattributed("issue_continuation", issue.ID)
	params, err := scoped.prepareMentionTask(ctx, issue, target.Agent.ID, pgtype.UUID{}, nil, target.SquadID.Valid, target.SquadID, true, note, attr, pgtype.UUID{}, OriginDerived, false)
	if err != nil {
		return err
	}
	if startGoal || (!coord && method.GoalExecution) {
		params.GoalExecutionID = execution.ID
	}
	params.TriggerSummary = pgtype.Text{String: summary, Valid: true}
	if !coord && (method.State == "working" || method.State == "review") {
		params.WorkMethodRevision = pgtype.Int8{Int64: int64(method.Revision), Valid: method.Revision > 0}
	}
	params.FireAt = pgtype.Timestamptz{Time: time.Now(), Valid: true}
	task, err := q.CreateAgentTask(ctx, params)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE agent_task_queue SET context=coalesce(context,'{}')||jsonb_build_object('continuation_gap',$2::text,'continuation_role',$3::text,'continuation_manager',$4::text,'continuation_goal_key',$5::text) WHERE id=$1`, task.ID, gap, role, util.UUIDToString(manager.ID), goalGate.key); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	return s.Tasks.PublishWorkMethodHandoff(ctx, task)
}

type storedBacklogPlan struct {
	State        string
	Next         string
	Dependencies []struct{ Issue, Status string }
}

func readBacklogPlan(issue db.Issue) *storedBacklogPlan {
	var meta struct {
		Plan json.RawMessage `json:"backlog_plan"`
	}
	if len(issue.Metadata) == 0 || json.Unmarshal(issue.Metadata, &meta) != nil {
		return nil
	}
	raw := meta.Plan
	var encoded string
	if json.Unmarshal(raw, &encoded) == nil {
		raw = []byte(encoded)
	}
	var plan storedBacklogPlan
	if json.Unmarshal(raw, &plan) != nil || strings.TrimSpace(plan.Next) == "" {
		return nil
	}
	switch plan.State {
	case "waiting_scope", "needs_story", "waiting_dependency":
	default:
		return nil
	}
	for _, dep := range plan.Dependencies {
		if dep.Issue == "" || dep.Status == "" {
			return nil
		}
	}
	return &plan
}

func backlogPlanDependencies(ctx context.Context, tx pgx.Tx, issue db.Issue, plan *storedBacklogPlan) (string, string, error) {
	var changes, facts []string
	tracked := make(map[string]bool, len(plan.Dependencies))
	for _, dep := range plan.Dependencies {
		tracked[strings.ToUpper(dep.Issue)] = true
	}
	for _, dep := range plan.Dependencies {
		var status, transition, choiceOwner string
		err := tx.QueryRow(ctx, `SELECT i.status,
    CASE WHEN i.assignee_type='member' AND i.metadata ? 'member_todo_key'
      AND upper(i.metadata->>'member_todo_source') <> upper(w.issue_prefix||'-'||$4::integer)
      THEN coalesce(upper(i.metadata->>'member_todo_source'),'') ELSE '' END,
    CASE WHEN i.status=$3 THEN '' ELSE coalesce((SELECT a.id::text FROM activity_log a WHERE a.issue_id=i.id AND a.action='status_changed' ORDER BY a.created_at DESC,a.id DESC LIMIT 1),'') END
    FROM issue i JOIN workspace w ON w.id=i.workspace_id WHERE w.id=$1 AND upper(w.issue_prefix||'-'||i.number)=upper($2)`, issue.WorkspaceID, dep.Issue, dep.Status, issue.Number).Scan(&status, &choiceOwner, &transition)
		if errors.Is(err, pgx.ErrNoRows) {
			status = "missing"
		} else if err != nil {
			return "", "", err
		}
		// A downstream plan tracking the requesting story observes its delivery.
		// The member's choice is input to that story alone.
		if choiceOwner != "" && tracked[choiceOwner] {
			continue
		}
		facts = append(facts, strings.ToUpper(dep.Issue)+":"+status)
		if status != dep.Status {
			changes = append(changes, fmt.Sprintf("%s:%s>%s:%s", strings.ToUpper(dep.Issue), dep.Status, status, transition))
		}
	}
	sort.Strings(changes)
	sort.Strings(facts)
	return strings.Join(changes, ","), strings.Join(facts, ","), nil
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
		GoalKey string `json:"continuation_goal_key"`
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
	if issuestatus.Effective(ctx, q, issue.WorkspaceID, issue.Status) == issuestatus.Backlog {
		gate, err := goalBacklogPosition(ctx, q, issue)
		if err != nil {
			return err
		}
		if (gate.managed && !gate.ready && !(gate.planning && binding.Role == "planning")) ||
			(binding.GoalKey != "" && binding.GoalKey != gate.key) {
			return ErrWakeupForbidden
		}
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
	case "coordinator", "planning":
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
