package githubconnect

import (
	"context"
	"errors"
	"fmt"

	"github.com/noisefloor-oss/deck/internal/session"
	"github.com/noisefloor-oss/deck/internal/terminal"
	"github.com/noisefloor-oss/deck/internal/tmux"
)

// Open refuses unknown or non-current attempts and verifies the tmux
// socket's security before anything is attached. Only the one current
// attempt ID verifies: reload reattaches the live attempt, and no other ID
// names a target.
func (m *Manager) Open(ctx context.Context, id string) error {
	if _, err := m.lookupCurrent(id); err != nil {
		return err
	}
	if err := m.tmux.VerifySocket(); err != nil {
		return fmt.Errorf("github connect: tmux socket: %w", err)
	}
	if err := m.tmux.VerifySecurity(); err != nil {
		return fmt.Errorf("github connect: tmux socket security: %w", err)
	}
	return nil
}

// Verify reconciles the live pane against the attempt's full captured
// identity — tmux session id, pane id, pid, process group, start ticks,
// generation and marker pane options, and the recorded boot epoch — using
// the session package's VerifyIdentity over an in-memory record (this
// surface deliberately has no session rows). Request principal and
// current-ID enforcement happen before the proxy reaches the target, in the
// handler's attach preflight.
func (m *Manager) Verify(ctx context.Context, id string) (terminal.Target, *tmux.PaneInfo, error) {
	a, err := m.lookupCurrent(id)
	if err != nil {
		return terminal.Target{}, nil, err
	}
	live, err := m.observeOwned(ctx, a)
	if err != nil {
		if errors.Is(err, tmux.ErrNoSession) {
			return terminal.Target{}, nil, terminal.ErrTargetNotRunning
		}
		return terminal.Target{}, nil, fmt.Errorf("github connect: observe login pane: %w", err)
	}
	if live.Dead {
		return terminal.Target{}, nil, terminal.ErrTargetNotRunning
	}
	return terminal.Target{TmuxSession: live.SessionName, Generation: a.generation}, live, nil
}

// sessionRecord projects the captured identity into the shape
// session.VerifyIdentity compares, without ever writing a repository row.
func (a *attempt) sessionRecord() *session.Session {
	sessionID := a.info.SessionID
	paneID := a.info.PaneID
	marker := a.marker
	pid := a.info.PID
	pgid := a.info.ProcessGroupID
	ticks := a.info.StartTicks
	epoch := a.bootEpoch
	return &session.Session{
		ID:              a.id,
		TmuxSessionName: a.info.SessionName,
		TmuxSessionID:   &sessionID,
		TmuxPaneID:      &paneID,
		Generation:      a.generation,
		IdentityMarker:  &marker,
		PanePID:         &pid,
		ProcessGroupID:  &pgid,
		StartTicks:      &ticks,
		BootEpoch:       &epoch,
	}
}
