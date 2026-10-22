package terminal

import (
	"context"
	"errors"

	"github.com/noisefloor-oss/deck/internal/lease"
	"github.com/noisefloor-oss/deck/internal/tmux"
)

// Absent and not-running targets are refused before lease acquisition.
// Identity mismatches are detected during attachment or input verification.
var (
	// ErrTargetAbsent reports a target that does not exist or has been
	// removed.
	ErrTargetAbsent = errors.New("terminal target is absent")
	// ErrTargetNotRunning reports a target with no live terminal to
	// attach to.
	ErrTargetNotRunning = errors.New("terminal target is not running")
	// ErrTargetIdentityMismatch reports a live terminal that no longer
	// matches the target's recorded identity.
	ErrTargetIdentityMismatch = errors.New("terminal target identity mismatch")
)

// Target is a verified terminal attachment point, returned by Verify from
// the read that was verified: the proxy attaches exactly the tmux session
// that was checked, and fences frames with that read's generation.
type Target struct {
	// TmuxSession is the tmux session the proxy's terminal client
	// attaches to.
	TmuxSession string
	// Generation is the target's lifetime counter; every input, reply,
	// and scroll frame must carry it.
	Generation int
}

// Targets is the surface of terminal attachment points the proxy serves.
// The session adapter backs it with the session repository; any
// fixed-purpose terminal the user can already reach can back it the
// same way without the proxy learning what a target is.
type Targets interface {
	// Open refuses a target that cannot be attached — absent
	// (ErrTargetAbsent) or not running (ErrTargetNotRunning) — before
	// any lease is acquired. It returns no facts: serve re-resolves
	// everything through Verify, and stale boundary facts are not a
	// contract.
	Open(ctx context.Context, id string) error

	// Verify reconciles the recorded target against the live terminal —
	// reload, running state, socket, and full pane identity — and returns
	// the verified target and observed pane. The proxy validates the
	// lease before calling this; an attachment can import bytes from tmux
	// immediately, so Verify is the last look before the terminal client
	// starts.
	Verify(ctx context.Context, id string) (Target, *tmux.PaneInfo, error)
}

// Fence is the controller-fencing triple every attachment runs under:
// acquire at the boundary, heartbeat while attached, validate before each
// write. Implementations must keep the lease transition rules and return
// the lease package sentinels (ErrHeld, ErrNotHeld, ErrExpired,
// ErrStaleToken); the proxy's takeover, fencing, and expiry reporting are
// wired to them.
type Fence interface {
	Acquire(ctx context.Context, targetID, controllerID string, deliberate bool) (*lease.Lease, error)
	Heartbeat(ctx context.Context, targetID, controllerID string, token int64) (*lease.Lease, error)
	Validate(ctx context.Context, targetID, controllerID string, token int64) error
}

// The lease manager satisfies the fence as is.
var _ Fence = (*lease.Manager)(nil)
