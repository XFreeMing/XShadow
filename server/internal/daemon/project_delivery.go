package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/daemon/execenv"
	"github.com/multica-ai/multica/server/pkg/workmethod"
)

// This is an owner-configured local credential. Project authors can select a
// target, while only an operator of this machine can authorize delivery to it.
const (
	projectDeliveryReposEnv = "MULTICA_GITHUB_ACTIONS_DELIVERY_REPOS"
	projectDeliveryGHEnv    = "MULTICA_GITHUB_ACTIONS_DELIVERY_GH"
)

func (d *Daemon) projectDeliveryLoop(ctx context.Context) {
	allowed := strings.FieldsFunc(strings.ToLower(os.Getenv(projectDeliveryReposEnv)), func(r rune) bool { return r == ',' || r == ' ' })
	if len(allowed) == 0 {
		return
	}
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		d.deliverApprovedProjects(ctx, allowed)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (d *Daemon) deliverApprovedProjects(ctx context.Context, allowed []string) {
	type destination struct{ workspace, runtime string }
	var destinations []destination
	d.mu.Lock()
	for wsID, ws := range d.workspaces {
		if len(ws.runtimeIDs) != 0 {
			destinations = append(destinations, destination{wsID, ws.runtimeIDs[0]})
		}
	}
	d.mu.Unlock()
	for _, dest := range destinations {
		if ctx.Err() != nil {
			return
		}
		path := "/api/daemon/runtimes/" + url.PathEscape(dest.runtime) + "/project-deliveries"
		var jobs []workmethod.DeliveryJob
		if err := d.client.getJSON(ctx, path, &jobs); err != nil {
			continue
		}
		for _, job := range jobs {
			jobCtx, cancel := context.WithTimeout(ctx, 50*time.Second)
			receipt, err := reconcileProjectDelivery(jobCtx, job, allowed, func() (bool, error) {
				return execenv.ReclaimApprovedProjectBranch(job.Directory, job.Branch, job.Head, dest.workspace, job.IssueID, d.logger)
			})
			if err != nil {
				d.logger.Warn("project delivery reconciliation failed", "issue_id", job.IssueID, "error", err)
			}
			if receipt.Valid() && receipt != job.Receipt {
				report := map[string]any{"revision": job.Revision, "head": job.Head, "receipt": receipt}
				if err := d.client.postJSON(jobCtx, path+"/"+url.PathEscape(job.IssueID), report, nil); err != nil {
					d.logger.Warn("project delivery receipt not saved", "issue_id", job.IssueID, "error", err)
				}
			}
			cancel()
		}
	}
}

type actionsPullRequest struct {
	Number    int    `json:"number"`
	State     string `json:"state"`
	Merged    bool   `json:"merged"`
	Draft     bool   `json:"draft"`
	Merge     string `json:"merge_commit_sha"`
	Mergeable string `json:"mergeable_state"`
	Head      actionsPullRef
	Base      actionsPullRef
}

type actionsPullRef struct {
	SHA  string
	Ref  string
	Repo struct {
		FullName string `json:"full_name"`
	}
}

type projectDeliveryRemote struct {
	gh, root string
	job      workmethod.DeliveryJob
	repo     string
	owner    string
	branch   string
}

func reconcileProjectDelivery(ctx context.Context, job workmethod.DeliveryJob, allowed []string, reclaim func() (bool, error)) (workmethod.DeliveryReceipt, error) {
	setup := job.Receipt
	if setup.PullRequest == 0 {
		setup = workmethod.DeliveryReceipt{State: workmethod.DeliverySetup}
	}
	issue, parseErr := uuid.Parse(job.IssueID)
	if parseErr != nil || !job.Target.Valid() || job.Target.BaseRef != "dev" ||
		!validDeliverySHA(job.Head) || !slices.Contains(allowed, strings.ToLower(job.Target.Repository)) {
		return setup, errors.New("repository is not enabled for GitHub Actions delivery on this machine")
	}
	ghCommand := strings.TrimSpace(os.Getenv(projectDeliveryGHEnv))
	if ghCommand == "" {
		ghCommand = "gh"
	}
	gh, err := exec.LookPath(ghCommand)
	if err != nil {
		return setup, errors.New("GitHub CLI is required for project delivery")
	}
	root := execenv.LocalCheckoutRoot(job.Directory)
	if root == "" {
		return setup, errors.New("delivery directory is not an available Git checkout")
	}
	for _, flag := range [][]string{{"remote", "get-url", "origin"}, {"remote", "get-url", "--push", "origin"}} {
		remote, err := runGitGCCommandContext(ctx, root, flag...)
		if err != nil || !strings.EqualFold(gitHubCheckoutRepository(remote), job.Target.Repository) {
			return setup, errors.New("configured fetch and push remotes must match the approved GitHub repository")
		}
	}
	owner, _, _ := strings.Cut(job.Target.Repository, "/")
	remote := projectDeliveryRemote{gh: gh, root: root, job: job, repo: "repos/" + job.Target.Repository, owner: owner,
		branch: "codex/deliveries/" + issue.String() + "-" + job.Head}
	var account struct{ Login string }
	if err := remote.api(ctx, "GET", "user", nil, &account); err != nil || !strings.EqualFold(account.Login, owner) {
		return setup, errors.New("authenticate GitHub CLI as the repository owner to request delivery")
	}
	return remote.reconcile(ctx, reclaim)
}

func (r projectDeliveryRemote) reconcile(ctx context.Context, reclaim func() (bool, error)) (workmethod.DeliveryReceipt, error) {
	pr, err := r.findPullRequest(ctx)
	if err != nil {
		return r.job.Receipt, err
	}
	if pr.Number == 0 {
		if err := r.ensureRemoteRef(ctx, "refs/heads/"+r.branch, r.job.Head); err != nil {
			return r.job.Receipt, err
		}
		input := map[string]string{"head": r.branch, "base": r.job.Target.BaseRef,
			"title": r.job.Identifier + " reviewed delivery", "body": "XShadow independent review approved `" + r.job.Head + "`.\n\nRefs " + r.job.Identifier}
		if err := r.api(ctx, "POST", r.repo+"/pulls", input, &pr); err != nil {
			pr, _ = r.findPullRequest(ctx)
			if pr.Number == 0 {
				return r.job.Receipt, err
			}
		}
	}
	pullPath := r.repo + "/pulls/" + strconv.Itoa(pr.Number)
	if err := r.api(ctx, "GET", pullPath, nil, &pr); err != nil {
		return r.job.Receipt, err
	}
	if !r.matches(pr) {
		return r.job.Receipt, errors.New("GitHub PR no longer targets the approved delivery")
	}
	receipt := workmethod.DeliveryReceipt{PullRequest: pr.Number, Base: pr.Base.SHA, State: workmethod.DeliveryBlocked}
	if pr.Merged {
		return r.reclaimAfterMerge(ctx, pr, receipt, reclaim)
	}
	base, err := r.remoteTip(ctx, "refs/heads/"+r.job.Target.BaseRef)
	if err != nil || base == "" {
		return r.job.Receipt, errors.New("could not verify the current GitHub target branch")
	}
	receipt.Base = base
	if pr.State != "open" || pr.Draft || pr.Mergeable == "dirty" {
		return receipt, nil
	}
	if r.job.Receipt.PullRequest == pr.Number && r.job.Receipt.Base == receipt.Base {
		state, err := r.checkAction(ctx, receipt)
		if err != nil {
			return r.job.Receipt, err
		}
		if state != "" {
			receipt.State = state
			return receipt, nil
		}
		if r.job.Receipt.State == workmethod.DeliveryWaiting {
			return r.job.Receipt, nil
		}
	}
	if err := r.requestMerge(ctx, receipt); err != nil {
		return r.job.Receipt, err
	}
	receipt.State = workmethod.DeliveryWaiting
	return receipt, nil
}

func (r projectDeliveryRemote) findPullRequest(ctx context.Context) (actionsPullRequest, error) {
	if number := r.job.Receipt.PullRequest; number != 0 {
		return actionsPullRequest{Number: number}, nil
	}
	query := url.Values{"state": {"all"}, "head": {r.owner + ":" + r.branch}, "base": {r.job.Target.BaseRef}}
	var prs []actionsPullRequest
	if err := r.api(ctx, "GET", r.repo+"/pulls?"+query.Encode(), nil, &prs); err != nil {
		return actionsPullRequest{}, err
	}
	for _, pr := range prs {
		if r.matches(pr) {
			return pr, nil
		}
	}
	if len(prs) != 0 {
		return actionsPullRequest{}, errors.New("existing GitHub PR differs from the approved delivery")
	}
	return actionsPullRequest{}, nil
}

func (r projectDeliveryRemote) matches(pr actionsPullRequest) bool {
	return pr.Number > 0 && pr.Head.Ref == r.branch && pr.Head.SHA == r.job.Head &&
		pr.Base.Ref == r.job.Target.BaseRef && validDeliverySHA(pr.Base.SHA) &&
		strings.EqualFold(pr.Head.Repo.FullName, r.job.Target.Repository) && strings.EqualFold(pr.Base.Repo.FullName, r.job.Target.Repository)
}

func (r projectDeliveryRemote) reclaimAfterMerge(ctx context.Context, pr actionsPullRequest, receipt workmethod.DeliveryReceipt, reclaim func() (bool, error)) (workmethod.DeliveryReceipt, error) {
	var comparison struct{ Status string }
	if !validDeliverySHA(pr.Merge) {
		return r.job.Receipt, errors.New("merged PR has no valid merge commit")
	}
	if err := r.api(ctx, "GET", r.repo+"/compare/"+pr.Merge+"..."+r.job.Target.BaseRef, nil, &comparison); err != nil {
		return r.job.Receipt, err
	}
	if comparison.Status != "identical" && comparison.Status != "ahead" {
		return r.job.Receipt, errors.New("GitHub merge commit is absent from the target branch")
	}
	receipt.Merge, receipt.State = pr.Merge, workmethod.DeliveryCleanup
	ref := "refs/heads/" + r.branch
	tip, err := r.remoteTip(ctx, ref)
	switch {
	case err != nil:
		return receipt, err
	case tip != "" && tip != r.job.Head:
		return receipt, errors.New("the delivery branch advanced after approval")
	case tip == r.job.Head:
		if _, err := runGitGCCommandContext(ctx, r.root, "push", "--force-with-lease="+ref+":"+tip, "origin", ":"+ref); err != nil {
			return receipt, errors.New("could not retire the merged GitHub branch")
		}
	}
	tracking := "refs/remotes/origin/" + r.branch
	local, err := runGitGCCommandContext(ctx, r.root, "for-each-ref", "--format=%(refname) %(objectname)", tracking)
	if err != nil {
		return receipt, errors.New("could not read the remote-tracking delivery branch")
	}
	if local == tracking+" "+r.job.Head {
		if _, err := runGitGCCommandContext(ctx, r.root, "update-ref", "-d", tracking, r.job.Head); err != nil {
			return receipt, errors.New("could not retire the exact remote-tracking delivery branch")
		}
	}
	pattern := "refs/heads/codex/merge-requests/" + strconv.Itoa(pr.Number) + "-" + r.job.Head + "-*"
	requests, err := runGitGCCommandContext(ctx, r.root, "ls-remote", "--refs", "origin", pattern)
	if err != nil || requests != "" {
		return receipt, err
	}
	if err := r.reclaimRequestTrackingRefs(ctx, pr.Number); err != nil {
		return receipt, err
	}
	if done, err := reclaim(); err != nil || !done {
		return receipt, err
	}
	receipt.State = workmethod.DeliveryDone
	return receipt, nil
}

func (r projectDeliveryRemote) reclaimRequestTrackingRefs(ctx context.Context, number int) error {
	prefix := "refs/remotes/origin/codex/merge-requests/" + strconv.Itoa(number) + "-" + r.job.Head + "-"
	output, err := runGitGCCommandContext(ctx, r.root, "for-each-ref", "--format=%(refname) %(objectname)", prefix+"*")
	if err != nil {
		return errors.New("could not read local merge-request tracking refs")
	}
	for line := range strings.SplitSeq(output, "\n") {
		ref, sha, ok := strings.Cut(line, " ")
		if ok && strings.HasPrefix(ref, prefix) && validDeliverySHA(strings.TrimPrefix(ref, prefix)) && validDeliverySHA(sha) {
			if _, err := runGitGCCommandContext(ctx, r.root, "update-ref", "-d", ref, sha); err != nil {
				return errors.New("could not retire the merge-request tracking ref")
			}
		}
	}
	return nil
}

func (r projectDeliveryRemote) remoteTip(ctx context.Context, ref string) (string, error) {
	out, err := runGitGCCommandContext(ctx, r.root, "ls-remote", "--refs", "origin", ref)
	if err != nil {
		return "", errors.New("could not read GitHub delivery ref")
	}
	if out == "" {
		return "", nil
	}
	head, name, ok := strings.Cut(out, "\t")
	if !ok || name != ref || !validDeliverySHA(head) {
		return "", errors.New("unexpected GitHub delivery ref")
	}
	return head, nil
}

func (r projectDeliveryRemote) ensureRemoteRef(ctx context.Context, ref, commit string) error {
	tip, err := r.remoteTip(ctx, ref)
	if err != nil {
		return err
	}
	if tip == commit {
		return nil
	}
	if tip != "" {
		return errors.New("GitHub delivery ref already has a different commit")
	}
	if _, err := runGitGCCommandContext(ctx, r.root, "push", "--force-with-lease="+ref+":", "origin", commit+":"+ref); err != nil {
		return errors.New("could not publish the approved GitHub ref")
	}
	return nil
}

func (r projectDeliveryRemote) api(ctx context.Context, method, endpoint string, input, output any) error {
	args := []string{"api", "--hostname", "github.com", "--method", method, endpoint}
	var stdin string
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return err
		}
		stdin = string(data)
		args = append(args, "--input", "-")
	}
	cmd := exec.CommandContext(ctx, r.gh, args...)
	cmd.Stdin = strings.NewReader(stdin)
	response, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("GitHub CLI request failed (%s)", method)
	}
	if err := json.Unmarshal(response, output); err != nil {
		return errors.New("GitHub CLI returned an unreadable response")
	}
	return nil
}

func (r projectDeliveryRemote) mergeRequestBranch(receipt workmethod.DeliveryReceipt) string {
	return "codex/merge-requests/" + strconv.Itoa(receipt.PullRequest) + "-" + r.job.Head + "-" + receipt.Base
}

func (r projectDeliveryRemote) checkAction(ctx context.Context, receipt workmethod.DeliveryReceipt) (string, error) {
	branch := r.mergeRequestBranch(receipt)
	query := url.Values{"branch": {branch}, "event": {"push"}, "per_page": {"1"}}
	var result struct {
		Runs []struct{ Status, Conclusion string } `json:"workflow_runs"`
	}
	if err := r.api(ctx, "GET", r.repo+"/actions/workflows/merge-to-dev.yml/runs?"+query.Encode(), nil, &result); err != nil {
		return "", err
	}
	if len(result.Runs) == 0 {
		tip, err := r.remoteTip(ctx, "refs/heads/"+branch)
		if tip != "" {
			return workmethod.DeliveryWaiting, err
		}
		return "", err
	}
	if run := result.Runs[0]; run.Status == "completed" && run.Conclusion != "success" {
		return workmethod.DeliveryBlocked, nil
	}
	return workmethod.DeliveryWaiting, nil
}

func (r projectDeliveryRemote) requestMerge(ctx context.Context, receipt workmethod.DeliveryReceipt) error {
	if _, err := runGitGCCommandContext(ctx, r.root, "fetch", "--no-tags", "--no-write-fetch-head", "origin", "refs/heads/"+r.job.Target.BaseRef); err != nil {
		return errors.New("could not fetch the verified target commit")
	}
	if _, err := runGitGCCommandContext(ctx, r.root, "cat-file", "-e", receipt.Base+":.github/workflows/merge-to-dev.yml"); err != nil {
		return errors.New("target branch does not contain the merge-to-dev Actions workflow")
	}
	dir, err := os.MkdirTemp("", "xshadow-github-delivery-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	const manifest = ".github/merge-to-dev.json"
	git := func(input string, args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", r.root}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_INDEX_FILE="+filepath.Join(dir, "index"))
		cmd.Stdin = strings.NewReader(input)
		out, err := cmd.Output()
		return strings.TrimSpace(string(out)), err
	}
	if _, err := git("", "read-tree", receipt.Base); err != nil {
		return errors.New("could not stage the approved merge request base")
	}
	input, err := json.Marshal(map[string]any{"pr": receipt.PullRequest, "head_sha": r.job.Head, "base_sha": receipt.Base})
	if err != nil {
		return err
	}
	blob, err := git(string(input)+"\n", "hash-object", "-w", "--stdin")
	if err != nil {
		return errors.New("could not create the merge request manifest")
	}
	if _, err := git("", "update-index", "--add", "--cacheinfo", "100644,"+blob+","+manifest); err != nil {
		return errors.New("could not stage the merge request manifest")
	}
	tree, err := git("", "write-tree")
	if err != nil {
		return errors.New("could not create the merge request tree")
	}
	date, err := git("", "show", "-s", "--format=%cI", receipt.Base)
	if err != nil {
		return errors.New("could not read the reviewed target commit date")
	}
	cmd := exec.CommandContext(ctx, "git", "-C", r.root, "-c", "user.name=XShadow", "-c",
		"user.email=xshadow@users.noreply.github.com", "-c", "commit.gpgSign=false",
		"commit-tree", tree, "-p", receipt.Base, "-m", "Request GitHub Actions merge of #"+strconv.Itoa(receipt.PullRequest))
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date)
	out, err := cmd.Output()
	if err != nil {
		return errors.New("could not create the merge request commit")
	}
	return r.ensureRemoteRef(ctx, "refs/heads/"+r.mergeRequestBranch(receipt), strings.TrimSpace(string(out)))
}

func validDeliverySHA(sha string) bool {
	return len(sha) == 40 && strings.Trim(sha, "0123456789abcdef") == ""
}
