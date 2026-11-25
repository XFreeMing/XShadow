package handler

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/projectaccess"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/pkg/acceptance"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/workmethod"
)

type GoalReviewAssessment struct {
	Decision            string            `json:"decision"`
	Summary             string            `json:"summary"`
	Checks              []GoalReviewCheck `json:"checks"`
	ReplaceDependencies []string          `json:"replace_dependencies_for,omitempty"`
}

type GoalReviewCheck struct {
	Outcome string   `json:"outcome"`
	Result  string   `json:"result"`
	ItemIDs []string `json:"item_ids"`
	Reason  string   `json:"reason"`
}

type GoalPlanReviewView struct {
	GenerationID string                `json:"generation_id"`
	BaseRevision int32                 `json:"base_revision"`
	Phase        string                `json:"phase"`
	State        string                `json:"state"`
	Assessment   *GoalReviewAssessment `json:"assessment,omitempty"`
}

type goalReviewInput struct {
	OwnerID     pgtype.UUID       `json:"owner_id"`
	Fingerprint string            `json:"fingerprint"`
	Phase       string            `json:"phase"`
	Issues      []goalReviewIssue `json:"issues"`
}

type goalReviewIssue struct {
	ItemID     string               `json:"item_id"`
	IssueID    string               `json:"issue_id"`
	Title      string               `json:"title"`
	Status     string               `json:"status"`
	Revision   int64                `json:"revision,omitempty"`
	ProblemAt  string               `json:"problem_at,omitempty"`
	Problem    string               `json:"problem,omitempty"`
	SourceTask string               `json:"source_task,omitempty"`
	Closeout   *acceptance.Closeout `json:"closeout,omitempty"`
}

func readGoalReviewInput(raw []byte) (*goalReviewInput, error) {
	var input struct {
		Review *goalReviewInput `json:"goal_review"`
	}
	err := json.Unmarshal(raw, &input)
	return input.Review, err
}

func (h *Handler) StartGoalPlanReview(w http.ResponseWriter, r *http.Request) {
	goal, ok := h.loadVisibleGoal(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	if !h.canManageGoalExecution(r, goal) {
		writeError(w, 403, "goal review requires the goal creator or a workspace administrator")
		return
	}
	var req struct {
		ExpectedRevision int32  `json:"expected_revision"`
		AgentID          string `json:"agent_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil || req.ExpectedRevision < 1 {
		writeError(w, 400, "a saved revision and reviewer are required")
		return
	}
	agentID, ok := parseUUIDOrBadRequest(w, req.AgentID, "agent_id")
	if !ok {
		return
	}
	generation, err := h.startGoalPlanReview(r.Context(), goal, parseUUID(requestUserID(r)), agentID, req.ExpectedRevision)
	if err != nil {
		switch {
		case errors.Is(err, errGoalReviewAccess):
			writeError(w, 403, err.Error())
		case errors.Is(err, errGoalReviewChanged):
			writeError(w, 409, err.Error())
		default:
			writeError(w, 500, "failed to start goal plan review")
		}
		return
	}
	h.writeGoalPlanGenerationReplay(w, r, goal, generation)
}

var (
	errGoalReviewAccess  = errors.New("reviewer or authorizing member does not have goal project access")
	errGoalReviewChanged = errors.New("goal or plan changed; reload before starting the review")
)

// TickGoalPlanReviews watches only explicitly enrolled goals. The request key
// is derived from material facts, so repeated sweeps reuse the same run.
func (h *Handler) TickGoalPlanReviews(ctx context.Context) error {
	goals, err := h.Queries.ListGoalsWithPlanReview(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, goal := range goals {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		last, err := h.Queries.GetLatestGoalPlanReview(ctx, db.GetLatestGoalPlanReviewParams{WorkspaceID: goal.WorkspaceID, GoalID: goal.ID})
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if state := h.effectiveGoalPlanGenerationState(ctx, last); state == "queued" || state == "running" {
			continue
		}
		policy, err := readGoalReviewInput(last.InputContext)
		if err != nil || policy == nil {
			continue
		}
		plan, err := h.Queries.GetGoalPlan(ctx, db.GetGoalPlanParams{WorkspaceID: goal.WorkspaceID, GoalID: goal.ID})
		if err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				failures = append(failures, err)
			}
			continue
		}
		progress, err := service.ReadGoalWorkProgress(ctx, h.Queries, goal, plan)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		current, err := buildGoalReviewInputWithBindings(ctx, h.Queries, goal, plan, policy.OwnerID, progress.PreparationBindings)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if current.Fingerprint == policy.Fingerprint {
			if last.State == "succeeded" && last.BaseRevision == plan.Revision {
				if err := h.materializeGoalPreparation(ctx, goal, plan, last, policy.OwnerID, progress); err != nil {
					failures = append(failures, err)
				}
			}
			continue
		}
		_, err = h.startGoalPlanReview(ctx, goal, policy.OwnerID, last.AgentID, 0)
		if err != nil && !errors.Is(err, errGoalReviewAccess) && !errors.Is(err, errGoalReviewChanged) {
			failures = append(failures, fmt.Errorf("goal review %s: %w", uuidToString(goal.ID), err))
		}
	}
	return errors.Join(failures...)
}

func (h *Handler) startGoalPlanReview(ctx context.Context, goal db.Goal, owner, reviewer pgtype.UUID, expected int32) (db.GoalPlanGeneration, error) {
	var empty db.GoalPlanGeneration
	if !goal.ProjectID.Valid || goal.Status != "in_progress" {
		return empty, errGoalReviewChanged
	}
	agent, err := h.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{ID: reviewer, WorkspaceID: goal.WorkspaceID})
	if err != nil {
		return empty, errGoalReviewAccess
	}
	sessionID := dbid.NewV7()
	prepared, prepErr := h.TaskService.PrepareGoalReviewSend(ctx, agent, goal.ID)
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback(ctx)
	q := h.Queries.WithTx(tx)
	if _, err := q.LockWorkspaceForChatSessionCreate(ctx, goal.WorkspaceID); err != nil {
		return empty, err
	}
	if _, err := q.LockProjectForChatSessionCreate(ctx, db.LockProjectForChatSessionCreateParams{ID: goal.ProjectID, WorkspaceID: goal.WorkspaceID}); err != nil {
		return empty, err
	}
	locked, err := q.LockGoalForUpdate(ctx, db.LockGoalForUpdateParams{ID: goal.ID, WorkspaceID: goal.WorkspaceID})
	if err != nil {
		return empty, err
	}
	if locked.ProjectID != goal.ProjectID || locked.Status != "in_progress" {
		return empty, errGoalReviewChanged
	}
	member, err := q.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{UserID: owner, WorkspaceID: goal.WorkspaceID})
	if err != nil || !service.CanManageGoalExecution(member, locked) || !service.CanMemberInvokeAgent(ctx, q, agent, owner, goal.WorkspaceID) {
		return empty, errGoalReviewAccess
	}
	access, err := projectaccess.ForPrincipals(ctx, q, goal.WorkspaceID, owner, agent.OwnerID)
	if err != nil {
		return empty, err
	}
	if !access.CanSeeProject(goal.ProjectID) {
		return empty, errGoalReviewAccess
	}
	plan, err := q.GetGoalPlan(ctx, db.GetGoalPlanParams{GoalID: locked.ID, WorkspaceID: locked.WorkspaceID})
	if errors.Is(err, pgx.ErrNoRows) || (expected != 0 && expected != plan.Revision) {
		return empty, errGoalReviewChanged
	}
	if err != nil {
		return empty, err
	}
	review, err := buildGoalReviewInput(ctx, q, locked, plan, owner)
	if err != nil {
		return empty, err
	}
	latest, err := q.GetLatestGoalPlanReview(ctx, db.GetLatestGoalPlanReviewParams{WorkspaceID: goal.WorkspaceID, GoalID: goal.ID})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return empty, err
	}
	forExecution := false
	if expected != 0 && review.Phase != "closeout" {
		execution, err := service.ReadGoalExecution(ctx, q, locked, plan)
		if err != nil {
			return empty, err
		}
		forExecution = !execution.ID.Valid
	}
	if (latest.BaseRevision != plan.Revision || forExecution) && review.Phase != "closeout" {
		review.Phase = "preflight"
	}
	if expected == 0 && (!latest.ID.Valid || latest.AgentID != reviewer) {
		return empty, errGoalReviewChanged
	}
	if expected == 0 {
		policy, err := readGoalReviewInput(latest.InputContext)
		if err != nil || policy == nil || policy.OwnerID != owner {
			return empty, errGoalReviewChanged
		}
	}
	key := "review/" + review.Fingerprint + "/" + uuidToString(reviewer)
	if forExecution {
		if previous, _ := readGoalReviewInput(latest.InputContext); previous != nil &&
			previous.Fingerprint == review.Fingerprint && previous.Phase == "preflight" && latest.AgentID == reviewer &&
			h.effectiveGoalPlanGenerationState(ctx, latest) != "failed" {
			return latest, nil
		}
		key += "/preflight/" + uuidToString(latest.ID)
	}
	known, err := q.GetGoalPlanGenerationByRequestKey(ctx, db.GetGoalPlanGenerationByRequestKeyParams{GoalID: goal.ID, WorkspaceID: goal.WorkspaceID, RequestKey: key})
	if err == nil {
		if previous, _ := readGoalReviewInput(latest.InputContext); previous != nil &&
			previous.Fingerprint == review.Fingerprint && latest.AgentID == reviewer {
			known = latest
		}
		if expected == 0 || h.effectiveGoalPlanGenerationState(ctx, known) != "failed" {
			return known, nil
		}
		// An explicit retry of a failed run is distinct from a scheduler replay.
		retry := uuidToString(known.ID)
		key += "/" + retry[len(retry)-8:]
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return empty, err
	}
	if prepErr != nil {
		return empty, prepErr
	}
	input, err := h.buildGoalPlanGenerationInput(ctx, q, locked, plan, true)
	if err != nil {
		return empty, err
	}
	var snapshot map[string]json.RawMessage
	if err := json.Unmarshal(input.Snapshot, &snapshot); err != nil {
		return empty, err
	}
	snapshot["goal_review"], err = json.Marshal(review)
	if err != nil {
		return empty, err
	}
	input.Snapshot, err = json.Marshal(snapshot)
	if err != nil {
		return empty, err
	}
	input.Review = &review
	generation, session, sent, err := h.enqueueGoalPlanRun(ctx, tx, locked, input, goalPlanLaunch{
		Key: key, Intent: "持续目标复审", SessionID: sessionID, AgentID: reviewer, OwnerID: owner,
		Prepared: prepared,
	})
	if err != nil {
		return empty, err
	}
	if err := tx.Commit(ctx); err != nil {
		return empty, err
	}
	h.TaskService.FinalizeDirectChatSend(ctx, session, sent)
	h.publishGoalChanged(uuidToString(goal.WorkspaceID), "", "")
	return generation, nil
}

func buildGoalReviewInput(ctx context.Context, q *db.Queries, goal db.Goal, plan db.GoalPlan, owner pgtype.UUID) (goalReviewInput, error) {
	progress, err := service.ReadGoalWorkProgress(ctx, q, goal, plan)
	if err != nil {
		return goalReviewInput{}, err
	}
	return buildGoalReviewInputWithBindings(ctx, q, goal, plan, owner, progress.PreparationBindings)
}

func buildGoalReviewInputWithBindings(ctx context.Context, q *db.Queries, goal db.Goal, plan db.GoalPlan, owner pgtype.UUID, bindings map[string]string) (goalReviewInput, error) {
	var content GoalPlanContent
	if err := json.Unmarshal(plan.Content, &content); err != nil {
		return goalReviewInput{}, err
	}
	bindGoalPreparationIssues(content.Items, bindings)
	review := goalReviewInput{OwnerID: owner, Phase: "execution", Issues: []goalReviewIssue{}}
	complete := 0
	for _, item := range content.Items {
		if item.Binding.IssueID == nil {
			continue
		}
		id := parseUUIDOrInvalid(*item.Binding.IssueID)
		issue, err := q.GetIssue(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			review.Issues = append(review.Issues, goalReviewIssue{ItemID: item.ID, IssueID: *item.Binding.IssueID, Title: item.Title, Status: "missing"})
			continue
		}
		if err != nil {
			return review, err
		}
		if issue.WorkspaceID != goal.WorkspaceID || issue.ProjectID != goal.ProjectID {
			return review, errGoalReviewAccess
		}
		link, err := q.GetGoalIssueLink(ctx, db.GetGoalIssueLinkParams{WorkspaceID: goal.WorkspaceID, IssueID: issue.ID})
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && link.GoalID != goal.ID) {
			review.Issues = append(review.Issues, goalReviewIssue{ItemID: item.ID, IssueID: *item.Binding.IssueID, Title: item.Title, Status: "unlinked"})
			continue
		}
		if err != nil {
			return review, err
		}
		fact := goalReviewIssue{ItemID: item.ID, IssueID: *item.Binding.IssueID, Title: item.Title, Status: "open"}
		state := issuestatus.Effective(ctx, q, goal.WorkspaceID, issue.Status)
		var run workmethod.Run
		if len(issue.WorkMethodRun) > 0 {
			if err := json.Unmarshal(issue.WorkMethodRun, &run); err != nil {
				return review, err
			}
		}
		switch state {
		case issuestatus.Done:
			complete++
			fact.Status, fact.Revision = state, issue.Revision
			if run.State != "closed" && run.GoalExecution {
				complete--
				fact.Status = "unverified"
			}
			if run.State == "closed" && len(run.History) > 0 {
				last := run.History[len(run.History)-1]
				fact.Closeout = last.Closeout
				if fact.Closeout == nil && last.Action == "approve" {
					trace, traceErr := acceptance.Decode(issue.AcceptanceCriteria)
					approvedAt, timeErr := time.Parse(time.RFC3339Nano, last.At)
					if traceErr == nil && timeErr == nil && len(trace.Criteria) > 0 {
						closeout := trace.Closeout(&approvedAt)
						fact.Closeout = &closeout
					}
				}
			}
		case issuestatus.Blocked, issuestatus.Cancelled:
			fact.Status, fact.Revision = state, issue.Revision
		}
		if run.State != "closed" {
			for i := len(run.History) - 1; i >= 0; i-- {
				event := run.History[i]
				if event.Action == "rework" {
					fact.Problem = event.Evidence
					fact.ProblemAt = event.At
					break
				}
			}
		}
		if state != issuestatus.Done && state != issuestatus.Cancelled {
			latest, err := q.LatestGoalReviewIssueTask(ctx, issue.ID)
			if err == nil && latest.Status == "failed" {
				fact.Problem, fact.SourceTask = latest.FailureReason.String, uuidToString(latest.ID)
			} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return review, err
			}
		}
		// Creating or preparing a card alone does not invalidate the review.
		// An execution problem or a closeout still becomes review evidence.
		if bindings[item.ID] != "" && fact.Status == "open" && fact.ProblemAt == "" && fact.SourceTask == "" {
			continue
		}
		review.Issues = append(review.Issues, fact)
	}
	if complete > 0 && complete == len(content.Items) {
		review.Phase = "closeout"
	}
	facts, err := json.Marshal([]any{goal.ProjectID, goal.Title, goal.Intent, goal.Scope, goal.AcceptanceCriteria, plan.Revision, review.Issues})
	if err != nil {
		return review, err
	}
	review.Fingerprint = fmt.Sprintf("%x", sha256.Sum256(facts))
	return review, nil
}

func buildGoalReviewPrompt(generation db.GoalPlanGeneration, input goalPlanGenerationInput, base string) string {
	facts, _ := json.Marshal(input.Review.Issues)
	base, _, _ = strings.Cut(base, "\n## 输出要求\n")
	return base + fmt.Sprintf(`
## XShadow 自动目标复审
阶段 %s。下列是计划绑定事项的最新状态和正式结案证据。附件 ID 及版本是引用，避免重新抄写证据。遇到事实缺失再读取原卡或项目资源。
%s

先以独立评判者身份逐条核对目标原有验收条件。设想所有事项都按各自标准交付，找具体的反例，尤其核对不同卡能否用同一身份、同一任务/版次沿实际入口联验。closeout 阶段必须以真实已采信结果核对。
preflight 阶段评判计划能否覆盖目标。尚未交付的事项按其用户结果、验收标准和依赖评判。仅缺少实施或终验结果时，若计划清楚覆盖原条件，可以给 sufficient 并说明未来联验方式。需要验证但计划没有安排的目标行为仍是缺口。
执行中先归因。若当前卡正确交付仍无法达成目标，归为 planning_gap。实现或用例返工、提供商、权限或环境问题归为 execution_issue，给出原处理路径。缺乏证据时用 insufficient_evidence。充分覆盖才用 covered。结案证据只证明其记录的范围。
仅 planning_gap 自动生成局部候选。保留其余事项、已绑定事项和已采信证据。现有节点同一 ID，仅补全可调整的未绑定节点。新增节点需 UUID、binding {type:"new"}、实际证据 refs、交付类型、评审方法和独立可执行的验证标准。items 只写新增或变更的节点，edges 只写新增边或显式替换的入边。若要替换现有待办节点的入边，列入 replace_dependencies_for，并提交该节点的全部新版入边。禁止删除或更改已绑定节点。不要保存计划、创建事项、修改项目文件或派工。
提交 UTF-8 JSON 如下。checks 每条目标条件一项，item_ids 填实际对应的计划节点 UUID，result 为 sufficient、gap 或 unverified，reason 写证据或可复现缺口。非规划缺口时 items 和 edges 留空。
{"review":{"decision":"covered|planning_gap|execution_issue|insufficient_evidence","summary":"判断及处理路径","checks":[{"outcome":"目标原有验收条件","result":"sufficient|gap|unverified","item_ids":[],"reason":"证据或缺口"}],"replace_dependencies_for":[]},"items":[],"edges":[],"context_snapshot":{}}
context_snapshot 记录实际查阅的版本和证据。用 "$MULTICA_CLI_PATH" goal plan-result --goal %s --generation %s --file <JSON路径> 提交。此运行只读，不进入产品代码写入队列。
`, input.Review.Phase, facts, uuidToString(generation.GoalID), uuidToString(generation.ID))
}

func (h *Handler) goalReviewResult(ctx context.Context, q *db.Queries, goal db.Goal, generation db.GoalPlanGeneration, input goalReviewInput, req GoalPlanResultRequest) ([]byte, error) {
	review := req.Review
	if review == nil || strings.TrimSpace(review.Summary) == "" || checkGoalPlanText(review.Summary, 2000) != nil || len(review.Checks) == 0 || len(review.Checks) > 30 {
		return nil, errors.New("review requires a summary and goal outcome checks")
	}
	switch review.Decision {
	case "covered", "planning_gap", "execution_issue", "insufficient_evidence":
	default:
		return nil, errors.New("invalid goal review decision")
	}
	plan, err := q.GetGoalPlan(ctx, db.GetGoalPlanParams{WorkspaceID: goal.WorkspaceID, GoalID: goal.ID})
	if err != nil {
		return nil, err
	}
	current, err := buildGoalReviewInput(ctx, q, goal, plan, input.OwnerID)
	if err != nil {
		return nil, err
	}
	if generation.BaseRevision != plan.Revision || input.Fingerprint != current.Fingerprint {
		return nil, errors.New("goal review facts changed; the latest context will be reviewed again")
	}
	var content GoalPlanContent
	if err := json.Unmarshal(plan.Content, &content); err != nil {
		return nil, err
	}
	known := make(map[string]int, len(content.Items))
	for i, item := range content.Items {
		known[item.ID] = i
	}
	gaps := 0
	affected := make(map[string]bool)
	outcomes := make(map[string]bool)
	for _, check := range review.Checks {
		outcome := strings.TrimSpace(check.Outcome)
		if outcome == "" || outcomes[outcome] || strings.TrimSpace(check.Reason) == "" ||
			checkGoalPlanText(check.Outcome, 1000) != nil || checkGoalPlanText(check.Reason, 2000) != nil || len(check.ItemIDs) > 20 {
			return nil, errors.New("review checks require a distinct outcome and an evidence or gap explanation")
		}
		outcomes[outcome] = true
		switch check.Result {
		case "sufficient":
			if len(check.ItemIDs) == 0 {
				return nil, errors.New("a sufficient outcome must identify the plan items that cover it")
			}
		case "gap":
			gaps++
		case "unverified":
		default:
			return nil, errors.New("invalid goal outcome check result")
		}
		if review.Decision == "covered" && check.Result != "sufficient" {
			return nil, errors.New("a covered review requires sufficient evidence for every outcome")
		}
		for _, id := range check.ItemIDs {
			if _, exists := known[id]; !exists {
				return nil, errors.New("review references an item outside the current plan")
			}
			if check.Result == "gap" {
				affected[id] = true
			}
		}
	}
	snapshot, err := normalizeGoalPlanSnapshot(req.ContextSnapshot)
	if err != nil {
		return nil, err
	}
	candidate := GoalPlanCandidate{Review: review, ContextSnapshot: snapshot}
	if review.Decision != "planning_gap" {
		if len(req.Items)+len(req.Edges)+len(review.ReplaceDependencies) > 0 {
			return nil, errors.New("only a planning gap may propose plan changes")
		}
		return json.Marshal(candidate)
	}
	if gaps == 0 || len(req.Items)+len(req.Edges)+len(review.ReplaceDependencies) == 0 {
		return nil, errors.New("a planning gap requires a specific gap and a local adjustment")
	}
	adjusted, err := applyGoalReviewPatch(content, known, affected, req)
	if err != nil {
		return nil, err
	}
	normalized, err := h.validateGoalPlanContent(ctx, q, goal.WorkspaceID, goal.ProjectID, adjusted.Items, adjusted.Edges, true)
	if err != nil {
		return nil, err
	}
	if reflect.DeepEqual(content, normalized) {
		return nil, errors.New("a planning gap requires an actual plan change")
	}
	candidate.Items, candidate.Edges = normalized.Items, normalized.Edges
	raw, err := json.Marshal(candidate)
	if err == nil && len(raw) > goalPlanMaxContentSize {
		return nil, errors.New("plan review candidate is too large")
	}
	return raw, err
}

func applyGoalReviewPatch(content GoalPlanContent, known map[string]int, affected map[string]bool, req GoalPlanResultRequest) (GoalPlanContent, error) {
	items := append([]GoalPlanItem(nil), content.Items...)
	changed := make(map[string]bool, len(req.Items))
	added := make(map[string]bool)
	for _, item := range req.Items {
		if changed[item.ID] || item.Binding.Type != "new" || item.Binding.IssueID != nil || item.Delivery == nil {
			return GoalPlanContent{}, errors.New("review changes require distinct unbound items with delivery contracts")
		}
		changed[item.ID] = true
		if i, exists := known[item.ID]; exists {
			if !affected[item.ID] || content.Items[i].Binding.IssueID != nil {
				return GoalPlanContent{}, errors.New("review cannot rewrite a bound or unaffected item")
			}
			items[i] = item
		} else {
			added[item.ID] = true
			items = append(items, item)
		}
	}
	replaced := make(map[string]bool, len(req.Review.ReplaceDependencies))
	for _, id := range req.Review.ReplaceDependencies {
		i, exists := known[id]
		if !exists || !affected[id] || content.Items[i].Binding.IssueID != nil || replaced[id] {
			return GoalPlanContent{}, errors.New("review can only replace dependencies of affected unbound items")
		}
		replaced[id] = true
	}
	edges := make([]GoalPlanEdge, 0, len(content.Edges)+len(req.Edges))
	for _, edge := range content.Edges {
		if !replaced[edge.DependentItemID] {
			edges = append(edges, edge)
		}
	}
	for _, edge := range req.Edges {
		if !added[edge.DependentItemID] && !replaced[edge.DependentItemID] {
			return GoalPlanContent{}, errors.New("review dependency changes must target a new item or an explicit replacement")
		}
		edges = append(edges, edge)
	}
	return GoalPlanContent{Items: items, Edges: edges}, nil
}

func goalPlanReviewView(ctx context.Context, q *db.Queries, plan db.GoalPlan) (*GoalPlanReviewView, error) {
	last, err := q.GetLatestGoalPlanReview(ctx, db.GetLatestGoalPlanReviewParams{WorkspaceID: plan.WorkspaceID, GoalID: plan.GoalID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	input, err := readGoalReviewInput(last.InputContext)
	if err != nil || input == nil {
		return nil, err
	}
	view := GoalPlanReviewView{GenerationID: uuidToString(last.ID), BaseRevision: last.BaseRevision, Phase: input.Phase, State: last.State}
	if last.BaseRevision != plan.Revision {
		view.State = "stale"
	} else if last.State == "queued" && last.TaskID.Valid {
		task, err := q.GetAgentTask(ctx, last.TaskID)
		if err == nil {
			switch task.Status {
			case "running":
				view.State = "running"
			case "failed", "cancelled", "completed":
				view.State = "failed"
			}
		}
	}
	if len(last.Candidate) > 0 {
		var candidate GoalPlanCandidate
		if err := json.Unmarshal(last.Candidate, &candidate); err != nil {
			return nil, err
		}
		view.Assessment = candidate.Review
	}
	return &view, nil
}

func (h *Handler) goalReviewAllows(ctx context.Context, q *db.Queries, goal db.Goal, plan db.GoalPlan, closeout bool) (bool, error) {
	last, err := q.GetLatestGoalPlanReview(ctx, db.GetLatestGoalPlanReviewParams{WorkspaceID: plan.WorkspaceID, GoalID: plan.GoalID})
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if last.State != "succeeded" || last.BaseRevision != plan.Revision {
		return false, nil
	}
	input, err := readGoalReviewInput(last.InputContext)
	if err != nil || input == nil {
		return false, err
	}
	if closeout && input.Phase != "closeout" {
		return false, nil
	}
	current, err := buildGoalReviewInput(ctx, q, goal, plan, input.OwnerID)
	if err != nil || current.Fingerprint != input.Fingerprint {
		return false, err
	}
	var candidate GoalPlanCandidate
	if err := json.Unmarshal(last.Candidate, &candidate); err != nil {
		return false, err
	}
	review := candidate.Review
	if review == nil {
		return false, nil
	}
	if review.Decision == "covered" {
		return true, nil
	}
	if closeout || input.Phase != "preflight" || review.Decision != "execution_issue" || len(review.Checks) == 0 {
		return false, nil
	}
	for _, check := range review.Checks {
		if check.Result != "sufficient" || len(check.ItemIDs) == 0 {
			return false, nil
		}
	}
	// Work already in delivery can address the recorded execution problem.
	// The separate backlog gate still holds new work under this review.
	progress, err := service.ReadGoalWorkProgress(ctx, q, goal, plan)
	return progress.Phase == "delivering", err
}
