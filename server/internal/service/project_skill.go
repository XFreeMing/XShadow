package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/projectaccess"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// LoadIssueProjectSkills supplies released project skills only to issue runs
// in that project. A nil IDs slice loads the catalog at claim time.
func (s *TaskService) LoadIssueProjectSkills(ctx context.Context, task db.AgentTaskQueue, issue db.Issue, agent db.Agent, ids []pgtype.UUID) ([]AgentSkillData, error) {
	if task.ChatSessionID.Valid || !issue.ProjectID.Valid || issue.ID != task.IssueID ||
		issue.WorkspaceID != agent.WorkspaceID || agent.RuntimeID != task.RuntimeID || agent.ID != task.AgentID {
		return nil, nil
	}
	hidden, err := projectaccess.ForPrincipals(ctx, s.Queries, issue.WorkspaceID, projectaccess.RunPrincipals(task, agent.OwnerID)...)
	if err != nil {
		return nil, fmt.Errorf("resolve project skill access: %w", err)
	}
	if !hidden.CanSeeProject(issue.ProjectID) {
		return nil, nil
	}
	var skills []db.Skill
	if ids == nil {
		skills, err = s.Queries.ListProjectSkills(ctx, db.ListProjectSkillsParams{WorkspaceID: issue.WorkspaceID, ProjectID: issue.ProjectID})
	} else {
		skills, err = s.Queries.ListProjectSkillsByIDs(ctx, db.ListProjectSkillsByIDsParams{
			WorkspaceID: issue.WorkspaceID, ProjectID: issue.ProjectID, SkillIds: ids,
		})
	}
	if err != nil || len(skills) == 0 {
		return nil, err
	}
	return s.skillsWithFiles(ctx, skills)
}

func (s *TaskService) ResolveIssueProjectSkills(ctx context.Context, task db.AgentTaskQueue, ids []pgtype.UUID) ([]AgentSkillData, error) {
	if !task.IssueID.Valid || task.ChatSessionID.Valid || len(ids) == 0 {
		return nil, nil
	}
	issue, err := s.Queries.GetIssue(ctx, task.IssueID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	agent, err := s.Queries.GetAgent(ctx, task.AgentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.LoadIssueProjectSkills(ctx, task, issue, agent, ids)
}
