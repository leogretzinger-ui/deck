package project

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(gitEnv(nil), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func putFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func changesByPath(l *FileListing) map[string]string {
	m := map[string]string{}
	for _, e := range l.Entries {
		m[e.Path] = e.Change
	}
	return m
}

func changeRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitRun(t, dir, "init", "--quiet", "--initial-branch=main")
	putFile(t, filepath.Join(dir, "kept.txt"), "kept\n")
	putFile(t, filepath.Join(dir, "edited.txt"), "one\n")
	putFile(t, filepath.Join(dir, "gone.txt"), "gone\n")
	putFile(t, filepath.Join(dir, "src", "deep", "a.go"), "package a\n")
	putFile(t, filepath.Join(dir, "quiet", "b.go"), "package b\n")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "--quiet", "-m", "init")
	putFile(t, filepath.Join(dir, "edited.txt"), "two\n")
	putFile(t, filepath.Join(dir, "fresh.txt"), "new\n")
	putFile(t, filepath.Join(dir, "src", "deep", "a.go"), "package a // changed\n")
	if err := os.Remove(filepath.Join(dir, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestParseGitStatus(t *testing.T) {
	raw := "1 .M N... 100644 100644 100644 aaa bbb edited.txt\x00" +
		"1 .D N... 100644 100644 000000 aaa bbb gone.txt\x00" +
		"1 D. N... 100644 000000 000000 aaa 000 staged-gone.txt\x00" +
		"u UU N... 100644 100644 100644 100644 a b c conflict with space.txt\x00" +
		"? dir/new file.txt\x00" +
		"! ignored.log\x00"
	got := parseGitStatus([]byte(raw))
	want := map[string]string{
		"edited.txt":              ChangeModified,
		"gone.txt":                changeDeleted,
		"staged-gone.txt":         changeDeleted,
		"conflict with space.txt": ChangeModified,
		"dir/new file.txt":        ChangeNew,
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s = %q, want %q (all: %v)", k, got[k], v, got)
		}
	}
}

func TestListingMarksChangedEntries(t *testing.T) {
	dir := changeRepo(t)
	root, err := ListFiles(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	annotateChanges(context.Background(), dir, root)
	got := changesByPath(root)
	for path, want := range map[string]string{
		"edited.txt": ChangeModified, "fresh.txt": ChangeNew, "kept.txt": "",
		"src": ChangeModified, "quiet": "",
	} {
		if got[path] != want {
			t.Fatalf("%s = %q, want %q (all: %v)", path, got[path], want, got)
		}
	}
	if _, listed := got["gone.txt"]; listed {
		t.Fatal("a deleted file must not be listed")
	}

	sub, err := ListFiles(dir, "src")
	if err != nil {
		t.Fatal(err)
	}
	annotateChanges(context.Background(), dir, sub)
	if got := changesByPath(sub)["src/deep"]; got != ChangeModified {
		t.Fatalf("src/deep = %q, want modified", got)
	}
}

func TestListingWithoutRepositoryHasNoMarkers(t *testing.T) {
	// The ceiling is the temp dir's parent, so no enclosing repository is found.
	dir := t.TempDir()
	putFile(t, filepath.Join(dir, "a.txt"), "a\n")
	l, err := ListFiles(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	annotateChanges(context.Background(), dir, l)
	if got := changesByPath(l)["a.txt"]; got != "" {
		t.Fatalf("a.txt = %q, want no marker", got)
	}
}

func TestListingWhenGitFailsHasNoMarkers(t *testing.T) {
	dir := changeRepo(t)
	bin := t.TempDir()
	putFile(t, filepath.Join(bin, "git"), "#!/bin/sh\nexit 1\n")
	if err := os.Chmod(filepath.Join(bin, "git"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	l, err := ListFiles(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	annotateChanges(context.Background(), dir, l)
	for path, change := range changesByPath(l) {
		if change != "" {
			t.Fatalf("%s = %q, want no markers when git fails", path, change)
		}
	}
}

func TestListingWithoutGitHasNoMarkers(t *testing.T) {
	dir := changeRepo(t)
	t.Setenv("PATH", t.TempDir())
	l, err := ListFiles(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	annotateChanges(context.Background(), dir, l)
	for path, change := range changesByPath(l) {
		if change != "" {
			t.Fatalf("%s = %q", path, change)
		}
	}
}

func TestListFilesHandlerCarriesChange(t *testing.T) {
	h, id, dir := uploadHandler(t)
	gitRun(t, dir, "config", "commit.gpgsign", "false")
	putFile(t, filepath.Join(dir, "a.txt"), "one\n")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "--quiet", "-m", "init")
	putFile(t, filepath.Join(dir, "a.txt"), "two\n")
	putFile(t, filepath.Join(dir, "b.txt"), "b\n")

	req := httptest.NewRequest(http.MethodGet, "/api/projects/x/files", nil)
	req.SetPathValue("id", id)
	rec := httptest.NewRecorder()
	h.ListFiles(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var body FileListing
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	got := changesByPath(&body)
	if got["a.txt"] != ChangeModified || got["b.txt"] != ChangeNew {
		t.Fatalf("changes = %v", got)
	}
}

func survivingFolderRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitRun(t, dir, "init", "--quiet", "--initial-branch=main")
	putFile(t, filepath.Join(dir, "src", "gone.txt"), "gone\n")
	putFile(t, filepath.Join(dir, "src", "kept.txt"), "kept\n")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "--quiet", "-m", "init")
	return dir
}

func listAnnotated(t *testing.T, dir, rel string) map[string]string {
	t.Helper()
	l, err := ListFiles(dir, rel)
	if err != nil {
		t.Fatal(err)
	}
	annotateChanges(context.Background(), dir, l)
	return changesByPath(l)
}

func TestFolderStaysMarkedWhenOnlyADeletionRemains(t *testing.T) {
	t.Run("unstaged", func(t *testing.T) {
		dir := survivingFolderRepo(t)
		if err := os.Remove(filepath.Join(dir, "src", "gone.txt")); err != nil {
			t.Fatal(err)
		}
		if got := listAnnotated(t, dir, "")["src"]; got != ChangeModified {
			t.Fatalf("src = %q, want modified", got)
		}
		if got := listAnnotated(t, dir, "src"); got["src/kept.txt"] != "" || len(got) != 1 {
			t.Fatalf("src listing = %v", got)
		}
	})
	t.Run("staged", func(t *testing.T) {
		dir := survivingFolderRepo(t)
		gitRun(t, dir, "rm", "--quiet", "src/gone.txt")
		if got := listAnnotated(t, dir, "")["src"]; got != ChangeModified {
			t.Fatalf("src = %q, want modified", got)
		}
	})
}

func TestModifyDeleteConflictMarksTheSurvivingFile(t *testing.T) {
	dir := t.TempDir()
	gitRun(t, dir, "init", "--quiet", "--initial-branch=main")
	putFile(t, filepath.Join(dir, "c.txt"), "base\n")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "--quiet", "-m", "base")
	gitRun(t, dir, "checkout", "--quiet", "-b", "other")
	gitRun(t, dir, "rm", "--quiet", "c.txt")
	gitRun(t, dir, "commit", "--quiet", "-m", "delete")
	gitRun(t, dir, "checkout", "--quiet", "main")
	putFile(t, filepath.Join(dir, "c.txt"), "changed\n")
	gitRun(t, dir, "commit", "--quiet", "-am", "modify")
	cmd := exec.Command("git", "-C", dir, "merge", "other")
	cmd.Env = append(gitEnv(nil), "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid")
	if err := cmd.Run(); err == nil {
		t.Fatal("expected a modify/delete conflict")
	}
	if _, err := os.Stat(filepath.Join(dir, "c.txt")); err != nil {
		t.Fatalf("conflicted file should remain on disk: %v", err)
	}
	if got := listAnnotated(t, dir, "")["c.txt"]; got != ChangeModified {
		t.Fatalf("c.txt = %q, want modified", got)
	}
}

func TestOversizedStatusLeavesListingUnmarked(t *testing.T) {
	dir := changeRepo(t)
	bin := t.TempDir()
	script := "#!/bin/sh\nhead -c " + strconv.Itoa(gitStatusMaxOutput+1024) + " /dev/zero | tr '\\0' 'x'\nexit 0\n"
	putFile(t, filepath.Join(bin, "git"), script)
	if err := os.Chmod(filepath.Join(bin, "git"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	if _, err := gitChanges(context.Background(), dir); err == nil {
		t.Fatal("output past the cap must be an error")
	}
	for path, change := range listAnnotated(t, dir, "") {
		if change != "" {
			t.Fatalf("%s = %q, want no markers", path, change)
		}
	}
}

func TestChangedSubmoduleMarksItsFolder(t *testing.T) {
	sub := t.TempDir()
	gitRun(t, sub, "init", "--quiet", "--initial-branch=main")
	putFile(t, filepath.Join(sub, "lib.txt"), "one\n")
	gitRun(t, sub, "add", ".")
	gitRun(t, sub, "commit", "--quiet", "-m", "init")

	dir := t.TempDir()
	gitRun(t, dir, "init", "--quiet", "--initial-branch=main")
	gitRun(t, dir, "-c", "protocol.file.allow=always", "submodule", "--quiet", "add", sub, "module")
	gitRun(t, dir, "commit", "--quiet", "-m", "add module")
	if got := listAnnotated(t, dir, "")["module"]; got != "" {
		t.Fatalf("clean module = %q, want no marker", got)
	}
	putFile(t, filepath.Join(dir, "module", "lib.txt"), "two\n")
	if got := listAnnotated(t, dir, "")["module"]; got != ChangeModified {
		t.Fatalf("module = %q, want modified", got)
	}
}

// openFilesEvents starts the live-refresh stream for a directory and
// returns a reader positioned after the connect preamble.
func openFilesEvents(t *testing.T, h *Handler, id, rel string) *bufio.Reader {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.SetPathValue("id", id)
		h.FilesEvents(w, r)
	}))
	t.Cleanup(server.Close)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/projects/x/files/events?path="+rel, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	reader := bufio.NewReader(resp.Body)
	for i := 0; i < 2; i++ {
		if _, err := reader.ReadString('\n'); err != nil {
			t.Fatalf("connect preamble: %v", err)
		}
	}
	// The baseline fingerprint is taken after the preamble is flushed; let
	// it settle so the test's change lands after it.
	time.Sleep(500 * time.Millisecond)
	return reader
}

func expectFilesChanged(t *testing.T, reader *bufio.Reader) {
	t.Helper()
	line := make(chan string, 1)
	go func() {
		l, _ := reader.ReadString('\n')
		line <- l
	}()
	select {
	case l := <-line:
		if l != "event: files-changed\n" {
			t.Fatalf("event = %q", l)
		}
	case <-time.After(2*filesEventsInterval + 500*time.Millisecond):
		t.Fatal("no files-changed event: the markers moved but the stream stayed quiet")
	}
}

func TestFilesEventsFireWhenADescendantEditMovesAFolderMarker(t *testing.T) {
	h, id, dir := uploadHandler(t)
	gitRun(t, dir, "config", "commit.gpgsign", "false")
	putFile(t, filepath.Join(dir, "src", "deep", "a.txt"), "one\n")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "--quiet", "-m", "init")
	reader := openFilesEvents(t, h, id, ".")
	putFile(t, filepath.Join(dir, "src", "deep", "a.txt"), "two\n")
	expectFilesChanged(t, reader)
}

func TestFilesEventsFireWhenACommitClearsAMarker(t *testing.T) {
	h, id, dir := uploadHandler(t)
	gitRun(t, dir, "config", "commit.gpgsign", "false")
	putFile(t, filepath.Join(dir, "src", "kept.txt"), "one\n")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "--quiet", "-m", "init")
	putFile(t, filepath.Join(dir, "src", "kept.txt"), "two\n")
	reader := openFilesEvents(t, h, id, "src")
	gitRun(t, dir, "commit", "--quiet", "-am", "save")
	expectFilesChanged(t, reader)
}
