package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/pkg/goalproblem"
	"github.com/spf13/cobra"
)

func newGoalSolveCommand() *cobra.Command {
	var file string
	var taskTitle string
	var revision int32
	cmd := &cobra.Command{
		Use:   "solve <goal-id>",
		Short: "Read the goal's problem-solving record or submit a command",
		Long: `Read the native problem-solving record, including goal versions, hypotheses,
verification batches and evidence.

To submit a command, use --file with a UTF-8 JSON object containing an action
and its typed payload. For example:
  {"action":"confirm_problem","confirm_problem":{"problem_ref":{"id":"<id>","revision":1}}}

Set --expected-revision to the record revision you read with this command.
The server checks action permissions and all referenced revisions.`,
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if (file != "") != cmd.Flags().Changed("expected-revision") || revision < 0 || (taskTitle != "" && file == "") {
				return fmt.Errorf("--file and a nonnegative --expected-revision must be provided together")
			}
			client, err := newGoalAPIClient(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := cli.APIContext(cmd.Context())
			defer cancel()
			path := "/api/goals/" + url.PathEscape(args[0]) + "/problem-solving"
			var response json.RawMessage
			if file == "" {
				err = client.GetJSON(ctx, path, &response)
			} else {
				raw, readErr := os.ReadFile(file)
				if readErr != nil {
					return fmt.Errorf("read goal command: %w", readErr)
				}
				command, decodeErr := goalproblem.DecodeCommand(raw)
				if decodeErr != nil {
					return fmt.Errorf("parse goal command: %w", decodeErr)
				}
				err = client.PostJSON(ctx, path+"/commands", struct {
					ExpectedRevision int32               `json:"expected_revision"`
					Command          goalproblem.Command `json:"command"`
					NewTaskTitle     string              `json:"new_task_title,omitempty"`
				}{revision, command, taskTitle}, &response)
			}
			if err != nil {
				return fmt.Errorf("goal problem solving: %w", err)
			}
			return cli.PrintJSON(cmd.OutOrStdout(), response)
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "Path to the goal command JSON file")
	cmd.Flags().StringVar(&taskTitle, "new-task-title", "", "Create a linked issue when starting a verification batch")
	cmd.Flags().Int32Var(&revision, "expected-revision", 0, "Current problem-solving revision")
	return cmd
}
