package terminal

import (
	"context"
	"fmt"

	"github.com/noisefloor-oss/deck/internal/session"
	"github.com/noisefloor-oss/deck/internal/tmux"
)

// sessionTargets verifies recorded session state, socket and live pane identity.
type sessionTargets struct {
	tmux     *tmux.Client
	sessions *session.Repository
}

// newSessionTargets wires the session adapter over the same tmux server
// the proxy queries for pane state.
func newSessionTargets(t *tmux.Client, sessions *session.Repository) *sessionTargets {
	return &sessionTargets{tmux: t, sessions: sessions}
}

// Open refuses a session the repository does not record (or has removed)
// and one not recorded running.
func (a *sessionTargets) Open(ctx context.Context, id string) error {
	s, err := a.sessions.Get(ctx, id)
	if err != nil || s.RemovedAt != nil {
		return fmt.Errorf("%w: session %s", ErrTargetAbsent, id)
	}
	if s.Status != session.StatusRunning {
		return fmt.Errorf("%w: recorded session is %s", ErrTargetNotRunning, s.Status)
	}
	return nil
}

// Verify reloads the recorded session and reconciles it with the live
// terminal: still running, on this proxy's socket, pane alive, and
// process-identical to the record.
func (a *sessionTargets) Verify(ctx context.Context, id string) (Target, *tmux.PaneInfo, error) {
	s, err := a.sessions.Get(ctx, id)
	if err != nil {
		return Target{}, nil, fmt.Errorf("reload recorded session: %w", err)
	}
	if s.Status != session.StatusRunning {
		return Target{}, nil, fmt.Errorf("recorded session is %s, not running", s.Status)
	}
	if s.TmuxSocketPath != a.tmux.SocketPath() {
		return Target{}, nil, fmt.Errorf("%w: tmux_socket_path recorded %s, proxy serves %s",
			ErrTargetIdentityMismatch, s.TmuxSocketPath, a.tmux.SocketPath())
	}
	info, err := a.tmux.PaneInfoForSession(ctx, s.TmuxSessionName)
	if err != nil {
		return Target{}, nil, fmt.Errorf("observe attach target: %w", err)
	}
	// A dead pane has no live CLI to attach to. Identity checks that only
	// apply to a live process are skipped inside VerifyIdentity by design,
	// so the attachment preflight refuses it here instead.
	if info.Dead {
		return Target{}, nil, fmt.Errorf("%w: recorded pane %s is dead",
			ErrTargetIdentityMismatch, info.PaneID)
	}
	if err := session.VerifyIdentity(s, info); err != nil {
		return Target{}, nil, fmt.Errorf("%w: %w", ErrTargetIdentityMismatch, err)
	}
	return Target{TmuxSession: s.TmuxSessionName, Generation: s.Generation}, info, nil
}
