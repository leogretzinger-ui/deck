package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	"github.com/noisefloor-oss/deck/internal/db"
	"github.com/noisefloor-oss/deck/internal/project"
)

// newProjectEnv registers a real project in a real SQLite registry and
// returns its canonical path and id.
func newProjectEnv(t *testing.T) (*project.Repository, *project.Project) {
	t.Helper()
	d, err := db.Open(context.Background(), db.Config{
		Path: filepath.Join(t.TempDir(), "control.db"),
	})
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	repo := project.NewRepository(d)
	root := t.TempDir()
	p, _, err := repo.Register(context.Background(), "demo", root, []string{root}, project.GitIdentity{Name: "Test Author", Email: "test@example.invalid"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	return repo, p
}

// writeTM lays down a retired .taskmaster projection for low-level path and
// containment tests. Production discovery uses the engine-selected SQLite
// authority instead.
func writeTM(t *testing.T, root string, tasksJSON string, prds ...string) {
	t.Helper()
	tasksDir := filepath.Join(root, ".taskmaster", "tasks")
	docsDir := filepath.Join(root, ".taskmaster", "docs")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(docsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tasksDir, "tasks.json"), []byte(tasksJSON), 0o644); err != nil {
		t.Fatalf("write tasks.json: %v", err)
	}
	for _, prd := range prds {
		if err := os.WriteFile(filepath.Join(docsDir, prd), []byte("# prd"), 0o644); err != nil {
			t.Fatalf("write prd: %v", err)
		}
	}
}

// writeStore lays down the schema-4 project shape the stub engines key on:
// the store marker standing in for the SQLite database, plus the docs root.
func writeStore(t *testing.T, root string, prds ...string) {
	t.Helper()
	docsDir := filepath.Join(root, ".tasks", "docs")
	if err := os.MkdirAll(docsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".tasks", "tasks.sqlite"), nil, 0o600); err != nil {
		t.Fatalf("write store marker: %v", err)
	}
	for _, prd := range prds {
		if err := os.WriteFile(filepath.Join(docsDir, prd), []byte("# prd"), 0o644); err != nil {
			t.Fatalf("write prd: %v", err)
		}
	}
}

const sampleTasks = `{"master": {"tasks": [
  {"id": "1", "title": "first", "description": "d", "details": "long form", "status": "done",
   "priority": "high", "dependencies": []},
  {"id": "2", "title": "second", "description": "d", "status": "in-progress",
   "priority": "high", "dependencies": ["1"],
   "subtasks": [{"id": 1, "parentId": "2", "title": "sub", "status": "pending"}]}
]}}`

func TestDetectProject(t *testing.T) {
	root := t.TempDir()
	if _, err := DetectProject(root); !errors.Is(err, ErrNotTaskmasterProject) {
		t.Fatalf("empty dir: %v", err)
	}
	writeTM(t, root, sampleTasks)
	info, err := DetectProject(root)
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if info.TasksPath != filepath.Join(root, ".taskmaster", "tasks") {
		t.Fatalf("tasks path = %q", info.TasksPath)
	}
}

func TestReadTasks(t *testing.T) {
	root := t.TempDir()
	writeTM(t, root, sampleTasks)
	tags, err := ReadTasks(root)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	master, ok := tags["master"]
	if !ok || len(master.Tasks) != 2 {
		t.Fatalf("tags = %+v", tags)
	}
	if master.Tasks[1].Subtasks[0].Title != "sub" {
		t.Fatalf("subtask lost: %+v", master.Tasks[1].Subtasks)
	}

	// An external edit is visible on the next read, with no watch machinery
	// and no cache invalidation.
	writeTM(t, root, `{"master": {"tasks": [{"id": "9", "title": "edited", "status": "pending"}]}}`)
	tags, err = ReadTasks(root)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if tags["master"].Tasks[0].Title != "edited" {
		t.Fatal("external edit not visible on re-read")
	}
}

func TestTaskFileAccessPinsDescriptorsAcrossDirectorySwaps(t *testing.T) {
	for _, swapRoot := range []bool{false, true} {
		swapName := "descendant"
		if swapRoot {
			swapName = "project-root"
		}
		for _, operation := range []string{"read", "list", "create", "replace"} {
			t.Run(swapName+"/"+operation, func(t *testing.T) {
				root, insideTree, outsideTree, swap := taskSwapFixture(t, swapRoot)
				hooks := taskAccessHooks{}
				if swapRoot {
					hooks.rootAnchored = swap
				} else {
					hooks.targetOpened = swap
				}

				switch operation {
				case "read":
					tags, err := readTasks(root, hooks)
					if err != nil {
						t.Fatalf("read after swap: %v", err)
					}
					if got := tags["master"].Tasks[0].Title; got != "inside" {
						t.Fatalf("read crossed the descriptor boundary: %q", got)
					}
				case "list":
					prds, err := listPRDFiles(root, hooks)
					if err != nil {
						t.Fatalf("list after swap: %v", err)
					}
					if len(prds) != 1 || prds[0] != "inside.md" {
						t.Fatalf("listing crossed the descriptor boundary: %v", prds)
					}
				case "create":
					if err := writePRDFile(root, "created.md", "# created inside", false, hooks); err != nil {
						t.Fatalf("create after swap: %v", err)
					}
					assertFileBody(t, filepath.Join(insideTree(), "docs", "created.md"), "# created inside")
					assertFileMissing(t, filepath.Join(outsideTree, "docs", "created.md"))
				case "replace":
					if err := writePRDFile(root, "inside.md", "# replaced inside", true, hooks); err != nil {
						t.Fatalf("replace after swap: %v", err)
					}
					assertFileBody(t, filepath.Join(insideTree(), "docs", "inside.md"), "# replaced inside")
					assertFileBody(t, filepath.Join(outsideTree, "docs", "inside.md"), "# outside")
				}
			})
		}
	}
}

func taskSwapFixture(t *testing.T, swapRoot bool) (string, func() string, string, func()) {
	t.Helper()
	container := t.TempDir()
	root := filepath.Join(container, "project")
	outsideRoot := filepath.Join(t.TempDir(), "outside")
	insideJSON := `{"master":{"tasks":[{"id":"1","title":"inside","status":"pending"}]}}`
	outsideJSON := `{"master":{"tasks":[{"id":"1","title":"outside","status":"pending"}]}}`
	writeTM(t, root, insideJSON, "inside.md")
	writeTM(t, outsideRoot, outsideJSON, "inside.md", "outside.md")
	if err := os.WriteFile(filepath.Join(root, ".taskmaster", "docs", "inside.md"), []byte("# inside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outsideRoot, ".taskmaster", "docs", "inside.md"), []byte("# outside"), 0o644); err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(root, ".taskmaster")
	replacement := filepath.Join(outsideRoot, ".taskmaster")
	saved := filepath.Join(root, ".taskmaster.anchored")
	insideTree := func() string { return saved }
	if swapRoot {
		target = root
		replacement = outsideRoot
		saved = root + ".anchored"
		insideTree = func() string { return filepath.Join(saved, ".taskmaster") }
	}
	swapped := false
	swap := func() {
		if swapped {
			t.Fatal("swap hook ran more than once")
		}
		swapped = true
		if err := os.Rename(target, saved); err != nil {
			t.Fatalf("rename swap target: %v", err)
		}
		if err := os.Symlink(replacement, target); err != nil {
			t.Fatalf("install outside symlink: %v", err)
		}
	}
	return root, insideTree, filepath.Join(outsideRoot, ".taskmaster"), swap
}

func assertFileBody(t *testing.T, path, want string) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(body) != want {
		t.Fatalf("%s = %q, want %q", path, body, want)
	}
}

func assertFileMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("outside file exists or cannot be checked: %s: %v", path, err)
	}
}

func TestReadTasksMissing(t *testing.T) {
	root := t.TempDir()
	writeTM(t, root, sampleTasks)
	if err := os.Remove(filepath.Join(root, ".taskmaster", "tasks", "tasks.json")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := ReadTasks(root); err == nil || errors.Is(err, ErrNotTaskmasterProject) {
		t.Fatalf("missing tasks.json: err = %v", err)
	}
}

func TestListPRDFiles(t *testing.T) {
	root := t.TempDir()
	writeTM(t, root, sampleTasks, "b.md", "a.md", "not-a-prd.txt")
	prds, err := ListPRDFiles(root)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(prds) != 2 || prds[0] != "a.md" || prds[1] != "b.md" {
		t.Fatalf("prds = %v", prds)
	}

	// No docs dir at all is an empty list, not an error.
	root2 := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root2, ".taskmaster", "tasks"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	prds, err = ListPRDFiles(root2)
	if err != nil || len(prds) != 0 {
		t.Fatalf("no docs dir: %v %v", prds, err)
	}
}

// The endpoints: registered project -> detection/tasks/prds; unregistered
// -> 404; uninitialized store -> 404 with the distinct code.
func TestHandlerEndpoints(t *testing.T) {
	repo, p := newProjectEnv(t)
	writeStore(t, p.CanonicalPath, "PRD.md")

	h := NewHandler(repo, writeStub(t, `
if [ "$1" = tasks ] && [ "$2" = list ] && [ "$3" = --json ]; then
  printf '%s\n' '{"tag":"main","total":2,"counts":{"done":1,"in-progress":1},"tasks":[{"id":"1","title":"first","description":"d","details":"long form","status":"done","priority":"high","revision":1},{"id":"2","title":"second","description":"d","status":"in-progress","priority":"high","revision":2,"subtasks":[{"id":1,"parentId":"2","title":"sub","status":"pending"}]}]}'
  exit 0
fi
exit 29
`), zerolog.Nop())
	mux := http.NewServeMux()
	h.Mount(mux.Handle, func(next http.Handler) http.Handler { return next })

	do := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	if rec := do("/api/projects/" + p.ID + "/taskmaster"); rec.Code != http.StatusOK {
		t.Fatalf("info: status = %d, body %s", rec.Code, rec.Body)
	}
	rec := do("/api/projects/" + p.ID + "/taskmaster/tasks")
	if rec.Code != http.StatusOK {
		t.Fatalf("tasks: status = %d, body %s", rec.Code, rec.Body)
	}
	var tags map[string]TagFile
	if err := json.NewDecoder(rec.Body).Decode(&tags); err != nil || len(tags["main"].Tasks) != 2 {
		t.Fatalf("tasks body: %v", err)
	}
	if tags["main"].Tasks[0].Details != "" {
		t.Fatalf("list leaked task details: %q", tags["main"].Tasks[0].Details)
	}

	rec = do("/api/projects/" + p.ID + "/taskmaster/tasks/1")
	var task Task
	if err := json.NewDecoder(rec.Body).Decode(&task); err != nil || task.Details != "long form" {
		t.Fatalf("task detail body: %+v, %v", task, err)
	}
	if rec := do("/api/projects/" + p.ID + "/taskmaster/tasks/missing"); rec.Code != http.StatusNotFound {
		t.Fatalf("missing task: status = %d, want 404", rec.Code)
	}
	if rec := do("/api/projects/" + p.ID + "/taskmaster/tasks/2.1"); rec.Code != http.StatusOK {
		t.Fatalf("qualified subtask: status = %d, body %s", rec.Code, rec.Body)
	}

	rec = do("/api/projects/" + p.ID + "/taskmaster/prds")
	var prds []string
	if err := json.NewDecoder(rec.Body).Decode(&prds); err != nil || len(prds) != 1 || prds[0] != "PRD.md" {
		t.Fatalf("prds body: %v %v", prds, err)
	}

	if rec := do("/api/projects/nope/taskmaster"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown project: status = %d, want 404", rec.Code)
	}

	// A registered project without an initialized store: the distinct 404.
	plainDir := t.TempDir()
	p2, _, err := repo.Register(context.Background(), "plain", plainDir, []string{plainDir}, project.GitIdentity{Name: "Test Author", Email: "test@example.invalid"})
	if err != nil {
		t.Fatalf("register plain: %v", err)
	}
	rec = do("/api/projects/" + p2.ID + "/taskmaster")
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "not_taskmaster_project") ||
		!strings.Contains(rec.Body.String(), "no initialized task state") {
		t.Fatalf("non-taskmaster project: status = %d body %s", rec.Code, rec.Body)
	}
	for _, path := range []string{
		"/api/projects/" + p2.ID + "/taskmaster/tasks",
		"/api/projects/" + p2.ID + "/taskmaster/prds",
	} {
		rec = do(path)
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "no initialized task state") {
			t.Fatalf("uninitialized task endpoint %s: status = %d body %s", path, rec.Code, rec.Body)
		}
	}
}

// The upload endpoint uses exclusive-create by default (a second
// prd.md is a 409), but replace=true overwrites. The request shape stays
// backward compatible — an absent replace reads as false.
func TestHandlerUploadPRDReplace(t *testing.T) {
	repo, p := newProjectEnv(t)
	writeStore(t, p.CanonicalPath)

	h := NewHandler(repo, writeStub(t, "exit 0"), zerolog.Nop())
	mux := http.NewServeMux()
	h.Mount(mux.Handle, func(next http.Handler) http.Handler { return next })

	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost,
			"/api/projects/"+p.ID+"/taskmaster/prds", strings.NewReader(body))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	rec := post(`{"name":"prd.md","content":"# one"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("first upload: status = %d body %s", rec.Code, rec.Body)
	}
	// Without the flag, a repeat is the prd_exists 409.
	rec = post(`{"name":"prd.md","content":"# two"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "prd_exists") {
		t.Fatalf("collision: status = %d body %s", rec.Code, rec.Body)
	}
	// replace=true overwrites and the new content lands on disk.
	rec = post(`{"name":"prd.md","content":"# two","replace":true}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("replace upload: status = %d body %s", rec.Code, rec.Body)
	}
	data, err := os.ReadFile(filepath.Join(p.CanonicalPath, ".tasks", "docs", "prd.md"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(data), "# two") || strings.Contains(string(data), "# one") {
		t.Fatalf("replace did not overwrite: %s", data)
	}
}

// A .taskmaster tree that symlinks outside the registered project root is
// refused, not followed; a symlink resolving inside the root is fine.
func TestSymlinkEscape(t *testing.T) {
	outside := t.TempDir()
	writeTM(t, outside, `{"master": {"tasks": [{"id": "1", "title": "outside", "status": "pending"}]}}`, "OUT.md")

	root := t.TempDir()
	if err := os.Symlink(filepath.Join(outside, ".taskmaster"), filepath.Join(root, ".taskmaster")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if _, err := DetectProject(root); !errors.Is(err, ErrUnsafeTree) {
		t.Fatalf("detect through escape: %v", err)
	}
	if _, err := ReadTasks(root); !errors.Is(err, ErrUnsafeTree) {
		t.Fatalf("read through escape: %v", err)
	}
	if _, err := ListPRDFiles(root); !errors.Is(err, ErrUnsafeTree) {
		t.Fatalf("list through escape: %v", err)
	}

	// A symlinked tasks.json inside a real .taskmaster is refused too.
	root2 := t.TempDir()
	writeTM(t, root2, sampleTasks)
	if err := os.Remove(filepath.Join(root2, ".taskmaster", "tasks", "tasks.json")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := os.Symlink(filepath.Join(outside, ".taskmaster", "tasks", "tasks.json"),
		filepath.Join(root2, ".taskmaster", "tasks", "tasks.json")); err != nil {
		t.Fatalf("symlink tasks.json: %v", err)
	}
	if _, err := ReadTasks(root2); !errors.Is(err, ErrUnsafeTree) {
		t.Fatalf("read symlinked tasks.json: %v", err)
	}

	// A symlink that resolves INSIDE the root is followed normally.
	root3 := t.TempDir()
	writeTM(t, root3, sampleTasks)
	if err := os.Rename(filepath.Join(root3, ".taskmaster"), filepath.Join(root3, "tm-real")); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if err := os.Symlink(filepath.Join(root3, "tm-real"), filepath.Join(root3, ".taskmaster")); err != nil {
		t.Fatalf("symlink in-root: %v", err)
	}
	if _, err := ReadTasks(root3); err != nil {
		t.Fatalf("in-root symlink must be followed: %v", err)
	}
}

// The endpoints surface an unsafe tree as a refusal, not a 404 the panel
// would silently hide on. Under the schema-4 contract the only worktree
// path the console traverses is the engine-selected docs root.
func TestHandlerUnsafeTree(t *testing.T) {
	repo, p := newProjectEnv(t)
	writeStore(t, p.CanonicalPath)
	outside := t.TempDir()
	if err := os.RemoveAll(filepath.Join(p.CanonicalPath, ".tasks", "docs")); err != nil {
		t.Fatalf("remove docs: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(p.CanonicalPath, ".tasks", "docs")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	h := NewHandler(repo, writeStub(t, "exit 0"), zerolog.Nop())
	mux := http.NewServeMux()
	h.Mount(mux.Handle, func(next http.Handler) http.Handler { return next })

	req := httptest.NewRequest(http.MethodGet, "/api/projects/"+p.ID+"/taskmaster/prds", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "unsafe_taskmaster_tree") {
		t.Fatalf("prds: status = %d body %s", rec.Code, rec.Body)
	}
	// Detection never traverses the docs root, so it still answers.
	req = httptest.NewRequest(http.MethodGet, "/api/projects/"+p.ID+"/taskmaster", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("info with escaped docs: status = %d body %s", rec.Code, rec.Body)
	}
}

// A DANGLING symlink still escapes: the link target's location decides,
// so a missing destination must be ErrUnsafeTree, never absence (200/404).
func TestDanglingSymlinkEscape(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "does-not-exist")

	// .taskmaster -> outside/nonexistent
	root := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, ".taskmaster")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if _, err := DetectProject(root); !errors.Is(err, ErrUnsafeTree) {
		t.Fatalf("dangling .taskmaster escape: %v", err)
	}

	// .taskmaster -> outside REGULAR FILE: Stat succeeds through the link,
	// so the escape check must run before the not-a-directory one.
	outsideFile := filepath.Join(t.TempDir(), "plain-file")
	if err := os.WriteFile(outsideFile, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	rootFile := t.TempDir()
	if err := os.Symlink(outsideFile, filepath.Join(rootFile, ".taskmaster")); err != nil {
		t.Fatalf("symlink file: %v", err)
	}
	if _, err := DetectProject(rootFile); !errors.Is(err, ErrUnsafeTree) {
		t.Fatalf(".taskmaster -> outside regular file: %v", err)
	}
	// Same shape in-root: a regular file is plain absence, not an escape.
	rootInFile := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootInFile, ".taskmaster"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write in-root file: %v", err)
	}
	if _, err := DetectProject(rootInFile); !errors.Is(err, ErrNotTaskmasterProject) {
		t.Fatalf("in-root regular .taskmaster: %v", err)
	}

	// docs -> outside/nonexistent inside a real .taskmaster
	root2 := t.TempDir()
	writeTM(t, root2, sampleTasks)
	if err := os.RemoveAll(filepath.Join(root2, ".taskmaster", "docs")); err != nil {
		t.Fatalf("remove docs: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root2, ".taskmaster", "docs")); err != nil {
		t.Fatalf("symlink docs: %v", err)
	}
	if _, err := ListPRDFiles(root2); !errors.Is(err, ErrUnsafeTree) {
		t.Fatalf("dangling docs escape: %v", err)
	}

	// tasks.json -> outside/nonexistent
	root3 := t.TempDir()
	writeTM(t, root3, sampleTasks)
	if err := os.Remove(filepath.Join(root3, ".taskmaster", "tasks", "tasks.json")); err != nil {
		t.Fatalf("remove tasks.json: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root3, ".taskmaster", "tasks", "tasks.json")); err != nil {
		t.Fatalf("symlink tasks.json: %v", err)
	}
	if _, err := ReadTasks(root3); !errors.Is(err, ErrUnsafeTree) {
		t.Fatalf("dangling tasks.json escape: %v", err)
	}
}

// The endpoints map a dangling escape to the same refusal as a live one.
func TestHandlerDanglingEscape(t *testing.T) {
	repo, p := newProjectEnv(t)
	writeStore(t, p.CanonicalPath)
	outside := filepath.Join(t.TempDir(), "does-not-exist")
	if err := os.RemoveAll(filepath.Join(p.CanonicalPath, ".tasks", "docs")); err != nil {
		t.Fatalf("remove docs: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(p.CanonicalPath, ".tasks", "docs")); err != nil {
		t.Fatalf("symlink docs: %v", err)
	}

	h := NewHandler(repo, writeStub(t, "exit 0"), zerolog.Nop())
	mux := http.NewServeMux()
	h.Mount(mux.Handle, func(next http.Handler) http.Handler { return next })

	req := httptest.NewRequest(http.MethodGet, "/api/projects/"+p.ID+"/taskmaster/prds", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "unsafe_taskmaster_tree") {
		t.Fatalf("prds: status = %d body %s", rec.Code, rec.Body)
	}
	// Detection and task reads never traverse the docs root.
	req = httptest.NewRequest(http.MethodGet, "/api/projects/"+p.ID+"/taskmaster", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("info with dangling docs: status = %d body %s", rec.Code, rec.Body)
	}
}

// A Stat failure that is not "does not exist" must not become the
// not-a-project state.
func TestStatFailureIsNotAbsence(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores permission bits")
	}
	root := t.TempDir()
	writeTM(t, root, sampleTasks)
	// Locking the ROOT makes Stat(.taskmaster) fail with EACCES: stat on a
	// path needs execute on its parent, not on the path itself.
	if err := os.Chmod(root, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o755) })
	if _, err := DetectProject(root); err == nil ||
		errors.Is(err, ErrNotTaskmasterProject) || errors.Is(err, ErrUnsafeTree) {
		t.Fatalf("permission failure misclassified: %v", err)
	}
}

func TestWritePRDFileOnboardsAndRefuses(t *testing.T) {
	root := t.TempDir()
	// A project with no .taskmaster at all gets the skeleton and the file.
	if err := WritePRDFile(root, "checkout-redesign.md", "# PRD\n\nDo the thing.", false); err != nil {
		t.Fatalf("write: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, ".taskmaster", "docs", "checkout-redesign.md"))
	if err != nil || !strings.Contains(string(data), "PRD") {
		t.Fatalf("file: %v", err)
	}
	// Never overwrite without the replace flag.
	if err := WritePRDFile(root, "checkout-redesign.md", "other", false); !errors.Is(err, ErrPRDExists) {
		t.Fatalf("overwrite: %v", err)
	}
	// Bad names and content.
	for _, bad := range []string{"", "x.txt", "a/b.md", ".hidden.md", " lead.md"} {
		if err := WritePRDFile(root, bad, "content", false); !errors.Is(err, ErrInvalidPRD) {
			t.Errorf("name %q: %v", bad, err)
		}
	}
	if err := WritePRDFile(root, "ok.md", "  ", false); !errors.Is(err, ErrInvalidPRD) {
		t.Errorf("empty content: %v", err)
	}
	// A symlinked docs dir escaping the root is refused.
	escape := t.TempDir()
	linkRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(linkRoot, ".taskmaster"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(escape, filepath.Join(linkRoot, ".taskmaster", "docs")); err != nil {
		t.Fatal(err)
	}
	if err := WritePRDFile(linkRoot, "x.md", "content", false); !errors.Is(err, ErrUnsafeTree) {
		t.Fatalf("symlink escape: %v", err)
	}
}

// The replace flag overwrites an existing canonical prd.md, but
// a symlinked prd.md pointing outside the root is still refused (replace
// must never follow a link out of the project).
func TestWritePRDFileReplaceOverwritesAndRefusesEscape(t *testing.T) {
	root := t.TempDir()
	if err := WritePRDFile(root, "prd.md", "# original", false); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := WritePRDFile(root, "prd.md", "# replaced", true); err != nil {
		t.Fatalf("replace: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, ".taskmaster", "docs", "prd.md"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(data), "replaced") || strings.Contains(string(data), "original") {
		t.Fatalf("replace did not overwrite: %s", data)
	}

	// A symlinked prd.md inside docs that escapes is refused even in
	// replace mode — O_TRUNC must not write through an escaping link.
	escRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(escRoot, ".taskmaster", "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "stolen.md")
	if err := os.Symlink(outside, filepath.Join(escRoot, ".taskmaster", "docs", "prd.md")); err != nil {
		t.Fatal(err)
	}
	if err := WritePRDFile(escRoot, "prd.md", "content", true); !errors.Is(err, ErrUnsafeTree) {
		t.Fatalf("replace through escape: %v", err)
	}
}

func TestFallbackRefusesLinksThatLeaveAndReenterProject(t *testing.T) {
	project := t.TempDir()
	real := filepath.Join(project, "real")
	if err := os.Mkdir(real, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, "file.md"), []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	bridge := filepath.Join(t.TempDir(), "bridge")
	if err := os.Symlink(real, bridge); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(bridge, filepath.Join(project, "docs")); err != nil {
		t.Fatal(err)
	}
	root, err := openTaskRoot(project, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	f, err := root.openFile("docs/file.md", os.O_RDONLY, 0)
	if f != nil {
		f.Close()
	}
	if !errors.Is(err, ErrUnsafeTree) {
		t.Fatalf("fallback traversed an outside link: %v", err)
	}
}

func TestFallbackUsesAnchoredRootAfterRename(t *testing.T) {
	project := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(filepath.Join(project, "real"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "real", "file.md"), []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(project, "real"), filepath.Join(project, "docs")); err != nil {
		t.Fatal(err)
	}
	root, err := openTaskRoot(project, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := os.Rename(project, project+"-saved"); err != nil {
		t.Fatal(err)
	}
	f, err := root.openFile("docs/file.md", os.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	body, err := io.ReadAll(f)
	if err != nil || string(body) != "inside" {
		t.Fatalf("wrong anchored file: %q %v", body, err)
	}
}

func TestFallbackResolvesLinkBeforeParentComponent(t *testing.T) {
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, "deep", "child"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "deep", "file.md"), []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("deep/child", filepath.Join(project, "jump")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(project+"/jump/../file.md", filepath.Join(project, "document.md")); err != nil {
		t.Fatal(err)
	}
	root, err := openTaskRoot(project, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	f, err := root.openFile("document.md", os.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	body, err := io.ReadAll(f)
	if err != nil || string(body) != "inside" {
		t.Fatalf("wrong file: %q %v", body, err)
	}
}
