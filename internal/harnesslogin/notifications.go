package harnesslogin

import (
	"io"
	"net/http"
	"time"
)

// Notifications only invalidate the browser's pending list. Native login
// addresses and callback authority stay behind the existing request handlers.
func (s *Service) subscribe(session string) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	s.mu.Lock()
	if s.subscribers == nil {
		s.subscribers = make(map[string]map[chan struct{}]struct{})
	}
	if s.subscribers[session] == nil {
		s.subscribers[session] = make(map[chan struct{}]struct{})
	}
	s.subscribers[session][ch] = struct{}{}
	s.mu.Unlock()
	return ch, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.subscribers[session], ch)
		if len(s.subscribers[session]) == 0 {
			delete(s.subscribers, session)
		}
	}
}

func (s *Service) notifyLocked(session string) {
	for ch := range s.subscribers[session] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (h *Handler) watch(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	originAllowed := origin == ""
	for _, allowed := range h.Origins {
		if allowed != "" && origin == allowed {
			originAllowed = true
		}
	}
	if !originAllowed || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		http.Error(w, "Harness login request refused.", http.StatusForbidden)
		return
	}
	session := r.URL.Query().Get("session")
	if session == "" {
		http.Error(w, "A session is required.", http.StatusBadRequest)
		return
	}
	if h.Service == nil {
		http.Error(w, "Harness login is unavailable.", http.StatusServiceUnavailable)
		return
	}
	if _, ok := w.(http.Flusher); !ok {
		http.Error(w, "Harness login notifications are unavailable.", http.StatusInternalServerError)
		return
	}
	changes, unsubscribe := h.Service.subscribe(session)
	defer unsubscribe()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	controller := http.NewResponseController(w)
	send := func(message string) bool {
		_, err := io.WriteString(w, message)
		return err == nil && controller.Flush() == nil
	}
	const pending = "event: pending\ndata: {}\n\n"
	// Subscribe first, then refresh on every connection so neither an earlier
	// registration nor a registration racing the initial listing is missed.
	if !send(pending) {
		return
	}
	keepalive := time.NewTicker(25 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-changes:
			if !send(pending) {
				return
			}
		case <-keepalive.C:
			if !send(": keepalive\n\n") {
				return
			}
		}
	}
}
