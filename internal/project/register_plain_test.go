package project

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rs/zerolog"
)

func TestEnsureDirectoryCreatesOnlyTheLeafInsideARoot(t *testing.T) {
	root := mkdir(t, filepath.Join(t.TempDir(), "root"))
	target := filepath.Join(root, "brand-new")
	if err := EnsureDirectory(target, []string{root}); err != nil {
		t.Fatalf("create leaf: %v", err)
	}
	if info, err := os.Stat(target); err != nil || !info.IsDir() {
		t.Fatalf("leaf not created: %v", err)
	}
	if err := EnsureDirectory(target, []string{root}); err != nil {
		t.Fatalf("an existing directory is left alone: %v", err)
	}
	if err := EnsureDirectory(filepath.Join(root, "missing", "deeper"), []string{root}); !errors.Is(err, ErrInvalidDir) {
		t.Fatalf("a missing parent is not created: %v", err)
	}
	if err := EnsureDirectory(filepath.Join(t.TempDir(), "outside"), []string{root}); !errors.Is(err, ErrOutsideRoots) {
		t.Fatalf("outside the roots: %v", err)
	}
	if err := EnsureDirectory("relative", []string{root}); !errors.Is(err, ErrInvalidDir) {
		t.Fatalf("relative: %v", err)
	}
}

func TestRegisterPlainRecordsWhatGitWouldSignWithoutWriting(t *testing.T) {
	repo, _ := newTestRepo(t)
	ctx := context.Background()
	root := inspectRoot(t)
	// A directory inside the repository inherits its configuration and
	// gains no configuration of its own.
	inside := filepath.Join(root, "deck", "src")
	p, err := repo.RegisterPlain(ctx, "src", inside, []string{root})
	if err != nil {
		t.Fatalf("register inside: %v", err)
	}
	if p.GitAuthorName != "Test Author" || p.GitAuthorEmail != "test@example.invalid" {
		t.Fatalf("inherited identity: %+v", p)
	}
	if p.GitIdentityProfileID != nil || p.GitIdentitySetupRevision != 0 {
		t.Fatalf("a plain registration provisions nothing: %+v", p)
	}
	if _, err := os.Stat(filepath.Join(inside, ".git")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("no repository was created inside the repository: %v", err)
	}
	// A plain directory outside any repository records only what the
	// user's own Git would resolve, and never writes it.
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	plain := filepath.Join(root, "plain")
	q, err := repo.RegisterPlain(ctx, "plain", plain, []string{root})
	if err != nil {
		t.Fatalf("register plain: %v", err)
	}
	if q.GitAuthorName != "" || q.GitAuthorEmail != "" {
		t.Fatalf("nothing to sign with: %+v", q)
	}
	if _, err := os.Stat(filepath.Join(plain, ".git")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a plain directory stays plain: %v", err)
	}
	if _, err := repo.RegisterPlain(ctx, "again", plain, []string{root}); !errors.Is(err, ErrDuplicatePath) {
		t.Fatalf("duplicate: %v", err)
	}
}

func TestRegisterPlainReadsNoIdentityFromAnOutsideRepository(t *testing.T) {
	repo, _ := newTestRepo(t)
	ctx := context.Background()
	root := mkdir(t, filepath.Join(t.TempDir(), "root"))
	outside := mkdir(t, filepath.Join(t.TempDir(), "outside"))
	testGit(t, outside, "init", "--quiet", "--initial-branch=main")
	testGit(t, outside, "config", "user.name", "Outside Author")
	testGit(t, outside, "config", "user.email", "outside@example.invalid")
	if err := os.WriteFile(filepath.Join(outside, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	testGit(t, outside, "add", ".")
	testGit(t, outside, "commit", "--quiet", "-m", "outside")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	// A .git pointer file naming an outside gitdir.
	pointer := mkdir(t, filepath.Join(root, "pointer"))
	if err := os.WriteFile(filepath.Join(pointer, ".git"), []byte("gitdir: "+filepath.Join(outside, ".git")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A linked worktree inside the root of the outside repository.
	linked := filepath.Join(root, "linked")
	testGit(t, outside, "worktree", "add", "--quiet", linked, "-b", "feat/linked")
	for _, dir := range []string{pointer, linked} {
		p, err := repo.RegisterPlain(ctx, filepath.Base(dir), dir, []string{root})
		if err != nil {
			t.Fatalf("register %s: %v", dir, err)
		}
		if p.GitAuthorName != "" || p.GitAuthorEmail != "" {
			t.Fatalf("%s: an outside repository's identity is not read: %+v", dir, p)
		}
	}
}

func TestProvisioningRefusesARepositoryOutsideTheRoots(t *testing.T) {
	repo, _ := newTestRepo(t)
	ctx := context.Background()
	root := mkdir(t, filepath.Join(t.TempDir(), "root"))
	outside := mkdir(t, filepath.Join(t.TempDir(), "outside"))
	testGit(t, outside, "init", "--quiet", "--initial-branch=main")
	testGit(t, outside, "config", "user.name", "Outside Author")
	testGit(t, outside, "config", "user.email", "outside@example.invalid")
	if err := os.WriteFile(filepath.Join(outside, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	testGit(t, outside, "add", ".")
	testGit(t, outside, "commit", "--quiet", "-m", "outside")
	pointer := mkdir(t, filepath.Join(root, "pointer"))
	if err := os.WriteFile(filepath.Join(pointer, ".git"), []byte("gitdir: "+filepath.Join(outside, ".git")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(root, "linked")
	testGit(t, outside, "worktree", "add", "--quiet", linked, "-b", "feat/linked")
	identity := GitIdentity{Name: "Deck Author", Email: "deck@example.invalid"}
	for _, dir := range []string{pointer, linked} {
		if _, _, err := repo.Register(ctx, filepath.Base(dir), dir, []string{root}, identity); !errors.Is(err, ErrOutsideRoots) {
			t.Fatalf("%s: provisioning is refused, not written outside: %v", dir, err)
		}
	}
	if name := strings.TrimSpace(testGit(t, outside, "config", "--local", "--get", "user.name")); name != "Outside Author" {
		t.Fatalf("the outside repository's configuration was written: %q", name)
	}
	// A registered project whose .git is later pointed outside is refused a
	// new identity too.
	own := mkdir(t, filepath.Join(root, "own"))
	p, _, err := repo.Register(ctx, "own", own, []string{root}, identity)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(own, ".git")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(own, ".git"), []byte("gitdir: "+filepath.Join(outside, ".git")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.SetGitIdentity(ctx, p.ID, []string{root}, GitIdentity{Name: "Later", Email: "later@example.invalid"}); !errors.Is(err, ErrOutsideRoots) {
		t.Fatalf("a re-pointed project is refused: %v", err)
	}
}

func TestInheritedGitEnvironmentCannotSelectAnOutsideRepository(t *testing.T) {
	repo, _ := newTestRepo(t)
	ctx := context.Background()
	root := mkdir(t, filepath.Join(t.TempDir(), "root"))
	outside := mkdir(t, filepath.Join(t.TempDir(), "outside"))
	testGit(t, outside, "init", "--quiet", "--initial-branch=main")
	testGit(t, outside, "config", "user.name", "Outside Author")
	testGit(t, outside, "config", "user.email", "outside@example.invalid")
	testGit(t, outside, "remote", "add", "origin", "https://example.invalid/outside.git")
	// The process is handed a repository selection pointing outside the
	// roots; every git the package runs must ignore it.
	t.Setenv("GIT_DIR", filepath.Join(outside, ".git"))
	t.Setenv("GIT_WORK_TREE", root)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	empty := mkdir(t, filepath.Join(root, "empty"))
	p, _, err := repo.Register(ctx, "empty", empty, []string{root}, GitIdentity{Name: "Deck Author", Email: "deck@example.invalid"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, err := os.Stat(filepath.Join(empty, ".git")); err != nil {
		t.Fatalf("the project got its own repository: %v", err)
	}
	if name := strings.TrimSpace(testGit(t, outside, "config", "--local", "--get", "user.name")); name != "Outside Author" {
		t.Fatalf("the outside repository's configuration was written: %q", name)
	}
	if p.GitAuthorName != "Deck Author" {
		t.Fatalf("registered: %+v", p)
	}
	mkdir(t, filepath.Join(root, "plain"))
	out := inspect(t, root, filepath.Join(root, "plain"), "")
	if out.Kind != KindPlain || out.Remote != "" || out.Parent != nil {
		t.Fatalf("inspection under a foreign GIT_DIR reports nothing foreign: %+v", out)
	}
	q, err := repo.RegisterPlain(ctx, "plain", filepath.Join(root, "plain"), []string{root})
	if err != nil {
		t.Fatal(err)
	}
	if q.GitAuthorName != "" {
		t.Fatalf("plain registration read the outside identity: %+v", q)
	}
}

func inspectHandler(t *testing.T) (*Handler, string) {
	t.Helper()
	repo, _ := newTestRepo(t)
	root := inspectRoot(t)
	if _, _, err := repo.Register(context.Background(), "Deck", filepath.Join(root, "deck"), []string{root},
		GitIdentity{Name: "Test Author", Email: "test@example.invalid"}); err != nil {
		t.Fatalf("register: %v", err)
	}
	return NewHandler(repo, []string{root}, nil, zerolog.Nop()), root
}

func TestInspectHandlerNamesTheRegisteredParent(t *testing.T) {
	h, root := inspectHandler(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/projects/inspect?input="+filepath.Join(root, "tmp", "vd-gate2"), nil)
	h.Inspect(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var out Inspection
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Kind != KindWorktree || out.Parent == nil || out.Parent.Name != "Deck" {
		t.Fatalf("worktree of a registered project: %+v", out)
	}
	rec = httptest.NewRecorder()
	h.Inspect(rec, httptest.NewRequest(http.MethodGet, "/api/projects/inspect?input="+t.TempDir(), nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("outside the roots: %d %s", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	h.Inspect(rec, httptest.NewRequest(http.MethodGet, "/api/projects/inspect?input=", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty input: %d %s", rec.Code, rec.Body)
	}
}

func TestCreateMakesAnAbsentPathAndRegistersPlainDirectories(t *testing.T) {
	h, root := inspectHandler(t)
	// Absent path with a profile: created, then provisioned as before.
	rec := httptest.NewRecorder()
	body := `{"name":"fresh","path":"` + filepath.Join(root, "fresh") + `","git_author_name":"Test Author","git_author_email":"test@example.invalid"}`
	h.Create(rec, httptest.NewRequest(http.MethodPost, "/api/projects", strings.NewReader(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create absent: %d %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(filepath.Join(root, "fresh", ".git")); err != nil {
		t.Fatalf("a fresh project becomes a repository: %v", err)
	}
	// Plain: registered as it is, nothing provisioned.
	rec = httptest.NewRecorder()
	body = `{"name":"plain","path":"` + filepath.Join(root, "plain") + `","plain":true}`
	h.Create(rec, httptest.NewRequest(http.MethodPost, "/api/projects", strings.NewReader(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create plain: %d %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(filepath.Join(root, "plain", ".git")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("plain stays plain: %v", err)
	}
	var p Project
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if p.GitIdentityProfileID != nil {
		t.Fatalf("plain carries no profile: %+v", p)
	}
	// Absent path whose parent is missing is refused, not created deeply.
	rec = httptest.NewRecorder()
	body = `{"name":"deep","path":"` + filepath.Join(root, "missing", "deep") + `","plain":true}`
	h.Create(rec, httptest.NewRequest(http.MethodPost, "/api/projects", strings.NewReader(body)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing parent: %d %s", rec.Code, rec.Body)
	}
}
