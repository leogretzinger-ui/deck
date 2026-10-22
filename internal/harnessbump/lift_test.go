package harnessbump

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A real repository: main, a bump tip on a branch, main moving on, and
// the branch brought up to date by a merge, as settle does it.
func TestMergeLiftAcceptsOnlyTheMechanicalUpdateOfAnEvidencedTip(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	origin := t.TempDir()
	run := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(dir, name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run(origin, "init", "-q", "-b", "main")
	write(origin, "Dockerfile", "ARG X=1\n")
	write(origin, "other.txt", "a\n")
	run(origin, "add", "-A")
	run(origin, "commit", "-q", "-m", "base")
	run(origin, "checkout", "-q", "-b", "harness-bump/x-2")
	write(origin, "Dockerfile", "ARG X=2\n")
	run(origin, "commit", "-q", "-am", "pin x 2")
	tip := run(origin, "rev-parse", "HEAD")
	run(origin, "checkout", "-q", "main")
	write(origin, "other.txt", "b\n")
	run(origin, "commit", "-q", "-am", "main moves")
	mainHead := run(origin, "rev-parse", "HEAD")
	// The mechanical update: main merged into the branch, nothing else.
	run(origin, "checkout", "-q", "harness-bump/x-2")
	run(origin, "merge", "-q", "--no-edit", "--no-ff", "main")
	updated := run(origin, "rev-parse", "HEAD")
	// A tampered update: the same merge plus a change of its own.
	run(origin, "checkout", "-q", "-b", "tampered", tip)
	run(origin, "merge", "-q", "--no-edit", "--no-ff", "main")
	write(origin, "other.txt", "c\n")
	run(origin, "commit", "-q", "--amend", "-a", "--no-edit")
	tampered := run(origin, "rev-parse", "HEAD")
	run(origin, "checkout", "-q", "main")

	// settle's root: a clone checked out at main, as the workflow has it.
	root := t.TempDir()
	run(root, "clone", "-q", origin, ".")
	if run(root, "rev-parse", "HEAD") != mainHead {
		t.Fatal("clone is not at main")
	}
	evidenced := func(_ context.Context, _ Harness, _ string, head string) (bool, error) { return head == tip, nil }
	lifted := WithMergeLift(root, evidenced)
	ctx := context.Background()
	if ok, err := lifted(ctx, Harness{}, "2", tip); err != nil || !ok {
		t.Errorf("the evidenced tip itself: %v %v", ok, err)
	}
	if ok, err := lifted(ctx, Harness{}, "2", updated); err != nil || !ok {
		t.Errorf("the mechanical update of the evidenced tip: %v %v", ok, err)
	}
	if ok, err := lifted(ctx, Harness{}, "2", tampered); err != nil || ok {
		t.Errorf("a merge carrying a change of its own was accepted: %v %v", ok, err)
	}
	if ok, err := lifted(ctx, Harness{}, "2", mainHead); err != nil || ok {
		t.Errorf("a plain main commit was accepted: %v %v", ok, err)
	}
}
