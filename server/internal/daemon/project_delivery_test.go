package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/multica-ai/multica/server/pkg/workmethod"
)

func TestProjectActionsRequestPinsReviewedHeadAndBase(t *testing.T) {
	dir, origin := t.TempDir(), t.TempDir()
	git := func(root string, args ...string) string {
		t.Helper()
		output, err := runGitGCCommandContext(t.Context(), root, args...)
		if err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, output)
		}
		return output
	}
	git(origin, "init", "--bare")
	git(dir, "init", "-b", "dev")
	git(dir, "config", "user.name", "Test")
	git(dir, "config", "user.email", "test@example.test")
	workflow := filepath.Join(dir, ".github", "workflows", "merge-to-dev.yml")
	if err := os.MkdirAll(filepath.Dir(workflow), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workflow, []byte("name: Verify delivery\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git(dir, "add", ".github")
	git(dir, "commit", "-m", "Target")
	base := git(dir, "rev-parse", "HEAD")
	git(dir, "remote", "add", "origin", origin)
	git(dir, "push", "origin", "dev")
	git(dir, "commit", "--allow-empty", "-m", "Approved candidate")
	head := git(dir, "rev-parse", "HEAD")
	if err := os.WriteFile(workflow, []byte("User's unrelated edit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git(dir, "add", ".github")
	staged := git(dir, "diff", "--cached")
	r := projectDeliveryRemote{root: dir, job: workmethod.DeliveryJob{Head: head,
		Target: workmethod.GitHubDelivery{BaseRef: "dev"}}}
	receipt := workmethod.DeliveryReceipt{PullRequest: 7, Base: base}
	for range 2 {
		if err := r.requestMerge(t.Context(), receipt); err != nil {
			t.Fatal(err)
		}
	}
	ref := "refs/heads/" + r.mergeRequestBranch(receipt)
	if got := git(origin, "rev-parse", ref+"^"); got != base {
		t.Fatalf("merge request parent %s, want %s", got, base)
	}
	if got := git(origin, "diff-tree", "--no-commit-id", "--name-only", "-r", ref); got != ".github/merge-to-dev.json" {
		t.Fatalf("request altered more than its manifest: %s", got)
	}
	var manifest map[string]any
	if err := json.Unmarshal([]byte(git(origin, "show", ref+":.github/merge-to-dev.json")), &manifest); err != nil ||
		manifest["pr"] != float64(7) || manifest["head_sha"] != head || manifest["base_sha"] != base {
		t.Fatalf("wrong merge request manifest: %+v, %v", manifest, err)
	}
	if git(dir, "diff", "--cached") != staged || git(dir, "rev-parse", "HEAD") != head {
		t.Fatal("delivery changed the developer checkout or index")
	}
	git(origin, "update-ref", ref, base)
	if err := r.requestMerge(t.Context(), receipt); err == nil || git(origin, "rev-parse", ref) != base {
		t.Fatal("request replaced a ref that pointed to a different commit")
	}
}
