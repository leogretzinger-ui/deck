package tasks

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"
)

// initStub answers `tasks init -json` by creating the store marker the
// context stub reads, so initialization moves the project from
// uninitialized to initialized exactly as the engine does.
const initStub = `
if [ "$1" = tasks ] && [ "$2" = init ] && [ "$3" = -json ]; then
  project=${NOISE_PROJECT_DIR:-$PWD}
  mkdir -p "$project/.tasks"
  : > "$project/.tasks/tasks.sqlite"
  printf '{"path":"%s/.tasks/tasks.sqlite","tag":"main"}\n' "$project"
  exit 0
fi
exit 29
`

func TestInitializeGivesAnUnstartedProjectABoard(t *testing.T) {
	stub := writeStub(t, initStub)
	e := newJobEnv(t, stub)
	removeStubStore(t, e)
	h := NewHandler(e.projects, stub, zerolog.Nop())

	// Detection reports the absence as a state, which is what hides the panel.
	if _, err := DetectProjectWithEngine(context.Background(), stub, e.project.CanonicalPath); !errors.Is(err, ErrNotTaskmasterProject) {
		t.Fatalf("detect before init = %v, want ErrNotTaskmasterProject", err)
	}

	result, err := h.initialize(context.Background(), e.project.ID)
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if result.AlreadyDone {
		t.Fatal("a first initialization reported itself as already done")
	}
	if !strings.HasSuffix(result.Path, "tasks.sqlite") {
		t.Fatalf("store path = %q, want the engine's store", result.Path)
	}

	// The project is now usable — no PRD was supplied at any point.
	if _, err := DetectProjectWithEngine(context.Background(), stub, e.project.CanonicalPath); err != nil {
		t.Fatalf("detect after init = %v, want a detected project", err)
	}
}

func TestInitializeIsIdempotent(t *testing.T) {
	stub := writeStub(t, initStub)
	e := newJobEnv(t, stub)
	h := NewHandler(e.projects, stub, zerolog.Nop())

	// newJobEnv starts initialized, so this is the repeated-click path: the
	// caller asked for task state to exist, and it does.
	result, err := h.initialize(context.Background(), e.project.ID)
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if !result.AlreadyDone {
		t.Fatal("an initialized project did not report itself as already done")
	}
}

func TestInitializeEndpointReportsCreatedThenOK(t *testing.T) {
	stub := writeStub(t, initStub)
	e := newJobEnv(t, stub)
	removeStubStore(t, e)
	h := NewHandler(e.projects, stub, zerolog.Nop())
	mux := http.NewServeMux()
	h.Mount(mux.Handle, func(next http.Handler) http.Handler { return next })

	post := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost,
			"/api/projects/"+e.project.ID+"/taskmaster", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	if rec := post(); rec.Code != http.StatusCreated {
		t.Fatalf("first init: %d %s", rec.Code, rec.Body)
	}
	if rec := post(); rec.Code != http.StatusOK {
		t.Fatalf("repeat init: %d %s", rec.Code, rec.Body)
	}
}

func TestInitializeReportsAnEngineExitAsAFailedInit(t *testing.T) {
	stub := writeStub(t, "exit 17")
	e := newJobEnv(t, stub)
	removeStubStore(t, e)
	h := NewHandler(e.projects, stub, zerolog.Nop())
	mux := http.NewServeMux()
	h.Mount(mux.Handle, func(next http.Handler) http.Handler { return next })

	req := httptest.NewRequest(http.MethodPost,
		"/api/projects/"+e.project.ID+"/taskmaster", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "task_init_failed") {
		t.Fatalf("engine exit: %d %s", rec.Code, rec.Body)
	}
}

// loserStub is the engine as the LOSER of a first-time race sees it: the
// store did not exist when the handler checked, another writer created it
// before this invocation ran, and the engine refuses and exits non-zero with
// nothing on stdout. Creating the marker here IS the other writer — which is
// what makes the race deterministic instead of timing-dependent.
const loserStub = `
if [ "$1" = tasks ] && [ "$2" = init ] && [ "$3" = -json ]; then
  project=${NOISE_PROJECT_DIR:-$PWD}
  mkdir -p "$project/.tasks"
  : > "$project/.tasks/tasks.sqlite"
  echo "noise-tasks: backlog already exists" >&2
  exit 1
fi
exit 29
`

func TestInitializeLosingAFirstTimeRaceIsStillASuccess(t *testing.T) {
	stub := writeStub(t, loserStub)
	e := newJobEnv(t, stub)
	removeStubStore(t, e)
	h := NewHandler(e.projects, stub, zerolog.Nop())

	result, err := h.initialize(context.Background(), e.project.ID)
	if err != nil {
		t.Fatalf("initialize lost the race and reported failure: %v", err)
	}
	if !result.AlreadyDone {
		t.Fatal("the loser of a race did not report itself as already done")
	}
	if !strings.HasSuffix(result.Path, "tasks.sqlite") {
		t.Fatalf("store path = %q, want the settled store", result.Path)
	}
}

func TestInitializeEndpointAnswersARaceLoserWithOK(t *testing.T) {
	stub := writeStub(t, loserStub)
	e := newJobEnv(t, stub)
	removeStubStore(t, e)
	h := NewHandler(e.projects, stub, zerolog.Nop())
	mux := http.NewServeMux()
	h.Mount(mux.Handle, func(next http.Handler) http.Handler { return next })

	req := httptest.NewRequest(http.MethodPost,
		"/api/projects/"+e.project.ID+"/taskmaster", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("race loser: %d %s, want 200", rec.Code, rec.Body)
	}
}

// The reconciliation reads the post-state, so a failure that left no store
// stays a failure. Without that, any init error would report success.
func TestInitializeFailingWithoutAStoreStaysAFailure(t *testing.T) {
	stub := writeStub(t, "exit 17")
	e := newJobEnv(t, stub)
	removeStubStore(t, e)
	h := NewHandler(e.projects, stub, zerolog.Nop())

	_, err := h.initialize(context.Background(), e.project.ID)
	var exitErr *initializeExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("initialize = %v, want an exit failure", err)
	}
}
