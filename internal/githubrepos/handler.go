// HTTP surface for repository listing. Read-only GETs behind the shared
// authentication middleware; no CSRF surface because nothing mutates.
package githubrepos

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/noisefloor-oss/deck/internal/auth"
)

// Handler serves the repository list endpoints over the service.
type Handler struct {
	service *Service
}

// NewHandler builds the handler.
func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

// Mount registers the endpoints behind the same authentication middleware
// as every other control API.
func (h *Handler) Mount(handle func(string, http.Handler), wrap func(http.Handler) http.Handler) {
	handle("GET /api/github/repositories", wrap(http.HandlerFunc(h.list)))
	handle("GET /api/github/repositories/{id}", wrap(http.HandlerFunc(h.repository)))
	handle("GET /api/github/account", wrap(http.HandlerFunc(h.account)))
}

// list serves GET /api/github/repositories?account=X&refresh=1. Every
// structured outcome is HTTP 200; only missing authentication and a missing
// account parameter are request errors.
func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if auth.Principal(r.Context()) == "" {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	account := r.URL.Query().Get("account")
	if account == "" {
		http.Error(w, "account required", http.StatusBadRequest)
		return
	}
	refresh := r.URL.Query().Get("refresh") == "1"
	writeJSON(w, http.StatusOK, h.service.List(r.Context(), account, refresh))
}

// repository serves GET /api/github/repositories/{id}?account=X.
func (h *Handler) repository(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if auth.Principal(r.Context()) == "" {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid repository id", http.StatusBadRequest)
		return
	}
	account := r.URL.Query().Get("account")
	if account == "" {
		http.Error(w, "account required", http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, h.service.Repository(r.Context(), account, id))
}

// account serves GET /api/github/account: the active account's name and
// noreply commit address, for prefilling a Git identity.
func (h *Handler) account(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if auth.Principal(r.Context()) == "" {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	writeJSON(w, http.StatusOK, h.service.Account(r.Context()))
}

func writeJSON(w http.ResponseWriter, code int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(value)
}
