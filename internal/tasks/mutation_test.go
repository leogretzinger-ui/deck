package tasks

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rs/zerolog"
)

func TestAddTaskEngineArgvKeepsHostileValuesInSingleArguments(t *testing.T) {
	input := AddTaskInput{
		Title:       `$(touch nope); "quoted"`,
		Description: "line one\nline two",
		Details:     `--file=/tmp/escape`,
		Priority:    "high",
	}
	got := addTaskEngineArgv(input)
	want := []string{
		"tasks", "add",
		"-title", input.Title,
		"-description", input.Description,
		"-details", input.Details,
		"-priority", "high", "-json",
	}
	if len(got) != len(want) {
		t.Fatalf("argv length = %d, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("argv[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestAddTaskRunsEngineAtTheAnchoredProjectRoot(t *testing.T) {
	stub := writeStub(t, `
if [ "$1" != tasks ] || [ "$2" != add ] || [ "$3" != -title ] || [ ! -f ./.tasks/tasks.sqlite ]; then
  exit 29
fi
printf '{"id":"99"}\n'
`)
	e := newJobEnv(t, stub)
	h := NewHandler(e.projects, stub, zerolog.Nop())
	result, err := h.addTask(context.Background(), e.project.ID, AddTaskInput{
		Title: "A task", Description: "A description", Details: "Details", Priority: "medium",
	})
	if err != nil {
		t.Fatalf("addTask: %v", err)
	}
	if result.ID != "99" {
		t.Fatalf("id = %q, want 99", result.ID)
	}
	// The engine owns the write; no worktree file becomes task state.
	assertFileMissing(t, filepath.Join(e.project.CanonicalPath, ".tasks", "tasks"))
}

func TestAddTaskRefusesAnUninitializedStore(t *testing.T) {
	e := newJobEnv(t, writeStub(t, "exit 29"))
	removeStubStore(t, e)
	h := NewHandler(e.projects, e.runner.executable, zerolog.Nop())
	_, err := h.addTask(context.Background(), e.project.ID, AddTaskInput{
		Title: "A task", Description: "A description", Details: "Details", Priority: "medium",
	})
	if !errors.Is(err, ErrNotTaskmasterProject) {
		t.Fatalf("addTask = %v, want ErrNotTaskmasterProject", err)
	}
}

func TestAddTaskEndpointValidatesAndReportsCLIExit(t *testing.T) {
	stub := writeStub(t, "exit 17")
	e := newJobEnv(t, stub)
	h := NewHandler(e.projects, stub, zerolog.Nop())
	mux := http.NewServeMux()
	h.Mount(mux.Handle, func(next http.Handler) http.Handler { return next })

	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost,
			"/api/projects/"+e.project.ID+"/taskmaster/tasks", strings.NewReader(body))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	if rec := post(`{"title":"","priority":"medium"}`); rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), "invalid_task") {
		t.Fatalf("invalid: %d %s", rec.Code, rec.Body)
	}
	if rec := post(`{"title":"Valid","description":"","details":"","priority":"medium"}`); rec.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(rec.Body.String(), "task_add_failed") {
		t.Fatalf("exit: %d %s", rec.Code, rec.Body)
	}
}

func TestValidateAddTaskRequiresKnownPriority(t *testing.T) {
	_, err := validateAddTask(AddTaskInput{Title: "Valid", Priority: "urgent"})
	if !errors.Is(err, ErrInvalidTask) {
		t.Fatalf("err = %v, want ErrInvalidTask", err)
	}
}
