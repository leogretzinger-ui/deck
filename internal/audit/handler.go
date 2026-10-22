package audit

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/rs/zerolog"
)

// Handler exposes the audit trail over HTTP: one filtered, ordered read.
// The trail belongs to the user — auditable actions exist to be inspected;
// it carries identifiers and transitions only, so serving it back over
// the authenticated API leaks nothing it must not.
type Handler struct {
	repo *Repository
	log  zerolog.Logger
}

// NewHandler wires the endpoint.
func NewHandler(repo *Repository, log zerolog.Logger) *Handler {
	return &Handler{repo: repo, log: log}
}

// Mount registers the route. handle and wrap are the server's registrar
// and the auth middleware, exactly as the other handlers take them.
func (h *Handler) Mount(handle func(pattern string, hd http.Handler), wrap func(http.Handler) http.Handler) {
	handle("GET /api/audit/events", wrap(http.HandlerFunc(h.Events)))
}

// Events lists the trail: ?session_id=&type=&limit=. The response is
// oldest-first within the page (the trail's own order).
func (h *Handler) Events(w http.ResponseWriter, r *http.Request) {
	q := Query{
		SessionID: r.URL.Query().Get("session_id"),
		Type:      r.URL.Query().Get("type"),
	}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 || n > 1000 {
			h.err(w, http.StatusBadRequest, "bad_request", "limit must be 0..1000")
			return
		}
		q.Limit = n
	}
	events, err := h.repo.List(r.Context(), q)
	if err != nil {
		h.log.Error().Err(err).Msg("audit list failed")
		h.err(w, http.StatusInternalServerError, "internal", "could not read audit events")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(events)
}

func (h *Handler) err(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "message": message})
}
