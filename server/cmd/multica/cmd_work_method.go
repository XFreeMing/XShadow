package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/spf13/cobra"
)

func init() {
	workMethods := &cobra.Command{Use: "work-method", Short: "Manage the workspace's work methods"}
	list := &cobra.Command{Use: "list", Args: exactArgs(0), Short: "List built-in and custom methods"}
	list.RunE = func(cmd *cobra.Command, _ []string) error {
		return requestWorkMethod(cmd, "/api/work-methods", "", false)
	}
	create := &cobra.Command{Use: "create", Args: exactArgs(0), Short: "Create a method with deliverables, a review and a rework step"}
	addWorkMethodInput(create, "definition")
	create.RunE = func(cmd *cobra.Command, _ []string) error {
		return requestWorkMethod(cmd, "/api/work-methods", "definition", true)
	}
	workMethods.AddCommand(list, create)
	rootCmd.AddCommand(workMethods)

	issueMethod := &cobra.Command{
		Use: "method <issue>", Args: exactArgs(1),
		Short: "Read or advance an issue's goal-selected work method",
		Long: `Read the method, selection reason, current step, required output, decision history
and revision. Supply one JSON change with --change-file or --change-stdin to select
a method, submit a step's output, approve its review, or return work for revision.

Examples of changes (base_revision must match the latest read):
  {"action":"select","base_revision":0,"method_id":"seven-step"}
  {"action":"advance","base_revision":1,"evidence":"Report attachment and verified findings"}
  {"action":"rework","base_revision":8,"target_step":4,"evidence":"The data excludes returning customers"}
  {"action":"approve","base_revision":12,"evidence":"Reviewer confirmed the revised sample and recommendation"}`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return requestWorkMethod(cmd, "/api/issues/"+url.PathEscape(args[0])+"/work-method", "change", false)
		},
	}
	addWorkMethodInput(issueMethod, "change")
	issueCmd.AddCommand(issueMethod)
}

func addWorkMethodInput(cmd *cobra.Command, flag string) {
	cmd.Flags().String(flag+"-file", "", "Read JSON from a UTF-8 file")
	cmd.Flags().Bool(flag+"-stdin", false, "Read JSON from standard input")
	cmd.Flags().Bool("allow-external-file", false, "Allow an input file outside the current working directory")
}

func requestWorkMethod(cmd *cobra.Command, path, input string, required bool) error {
	var data string
	var supplied bool
	var err error
	if input != "" {
		data, supplied, err = resolveTextFlag(cmd, input)
		if err != nil {
			return err
		}
	}
	if required && !supplied {
		return errors.New("a method definition is required; use --definition-file or --definition-stdin")
	}
	if supplied && !json.Valid([]byte(data)) {
		return errors.New("invalid work method JSON")
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var output json.RawMessage
	if supplied {
		err = client.PostJSON(ctx, path, json.RawMessage(data), &output)
	} else {
		err = client.GetJSON(ctx, path, &output)
	}
	if err != nil {
		return err
	}
	return cli.PrintJSON(cmd.OutOrStdout(), output)
}
