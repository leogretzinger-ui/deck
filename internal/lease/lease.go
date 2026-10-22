// Package lease owns the controller lease: at most one browser drives a
// session's terminal at a time, and a fencing token makes a superseded
// controller's writes impossible rather than merely unlikely.
//
// # Why a token and not a flag
//
// A takeover cannot rely on the old controller noticing. Its WebSocket may
// be wedged, its network may be black-holing, its process may be paused mid
// write. So every raw-input frame carries the token it believes is current,
// and the server refuses anything that does not match the recorded one. A
// deliberate takeover increments the token in the same transaction that
// changes the holder, so the instant the new controller is recorded, every
// frame the old one sends — including frames already in flight — is invalid.
// Fencing is a property of the write path, not of disconnect handling.
//
// # Expiry vs takeover
//
// Two different things end a lease. A lease EXPIRES when its holder stops
// heartbeating (renewed every five seconds and expired fifteen seconds after
// heartbeat loss); claiming an expired lease is ordinary acquisition and does
// NOT increment the token, because no live
// controller is being displaced. A DELIBERATE takeover displaces a live
// holder and always increments — that is the user explicitly saying
// "take control from whoever has it", and it is auditable.
//
// A second browser that has NOT asked to take over is refused with
// ErrHeld. That is what makes the takeover screen honest: the server never
// quietly hands over control just because a new connection appeared.
package lease

import (
	"errors"
	"fmt"
	"time"

	"github.com/noisefloor-oss/deck/internal/audit"
)

// HeartbeatInterval is what a holder must not exceed between renewals; TTL is
// how long a lease survives without one.
const (
	HeartbeatInterval = 5 * time.Second
	TTL               = 15 * time.Second
)

// Lease is the recorded controller of one session.
type Lease struct {
	SessionID     string
	ControllerID  string
	FencingToken  int64
	LastHeartbeat time.Time
	ExpiresAt     time.Time
}

// Expired reports whether the lease has outlived its heartbeat window.
func (l *Lease) Expired(now time.Time) bool { return !now.Before(l.ExpiresAt) }

// Audit event types. Acquisitions and takeovers are user-visible control
// changes; invalidation records a lifecycle action that voided the browser.
const (
	AuditAcquired = string(audit.LeaseAcquired)
	AuditTakeover = string(audit.LeaseTakeover)
	AuditExpired  = string(audit.LeaseExpired)
	// AuditInvalidated records a lease voided unconditionally by the
	// session layer — native resume invalidates every outstanding
	// controller token before the new generation starts. It is not a
	// disconnect: the controller did not agree.
	AuditInvalidated = string(audit.LeaseInvalidated)
)

var (
	// ErrHeld reports a live lease held by someone else. The caller may
	// retry with a deliberate takeover; nothing is handed over implicitly.
	ErrHeld = errors.New("lease: held by another controller")
	// ErrNotHeld reports an operation by a controller that does not hold
	// the lease — a stale renewal or write after being fenced out.
	ErrNotHeld = errors.New("lease: not the current controller")
	// ErrExpired reports a lease that simply timed out — the holder went
	// quiet past the TTL and NOBODY took it. It wraps ErrNotHeld so every
	// existing check keeps treating it as loss of the lease, while a
	// caller that wants to tell "you were displaced" from "you went quiet"
	// can. The distinction is the difference between a true statement and
	// a false one: only displacement means another browser has control.
	ErrExpired = fmt.Errorf("%w: expired", ErrNotHeld)
	// ErrStaleToken reports a fencing-token mismatch: the caller was
	// superseded. Its writes must not reach the terminal.
	ErrStaleToken = errors.New("lease: stale fencing token")
	// ErrNotFound reports no lease for the session.
	ErrNotFound = errors.New("lease: not found")
)
