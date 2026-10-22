package terminal

import "net/http"

// GoneHandler serves 410 Gone for the retired composer/outbox API (PRD
// §7.4, §5): submit, list, release, cancel, resolve, and the user's
// input-line clear all belonged to a durable input surface that no longer
// exists. The routes stay mounted so a stale caller gets a definitive
// answer instead of a misleading 404, a 405, or an accidental fall-through
// to a different handler.
type GoneHandler struct{}

// NewGoneHandler builds the retired-route handler. The caller wraps it in
// the auth middleware, exactly as every other API route.
func NewGoneHandler() *GoneHandler { return &GoneHandler{} }

// Mount registers the retired routes. handle and wrap are the server's
// registrar and auth wrapper, matching the other handlers' Mount signature.
func (h *GoneHandler) Mount(handle func(pattern string, hd http.Handler), wrap func(http.Handler) http.Handler) {
	gone := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "the composer/message API was retired with direct terminal input (PRD §7.4); input now flows only through the terminal WebSocket", http.StatusGone)
	})
	for _, pattern := range []string{
		"POST /api/sessions/{id}/messages",
		"GET /api/sessions/{id}/messages",
		"POST /api/sessions/{id}/messages/{msgID}/release",
		"POST /api/sessions/{id}/messages/{msgID}/cancel",
		"POST /api/sessions/{id}/messages/{msgID}/resolve",
		"POST /api/sessions/{id}/input/clear",
	} {
		handle(pattern, wrap(gone))
	}
}
