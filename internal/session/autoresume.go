package session

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/noisefloor-oss/deck/internal/inputgate"
	"github.com/noisefloor-oss/deck/internal/provider"
)

// sweepAdmissionSlack covers launch and first observation on top of the
// attestation bound when the sweep weighs whether one more candidate fits
// this pass. A pass without that much budget left must not start a flight
// it will abort.
const sweepAdmissionSlack = 2 * time.Second

type RecoveryCounts struct {
	AutomaticPending  int
	AutomaticInFlight int
}

func (m *Manager) RecoveryCounts(ctx context.Context) (RecoveryCounts, error) {
	var counts RecoveryCounts
	candidates, slots, err := m.repo.RecoveryFactsSnapshot(ctx)
	if err != nil {
		return counts, err
	}
	for _, source := range candidates {
		desc, ok := m.providers[provider.Name(source.Provider)]
		if automaticRecoveryOpportunity(source, desc, ok, m.now()) {
			counts.AutomaticPending++
		}
	}
	counts.AutomaticInFlight = len(slots)
	return counts, nil
}

// AutoResumeSweep gives each exact lost generation one row-local recovery
// execution. Admission itself consumes the opportunity; ordinary provider
// refusal is a diagnostic outcome, not a retry series.
//
// The bound below budgets ONE pass, not the backlog. A candidate that
// cannot finish inside the remaining budget is left untouched for the next
// reconciliation pass, which re-lists it; a budget that expires mid-flight
// is unwound by the recovery itself, which returns the admission so the
// next pass asks again. The sweep therefore converges across passes for any
// number of interrupted sessions instead of silently spending the tail of
// the list — the failure that left a user's newest sessions
// unrecovered after a redeploy.
func (m *Manager) AutoResumeSweep(ctx context.Context) error {
	if m.gate == nil || m.leases == nil || m.workDir == nil {
		return errors.New("session: automatic recovery is not wired")
	}
	candidates, err := m.repo.ListResumeCandidates(ctx)
	if err != nil {
		return err
	}
	sctx, cancel := context.WithTimeout(ctx, m.resumeSweepBound)
	defer cancel()
	deadline, _ := sctx.Deadline()
	for _, source := range candidates {
		if time.Until(deadline) < m.resumeAttestationBound+sweepAdmissionSlack {
			// Out of budget to finish another candidate, not a refusal:
			// stop admitting and let the next reconciliation pass continue.
			return nil
		}
		_, err := m.recover(sctx, source.ID, "", false)
		switch {
		case err == nil,
			errors.Is(err, ErrNotResumable),
			errors.Is(err, ErrRecoverySuperseded),
			errors.Is(err, ErrRecoveryInProgress),
			errors.Is(err, ErrProjectClosing):
			continue
		case errors.Is(err, inputgate.ErrWorkspaceLocking):
			return nil
		case errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil:
			return nil
		default:
			return fmt.Errorf("session: automatic recovery: %w", err)
		}
	}
	return nil
}
