package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
)

// Config parameterizes the server. Values come from the validated
// application config (internal/config); this package does not re-validate
// what config already refused fail-closed (the loopback rule lives there,
// before a socket ever opens).
type Config struct {
	// Bind is the listen address, already validated as an IP-literal
	// host:port.
	Bind string
	// UnixSocket selects a private local listener instead of TCP. Every
	// connection must have GatewayUID as its kernel-observed peer identity.
	UnixSocket string
	GatewayUID int
	// AllowedOrigins are the exact origins permitted to make state-changing
	// browser requests and (later) open WebSockets. Strict comparison, no
	// wildcards.
	AllowedOrigins []string
	// ProtectBrowserAPI refuses cross-origin browser API reads as well as writes.
	ProtectBrowserAPI bool
}

// Check is a named deep-readiness probe. Checks run concurrently
// under one request context and must be independent. The startup sequence
// registers the real ones: lock held, database writable, tmux socket
// verified, reconciliation completed.
type Check struct {
	Name  string
	Probe func(context.Context) error
}

// Server is the HTTP foundation: health endpoints, middleware, and a mount
// surface for the API and WebSocket layers that later tasks register.
type Server struct {
	cfg    Config
	log    zerolog.Logger
	mux    *http.ServeMux
	http   *http.Server
	ready  atomic.Bool
	checks []Check
	// bound holds the listener's actual address once Run has bound it —
	// distinct from cfg.Bind when the port is kernel-chosen (":0" in tests).
	bound atomic.Value
	// draining is cancelled when the drain starts. Only responses that
	// have committed to an event stream follow it (see logRequests).
	draining   context.Context
	startDrain context.CancelFunc
}

// New builds the server. It binds nothing; call Run.
func New(cfg Config, log zerolog.Logger, checks []Check) *Server {
	s := &Server{cfg: cfg, log: log, mux: http.NewServeMux(), checks: checks}
	s.mux.HandleFunc("GET /livez", s.handleLivez)
	s.mux.HandleFunc("GET /readyz", s.handleReadyz)

	// Order, outermost first: every response — including origin rejections
	// and recovered panics — must carry a request id and produce a log line,
	// so requestID and logRequests wrap everything; recover sits inside
	// logRequests so its 500 is the status the log records; the origin guard
	// runs last, protecting only routed handlers.
	handler := s.requestID(s.logRequests(s.recover(s.browserAPIGuard(s.originGuard(s.mux)))))
	s.draining, s.startDrain = context.WithCancel(context.Background())
	s.http = &http.Server{
		Addr:    cfg.Bind,
		Handler: handler,
		// A stuck client must not hold connections forever; the WebSocket
		// layer manages its own long-lived connections and will
		// hijack out of these deadlines.
		ReadHeaderTimeout: 10 * time.Second,
	}
	return s
}

// Handle mounts a handler on the shared mux. Later layers (API, WebSocket,
// static assets) register through this so every route passes the same
// middleware chain.
func (s *Server) Handle(pattern string, h http.Handler) {
	s.mux.Handle(pattern, h)
}

// SetReady flips the readiness gate. Only the startup sequence may set it
// true, and only after its preconditions hold: lock, storage, tmux
// inventory, reconciliation.
func (s *Server) SetReady(v bool) { s.ready.Store(v) }

// Run listens and serves until ctx is canceled, then shuts down gracefully
// with a bounded drain. It returns the first serve or shutdown error;
// context cancellation itself is a clean exit.
func (s *Server) Run(ctx context.Context) error {
	ln, err := s.listen()
	if err != nil {
		return fmt.Errorf("server: listen %s: %w", s.cfg.Bind, err)
	}
	s.bound.Store(ln.Addr().String())
	s.log.Info().Str("addr", ln.Addr().String()).Msg("http listening")

	errCh := make(chan error, 1)
	go func() { errCh <- s.http.Serve(ln) }()

	select {
	case err := <-errCh:
		return fmt.Errorf("server: serve: %w", err)
	case <-ctx.Done():
	}

	// The provider CLIs must survive this shutdown untouched: only
	// HTTP state drains here, nothing signals a session.
	s.ready.Store(false)
	s.startDrain()
	shCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := s.http.Shutdown(shCtx); err != nil {
		return fmt.Errorf("server: shutdown: %w", err)
	}
	if err := <-errCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("server: serve: %w", err)
	}
	return nil
}

func (s *Server) listen() (net.Listener, error) {
	if s.cfg.UnixSocket != "" {
		if s.cfg.Bind != "" {
			return nil, errors.New("server: private socket cannot also bind TCP")
		}
		return listenPrivate(s.cfg.UnixSocket, s.cfg.GatewayUID)
	}
	return net.Listen("tcp", s.cfg.Bind)
}

// BoundAddr reports the address Run actually bound, "" before listening.
func (s *Server) BoundAddr() string {
	v, _ := s.bound.Load().(string)
	return v
}
