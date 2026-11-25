package handler

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// issueStatusPreparedEnqueue is the internal Rust contract for atomically
// enqueueing a trusted backlog promotion alongside the status row write.
type issueStatusPreparedEnqueue struct {
	TaskID               string          `json:"task_id"`
	AgentID              string          `json:"agent_id"`
	RuntimeID            string          `json:"runtime_id"`
	IssueID              string          `json:"issue_id"`
	Priority             int32           `json:"priority"`
	WorkflowStep         string          `json:"workflow_step"`
	HeadSha              string          `json:"head_sha,omitempty"`
	OriginatorUserID     string          `json:"originator_user_id"`
	AccountableUserID    string          `json:"accountable_user_id"`
	OriginatorSource     string          `json:"originator_source"`
	DelegatedFromTaskID  string          `json:"delegated_from_task_id,omitempty"`
	RuleVersionID        string          `json:"rule_version_id,omitempty"`
	TriggerEvidenceKind  string          `json:"trigger_evidence_kind,omitempty"`
	TriggerEvidenceRefID string          `json:"trigger_evidence_ref_id,omitempty"`
	HandoffNote          string          `json:"handoff_note,omitempty"`
	RuntimeMcpOverlay    json.RawMessage `json:"runtime_mcp_overlay,omitempty"`
	RuntimeConnectedApps json.RawMessage `json:"runtime_connected_apps,omitempty"`
	QueueKeyHeadSha      string          `json:"queue_key_head_sha"`
}

func marshalIssueStatusPreparedEnqueue(prep service.IssuePreparedAgentEnqueue) ([]byte, error) {
	task := prep.Task
	wire := issueStatusPreparedEnqueue{
		TaskID:            uuidToString(task.ID),
		AgentID:           uuidToString(task.AgentID),
		RuntimeID:         uuidToString(task.RuntimeID),
		IssueID:           uuidToString(task.IssueID),
		Priority:          task.Priority,
		WorkflowStep:      textToString(task.WorkflowStep),
		OriginatorUserID:  uuidToString(task.OriginatorUserID),
		AccountableUserID: uuidToString(task.AccountableUserID),
		QueueKeyHeadSha:   prep.QueueKeyHeadSha,
	}
	if task.HeadSha.Valid {
		wire.HeadSha = task.HeadSha.String
	}
	if task.OriginatorSource.Valid {
		wire.OriginatorSource = task.OriginatorSource.String
	}
	if task.DelegatedFromTaskID.Valid {
		wire.DelegatedFromTaskID = uuidToString(task.DelegatedFromTaskID)
	}
	if task.RuleVersionID.Valid {
		wire.RuleVersionID = uuidToString(task.RuleVersionID)
	}
	if task.TriggerEvidenceKind.Valid {
		wire.TriggerEvidenceKind = task.TriggerEvidenceKind.String
	}
	if task.TriggerEvidenceRefID.Valid {
		wire.TriggerEvidenceRefID = uuidToString(task.TriggerEvidenceRefID)
	}
	if task.HandoffNote.Valid {
		wire.HandoffNote = task.HandoffNote.String
	}
	if len(task.RuntimeMcpOverlay) > 0 {
		wire.RuntimeMcpOverlay = json.RawMessage(task.RuntimeMcpOverlay)
	}
	if len(task.RuntimeConnectedApps) > 0 {
		wire.RuntimeConnectedApps = json.RawMessage(task.RuntimeConnectedApps)
	}
	return json.Marshal(wire)
}

func textToString(t pgtype.Text) string {
	if !t.Valid {
		return ""
	}
	return t.String
}

func rustStatusQueuedTaskValid(
	ctx context.Context,
	q *db.Queries,
	receipt string,
	prep service.IssuePreparedAgentEnqueue,
) bool {
	if receipt == "" {
		return false
	}
	taskID, err := util.ParseUUID(receipt)
	if err != nil {
		return false
	}
	row, err := q.GetAgentTask(ctx, taskID)
	if err != nil {
		return false
	}
	want := prep.Task
	if row.ID != want.ID || row.AgentID != want.AgentID || row.IssueID != want.IssueID {
		return false
	}
	if row.RuntimeID != want.RuntimeID {
		return false
	}
	if row.OriginatorUserID != want.OriginatorUserID || row.AccountableUserID != want.AccountableUserID {
		return false
	}
	if want.OriginatorSource.Valid && row.OriginatorSource.String != want.OriginatorSource.String {
		return false
	}
	return true
}

func rustStatusCoalescedTaskValid(
	ctx context.Context,
	q *db.Queries,
	receipt string,
	prep service.IssuePreparedAgentEnqueue,
) bool {
	taskID, err := util.ParseUUID(receipt)
	if err != nil {
		return false
	}
	row, err := q.GetAgentTask(ctx, taskID)
	if err != nil {
		return false
	}
	// An existing task may belong to another initiator or have been claimed since
	// the promotion committed. Its issue and agent are the stable ownership keys.
	return row.IssueID == prep.Task.IssueID && row.AgentID == prep.Task.AgentID && row.ID != prep.Task.ID
}
