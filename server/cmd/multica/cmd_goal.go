package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
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

func init() {
	goalRecordCmd.Flags().String("file", "", "Path to the goal JSON file")
	_ = goalRecordCmd.MarkFlagRequired("file")
	goalCmd.AddCommand(goalRecordCmd)

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
			Items           json.RawMessage `json:"items"`
			Edges           json.RawMessage `json:"edges"`
			ContextSnapshot json.RawMessage `json:"context_snapshot"`
		}
		if err := json.Unmarshal(raw, &plan); err != nil {
			return fmt.Errorf("parse plan file as JSON: %w", err)
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
