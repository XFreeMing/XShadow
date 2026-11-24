package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/workmethod"
)

var gitHubCommitSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

type gitHubAutoMergeRequest struct {
	HeadSHA string `json:"head_sha"`
	BaseRef string `json:"base_ref"`
}

type gitHubMergePR struct {
	NodeID         string               `json:"node_id"`
	State          string               `json:"state"`
	Draft          bool                 `json:"draft"`
	Merged         bool                 `json:"merged"`
	MergeCommitSHA string               `json:"merge_commit_sha"`
	MergeableState string               `json:"mergeable_state"`
	AutoMerge      json.RawMessage      `json:"auto_merge"`
	Head           struct{ SHA string } `json:"head"`
	Base           struct{ Ref string } `json:"base"`
}

type gitHubAutoMergeResult struct {
	State    string `json:"state"`
	MergeSHA string `json:"merge_sha,omitempty"`
}

var errGitHubMergeCandidateChanged = errors.New("pull request head or base branch has changed")

func (h *Handler) RequestIssueGitHubAutoMerge(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	prID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "prId"), "pr_id")
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var expected gitHubAutoMergeRequest
	if json.NewDecoder(r.Body).Decode(&expected) != nil || !gitHubCommitSHA.MatchString(expected.HeadSHA) || strings.TrimSpace(expected.BaseRef) == "" {
		writeError(w, http.StatusBadRequest, "supply the full head SHA and target branch")
		return
	}
	ctx := r.Context()
	ws, err := h.Queries.GetWorkspace(ctx, issue.WorkspaceID)
	if err != nil || !githubFeaturesEnabled(ws) {
		writeError(w, http.StatusConflict, "GitHub integration is disabled for this workspace")
		return
	}
	if err := h.checkMethodCompletion(ctx, issue, issuestatus.Done); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if err := verifyAutoMergeReview(issue.WorkMethodRun, expected.HeadSHA); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	prs, err := h.Queries.ListPullRequestsByIssue(ctx, issue.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read issue pull requests")
		return
	}
	linked := false
	for _, pr := range prs {
		if pr.ID == prID {
			linked = true
			break
		}
	}
	if !linked {
		writeError(w, http.StatusNotFound, "linked GitHub pull request not found")
		return
	}
	pr, err := h.Queries.GetGitHubPullRequestInWorkspace(ctx, db.GetGitHubPullRequestInWorkspaceParams{ID: prID, WorkspaceID: issue.WorkspaceID})
	if err != nil {
		writeError(w, http.StatusNotFound, "linked GitHub pull request not found")
		return
	}
	token, installationID, err := h.issueQoderRepositoryToken(ctx, uuidToString(issue.WorkspaceID), strings.ToLower(pr.RepoOwner), strings.ToLower(pr.RepoName))
	if err != nil {
		writeError(w, http.StatusConflict, "the workspace GitHub App needs write access to this repository")
		return
	}
	client := &http.Client{Timeout: 12 * time.Second}
	defer revokeGitHubInstallationToken(client, token)
	if installationID != pr.InstallationID {
		writeError(w, http.StatusConflict, "the pull request installation no longer matches the workspace")
		return
	}
	repository := fmt.Sprintf("/repos/%s/%s", url.PathEscape(pr.RepoOwner), url.PathEscape(pr.RepoName))
	result, err := requestGitHubAutoMerge(ctx, client, githubAPIBase, token, repository, pr.PrNumber, expected)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	h.PRRefresh.Enqueue(pr.InstallationID, pr.RepoOwner, pr.RepoName, pr.PrNumber)
	writeJSON(w, http.StatusOK, result)
}

func verifyAutoMergeReview(raw []byte, head string) error {
	if len(raw) == 0 {
		return nil
	}
	var run workmethod.Run
	if err := json.Unmarshal(raw, &run); err != nil {
		return errors.New("could not read the issue's review")
	}
	if run.Method == nil || run.Method.Kind != "development" {
		return nil
	}
	if run.State == "closed" && len(run.History) > 0 {
		closeout := run.History[len(run.History)-1].Closeout
		if closeout != nil && len(closeout.Criteria) > 0 {
			for _, criterion := range closeout.Criteria {
				if !criterion.Passed || criterion.Candidate != head {
					return errors.New("the pull request head differs from the approved candidate")
				}
			}
			return nil
		}
	}
	return errors.New("complete a development review on the current pull request head")
}

func requestGitHubAutoMerge(ctx context.Context, client *http.Client, apiBase, token, repository string, number int32, expected gitHubAutoMergeRequest) (gitHubAutoMergeResult, error) {
	path := fmt.Sprintf("%s/pulls/%d", repository, number)
	load := func() (gitHubMergePR, error) {
		var pr gitHubMergePR
		err := gitHubMergeJSON(ctx, client, apiBase, token, http.MethodGet, path, nil, &pr)
		if err == nil && (pr.Head.SHA != expected.HeadSHA || pr.Base.Ref != expected.BaseRef) {
			err = errGitHubMergeCandidateChanged
		}
		return pr, err
	}
	pr, err := load()
	if err != nil {
		return gitHubAutoMergeResult{}, err
	}
	if result, complete := pr.mergeResult(); complete {
		return result, nil
	}
	if pr.State != "open" || pr.Draft || pr.NodeID == "" {
		return gitHubAutoMergeResult{}, errors.New("the pull request must be open and ready for review")
	}
	var writeErr error
	if pr.MergeableState == "clean" {
		// GitHub rejects enabling auto-merge once the PR can merge immediately.
		// Its normal merge endpoint still enforces repository rules.
		var response struct {
			Merged bool   `json:"merged"`
			SHA    string `json:"sha"`
		}
		writeErr = gitHubMergeJSON(ctx, client, apiBase, token, http.MethodPut, path+"/merge", map[string]string{
			"sha": expected.HeadSHA, "merge_method": "squash",
		}, &response)
		if writeErr == nil && response.Merged && gitHubCommitSHA.MatchString(response.SHA) {
			return gitHubAutoMergeResult{State: "merged", MergeSHA: response.SHA}, nil
		}
	} else {
		// GitHub may retain auto-merge after a write-authorized push. Its
		// target branch must still require independent review of the new head.
		if err := requireGitHubHeadReview(ctx, client, apiBase, token, repository, expected.BaseRef); err != nil {
			return gitHubAutoMergeResult{}, err
		}
		var response struct {
			Errors []json.RawMessage `json:"errors"`
		}
		writeErr = gitHubMergeJSON(ctx, client, apiBase, token, http.MethodPost, "/graphql", map[string]any{
			"query":     `mutation($id: ID!, $head: GitObjectID!) { enablePullRequestAutoMerge(input: {pullRequestId: $id, expectedHeadOid: $head, mergeMethod: SQUASH}) { pullRequest { id } } }`,
			"variables": map[string]string{"id": pr.NodeID, "head": expected.HeadSHA},
		}, &response)
		if writeErr == nil && len(response.Errors) > 0 {
			writeErr = errors.New("GitHub declined auto-merge; check repository rules, permissions, and the current PR")
		}
	}
	// A failed HTTP response can arrive after GitHub has accepted the write.
	// Always reconcile before reporting a failure or asking the caller to retry.
	updated, err := load()
	if err != nil {
		return gitHubAutoMergeResult{}, err
	}
	if result, complete := updated.mergeResult(); complete {
		return result, nil
	}
	if writeErr != nil {
		return gitHubAutoMergeResult{}, writeErr
	}
	return gitHubAutoMergeResult{}, errors.New("GitHub has not confirmed auto-merge or a merge")
}

func requireGitHubHeadReview(ctx context.Context, client *http.Client, apiBase, token, repository, branch string) error {
	var rules []struct {
		Type       string `json:"type"`
		Parameters struct {
			DismissStaleReviews bool `json:"dismiss_stale_reviews_on_push"`
			RequiredApprovals   int  `json:"required_approving_review_count"`
			Checks              []struct {
				Context string `json:"context"`
			} `json:"required_status_checks"`
		} `json:"parameters"`
	}
	if err := gitHubMergeJSON(ctx, client, apiBase, token, http.MethodGet,
		repository+"/rules/branches/"+url.PathEscape(branch), nil, &rules); err != nil {
		return err
	}
	for _, rule := range rules {
		if rule.Type == "pull_request" && rule.Parameters.DismissStaleReviews && rule.Parameters.RequiredApprovals > 0 {
			return nil
		}
		if rule.Type == "required_status_checks" {
			for _, check := range rule.Parameters.Checks {
				if check.Context == "XShadow Quality Review" {
					return nil
				}
			}
		}
	}
	return errors.New("enable a GitHub ruleset requiring XShadow Quality Review or dismissing stale required GitHub approvals before enabling auto-merge")
}

func (pr gitHubMergePR) mergeResult() (gitHubAutoMergeResult, bool) {
	if pr.Merged && gitHubCommitSHA.MatchString(pr.MergeCommitSHA) {
		return gitHubAutoMergeResult{State: "merged", MergeSHA: pr.MergeCommitSHA}, true
	}
	if pr.State == "open" && !pr.Draft && len(pr.AutoMerge) > 0 && !bytes.Equal(pr.AutoMerge, []byte("null")) {
		return gitHubAutoMergeResult{State: "waiting_for_github"}, true
	}
	return gitHubAutoMergeResult{}, false
}

func gitHubMergeJSON(ctx context.Context, client *http.Client, apiBase, token, method, path string, input, result any) error {
	var body io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(apiBase, "/")+path, body)
	if err != nil {
		return errors.New("could not build GitHub request")
	}
	setGitHubAPIHeaders(req, token)
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return errors.New("GitHub request failed; retry to reconcile the current PR")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("GitHub returned HTTP %d", response.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, githubAPIResponseLimit)).Decode(result); err != nil {
		return errors.New("GitHub returned an invalid response")
	}
	return nil
}
