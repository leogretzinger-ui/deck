package session

import (
	"context"
	"time"
)

// Last-activity sampling.
//
// Activity is tmux's content-free window activity timestamp. Deck owns one
// pane in one window per managed session, so it observes PTY activity for
// selected and background sessions without reading, parsing, or transporting
// terminal bytes and without attaching a controller.

// activitySweepBound caps database recording for the whole pass so a slow
// store cannot stall the reconciler's observation cadence.
const activitySweepBound = 10 * time.Second

// SampleActivity records each live session's tmux activity timestamp. It is
// run by the reconciler after identity observation.
func (m *Manager) SampleActivity(ctx context.Context) error {
	activities, err := m.ObservedActivity(ctx)
	if err != nil {
		return err
	}
	sctx, cancel := context.WithTimeout(ctx, activitySweepBound)
	defer cancel()
	for _, activity := range activities {
		if sctx.Err() != nil {
			break
		}
		if activity.PreviousAt != nil && !activity.At.After(*activity.PreviousAt) {
			continue
		}
		_ = m.repo.RecordOutput(sctx, activity.SessionID, activity.At)
	}
	return ctx.Err()
}

type OutputActivity struct {
	SessionID  string     `json:"session_id"`
	Generation int        `json:"generation"`
	At         time.Time  `json:"at"`
	PreviousAt *time.Time `json:"-"`
}

// ObservedActivity joins tmux's content-free activity stamps to reconciled
// session identity. Unknown, dead, stale-generation, or marker-mismatched
// panes produce no event.
func (m *Manager) ObservedActivity(ctx context.Context) ([]OutputActivity, error) {
	sessions, err := m.repo.ListActive(ctx)
	if err != nil {
		return nil, err
	}
	if len(sessions) == 0 {
		return nil, nil
	}
	panes, err := m.tmux.ListPanes(ctx)
	if err != nil {
		return nil, err
	}
	byMarker := make(map[string]struct {
		generation int
		at         time.Time
		sessionID  string
		paneID     string
	}, len(panes))
	for _, pane := range panes {
		if pane.Dead || pane.Marker == "" || pane.ActivityAt.IsZero() {
			continue
		}
		byMarker[pane.Marker] = struct {
			generation int
			at         time.Time
			sessionID  string
			paneID     string
		}{pane.Generation, pane.ActivityAt, pane.SessionID, pane.PaneID}
	}
	out := make([]OutputActivity, 0, len(sessions))
	for _, session := range sessions {
		if session.Status != StatusRunning || session.IdentityMarker == nil {
			continue
		}
		pane, ok := byMarker[*session.IdentityMarker]
		if !ok || pane.generation != session.Generation ||
			session.TmuxSessionID == nil || *session.TmuxSessionID != pane.sessionID ||
			session.TmuxPaneID == nil || *session.TmuxPaneID != pane.paneID {
			continue
		}
		out = append(out, OutputActivity{SessionID: session.ID, Generation: session.Generation, At: pane.at, PreviousAt: session.LastOutputAt})
	}
	return out, nil
}
