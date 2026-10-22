package project

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rs/zerolog"
)

func starterGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestStarterCommitIsFixedIdempotentAndRefusesHistory(t *testing.T) {
	repo, _ := newTestRepo(t)
	root := mkdir(t, filepath.Join(t.TempDir(), "root"))
	dir := mkdir(t, filepath.Join(root, "practice"))
	p, _, err := repo.Register(context.Background(), "practice", dir, []string{root}, GitIdentity{Name: "Pat Example", Email: "pat@example.invalid"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	for name, body := range map[string]string{"a.mjs": "a\n", "sub/b.mjs": "b\n", "tutorial.json": "{}"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h := NewHandler(repo, []string{root}, &fakeRemover{}, zerolog.Nop())
	mux := http.NewServeMux()
	h.Mount(mux.Handle, func(next http.Handler) http.Handler { return next })
	post := func(id, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/projects/"+id+"/starter-commit", strings.NewReader(body))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	if rec := post("nope", `{"paths":["a.mjs"]}`); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown project: %d", rec.Code)
	}
	for _, bad := range []string{`{"paths":[]}`, `{"paths":["../x"]}`, `{"paths":["/etc/passwd"]}`, `{"paths":[".git/config"]}`, `{"paths":["a.mjs","a.mjs"]}`, `{"paths":["missing"]}`, `{"paths":["sub"]}`} {
		if rec := post(p.ID, bad); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", bad, rec.Code, rec.Body)
		}
	}
	if err := os.Symlink("/etc/hostname", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if rec := post(p.ID, `{"paths":["link"]}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("symlink: %d %s", rec.Code, rec.Body)
	}

	rec := post(p.ID, `{"paths":["sub/b.mjs","a.mjs"]}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("commit: %d %s", rec.Code, rec.Body)
	}
	var made struct {
		Commit  string `json:"commit"`
		Created bool   `json:"created"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&made); err != nil || !made.Created || made.Commit == "" {
		t.Fatalf("response: %+v %v", made, err)
	}
	if got := starterGit(t, dir, "log", "--format=%s|%an <%ae>"); got != "Practice project|Pat Example <pat@example.invalid>" {
		t.Fatalf("log = %q", got)
	}
	if got := starterGit(t, dir, "ls-tree", "-r", "--name-only", "HEAD"); got != "a.mjs\nsub/b.mjs" {
		t.Fatalf("committed files = %q", got)
	}

	rec = post(p.ID, `{"paths":["a.mjs","sub/b.mjs"]}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"created":false`) || !strings.Contains(rec.Body.String(), made.Commit) {
		t.Fatalf("retry: %d %s", rec.Code, rec.Body)
	}
	if starterGit(t, dir, "rev-list", "--count", "HEAD") != "1" {
		t.Fatal("a retry made a second commit")
	}

	if rec := post(p.ID, `{"paths":["a.mjs"]}`); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "history_not_empty") {
		t.Fatalf("different list over the same history: %d %s", rec.Code, rec.Body)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.mjs"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	starterGit(t, dir, "add", "a.mjs")
	starterGit(t, dir, "commit", "-q", "-m", "More work")
	if rec := post(p.ID, `{"paths":["a.mjs","sub/b.mjs"]}`); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "history_not_empty") {
		t.Fatalf("history: %d %s", rec.Code, rec.Body)
	}
}

func TestStarterCommitNeedsARepositoryAuthor(t *testing.T) {
	repo, _ := newTestRepo(t)
	root := mkdir(t, filepath.Join(t.TempDir(), "root"))
	dir := mkdir(t, filepath.Join(root, "bare"))
	starterGit(t, dir, "init", "--quiet")
	if err := os.WriteFile(filepath.Join(dir, "a.mjs"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := repo.RegisterPlain(context.Background(), "bare", dir, []string{root})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	h := NewHandler(repo, []string{root}, &fakeRemover{}, zerolog.Nop())
	mux := http.NewServeMux()
	h.Mount(mux.Handle, func(next http.Handler) http.Handler { return next })
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+p.ID+"/starter-commit", strings.NewReader(`{"paths":["a.mjs"]}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "git_identity_missing") {
		t.Fatalf("no author: %d %s", rec.Code, rec.Body)
	}
	if out, _ := exec.Command("git", "-C", dir, "rev-parse", "--verify", "--quiet", "HEAD").CombinedOutput(); len(out) != 0 {
		t.Fatalf("a commit was made without an author: %s", out)
	}
}

func TestStarterCommitTreatsPathsLiterally(t *testing.T) {
	repo, _ := newTestRepo(t)
	root := mkdir(t, filepath.Join(t.TempDir(), "root"))
	dir := mkdir(t, filepath.Join(root, "practice"))
	if _, _, err := repo.Register(context.Background(), "practice", dir, []string{root}, GitIdentity{Name: "Pat Example", Email: "pat@example.invalid"}); err != nil {
		t.Fatalf("register: %v", err)
	}
	for _, name := range []string{":(top)x", "x"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, created, err := CommitStarter(context.Background(), dir, []string{root}, []string{":(top)x"}); err != nil || !created {
		t.Fatalf("commit: created=%v err=%v", created, err)
	}
	if tree := starterGit(t, dir, "-c", "core.quotePath=false", "ls-tree", "-r", "--name-only", "HEAD"); tree != ":(top)x" {
		t.Fatalf("a name shaped like pathspec magic was not staged as itself: %q", tree)
	}
}

func TestStarterCommitTakesOnlyTheRequestedFilesAndKeepsStagedWork(t *testing.T) {
	root := mkdir(t, filepath.Join(t.TempDir(), "root"))
	dir := mkdir(t, filepath.Join(root, "practice"))
	starterGit(t, dir, "init", "--quiet", "--initial-branch=main")
	starterGit(t, dir, "config", "user.name", "Pat Example")
	starterGit(t, dir, "config", "user.email", "pat@example.invalid")
	for name, body := range map[string]string{"a.txt": "a\n", "private.txt": "secret\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	starterGit(t, dir, "add", "private.txt")

	commit, created, err := CommitStarter(context.Background(), dir, []string{root}, []string{"a.txt"})
	if err != nil || !created {
		t.Fatalf("commit: %v created=%v", err, created)
	}
	if got := starterGit(t, dir, "ls-tree", "-r", "--name-only", commit); got != "a.txt" {
		t.Fatalf("commit tree = %q, want only a.txt", got)
	}
	if got := starterGit(t, dir, "rev-parse", "HEAD"); got != commit {
		t.Fatalf("HEAD = %s, want %s", got, commit)
	}
	if got := starterGit(t, dir, "status", "--porcelain"); got != "A  private.txt" {
		t.Fatalf("status = %q, want the user's staged file kept and nothing else", got)
	}
	if _, created, err := CommitStarter(context.Background(), dir, []string{root}, []string{"a.txt"}); err != nil || created {
		t.Fatalf("retry: %v created=%v", err, created)
	}
}

func TestStarterCommitRefusesAProjectOutsideTheCurrentRoots(t *testing.T) {
	base := t.TempDir()
	allowed := mkdir(t, filepath.Join(base, "allowed"))
	outside := mkdir(t, filepath.Join(base, "outside"))
	starterGit(t, outside, "init", "--quiet", "--initial-branch=main")
	starterGit(t, outside, "config", "user.name", "Pat Example")
	starterGit(t, outside, "config", "user.email", "pat@example.invalid")
	if err := os.WriteFile(filepath.Join(outside, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	starterGit(t, outside, "add", "a.txt")
	before := starterGit(t, outside, "status", "--porcelain")

	_, created, err := CommitStarter(context.Background(), outside, []string{allowed}, []string{"a.txt"})
	if !errors.Is(err, ErrOutsideRoots) || created {
		t.Fatalf("outside root: err=%v created=%v", err, created)
	}
	if out, _ := exec.Command("git", "-C", outside, "rev-parse", "--verify", "--quiet", "HEAD").CombinedOutput(); len(out) != 0 {
		t.Fatalf("history changed: %s", out)
	}
	if got := starterGit(t, outside, "status", "--porcelain"); got != before {
		t.Fatalf("index changed: %q -> %q", before, got)
	}
}
