package tasks

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"github.com/noisefloor-oss/deck/internal/project"
)

// Handler exposes the Taskmaster read side over HTTP: detection, canonical
// tasks, and PRD files for one registered project. With a runner attached
// (WithRunner) it also exposes the generation jobs.
type Handler struct {
	projects   *project.Repository
	executable string
	runner     *Runner
	log        zerolog.Logger
}

// NewHandler wires the endpoints. The projects repository owns the
// canonical-path lookup; this handler never takes a raw path from a client,
// so allowlisting stays the registry's business.
func NewHandler(projects *project.Repository, executable string, log zerolog.Logger) *Handler {
	return &Handler{projects: projects, executable: executable, log: log}
}

// WithRunner attaches the generation runner and returns the handler for
// chaining. The job endpoints are mounted only when a runner is attached.
func (h *Handler) WithRunner(r *Runner) *Handler {
	h.runner = r
	return h
}

// Mount registers the routes. handle and wrap are the server's registrar
// and the auth middleware, exactly as the other handlers take them.
func (h *Handler) Mount(handle func(pattern string, hd http.Handler), wrap func(http.Handler) http.Handler) {
	handle("GET /api/projects/{id}/taskmaster", wrap(http.HandlerFunc(h.Info)))
	handle("POST /api/projects/{id}/taskmaster", wrap(http.HandlerFunc(h.Initialize)))
	handle("GET /api/projects/{id}/taskmaster/tasks", wrap(http.HandlerFunc(h.Tasks)))
	handle("POST /api/projects/{id}/taskmaster/tasks", wrap(http.HandlerFunc(h.AddTask)))
	handle("GET /api/projects/{id}/taskmaster/tasks/{task}", wrap(http.HandlerFunc(h.TaskDetail)))
	handle("GET /api/projects/{id}/taskmaster/events", wrap(http.HandlerFunc(h.Events)))
	handle("GET /api/projects/{id}/taskmaster/prds", wrap(http.HandlerFunc(h.PRDs)))
	handle("POST /api/projects/{id}/taskmaster/prds", wrap(http.HandlerFunc(h.UploadPRD)))
	if h.runner != nil {
		handle("POST /api/projects/{id}/taskmaster/generate", wrap(http.HandlerFunc(h.Generate)))
		handle("GET /api/projects/{id}/taskmaster/runs", wrap(http.HandlerFunc(h.Runs)))
		handle("GET /api/projects/{id}/taskmaster/runs/{run}", wrap(http.HandlerFunc(h.RunDetail)))
	}
}

// Info reports whether the project is a Taskmaster project and, when it
// is, its structure. A non-Taskmaster project is a 404 with a distinct
// code, not an error: the panel hides itself on that answer.
func (h *Handler) Info(w http.ResponseWriter, r *http.Request) {
	path, ok := h.projectPath(w, r)
	if !ok {
		return
	}
	info, err := DetectProjectWithEngine(r.Context(), h.executable, path)
	if err != nil {
		h.detectErr(w, err)
		return
	}
	h.json(w, http.StatusOK, info)
}

// Initialize creates task state for a project that has none, so a project
// reaches a usable board without a PRD. Detection reports the absence as a
// state rather than a failure; this is the action that answers it.
//
// It is idempotent, and an already-initialized project is a success, not a
// conflict: the caller asked for task state to exist, and it does.
func (h *Handler) Initialize(w http.ResponseWriter, r *http.Request) {
	result, err := h.initialize(r.Context(), r.PathValue("id"))
	if err != nil {
		var exitErr *initializeExitError
		switch {
		case errors.Is(err, project.ErrNotFound):
			h.err(w, http.StatusNotFound, "not_found", "project not found")
		case errors.Is(err, ErrUnsafeTree), errors.Is(err, ErrTaskStateBlocked):
			h.detectErr(w, err)
		case errors.Is(err, ErrTaskStateReadOnly):
			h.err(w, http.StatusConflict, "task_state_blocked", err.Error())
		case errors.As(err, &exitErr):
			h.err(w, http.StatusUnprocessableEntity, "task_init_failed",
				"noise tasks init exited unsuccessfully")
		default:
			h.log.Error().Err(err).Msg("taskmaster initialize failed")
			h.err(w, http.StatusInternalServerError, "internal", "could not initialize task state")
		}
		return
	}
	status := http.StatusCreated
	if result.AlreadyDone {
		status = http.StatusOK
	}
	h.json(w, status, result)
}

// detectErr maps a detection/read failure: an unsafe tree is a refusal
// (403), a non-Taskmaster project is the distinct 404 the panel hides on,
// anything else is an internal failure.
func (h *Handler) detectErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrUnsafeTree):
		h.log.Warn().Err(err).Msg("taskmaster tree escapes project root")
		h.err(w, http.StatusForbidden, "unsafe_taskmaster_tree",
			"task state resolves outside the project root")
	case errors.Is(err, ErrTaskStateBlocked):
		h.err(w, http.StatusConflict, "task_state_blocked", err.Error())
	case errors.Is(err, ErrNotTaskmasterProject):
		h.err(w, http.StatusNotFound, "not_taskmaster_project",
			"this project has no initialized task state")
	default:
		h.log.Error().Err(err).Msg("taskmaster detect failed")
		h.err(w, http.StatusInternalServerError, "internal", "could not inspect project")
	}
}

// Tasks returns the engine's authoritative task state, read fresh on every
// request.
func (h *Handler) Tasks(w http.ResponseWriter, r *http.Request) {
	path, ok := h.projectPath(w, r)
	if !ok {
		return
	}
	tags, err := ReadTasksWithEngine(r.Context(), h.executable, path)
	if err != nil {
		if errors.Is(err, ErrUnsafeTree) ||
			errors.Is(err, ErrTaskStateBlocked) ||
			errors.Is(err, ErrNotTaskmasterProject) {
			h.detectErr(w, err)
			return
		}
		h.log.Error().Err(err).Msg("taskmaster tasks read failed")
		h.err(w, http.StatusInternalServerError, "internal", "could not read task state")
		return
	}
	for tag, file := range tags {
		stripTaskDetails(file.Tasks)
		tags[tag] = file
	}
	h.json(w, http.StatusOK, tags)
}

// AddTask is the one user-initiated browser mutation the first release
// permits. The pinned engine owns validation, ID allocation, and the
// authoritative write; this endpoint only validates its bounded request and
// maps process outcomes.
func (h *Handler) AddTask(w http.ResponseWriter, r *http.Request) {
	var input AddTaskInput
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, (3*addTaskMaxFieldSize)+1024))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&input); err != nil {
		h.err(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		h.err(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	result, err := h.addTask(r.Context(), r.PathValue("id"), input)
	if err != nil {
		var exitErr *addTaskExitError
		switch {
		case errors.Is(err, project.ErrNotFound):
			h.err(w, http.StatusNotFound, "not_found", "project not found")
		case errors.Is(err, ErrInvalidTask):
			h.err(w, http.StatusBadRequest, "invalid_task", err.Error())
		case errors.Is(err, ErrUnsafeTree), errors.Is(err, ErrNotTaskmasterProject), errors.Is(err, ErrTaskStateBlocked):
			h.detectErr(w, err)
		case errors.Is(err, ErrTaskStateReadOnly):
			h.err(w, http.StatusConflict, "task_state_blocked", err.Error())
		case errors.As(err, &exitErr):
			h.err(w, http.StatusUnprocessableEntity, "task_add_failed", "noise tasks add exited unsuccessfully")
		default:
			h.log.Error().Err(err).Msg("taskmaster add task failed")
			h.err(w, http.StatusInternalServerError, "internal", "could not add task")
		}
		return
	}
	h.json(w, http.StatusCreated, result)
}

// TaskDetail returns one authoritative task with its long-form fields. The board
// list deliberately omits details; opening a card pays this read cost once.
func (h *Handler) TaskDetail(w http.ResponseWriter, r *http.Request) {
	path, ok := h.projectPath(w, r)
	if !ok {
		return
	}
	tags, err := ReadTasksWithEngine(r.Context(), h.executable, path)
	if err != nil {
		if errors.Is(err, ErrUnsafeTree) ||
			errors.Is(err, ErrTaskStateBlocked) ||
			errors.Is(err, ErrNotTaskmasterProject) {
			h.detectErr(w, err)
			return
		}
		h.log.Error().Err(err).Msg("taskmaster task detail read failed")
		h.err(w, http.StatusInternalServerError, "internal", "could not read task state")
		return
	}
	wanted := r.PathValue("task")
	for _, file := range tags {
		if task := findTask(file.Tasks, wanted, ""); task != nil {
			h.json(w, http.StatusOK, task)
			return
		}
	}
	h.err(w, http.StatusNotFound, "not_found", "task not found")
}

func stripTaskDetails(tasks []Task) {
	for i := range tasks {
		tasks[i].Details = ""
		stripTaskDetails(tasks[i].Subtasks)
	}
}

func findTask(tasks []Task, wanted, parentID string) *Task {
	for i := range tasks {
		id := fmt.Sprint(tasks[i].ID)
		if parentID != "" && !strings.HasPrefix(id, parentID+".") {
			id = parentID + "." + id
		}
		if id == wanted {
			return &tasks[i]
		}
		if task := findTask(tasks[i].Subtasks, wanted, id); task != nil {
			return task
		}
	}
	return nil
}

// PRDs lists the eligible PRD filenames.
func (h *Handler) PRDs(w http.ResponseWriter, r *http.Request) {
	path, ok := h.projectPath(w, r)
	if !ok {
		return
	}
	prds, err := ListPRDFilesWithEngine(r.Context(), h.executable, path)
	if err != nil {
		if errors.Is(err, ErrUnsafeTree) || errors.Is(err, ErrTaskStateBlocked) || errors.Is(err, ErrNotTaskmasterProject) {
			h.detectErr(w, err)
			return
		}
		h.err(w, http.StatusInternalServerError, "internal", "could not list PRD files")
		return
	}
	h.json(w, http.StatusOK, prds)
}

// UploadPRD adds a PRD document: a plain .md name and content. The default is
// exclusive-create inside the engine-selected docs root (creating the
// skeleton for a project that has none); replace=true overwrites an
// existing file, set only after the user confirmed the collision.
// The request shape stays backward compatible: an absent replace is false.
func (h *Handler) UploadPRD(w http.ResponseWriter, r *http.Request) {
	path, ok := h.projectPath(w, r)
	if !ok {
		return
	}
	var body struct {
		Name    string `json:"name"`
		Content string `json:"content"`
		Replace bool   `json:"replace"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<21)).Decode(&body); err != nil {
		h.err(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	if err := WritePRDFileWithEngine(r.Context(), h.executable, path, body.Name, body.Content, body.Replace); err != nil {
		switch {
		case errors.Is(err, ErrUnsafeTree):
			h.detectErr(w, err)
		case errors.Is(err, ErrPRDExists):
			h.err(w, http.StatusConflict, "prd_exists", err.Error())
		case errors.Is(err, ErrInvalidPRD):
			h.err(w, http.StatusBadRequest, "invalid_prd", err.Error())
		case errors.Is(err, ErrTaskStateBlocked), errors.Is(err, ErrTaskStateReadOnly):
			h.err(w, http.StatusConflict, "task_state_blocked", err.Error())
		default:
			h.err(w, http.StatusInternalServerError, "internal", "could not write the PRD")
		}
		return
	}
	h.json(w, http.StatusCreated, map[string]string{"name": body.Name})
}

// Events streams a content-free notification when the engine-owned task state
// changes, advancing through the engine's bounded live feed. An uninitialized
// family has no task state at all: the distinct 404 the panel hides on.
func (h *Handler) Events(w http.ResponseWriter, r *http.Request) {
	path, ok := h.projectPath(w, r)
	if !ok {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		h.err(w, http.StatusInternalServerError, "internal", "streaming unsupported")
		return
	}
	root, err := openEngineTaskRoot(r.Context(), h.executable, path, nil)
	if err != nil {
		h.detectErr(w, err)
		return
	}
	defer func() { _ = root.Close() }()
	if _, err := root.detectProject(); err != nil {
		h.detectErr(w, err)
		return
	}
	initial, err := readLiveFeed(r.Context(), h.executable, root, 0)
	if err != nil {
		h.detectErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	h.streamLiveEvents(w, r, flusher, root, initial)
}

func (h *Handler) streamLiveEvents(w http.ResponseWriter, r *http.Request, flusher http.Flusher, root *taskRoot, initial liveFeedResult) {
	cursor := liveFeedCursor(initial, 0)
	write := func(sequence int64, resync bool) bool {
		if _, err := fmt.Fprintf(w, "event: tasks-changed\ndata: {\"sequence\":%d,\"resync\":%t}\n\n", sequence, resync); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	if _, err := fmt.Fprint(w, ": connected\n\n"); err != nil {
		return
	}
	flusher.Flush()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			feed, err := readLiveFeed(r.Context(), h.executable, root, cursor)
			if err != nil {
				return
			}
			if feed.Truncated {
				feed, err = readLiveFeed(r.Context(), h.executable, root, 0)
				if err != nil {
					return
				}
				cursor = liveFeedCursor(feed, cursor)
				if !write(cursor, true) {
					return
				}
				continue
			}
			if next := liveFeedCursor(feed, cursor); next > cursor {
				cursor = next
				if !write(cursor, false) {
					return
				}
			}
		}
	}
}

func (h *Handler) projectPath(w http.ResponseWriter, r *http.Request) (string, bool) {
	p, err := h.projects.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, project.ErrNotFound) {
			h.err(w, http.StatusNotFound, "not_found", "project not found")
			return "", false
		}
		h.log.Error().Err(err).Msg("project read failed")
		h.err(w, http.StatusInternalServerError, "internal", "could not read project")
		return "", false
	}
	return p.CanonicalPath, true
}

func (h *Handler) json(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (h *Handler) err(w http.ResponseWriter, status int, code, message string) {
	h.json(w, status, map[string]string{"error": code, "message": message})
}
