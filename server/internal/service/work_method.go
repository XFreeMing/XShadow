package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/workmethod"
)

// WorkMethodCatalogFromOverrides merges workspace-stored template overrides with
// builtins. Callers that already read overrides once should use this helper rather
// than issuing a second catalog read.
func WorkMethodCatalogFromOverrides(raw []byte) ([]workmethod.Template, error) {
	var custom []workmethod.Template
	if len(raw) > 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		if err := json.Unmarshal(raw, &custom); err != nil {
			return nil, err
		}
	}
	return workmethod.Catalog(custom), nil
}

func WorkMethodCatalog(ctx context.Context, q *db.Queries, ws pgtype.UUID) ([]workmethod.Template, error) {
	raw, err := q.ListWorkMethodTemplates(ctx, ws)
	if err != nil {
		return nil, err
	}
	return WorkMethodCatalogFromOverrides(raw)
}

func WorkMethodGoalText(title, intent, criteria string) string {
	return strings.Join([]string{title, intent, criteria}, "\n")
}

// RefreshReadyWorkMethod runs in the creation or goal relation transaction with
// the issue row locked. A manual selection or submitted evidence freezes the run.
func RefreshReadyWorkMethod(ctx context.Context, q *db.Queries, issue db.Issue, goalText string) (db.Issue, error) {
	var previous workmethod.Run
	if len(issue.WorkMethodRun) > 0 {
		if err := json.Unmarshal(issue.WorkMethodRun, &previous); err != nil {
			return issue, err
		}
		if previous.State != "ready" {
			return issue, nil
		}
	}
	raw, err := readyWorkMethodJSON(ctx, q, issue.WorkspaceID, issue.Status, issue.Title, issue.Description.String, goalText)
	if err != nil {
		return issue, err
	}
	// PostgreSQL JSONB can reorder keys and insert whitespace. Marshal the
	// typed value in the same field order as the new choice for a no-op test.
	var prior []byte
	if len(issue.WorkMethodRun) > 0 {
		prior, err = json.Marshal(previous)
		if err != nil {
			return issue, err
		}
	}
	if bytes.Equal(raw, prior) {
		return issue, nil
	}
	return q.SaveIssueWorkMethodRun(ctx, db.SaveIssueWorkMethodRunParams{WorkspaceID: issue.WorkspaceID, ID: issue.ID, IssueRevision: issue.Revision, Run: raw})
}

// ReadyWorkMethodJSON seeds a new issue's work_method_run from the workspace catalog.
func ReadyWorkMethodJSON(ctx context.Context, q *db.Queries, ws pgtype.UUID, status, title, description, goalText string) ([]byte, error) {
	return readyWorkMethodJSON(ctx, q, ws, status, title, description, goalText)
}

func readyWorkMethodJSON(ctx context.Context, q *db.Queries, ws pgtype.UUID, status, title, description, goalText string) ([]byte, error) {
	category, err := issuestatus.CategoryWithError(ctx, q, ws, status)
	if err != nil {
		return nil, err
	}
	if category == issuestatus.CategoryDone || category == issuestatus.CategoryClosed {
		return nil, nil
	}
	methods, err := WorkMethodCatalog(ctx, q, ws)
	if err != nil {
		return nil, err
	}
	next := workmethod.Recommend(methods, title, description, goalText)
	if next.Method == nil {
		return nil, nil
	}
	return json.Marshal(next)
}

// Goal edits hold the goal row lock. Lock linked issues in stable order before
// refreshing their choices; a first submission racing this operation keeps its
// evidence, or detects the changed issue revision and retries.
func RefreshGoalWorkMethods(ctx context.Context, tx pgx.Tx, q *db.Queries, goal db.Goal, goalText string) error {
	rows, err := tx.Query(ctx, `SELECT i.id FROM issue i JOIN goal_issue gi ON gi.issue_id=i.id AND gi.workspace_id=i.workspace_id
		WHERE gi.workspace_id=$1 AND gi.goal_id=$2 AND (($3 AND i.work_method_run IS NULL) OR i.work_method_run->>'state'='ready')
		ORDER BY i.id FOR UPDATE OF i`, goal.WorkspaceID, goal.ID, goalText != "")
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[pgtype.UUID])
	if err != nil {
		return err
	}
	for _, id := range ids {
		issue, err := q.GetIssueInWorkspace(ctx, db.GetIssueInWorkspaceParams{WorkspaceID: goal.WorkspaceID, ID: id})
		if err != nil {
			return err
		}
		if _, err := RefreshReadyWorkMethod(ctx, q, issue, goalText); err != nil {
			return err
		}
	}
	return nil
}

// UpdateIssueWithWorkMethod uses the same locked old/new values for a content
// edit and its pending recommendation. The caller owns the transaction.
func UpdateIssueWithWorkMethod(ctx context.Context, q *db.Queries, ws pgtype.UUID, p db.UpdateIssueParams) (db.UpdateIssueRow, error) {
	current, err := q.LockIssueForDescriptionUpdate(ctx, db.LockIssueForDescriptionUpdateParams{WorkspaceID: ws, ID: p.ID})
	if err != nil {
		return db.UpdateIssueRow{}, err
	}
	if p.Status.Valid && len(current.WorkMethodRun) > 0 && IsMemberTodo(current) {
		category, err := issuestatus.CategoryWithError(ctx, q, ws, p.Status.String)
		if err != nil {
			return db.UpdateIssueRow{}, err
		}
		if category == issuestatus.CategoryDone {
			var run workmethod.Run
			if err := json.Unmarshal(current.WorkMethodRun, &run); err != nil {
				return db.UpdateIssueRow{}, err
			}
			if run.State == "ready" && run.Revision == 0 {
				// Remove only the recommendation in the same status write.
				p.ReplaceWorkMethod, p.ReadyMethod = true, nil
				return q.UpdateIssue(ctx, p)
			}
		}
	}
	title, description := current.Title, current.Description.String
	if p.Title.Valid {
		title = p.Title.String
	}
	if p.Description.Valid {
		description = p.Description.String
	}
	moving := current.ProjectID != p.ProjectID
	if title == current.Title && description == current.Description.String && !moving {
		return q.UpdateIssue(ctx, p)
	}
	if len(current.WorkMethodRun) > 0 {
		var run workmethod.Run
		if err := json.Unmarshal(current.WorkMethodRun, &run); err != nil {
			return db.UpdateIssueRow{}, err
		}
		if run.State != "ready" {
			return q.UpdateIssue(ctx, p)
		}
	}
	var goalText string
	if !moving {
		goal, err := q.GetIssueMethodGoal(ctx, db.GetIssueMethodGoalParams{WorkspaceID: ws, IssueID: p.ID})
		if err == nil {
			goalText = WorkMethodGoalText(goal.Title, goal.Intent, goal.AcceptanceCriteria)
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return db.UpdateIssueRow{}, err
		}
	}
	p.ReadyMethod, err = readyWorkMethodJSON(ctx, q, ws, current.Status, title, description, goalText)
	if err != nil {
		return db.UpdateIssueRow{}, err
	}
	p.ReplaceWorkMethod = true
	return q.UpdateIssue(ctx, p)
}
