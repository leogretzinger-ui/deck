package tasks

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/noisefloor-oss/deck/internal/project"
)

// Generate starts one task-generation job. 202 Accepted: the run record
// is the product — serialization conflicts, ineligible PRDs, and launch
// outcomes are all reported through it or through honest error codes.
func (h *Handler) Generate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PRD      string `json:"prd"`
		NumTasks *int   `json:"numTasks"`
		Harness  string `json:"harness"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		h.err(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	numTasks := 0
	if body.NumTasks != nil {
		numTasks = *body.NumTasks
		if numTasks < 1 || numTasks > maxGenerateTasks {
			h.err(w, http.StatusBadRequest, "invalid_num_tasks", ErrInvalidTaskCount.Error())
			return
		}
	}
	run, err := h.runner.Generate(r.Context(), r.PathValue("id"), body.PRD, numTasks, body.Harness)
	if err != nil {
		h.generateErr(w, err)
		return
	}
	h.json(w, http.StatusAccepted, run)
}

// generateErr maps a generation failure. A launch that failed AFTER the
// row was committed has already been finalized honestly; the 500 tells the
// user to look at the run history.
func (h *Handler) generateErr(w http.ResponseWriter, err error) {
	var unsupported *unsupportedGenerationHarness
	switch {
	case errors.As(err, &unsupported):
		h.err(w, http.StatusBadRequest, "generation_harness_unsupported", err.Error())
	case errors.Is(err, ErrGenerationUnsupported):
		h.err(w, http.StatusNotImplemented, "generation_unsupported", err.Error())
	case errors.Is(err, project.ErrNotFound):
		h.err(w, http.StatusNotFound, "not_found", "project not found")
	case errors.Is(err, ErrUnsafeTree), errors.Is(err, ErrNotTaskmasterProject), errors.Is(err, ErrTaskStateBlocked):
		h.detectErr(w, err)
	case errors.Is(err, ErrTaskStateReadOnly):
		h.err(w, http.StatusConflict, "task_state_blocked", err.Error())
	case errors.Is(err, ErrInvalidPRD):
		h.err(w, http.StatusBadRequest, "invalid_prd",
			"not an eligible PRD file; list them from the prds endpoint")
	case errors.Is(err, ErrInvalidTaskCount):
		h.err(w, http.StatusBadRequest, "invalid_num_tasks", err.Error())
	case errors.Is(err, ErrGenerationRunning):
		h.err(w, http.StatusConflict, "generation_running",
			"a generation is already running for this project")
	default:
		h.log.Error().Err(err).Msg("taskmaster generate failed")
		h.err(w, http.StatusInternalServerError, "internal", "could not start generation")
	}
}

// Runs lists a project's generation history, newest first, without the
// captured output bodies (the detail endpoint carries those).
func (h *Handler) Runs(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.projectPath(w, r); !ok {
		return
	}
	runs, err := h.runner.runs.ListRuns(r.Context(), r.PathValue("id"), 20)
	if err != nil {
		h.log.Error().Err(err).Msg("taskmaster runs list failed")
		h.err(w, http.StatusInternalServerError, "internal", "could not list runs")
		return
	}
	for _, run := range runs {
		run.Stdout, run.Stderr = nil, nil
	}
	h.json(w, http.StatusOK, runs)
}

// RunDetail returns one run with its captured output. The run must belong
// to the addressed project — no cross-project reads.
func (h *Handler) RunDetail(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.projectPath(w, r); !ok {
		return
	}
	run, err := h.runner.runs.GetRun(r.Context(), r.PathValue("run"))
	if err != nil {
		if errors.Is(err, ErrRunNotFound) {
			h.err(w, http.StatusNotFound, "not_found", "run not found")
			return
		}
		h.log.Error().Err(err).Msg("taskmaster run read failed")
		h.err(w, http.StatusInternalServerError, "internal", "could not read run")
		return
	}
	if run.ProjectID != r.PathValue("id") {
		h.err(w, http.StatusNotFound, "not_found", "run not found")
		return
	}
	h.json(w, http.StatusOK, run)
}
