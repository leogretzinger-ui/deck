package boot

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/noisefloor-oss/deck/internal/server"
	"github.com/noisefloor-oss/deck/internal/tmux"
)

// reconcilerRecent fails readiness when the reconciler has stopped making
// passes: a stale running record is a surfaced fault, and a wedged
// reconciler is how one would hide. Four intervals of slack
// tolerate a slow pass without masking a dead loop.
func (h *Handles) reconcilerRecent(context.Context) error {
	last := h.Reconciler.LastPass()
	if last.IsZero() {
		return fmt.Errorf("no reconciliation pass has completed")
	}
	if age := time.Since(last); age > 4*h.interval {
		return fmt.Errorf("last reconciliation pass was %s ago (interval %s)", age.Round(time.Millisecond), h.interval)
	}
	return nil
}

func (h *Handles) readinessChecks() []server.Check {
	checks := []server.Check{
		{Name: "backend-lock", Probe: func(context.Context) error {
			if !h.Lock.IsHeld() {
				return fmt.Errorf("exclusive backend lock is not held")
			}
			return nil
		}},
		{Name: "database", Probe: h.DB.VerifyWritable},
		{Name: "tmux-security", Probe: func(context.Context) error { return h.Tmux.VerifySecurity() }},
		{Name: "tmux-access", Probe: func(ctx context.Context) error {
			_, err := h.Tmux.ListSessions(ctx)
			if errors.Is(err, tmux.ErrNoServer) {
				// tmux exits normally when its last session closes. An empty
				// private namespace is therefore accessible without a server.
				return nil
			}
			return err
		}},
		{Name: "reconciler-recency", Probe: h.reconcilerRecent},
	}

	return checks
}
