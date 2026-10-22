package terminal

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"

	"github.com/noisefloor-oss/deck/internal/inputgate"
	"github.com/noisefloor-oss/deck/internal/lease"
	"github.com/noisefloor-oss/deck/internal/session"
	"github.com/noisefloor-oss/deck/internal/tmux"
)

// Tunables. outputQueue bounds how far a browser may fall behind before it
// is dropped: big enough to ride out a redraw burst, small enough that a
// wedged client is noticed in well under a second of provider output.
const (
	outputQueue    = 256
	readLimit      = 64 << 10
	writeTimeout   = 10 * time.Second
	pongTimeout    = 30 * time.Second
	pingInterval   = 10 * time.Second
	readBufferSize = 32 << 10
	// ptyWriteTimeout bounds how long one input frame may occupy the
	// session write gate. Generous for a healthy terminal (a keystroke is
	// bytes), short enough that a wedged pane cannot delay a takeover.
	ptyWriteTimeout = 2 * time.Second
)

// Proxy serves terminal attachments.
type Proxy struct {
	tmux *tmux.Client
	// targets resolves and verifies what an attachment may connect to;
	// fence is the controller-fencing triple it runs under. Neither is
	// session-shaped: the proxy transports bytes and enforces who may
	// send them, and knows a target only through these interfaces.
	targets Targets
	fence   Fence
	log     zerolog.Logger
	origins map[string]bool
	gate    *inputgate.Gate
	// writeWithin is the bounded PTY write. Keeping this boundary explicit
	// lets the failure tests return an exact written prefix: the write
	// result must follow n, not the frame the browser attempted to send.
	writeWithin func(*tmux.Attachment, []byte, time.Duration) (int, error)
	upgrader    websocket.Upgrader

	// writeFault, when non-nil, is a test-only seam at the write boundary:
	// it replaces the real PTY write for one frame. Production never
	// installs one; the acceptance suite uses it to force the uncertain
	// outcome that a healthy terminal cannot produce. The hook receives the
	// attachment so a test can write a genuine prefix to the PTY and then
	// fail — an outcome that cannot prove how many bytes crossed the
	// boundary, unlike a fabricated (0, error).
	writeFaultMu sync.Mutex
	writeFault   func(att *tmux.Attachment, data []byte) (int, error)

	// live tracks attachments this process is serving, so a takeover can
	// close the displaced one IMMEDIATELY. Waiting for the old browser to
	// send another frame would leave two tmux clients attached to one
	// session for as long as it stays idle. At most one browser holds the
	// attachment, not at most one that is currently typing.
	mu   sync.Mutex
	live map[string][]*attachment
}

// attachment is one served connection. The pointer is its process-local
// identity: same-controller reconnects deliberately reuse a fencing token, so
// the token cannot identify per-attachment diagnostic state.
type attachment struct {
	sessionID  string
	controller string
	token      int64
	cancel     context.CancelFunc
	lastWrite  string
	// out is this attachment's writer queue, so a takeover can tell the
	// displaced browser WHY it is being closed before closing it.
	out chan ServerFrame
}

// ConnectionSnapshot is process-memory evidence about the attachment this
// backend is serving now. It contains no input bytes and disappears when
// the attachment disconnects or the service restarts.
type ConnectionSnapshot struct {
	AttachmentCount int
	FencingToken    int64
	LastWriteResult string
}

// fenceGrace is how long a displaced attachment is given to flush its
// "you lost control" frame before it is torn down. Long enough for a live
// socket to deliver one small message, short enough that a wedged one is
// gone promptly.
const fenceGrace = 300 * time.Millisecond

// New builds the proxy over recorded sessions: the session repository
// becomes a Targets adapter and the lease manager is the Fence, so every
// existing caller behaves identically. allowedOrigins is the exact-match
// allowlist a WebSocket handshake must satisfy — the browser's Origin is
// the only cross-site defense a WebSocket has, since it is exempt from
// the same-origin policy.
func New(t *tmux.Client, sessions *session.Repository, leases *lease.Manager,
	gate *inputgate.Gate, allowedOrigins []string, log zerolog.Logger) *Proxy {
	return NewFor(t, newSessionTargets(t, sessions), leases, gate, allowedOrigins, log)
}

// NewFor builds the proxy over any verified-target and fence pair. The
// tmux client stays concrete on purpose: the proxy is a tmux transport,
// and that client is what queries pane state and holds the attachment's
// write side. Targets on a different Deck-owned tmux server construct
// their proxy with that server's client.
func NewFor(t *tmux.Client, targets Targets, fence Fence,
	gate *inputgate.Gate, allowedOrigins []string, log zerolog.Logger) *Proxy {
	p := &Proxy{
		tmux:        t,
		targets:     targets,
		fence:       fence,
		gate:        gate,
		writeWithin: (*tmux.Attachment).WriteWithin,
		log:         log,
		origins:     make(map[string]bool, len(allowedOrigins)),
		live:        make(map[string][]*attachment),
	}
	for _, o := range allowedOrigins {
		p.origins[strings.TrimRight(o, "/")] = true
	}
	p.upgrader = websocket.Upgrader{
		ReadBufferSize:  readBufferSize,
		WriteBufferSize: readBufferSize,
		CheckOrigin:     p.originAllowed,
	}
	return p
}

// SetWriteFaultForTest installs a one-shot fault at the bounded PTY write
// boundary: the next single input or reply write is routed through the hook
// instead of the terminal, and the hook is then cleared. nil disarms. This
// is the fault-injection seam — production never installs a fault, but the
// acceptance suite needs a terminal that returns the uncertain outcome a
// healthy one cannot. The hook takes the attachment so a test can genuinely
// touch the PTY (a partial write) before failing.
func (p *Proxy) SetWriteFaultForTest(fault func(att *tmux.Attachment, data []byte) (int, error)) {
	p.writeFaultMu.Lock()
	defer p.writeFaultMu.Unlock()
	p.writeFault = fault
}

// writeBounded routes a frame through the single serialized PTY write. When a
// test fault is armed it consumes the fault (one-shot) and returns its result
// so the write outcome follows the hook, not the frame the browser sent.
func (p *Proxy) writeBounded(att *tmux.Attachment, data []byte) (int, error) {
	p.writeFaultMu.Lock()
	fault := p.writeFault
	p.writeFault = nil
	p.writeFaultMu.Unlock()
	if fault != nil {
		return fault(att, data)
	}
	return p.writeWithin(att, data, ptyWriteTimeout)
}

// originAllowed enforces the exact-origin allowlist. A handshake with NO
// Origin header is refused here, unlike ordinary HTTP: a browser always
// sends one on a WebSocket handshake, so its absence means a non-browser
// client, and the terminal is a browser attachment.
func (p *Proxy) originAllowed(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return false
	}
	return p.origins[strings.TrimRight(o, "/")]
}

// Handle serves GET /api/sessions/{id}/terminal. The caller must have
// wrapped it in the auth middleware: an unauthenticated request never
// reaches here.
//
// Query parameters: controller (this browser's stable id), cols and rows
// for the already-fitted browser viewport, and takeover=1 for a deliberate
// takeover.
func (p *Proxy) Handle(w http.ResponseWriter, r *http.Request) {
	// ORIGIN FIRST, before anything mutates state. The upgrader also checks
	// it, but that runs inside Upgrade — after the lease acquire below — so
	// a foreign-origin request carrying takeover=1 would fence the
	// legitimate controller and only then be refused the socket. Cross-site
	// requests must not be able to reach the lease at all.
	if !p.originAllowed(r) {
		http.Error(w, "origin not allowed", http.StatusForbidden)
		return
	}

	sessionID := r.PathValue("id")
	controllerID := r.URL.Query().Get("controller")
	if sessionID == "" || controllerID == "" {
		http.Error(w, "session and controller are required", http.StatusBadRequest)
		return
	}
	cols, rows, err := initialTerminalSize(r)
	if err != nil {
		http.Error(w, "valid terminal dimensions are required", http.StatusBadRequest)
		return
	}

	// The target must be recorded running with a terminal to attach to.
	// This is only the cheap HTTP-boundary check, and it runs before any
	// lease is touched; serve revalidates the complete local controller
	// tuple under the session gate before invoking tmux Attach.
	if err := p.targets.Open(r.Context(), sessionID); err != nil {
		if errors.Is(err, ErrTargetNotRunning) {
			http.Error(w, "session is not running", http.StatusConflict)
			return
		}
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}

	// A refused second browser is told IN-BAND, not by HTTP status: the
	// takeover screen is the answer to a held lease, but the browser
	// WebSocket API does not expose the handshake's status, so a plain 409
	// would strand the user with a dead socket and no way to act.
	//
	// The acquire runs UNDER the session write gate. A writer holds that
	// gate across [revalidate lease -> write bytes]; without taking it
	// here, a takeover could commit inside that window, so the displaced
	// controller's revalidation would pass and its bytes would still
	// reach the pane after control had moved. Held only across the
	// acquire and the fencing — never across serve, which would block
	// every other path for the life of the attachment.
	gate := p.gate.Session(sessionID)
	gate.Lock()
	l, err := p.fence.Acquire(r.Context(), sessionID, controllerID,
		r.URL.Query().Get("takeover") == "1")
	if errors.Is(err, lease.ErrHeld) {
		gate.Unlock()
		// Upgrade, say why, close: the protocol's own refusal shape.
		conn, uerr := p.upgrader.Upgrade(w, r, nil)
		if uerr != nil {
			return // the upgrade already wrote its response
		}
		p.sendError(conn, CodeLeaseHeld, "another controller holds this session")
		_ = conn.Close()
		return
	}
	if err != nil {
		gate.Unlock()
		p.log.Warn().Err(err).Str("session", sessionID).Msg("lease acquire failed")
		http.Error(w, "lease unavailable", http.StatusInternalServerError)
		return
	}

	// Close any attachment this acquisition displaced, now rather than
	// whenever the old browser next speaks.
	p.fenceOthers(sessionID, l.ControllerID, l.FencingToken)
	gate.Unlock()

	conn, err := p.upgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade has already written its own response.
		return
	}
	p.serve(r.Context(), conn, sessionID, l, cols, rows)
}

func initialTerminalSize(r *http.Request) (uint16, uint16, error) {
	parse := func(name string) (uint16, error) {
		value, err := strconv.ParseUint(r.URL.Query().Get(name), 10, 16)
		if err != nil || value == 0 {
			return 0, errors.New("invalid terminal dimension")
		}
		return uint16(value), nil
	}
	cols, err := parse("cols")
	if err != nil {
		return 0, 0, err
	}
	rows, err := parse("rows")
	if err != nil {
		return 0, 0, err
	}
	return cols, rows, nil
}

// fenceOthers cancels every attachment for this session that does not hold
// the acquired controller tuple. An expired reclaim keeps the fencing token,
// so the controller id is part of the comparison.
func (p *Proxy) fenceOthers(sessionID, keepController string, keepToken int64) {
	p.mu.Lock()
	var doomed []*attachment
	kept := p.live[sessionID][:0:0]
	for _, a := range p.live[sessionID] {
		if a.controller == keepController && a.token == keepToken {
			kept = append(kept, a)
			continue
		}
		doomed = append(doomed, a)
	}
	p.live[sessionID] = kept
	p.mu.Unlock()

	for _, a := range doomed {
		p.log.Info().Str("session", sessionID).
			Msg("closing attachment displaced by controller acquisition")
		select {
		case a.out <- ServerFrame{Type: TypeError, Code: CodeFenced,
			Message: "another browser has taken control of this session"}:
		default: // queue full: the browser is not keeping up anyway
		}
		// Cancel after a short grace so the notice can leave, but never
		// depend on it: the attachment ends either way.
		time.AfterFunc(fenceGrace, a.cancel)
	}
}

func (p *Proxy) register(a *attachment) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.live[a.sessionID] = append(p.live[a.sessionID], a)
}

func (p *Proxy) deregister(a *attachment) {
	p.mu.Lock()
	defer p.mu.Unlock()
	rest := p.live[a.sessionID][:0]
	for _, x := range p.live[a.sessionID] {
		if x != a {
			rest = append(rest, x)
		}
	}
	if len(rest) == 0 {
		delete(p.live, a.sessionID)
		return
	}
	p.live[a.sessionID] = rest
}

// ConnectionSnapshot reports the current attachment without treating the
// durable lease row as proof that a browser socket is alive. More than one is
// surfaced as a count rather than hidden behind a boolean.
func (p *Proxy) ConnectionSnapshot(sessionID string) ConnectionSnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	attachments := p.live[sessionID]
	snapshot := ConnectionSnapshot{AttachmentCount: len(attachments)}
	if len(attachments) == 0 {
		return snapshot
	}
	current := attachments[len(attachments)-1]
	snapshot.FencingToken = current.token
	snapshot.LastWriteResult = current.lastWrite
	return snapshot
}

func (p *Proxy) recordWriteOutcome(a *attachment, outcome string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	a.lastWrite = outcome
}

// serve runs one attachment until the browser leaves, the client dies, or
// the controller is fenced.
func (p *Proxy) serve(ctx context.Context, conn *websocket.Conn, sessionID string,
	l *lease.Lease, cols, rows uint16) {
	log := p.log.With().Str("session", sessionID).Str("controller", l.ControllerID).Logger()
	defer func() { _ = conn.Close() }()
	done, err := p.gate.Admit()
	if err != nil {
		p.sendError(conn, CodeAttachFailed, "the workspace is locking")
		return
	}
	defer done()

	// Attach is itself an old-runtime effect: tmux may immediately import the
	// pane's output into this process. Validate the lease and verify the
	// complete current target under the same gate used by lifecycle changes
	// before a tmux client can start. The verified target carries the tmux
	// session to attach and the generation to fence frames with, both from
	// the read that was verified. The later live-view preflight remains the
	// race fence before any imported bytes are published to the browser.
	gate := p.gate.Session(sessionID)
	gate.Lock()
	target, _, err := p.verifyAttachTarget(ctx, sessionID, l.ControllerID, l.FencingToken)
	if err != nil {
		gate.Unlock()
		log.Warn().Err(err).Msg("pre-attach identity verification failed")
		p.sendError(conn, CodeAttachFailed, "could not attach to the session terminal")
		return
	}
	att, err := p.tmux.Attach(target.TmuxSession, cols, rows)
	if err != nil {
		gate.Unlock()
		log.Warn().Err(err).Msg("attach failed")
		p.sendError(conn, CodeAttachFailed, "could not attach to the session terminal")
		return
	}

	ctx, cancel := context.WithCancel(ctx)
	out := make(chan ServerFrame, outputQueue)
	self := &attachment{
		sessionID: sessionID, controller: l.ControllerID,
		token: l.FencingToken, cancel: cancel, out: out,
	}
	// Registration happens before releasing the session gate. A takeover also
	// takes this gate, so it either preceded the attach preflight or sees this
	// attachment directly; no polling loop is needed to close a raced socket.
	p.register(self)
	gate.Unlock()

	defer func() { _ = att.Close() }()
	defer cancel()
	// Deregister last (defer order is LIFO). Nothing this attachment
	// authorized outlives it: input is non-durable and the lease itself
	// expires on its normal TTL.
	defer p.deregister(self)

	// The PTY read below blocks and does not watch a context, so cancelling
	// alone would leave the tmux client alive until the pane next emitted a
	// byte — on a quiet pane, indefinitely. Closing the PTY is what unblocks
	// it, so every exit path (disconnect, takeover, slow consumer) reaches
	// teardown promptly.
	go func() {
		<-ctx.Done()
		_ = att.Close()
	}()

	// One writer goroutine owns the socket: gorilla permits only one
	// concurrent writer, and the bounded queue above is what keeps a slow
	// browser from backpressuring the CLI.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); p.writePump(ctx, cancel, conn, out, log) }()

	// An attachment is not live while tmux still owns a copy-mode view. Recheck
	// the complete attachment target, then exit the view under the session gate
	// before publishing attached.
	// A transient refusal leaves the browser disconnected with a reconnect
	// action; reconnect retries this handshake instead of hiding copy-mode
	// optimistically in browser state.
	gate.Lock()
	current, info, verr := p.verifyAttachTarget(ctx, sessionID,
		l.ControllerID, l.FencingToken)
	// Verification must still describe the client we already attached. A
	// replacement target cannot authorize publishing the old client's bytes.
	if verr == nil && (current != target || info.PaneID != att.PaneID()) {
		verr = ErrTargetIdentityMismatch
	}
	var active bool
	if verr == nil {
		active, verr = p.tmux.ExitCopyModeState(ctx, info.PaneID)
		if verr == nil && active {
			verr = errors.New("tmux pane remained in copy-mode after exit")
		}
	}
	gate.Unlock()
	done()
	if verr != nil {
		if errors.Is(verr, lease.ErrNotHeld) || errors.Is(verr, lease.ErrStaleToken) {
			p.closeFenced(out, cancel, log, verr)
		} else {
			log.Warn().Err(verr).Msg("attachment live-view preflight refused")
			p.queue(out, ServerFrame{Type: TypeError, Code: CodeAttachFailed,
				Message: "could not prepare the session terminal's live view"}, cancel, log)
		}
		cancel()
		_ = att.Close()
		wg.Wait()
		return
	}

	// The observed inactive state precedes attached so browser chrome can
	// clear a preserved reconnect affordance only from authoritative tmux
	// evidence, never merely because a new socket opened.
	p.queue(out, ServerFrame{Type: TypeScrollStatus, Active: &active}, cancel, log)
	p.queue(out, ServerFrame{Type: TypeAttached, Token: l.FencingToken,
		Generation: target.Generation}, cancel, log)

	wg.Add(1)
	go func() {
		defer wg.Done()
		p.readPump(ctx, cancel, conn, att, self, sessionID, target.Generation, l, out, log)
	}()

	// tmux -> browser, on this goroutine.
	p.pumpOutput(ctx, cancel, att, self, out, log)

	// Teardown. writePump owns the socket close (see writePump), which is
	// also what unblocks readPump's ReadMessage; no frame that was queued as
	// the reason is dropped by an eager close here.
	cancel()
	_ = att.Close()
	wg.Wait()
}

// pumpOutput drains the tmux client into the bounded queue. It never blocks
// on the browser: a slow browser is dropped rather than allowed to stall
// the provider.
func (p *Proxy) pumpOutput(ctx context.Context, cancel context.CancelFunc,
	att *tmux.Attachment, current *attachment, out chan<- ServerFrame, log zerolog.Logger) {
	buf := make([]byte, readBufferSize)
	for {
		n, err := att.Read(buf)
		if n > 0 {
			frame := ServerFrame{
				Type: TypeOutput,
				Data: base64.StdEncoding.EncodeToString(buf[:n]),
			}
			if !p.queue(out, frame, cancel, log) {
				return
			}
		}
		if err != nil {
			// The tmux client exited: the pane died, the session was
			// killed, or this attachment was closed. When the attachment
			// was not already being torn down with a reason (the context is
			// still live), this is connection/service loss, and the only
			// honest report is interrupted — never a delivery claim, and
			// nothing is replayed. The session's own state is the
			// reconciler's business.
			if ctx.Err() == nil {
				p.recordWriteOutcome(current, WriteInterrupted)
				p.queue(out, ServerFrame{Type: TypeWriteStatus, Code: WriteInterrupted,
					Message: "the terminal connection was lost; nothing was replayed"}, cancel, log)
			}
			cancel()
			return
		}
		if ctx.Err() != nil {
			return
		}
	}
}

// queue enqueues a frame, dropping the attachment if the browser has fallen
// too far behind. It returns false once the attachment is over.
func (p *Proxy) queue(out chan<- ServerFrame, f ServerFrame,
	cancel context.CancelFunc, log zerolog.Logger) bool {
	select {
	case out <- f:
		return true
	default:
		log.Warn().Msg("slow consumer: dropping attachment to keep the CLI unblocked")
		select {
		case out <- ServerFrame{Type: TypeError, Code: CodeSlowConsumer,
			Message: "terminal disconnected: the browser could not keep up"}:
		default:
		}
		cancel()
		return false
	}
}

// writePump owns the socket's write side. It also owns the close: closing the
// socket is what unblocks readPump's ReadMessage, and any frame queued as the
// reason for ending the attachment (fenced, slow_consumer, or a write
// outcome) must reach the browser before that close. On cancellation it
// drains the queue first, so the final frame does not race the close and get
// dropped.
func (p *Proxy) writePump(ctx context.Context, cancel context.CancelFunc,
	conn *websocket.Conn, out <-chan ServerFrame, log zerolog.Logger) {
	ping := time.NewTicker(pingInterval)
	defer ping.Stop()
	defer cancel()
	defer func() { _ = conn.Close() }()
	for {
		select {
		case <-ctx.Done():
			for {
				select {
				case f, ok := <-out:
					if !ok {
						return
					}
					if !p.writeFrame(conn, f, log) {
						return
					}
				default:
					return
				}
			}
		case f, ok := <-out:
			if !ok {
				return
			}
			if !p.writeFrame(conn, f, log) {
				return
			}
		case <-ping.C:
			_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// writeFrame encodes and writes one server frame. It reports false only when
// the socket is gone; an unencodable frame is logged and skipped, not fatal
// to the attachment.
func (p *Proxy) writeFrame(conn *websocket.Conn, f ServerFrame, log zerolog.Logger) bool {
	b, err := encodeFrame(f)
	if err != nil {
		log.Error().Err(err).Msg("encode frame")
		return true
	}
	_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	if err := conn.WriteMessage(websocket.TextMessage, b); err != nil {
		return false
	}
	return true
}

// readPump handles browser frames. Every input and resize is gated on the
// lease and its fencing token — a superseded controller is closed, not
// merely ignored, so it stops believing it is in control.
func (p *Proxy) readPump(ctx context.Context, cancel context.CancelFunc,
	conn *websocket.Conn, att *tmux.Attachment, current *attachment,
	sessionID string, sessionGeneration int,
	l *lease.Lease, out chan<- ServerFrame, log zerolog.Logger) {
	defer cancel()
	conn.SetReadLimit(readLimit)
	scrolls := newScrollLimiter(time.Now())
	// Returning to live is a one-shot state transition, not ordinary view
	// movement. Its reserved low-rate bucket cannot be depleted by movement
	// churn, while still bounding hostile retries that each run the pane
	// preflight and may fork tmux.
	liveScrolls := newLiveScrollLimiter(time.Now())
	_ = conn.SetReadDeadline(time.Now().Add(pongTimeout))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(pongTimeout))
	})

	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(pongTimeout))

		var f ClientFrame
		if err := json.Unmarshal(msg, &f); err != nil {
			p.queue(out, ServerFrame{Type: TypeError, Code: CodeBadFrame,
				Message: "unreadable frame"}, cancel, log)
			return
		}

		switch f.Type {
		case TypeHeartbeat:
			if _, err := p.fence.Heartbeat(ctx, sessionID, l.ControllerID, f.Token); err != nil {
				p.closeFenced(out, cancel, log, err)
				return
			}
		case TypeInput, TypeReply:
			data, derr := base64.StdEncoding.DecodeString(f.Data)
			if derr != nil {
				p.queue(out, ServerFrame{Type: TypeError, Code: CodeBadFrame,
					Message: "unreadable input"}, cancel, log)
				return
			}
			// Emulator replies carry no user intent, so they face a
			// second boundary raw input does not: only complete allowlisted
			// machine replies may flow at all. A rejected frame is not
			// written and the attachment stays up for real input, but it is
			// answered with a non-terminal dropped status so every write
			// frame gets exactly one status.
			if f.Type == TypeReply && !filterReply(data) {
				log.Warn().Msg("blocked terminal reply outside the allowlist")
				p.queue(out, ServerFrame{Type: TypeWriteStatus, Code: WriteDropped,
					Message: "a terminal reply outside the allowed set was not written"}, cancel, log)
				continue
			}
			// Direct terminal input: every byte is forwarded immediately
			// through the single serialized writer. The session write gate
			// is held across [revalidate token + generation -> write] so a
			// takeover or respawn can never commit between a stale controller's
			// validation and its bytes.
			done, admissionErr := p.gate.Admit()
			if admissionErr != nil {
				p.rejectWrite(current, out, cancel, log, "the workspace is locking; input was not sent")
				return
			}
			gate := p.gate.Session(sessionID)
			gate.Lock()

			// The token is checked once, immediately before the tmux boundary.
			// Lifecycle changes and takeover use this same process-local gate;
			// reloading the session row and full process identity here only
			// defended against a second writer.
			verr := p.fence.Validate(ctx, sessionID, l.ControllerID, f.Token)
			if verr == nil && f.Generation != sessionGeneration {
				verr = fmt.Errorf("%w: frame carries %d, attachment is on %d",
					errStaleGeneration, f.Generation, sessionGeneration)
			}
			var paneState tmux.PaneInputState
			if verr == nil {
				paneState, verr = p.tmux.PaneInputState(ctx, att.PaneID())
			}
			if verr == nil && paneState.Dead {
				verr = fmt.Errorf("%w: attached pane %s is dead",
					ErrTargetIdentityMismatch, att.PaneID())
			}
			if verr != nil {
				gate.Unlock()
				done()
				p.rejectWrite(current, out, cancel, log,
					writeRejectionMessage(verr))
				return
			}

			// A pane in a tmux view mode (copy-mode, entered by a scroll or
			// by the user through tmux's own prefix bindings)
			// routes EVERY key to the mode's key table: bytes written now
			// would be silently discarded while still acknowledged by tmux —
			// the one claim ("sent") this product must never make falsely.
			// So user input first exits the view, exactly as a native
			// terminal snaps to the bottom when the user types; the bytes
			// then land in the live TUI. This runs under the same gate as
			// the write, and a scroll frame also takes this gate, so no
			// re-entry can interleave. If the view cannot be exited, the
			// write is refused as a provable pre-write rejection — nothing
			// crossed.
			//
			// TypeReply is deliberately NOT included: a machine reply
			// carries no user intent, and yanking the user out of
			// their scrollback view because the emulator answered a
			// background query would be a worse trade. Replies are rare,
			// tmux answers most pane queries itself, and a swallowed reply
			// is re-asked by the querier; swallowed typing is simply lost.
			if f.Type == TypeInput && paneState.InMode {
				active, cerr := p.tmux.ExitCopyModeState(ctx, att.PaneID())
				if cerr == nil && active {
					cerr = errors.New("tmux pane remained in copy-mode after exit")
				}
				if cerr != nil {
					gate.Unlock()
					done()
					log.Warn().Err(cerr).Msg("could not exit the pane's view mode before input")
					p.rejectWrite(current, out, cancel, log,
						"the pane is showing a scrollback view that could not be exited")
					return
				}
			}

			// Bounded, because the gate is held across this write and a
			// takeover needs the gate: an unbounded write into a pane whose
			// reader has stalled would block the user from taking
			// control, which must always be available. A terminal that will
			// not drain within the bound loses its attachment, exactly as a
			// browser that will not drain does.
			n, werr := p.writeBounded(att, data)
			gate.Unlock()
			done()
			if werr == nil && n != len(data) {
				werr = io.ErrShortWrite
			}
			if werr != nil {
				// A live write that cannot prove zero bytes crossed the
				// tmux boundary is UNCERTAIN, never "sent" and never
				// retried. The attachment ends; the user inspects the
				// native TUI before typing again.
				log.Warn().Err(werr).Int("written", n).
					Msg("terminal write failed or timed out; dropping the attachment")
				p.recordWriteOutcome(current, WriteUncertain)
				p.queue(out, ServerFrame{Type: TypeWriteStatus, Code: WriteUncertain,
					Message: "the terminal write could not be verified; the input may not have reached the CLI"}, cancel, log)
				return
			}
			// tmux acknowledged the write. Sent means bytes handed to tmux for
			// the live pane — never arrived at, accepted by, or executed by the CLI.
			p.recordWriteOutcome(current, WriteSent)
			p.queue(out, ServerFrame{Type: TypeWriteStatus, Code: WriteSent}, cancel, log)
		case TypeResize:
			done, admissionErr := p.gate.Admit()
			if admissionErr != nil {
				return
			}
			if err := p.fence.Validate(ctx, sessionID, l.ControllerID, f.Token); err != nil {
				p.closeFenced(out, cancel, log, err)
				done()
				return
			}
			if rerr := att.Resize(f.Cols, f.Rows); rerr != nil {
				log.Warn().Err(rerr).Msg("resize refused")
			}
			done()
		case TypeScroll:
			// A scroll is a view command on tmux-owned pane state. It carries
			// no terminal bytes, but it runs under the session write gate with
			// the same lease and generation checks so it cannot interleave with
			// the input path's exit-view-then-write sequence.
			if f.Direction != ScrollUp && f.Direction != ScrollDown && f.Direction != ScrollLive {
				p.queue(out, ServerFrame{Type: TypeError, Code: CodeBadFrame,
					Message: "unknown scroll direction"}, cancel, log)
				return
			}
			// Server-side rate limit: each scroll forks one tmux process,
			// and the browser's own coalescing interval is a courtesy, not
			// a control. Excess movement frames are dropped — the wheel keeps
			// sending, so a dropped movement self-heals. Live has a separate
			// bounded one-shot allowance because it is the escape from the
			// state created by an accepted upward movement.
			if f.Direction == ScrollLive {
				if !liveScrolls.allow(time.Now()) {
					log.Debug().Msg("live scroll frame dropped by the reserved rate limit")
					continue
				}
			} else if !scrolls.allow(time.Now()) {
				log.Debug().Msg("scroll frame dropped by the server-side rate limit")
				continue
			}
			done, admissionErr := p.gate.Admit()
			if admissionErr != nil {
				p.rejectWrite(current, out, cancel, log, "the workspace is locking; input was not sent")
				return
			}
			gate := p.gate.Session(sessionID)
			gate.Lock()
			verr := p.fence.Validate(ctx, sessionID, l.ControllerID, f.Token)
			if verr == nil && f.Generation != sessionGeneration {
				verr = fmt.Errorf("%w: frame carries %d, attachment is on %d",
					errStaleGeneration, f.Generation, sessionGeneration)
			}
			if verr != nil {
				gate.Unlock()
				done()
				if errors.Is(verr, lease.ErrNotHeld) || errors.Is(verr, lease.ErrStaleToken) {
					p.closeFenced(out, cancel, log, verr)
					return
				}
				log.Warn().Err(verr).Msg("scroll refused by the write-target preflight")
				continue
			}
			var active bool
			var serr error
			switch f.Direction {
			case ScrollUp:
				active, serr = p.tmux.CopyModeScrollUpState(ctx, att.PaneID(), f.Lines)
			case ScrollDown:
				active, serr = p.tmux.CopyModeScrollDownState(ctx, att.PaneID(), f.Lines)
			case ScrollLive:
				active, serr = p.tmux.ExitCopyModeState(ctx, att.PaneID())
			}
			gate.Unlock()
			done()
			if serr != nil {
				log.Warn().Err(serr).Msg("copy-mode scroll refused")
				continue
			}
			p.queue(out, ServerFrame{Type: TypeScrollStatus, Active: &active}, cancel, log)
		default:
			p.queue(out, ServerFrame{Type: TypeError, Code: CodeBadFrame,
				Message: "unknown frame type"}, cancel, log)
			return
		}
	}
}

// verifyAttachTarget is the attachment preflight. An attach can import bytes
// from tmux immediately, so the lease is validated and the complete target
// reconciled before starting the tmux client — in that order: a fenced
// controller is refused before any target state is read, let alone opened.
// The hot input path relies on this verified attachment plus the
// process-local session gate; it does not repeat the database, tmux, and
// process inspection for every frame.
func (p *Proxy) verifyAttachTarget(ctx context.Context, targetID, controllerID string,
	token int64) (Target, *tmux.PaneInfo, error) {
	if err := p.fence.Validate(ctx, targetID, controllerID, token); err != nil {
		return Target{}, nil, err
	}
	return p.targets.Verify(ctx, targetID)
}

// errStaleGeneration marks a frame whose generation does not match the
// attachment generation — the browser is speaking for another terminal
// lifetime.
var errStaleGeneration = errors.New("write frame generation is stale")

// Scroll-frame rate limiting. Each scroll frame forks
// one tmux process server-side; the browser coalesces to ~40 commands/s,
// but a client is not a control. A small token bucket per attachment caps
// the fork rate at what the browser legitimately produces, with headroom
// for network jitter delivering a burst at once.
const (
	scrollBurst      = 10.0
	scrollRatePerSec = 40.0
	liveScrollBurst  = 4.0
	liveScrollRate   = 4.0
)

type scrollLimiter struct {
	tokens float64
	last   time.Time
	burst  float64
	rate   float64
}

func newScrollLimiter(now time.Time) *scrollLimiter {
	return newBoundedScrollLimiter(now, scrollBurst, scrollRatePerSec)
}

func newLiveScrollLimiter(now time.Time) *scrollLimiter {
	return newBoundedScrollLimiter(now, liveScrollBurst, liveScrollRate)
}

func newBoundedScrollLimiter(now time.Time, burst, rate float64) *scrollLimiter {
	return &scrollLimiter{tokens: burst, last: now, burst: burst, rate: rate}
}

// allow consumes one token if available, refilling by elapsed time. It is
// called only from the attachment's read loop, so it needs no lock.
func (s *scrollLimiter) allow(now time.Time) bool {
	if elapsed := now.Sub(s.last).Seconds(); elapsed > 0 {
		s.tokens = math.Min(s.burst, s.tokens+elapsed*s.rate)
		s.last = now
	}
	if s.tokens < 1 {
		return false
	}
	s.tokens--
	return true
}

// rejectWrite reports a provable pre-write rejection as failed and ends the
// attachment. Nothing crossed the tmux boundary, and the browser is told
// exactly that rather than left guessing.
func (p *Proxy) rejectWrite(current *attachment, out chan<- ServerFrame,
	cancel context.CancelFunc, log zerolog.Logger, reason string) {
	log.Warn().Msg("write refused before the tmux boundary: " + reason)
	p.recordWriteOutcome(current, WriteFailed)
	p.queue(out, ServerFrame{Type: TypeWriteStatus, Code: WriteFailed,
		Message: "input refused before reaching the terminal: " + reason}, cancel, log)
	cancel()
}

// writeRejectionMessage maps a preflight failure to a user-facing
// reason. Messages carry no internal detail.
func writeRejectionMessage(err error) string {
	switch {
	case errors.Is(err, lease.ErrNotHeld), errors.Is(err, lease.ErrStaleToken):
		return "this browser no longer controls the session"
	case errors.Is(err, ErrTargetIdentityMismatch):
		return "the session's terminal no longer matches its recorded identity"
	case errors.Is(err, errStaleGeneration):
		return "the session was restarted; reattach to continue"
	default:
		return "the session is not in a state that accepts input"
	}
}

// closeFenced closes an attachment whose controller no longer holds the
// lease, and says WHICH way it lost it. A lease that merely timed out was
// not taken by anyone: reporting that as a takeover tells the user
// something untrue, and denies the browser the one distinction it needs
// to reclaim its own idle session without displacing a live one.
func (p *Proxy) closeFenced(out chan<- ServerFrame, cancel context.CancelFunc,
	log zerolog.Logger, err error) {
	code, message := CodeFenced, "another browser has taken control of this session"
	if errors.Is(err, lease.ErrExpired) {
		code, message = CodeLeaseExpired, "this browser's session lease timed out"
	}
	log.Info().Err(err).Str("code", code).Msg("controller lost the lease; closing attachment")
	p.queue(out, ServerFrame{Type: TypeError, Code: code, Message: message}, cancel, log)
	cancel()
}

func (p *Proxy) sendError(conn *websocket.Conn, code, msg string) {
	b, err := encodeFrame(ServerFrame{Type: TypeError, Code: code, Message: msg})
	if err != nil {
		return
	}
	_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	_ = conn.WriteMessage(websocket.TextMessage, b)
}
