package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/pkg/acceptance"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/workmethod"
)

type verifiedProjectLesson struct {
	traceRevision int32
	commit        string
	raw           []byte
}

func verifyProjectLesson(issue db.Issue, run workmethod.Run, lesson *workmethod.ProjectLesson, actor string) (*verifiedProjectLesson, error) {
	if !issue.ProjectID.Valid || !run.CanProposeProjectLesson() || len(run.History) == 0 {
		return nil, errors.New("project_lesson requires independent final approval of a project development issue")
	}
	var submitter string
	for i := len(run.History) - 1; i >= 0; i-- {
		event := run.History[i]
		if event.Action == "advance" && event.Step == run.Step {
			submitter = event.Actor
			break
		}
	}
	if submitter == "" || submitter == actor {
		return nil, errors.New("the final reviewer must be different from the submitter")
	}
	if err := lesson.Validate(); err != nil {
		return nil, err
	}
	trace, err := acceptance.Decode(issue.AcceptanceCriteria)
	if err != nil {
		return nil, err
	}
	for _, criterion := range trace.Criteria {
		if criterion.ID != lesson.CriterionID {
			continue
		}
		for _, test := range criterion.Cases {
			if test.ID != lesson.CaseID || test.Retired != nil || len(test.Runs) == 0 {
				continue
			}
			last := test.Runs[len(test.Runs)-1]
			if last.Result != "pass" || last.Version != len(criterion.Versions) {
				break
			}
			for _, implementation := range criterion.Implementations {
				if implementation.Version != last.Version || implementation.Commit != last.Candidate || implementation.Commit == "" {
					continue
				}
				raw, err := json.Marshal(lesson)
				return &verifiedProjectLesson{traceRevision: int32(trace.Revision), commit: implementation.Commit, raw: raw}, err
			}
		}
	}
	return nil, errors.New("project_lesson must reference a current passing acceptance case on the verified candidate")
}

type projectSkillEntry struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type projectSkillProposalView struct {
	IssueID        string                   `json:"issue_id"`
	IssueNumber    int32                    `json:"issue_number"`
	MethodRevision int32                    `json:"method_revision"`
	Candidate      string                   `json:"candidate_commit"`
	Lesson         workmethod.ProjectLesson `json:"lesson"`
	State          string                   `json:"state"`
	SkillID        *string                  `json:"skill_id"`
	SkillVersion   *int32                   `json:"skill_version"`
}

type projectSkillsView struct {
	CanManage bool                       `json:"can_manage"`
	Skills    []projectSkillEntry        `json:"skills"`
	Proposals []projectSkillProposalView `json:"proposals"`
}

func (h *Handler) requireProjectSkillRunScope(w http.ResponseWriter, r *http.Request, workspaceID, projectID pgtype.UUID) bool {
	actor, actorID := h.resolveActor(r, requestUserID(r), uuidToString(workspaceID))
	if actor != "agent" {
		return true
	}
	task, ok := h.taskFromRequestHeader(r)
	if ok && task.AgentID == parseUUIDOrInvalid(actorID) && task.IssueID.Valid && !task.ChatSessionID.Valid {
		issue, err := h.Queries.GetIssue(r.Context(), task.IssueID)
		if err == nil && issue.WorkspaceID == workspaceID && issue.ProjectID == projectID {
			return true
		}
	}
	writeError(w, http.StatusNotFound, "project skill not found")
	return false
}

func (h *Handler) ListProjectSkills(w http.ResponseWriter, r *http.Request) {
	project, ok := h.loadProjectForResource(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	if !h.requireProjectSkillRunScope(w, r, project.WorkspaceID, project.ID) {
		return
	}
	member, ok := h.requireWorkspaceMember(w, r, uuidToString(project.WorkspaceID), "project not found")
	if !ok {
		return
	}
	view := projectSkillsView{
		CanManage: !isMachineCredentialActor(r) && canManageProjectAccess(member, project),
		Skills:    []projectSkillEntry{}, Proposals: []projectSkillProposalView{},
	}
	ctx := r.Context()
	skills, err := h.Queries.ListProjectSkills(ctx, db.ListProjectSkillsParams{WorkspaceID: project.WorkspaceID, ProjectID: project.ID})
	if err != nil {
		writeError(w, 500, "failed to read project skills")
		return
	}
	for _, skill := range skills {
		view.Skills = append(view.Skills, projectSkillEntry{
			ID: uuidToString(skill.ID), Name: skill.Name,
			Description: skill.Description,
		})
	}
	if view.CanManage {
		rows, err := h.Queries.ListProjectSkillProposals(ctx, db.ListProjectSkillProposalsParams{WorkspaceID: project.WorkspaceID, ProjectID: project.ID})
		if err != nil {
			writeError(w, 500, "failed to read project skill proposals")
			return
		}
		for _, row := range rows {
			viewRow := projectSkillProposalView{
				IssueID: uuidToString(row.IssueID), IssueNumber: row.IssueNumber,
				MethodRevision: row.MethodRevision, Candidate: row.CandidateCommit,
				State: "pending", SkillID: uuidToPtr(row.SkillID),
			}
			if row.SkillVersion.Valid {
				viewRow.SkillVersion = &row.SkillVersion.Int32
			}
			if err := json.Unmarshal(row.Lesson, &viewRow.Lesson); err != nil {
				writeError(w, 500, "failed to decode project skill proposal")
				return
			}
			switch {
			case row.SkillID.Valid:
				viewRow.State = "published"
			case row.DecidedAt.Valid:
				viewRow.State = "dismissed"
			}
			view.Proposals = append(view.Proposals, viewRow)
		}
	}
	writeJSON(w, 200, view)
}

func (h *Handler) DecideProjectSkill(w http.ResponseWriter, r *http.Request) {
	project, ok := h.loadProjectForResource(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	member, ok := h.requireWorkspaceMember(w, r, uuidToString(project.WorkspaceID), "project not found")
	if !ok {
		return
	}
	if isMachineCredentialActor(r) || !canManageProjectAccess(member, project) {
		writeError(w, 403, "only a project maintainer can decide skill proposals")
		return
	}
	issueID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "issueId"), "issue id")
	if !ok {
		return
	}
	revision, err := strconv.ParseInt(chi.URLParam(r, "revision"), 10, 32)
	if err != nil || revision <= 0 {
		writeError(w, 400, "invalid method revision")
		return
	}
	var req struct {
		Decision string `json:"decision"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req); err != nil ||
		(req.Decision != "publish" && req.Decision != "dismiss") {
		writeError(w, 400, "decision must be publish or dismiss")
		return
	}
	ctx := r.Context()
	tx, err := h.TxStarter.Begin(ctx)
	if err != nil {
		writeError(w, 500, "failed to start project skill decision")
		return
	}
	defer tx.Rollback(ctx)
	q := h.Queries.WithTx(tx)
	if _, err = q.LockProjectForDelete(ctx, db.LockProjectForDeleteParams{WorkspaceID: project.WorkspaceID, ID: project.ID}); err != nil {
		writeError(w, 409, "project changed; reload the proposal")
		return
	}
	project, err = q.GetProjectInWorkspace(ctx, db.GetProjectInWorkspaceParams{WorkspaceID: project.WorkspaceID, ID: project.ID})
	if err != nil || !canManageProjectAccess(member, project) {
		writeError(w, 403, "project maintenance access changed")
		return
	}
	if _, err = q.LockIssueForDelete(ctx, db.LockIssueForDeleteParams{WorkspaceID: project.WorkspaceID, ID: issueID}); err != nil {
		writeError(w, 404, "source issue not found")
		return
	}
	issue, err := q.GetIssue(ctx, issueID)
	if err != nil || issue.WorkspaceID != project.WorkspaceID || issue.ProjectID != project.ID {
		writeError(w, 404, "source issue not found")
		return
	}
	proposal, err := q.LockProjectSkillProposal(ctx, db.LockProjectSkillProposalParams{
		WorkspaceID: project.WorkspaceID, ProjectID: project.ID, IssueID: issue.ID, MethodRevision: int32(revision),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 404, "project skill proposal not found")
		return
	}
	if err != nil {
		writeError(w, 500, "failed to read project skill proposal")
		return
	}
	if proposal.DecidedAt.Valid {
		if proposal.SkillID.Valid == (req.Decision == "publish") {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeError(w, 409, "the proposal has already been reviewed")
		return
	}
	decision := db.DecideProjectSkillProposalParams{
		WorkspaceID: project.WorkspaceID, ProjectID: project.ID,
		IssueID: issue.ID, MethodRevision: proposal.MethodRevision, DecidedBy: member.UserID,
	}
	if req.Decision == "publish" {
		if !projectSkillSourceCurrent(issue, proposal) {
			writeError(w, 409, "source acceptance changed; publish a proposal from the current review")
			return
		}
		decision.SkillID, decision.SkillVersion, err = publishProjectLesson(ctx, q, proposal, member.UserID)
		if err != nil {
			writeError(w, 500, "failed to publish project skill")
			return
		}
	}
	if err = q.DecideProjectSkillProposal(ctx, decision); err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		writeError(w, 500, "failed to save project skill decision")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func projectSkillSourceCurrent(issue db.Issue, proposal db.ProjectSkillProposal) bool {
	var run workmethod.Run
	trace, err := acceptance.Decode(issue.AcceptanceCriteria)
	return err == nil && int32(trace.Revision) == proposal.TraceRevision &&
		json.Unmarshal(issue.WorkMethodRun, &run) == nil && run.State == "closed" && int32(run.Revision) == proposal.MethodRevision
}

func publishProjectLesson(ctx context.Context, q *db.Queries, proposal db.ProjectSkillProposal, userID pgtype.UUID) (pgtype.UUID, pgtype.Int4, error) {
	var lesson workmethod.ProjectLesson
	if err := json.Unmarshal(proposal.Lesson, &lesson); err != nil {
		return pgtype.UUID{}, pgtype.Int4{}, err
	}
	name := lesson.Name
	description := sanitizeNullBytes(strings.TrimSpace(lesson.When))
	// JSON-quoted YAML scalars preserve punctuation and cannot break frontmatter.
	quotedDescription, _ := json.Marshal(description)
	var content strings.Builder
	fmt.Fprintf(&content, "---\nname: %s\ndescription: %s\n---\n\n# %s\n\n## When to use\n\n%s\n\n## Steps\n\n",
		name, quotedDescription, lesson.Name, description)
	for i, step := range lesson.Steps {
		fmt.Fprintf(&content, "%d. %s\n", i+1, strings.TrimSpace(step))
	}
	fmt.Fprintf(&content, "\n## Verify\n\n%s\n\n## Avoid\n\n%s\n\n## Accepted evidence\n\nIssue %s, method revision %d, acceptance case %s/%s, candidate %s.\n",
		strings.TrimSpace(lesson.Verify), strings.TrimSpace(lesson.Avoid), uuidToString(proposal.IssueID),
		proposal.MethodRevision, lesson.CriterionID, lesson.CaseID, proposal.CandidateCommit)

	skill, err := q.GetProjectSkillByName(ctx, db.GetProjectSkillByNameParams{WorkspaceID: proposal.WorkspaceID, ProjectID: proposal.ProjectID, Name: name})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		created, createErr := createSkillWithFilesInTx(ctx, q, skillCreateInput{
			WorkspaceID: proposal.WorkspaceID, ProjectID: proposal.ProjectID, CreatorID: userID,
			Name: name, Description: description, Content: content.String(),
		})
		if createErr != nil {
			return pgtype.UUID{}, pgtype.Int4{}, createErr
		}
		skill.ID, err = util.ParseUUID(created.ID)
	case err == nil:
		_, err = q.UpdateSkill(ctx, db.UpdateSkillParams{
			ID: skill.ID, Description: pgtype.Text{String: description, Valid: true},
			Content: pgtype.Text{String: sanitizeNullBytes(content.String()), Valid: true},
		})
		if err == nil {
			_, err = recordSkillVersion(ctx, q, skill.ID, skillVersionRecord{Source: skillVersionSourceUpdate, CreatedBy: userID})
		}
	}
	if err != nil {
		return pgtype.UUID{}, pgtype.Int4{}, err
	}
	latest, err := q.GetLatestSkillVersion(ctx, skill.ID)
	return skill.ID, pgtype.Int4{Int32: latest.Version, Valid: err == nil}, err
}
