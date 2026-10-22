package project

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

var fixtureIdentity = GitIdentity{Name: "Test Author", Email: "test@example.invalid"}

func TestGitBooleanTrueAcceptsGitTruthySpellingsOnly(t *testing.T) {
	for _, value := range []string{"true", "TRUE", "yes", "on", "1"} {
		if !gitBooleanTrue(value) {
			t.Errorf("gitBooleanTrue(%q) = false", value)
		}
	}
	for _, value := range []string{"", "false", "no", "off", "0", "maybe"} {
		if gitBooleanTrue(value) {
			t.Errorf("gitBooleanTrue(%q) = true", value)
		}
	}
}

func testGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}

func TestProvisionGitIdentityFreshAndIdempotent(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 2; i++ {
		got, replaced, err := provisionGitIdentity(context.Background(), root, []string{root}, fixtureIdentity)
		if err != nil {
			t.Fatalf("provision pass %d: %v", i+1, err)
		}
		if got != fixtureIdentity {
			t.Fatalf("identity = %+v, want %+v", got, fixtureIdentity)
		}
		if replaced != nil {
			t.Fatalf("pass %d replaced %+v in a repository that had no local identity", i+1, replaced)
		}
	}
	for key, want := range map[string]string{
		"user.name": fixtureIdentity.Name, "user.email": fixtureIdentity.Email,
		"user.useConfigOnly": "true",
	} {
		if got := stringsTrim(testGit(t, root, "config", "--local", "--get", key)); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestProvisionGitIdentityReplacesLocalValuesAndReportsThem(t *testing.T) {
	root := t.TempDir()
	testGit(t, root, "init", "--quiet", "--initial-branch=main")
	testGit(t, root, "config", "--local", "user.name", "Existing Author")
	testGit(t, root, "config", "--local", "user.email", "existing@example.invalid")
	testGit(t, root, "config", "--local", "user.useConfigOnly", "false")

	got, replaced, err := provisionGitIdentity(context.Background(), root, []string{root}, fixtureIdentity)
	if err != nil {
		t.Fatalf("provision over an existing identity: %v", err)
	}
	if got != fixtureIdentity {
		t.Fatalf("identity = %+v, want %+v", got, fixtureIdentity)
	}
	if replaced == nil {
		t.Fatal("replaced = nil, want the previous repository-local values")
	}
	want := ReplacedGitIdentity{Name: "Existing Author", Email: "existing@example.invalid", UseConfigOnly: "false"}
	if *replaced != want {
		t.Fatalf("replaced = %+v, want %+v", *replaced, want)
	}
	for key, value := range map[string]string{
		"user.name": fixtureIdentity.Name, "user.email": fixtureIdentity.Email,
		"user.useConfigOnly": "true",
	} {
		if now := stringsTrim(testGit(t, root, "config", "--local", "--get", key)); now != value {
			t.Errorf("%s = %q, want %q", key, now, value)
		}
	}
}

func TestProvisionGitIdentityReportsOnlyTheKeysItOverwrote(t *testing.T) {
	root := t.TempDir()
	testGit(t, root, "init", "--quiet", "--initial-branch=main")
	testGit(t, root, "config", "--local", "user.name", fixtureIdentity.Name)
	testGit(t, root, "config", "--local", "user.email", "existing@example.invalid")

	_, replaced, err := provisionGitIdentity(context.Background(), root, []string{root}, fixtureIdentity)
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	want := ReplacedGitIdentity{Email: "existing@example.invalid"}
	if replaced == nil || *replaced != want {
		t.Fatalf("replaced = %+v, want %+v: a matching name was not overwritten and an absent useConfigOnly replaced nothing", replaced, want)
	}
}

func TestProvisionedIdentityIsInheritedByLinkedWorktree(t *testing.T) {
	root := t.TempDir()
	if _, _, err := provisionGitIdentity(context.Background(), root, []string{root}, fixtureIdentity); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	testGit(t, root, "add", "README.md")
	testGit(t, root, "commit", "--quiet", "-m", "fixture")
	linked := filepath.Join(t.TempDir(), "linked")
	testGit(t, root, "worktree", "add", "--quiet", "-b", "linked-test", linked)
	for key, want := range map[string]string{"user.name": fixtureIdentity.Name, "user.email": fixtureIdentity.Email, "user.useConfigOnly": "true"} {
		if got := stringsTrim(testGit(t, linked, "config", "--local", "--get", key)); got != want {
			t.Errorf("linked %s = %q, want %q", key, got, want)
		}
	}
}

func TestProvisionGitIdentityConcurrentRepositoriesStayIsolated(t *testing.T) {
	roots := []string{t.TempDir(), t.TempDir()}
	identities := []GitIdentity{{Name: "First", Email: "first@example.invalid"}, {Name: "Second", Email: "second@example.invalid"}}
	var wg sync.WaitGroup
	errs := make(chan error, len(roots))
	for i := range roots {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, err := provisionGitIdentity(context.Background(), roots[i], []string{roots[i]}, identities[i])
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for i := range roots {
		if got := stringsTrim(testGit(t, roots[i], "config", "--local", "--get", "user.email")); got != identities[i].Email {
			t.Errorf("repository %d email = %q, want %q", i, got, identities[i].Email)
		}
	}
}

func stringsTrim(value string) string {
	return strings.TrimSpace(value)
}

// A key holding several local values is reported whole. Git's --get returns
// only the last one, while the write uses --replace-all and destroys them
// all, so a reader that saw one value would name one and remove several.
func TestReplacedIdentityNamesEveryValueOfAMultiValuedKey(t *testing.T) {
	root := t.TempDir()
	testGit(t, root, "init", "--quiet", "--initial-branch=main")
	testGit(t, root, "config", "--local", "--add", "user.email", "first@example.invalid")
	testGit(t, root, "config", "--local", "--add", "user.email", "second@example.invalid")

	_, replaced, err := provisionGitIdentity(context.Background(), root, []string{root}, fixtureIdentity)
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if replaced == nil {
		t.Fatal("replaced = nil, want both prior emails reported")
	}
	if !strings.Contains(replaced.Email, "first@example.invalid") ||
		!strings.Contains(replaced.Email, "second@example.invalid") {
		t.Fatalf("replaced.Email = %q, want both destroyed values named", replaced.Email)
	}
}
