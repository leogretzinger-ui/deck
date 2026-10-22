package supervisor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// This is the positive half of the task-worktree Git contract. Kernel denial for
// the nested read-only covers remains real-host Phase A evidence.
func TestTaskWorktreeGitVectorSupportsOrdinaryAndRootMetadataWrites(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	origin, seed, assigned := filepath.Join(root, "origin.git"), filepath.Join(root, "seed"), filepath.Join(root, "assigned")
	gitRun(t, root, "init", "--bare", "--initial-branch=main", origin)
	gitRun(t, root, "clone", origin, seed)
	gitRun(t, seed, "config", "user.name", "Task probe")
	gitRun(t, seed, "config", "user.email", "task-probe@example.invalid")
	gitRun(t, seed, "config", "gc.auto", "1")
	gitRun(t, seed, "config", "gc.autoDetach", "false")
	gitRun(t, seed, "config", "gc.writeCommitGraph", "false")
	writeProbeCommit(t, seed, "one", "one")
	writeProbeCommit(t, seed, "two", "two")
	gitRun(t, seed, "push", "-u", "origin", "main")
	gitRun(t, seed, "worktree", "add", "-b", "task-probe", assigned, "main")

	common := filepath.Join(seed, ".git")
	adminName := strings.TrimSpace(gitOutput(t, assigned, "rev-parse", "--git-path", "commondir"))
	_ = adminName // rev-parse itself is part of the positive linked-worktree probe.
	alternates := filepath.Join(common, "objects", "info", "alternates")
	if err := os.WriteFile(alternates, nil, 0644); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(common, "config"), filepath.Join(common, "hooks"), filepath.Join(common, "info"), alternates} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0444)
		if info.IsDir() {
			mode = 0555
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(path, info.Mode().Perm())
	}

	writeProbeCommit(t, assigned, "task", "task")
	gitRun(t, assigned, "branch", "created")
	gitRun(t, assigned, "branch", "-m", "created", "renamed")
	gitRun(t, assigned, "branch", "-D", "renamed")
	gitRun(t, assigned, "pack-refs", "--all", "--prune")
	if _, err := os.Stat(filepath.Join(common, "packed-refs")); err != nil {
		t.Fatalf("packed refs unavailable: %v", err)
	}
	gitRun(t, assigned, "push", "-u", "origin", "task-probe")
	gitRun(t, assigned, "gc", "--auto")
	gitRun(t, assigned, "commit-graph", "write", "--reachable")
	if _, err := os.Stat(filepath.Join(common, "objects", "info", "commit-graph")); err != nil {
		t.Fatalf("ordinary objects/info maintenance is unavailable: %v", err)
	}

	shallow := filepath.Join(root, "shallow")
	gitRun(t, root, "clone", "--depth=1", "file://"+origin, shallow)
	if count := strings.TrimSpace(gitOutput(t, shallow, "rev-list", "--count", "HEAD")); count != "1" {
		t.Fatalf("clone is not shallow: %s", count)
	}
	gitRun(t, shallow, "fetch", "--deepen=1", "origin", "main")
	count, err := strconv.Atoi(strings.TrimSpace(gitOutput(t, shallow, "rev-list", "--count", "HEAD")))
	if err != nil || count < 2 {
		t.Fatalf("shallow deepen failed: count=%d err=%v", count, err)
	}
}

func writeProbeCommit(t *testing.T, repo, name, body string) {
	t.Helper()
	path := filepath.Join(repo, name+".txt")
	if err := os.WriteFile(path, []byte(body+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", name+".txt")
	gitRun(t, repo, "commit", "-m", name)
}

func gitRun(t *testing.T, dir string, args ...string) { t.Helper(); _ = gitOutput(t, dir, args...) }
func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}
