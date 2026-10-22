package lane

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/rs/zerolog"

	"github.com/noisefloor-oss/deck/internal/project"
)

// Handler exposes the lane lifecycle over HTTP behind the same
// authentication and CSRF middleware as the rest of the control API.
type Handler struct {
	svc *Service
	log zerolog.Logger
}

func NewHandler(svc *Service, log zerolog.Logger) *Handler { return &Handler{svc: svc, log: log} }

func (h *Handler) Mount(handle func(pattern string, hd http.Handler), wrap func(http.Handler) http.Handler) {
	handle("GET /api/projects/{id}/lanes", wrap(http.HandlerFunc(h.List)))
	handle("POST /api/projects/{id}/lanes", wrap(http.HandlerFunc(h.Start)))
	handle("GET /api/projects/{id}/lanes/{tag}/{task}/review", wrap(http.HandlerFunc(h.Review)))
	handle("POST /api/projects/{id}/lanes/{tag}/{task}/review", wrap(http.HandlerFunc(h.RequestReview)))
	handle("POST /api/projects/{id}/taskmaster/tasks/{task}/done", wrap(http.HandlerFunc(h.Done)))
}

func decode(w http.ResponseWriter, r *http.Request, into any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return false
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return false
	}
	return true
}

func taskID(w http.ResponseWriter, raw string) (int64, bool) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "lanes work on top-level tasks with a numeric id")
		return 0, false
	}
	return id, true
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	views, err := h.svc.List(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"lanes": views})
}

func (h *Handler) Start(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Tag   string `json:"tag"`
		Task  int64  `json:"task"`
		Agent string `json:"agent"`
	}
	if !decode(w, r, &body) {
		return
	}
	view, err := h.svc.Start(r.Context(), r.PathValue("id"), StartInput{Tag: body.Tag, TaskID: body.Task, Agent: body.Agent})
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, view)
}

func (h *Handler) Done(w http.ResponseWriter, r *http.Request) {
	id, ok := taskID(w, r.PathValue("task"))
	if !ok {
		return
	}
	var body struct {
		Tag          string `json:"tag"`
		Revision     *int64 `json:"revision"`
		StopSessions bool   `json:"stop_sessions"`
	}
	if !decode(w, r, &body) {
		return
	}
	result, err := h.svc.Done(r.Context(), r.PathValue("id"), DoneInput{
		Tag: body.Tag, TaskID: id, Revision: body.Revision, StopSessions: body.StopSessions,
	})
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) RequestReview(w http.ResponseWriter, r *http.Request) {
	id, ok := taskID(w, r.PathValue("task"))
	if !ok {
		return
	}
	var body struct {
		Agent string `json:"agent"`
	}
	if !decode(w, r, &body) {
		return
	}
	view, err := h.svc.RequestReview(r.Context(), r.PathValue("id"), r.PathValue("tag"), id, body.Agent)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (h *Handler) Review(w http.ResponseWriter, r *http.Request) {
	id, ok := taskID(w, r.PathValue("task"))
	if !ok {
		return
	}
	state, err := h.svc.Review(r.Context(), r.PathValue("id"), r.PathValue("tag"), id)
	if err != nil {
		h.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, state)
}

// fail maps a service error to a status and a stable code. Engine text in a
// step error has already been scrubbed of token values.
func (h *Handler) fail(w http.ResponseWriter, err error) {
	var step *StepError
	var risk *WorkAtRiskError
	var live *SessionsLiveError
	switch {
	case errors.Is(err, project.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "project not found")
	case errors.Is(err, ErrNoLane):
		writeError(w, http.StatusNotFound, "no_lane", "no lane is open for this task")
	case errors.Is(err, ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid_lane", err.Error())
	case errors.Is(err, ErrClaimed):
		writeError(w, http.StatusConflict, "task_claimed", "Someone else has claimed this task, so Deck did not start a lane for it.")
	case errors.Is(err, ErrNotOpen):
		writeError(w, http.StatusConflict, "task_not_open", err.Error())
	case errors.Is(err, ErrStale):
		writeError(w, http.StatusConflict, "stale_task", "The task changed since Deck last read it. Nothing was changed; try again.")
	case errors.Is(err, ErrNotReady):
		writeError(w, http.StatusConflict, "lane_not_ready", "The lane is not ready. It is still being set up, is closing, or failed to start.")
	case errors.Is(err, ErrSameAgent):
		writeError(w, http.StatusConflict, "same_agent", "A review needs a different agent than the one that wrote the change.")
	case errors.As(err, &risk):
		code := "lane_unpushed"
		if risk.Dirty > 0 {
			code = "lane_dirty"
		}
		writeError(w, http.StatusConflict, code, "Nothing was changed. "+risk.Error()+".")
	case errors.As(err, &live):
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "lane_sessions_live", "message": live.Error(), "sessions": live.Labels,
		})
	case errors.As(err, &step):
		h.log.Warn().Str("step", step.Step).Msg("lane step failed")
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error": "lane_step_failed", "step": step.Step, "message": step.Error(),
		})
	default:
		h.log.Error().Err(err).Msg("lane request failed")
		writeError(w, http.StatusInternalServerError, "internal", "the lane request failed")
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"error": code, "message": message})
}
