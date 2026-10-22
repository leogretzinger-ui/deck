package server

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"time"
)

type ctxKey int

const requestIDKey ctxKey = 0

// RequestIDHeader carries the per-request id back to the client and into
// every log line, so a browser-reported failure can be joined to its server
// records.
const RequestIDHeader = "X-Request-Id"

// RequestID returns the id assigned to this request, "" outside the chain.
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// requestID assigns a random id to every request. Inbound values are
// deliberately ignored: an attacker-chosen id could poison log joins.
func (s *Server) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b [8]byte
		if _, err := rand.Read(b[:]); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		id := hex.EncodeToString(b[:])
		w.Header().Set(RequestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey, id)))
	})
}

// recover converts a handler panic into a 500 without killing the process,
// and logs the stack with the request id. The response carries no panic
// detail.
func (s *Server) recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				s.log.Error().
					Str("request_id", RequestID(r.Context())).
					Interface("panic", v).
					Bytes("stack", debug.Stack()).
					Msg("handler panic")
				http.Error(w, "internal error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// logRequests emits one structured line per request: identifiers and
// outcomes only — never bodies, never query strings, which is where tokens
// end up.
//
// It also owns how an event stream ends on shutdown. A stream returns only
// when its request context is cancelled, and Shutdown waits for it, so a
// response that flushes as text/event-stream has its context cancelled
// when the drain starts. Every other request keeps its context and gets
// the drain budget to finish: cancelling a session create mid-spawn would
// roll back a live provider process.
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		stop := func() bool { return false }
		defer func() { stop() }()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		sw.onStream = func() {
			if s.draining != nil {
				stop = context.AfterFunc(s.draining, cancel)
			}
		}
		next.ServeHTTP(sw, r.WithContext(ctx))
		s.log.Info().
			Str("request_id", RequestID(r.Context())).
			Str("method", r.Method).
			Str("path", r.URL.Path).
			Int("status", sw.status).
			Dur("duration", time.Since(start)).
			Msg("http")
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
	// onStream runs once, on the first Flush of a text/event-stream
	// response.
	onStream  func()
	streaming bool
}

func (w *statusWriter) WriteHeader(code int) {
	// Record only the first FINAL status. Informational 1xx responses (103
	// Early Hints) are forwarded but never recorded: net/http sends the
	// later final status to the client, and the log must match it.
	if !w.wrote && code >= 200 {
		w.status = code
		w.wrote = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	// A body write without a prior WriteHeader commits an implicit 200 on
	// the wire; net/http ignores any WriteHeader after it. Record that
	// commitment so a late WriteHeader(500) cannot make the log disagree
	// with what the client received.
	if !w.wrote {
		w.status = http.StatusOK
		w.wrote = true
	}
	return w.ResponseWriter.Write(p)
}

// Unwrap exposes the underlying writer for http.ResponseController users.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Flush satisfies the DIRECT http.Flusher assertion the event-stream
// handlers (files, assets, tasks, session) perform before they commit to
// streaming; without it every live feed answers "streaming unsupported"
// through this chain. A body flush commits the status on the wire, so it
// is recorded the same way a body write is.
func (w *statusWriter) Flush() {
	if !w.wrote {
		w.status = http.StatusOK
		w.wrote = true
	}
	if !w.streaming && w.onStream != nil && strings.HasPrefix(w.Header().Get("Content-Type"), "text/event-stream") {
		w.streaming = true
		w.onStream()
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

// Hijack satisfies the DIRECT http.Hijacker assertion the pinned
// gorilla/websocket upgrader performs — Unwrap alone only serves
// ResponseController callers, and the WebSocket layer is the
// whole reason this wrapper must not swallow the capability.
func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("server: underlying ResponseWriter does not support hijacking")
	}
	conn, buf, err := h.Hijack()
	if err == nil {
		// Everything after a hijack bypasses WriteHeader, so record the
		// outcome here: a hijacked request IS a protocol switch, and logging
		// the default 200 for it would misreport what happened on the wire.
		w.status = http.StatusSwitchingProtocols
		w.wrote = true
	}
	return conn, buf, err
}

// originGuard refuses state-changing browser requests from origins outside
// the allowlist. Strict string comparison, no wildcards. Safe
// methods and requests without an Origin header (curl, same-origin
// old-browser GETs) pass — this is the transport-level baseline; cookie and
// CSRF-token enforcement land with the auth layer, which tightens
// on top of this, never instead of it.
func (s *Server) originGuard(next http.Handler) http.Handler {
	allowed := make(map[string]bool, len(s.cfg.AllowedOrigins))
	for _, o := range s.cfg.AllowedOrigins {
		allowed[o] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if o := r.Header.Get("Origin"); o != "" && !allowed[o] {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
