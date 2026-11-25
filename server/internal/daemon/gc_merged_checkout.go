package daemon

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
	"github.com/multica-ai/multica/server/internal/daemon/repocache"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func (d *Daemon) runMergedCheckoutGC(ctx context.Context) {
	entries, err := os.ReadDir(d.cfg.WorkspacesRoot)
	if err != nil {
		return
	}
	stats := &gcStats{}
	for _, entry := range entries {
		if ctx.Err() != nil {
			return
		}
		if entry.IsDir() && entry.Type()&linkedDirModes == 0 && !strings.HasPrefix(entry.Name(), ".") {
			d.gcWorkspaceWithPolicy(ctx, filepath.Join(d.cfg.WorkspacesRoot, entry.Name()), stats, true)
		}
	}
}

func hasManagedCheckout(taskDir string) bool {
	workDir := filepath.Join(taskDir, "workdir")
	info, err := os.Lstat(workDir)
	if err != nil || !info.IsDir() || info.Mode()&linkedDirModes != 0 {
		return false
	}
	entries, err := os.ReadDir(workDir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.IsDir() && entry.Type()&linkedDirModes == 0 {
			if _, err := os.Lstat(filepath.Join(workDir, entry.Name(), ".git")); err == nil {
				return true
			}
		}
	}
	return false
}

func (d *Daemon) reclaimMergedCheckouts(ctx context.Context, candidate issueGCCandidate, heads []protocol.MergedPullRequestHead) {
	taskDir := candidate.taskDir
	release, ok := d.reserveEnvRootForGC(taskDir)
	if !ok {
		return
	}
	defer release()
	owner, err := d.gcTaskDirOwner(taskDir)
	if err != nil || owner.WorkspaceID != candidate.meta.WorkspaceID || d.environments.servesFrom(taskDir) {
		return
	}
	// Another run can reuse an environment while the daemon is reading the
	// batch issue state. Its metadata must still identify the same run.
	current, err := execenv.ReadGCMeta(taskDir)
	if err != nil || current.LocalDirectory || current.TaskID != candidate.meta.TaskID ||
		current.IssueID != candidate.meta.IssueID || current.WorkspaceID != candidate.meta.WorkspaceID ||
		!current.CompletedAt.Equal(candidate.meta.CompletedAt) {
		return
	}
	if delivery := current.LocalDelivery; delivery != nil {
		if d.reclaimMergedLocalBranch(ctx, current, delivery, heads) {
			current.LocalDelivery = nil
			if err := execenv.WriteGCMeta(taskDir, *current, d.logger); err != nil {
				d.logger.Warn("gc: update local delivery metadata failed", "error", err)
			}
		}
	}
	workDir := filepath.Join(taskDir, "workdir")
	info, err := os.Lstat(workDir)
	if err != nil || !info.IsDir() || info.Mode()&linkedDirModes != 0 {
		return
	}
	entries, err := os.ReadDir(workDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		// repo checkout creates one direct child for each managed repository.
		// Preserve the run logs, outputs and any unrelated checkouts.
		if entry.IsDir() && entry.Type()&linkedDirModes == 0 {
			checkout := filepath.Join(workDir, entry.Name())
			cacheDir := filepath.Join(d.cfg.WorkspacesRoot, reposDirName, current.WorkspaceID)
			if reclaimed, err := reclaimMergedGitHubCheckout(ctx, checkout, cacheDir, heads); err != nil {
				d.logger.Warn("gc: merged checkout cleanup failed", "dir", checkout, "error", err)
			} else if reclaimed {
				d.logger.Info("gc: reclaimed merged checkout", "dir", checkout, "issue_id", current.IssueID)
			}
		}
	}
}

func (d *Daemon) reclaimMergedLocalBranch(ctx context.Context, meta *execenv.GCMeta, delivery *execenv.LocalBranchDelivery, heads []protocol.MergedPullRequestHead) bool {
	origin, err := runGitGCCommandContext(ctx, delivery.Directory, "config", "--local", "--get", "remote.origin.url")
	if err != nil {
		return false
	}
	repository := gitHubCheckoutRepository(origin)
	if repository == "" {
		return false
	}
	for _, head := range heads {
		if head.Branch != delivery.Branch || !strings.EqualFold(repository, head.Repository) {
			continue
		}
		confirmed, err := execenv.ConfirmDeliveredLocalBranch(delivery.Directory, delivery.Branch, head.SHA,
			meta.WorkspaceID, delivery.AgentID, meta.IssueID, d.logger)
		if err != nil {
			d.logger.Warn("gc: merged local branch cleanup failed", "branch", delivery.Branch, "error", err)
		} else if confirmed {
			d.logger.Info("gc: reclaimed merged local branch", "branch", delivery.Branch, "issue_id", meta.IssueID)
			return true
		}
	}
	return false
}

func reclaimMergedGitHubCheckout(ctx context.Context, checkout, cacheDir string, heads []protocol.MergedPullRequestHead) (bool, error) {
	info, err := os.Lstat(filepath.Join(checkout, ".git"))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&linkedDirModes != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
		return false, nil
	}
	if info.IsDir() {
		if !repocache.IsManagedClone(ctx, checkout) {
			return false, nil
		}
	} else {
		common, err := runGitGCCommandContext(ctx, checkout, "rev-parse", "--path-format=absolute", "--git-common-dir")
		if err != nil || !isPathWithin(cacheDir, common) || cacheDir == common {
			return false, err
		}
	}
	origin, err := runGitGCCommandContext(ctx, checkout, "config", "--local", "--get", "remote.origin.url")
	if err != nil {
		return false, err
	}
	repository := gitHubCheckoutRepository(origin)
	if repository == "" || !slices.ContainsFunc(heads, func(head protocol.MergedPullRequestHead) bool {
		return strings.EqualFold(repository, head.Repository)
	}) {
		return false, nil
	}
	branch, err := runGitGCCommandContext(ctx, checkout, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil || !strings.HasPrefix(branch, "agent/") {
		return false, nil
	}
	sha, err := runGitGCCommandContext(ctx, checkout, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return false, err
	}
	if !slices.ContainsFunc(heads, func(head protocol.MergedPullRequestHead) bool {
		return strings.EqualFold(repository, head.Repository) && head.Branch == branch && head.SHA == sha
	}) {
		return false, nil
	}
	status, err := runGitGCCommandContext(ctx, checkout, "status", "--porcelain", "--untracked-files=all", "--ignored=matching", "--ignore-submodules=none")
	if err != nil || status != "" {
		return false, err
	}
	if info.IsDir() {
		refs, err := runGitGCCommandContext(ctx, checkout, "for-each-ref", "--format=%(refname)")
		if err != nil {
			return false, err
		}
		for ref := range strings.SplitSeq(refs, "\n") {
			if ref != "refs/heads/"+branch && !strings.HasPrefix(ref, "refs/remotes/origin/") {
				return false, nil
			}
		}
		worktrees, err := linkedWorktreeCountContext(ctx, checkout)
		if err != nil || worktrees != 1 {
			return false, err
		}
		err = os.RemoveAll(checkout)
	} else {
		_, err = runGitGCCommandContext(ctx, checkout, "worktree", "remove", "--", checkout)
	}
	return err == nil, err
}

func gitHubCheckoutRepository(origin string) string {
	if strings.HasPrefix(origin, "git@github.com:") {
		origin = "ssh://git@github.com/" + strings.TrimPrefix(origin, "git@github.com:")
	}
	remote, err := url.Parse(origin)
	if err != nil || !strings.EqualFold(remote.Host, "github.com") ||
		(remote.Scheme != "https" && remote.Scheme != "ssh") {
		return ""
	}
	slug := strings.TrimSuffix(strings.Trim(remote.Path, "/"), ".git")
	owner, name, ok := strings.Cut(slug, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return ""
	}
	return slug
}
