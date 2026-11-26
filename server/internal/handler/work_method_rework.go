package handler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/issueposition"
	"github.com/multica-ai/multica/server/internal/issueworkflow"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/workmethod"
)

func (h *Handler) createReworkAnalysis(ctx context.Context, tx pgx.Tx, parent db.Issue, run workmethod.Run, cycle workmethod.ReworkCycle, policy service.IssueCountPolicy, actorKind string, actorID, requester pgtype.UUID, source *db.AgentTaskQueue) (db.Issue, db.AgentTaskQueue, error) {
	var emptyIssue db.Issue
	var emptyTask db.AgentTaskQueue
	q := h.Queries.WithTx(tx)
	id := pgtype.UUID{Bytes: uuid.NewSHA1(uuid.UUID(parent.ID.Bytes), []byte(fmt.Sprintf("rework-analysis/%d", cycle.FirstIndex))), Valid: true}
	if _, err := q.GetIssue(ctx, id); !errors.Is(err, pgx.ErrNoRows) {
		return emptyIssue, emptyTask, err
	}
	goal, err := q.GetIssueMethodGoal(ctx, db.GetIssueMethodGoalParams{WorkspaceID: parent.WorkspaceID, IssueID: parent.ID})
	if errors.Is(err, pgx.ErrNoRows) {
		return emptyIssue, emptyTask, nil
	}
	if err != nil {
		return emptyIssue, emptyTask, err
	}
	squad, err := q.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{WorkspaceID: parent.WorkspaceID, ID: h.ReworkAnalysisSquadID})
	if errors.Is(err, pgx.ErrNoRows) {
		return emptyIssue, emptyTask, nil
	}
	if err != nil {
		return emptyIssue, emptyTask, err
	}
	if squad.ArchivedAt.Valid || !squad.LeaderID.Valid {
		return emptyIssue, emptyTask, nil
	}
	def, _, err := issueworkflow.ForProject(ctx, q, parent.WorkspaceID, parent.ProjectID)
	if err != nil {
		return emptyIssue, emptyTask, err
	}
	status := issueworkflow.StartStatus
	if err := issueworkflow.CheckStatus(def, status); err != nil {
		return emptyIssue, emptyTask, err
	}
	permission := service.CheckGoalExecutionRecipient(ctx, q, parent, squad.LeaderID, requester)
	if permission != nil && !errors.Is(permission, service.ErrWakeupForbidden) && !errors.Is(permission, service.ErrProjectAccessDenied) {
		return emptyIssue, emptyTask, permission
	}
	number, err := service.AllocateIssueNumber(ctx, q, parent.WorkspaceID, policy)
	if err != nil {
		return emptyIssue, emptyTask, err
	}
	position, err := issueposition.NextTopPosition(ctx, tx, parent.WorkspaceID, status)
	if err != nil {
		return emptyIssue, emptyTask, err
	}
	step := run.Method.Steps[cycle.ReviewStep].Name
	description := reworkAnalysisDescription(parent, goal.Title, step, cycle)
	params := db.CreateIssueParams{
		ID: id, WorkspaceID: parent.WorkspaceID, ProjectID: parent.ProjectID, ParentIssueID: parent.ID,
		Title: fmt.Sprintf("重复返工归因 · %s", step), Description: pgtype.Text{String: description, Valid: true},
		Status: status, Priority: parent.Priority, CreatorType: actorKind, CreatorID: actorID, Number: number, Position: position,
	}
	if permission == nil {
		params.AssigneeType = pgtype.Text{String: "squad", Valid: true}
		params.AssigneeID = squad.ID
	}
	issue, err := q.CreateIssue(ctx, params)
	if err != nil {
		return emptyIssue, emptyTask, err
	}
	if _, err = q.LinkGoalIssue(ctx, db.LinkGoalIssueParams{WorkspaceID: parent.WorkspaceID, GoalID: goal.ID, IssueID: issue.ID}); err != nil {
		return emptyIssue, emptyTask, err
	}
	if permission != nil {
		return issue, emptyTask, nil
	}
	// A temporarily unavailable runtime must not roll back the investigation or the return.
	dispatch, err := tx.Begin(ctx)
	if err != nil {
		return emptyIssue, emptyTask, err
	}
	defer dispatch.Rollback(ctx)
	task, err := h.TaskService.CreateWorkMethodHandoff(ctx, h.Queries.WithTx(dispatch), issue, squad.LeaderID, squad.ID, 0,
		"请原因分析小队队长接手本子任务，按正文核查原事项和两次退回依据，并留下可复验的预防方案。", requester, source, pgtype.UUID{})
	if err != nil {
		slog.Warn("rework analysis awaiting assignment", "issue_id", uuidToString(issue.ID), "error", err)
		return issue, emptyTask, nil
	}
	if err := dispatch.Commit(ctx); err != nil {
		return emptyIssue, emptyTask, err
	}
	return issue, task, nil
}

func reworkAnalysisDescription(parent db.Issue, goal, step string, cycle workmethod.ReworkCycle) string {
	var text strings.Builder
	fmt.Fprintf(&text, "## 用户成果\n查清「%s」在「%s」反复退回的原因，给出一项可复验的改动，减少目标「%s」后续故事的重复返工。\n\n原事项：`%s`。本子任务由原因分析小队负责。\n", parent.Title, step, goal, uuidToString(parent.ID))
	for _, evidence := range []struct {
		label string
		event workmethod.Event
	}{{"首次退回", cycle.First}, {"再次退回", cycle.Latest}} {
		fmt.Fprintf(&text, "\n### %s · %s\n\n%s\n", evidence.label, evidence.event.At, truncateRunes(evidence.event.Evidence, 1000))
		for _, finding := range evidence.event.Findings[:min(len(evidence.event.Findings), 2)] {
			fmt.Fprintf(&text, "\n- %s %s。预期：%s。实际：%s。复现：%s\n", finding.CriterionID, finding.CaseID,
				truncateRunes(finding.Expected, 300), truncateRunes(finding.Actual, 300), truncateRunes(finding.Evidence, 300))
		}
	}
	text.WriteString(`
完整回执、附件和后续退回见原事项的工作方法记录。

## 分析与验收

1. 对照两次退回、原标准和实际候选，复现同一个失败或说明两次退回的区别。列出最早出现偏差的环节及其证据入口。
2. 先验证设计假设。检查模块职责、限界上下文、实体身份、值对象、业务不变量和上下游接口。说明若当时采用修正后的设计，两次退回中的哪些能提前被发现。设计证据不足时，继续核查用例、运行环境、权限和交接。
3. 写明根因、尚待证实的判断、最小预防改动、接手角色和验证用例。用原失败样本证实预防措施能在送审前识别该问题，或交代尚缺的证据。把结论和本卡入口写回原事项，再关闭本子任务。

本轮只做归因和设计复核。保留原事项的标准、候选、负责人和交付授权。需要落实预防改动时交回原负责人决定。
`)
	return text.String()
}
