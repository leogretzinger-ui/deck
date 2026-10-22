package deploy

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestTaskSourceVerifierRequiresExactCleanHead(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is unavailable")
	}

	repo := t.TempDir()
	runGit := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(git, append([]string{"-C", repo}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	runGit("init", "--quiet")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("release fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit("add", "README.md")
	runGit("-c", "user.name=Release fixture", "-c", "user.email=release@example.invalid", "commit", "--quiet", "-m", "fixture")
	head := runGit("rev-parse", "HEAD")

	verifier := filepath.Join("..", "scripts", "verify-noise-tasks-source.sh")
	runVerifier := func(expected string) (string, error) {
		t.Helper()
		cmd := exec.Command(verifier, repo, expected)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := runVerifier(head); err != nil {
		t.Fatalf("exact clean HEAD refused: %v: %s", err, out)
	}

	wrong := strings.Repeat("0", 40)
	out, err := runVerifier(wrong)
	if err == nil {
		t.Fatal("mismatched source HEAD was accepted")
	}
	if !strings.Contains(out, "HEAD is "+head+", require "+wrong) {
		t.Fatalf("mismatch diagnostic = %q", out)
	}

	if err := os.WriteFile(filepath.Join(repo, "UNTRACKED"), []byte("not release source\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err = runVerifier(head)
	if err == nil {
		t.Fatal("dirty source at the exact HEAD was accepted")
	}
	if !strings.Contains(out, "has uncommitted or untracked files") {
		t.Fatalf("dirty-source diagnostic = %q", out)
	}
}
