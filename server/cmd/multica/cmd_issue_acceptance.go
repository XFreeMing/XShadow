package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/pkg/acceptance"
	"github.com/spf13/cobra"
)

func init() {
	cmd := &cobra.Command{Use: "acceptance <issue>", Short: "Inspect acceptance criteria, test coverage and implementation commits", Args: exactArgs(1), RunE: runIssueAcceptance,
		Example: `  multica issue acceptance DEV-79 --criterion C1
  multica issue acceptance DEV-79 --case T1
  multica issue acceptance DEV-79 --current-case T1
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
	cmd.Flags().String("current-case", "", "Read one case, its current rule, candidate and last run")
	for _, other := range []string{"criterion", "case", "commit", "change-file", "change-stdin"} {
		cmd.MarkFlagsMutuallyExclusive("current-case", other)
	}
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
	if cmd.Flags().Changed("current-case") {
		id, _ := cmd.Flags().GetString("current-case")
		view, err := selectCurrentCase(trace, id)
		if err != nil {
			return err
		}
		return cli.PrintJSON(cmd.OutOrStdout(), view)
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

type currentCaseView struct {
	Revision       int                        `json:"revision"`
	CriterionID    string                     `json:"criterion_id"`
	Version        int                        `json:"criterion_version"`
	Rule           string                     `json:"rule"`
	ID             string                     `json:"case_id"`
	Specification  string                     `json:"specification"`
	Retired        *acceptance.Retirement     `json:"retired,omitempty"`
	Implementation *acceptance.Implementation `json:"current_implementation"`
	LatestRun      *acceptance.Run            `json:"latest_run"`
	Impact         acceptance.Impact          `json:"impact"`
}

func selectCurrentCase(trace *acceptance.Trace, id string) (currentCaseView, error) {
	for _, c := range trace.Criteria {
		for _, tc := range c.Cases {
			if tc.ID != id {
				continue
			}
			version := len(c.Versions)
			if version == 0 {
				return currentCaseView{}, fmt.Errorf("criterion %s has no version", c.ID)
			}
			c.Cases = []acceptance.TestCase{tc}
			view := currentCaseView{
				Revision:      trace.Revision,
				CriterionID:   c.ID,
				Version:       version,
				Rule:          c.Versions[version-1],
				ID:            tc.ID,
				Specification: tc.Specification,
				Retired:       tc.Retired,
				Impact:        c.Impact(),
			}
			for i := len(c.Implementations) - 1; i >= 0; i-- {
				if c.Implementations[i].Version == version {
					view.Implementation = &c.Implementations[i]
					break
				}
			}
			if len(tc.Runs) > 0 {
				view.LatestRun = &tc.Runs[len(tc.Runs)-1]
			}
			return view, nil
		}
	}
	return currentCaseView{}, fmt.Errorf("case %q was not found in this issue", id)
}
