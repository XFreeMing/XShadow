package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/multica-ai/multica/server/internal/daemon/execenv"
	"github.com/multica-ai/multica/server/internal/daemon/repocache"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestLocalDirectoryAutoIsolatesOnlyOverlappingIssueRun(t *testing.T) {
	repo := t.TempDir()
	git := func(dir string, args ...string) string {
		t.Helper()
		out, err := runGitGCCommandContext(t.Context(), dir, args...)
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return out
	}
	git(repo, "init", "-b", "dev")
	git(repo, "config", "user.name", "Fixture")
	git(repo, "config", "user.email", "fixture@example.test")
	git(repo, "config", "commit.gpgsign", "false")
	git(repo, "commit", "--allow-empty", "-m", "base")
	d := &Daemon{cfg: Config{DaemonID: "machine"}, localPathLocks: NewLocalPathLocker()}
	task := func(id, issue string) Task {
		return Task{ID: id, IssueID: issue, IssueIdentifier: issue, AgentID: "coder", WorkspaceID: "workspace",
			ProjectResources: []ProjectResourceData{{ResourceType: "local_directory",
				ResourceRef: []byte(fmt.Sprintf(`{"local_path":%q,"daemon_id":"machine","execution_mode":"auto"}`, repo))}}}
	}
	first := task("first", "DEV-1")
	release, abort := d.acquireLocalDirectoryLockIfNeeded(t.Context(), &first, slog.Default())
	if abort || release == nil || first.localDirectoryConcurrent {
		t.Fatal("a lone task did not acquire the original checkout")
	}
	defer release()
	if err := os.WriteFile(filepath.Join(repo, "first-untouched.txt"), []byte("uncommitted"), 0o644); err != nil {
		t.Fatal(err)
	}
	second := task("second", "DEV-2")
	if release, abort := d.acquireLocalDirectoryLockIfNeeded(t.Context(), &second, slog.Default()); abort || release != nil || !second.localDirectoryConcurrent {
		t.Fatal("overlapping issue did not select its own worktree")
	}
	assignment, err := localDirectoryAssignmentForTask(second, "machine")
	if err != nil || !assignment.UsesWorktree() || !assignment.CommittedOnly {
		t.Fatalf("overlapping task has the wrong assignment: %+v %v", assignment, err)
	}
	params := execenv.LocalWorktreeParams{LocalPath: repo, EnvRoot: t.TempDir(), CommittedOnly: assignment.CommittedOnly,
		TaskID: second.ID, AgentName: "agent", WorkspaceID: second.WorkspaceID, AgentID: second.AgentID,
		ConversationID: second.IssueID, ConversationKey: second.IssueIdentifier}
	worktree, err := execenv.PrepareLocalWorktree(params, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(worktree.WorkDir, "first-untouched.txt")); !os.IsNotExist(err) {
		t.Fatalf("the first task's uncommitted work leaked: %v", err)
	}
	git(worktree.WorkDir, "commit", "--allow-empty", "-m", "delivery")
	sha := git(worktree.WorkDir, "rev-parse", "HEAD")
	if _, err := worktree.Finalize(slog.Default()); err != nil {
		t.Fatal(err)
	}
	release()
	followup := task("followup", "DEV-2")
	if hold, abort := d.acquireLocalDirectoryLockIfNeeded(t.Context(), &followup, slog.Default()); abort || hold != nil || !followup.localDirectoryConcurrent {
		t.Fatal("an overlapping delivery lost its branch when the other writer finished")
	}
	checkedOut := filepath.Join(t.TempDir(), "owner-checkout")
	git(repo, "worktree", "add", checkedOut, worktree.Branch)
	if confirmed, err := execenv.ConfirmDeliveredLocalBranch(repo, worktree.Branch, sha, "workspace", "coder", "DEV-2", slog.Default()); err != nil || confirmed {
		t.Fatalf("reclaiming the owner's checkout must wait: %v %v", confirmed, err)
	}
	git(repo, "worktree", "remove", checkedOut)
	if confirmed, err := execenv.ConfirmDeliveredLocalBranch(repo, worktree.Branch, sha, "workspace", "coder", "DEV-2", slog.Default()); err != nil || !confirmed {
		t.Fatalf("confirm merged branch: %v %v", confirmed, err)
	}
	if _, err := runGitGCCommandContext(t.Context(), repo, "show-ref", "--verify", "refs/heads/"+worktree.Branch); err == nil {
		t.Fatal("the merged branch is still listed")
	}
	if got := git(repo, "rev-parse", "refs/multica/merged-deliveries/"+sha); got != sha {
		t.Fatalf("the original commit must remain recoverable: %s", got)
	}
	if confirmed, err := execenv.ConfirmDeliveredLocalBranch(repo, worktree.Branch, sha, "workspace", "coder", "DEV-2", slog.Default()); err != nil || !confirmed {
		t.Fatalf("reconcile an already reclaimed branch: %v %v", confirmed, err)
	}
	next := task("next", "DEV-2")
	hold, abort := d.acquireLocalDirectoryLockIfNeeded(t.Context(), &next, slog.Default())
	if abort || hold == nil || next.localDirectoryConcurrent {
		t.Fatal("a later lone task did not return to the original checkout")
	}
	hold()
}

func TestReclaimMergedGitHubCheckout(t *testing.T) {
	for _, layout := range []string{"clone", "worktree"} {
		t.Run(layout, func(t *testing.T) {
			root := t.TempDir()
			repo := filepath.Join(root, "cache", "repo")
			if err := os.MkdirAll(repo, 0o755); err != nil {
				t.Fatal(err)
			}
			git := func(dir string, args ...string) string {
				t.Helper()
				out, err := runGitGCCommandContext(t.Context(), dir, args...)
				if err != nil {
					t.Fatalf("git %v: %v: %s", args, err, out)
				}
				return out
			}
			git(repo, "init", "-b", "dev")
			git(repo, "config", "user.name", "Fixture")
			git(repo, "config", "user.email", "fixture@example.test")
			git(repo, "config", "commit.gpgsign", "false")
			git(repo, "commit", "--allow-empty", "-m", "base")
			git(repo, "remote", "add", "origin", "git@github.com:acme/app.git")
			globalConfig := filepath.Join(root, "gitconfig")
			if err := os.WriteFile(globalConfig, []byte("[multica]\n\tcheckout-mode = isolated\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("GIT_CONFIG_GLOBAL", globalConfig)
			if repocache.IsManagedClone(t.Context(), repo) {
				t.Fatal("a global Git setting must not establish checkout ownership")
			}
			git(repo, "config", "multica.checkout-mode", "isolated")
			checkout := repo
			branch := "agent/coder/dev-1"
			if layout == "worktree" {
				checkout = filepath.Join(root, "worktree")
				git(repo, "worktree", "add", "-b", branch, checkout)
			} else {
				git(repo, "switch", "-c", branch)
			}
			if err := os.WriteFile(filepath.Join(checkout, "delivery.txt"), []byte("new behavior"), 0o644); err != nil {
				t.Fatal(err)
			}
			git(checkout, "add", "delivery.txt")
			git(checkout, "commit", "-m", "delivery")
			sha := git(checkout, "rev-parse", "HEAD")
			if layout == "clone" {
				git(repo, "switch", "dev")
			}
			git(repo, "merge", "--squash", branch)
			git(repo, "commit", "-m", "squashed delivery")
			if _, err := runGitGCCommandContext(t.Context(), repo, "merge-base", "--is-ancestor", sha, "dev"); err == nil {
				t.Fatal("squash unexpectedly preserved the original commit in dev")
			}
			if layout == "clone" {
				git(repo, "switch", branch)
				git(repo, "update-ref", "refs/remotes/origin/dev", git(repo, "rev-parse", "dev"))
				git(repo, "branch", "-D", "dev")
			}
			heads := []protocol.MergedPullRequestHead{{Repository: "acme/app", Branch: branch, SHA: sha}}
			dirty := filepath.Join(checkout, "untracked.txt")
			if err := os.WriteFile(dirty, []byte("unsaved work"), 0o644); err != nil {
				t.Fatal(err)
			}
			for _, proof := range [][]protocol.MergedPullRequestHead{
				{{Repository: "acme/other", Branch: branch, SHA: sha}},
				{{Repository: "acme/app", Branch: "agent/coder/dev-2", SHA: sha}},
				{{Repository: "acme/app", Branch: branch, SHA: "another-head"}},
				heads,
			} {
				if got, err := reclaimMergedGitHubCheckout(context.Background(), checkout, filepath.Join(root, "cache"), proof); err != nil || got {
					t.Fatalf("reclaimed checkout with %v: %v %v", proof, got, err)
				}
			}
			if err := os.Remove(dirty); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(repo, ".git", "info", "exclude"), []byte("local.scratch\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			ignored := filepath.Join(checkout, "local.scratch")
			if err := os.WriteFile(ignored, []byte("private output"), 0o644); err != nil {
				t.Fatal(err)
			}
			if got, err := reclaimMergedGitHubCheckout(t.Context(), checkout, filepath.Join(root, "cache"), heads); err != nil || got {
				t.Fatalf("reclaimed checkout with ignored work: %v %v", got, err)
			}
			if err := os.Remove(ignored); err != nil {
				t.Fatal(err)
			}
			if layout == "clone" {
				git(checkout, "update-ref", "refs/stash", sha)
				if got, err := reclaimMergedGitHubCheckout(t.Context(), checkout, filepath.Join(root, "cache"), heads); err != nil || got {
					t.Fatalf("reclaimed checkout with stash: %v %v", got, err)
				}
				git(checkout, "update-ref", "-d", "refs/stash")
			}
			if got, err := reclaimMergedGitHubCheckout(t.Context(), checkout, filepath.Join(root, "cache"), heads); err != nil || !got {
				t.Fatalf("merged checkout remained: %v %v", got, err)
			}
			if _, err := os.Lstat(checkout); !os.IsNotExist(err) {
				t.Fatalf("checkout still exists: %v", err)
			}
			if layout == "worktree" {
				cmd := exec.Command("git", "-C", repo, "worktree", "remove", checkout)
				if err := cmd.Run(); err == nil {
					t.Fatal("the worktree registration remained")
				}
			}
		})
	}
}
