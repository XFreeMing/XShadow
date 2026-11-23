package service

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// IsMemberTodo recognizes a member request with a durable source reference.
func IsMemberTodo(issue db.Issue) bool {
	if issue.AssigneeType.String != "member" || !issue.AssigneeID.Valid {
		return false
	}
	var metadata struct {
		Key    string `json:"member_todo_key"`
		Source string `json:"member_todo_source"`
	}
	if json.Unmarshal(issue.Metadata, &metadata) != nil {
		return false
	}
	return strings.TrimSpace(metadata.Key) != "" && strings.TrimSpace(metadata.Source) != ""
}

// Bind the choice to its requesting issue in the same project. Recheck this
// at evaluation: moving a card or changing its source invalidates the rule.
func memberChoiceSource(ctx context.Context, tx pgx.Tx, source, memberCard, workspace pgtype.UUID) (key, sourceRef, assignee string, err error) {
	err = tx.QueryRow(ctx, `SELECT m.metadata->>'member_todo_key', w.issue_prefix||'-'||s.number, m.assignee_id::text
 FROM issue m JOIN issue s ON s.id=$1 AND s.workspace_id=m.workspace_id
 JOIN workspace w ON w.id=m.workspace_id
 WHERE m.id=$2 AND m.workspace_id=$3 AND m.project_id IS NOT DISTINCT FROM s.project_id
 AND m.assignee_type='member' AND m.assignee_id IS NOT NULL AND coalesce(m.metadata->>'member_todo_key','')<>''
 AND upper(m.metadata->>'member_todo_source')=upper(w.issue_prefix||'-'||s.number)`, source, memberCard, workspace).Scan(&key, &sourceRef, &assignee)
	return
}

var memberOptionKey = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,47}$`)
var memberAnswerMarker = regexp.MustCompile(`(?s)<!--\s*multica-member-answer\s+(.+?)-->`)

func evaluateMemberChoice(ctx context.Context, tx pgx.Tx, source, memberCard, workspace pgtype.UUID, option string) (bool, string, map[string]any, error) {
	key, ref, user, err := memberChoiceSource(ctx, tx, source, memberCard, workspace)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, "", nil, nil
	}
	if err != nil {
		return false, "", nil, err
	}
	rows, err := tx.Query(ctx, `SELECT id,content FROM comment WHERE workspace_id=$1 AND issue_id=$2
 AND deleted_at IS NULL AND author_type='member' AND author_id=$3 AND type IN ('comment','progress_update')
 AND strpos(content,'multica-member-answer')>0 ORDER BY created_at DESC,id DESC`, workspace, memberCard, user)
	if err != nil {
		return false, "", nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id pgtype.UUID
		var content string
		if err := rows.Scan(&id, &content); err != nil {
			return false, "", nil, err
		}
		match := memberAnswerMarker.FindStringSubmatch(content)
		if match == nil {
			continue
		}
		var a struct {
			TodoKey     string `json:"todo_key"`
			AnswerID    string `json:"answer_id"`
			Answer      string `json:"answer"`
			OptionKey   string `json:"option_key"`
			AnsweredBy  string `json:"answered_by"`
			SourceIssue string `json:"source_issue"`
		}
		if json.Unmarshal([]byte(match[1]), &a) != nil || a.TodoKey != key || a.AnswerID != util.UUIDToString(id) || strings.TrimSpace(a.Answer) == "" {
			continue
		}
		if (a.AnsweredBy != "" && a.AnsweredBy != user) || (a.SourceIssue != "" && !strings.EqualFold(a.SourceIssue, ref)) {
			continue
		}
		// A later valid answer, including a deferral, supersedes an old approval.
		if a.OptionKey != option {
			return false, "", nil, nil
		}
		receipt := util.UUIDToString(id)
		return true, "answer:" + receipt, map[string]any{"issue_id": util.UUIDToString(memberCard), "status": "done", "answer_comment_id": receipt, "option_key": option}, nil
	}
	return false, "", nil, rows.Err()
}

func hasMemberChoice(raw []byte) bool {
	var c WakeupCondition
	return len(raw) > 0 && json.Unmarshal(raw, &c) == nil && c.MemberOption != ""
}
