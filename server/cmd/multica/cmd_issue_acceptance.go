package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/pkg/acceptance"
	"github.com/spf13/cobra"
)

func init() {
	cmd := &cobra.Command{Use: "acceptance <issue>", Short: "Inspect acceptance criteria, test coverage and implementation commits", Args: exactArgs(1), RunE: runIssueAcceptance,
		Example: `  multica issue acceptance DEV-79 --criterion C1
  multica issue acceptance DEV-79 --case T1
  multica issue acceptance DEV-79 --commit <full-sha>
  multica issue acceptance DEV-79 --change-file change.json

Change file examples (use the revision returned by the last read):
  {"base_revision":0,"action":"criterion","criterion_id":"C1","text":"Expected behavior"}
  {"base_revision":1,"action":"case","criterion_id":"C1","case_id":"T1","text":"Steps and expected result"}
  {"base_revision":2,"action":"implementation","criterion_id":"C1","repository":"org/repo","branch":"dev","commit":"<full-sha>"}
  {"base_revision":3,"action":"run","criterion_id":"C1","case_id":"T1","result":"pass","candidate":"<tested-sha>","evidence":"<log or report reference>"}
  {"base_revision":4,"action":"case_retire","criterion_id":"C1","case_id":"T1","reason":"replaced by T1R2","superseded_by":"T1R2"}

case_retire keeps the case specification and run history, records the retiring
revision and reason, and stops listing the case as required by the candidate
and delivery gates. superseded_by is optional and must name a case in the same
criterion. Retired cases appear under retired_cases in the impact output.`}
	cmd.Flags().String("change-file", "", "Apply one acceptance change from a JSON file")
	cmd.Flags().Bool("change-stdin", false, "Read one JSON acceptance change from standard input")
	cmd.Flags().Bool("allow-external-file", false, "Allow a change file outside the current working directory")
	cmd.Flags().String("criterion", "", "Show one criterion and its history")
	cmd.Flags().String("case", "", "Find the criterion owning this case")
	cmd.Flags().String("commit", "", "Find criteria linked to this implementation commit")
	issueCmd.AddCommand(cmd)
}

func runIssueAcceptance(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	issue, err := fetchIssueContextIssue(ctx, client, args[0])
	if err != nil {
		return err
	}
	trace := issue.Trace
	if trace == nil {
		return errors.New("this issue does not expose structured acceptance records")
	}
	data, supplied, err := resolveTextFlag(cmd, "change")
	if err != nil {
		return err
	}
	if supplied {
		if err := client.PostJSON(ctx, "/api/issues/"+url.PathEscape(issue.ID)+"/acceptance", json.RawMessage(data), &trace); err != nil {
			return err
		}
	}
	criterion, _ := cmd.Flags().GetString("criterion")
	test, _ := cmd.Flags().GetString("case")
	commit, _ := cmd.Flags().GetString("commit")
	selected := []acceptance.Criterion{}
	for _, c := range trace.Criteria {
		if criterion != "" && c.ID != criterion {
			continue
		}
		foundCase := test == ""
		for _, tc := range c.Cases {
			if tc.ID == test {
				foundCase = true
			}
		}
		foundCommit := commit == ""
		for _, link := range c.Implementations {
			if link.Commit == commit {
				foundCommit = true
			}
		}
		if foundCase && foundCommit {
			selected = append(selected, c)
		}
	}
	trace.Criteria = selected
	impact := make([]acceptance.Impact, 0, len(selected))
	for _, c := range selected {
		impact = append(impact, c.Impact())
	}
	return cli.PrintJSON(cmd.OutOrStdout(), struct {
		*acceptance.Trace
		Impact []acceptance.Impact `json:"impact"`
	}{trace, impact})
}
