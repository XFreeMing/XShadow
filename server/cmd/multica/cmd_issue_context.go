package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/pkg/acceptance"
)

// `multica issue context` reads one task's working context in a single call:
// the upper goal it is formally linked to, the story layer read from the issue
// itself, and the sources behind every value. It also states the task's
// effective goal, so a task with no upper goal is not left as a bare "none".
//
// The fixed daily server has no single-call goal endpoint, so this command
// composes only long-standing read APIs: the issue, the visible goals of the
// issue's own project (or standalone goals when it has none), each goal's
// linked issues, the goal detail, the goal plan (for a bound contribution),
// and the issue's recent feedback. The resolution is deliberately literal — a
// goal counts only when the server lists the issue among that goal's links.
// Titles, body links and search are never used to guess an association.
//
// A failure anywhere in that walk is an error, never "no goal": an issue whose
// project or goals cannot be read must not be presented as unattached. A
// successful response with a null collection instead of an array is treated as
// a structural failure for the same reason.

const (
	issueContextVersion          = 2
	issueContextPageSize         = 100
	issueContextFeedbackThreads  = 3
	issueContextTextOutputFormat = "text"
	issueContextJSONOutputFormat = "json"
)

var issueContextCmd = &cobra.Command{
	Use:   "context <issue>",
	Short: "Show the task's goal, story contribution and acceptance criteria",
	Long: `Read a task's current context in one call: the upper goal it is linked to,
the effective goal (the story itself when nothing is linked), the story layer
taken from the issue, and the source of each value.

The goal layer is resolved from the server's own records: the issue's project
(standalone goals when it has no project) → the visible goals of that project →
each goal's linked-issue list. An issue that is not listed under a goal has no
upper goal and its effective goal is the story itself. A permission, service,
or malformed-response failure is reported as an error instead of being
presented as "no goal".

The story layer carries the issue identifier and revision, the full description
as the evidence of record, an explicit result/contribution section when the
body has one, the acceptance criteria written in the body, and recent feedback.
When a body has no dedicated contribution section, the contribution comes from
the formal plan item bound to this issue, or from the body's own user-story
statement ("shown by the story result"); it is never inferred from the title or
another goal's text. The goal's acceptance criteria and the story's acceptance
criteria are reported separately.

Every read uses the current task's identity (MULTICA_TOKEN, MULTICA_SERVER_URL,
MULTICA_WORKSPACE_ID) and changes nothing on the server.`,
	Args: exactArgs(1),
	RunE: runIssueContext,
}

func init() {
	issueCmd.AddCommand(issueContextCmd)
	issueContextCmd.Flags().String("output", issueContextJSONOutputFormat, "Output format: json or text")
}

// issueContextProject distinguishes an absent project_id field from an
// explicit null. The server always sends the field; a missing one is a
// contract violation, while null means the resource has no project.
type issueContextProject struct {
	Present bool
	Value   *string
}

func (p *issueContextProject) UnmarshalJSON(data []byte) error {
	p.Present = true
	trimmed := bytes.TrimSpace(data)
	if bytes.Equal(trimmed, []byte("null")) {
		p.Value = nil
		return nil
	}
	var value string
	if err := json.Unmarshal(trimmed, &value); err != nil {
		return fmt.Errorf("project_id is not a string or null: %w", err)
	}
	p.Value = &value
	return nil
}

// filterValue returns the /api/goals?project_id= value: "none" for an issue
// with no project, otherwise the project UUID. An absent field or an empty
// string is rejected instead of being read as "no project".
func (p issueContextProject) filterValue() (string, error) {
	if !p.Present {
		return "", fmt.Errorf("the server response has no project_id field")
	}
	if p.Value == nil {
		return "none", nil
	}
	value := strings.TrimSpace(*p.Value)
	if value == "" {
		return "", fmt.Errorf("the server returned an empty project_id")
	}
	return value, nil
}

func (p issueContextProject) display() *string {
	if p.Value == nil {
		return nil
	}
	value := strings.TrimSpace(*p.Value)
	return &value
}

func (p issueContextProject) empty() bool {
	return p.Value == nil || strings.TrimSpace(*p.Value) == ""
}

// issueContextIssue is the subset of GET /api/issues/{id} this command reads.
type issueContextIssue struct {
	Trace       *acceptance.Trace   `json:"acceptance_trace"`
	ID          string              `json:"id"`
	Identifier  string              `json:"identifier"`
	Number      int64               `json:"number"`
	Title       string              `json:"title"`
	Status      string              `json:"status"`
	ProjectID   issueContextProject `json:"project_id"`
	Revision    int64               `json:"revision"`
	UpdatedAt   string              `json:"updated_at"`
	Description *string             `json:"description"`
}

// issueContextGoal is the goal shape used for both the list page and the
// detail read. The list response carries the same fields, so one type serves
// both and the detail read only refreshes the values.
type issueContextGoal struct {
	ID                 string              `json:"id"`
	Title              string              `json:"title"`
	Status             string              `json:"status"`
	Intent             string              `json:"intent"`
	Scope              string              `json:"scope"`
	AcceptanceCriteria string              `json:"acceptance_criteria"`
	ProjectID          issueContextProject `json:"project_id"`
	UpdatedAt          string              `json:"updated_at"`
}

type issueContextGoalResolution struct {
	Method            string `json:"method"`
	ProjectFilter     string `json:"project_filter"`
	GoalsScanned      int    `json:"goals_scanned"`
	GoalIssuesScanned int    `json:"goal_issues_scanned"`
}

type issueContextGoalLayer struct {
	State              string                     `json:"state"`
	Reason             string                     `json:"reason,omitempty"`
	Detail             string                     `json:"detail,omitempty"`
	ID                 string                     `json:"id,omitempty"`
	Title              string                     `json:"title,omitempty"`
	Status             string                     `json:"status,omitempty"`
	Intent             string                     `json:"intent,omitempty"`
	Scope              string                     `json:"scope,omitempty"`
	AcceptanceCriteria string                     `json:"acceptance_criteria,omitempty"`
	ProjectID          *string                    `json:"project_id"`
	UpdatedAt          string                     `json:"updated_at,omitempty"`
	Resolution         issueContextGoalResolution `json:"resolution"`
}

// issueContextEffectiveGoal names the goal a task is working toward right now:
// its formally linked upper goal, or the story itself when nothing is linked.
type issueContextEffectiveGoal struct {
	Layer      string `json:"layer"`
	ID         string `json:"id"`
	Identifier string `json:"identifier,omitempty"`
	Title      string `json:"title"`
	Revision   int64  `json:"revision,omitempty"`
	UpdatedAt  string `json:"updated_at,omitempty"`
	Basis      string `json:"basis"`
	Note       string `json:"note,omitempty"`
}

type issueContextBodyLayer struct {
	State     string `json:"state"`
	Text      string `json:"text"`
	SHA256    string `json:"sha256,omitempty"`
	LineCount int    `json:"line_count"`
}

type issueContextValue struct {
	State     string `json:"state"`
	Text      string `json:"text,omitempty"`
	Basis     string `json:"basis"`
	Section   string `json:"section,omitempty"`
	LineStart int    `json:"line_start,omitempty"`
	LineEnd   int    `json:"line_end,omitempty"`
	Source    string `json:"source,omitempty"`
	Note      string `json:"note,omitempty"`
}

type issueContextAcceptanceItem struct {
	CriterionID string `json:"criterion_id,omitempty"`
	Version     int    `json:"version,omitempty"`
	Text        string `json:"text"`
	Checked     *bool  `json:"checked,omitempty"`
	Line        int    `json:"line,omitempty"`
}

type issueContextAcceptanceSection struct {
	Heading   string `json:"heading"`
	LineStart int    `json:"line_start"`
	LineEnd   int    `json:"line_end"`
}

type issueContextAcceptance struct {
	Structured *acceptance.Trace               `json:"structured,omitempty"`
	State      string                          `json:"state"`
	Items      []issueContextAcceptanceItem    `json:"items"`
	Sections   []issueContextAcceptanceSection `json:"sections"`
	Source     string                          `json:"source"`
	Reason     string                          `json:"reason,omitempty"`
}

type issueContextFeedbackItem struct {
	ID         string `json:"id"`
	CreatedAt  string `json:"created_at"`
	AuthorType string `json:"author_type"`
	Content    string `json:"content"`
}

type issueContextFeedback struct {
	State   string                     `json:"state"`
	Threads int                        `json:"threads"`
	Items   []issueContextFeedbackItem `json:"items"`
	Source  string                     `json:"source"`
}

type issueContextStoryLayer struct {
	Identifier   string                 `json:"identifier"`
	Number       int64                  `json:"number"`
	Revision     int64                  `json:"revision"`
	Title        string                 `json:"title"`
	UpdatedAt    string                 `json:"updated_at"`
	Body         issueContextBodyLayer  `json:"body"`
	Statement    *issueContextValue     `json:"statement,omitempty"`
	Result       issueContextValue      `json:"result"`
	Contribution issueContextValue      `json:"contribution"`
	Acceptance   issueContextAcceptance `json:"acceptance_criteria"`
	Feedback     issueContextFeedback   `json:"feedback"`
}

type issueContextSource struct {
	Layer    string `json:"layer"`
	Field    string `json:"field"`
	Endpoint string `json:"endpoint"`
	Issue    string `json:"issue,omitempty"`
	Goal     string `json:"goal,omitempty"`
	Revision int64  `json:"revision,omitempty"`
	SHA256   string `json:"sha256,omitempty"`
}

type issueContextPayload struct {
	Version       int                       `json:"version"`
	FetchedAt     string                    `json:"fetched_at"`
	Issue         issueContextIssueLayer    `json:"issue"`
	EffectiveGoal issueContextEffectiveGoal `json:"effective_goal"`
	Goal          issueContextGoalLayer     `json:"goal"`
	Story         issueContextStoryLayer    `json:"story"`
	Sources       []issueContextSource      `json:"sources"`
	Warnings      []string                  `json:"warnings"`
}

type issueContextIssueLayer struct {
	ID         string  `json:"id"`
	Identifier string  `json:"identifier"`
	Number     int64   `json:"number"`
	Title      string  `json:"title"`
	Status     string  `json:"status"`
	ProjectID  *string `json:"project_id"`
	Revision   int64   `json:"revision"`
	UpdatedAt  string  `json:"updated_at"`
}

func runIssueContext(cmd *cobra.Command, args []string) error {
	output, _ := cmd.Flags().GetString("output")
	if output != issueContextJSONOutputFormat && output != issueContextTextOutputFormat {
		return fmt.Errorf("--output must be %q or %q", issueContextJSONOutputFormat, issueContextTextOutputFormat)
	}

	client, err := newGoalAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	issue, err := fetchIssueContextIssue(ctx, client, args[0])
	if err != nil {
		return err
	}

	description := ""
	if issue.Description != nil {
		description = *issue.Description
	}
	story := buildStoryContext(description, issue.Revision)
	if issue.Trace != nil && len(issue.Trace.Criteria) > 0 {
		story.Acceptance.Structured = issue.Trace
		if len(story.Acceptance.Items) == 0 {
			story.Acceptance.Source = "issue.acceptance_trace"
		} else {
			story.Acceptance.Source += " and issue.acceptance_trace"
		}
		story.Acceptance.State = "found"
		story.Acceptance.Reason = ""
		for _, c := range issue.Trace.Criteria {
			if n := len(c.Versions); n > 0 {
				story.Acceptance.Items = append(story.Acceptance.Items, issueContextAcceptanceItem{CriterionID: c.ID, Version: n, Text: c.Versions[n-1]})
			}
		}
	}

	feedback, err := fetchIssueContextFeedback(ctx, client, issue.ID)
	if err != nil {
		return contextReadError("read recent feedback for "+issue.Identifier, err)
	}

	goal, sources, warnings, err := resolveIssueContextGoal(ctx, client, issue)
	if err != nil {
		return err
	}

	contribution := story.Contribution
	if !story.ExplicitContribution {
		if goal.State == "linked" {
			planContribution, planSource, planWarning, err := findPlanContribution(ctx, client, goal.ID, issue.ID)
			if err != nil {
				return err
			}
			if planWarning != "" {
				warnings = append(warnings, planWarning)
			}
			switch {
			case planContribution != nil:
				contribution = *planContribution
				if planSource != nil {
					sources = append(sources, *planSource)
				}
			case story.Statement != nil:
				contribution = *story.Statement
			default:
				contribution.Basis = "full_description"
				contribution.Note = "no explicit contribution, no plan outcome bound to this task, and no user-story statement; the full description and recent feedback are the evidence"
			}
		} else {
			contribution = issueContextValue{
				State: "not_applicable",
				Basis: "none",
				Note:  "no upper goal is formally linked, so this story's effective goal is the story itself and there is no upper-goal contribution to state",
			}
		}
	}

	if goal.State != "linked" && story.ExplicitContribution {
		contribution.Note = strings.TrimSpace(contribution.Note + " No upper goal is formally linked; the effective goal is the story itself.")
	}

	if len(story.Acceptance.Sections) > 1 {
		warnings = append(warnings, "the description has more than one acceptance section; items from all of them are listed with their source lines")
	}

	effectiveGoal := effectiveGoalFromContext(issue, goal)

	story.Contribution = contribution

	payload := issueContextPayload{
		Version:   issueContextVersion,
		FetchedAt: time.Now().UTC().Format(time.RFC3339),
		Issue: issueContextIssueLayer{
			ID:         issue.ID,
			Identifier: issue.Identifier,
			Number:     issue.Number,
			Title:      issue.Title,
			Status:     issue.Status,
			ProjectID:  issue.ProjectID.display(),
			Revision:   issue.Revision,
			UpdatedAt:  issue.UpdatedAt,
		},
		EffectiveGoal: effectiveGoal,
		Goal:          goal,
		Story: issueContextStoryLayer{
			Identifier:   issue.Identifier,
			Number:       issue.Number,
			Revision:     issue.Revision,
			Title:        issue.Title,
			UpdatedAt:    issue.UpdatedAt,
			Body:         story.Body,
			Statement:    story.Statement,
			Result:       story.Result,
			Contribution: contribution,
			Acceptance:   story.Acceptance,
			Feedback:     feedback,
		},
		Sources:  append(sources, issueContextStorySources(issue, story, feedback)...),
		Warnings: warnings,
	}
	if payload.Sources == nil {
		payload.Sources = []issueContextSource{}
	}
	if payload.Warnings == nil {
		payload.Warnings = []string{}
	}

	if output == issueContextTextOutputFormat {
		writeIssueContextText(cmd, payload)
		return nil
	}
	return cli.PrintJSON(cmd.OutOrStdout(), payload)
}

func effectiveGoalFromContext(issue issueContextIssue, goal issueContextGoalLayer) issueContextEffectiveGoal {
	if goal.State == "linked" {
		return issueContextEffectiveGoal{
			Layer:     "goal",
			ID:        goal.ID,
			Title:     goal.Title,
			UpdatedAt: goal.UpdatedAt,
			Basis:     "linked_goal",
			Note:      "the task is formally linked to this upper goal; the goal's acceptance criteria apply to the goal, and the story's own criteria apply to this task",
		}
	}
	return issueContextEffectiveGoal{
		Layer:      "story",
		ID:         issue.ID,
		Identifier: issue.Identifier,
		Title:      issue.Title,
		Revision:   issue.Revision,
		UpdatedAt:  issue.UpdatedAt,
		Basis:      "story_self",
		Note:       "no upper goal is formally linked; this story is its own effective goal and only its own acceptance criteria apply",
	}
}

// fetchIssueContextIssue reads the issue once. The endpoint accepts an issue
// key or a full UUID; an ambiguous short prefix is rejected before any call so
// the command never silently reads a different task.
func fetchIssueContextIssue(ctx context.Context, client *cli.APIClient, input string) (issueContextIssue, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return issueContextIssue{}, fmt.Errorf("issue ref is required")
	}
	switch {
	case looksLikeIssueIdentifier(trimmed):
	case uuidRegexp.MatchString(trimmed):
		trimmed = strings.ToLower(trimmed)
	default:
		_, prefixErr := normalizeUUIDPrefix(trimmed)
		if prefixErr == nil {
			return issueContextIssue{}, fmt.Errorf("issue ref %q looks like a short UUID prefix; use the issue key (e.g. MUL-123) or the full UUID", input)
		}
		return issueContextIssue{}, fmt.Errorf("issue ref %q is not an issue key (e.g. MUL-123) or a full UUID", input)
	}

	path := "/api/issues/" + url.PathEscape(trimmed)
	var raw json.RawMessage
	if err := client.GetJSON(ctx, path, &raw); err != nil {
		return issueContextIssue{}, contextReadError("read issue "+trimmed, err)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return issueContextIssue{}, fmt.Errorf("read issue %s: the server returned an empty response", trimmed)
	}
	var issue issueContextIssue
	if err := json.Unmarshal(raw, &issue); err != nil {
		return issueContextIssue{}, fmt.Errorf("read issue %s: the server returned an invalid issue: %w", trimmed, err)
	}
	if issue.ID == "" || issue.Identifier == "" {
		return issueContextIssue{}, fmt.Errorf("read issue %s: the server returned an issue without an id or identifier", trimmed)
	}
	if _, err := issue.ProjectID.filterValue(); err != nil {
		return issueContextIssue{}, fmt.Errorf("read issue %s: %w", trimmed, err)
	}
	return issue, nil
}

// resolveIssueContextGoal walks project → visible goals → each goal's linked
// issues. It returns the goal layer plus the sources it read. Any read
// failure aborts: a failed request is not evidence that no goal exists.
func resolveIssueContextGoal(ctx context.Context, client *cli.APIClient, issue issueContextIssue) (issueContextGoalLayer, []issueContextSource, []string, error) {
	projectFilter, err := issue.ProjectID.filterValue()
	if err != nil {
		return issueContextGoalLayer{}, nil, nil, fmt.Errorf("read issue %s: %w", issue.Identifier, err)
	}
	resolution := issueContextGoalResolution{
		Method:        "issue-project-goals-goal-issues",
		ProjectFilter: projectFilter,
	}
	sources := []issueContextSource{
		{Layer: "issue", Field: "issue", Endpoint: "GET /api/issues/" + url.PathEscape(issue.ID), Issue: issue.Identifier},
		{Layer: "goal", Field: "association", Endpoint: "GET /api/goals?project_id=" + url.PathEscape(projectFilter) + "&limit=" + strconv.Itoa(issueContextPageSize)},
	}

	goals, err := listIssueContextGoals(ctx, client, projectFilter)
	if err != nil {
		return issueContextGoalLayer{}, nil, nil, contextReadError("list goals for project "+projectFilter, err)
	}

	for _, listed := range goals {
		resolution.GoalsScanned++
		found, scanned, err := goalLinksIssue(ctx, client, listed.ID, issue.ID)
		resolution.GoalIssuesScanned += scanned
		if err != nil {
			return issueContextGoalLayer{}, nil, nil, contextReadError("list issues linked to goal "+listed.ID, err)
		}
		if !found {
			continue
		}
		detail, err := fetchIssueContextGoalDetail(ctx, client, listed.ID)
		if err != nil {
			return issueContextGoalLayer{}, nil, nil, contextReadError("read goal "+listed.ID, err)
		}
		if detail.ID != listed.ID {
			return issueContextGoalLayer{}, nil, nil, fmt.Errorf("read goal %s: the server returned goal %q instead", listed.ID, detail.ID)
		}
		if !sameContextProject(detail.ProjectID, issue.ProjectID) {
			return issueContextGoalLayer{}, nil, nil, fmt.Errorf(
				"read goal %s: the goal is in project %s but the task is in project %s; refusing to attribute the task to a goal outside its own project",
				listed.ID, displayContextProject(detail.ProjectID), displayContextProject(issue.ProjectID))
		}
		layer := issueContextGoalLayer{
			State:              "linked",
			ID:                 detail.ID,
			Title:              detail.Title,
			Status:             detail.Status,
			Intent:             detail.Intent,
			Scope:              detail.Scope,
			AcceptanceCriteria: detail.AcceptanceCriteria,
			ProjectID:          detail.ProjectID.display(),
			UpdatedAt:          detail.UpdatedAt,
			Resolution:         resolution,
		}
		sources = append(sources,
			issueContextSource{Layer: "goal", Field: "association", Endpoint: "GET /api/goals/" + url.PathEscape(listed.ID) + "/issues", Goal: listed.ID},
			issueContextSource{Layer: "goal", Field: "goal", Endpoint: "GET /api/goals/" + url.PathEscape(listed.ID), Goal: listed.ID},
		)
		return layer, sources, nil, nil
	}

	layer := issueContextGoalLayer{
		State:      "none",
		ProjectID:  issue.ProjectID.display(),
		Resolution: resolution,
	}
	if len(goals) == 0 {
		layer.Reason = "no_visible_goal_in_project"
		layer.Detail = "the task's project has no visible goal under the current identity"
	} else {
		layer.Reason = "not_linked_to_any_goal"
		layer.Detail = fmt.Sprintf("none of the %d visible goal(s) in the task's project lists this task among its linked issues", len(goals))
	}
	return layer, sources, nil, nil
}

// decodeContextArray rejects a missing or null collection. A successful 200
// with `"goals": null` is not an empty list: it is a response that cannot be
// read, and treating it as "no association" would misattribute the task.
func decodeContextArray(raw json.RawMessage, field string, dst any) error {
	if raw == nil {
		return fmt.Errorf("the server returned no %s array", field)
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return fmt.Errorf("the server returned %s: null instead of an array", field)
	}
	if err := json.Unmarshal(trimmed, dst); err != nil {
		return fmt.Errorf("the server returned an invalid %s array: %w", field, err)
	}
	return nil
}

func contextPageKey(ids []string) string {
	return strings.Join(ids, "\x00")
}

// listIssueContextGoals walks every page of the project's visible goals. An
// empty page ends the walk; `total` is never an end condition because a failed
// count reports the page length. A page that repeats the previous page (or
// brings no new goal) aborts the walk instead of looping.
func listIssueContextGoals(ctx context.Context, client *cli.APIClient, projectFilter string) ([]issueContextGoal, error) {
	seen := make(map[string]struct{})
	var goals []issueContextGoal
	offset := 0
	previousKey := ""
	for {
		params := url.Values{}
		params.Set("project_id", projectFilter)
		params.Set("limit", strconv.Itoa(issueContextPageSize))
		if offset > 0 {
			params.Set("offset", strconv.Itoa(offset))
		}
		path := "/api/goals?" + params.Encode()

		var envelope struct {
			Goals json.RawMessage `json:"goals"`
		}
		if err := client.GetJSON(ctx, path, &envelope); err != nil {
			return nil, err
		}
		var page []issueContextGoal
		if err := decodeContextArray(envelope.Goals, "goals", &page); err != nil {
			return nil, err
		}
		if len(page) == 0 {
			break
		}
		ids := make([]string, 0, len(page))
		for _, goal := range page {
			if strings.TrimSpace(goal.ID) == "" {
				return nil, fmt.Errorf("the server returned a goal without an id")
			}
			ids = append(ids, goal.ID)
		}
		if key := contextPageKey(ids); key == previousKey {
			return nil, fmt.Errorf("the server returned the same goals page again at offset %d; refusing to loop", offset)
		} else {
			previousKey = key
		}
		added := 0
		for _, goal := range page {
			if _, duplicate := seen[goal.ID]; duplicate {
				continue
			}
			seen[goal.ID] = struct{}{}
			goals = append(goals, goal)
			added++
		}
		if added == 0 {
			return nil, fmt.Errorf("the goals page at offset %d brought no new goal; refusing to loop", offset)
		}
		offset += len(page)
	}
	return goals, nil
}

// goalLinksIssue walks one goal's linked-issue pages until the issue is found
// or a page comes back empty. It returns how many links it inspected so the
// output can show the resolution's evidence without exposing other tasks. A
// repeated page aborts instead of looping.
func goalLinksIssue(ctx context.Context, client *cli.APIClient, goalID, issueID string) (bool, int, error) {
	scanned := 0
	offset := 0
	previousKey := ""
	for {
		params := url.Values{}
		params.Set("limit", strconv.Itoa(issueContextPageSize))
		if offset > 0 {
			params.Set("offset", strconv.Itoa(offset))
		}
		path := "/api/goals/" + url.PathEscape(goalID) + "/issues?" + params.Encode()

		var envelope struct {
			Issues json.RawMessage `json:"issues"`
		}
		if err := client.GetJSON(ctx, path, &envelope); err != nil {
			return false, scanned, err
		}
		var page []struct {
			ID string `json:"id"`
		}
		if err := decodeContextArray(envelope.Issues, "issues", &page); err != nil {
			return false, scanned, err
		}
		if len(page) == 0 {
			return false, scanned, nil
		}
		ids := make([]string, 0, len(page))
		for index, linked := range page {
			if linked.ID == "" {
				return false, scanned, fmt.Errorf("the server returned a linked issue without an id")
			}
			if linked.ID == issueID {
				return true, scanned + index + 1, nil
			}
			ids = append(ids, linked.ID)
		}
		if key := contextPageKey(ids); key == previousKey {
			return false, scanned, fmt.Errorf("the server returned the same issues page again for goal %s at offset %d; refusing to loop", goalID, offset)
		} else {
			previousKey = key
		}
		scanned += len(page)
		offset += len(page)
	}
}

func fetchIssueContextGoalDetail(ctx context.Context, client *cli.APIClient, goalID string) (issueContextGoal, error) {
	var goal issueContextGoal
	path := "/api/goals/" + url.PathEscape(goalID)
	if err := client.GetJSON(ctx, path, &goal); err != nil {
		return issueContextGoal{}, err
	}
	if goal.ID == "" {
		return issueContextGoal{}, fmt.Errorf("the server returned a goal without an id")
	}
	if _, err := goal.ProjectID.filterValue(); err != nil {
		return issueContextGoal{}, fmt.Errorf("goal %s: %w", goalID, err)
	}
	return goal, nil
}

type issueContextPlanItem struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Outcome string `json:"outcome"`
	Binding struct {
		Type    string  `json:"type"`
		IssueID *string `json:"issue_id"`
	} `json:"binding"`
}

// findPlanContribution reads the goal's formal plan and returns the outcome of
// the item bound to this issue. The plan's outcome is the goal-level statement
// of what this story contributes; it is used only when the story body has no
// explicit contribution section, and its item id and plan revision are kept as
// the source.
func findPlanContribution(ctx context.Context, client *cli.APIClient, goalID, issueID string) (*issueContextValue, *issueContextSource, string, error) {
	path := "/api/goals/" + url.PathEscape(goalID) + "/plan"
	var envelope struct {
		GoalID   string          `json:"goal_id"`
		Revision int32           `json:"revision"`
		State    string          `json:"state"`
		Items    json.RawMessage `json:"items"`
	}
	if err := client.GetJSON(ctx, path, &envelope); err != nil {
		return nil, nil, "", contextReadError("read the plan of goal "+goalID, err)
	}
	if envelope.GoalID == "" {
		return nil, nil, "", fmt.Errorf("read the plan of goal %s: the server returned a plan without a goal_id", goalID)
	}
	if envelope.GoalID != goalID {
		return nil, nil, "", fmt.Errorf("read the plan of goal %s: the server returned the plan of goal %q instead", goalID, envelope.GoalID)
	}
	var items []issueContextPlanItem
	if err := decodeContextArray(envelope.Items, "plan items", &items); err != nil {
		return nil, nil, "", fmt.Errorf("read the plan of goal %s: %w", goalID, err)
	}

	matched := 0
	var chosen *issueContextPlanItem
	for i := range items {
		item := &items[i]
		if item.Binding.IssueID == nil || !strings.EqualFold(strings.TrimSpace(*item.Binding.IssueID), issueID) {
			continue
		}
		matched++
		if chosen == nil && strings.TrimSpace(item.Outcome) != "" {
			chosen = item
		}
	}
	if chosen == nil {
		if matched > 0 {
			return nil, nil, "the goal plan binds this task to an item without an outcome; the story body is the contribution evidence", nil
		}
		return nil, nil, "", nil
	}
	warning := ""
	if matched > 1 {
		warning = fmt.Sprintf("the goal plan binds this task to %d items; the outcome of item %s is used", matched, chosen.ID)
	}
	value := &issueContextValue{
		State:  "extracted",
		Text:   strings.TrimSpace(chosen.Outcome),
		Basis:  "goal_plan_outcome",
		Source: "goal plan revision " + strconv.Itoa(int(envelope.Revision)) + ", item " + chosen.ID,
		Note:   "the formal plan's outcome for the item bound to this task; the story body remains the acceptance evidence",
	}
	source := &issueContextSource{
		Layer:    "goal",
		Field:    "contribution",
		Endpoint: "GET " + path,
		Goal:     goalID,
	}
	return value, source, warning, nil
}

// fetchIssueContextFeedback reads the most recently active threads, folding
// resolved ones so settled discussion does not masquerade as current feedback.
// Read failures are returned to the caller: feedback is part of the evidence,
// and an unread section must not look like an empty one.
func fetchIssueContextFeedback(ctx context.Context, client *cli.APIClient, issueID string) (issueContextFeedback, error) {
	path := fmt.Sprintf("/api/issues/%s/comments?recent=%d&fold=true", url.PathEscape(issueID), issueContextFeedbackThreads)
	var raw json.RawMessage
	if err := client.GetJSON(ctx, path, &raw); err != nil {
		return issueContextFeedback{}, err
	}
	var comments []struct {
		ID         string `json:"id"`
		CreatedAt  string `json:"created_at"`
		AuthorType string `json:"author_type"`
		Content    string `json:"content"`
	}
	if err := decodeContextArray(raw, "comments", &comments); err != nil {
		return issueContextFeedback{}, err
	}
	items := make([]issueContextFeedbackItem, 0, len(comments))
	for _, comment := range comments {
		if comment.ID == "" {
			return issueContextFeedback{}, fmt.Errorf("the server returned a comment without an id")
		}
		items = append(items, issueContextFeedbackItem{
			ID:         comment.ID,
			CreatedAt:  comment.CreatedAt,
			AuthorType: comment.AuthorType,
			Content:    comment.Content,
		})
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].CreatedAt == items[j].CreatedAt {
			return items[i].ID < items[j].ID
		}
		return items[i].CreatedAt < items[j].CreatedAt
	})
	state := "none"
	if len(items) > 0 {
		state = "included"
	}
	return issueContextFeedback{
		State:   state,
		Threads: issueContextFeedbackThreads,
		Items:   items,
		Source:  "GET " + path,
	}, nil
}

func issueContextStorySources(issue issueContextIssue, story storyContext, feedback issueContextFeedback) []issueContextSource {
	sources := []issueContextSource{{
		Layer:    "story",
		Field:    "description",
		Endpoint: "GET /api/issues/" + url.PathEscape(issue.ID),
		Issue:    issue.Identifier,
		Revision: issue.Revision,
		SHA256:   story.Body.SHA256,
	}}
	if feedback.Source != "" {
		sources = append(sources, issueContextSource{
			Layer:    "story",
			Field:    "feedback",
			Endpoint: feedback.Source,
			Issue:    issue.Identifier,
		})
	}
	return sources
}

// contextReadError turns an HTTP failure into a message that names the layer
// and keeps a permission or server failure visibly different from an empty
// result. The server's own message is appended when it has one.
func contextReadError(action string, err error) error {
	var httpErr *cli.HTTPError
	if !errors.As(err, &httpErr) {
		return fmt.Errorf("%s: %w", action, err)
	}
	detail := strings.TrimSpace(cli.ServerMessage(err))
	suffix := ""
	if detail != "" {
		suffix = ": " + detail
	}
	switch httpErr.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%s: access denied (HTTP %d)%s", action, httpErr.StatusCode, suffix)
	case http.StatusNotFound:
		return fmt.Errorf("%s: not found or not visible to this task's identity (HTTP %d)%s", action, httpErr.StatusCode, suffix)
	default:
		return fmt.Errorf("%s: server returned HTTP %d%s", action, httpErr.StatusCode, suffix)
	}
}

func sameContextProject(a, b issueContextProject) bool {
	if a.Value == nil || b.Value == nil {
		return a.Value == nil && b.Value == nil
	}
	return strings.EqualFold(strings.TrimSpace(*a.Value), strings.TrimSpace(*b.Value))
}

func displayContextProject(project issueContextProject) string {
	if project.Value == nil {
		return "none"
	}
	if value := strings.TrimSpace(*project.Value); value != "" {
		return value
	}
	return "none"
}

func writeIssueContextText(cmd *cobra.Command, payload issueContextPayload) {
	out := cmd.OutOrStdout()
	issue := payload.Issue
	fmt.Fprintf(out, "%s  %s  [%s]\n", issue.Identifier, issue.Title, issue.Status)
	fmt.Fprintf(out, "revision %d, updated %s\n", issue.Revision, issue.UpdatedAt)

	effective := payload.EffectiveGoal
	if effective.Layer == "goal" {
		fmt.Fprintf(out, "Effective goal: upper goal %s (%s), updated %s\n", effective.Title, effective.ID, effective.UpdatedAt)
	} else {
		fmt.Fprintf(out, "Effective goal: story %s (%s) at revision %d — %s\n", effective.Title, effective.Identifier, effective.Revision, effective.Note)
	}

	goal := payload.Goal
	if goal.State == "linked" {
		fmt.Fprintf(out, "Goal: %s (%s), updated %s\n", goal.Title, goal.ID, goal.UpdatedAt)
		if goal.Intent != "" {
			fmt.Fprintf(out, "  Intent: %s\n", goal.Intent)
		}
		if goal.Scope != "" {
			fmt.Fprintf(out, "  Scope: %s\n", goal.Scope)
		}
		if goal.AcceptanceCriteria != "" {
			fmt.Fprintf(out, "  Goal acceptance (applies to the upper goal): %s\n", goal.AcceptanceCriteria)
		}
	} else {
		fmt.Fprintf(out, "Goal: none (%s)\n", goal.Detail)
	}

	story := payload.Story
	fmt.Fprintf(out, "Story result: %s\n", contextValueText(story.Result))
	if story.Statement != nil {
		fmt.Fprintf(out, "Story statement: %s\n", story.Statement.Text)
	}
	fmt.Fprintf(out, "Story contribution (%s): %s\n", story.Contribution.Basis, contextValueText(story.Contribution))
	if story.Contribution.Source != "" {
		fmt.Fprintf(out, "  Source: %s\n", story.Contribution.Source)
	}
	fmt.Fprintf(out, "Story acceptance (%s):", story.Acceptance.State)
	if story.Acceptance.State != "found" {
		fmt.Fprintf(out, " %s", story.Acceptance.Reason)
	}
	fmt.Fprintln(out)
	for _, item := range story.Acceptance.Items {
		if item.CriterionID != "" {
			fmt.Fprintf(out, "  %s v%d: %s\n", item.CriterionID, item.Version, item.Text)
			continue
		}
		check := " "
		if item.Checked != nil {
			if *item.Checked {
				check = "x"
			}
		}
		fmt.Fprintf(out, "  - [%s] %s (line %d)\n", check, item.Text, item.Line)
	}
	fmt.Fprintf(out, "Feedback: %d comment(s) from the %d most recently active thread(s)\n", len(story.Feedback.Items), story.Feedback.Threads)
	for _, warning := range payload.Warnings {
		fmt.Fprintf(out, "Warning: %s\n", warning)
	}
}

func contextValueText(value issueContextValue) string {
	switch value.State {
	case "extracted":
		return value.Text
	case "not_applicable":
		return "(not applicable: " + value.Note + ")"
	case "basis_only":
		return "(no dedicated section; use the full description and recent feedback)"
	default:
		if value.Note != "" {
			return "unavailable: " + value.Note
		}
		return "unavailable"
	}
}
