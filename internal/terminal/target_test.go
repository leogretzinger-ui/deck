package terminal

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"

	"github.com/noisefloor-oss/deck/internal/inputgate"
	"github.com/noisefloor-oss/deck/internal/lease"
	"github.com/noisefloor-oss/deck/internal/tmux"
)

// Interface-seam regressions for NewFor: the proxy must refuse a stale or
// invalid target before any terminal input can be delivered, in the same
// order the session path has always refused it — origin, target state,
// lease acquisition, lease validation before target verification — and a
// served interface attachment must still round-trip real bytes. Everything
// runs against a real tmux server on a private socket and a real SQLite
// control database, exactly like the session acceptance suite.

// stubTargets wraps a real Targets and injects refusals while recording
// whether Verify was ever reached.
type stubTargets struct {
	inner       Targets
	openErr     error
	verifyErr   error
	verifyCalls atomic.Int32
	afterVerify func(int32, Target, *tmux.PaneInfo) (Target, *tmux.PaneInfo)
}

func (s *stubTargets) Open(ctx context.Context, id string) error {
	if s.openErr != nil {
		return s.openErr
	}
	return s.inner.Open(ctx, id)
}

func (s *stubTargets) Verify(ctx context.Context, id string) (Target, *tmux.PaneInfo, error) {
	call := s.verifyCalls.Add(1)
	if s.verifyErr != nil {
		return Target{}, nil, s.verifyErr
	}
	target, pane, err := s.inner.Verify(ctx, id)
	if err == nil && s.afterVerify != nil {
		target, pane = s.afterVerify(call, target, pane)
	}
	return target, pane, err
}

func TestTargetChangeDuringAttachmentIsRefused(t *testing.T) {
	for _, change := range []string{"generation", "session", "pane"} {
		t.Run(change, func(t *testing.T) {
			e := newEnv(t)
			other, err := e.tmux.CreateSession(context.Background(), tmux.SessionSpec{
				Name: "other-target", WorkDir: t.TempDir(), Argv: []string{"/bin/sh"},
				Generation: 1, Marker: "other-target",
			})
			if err != nil {
				t.Fatal(err)
			}
			targets := &stubTargets{inner: newSessionTargets(e.tmux, e.repo)}
			targets.afterVerify = func(call int32, target Target, pane *tmux.PaneInfo) (Target, *tmux.PaneInfo) {
				if call == 2 {
					switch change {
					case "generation":
						target.Generation++
					case "session":
						target.TmuxSession = "other-target"
					case "pane":
						pane = other
					}
				}
				return target, pane
			}
			p := NewFor(e.tmux, targets, e.leases, inputgate.New(), []string{testOrigin}, zerolog.Nop())
			srv := mountProxy(t, p)
			c, status := dialProxy(t, srv, e.session.ID, "browser-a")
			if c == nil || status != http.StatusSwitchingProtocols {
				t.Fatalf("handshake status=%d, want101", status)
			}
			defer c.Close()
			if err := c.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
				t.Fatal(err)
			}
			// Refusal may close the socket before its queued error is flushed.
			// It must never publish attached, terminal output, or scroll state.
			for {
				var frame ServerFrame
				if err := c.ReadJSON(&frame); err != nil {
					var timed net.Error
					if errors.As(err, &timed) && timed.Timeout() {
						t.Fatal("changed target left its attachment open")
					}
					break
				}
				if frame.Type != TypeError || frame.Code != CodeAttachFailed {
					t.Fatalf("changed target published %+v", frame)
				}
			}
			if targets.verifyCalls.Load() != 2 {
				t.Fatalf("Verify calls=%d, want2", targets.verifyCalls.Load())
			}
		})
	}
}

// stubFence wraps a real Fence and fails Validate when armed, leaving
// Acquire's transition rules intact.
type stubFence struct {
	inner       Fence
	validateErr error
}

func (f *stubFence) Acquire(ctx context.Context, targetID, controllerID string,
	deliberate bool) (*lease.Lease, error) {
	return f.inner.Acquire(ctx, targetID, controllerID, deliberate)
}

func (f *stubFence) Heartbeat(ctx context.Context, targetID, controllerID string,
	token int64) (*lease.Lease, error) {
	return f.inner.Heartbeat(ctx, targetID, controllerID, token)
}

func (f *stubFence) Validate(ctx context.Context, targetID, controllerID string,
	token int64) error {
	if f.validateErr != nil {
		return f.validateErr
	}
	return f.inner.Validate(ctx, targetID, controllerID, token)
}

// mountProxy serves a proxy on its own HTTP server, separate from the
// env's session-route server.
func mountProxy(t *testing.T, p *Proxy) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/sessions/{id}/terminal", p.Handle)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// dialProxy opens an attachment to a mounted proxy as controllerID,
// returning the connection (nil when refused) and the handshake status.
func dialProxy(t *testing.T, srv *httptest.Server, sessionID, controllerID string) (*websocket.Conn, int) {
	t.Helper()
	u, _ := url.Parse(srv.URL)
	u.Scheme = "ws"
	u.Path = "/api/sessions/" + sessionID + "/terminal"
	q := u.Query()
	q.Set("controller", controllerID)
	q.Set("cols", "80")
	q.Set("rows", "24")
	u.RawQuery = q.Encode()
	conn, resp, err := websocket.DefaultDialer.Dial(u.String(),
		http.Header{"Origin": []string{testOrigin}})
	if err != nil && resp == nil {
		t.Fatalf("dial %s: %v", controllerID, err)
	}
	status := 0
	if resp != nil {
		status = resp.StatusCode
		_ = resp.Body.Close()
	}
	return conn, status
}

// countingAttaches observes every physical tmux attach on the env's
// client, so a test can prove no terminal client was ever started.
func countingAttaches(t *testing.T, e *env) *atomic.Int32 {
	t.Helper()
	var n atomic.Int32
	e.tmux.SetAttachObserverForTest(func(tmux.AttachObservation) { n.Add(1) })
	t.Cleanup(func() { e.tmux.SetAttachObserverForTest(nil) })
	return &n
}

// A target whose recorded state is not running is refused at the boundary
// — 409, not a takeover or a lease — and an absent one is 404. Both must
// happen before any lease is acquired, so a refused handshake cannot
// fence a live controller of that target.
func TestBoundaryRefusalPrecedesLeaseAcquisition(t *testing.T) {
	for _, tc := range []struct {
		name    string
		openErr error
		want    int
	}{
		{name: "not running", openErr: ErrTargetNotRunning, want: http.StatusConflict},
		{name: "absent", openErr: ErrTargetAbsent, want: http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			targets := &stubTargets{inner: newSessionTargets(e.tmux, e.repo), openErr: tc.openErr}
			p := NewFor(e.tmux, targets, e.leases, inputgate.New(), []string{testOrigin}, zerolog.Nop())
			srv := mountProxy(t, p)

			c, status := dialProxy(t, srv, e.session.ID, "browser-a")
			if c != nil {
				c.Close()
			}
			if status != tc.want {
				t.Fatalf("boundary refusal status = %d, want %d", status, tc.want)
			}
			if _, err := e.leases.Get(context.Background(), e.session.ID); !errors.Is(err, lease.ErrNotFound) {
				t.Fatalf("refused handshake left a lease behind: %v", err)
			}
		})
	}
}

// Lease validation precedes target verification: a controller whose token
// is stale is refused before the target is read at all, no tmux client is
// started, and no attachment is registered. The acquire itself still
// happened — the boundary passed — which is exactly the window the
// ordering rule protects.
func TestStaleFenceRefusesBeforeTargetVerification(t *testing.T) {
	e := newEnv(t)
	attaches := countingAttaches(t, e)
	targets := &stubTargets{inner: newSessionTargets(e.tmux, e.repo)}
	fence := &stubFence{inner: e.leases,
		validateErr: fmt.Errorf("%w: interface test staleness", lease.ErrStaleToken)}
	p := NewFor(e.tmux, targets, fence, inputgate.New(), []string{testOrigin}, zerolog.Nop())
	srv := mountProxy(t, p)

	c, status := dialProxy(t, srv, e.session.ID, "browser-a")
	if c == nil || status != http.StatusSwitchingProtocols {
		t.Fatalf("stale-token handshake: conn=%v status=%d, want 101", c, status)
	}
	defer c.Close()
	if frame := readFrame(t, c); frame.Type != TypeError || frame.Code != CodeAttachFailed {
		t.Fatalf("stale-token frame = %+v, want attach_failed", frame)
	}
	awaitSocketClosed(t, c)

	if got := targets.verifyCalls.Load(); got != 0 {
		t.Fatalf("target was verified %d times after a stale fence refusal", got)
	}
	if attaches.Load() != 0 {
		t.Fatal("stale fence refusal started a tmux client")
	}
	if snapshot := p.ConnectionSnapshot(e.session.ID); snapshot.AttachmentCount != 0 {
		t.Fatalf("stale fence refusal registered an attachment: %+v", snapshot)
	}
	if l, err := e.leases.Get(context.Background(), e.session.ID); err != nil ||
		l.ControllerID != "browser-a" {
		t.Fatalf("lease after stale refusal = %+v, %v; want still held by browser-a", l, err)
	}
}

// A failed target verification refuses the connection before starting or
// registering a terminal attachment.
func TestInvalidTargetRefusalStartsNoAttachment(t *testing.T) {
	e := newEnv(t)
	attaches := countingAttaches(t, e)
	targets := &stubTargets{inner: newSessionTargets(e.tmux, e.repo),
		verifyErr: fmt.Errorf("%w: interface test mismatch", ErrTargetIdentityMismatch)}
	p := NewFor(e.tmux, targets, e.leases, inputgate.New(), []string{testOrigin}, zerolog.Nop())
	srv := mountProxy(t, p)

	c, status := dialProxy(t, srv, e.session.ID, "browser-a")
	if c == nil || status != http.StatusSwitchingProtocols {
		t.Fatalf("invalid-target handshake: conn=%v status=%d, want 101", c, status)
	}
	defer c.Close()
	if frame := readFrame(t, c); frame.Type != TypeError || frame.Code != CodeAttachFailed {
		t.Fatalf("invalid-target frame = %+v, want attach_failed", frame)
	}
	awaitSocketClosed(t, c)

	if got := targets.verifyCalls.Load(); got == 0 {
		t.Fatal("invalid target was never verified; the refusal did not exercise the interface")
	}
	if attaches.Load() != 0 {
		t.Fatal("invalid target started a tmux client")
	}
	if snapshot := p.ConnectionSnapshot(e.session.ID); snapshot.AttachmentCount != 0 {
		t.Fatalf("invalid target registered an attachment: %+v", snapshot)
	}
}

// The interface path must serve a real session exactly as the adapter
// path does: the Target returned by Verify carries the tmux session and
// generation the attachment actually runs on, so input round-trips and is
// reported sent — never merely accepted.
func TestNewForServesSessionAttachmentsEndToEnd(t *testing.T) {
	e := newEnv(t)
	p := NewFor(e.tmux, newSessionTargets(e.tmux, e.repo), e.leases,
		inputgate.New(), []string{testOrigin}, zerolog.Nop())
	srv := mountProxy(t, p)

	c, status := dialProxy(t, srv, e.session.ID, "browser-a")
	if c == nil || status != http.StatusSwitchingProtocols {
		t.Fatalf("interface attach: conn=%v status=%d, want 101", c, status)
	}
	defer c.Close()
	token := awaitAttached(t, c)
	send(t, c, e.inputFrame(token,
		base64.StdEncoding.EncodeToString([]byte("printf 'interface-%s\\n' 'parity-ok'\n"))))
	var output strings.Builder
	sent := false
	for !sent || !strings.Contains(output.String(), "interface-parity-ok") {
		frame := readFrame(t, c)
		switch frame.Type {
		case TypeOutput:
			raw, err := base64.StdEncoding.DecodeString(frame.Data)
			if err != nil {
				t.Fatal(err)
			}
			output.Write(raw)
		case TypeWriteStatus:
			if frame.Code != WriteSent {
				t.Fatalf("write outcome=%s, want sent", frame.Code)
			}
			sent = true
		case TypeError:
			t.Fatalf("terminal error: %+v", frame)
		}
	}
}
