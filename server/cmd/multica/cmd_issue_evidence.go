package main

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/spf13/cobra"
)

func init() { issueCmd.AddCommand(newIssueEvidenceCommand()) }

func newIssueEvidenceCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use: "evidence <issue>", Short: "List independent evidence runs or request one for selected issue attachments", Args: exactArgs(1),
		Example: `  multica issue evidence DEV-69
  multica issue evidence DEV-69 --request-file evidence.json

Request file:
  {"request_id":"<new UUID>","agent_id":"<recipient UUID>","question":"What do these frozen inputs show?","inputs":[{"attachment_id":"<issue attachment UUID>","sha256":"<64 hex digits>"}]}

Retry the same request_id and payload after an interrupted response. The final answer and usage are visible to people with issue access. Select a local Codex or Claude CLI agent. Runs use a separate working directory. The operating system file permissions still apply.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := newAPIClient(cmd)
			if err != nil {
				return err
			}
			ctx, cancel := cli.APIContext(context.Background())
			defer cancel()
			path := "/api/issues/" + url.PathEscape(strings.TrimSpace(args[0])) + "/evidence-runs"
			input, supplied, err := resolveTextFlag(cmd, "request")
			if err != nil {
				return err
			}
			var result json.RawMessage
			if supplied {
				err = client.PostJSON(ctx, path, json.RawMessage(input), &result)
			} else {
				err = client.GetJSON(ctx, path, &result)
			}
			if err != nil {
				return err
			}
			return cli.PrintJSON(cmd.OutOrStdout(), result)
		},
	}
	cmd.Flags().String("request-file", "", "Start a dedicated run with an evidence request JSON file")
	cmd.Flags().Bool("request-stdin", false, "Read an evidence request JSON object from stdin")
	cmd.Flags().Bool("allow-external-file", false, "Allow a request file outside the current working directory")
	return cmd
}
