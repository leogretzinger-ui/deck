package settings

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/rs/zerolog"

	"github.com/noisefloor-oss/deck/internal/auth"
)

// Handler exposes the console settings over HTTP.
type Handler struct {
	repo *Repository
	log  zerolog.Logger
}

func NewHandler(repo *Repository, log zerolog.Logger) *Handler {
	return &Handler{repo: repo, log: log}
}

func (h *Handler) Mount(handle func(pattern string, hd http.Handler), wrap func(http.Handler) http.Handler) {
	handle("GET /api/settings", wrap(http.HandlerFunc(h.Get)))
	handle("PUT /api/settings", wrap(http.HandlerFunc(h.Put)))
	handle("GET /api/settings/layout", wrap(http.HandlerFunc(h.GetLayout)))
	handle("PUT /api/settings/layout", wrap(http.HandlerFunc(h.PutLayout)))
	handle("GET /api/settings/tutorial", wrap(http.HandlerFunc(h.GetTutorial)))
	handle("PUT /api/settings/tutorial", wrap(http.HandlerFunc(h.PutTutorial)))
	handle("GET /api/settings/last-session", wrap(http.HandlerFunc(h.GetLastSession)))
	handle("PUT /api/settings/last-session", wrap(http.HandlerFunc(h.PutLastSession)))
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	s, err := h.repo.Get(r.Context())
	if err != nil {
		h.log.Error().Err(err).Msg("settings read failed")
		h.err(w, http.StatusInternalServerError, "internal", "could not read settings")
		return
	}
	h.json(w, http.StatusOK, s)
}

// Put replaces the whole document. A full replace rather than a patch:
// the document is tiny, and a patch API would let a stale client leave
// a setting it never knew about in a state nobody chose.
func (h *Handler) Put(w http.ResponseWriter, r *http.Request) {
	var next Settings
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&next); err != nil {
		h.err(w, http.StatusBadRequest, "bad_request", "body must be a settings object")
		return
	}
	if err := h.repo.Set(r.Context(), next); err != nil {
		h.log.Error().Err(err).Msg("settings write failed")
		h.err(w, http.StatusInternalServerError, "internal", "could not save settings")
		return
	}
	h.json(w, http.StatusOK, next)
}

// GetLayout returns the user's sidebar arrangement. A missing document
// responds 404 so the UI can apply its default expansion; a written document
// — including the all-collapsed empty state — is returned as-is.
func (h *Handler) GetLayout(w http.ResponseWriter, r *http.Request) {
	l, found, err := h.repo.GetLayout(r.Context())
	if err != nil {
		h.log.Error().Err(err).Msg("layout read failed")
		h.err(w, http.StatusInternalServerError, "internal", "could not read layout")
		return
	}
	if !found {
		h.err(w, http.StatusNotFound, "not_found", "no layout saved")
		return
	}
	h.json(w, http.StatusOK, l)
}

// PutLayout replaces the whole layout document. A full replace is right here
// because the layout is one arrangement: a partial update would leave project
// and session order inconsistent.
func (h *Handler) PutLayout(w http.ResponseWriter, r *http.Request) {
	var next Layout
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536)).Decode(&next); err != nil {
		h.err(w, http.StatusBadRequest, "bad_request", "body must be a layout object")
		return
	}
	if err := h.repo.SetLayout(r.Context(), next); err != nil {
		h.log.Error().Err(err).Msg("layout write failed")
		h.err(w, http.StatusInternalServerError, "internal", "could not save layout")
		return
	}
	h.json(w, http.StatusOK, next)
}

// GetTutorial returns the authenticated user's tutorial presentation
// record. A missing record responds 404 so the UI can apply its first-use
// behavior. Reads retain the stored version so clients can recognize an
// unsupported record. The scope field says whose record this is: the user's,
// or — on an installation with no authentication, which is single-tenant —
// the one installation-wide record.
func (h *Handler) GetTutorial(w http.ResponseWriter, r *http.Request) {
	principal := auth.Principal(r.Context())
	p, found, err := h.repo.GetTutorial(r.Context(), principal)
	if err != nil {
		h.log.Error().Err(err).Msg("tutorial preference read failed")
		h.err(w, http.StatusInternalServerError, "internal", "could not read tutorial preference")
		return
	}
	if !found {
		h.err(w, http.StatusNotFound, "not_found", "no tutorial preference saved")
		return
	}
	h.json(w, http.StatusOK, tutorialResponse{TutorialPreference: p, Scope: tutorialScope(principal)})
}

// PutTutorial replaces the user's tutorial record. A full replace like
// the other small documents: the record is tiny and every writer knows the
// whole position it means to store. The write is refused — never silently
// resolved — when the incoming document or the stored one speaks a contract
// version this build does not.
func (h *Handler) PutTutorial(w http.ResponseWriter, r *http.Request) {
	principal := auth.Principal(r.Context())
	var next TutorialPreference
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&next); err != nil {
		h.err(w, http.StatusBadRequest, "bad_request", "body must be a tutorial preference object")
		return
	}
	next.normalize()
	if err := next.validate(); err != nil {
		h.err(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if err := h.repo.SetTutorial(r.Context(), principal, next); err != nil {
		if errors.Is(err, ErrTutorialVersionConflict) {
			h.err(w, http.StatusConflict, "version_conflict", "the stored tutorial preference belongs to a contract version this build does not speak; refusing to overwrite it")
			return
		}
		h.log.Error().Err(err).Msg("tutorial preference write failed")
		h.err(w, http.StatusInternalServerError, "internal", "could not save tutorial preference")
		return
	}
	h.json(w, http.StatusOK, tutorialResponse{TutorialPreference: next, Scope: tutorialScope(principal)})
}

// lastSessionDocument is the GET/PUT /api/settings/last-session body: the
// id of the session the user last opened, so a browser on another
// device opens the same one. An empty id on PUT forgets it.
type lastSessionDocument struct {
	SessionID string `json:"session_id"`
}

// GetLastSession returns the remembered session id, or 404 when nothing is
// remembered so the console falls back to its own browser's memory.
func (h *Handler) GetLastSession(w http.ResponseWriter, r *http.Request) {
	principal := auth.Principal(r.Context())
	id, found, err := h.repo.GetLastSession(r.Context(), principal)
	if err != nil {
		h.log.Error().Err(err).Msg("last session read failed")
		h.err(w, http.StatusInternalServerError, "internal", "could not read last session")
		return
	}
	if !found {
		h.err(w, http.StatusNotFound, "not_found", "no last session remembered")
		return
	}
	h.json(w, http.StatusOK, lastSessionDocument{SessionID: id})
}

// PutLastSession remembers the id the console just opened, or forgets it
// when the body carries an empty id. The id is stored, not checked: a
// session can end after the write, and the console validates the id
// against the live list before acting on it.
func (h *Handler) PutLastSession(w http.ResponseWriter, r *http.Request) {
	principal := auth.Principal(r.Context())
	var next lastSessionDocument
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&next); err != nil {
		h.err(w, http.StatusBadRequest, "bad_request", "body must be a last-session object")
		return
	}
	if err := h.repo.SetLastSession(r.Context(), principal, next.SessionID); err != nil {
		if errors.Is(err, errSessionIDTooLong) {
			h.err(w, http.StatusBadRequest, "bad_request", "session id is too long")
			return
		}
		h.log.Error().Err(err).Msg("last session write failed")
		h.err(w, http.StatusInternalServerError, "internal", "could not save last session")
		return
	}
	h.json(w, http.StatusOK, next)
}

// tutorialResponse is the stored document plus the scope it was read or
// written under. The scope is server-derived; a client-supplied value is
// ignored on write.
type tutorialResponse struct {
	TutorialPreference
	Scope string `json:"scope"`
}

func (h *Handler) json(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (h *Handler) err(w http.ResponseWriter, status int, code, message string) {
	h.json(w, status, map[string]string{"error": code, "message": message})
}
