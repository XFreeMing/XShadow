package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

const (
	// goalIssueListDefaultLimit is the page size `goal issues` asks for. The
	// server caps a page at goalIssueListMaxLimit and applies its own default
	// when the parameter is missing, so both values are sent explicitly.
	goalIssueListDefaultLimit = 100
	goalIssueListMaxLimit     = 100
)

// multica goal plan-result — submit the plan a planning run produced.
//
// A planning run is an ordinary agent task, so it holds a task-scoped mat_
// token and nothing else. This command is the only supported way for that run
// to hand the plan back: the server resolves the generation from the task
// token's task id, so a run can only ever write the goal plan it was started
// for. Do not post the plan as an issue comment instead.
var goalCmd = &cobra.Command{
	Use:   "goal",
	Short: "Work with goals and goal plans",
}

var goalPlanResultCmd = &cobra.Command{
	Use:   "plan-result",
	Short: "Submit the result of a goal plan generation run",
	Long: `Submit the plan draft produced by the goal plan generation this run owns.

The request is authenticated with the task-scoped token the daemon issued to
this run; the generation is resolved from that task, so --goal and --generation
must match the goal and generation the run was started for.

--file holds a UTF-8 JSON object:
  {"items": [...], "edges": [...], "context_snapshot": {...}}

Report a run that could not produce a plan with --error instead of inventing
evidence.`,
	Args: cobra.NoArgs,
	RunE: runGoalPlanResult,
}

var goalRecordCmd = &cobra.Command{
	Use:   "record",
	Short: "Record a goal from the chief of staff conversation",
	Long: `Record a goal from the dedicated chief of staff conversation using this run's task token.

--file holds a UTF-8 JSON object:
  {"title":"…","intent":"…","scope":"…","acceptance_criteria":"…","project_id":"…","issue_ids":["DEV-123"]}

project_id and issue_ids are optional. Existing task links are saved together
with the goal. The command returns the saved goal's Markdown link.`,
	Args: cobra.NoArgs,
	RunE: runGoalRecord,
}

var goalGetCmd = &cobra.Command{
	Use:   "get <goal-id>",
	Short: "Show a goal",
	Long: `Show a goal's title, intent, scope, acceptance criteria and status.

Use "multica goal issues <goal-id>" to list the tasks linked to it.`,
	Args: exactArgs(1),
	RunE: runGoalGet,
}

var goalIssuesCmd = &cobra.Command{
	Use:   "issues <goal-id>",
	Short: "List the tasks linked to a goal",
	Long: `List the tasks linked to a goal.

Pagination follows "multica issue list": each request returns one page and
--offset advances it. A walk ends only on a page the server returned empty; a
total is trusted to end it early only when it is larger than the page just
returned, because a server whose count query failed reports the page length as
the total. Use --all to read every page, including when the server applies
smaller pages than --limit asked for.`,
	Args: exactArgs(1),
	RunE: runGoalIssues,
}

var goalLinkCmd = &cobra.Command{
	Use:   "link <goal-id> <issue>...",
	Short: "Link tasks to a goal",
	Long: `Link one or more tasks to a goal. Tasks are accepted by identifier (DEV-68)
or UUID.

Linking is idempotent: linking a task that is already linked to this goal
succeeds without creating a second link. A task linked to another goal, or in
another project, fails with the server's reason and is never moved.

Every task is attempted even when an earlier one fails, and the command exits
non-zero if any did. Re-running the same command after the cause is fixed is
safe: the tasks that were already linked are left as they are.`,
	Args: cobra.MinimumNArgs(2),
	RunE: runGoalLink,
}

var goalUnlinkCmd = &cobra.Command{
	Use:   "unlink <goal-id> <issue>...",
	Short: "Unlink tasks from a goal",
	Long: `Unlink one or more tasks from a goal.

Unlinking a pair that is no longer linked succeeds. Neither the task nor the
goal is deleted.

Every task is attempted even when an earlier one fails, and the command exits
non-zero if any did.`,
	Args: cobra.MinimumNArgs(2),
	RunE: runGoalUnlink,
}

func runGoalRecord(cmd *cobra.Command, _ []string) error {
	path, _ := cmd.Flags().GetString("file")
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read goal file: %w", err)
	}
	var body struct {
		Title              string   `json:"title"`
		Intent             string   `json:"intent"`
		Scope              string   `json:"scope"`
		AcceptanceCriteria string   `json:"acceptance_criteria"`
		ProjectID          *string  `json:"project_id,omitempty"`
		IssueIDs           []string `json:"issue_ids,omitempty"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return fmt.Errorf("parse goal file: %w", err)
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var result struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	if err := client.PostJSON(ctx, "/api/goals", body, &result); err != nil {
		return fmt.Errorf("record goal: %w", err)
	}
	if result.ID == "" {
		return fmt.Errorf("the server did not confirm the saved goal")
	}
	fmt.Fprintf(cmd.OutOrStdout(), "[%s](/goals/%s)\n", result.Title, result.ID)
	return nil
}

// newGoalAPIClient builds the API client for the goal commands. Unlike
// `issue list`, which sends the workspace as a query parameter, the goal
// routes resolve the workspace from the request context or the
// X-Workspace-ID header, so the resolved id has to reach the client itself.
func newGoalAPIClient(cmd *cobra.Command) (*cli.APIClient, error) {
	client, err := newAPIClient(cmd)
	if err != nil {
		return nil, err
	}
	if client.WorkspaceID == "" {
		id, err := requireWorkspaceID(cmd)
		if err != nil {
			return nil, err
		}
		client.WorkspaceID = id
	}
	return client, nil
}

func runGoalGet(cmd *cobra.Command, args []string) error {
	client, err := newGoalAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var goal map[string]any
	path := "/api/goals/" + url.PathEscape(args[0])
	if err := client.GetJSON(ctx, path, &goal); err != nil {
		return fmt.Errorf("get goal: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(cmd.OutOrStdout(), goal)
	}

	project := strVal(goal, "project_id")
	if project == "" {
		project = "-"
	}
	updated := strVal(goal, "updated_at")
	if len(updated) >= 10 {
		updated = updated[:10]
	}
	headers := []string{"ID", "TITLE", "STATUS", "PROJECT", "UPDATED"}
	rows := [][]string{{
		strVal(goal, "id"),
		strVal(goal, "title"),
		strVal(goal, "status"),
		project,
		updated,
	}}
	cli.PrintTable(cmd.OutOrStdout(), headers, rows)
	return nil
}

// goalIssuesPage is one page of GET /api/goals/{id}/issues.
type goalIssuesPage struct {
	issues   []any
	total    int
	totalOK  bool
	returned int
}

func fetchGoalIssuesPage(ctx context.Context, client *cli.APIClient, goalID string, limit, offset int) (goalIssuesPage, error) {
	params := url.Values{}
	params.Set("limit", strconv.Itoa(limit))
	if offset > 0 {
		params.Set("offset", strconv.Itoa(offset))
	}
	path := fmt.Sprintf("/api/goals/%s/issues?%s", url.PathEscape(goalID), params.Encode())

	var result map[string]any
	if err := client.GetJSON(ctx, path, &result); err != nil {
		return goalIssuesPage{}, fmt.Errorf("list goal issues: %w", err)
	}
	issues, ok := result["issues"].([]any)
	if !ok {
		return goalIssuesPage{}, fmt.Errorf("list goal issues: the server returned no issues array")
	}
	total, totalOK := result["total"].(float64)
	return goalIssuesPage{
		issues:   issues,
		total:    int(total),
		totalOK:  totalOK,
		returned: len(issues),
	}, nil
}

func runGoalIssues(cmd *cobra.Command, args []string) error {
	client, err := newGoalAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	limit, _ := cmd.Flags().GetInt("limit")
	if limit < 1 || limit > goalIssueListMaxLimit {
		return fmt.Errorf("--limit must be between 1 and %d", goalIssueListMaxLimit)
	}
	offset, _ := cmd.Flags().GetInt("offset")
	if offset < 0 {
		return fmt.Errorf("--offset must be zero or greater")
	}
	all, _ := cmd.Flags().GetBool("all")
	output, _ := cmd.Flags().GetString("output")
	goalID := args[0]

	var page goalIssuesPage
	if all {
		collected := make([]any, 0, limit)
		seen := make(map[string]struct{}, limit)
		next := offset
		for {
			p, err := fetchGoalIssuesPage(ctx, client, goalID, limit, next)
			if err != nil {
				return err
			}
			page = p
			for _, raw := range p.issues {
				issue, ok := raw.(map[string]any)
				if !ok {
					return fmt.Errorf("list goal issues: the server returned an invalid issue")
				}
				id := strVal(issue, "id")
				if id != "" {
					if _, dup := seen[id]; dup {
						// A link added or removed between pages shifts
						// offset paging, so the same row can come back
						// twice. Keep the first occurrence.
						continue
					}
					seen[id] = struct{}{}
				}
				collected = append(collected, raw)
			}
			// Only an empty page ends the walk. total is never the end
			// condition here: a failed count reports the page length, which
			// would stop a full page early.
			if p.returned == 0 {
				break
			}
			next += p.returned
		}
		return printGoalIssues(cmd, goalID, collected, page, limit, offset, false, true, output)
	}

	page, err = fetchGoalIssuesPage(ctx, client, goalID, limit, offset)
	if err != nil {
		return err
	}
	// Same rule as `issue list`: trust a total only when it is larger than
	// the page just returned, which a failed count never is; otherwise let
	// the page decide.
	totalTrusted := page.totalOK && page.total > page.returned
	hasMore := false
	switch {
	case page.returned == 0:
		hasMore = false
	case totalTrusted:
		hasMore = offset+page.returned < page.total
	default:
		hasMore = page.returned == limit
	}
	return printGoalIssues(cmd, goalID, page.issues, page, limit, offset, hasMore, false, output)
}

func printGoalIssues(cmd *cobra.Command, goalID string, issues []any, page goalIssuesPage, limit, offset int, hasMore, complete bool, output string) error {
	if output == "json" {
		wrapped := map[string]any{
			"goal_id":  goalID,
			"issues":   issues,
			"total":    page.total,
			"limit":    limit,
			"offset":   offset,
			"has_more": hasMore,
		}
		return cli.PrintJSON(cmd.OutOrStdout(), wrapped)
	}

	rows := make([][]string, 0, len(issues))
	for _, raw := range issues {
		issue, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		rows = append(rows, []string{
			issueDisplayKey(issue),
			strVal(issue, "title"),
			strVal(issue, "status"),
			strVal(issue, "priority"),
		})
	}
	if len(rows) == 0 {
		fmt.Fprintln(cmd.ErrOrStderr(), "No tasks are linked to this goal.")
		return nil
	}
	cli.PrintTable(cmd.OutOrStdout(), []string{"KEY", "TITLE", "STATUS", "PRIORITY"}, rows)

	returned := len(issues)
	totalTrusted := page.totalOK && page.total > returned
	switch {
	case complete:
		fmt.Fprintf(cmd.ErrOrStderr(), "%d task(s) listed.\n", returned)
	case offset > 0 || hasMore:
		line := fmt.Sprintf("Showing tasks %d-%d.", offset+1, offset+returned)
		if totalTrusted {
			line = fmt.Sprintf("Showing %d-%d of %d tasks.", offset+1, offset+returned, page.total)
		}
		if hasMore {
			line += fmt.Sprintf(" Next page: --offset %d", offset+returned)
		}
		fmt.Fprintln(cmd.ErrOrStderr(), line)
	}
	return nil
}

// goalLinkChangeErrorText prefers the server's own reason (for example
// "the issue is already linked to another goal") over the generic status copy.
func goalLinkChangeErrorText(err error) string {
	if msg := cli.ServerMessage(err); msg != "" {
		if code := cli.ServerErrorCode(err); code != "" {
			return fmt.Sprintf("%s (%s)", msg, code)
		}
		return msg
	}
	return strings.TrimSpace(cli.FormatError(err, debugFlag))
}

// runGoalLinkChange applies one link operation per issue ref and reports each
// result. A failure on one ref does not stop the rest: every ref is attempted,
// and the command exits non-zero when any failed. Both PUT (link) and DELETE
// (unlink) are idempotent on the server, so re-running the same command is the
// retry.
func runGoalLinkChange(cmd *cobra.Command, args []string, verb string) error {
	client, err := newGoalAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	goalID := args[0]
	output, _ := cmd.Flags().GetString("output")
	results := make([]map[string]any, 0, len(args)-1)
	failed := 0
	for _, ref := range args[1:] {
		path := fmt.Sprintf("/api/goals/%s/issues/%s", url.PathEscape(goalID), url.PathEscape(ref))
		var changeErr error
		if verb == "unlink" {
			changeErr = client.DeleteJSON(ctx, path)
		} else {
			// The route takes no body; PutJSON sends a JSON null, which the
			// handler ignores.
			changeErr = client.PutJSON(ctx, path, nil, nil)
		}
		if changeErr != nil {
			failed++
			text := goalLinkChangeErrorText(changeErr)
			results = append(results, map[string]any{"issue": ref, "ok": false, "error": text})
			if output != "json" {
				fmt.Fprintf(cmd.ErrOrStderr(), "Error: %s %s: %s\n", verb, ref, text)
			}
			continue
		}
		results = append(results, map[string]any{"issue": ref, "ok": true})
		if output != "json" {
			if verb == "unlink" {
				fmt.Fprintf(cmd.OutOrStdout(), "Unlinked %s from goal %s.\n", ref, goalID)
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "Linked %s to goal %s.\n", ref, goalID)
			}
		}
	}

	if output == "json" {
		wrapped := map[string]any{
			"goal_id": goalID,
			"results": results,
			"failed":  failed,
		}
		if err := cli.PrintJSON(cmd.OutOrStdout(), wrapped); err != nil {
			return err
		}
	}
	if failed > 0 {
		// Per-item reasons are already on stderr (or in the JSON results);
		// errSilent keeps the exit non-zero without printing a second,
		// vaguer line.
		return errSilent
	}
	return nil
}

func runGoalLink(cmd *cobra.Command, args []string) error {
	return runGoalLinkChange(cmd, args, "link")
}

func runGoalUnlink(cmd *cobra.Command, args []string) error {
	return runGoalLinkChange(cmd, args, "unlink")
}

func init() {
	goalCmd.AddCommand(newGoalSolveCommand())

	goalRecordCmd.Flags().String("file", "", "Path to the goal JSON file")
	_ = goalRecordCmd.MarkFlagRequired("file")
	goalCmd.AddCommand(goalRecordCmd)

	goalGetCmd.Flags().String("output", "json", "Output format: table or json")
	goalCmd.AddCommand(goalGetCmd)

	goalIssuesCmd.Flags().Int("limit", goalIssueListDefaultLimit, fmt.Sprintf("Page size, 1 to %d", goalIssueListMaxLimit))
	goalIssuesCmd.Flags().Int("offset", 0, "Number of linked tasks to skip (advance by the number of tasks in the page just read)")
	goalIssuesCmd.Flags().Bool("all", false, "Read every page and print the complete list")
	goalIssuesCmd.Flags().String("output", "table", "Output format: table or json")
	goalCmd.AddCommand(goalIssuesCmd)

	goalLinkCmd.Flags().String("output", "table", "Output format: table or json")
	goalCmd.AddCommand(goalLinkCmd)

	goalUnlinkCmd.Flags().String("output", "table", "Output format: table or json")
	goalCmd.AddCommand(goalUnlinkCmd)

	goalPlanResultCmd.Flags().String("goal", "", "Goal ID the run was started for (required)")
	goalPlanResultCmd.Flags().String("generation", "", "Generation ID the run was started for (required)")
	goalPlanResultCmd.Flags().String("file", "", "Path to the plan JSON file")
	goalPlanResultCmd.Flags().String("error", "", "Report the run as failed with this reason instead of submitting a plan")
	goalPlanResultCmd.Flags().String("output", "table", "Output format: table or json")
	_ = goalPlanResultCmd.MarkFlagRequired("goal")
	_ = goalPlanResultCmd.MarkFlagRequired("generation")
	goalCmd.AddCommand(goalPlanResultCmd)
}

func runGoalPlanResult(cmd *cobra.Command, _ []string) error {
	goalID, _ := cmd.Flags().GetString("goal")
	generationID, _ := cmd.Flags().GetString("generation")
	filePath, _ := cmd.Flags().GetString("file")
	errorText, _ := cmd.Flags().GetString("error")

	if goalID == "" || generationID == "" {
		return fmt.Errorf("--goal and --generation are required")
	}
	if filePath == "" && errorText == "" {
		return fmt.Errorf("provide --file with the plan JSON, or --error with the reason the run produced none")
	}
	if filePath != "" && errorText != "" {
		return fmt.Errorf("--file and --error are mutually exclusive")
	}

	body := map[string]any{}
	if filePath != "" {
		raw, err := os.ReadFile(filePath)
		if err != nil {
			return fmt.Errorf("read plan file: %w", err)
		}
		var plan struct {
			Review          json.RawMessage `json:"review"`
			Items           json.RawMessage `json:"items"`
			Edges           json.RawMessage `json:"edges"`
			ContextSnapshot json.RawMessage `json:"context_snapshot"`
		}
		if err := json.Unmarshal(raw, &plan); err != nil {
			return fmt.Errorf("parse plan file as JSON: %w", err)
		}
		if len(plan.Review) > 0 {
			body["review"] = plan.Review
		}
		body["items"] = plan.Items
		body["edges"] = plan.Edges
		body["context_snapshot"] = plan.ContextSnapshot
	}
	if errorText != "" {
		body["error"] = errorText
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var result struct {
		ID    string `json:"id"`
		State string `json:"state"`
		Error string `json:"error"`
	}
	path := fmt.Sprintf("/api/goals/%s/plan/generations/%s/result", goalID, generationID)
	if err := client.PostJSON(ctx, path, body, &result); err != nil {
		return fmt.Errorf("submit plan result: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		encoded, err := json.Marshal(result)
		if err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), string(encoded))
		return nil
	}
	if result.State == "failed" {
		fmt.Fprintf(cmd.OutOrStdout(), "Reported generation %s as failed: %s\n", result.ID, result.Error)
		return nil
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Submitted plan for generation %s\n", result.ID)
	return nil
}
