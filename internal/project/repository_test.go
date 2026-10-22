package project

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	"github.com/noisefloor-oss/deck/internal/db"
)

func newTestRepo(t *testing.T) (*Repository, *db.DB) {
	t.Helper()
	d, err := db.Open(context.Background(), db.Config{
		Path: filepath.Join(t.TempDir(), "control.db"),
	})
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return NewRepository(d), d
}

func mkdir(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	return path
}

// Registration canonicalizes, checks the allowlist against the RESOLVED
// path, and audits.
func TestRegisterValid(t *testing.T) {
	repo, d := newTestRepo(t)
	ctx := context.Background()
	root := mkdir(t, filepath.Join(t.TempDir(), "root"))
	dir := mkdir(t, filepath.Join(root, "proj"))

	p, _, err := repo.Register(ctx, "demo", dir, []string{root}, GitIdentity{Name: "Test Author", Email: "test@example.invalid"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if p.CanonicalPath != dir {
		t.Fatalf("canonical = %q, want %q", p.CanonicalPath, dir)
	}

	got, err := repo.Get(ctx, p.ID)
	if err != nil || got.Name != "demo" || got.GitAuthorName != "Test Author" || got.GitAuthorEmail != "test@example.invalid" {
		t.Fatalf("get: %+v %v", got, err)
	}
	for key, want := range map[string]string{"user.name": "Test Author", "user.email": "test@example.invalid", "user.useConfigOnly": "true"} {
		if value, present, err := localGitValue(ctx, dir, key); err != nil || !present || value != want {
			t.Errorf("local %s = %q present=%v err=%v, want %q", key, value, present, err, want)
		}
	}

	var eventType string
	err = d.Reader().QueryRow(
		`SELECT event_type FROM audit_events WHERE event_type = ?`, AuditCreated).Scan(&eventType)
	if err != nil {
		t.Fatalf("no creation audit: %v", err)
	}
}

// A path outside every root is refused with an explicit error, never a
// silent fallback — including the ".." spelling of an outside directory.
func TestRegisterOutsideRoots(t *testing.T) {
	repo, _ := newTestRepo(t)
	ctx := context.Background()
	base := t.TempDir()
	root := mkdir(t, filepath.Join(base, "root"))
	outside := mkdir(t, filepath.Join(base, "outside"))

	if _, _, err := repo.Register(ctx, "x", outside, []string{root}, GitIdentity{Name: "Test Author", Email: "test@example.invalid"}); !errors.Is(err, ErrOutsideRoots) {
		t.Fatalf("outside root: %v", err)
	}
	// The ".." spelling of the same outside directory must fail the same
	// way — canonicalization settles it before the allowlist check.
	traversal := filepath.Join(root, "..", "outside")
	if _, _, err := repo.Register(ctx, "x", traversal, []string{root}, GitIdentity{Name: "Test Author", Email: "test@example.invalid"}); !errors.Is(err, ErrOutsideRoots) {
		t.Fatalf("traversal spelling: %v", err)
	}
}

// The allowlist decision happens AFTER symlink resolution: a link inside
// the root pointing outside is rejected; a link outside pointing in is
// registered under its real path.
func TestRegisterSymlinkResolution(t *testing.T) {
	repo, _ := newTestRepo(t)
	ctx := context.Background()
	root := mkdir(t, filepath.Join(t.TempDir(), "root"))
	real := mkdir(t, filepath.Join(root, "real"))
	outside := mkdir(t, filepath.Join(t.TempDir(), "outside"))

	escape := filepath.Join(root, "escape")
	if err := os.Symlink(outside, escape); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if _, _, err := repo.Register(ctx, "x", escape, []string{root}, GitIdentity{Name: "Test Author", Email: "test@example.invalid"}); !errors.Is(err, ErrOutsideRoots) {
		t.Fatalf("symlink escape: %v", err)
	}

	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	p, _, err := repo.Register(ctx, "x", link, []string{root}, GitIdentity{Name: "Test Author", Email: "test@example.invalid"})
	if err != nil {
		t.Fatalf("register via link: %v", err)
	}
	if p.CanonicalPath != real {
		t.Fatalf("canonical = %q, want resolved %q", p.CanonicalPath, real)
	}
}

func TestBrowseDirectoriesIsConfinedToCanonicalRoots(t *testing.T) {
	base := t.TempDir()
	root := mkdir(t, filepath.Join(base, "mounted-workspace"))
	alpha := mkdir(t, filepath.Join(root, "audio-player"))
	outside := mkdir(t, filepath.Join(base, "private"))
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatalf("escape symlink: %v", err)
	}
	if err := os.Symlink(alpha, filepath.Join(root, "audio-link")); err != nil {
		t.Fatalf("internal symlink: %v", err)
	}

	mounts, err := BrowseDirectories("", []string{root, root})
	if err != nil {
		t.Fatalf("browse mounts: %v", err)
	}
	if mounts.CurrentPath != "" || mounts.ParentPath != nil || len(mounts.Directories) != 1 {
		t.Fatalf("mount listing = %+v", mounts)
	}
	if mounts.Directories[0].Path != root {
		t.Fatalf("mount path = %q, want %q", mounts.Directories[0].Path, root)
	}

	listing, err := BrowseDirectories(root, []string{root})
	if err != nil {
		t.Fatalf("browse root: %v", err)
	}
	if listing.CurrentPath != root || listing.ParentPath != nil {
		t.Fatalf("root listing = %+v", listing)
	}
	// The internal symlink and real directory resolve to one canonical choice;
	// the regular file and escaping symlink are never exposed.
	if len(listing.Directories) != 1 || listing.Directories[0].Name != "audio-player" || listing.Directories[0].Path != alpha {
		t.Fatalf("children = %+v, want only %q", listing.Directories, alpha)
	}

	child, err := BrowseDirectories(alpha, []string{root})
	if err != nil {
		t.Fatalf("browse child: %v", err)
	}
	if child.ParentPath == nil || *child.ParentPath != root {
		t.Fatalf("child parent = %v, want %q", child.ParentPath, root)
	}
	if _, err := BrowseDirectories(outside, []string{root}); !errors.Is(err, ErrOutsideRoots) {
		t.Fatalf("outside browse: %v", err)
	}
	if _, err := BrowseDirectories("relative/path", []string{root}); !errors.Is(err, ErrInvalidDir) {
		t.Fatalf("relative browse: %v", err)
	}
	if _, err := BrowseDirectories("", []string{filepath.Join(base, "missing-mount")}); !errors.Is(err, ErrInvalidDir) {
		t.Fatalf("missing configured mount: %v", err)
	}
}

// The allowed root is now the whole workspace mount — a big mixed
// directory. The picker must surface only its immediate real subdirectories:
// never files, never dot-directories (.git, .cache, provider homes), and
// never the contents of nested directories (no recursion).
func TestBrowseDirectoriesMixedWorkspaceRoot(t *testing.T) {
	root := mkdir(t, filepath.Join(t.TempDir(), "workspace"))
	noisedeck := mkdir(t, filepath.Join(root, "noisedeck"))
	mkdir(t, filepath.Join(noisedeck, "internal")) // nested dir: must not surface at root
	notesApp := mkdir(t, filepath.Join(root, "notes-app"))
	dotGit := mkdir(t, filepath.Join(root, ".git"))
	mkdir(t, filepath.Join(root, ".cache"))
	for _, name := range []string{"README.md", "compose.env", ".envrc", "deploy.sh"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	// A dot-named symlink to a real in-root directory stays hidden too.
	if err := os.Symlink(noisedeck, filepath.Join(root, ".noisedeck-alias")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	listing, err := BrowseDirectories(root, []string{root})
	if err != nil {
		t.Fatalf("browse workspace root: %v", err)
	}
	if len(listing.Directories) != 2 {
		t.Fatalf("children = %+v, want exactly [noisedeck notes-app]", listing.Directories)
	}
	if listing.Directories[0].Name != "noisedeck" || listing.Directories[0].Path != noisedeck ||
		listing.Directories[1].Name != "notes-app" || listing.Directories[1].Path != notesApp {
		t.Fatalf("children = %+v, want noisedeck then notes-app", listing.Directories)
	}

	// Hiding dot-directories is picker noise control, not an access
	// boundary: an explicit in-root dot path remains browsable.
	direct, err := BrowseDirectories(dotGit, []string{root})
	if err != nil {
		t.Fatalf("browse explicit dot path: %v", err)
	}
	if direct.CurrentPath != dotGit {
		t.Fatalf("dot path listing = %+v", direct)
	}
}

// The allowed_roots setting is the workspace mount itself. The root passes
// the allowlist, and projects register cleanly directly beneath it — at any
// depth — with no per-root permission requirement (the 0700 rule covers
// only db/lock/socket dirs, never Projects.AllowedRoots).
func TestRegisterWithWorkspaceMountAsRoot(t *testing.T) {
	repo, _ := newTestRepo(t)
	ctx := context.Background()
	root := mkdir(t, filepath.Join(t.TempDir(), "workspace"))
	mkdir(t, filepath.Join(root, ".git"))
	if err := os.WriteFile(filepath.Join(root, "notes.md"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	direct := mkdir(t, filepath.Join(root, "noisedeck"))
	nested := mkdir(t, filepath.Join(root, "noisedeck-lanes", "task102"))

	// The root itself satisfies the allowlist (UnderRoot rel == ".").
	if _, _, err := repo.Register(ctx, "whole-workspace", root, []string{root}, GitIdentity{Name: "Test Author", Email: "test@example.invalid"}); err != nil {
		t.Fatalf("register root itself: %v", err)
	}
	p, _, err := repo.Register(ctx, "direct", direct, []string{root}, GitIdentity{Name: "Test Author", Email: "test@example.invalid"})
	if err != nil {
		t.Fatalf("register direct child: %v", err)
	}
	if p.CanonicalPath != direct {
		t.Fatalf("canonical = %q, want %q", p.CanonicalPath, direct)
	}
	if _, _, err := repo.Register(ctx, "nested", nested, []string{root}, GitIdentity{Name: "Test Author", Email: "test@example.invalid"}); err != nil {
		t.Fatalf("register nested child: %v", err)
	}
}

// Missing, non-directory, and non-writable paths are explicit errors.
func TestRegisterInvalidDirs(t *testing.T) {
	repo, _ := newTestRepo(t)
	ctx := context.Background()
	root := mkdir(t, filepath.Join(t.TempDir(), "root"))

	if _, _, err := repo.Register(ctx, "x", filepath.Join(root, "missing"), []string{root}, GitIdentity{Name: "Test Author", Email: "test@example.invalid"}); !errors.Is(err, ErrInvalidDir) {
		t.Fatalf("missing: %v", err)
	}
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, _, err := repo.Register(ctx, "x", file, []string{root}, GitIdentity{Name: "Test Author", Email: "test@example.invalid"}); !errors.Is(err, ErrInvalidDir) {
		t.Fatalf("not a directory: %v", err)
	}
	ro := mkdir(t, filepath.Join(root, "readonly"))
	if err := os.Chmod(ro, 0o555); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if _, _, err := repo.Register(ctx, "x", ro, []string{root}, GitIdentity{Name: "Test Author", Email: "test@example.invalid"}); !errors.Is(err, ErrInvalidDir) {
		t.Fatalf("read-only: %v", err)
	}
}

// The active-path unique index rejects a duplicate registration, but a
// removed project's path can be registered again.
func TestRegisterDuplicateAndReregister(t *testing.T) {
	repo, _ := newTestRepo(t)
	ctx := context.Background()
	root := mkdir(t, filepath.Join(t.TempDir(), "root"))
	dir := mkdir(t, filepath.Join(root, "proj"))

	p, _, err := repo.Register(ctx, "one", dir, []string{root}, GitIdentity{Name: "Test Author", Email: "test@example.invalid"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, _, err := repo.Register(ctx, "two", dir, []string{root}, GitIdentity{Name: "Test Author", Email: "test@example.invalid"}); !errors.Is(err, ErrDuplicatePath) {
		t.Fatalf("duplicate: %v", err)
	}
	if err := repo.Remove(ctx, p.ID); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, _, err := repo.Register(ctx, "two", dir, []string{root}, GitIdentity{Name: "Test Author", Email: "test@example.invalid"}); err != nil {
		t.Fatalf("re-register after removal: %v", err)
	}
}

// Removal hides the project and audits — the directory's files are
// untouched, and the audit trail survives.
func TestRemoveKeepsFiles(t *testing.T) {
	repo, d := newTestRepo(t)
	ctx := context.Background()
	root := mkdir(t, filepath.Join(t.TempDir(), "root"))
	dir := mkdir(t, filepath.Join(root, "proj"))
	kept := filepath.Join(dir, "kept.txt")
	if err := os.WriteFile(kept, []byte("data"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	p, _, err := repo.Register(ctx, "demo", dir, []string{root}, GitIdentity{Name: "Test Author", Email: "test@example.invalid"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := repo.Remove(ctx, p.ID); err != nil {
		t.Fatalf("remove: %v", err)
	}

	if _, err := os.Stat(kept); err != nil {
		t.Fatalf("removal touched project files: %v", err)
	}
	if _, err := repo.Get(ctx, p.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removed project still visible: %v", err)
	}
	list, err := repo.List(ctx)
	if err != nil || len(list) != 0 {
		t.Fatalf("removed project listed: %+v %v", list, err)
	}
	var eventType string
	if err := d.Reader().QueryRow(
		`SELECT event_type FROM audit_events WHERE event_type = ?`, AuditRemoved).Scan(&eventType); err != nil {
		t.Fatalf("no removal audit: %v", err)
	}
}

// Renaming sets the user's display name only. The canonical
// path is untouched, the directory on disk keeps its basename, and the
// rename is audited (project.renamed).
func TestSetNameRenamesDisplayOnly(t *testing.T) {
	repo, d := newTestRepo(t)
	ctx := context.Background()
	root := mkdir(t, filepath.Join(t.TempDir(), "root"))
	dir := mkdir(t, filepath.Join(root, "proj"))

	p, _, err := repo.Register(ctx, "proj", dir, []string{root}, GitIdentity{Name: "Test Author", Email: "test@example.invalid"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := repo.SetName(ctx, p.ID, "  Noise Deck  "); err != nil {
		t.Fatalf("rename: %v", err)
	}
	got, err := repo.Get(ctx, p.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "Noise Deck" {
		t.Fatalf("name = %q, want trimmed %q", got.Name, "Noise Deck")
	}
	if got.CanonicalPath != dir {
		t.Fatalf("canonical_path = %q changed, want %q", got.CanonicalPath, dir)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("rename touched the directory on disk: %v", err)
	}
	var eventType string
	if err := d.Reader().QueryRow(
		`SELECT event_type FROM audit_events WHERE event_type = ?`, AuditRenamed).Scan(&eventType); err != nil {
		t.Fatalf("no rename audit: %v", err)
	}
}

// An empty (or all-whitespace) name resets the display name to the
// directory basename — the name registration would derive.
func TestSetNameEmptyResetsToBasename(t *testing.T) {
	repo, _ := newTestRepo(t)
	ctx := context.Background()
	root := mkdir(t, filepath.Join(t.TempDir(), "root"))
	dir := mkdir(t, filepath.Join(root, "noisedeck"))

	p, _, err := repo.Register(ctx, "custom", dir, []string{root}, GitIdentity{Name: "Test Author", Email: "test@example.invalid"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := repo.SetName(ctx, p.ID, "   "); err != nil {
		t.Fatalf("clear: %v", err)
	}
	got, err := repo.Get(ctx, p.ID)
	if err != nil || got.Name != "noisedeck" {
		t.Fatalf("name after clear = %+v (%v), want basename noisedeck", got, err)
	}
}

// Bounds and targets: an over-long name is refused, and an unknown or
// removed project is ErrNotFound — a removed project has nothing to name.
func TestSetNameRefusals(t *testing.T) {
	repo, _ := newTestRepo(t)
	ctx := context.Background()
	root := mkdir(t, filepath.Join(t.TempDir(), "root"))
	dir := mkdir(t, filepath.Join(root, "proj"))

	p, _, err := repo.Register(ctx, "proj", dir, []string{root}, GitIdentity{Name: "Test Author", Email: "test@example.invalid"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := repo.SetName(ctx, p.ID, strings.Repeat("x", 101)); err == nil {
		t.Fatal("101-character name accepted")
	}
	if err := repo.SetName(ctx, "nope", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown id: %v, want ErrNotFound", err)
	}
	if err := repo.SetName(ctx, "nope", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown id with empty name: %v, want ErrNotFound", err)
	}
	if err := repo.Remove(ctx, p.ID); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := repo.SetName(ctx, p.ID, "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removed project renamed: %v, want ErrNotFound", err)
	}
}

// The endpoint mirrors the session rename's shape and codes: 204 on
// success, 400 bad_request for an unreadable body or over-long name,
// 404 not_found for an unknown project.
func TestHandlerRename(t *testing.T) {
	repo, _ := newTestRepo(t)
	root := mkdir(t, filepath.Join(t.TempDir(), "root"))
	dir := mkdir(t, filepath.Join(root, "proj"))
	p, _, err := repo.Register(context.Background(), "proj", dir, []string{root}, GitIdentity{Name: "Test Author", Email: "test@example.invalid"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	h := NewHandler(repo, []string{root}, &fakeRemover{}, zerolog.Nop())
	mux := http.NewServeMux()
	h.Mount(mux.Handle, func(next http.Handler) http.Handler { return next })

	do := func(id, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/projects/"+id+"/rename", strings.NewReader(body))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	if rec := do(p.ID, `{"name":"Noise Deck"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("rename: status = %d, want 204 (body %s)", rec.Code, rec.Body)
	}
	got, err := repo.Get(context.Background(), p.ID)
	if err != nil || got.Name != "Noise Deck" || got.CanonicalPath != dir {
		t.Fatalf("after rename: %+v (%v), want renamed with path untouched", got, err)
	}
	if rec := do(p.ID, `not json`); rec.Code != http.StatusBadRequest {
		t.Fatalf("unreadable body: status = %d, want 400", rec.Code)
	}
	long, _ := json.Marshal(map[string]string{"name": strings.Repeat("x", 101)})
	if rec := do(p.ID, string(long)); rec.Code != http.StatusBadRequest {
		t.Fatalf("over-long name: status = %d, want 400", rec.Code)
	}
	rec := do("nope", `{"name":"x"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown id: status = %d, want 404", rec.Code)
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil || body.Error != "not_found" {
		t.Fatalf("unknown id body = %+v (%v), want not_found", body, err)
	}
}

// insertSession plants a session row directly: the project package cannot
// import internal/session (cycle), and these tests only need rows the
// live-session check can see.
// insertSessionAt plants a session with an EXPLICIT creation time.
// Distinct times are what make an ordering claim testable: rows sharing
// one timestamp let any ORDER BY pass.
func insertSessionAt(t *testing.T, d *db.DB, id, projectID, status, displayName, createdAt string) {
	t.Helper()
	err := d.WithTx(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO sessions (id, project_id, provider, display_name,
				tmux_socket_path, tmux_session_name, status, created_at, updated_at)
			VALUES (?, ?, 'claude', ?, '/tmp/sock', 'noisedeck-test', ?, ?, ?)`,
			id, projectID, displayName, status,
			createdAt, createdAt)
		return err
	})
	if err != nil {
		t.Fatalf("insert session: %v", err)
	}
}

func insertSession(t *testing.T, d *db.DB, id, projectID, status, displayName string) {
	t.Helper()
	insertSessionAt(t, d, id, projectID, status, displayName, "2026-01-01T00:00:00.000Z")
}

// markSessionExited flips a planted session to exited with valid exit
// evidence, satisfying the schema's exit constraints.
func markSessionExited(t *testing.T, d *db.DB, id, at string) {
	t.Helper()
	err := d.WithTx(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE sessions SET status = 'exited', exited_at = ?, exit_code = 0
			WHERE id = ?`, at, id)
		return err
	})
	if err != nil {
		t.Fatalf("mark exited: %v", err)
	}
}

// fakeRemover stands in for the session manager in cascade tests: it
// records the ids it was asked to tear down and runs the test's own
// consequence (normally flipping the row so the repository backstop
// sees it ended, as the real manager's MarkRemoved would).
type fakeRemover struct {
	removed  []string
	fail     error
	onRemove func(id string)
}

func (f *fakeRemover) Remove(_ context.Context, id string) error {
	if f.fail != nil {
		return f.fail
	}
	f.removed = append(f.removed, id)
	if f.onRemove != nil {
		f.onRemove(id)
	}
	return nil
}

// The repository's in-transaction live check is the CASCADE'S BACKSTOP: a
// session launched between the handler's cascade and the
// removal transaction still refuses, names the row, and leaves the
// project active. Ended sessions never block, and removal keeps their
// history.
func TestRemoveRefusedWhileSessionsLive(t *testing.T) {
	repo, d := newTestRepo(t)
	ctx := context.Background()
	root := mkdir(t, filepath.Join(t.TempDir(), "root"))
	dir := mkdir(t, filepath.Join(root, "proj"))

	p, _, err := repo.Register(ctx, "demo", dir, []string{root}, GitIdentity{Name: "Test Author", Email: "test@example.invalid"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	insertSession(t, d, "s-live", p.ID, "starting", "fix-bug")
	insertSession(t, d, "s-done", p.ID, "starting", "")
	markSessionExited(t, d, "s-done", "2026-01-01T00:01:00.000Z")

	err = repo.Remove(ctx, p.ID)
	if !errors.Is(err, ErrLiveSessions) {
		t.Fatalf("remove with live session: %v, want ErrLiveSessions", err)
	}
	// The refusal lists the user's label for the session it refuses over.
	if !strings.Contains(err.Error(), "fix-bug") || !strings.Contains(err.Error(), "stop") {
		t.Fatalf("refusal does not name the live session: %q", err)
	}
	if _, err := repo.Get(ctx, p.ID); err != nil {
		t.Fatalf("refused removal must leave the project active: %v", err)
	}

	// With the live session ended, removal proceeds — and the exited
	// session rows survive it (no cascade; history is kept).
	markSessionExited(t, d, "s-live", "2026-01-01T00:02:00.000Z")
	if err := repo.Remove(ctx, p.ID); err != nil {
		t.Fatalf("remove after sessions ended: %v", err)
	}
	var n int
	if err := d.Reader().QueryRow(
		`SELECT COUNT(*) FROM sessions WHERE project_id = ?`, p.ID).Scan(&n); err != nil || n != 2 {
		t.Fatalf("session history after removal = %d (%v), want 2", n, err)
	}
}

// Removal with live sessions cascades: the
// endpoint tears each live session down through the session manager,
// then hides the project — 204, with the teardown on record.
func TestHandlerRemoveCascadesLiveSessions(t *testing.T) {
	repo, d := newTestRepo(t)
	root := mkdir(t, filepath.Join(t.TempDir(), "root"))
	dir := mkdir(t, filepath.Join(root, "proj"))
	p, _, err := repo.Register(context.Background(), "demo", dir, []string{root}, GitIdentity{Name: "Test Author", Email: "test@example.invalid"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	insertSession(t, d, "s-live", p.ID, "starting", "fix-bug")
	insertSession(t, d, "s-live2", p.ID, "starting", "")
	insertSession(t, d, "s-done", p.ID, "starting", "")
	markSessionExited(t, d, "s-done", "2026-01-01T00:01:00.000Z")

	remover := &fakeRemover{onRemove: func(id string) {
		markSessionExited(t, d, id, "2026-01-01T00:02:00.000Z")
	}}
	h := NewHandler(repo, []string{root}, remover, zerolog.Nop())
	mux := http.NewServeMux()
	h.Mount(mux.Handle, func(next http.Handler) http.Handler { return next })
	req := httptest.NewRequest(http.MethodDelete, "/api/projects/"+p.ID, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("remove with live sessions: status = %d, want 204 (body %s)", rec.Code, rec.Body)
	}
	// Exactly the live rows were torn down — the ended one was never touched.
	if len(remover.removed) != 2 ||
		remover.removed[0] != "s-live" || remover.removed[1] != "s-live2" {
		t.Fatalf("cascade removed %v, want [s-live s-live2]", remover.removed)
	}
	if _, err := repo.Get(context.Background(), p.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("project still visible after removal: %v", err)
	}
}

// A session the manager cannot stop fails the removal closed: 409 with
// a stable classification naming the user's label — never the raw
// teardown error (socket paths and process details stay in the log) —
// and the project stays active.
func TestHandlerRemoveFailsClosedWhenTeardownFails(t *testing.T) {
	repo, d := newTestRepo(t)
	root := mkdir(t, filepath.Join(t.TempDir(), "root"))
	dir := mkdir(t, filepath.Join(root, "proj"))
	p, _, err := repo.Register(context.Background(), "demo", dir, []string{root}, GitIdentity{Name: "Test Author", Email: "test@example.invalid"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	insertSession(t, d, "s-live", p.ID, "starting", "fix-bug")

	rawErr := errors.New("session s-live: recorded on socket \"/run/noisedeck/private.sock\"")
	h := NewHandler(repo, []string{root}, &fakeRemover{fail: rawErr}, zerolog.Nop())
	mux := http.NewServeMux()
	h.Mount(mux.Handle, func(next http.Handler) http.Handler { return next })
	req := httptest.NewRequest(http.MethodDelete, "/api/projects/"+p.ID, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("remove with failing teardown: status = %d, want 409 (body %s)", rec.Code, rec.Body)
	}
	var body struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error != "session_stop_failed" || !strings.Contains(body.Message, "fix-bug") {
		t.Fatalf("body = %+v, want session_stop_failed naming the session", body)
	}
	if strings.Contains(body.Message, "sock") {
		t.Fatalf("raw teardown error leaked into the response: %q", body.Message)
	}
	if _, err := repo.Get(context.Background(), p.ID); err != nil {
		t.Fatalf("failed cascade must leave the project active: %v", err)
	}
}

// The endpoints drive the same contract: create/list/get/remove, with the
// right error statuses.
func TestHandlerRoundTrip(t *testing.T) {
	repo, _ := newTestRepo(t)
	root := mkdir(t, filepath.Join(t.TempDir(), "root"))
	dir := mkdir(t, filepath.Join(root, "proj"))
	h := NewHandler(repo, []string{root}, &fakeRemover{}, zerolog.Nop())
	mux := http.NewServeMux()
	h.Mount(mux.Handle, func(next http.Handler) http.Handler { return next })

	do := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	outside := mkdir(t, filepath.Join(t.TempDir(), "outside"))
	body, _ := json.Marshal(map[string]string{"name": "x", "path": outside})
	if rec := do(http.MethodPost, "/api/projects", string(body)); rec.Code != http.StatusForbidden {
		t.Fatalf("outside roots: status = %d, want 403", rec.Code)
	}

	browse := do(http.MethodGet, "/api/project-directories?path="+root, "")
	if browse.Code != http.StatusOK {
		t.Fatalf("browse: status = %d, body %s", browse.Code, browse.Body)
	}
	var listing DirectoryListing
	if err := json.NewDecoder(browse.Body).Decode(&listing); err != nil {
		t.Fatalf("decode browse: %v", err)
	}
	if listing.CurrentPath != root || len(listing.Directories) != 1 || listing.Directories[0].Path != dir {
		t.Fatalf("browse listing = %+v", listing)
	}
	if rec := do(http.MethodGet, "/api/project-directories?path="+outside, ""); rec.Code != http.StatusForbidden {
		t.Fatalf("browse outside: status = %d, want 403", rec.Code)
	}

	body, _ = json.Marshal(map[string]string{
		"name": "demo", "path": dir,
		"git_author_name": "Test Author", "git_author_email": "test@example.invalid",
	})
	rec := do(http.MethodPost, "/api/projects", string(body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status = %d, body %s", rec.Code, rec.Body)
	}
	var created Project
	if err := json.NewDecoder(rec.Body).Decode(&created); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if rec := do(http.MethodPost, "/api/projects", string(body)); rec.Code != http.StatusConflict {
		t.Fatalf("duplicate: status = %d, want 409", rec.Code)
	}
	if rec := do(http.MethodGet, "/api/projects/"+created.ID, ""); rec.Code != http.StatusOK {
		t.Fatalf("get: status = %d, want 200", rec.Code)
	}
	identityBody, _ := json.Marshal(map[string]string{"name": "Test Author", "email": "test@example.invalid"})
	if rec := do(http.MethodPut, "/api/projects/"+created.ID+"/git-identity", string(identityBody)); rec.Code != http.StatusOK {
		t.Fatalf("idempotent Git setup: status = %d body=%s", rec.Code, rec.Body)
	}
	if rec := do(http.MethodGet, "/api/projects/nope", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("get missing: status = %d, want 404", rec.Code)
	}
	if rec := do(http.MethodDelete, "/api/projects/"+created.ID, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("remove: status = %d, want 204", rec.Code)
	}
	if rec := do(http.MethodDelete, "/api/projects/"+created.ID, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("remove twice: status = %d, want 404", rec.Code)
	}
}

// An empty or whitespace path must be rejected before canonicalization:
// filepath.Abs("") resolves to the process working directory, which could
// silently register the backend's own directory under an allowlisted root.
func TestRegisterEmptyPathRejected(t *testing.T) {
	repo, _ := newTestRepo(t)
	ctx := context.Background()
	root := mkdir(t, filepath.Join(t.TempDir(), "root"))

	for _, path := range []string{"", "   ", "\t"} {
		if _, _, err := repo.Register(ctx, "x", path, []string{root}, GitIdentity{Name: "Test Author", Email: "test@example.invalid"}); err == nil {
			t.Fatalf("path %q registered", path)
		}
	}
}

// The handler's empty-path case is a 400, not a registration.
func TestHandlerEmptyPathIs400(t *testing.T) {
	repo, _ := newTestRepo(t)
	root := mkdir(t, filepath.Join(t.TempDir(), "root"))
	h := NewHandler(repo, []string{root}, &fakeRemover{}, zerolog.Nop())
	mux := http.NewServeMux()
	h.Mount(mux.Handle, func(next http.Handler) http.Handler { return next })

	body, _ := json.Marshal(map[string]string{"name": "x", "path": ""})
	req := httptest.NewRequest(http.MethodPost, "/api/projects", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty path: status = %d, want 400", rec.Code)
	}
}

// The registration race: concurrent registration of the SAME directory must
// produce exactly one success and ErrDuplicatePath for every loser —
// never a phantom writability failure from a shared probe name — and
// leave no probe files behind.
func TestConcurrentRegistrationSamePath(t *testing.T) {
	repo, _ := newTestRepo(t)
	ctx := context.Background()
	root := mkdir(t, filepath.Join(t.TempDir(), "root"))
	dir := mkdir(t, filepath.Join(root, "proj"))

	const n = 8
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			_, _, err := repo.Register(ctx, "x", dir, []string{root}, GitIdentity{Name: "Test Author", Email: "test@example.invalid"})
			errs <- err
		}()
	}
	var succeeded, duplicates, other int
	for i := 0; i < n; i++ {
		err := <-errs
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrDuplicatePath):
			duplicates++
		default:
			other++
			t.Logf("unexpected error: %v", err)
		}
	}
	if succeeded != 1 || duplicates != n-1 || other != 0 {
		t.Fatalf("succeeded=%d duplicates=%d other=%d, want 1/%d/0", succeeded, duplicates, other, n-1)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if e.Name() != ".git" {
			t.Fatalf("probe left behind: %s", e.Name())
		}
	}
}

func TestMakeDirectoryCreatesOnlyPlainFoldersInsideRoots(t *testing.T) {
	root := t.TempDir()
	created, err := MakeDirectory(root, "brand-new", []string{root})
	if err != nil {
		t.Fatalf("MakeDirectory: %v", err)
	}
	info, err := os.Stat(created)
	if err != nil || !info.IsDir() {
		t.Fatalf("created path %q is not a directory: %v", created, err)
	}

	for _, bad := range []string{"", ".", "..", "a/b", "a\\b", " trailing", "lead "} {
		if _, err := MakeDirectory(root, bad, []string{root}); !errors.Is(err, ErrInvalidDir) {
			t.Errorf("name %q: err = %v, want ErrInvalidDir", bad, err)
		}
	}
	if _, err := MakeDirectory(root, "brand-new", []string{root}); !errors.Is(err, ErrDuplicatePath) {
		t.Errorf("existing folder: err = %v, want ErrDuplicatePath", err)
	}
	outside := t.TempDir()
	if _, err := MakeDirectory(outside, "x", []string{root}); !errors.Is(err, ErrOutsideRoots) {
		t.Errorf("outside parent: err = %v, want ErrOutsideRoots", err)
	}
	if _, err := MakeDirectory("relative", "x", []string{root}); !errors.Is(err, ErrInvalidDir) {
		t.Errorf("relative parent: err = %v, want ErrInvalidDir", err)
	}
}

// A cascade that fails PART WAY tells the user what it already
// destroyed. Fail-fast means earlier sessions are gone (tmux killed,
// scrollback with them) while the project stays — a message that named
// only the failure would read as "nothing happened".
// The planted creation times are DISTINCT so the documented
// created_at ordering is genuinely pinned: the older session must be
// the one torn down first.
func TestHandlerRemovePartialCascadeDisclosesWhatItDestroyed(t *testing.T) {
	repo, d := newTestRepo(t)
	root := mkdir(t, filepath.Join(t.TempDir(), "root"))
	dir := mkdir(t, filepath.Join(root, "proj"))
	p, _, err := repo.Register(context.Background(), "demo", dir, []string{root}, GitIdentity{Name: "Test Author", Email: "test@example.invalid"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	// "later" is inserted FIRST and carries the newer timestamp: if the
	// query dropped its ORDER BY, insertion order would tear it down
	// first and the assertions below would fail.
	insertSessionAt(t, d, "s-later", p.ID, "starting", "later-session", "2026-01-01T00:05:00.000Z")
	insertSessionAt(t, d, "s-older", p.ID, "starting", "older-session", "2026-01-01T00:01:00.000Z")

	// The second teardown fails; the first has already happened.
	var seen int
	remover := &fakeRemover{onRemove: func(id string) {
		markSessionExited(t, d, id, "2026-01-01T00:09:00.000Z")
	}}
	failing := &sequencedRemover{inner: remover, failAfter: 1, err: errors.New("pane dead")}
	h := NewHandler(repo, []string{root}, failing, zerolog.Nop())
	mux := http.NewServeMux()
	h.Mount(mux.Handle, func(next http.Handler) http.Handler { return next })
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/projects/"+p.ID, nil))
	seen = len(remover.removed)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", rec.Code, rec.Body)
	}
	if seen != 1 || remover.removed[0] != "s-older" {
		t.Fatalf("torn down %v, want only the OLDER session first", remover.removed)
	}
	var body struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(body.Message, "later-session") {
		t.Fatalf("message does not name the session that failed: %q", body.Message)
	}
	if !strings.Contains(body.Message, "older-session") {
		t.Fatalf("message hides the session it already destroyed: %q", body.Message)
	}
	if _, err := repo.Get(context.Background(), p.ID); err != nil {
		t.Fatalf("partial cascade must leave the project active: %v", err)
	}
}

// sequencedRemover succeeds failAfter times, then fails — the partial
// cascade the fail-fast loop can produce.
type sequencedRemover struct {
	inner     *fakeRemover
	failAfter int
	err       error
	calls     int
}

func (s *sequencedRemover) Remove(ctx context.Context, id string) error {
	s.calls++
	if s.calls > s.failAfter {
		return s.err
	}
	return s.inner.Remove(ctx, id)
}

// A lane on the project refuses the removal before any session is touched,
// and the refusal reaches the user as written.
func TestHandlerRemoveRefusesAProjectWithALane(t *testing.T) {
	repo, d := newTestRepo(t)
	root := mkdir(t, filepath.Join(t.TempDir(), "root"))
	dir := mkdir(t, filepath.Join(root, "proj"))
	p, _, err := repo.Register(context.Background(), "demo", dir, []string{root}, GitIdentity{Name: "Test Author", Email: "test@example.invalid"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	insertSession(t, d, "s-live", p.ID, "starting", "")
	if err := d.WithTx(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO tutorial_lanes (project_id, tag, task_id, phase, renewed_at, created_at)
			VALUES (?, 'main', 1, 'open', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`, p.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	remover := &fakeRemover{}
	h := NewHandler(repo, []string{root}, remover, zerolog.Nop())
	mux := http.NewServeMux()
	h.Mount(mux.Handle, func(next http.Handler) http.Handler { return next })
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/projects/"+p.ID, nil))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "removal_blocked") || !strings.Contains(rec.Body.String(), "open task lane") {
		t.Fatalf("remove = %d %s", rec.Code, rec.Body)
	}
	if len(remover.removed) != 0 {
		t.Fatalf("a refused removal stopped sessions: %v", remover.removed)
	}
	// The atomic check holds even for a caller that skips the pre-check.
	if err := repo.Remove(context.Background(), p.ID); !errors.Is(err, ErrRemovalBlocked) {
		t.Fatalf("Remove = %v, want ErrRemovalBlocked", err)
	}
	if _, err := repo.Get(context.Background(), p.ID); err != nil {
		t.Fatalf("project must stay: %v", err)
	}
}
