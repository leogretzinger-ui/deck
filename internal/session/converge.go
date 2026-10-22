package session

import (
	"context"

	"github.com/noisefloor-oss/deck/internal/provider"
)

// ConvergeLegacyRows moves every legacy needs_attention row to this contract
// before readiness. Present also calls ConvergeLegacyRow as a defensive
// pre-serialization fence.
func (m *Manager) ConvergeLegacyRows(ctx context.Context) error {
	rows, err := m.repo.ListLegacyNeedsAttention(ctx)
	if err != nil {
		return err
	}
	for _, s := range rows {
		if err := m.ConvergeLegacyRow(ctx, s); err != nil {
			return err
		}
	}
	return nil
}

// ConvergeLegacyRow never leaves a legacy product state behind. Exact live
// identity restores the existing controller. A complete previously committed
// tuple whose attachment is positively absent, dead, or foreign opens one
// automatic opportunity. Incomplete or uncertain rows become passive
// interrupted metadata with that opportunity closed.
func (m *Manager) ConvergeLegacyRow(ctx context.Context, s *Session) error {
	if s == nil || s.RemovedAt != nil || s.Status != StatusNeedsAttention {
		return nil
	}
	observed := m.ObserveRuntime(ctx, s)
	if info := observed.PaneInfo; info != nil && observed.IdentityVerified != nil &&
		*observed.IdentityVerified {
		if !info.Dead {
			_, err := m.repo.RestoreLegacyRunningObserved(ctx, s)
			return err
		}
		if info.ExitStatus != nil || info.ExitSignal != nil {
			code, sig := evidence(info)
			candidate := *s
			candidate.Status = StatusExited
			candidate.ExitCode, candidate.ExitSignal = code, sig
			desc, configured := m.providers[provider.Name(s.Provider)]
			allowAutomatic := completeVerifiedGeneration(s) &&
				automaticRecoveryOpportunity(&candidate, desc, configured, m.now())
			_, err := m.repo.RecordLegacyExitObserved(ctx, s, code, sig, m.now(), allowAutomatic)
			return err
		}
	}

	// A complete tuple proves this generation once committed verified-live.
	// Once its exact attachment is no longer verified, old runtime liveness or
	// observation uncertainty cannot gate a separately fenced successor.
	allowAutomatic := completeVerifiedGeneration(s)
	detail := "legacy detached row converged to passive interrupted metadata"
	if allowAutomatic {
		detail = "legacy verified generation lost its exact attachment"
	}
	_, err := m.repo.ConvergeLegacyInterrupted(ctx, s, allowAutomatic, detail)
	return err
}

func completeVerifiedGeneration(s *Session) bool {
	return s != nil && s.Generation >= 1 && s.TmuxSocketPath != "" &&
		s.TmuxSessionID != nil && *s.TmuxSessionID != "" &&
		s.TmuxPaneID != nil && *s.TmuxPaneID != "" &&
		s.IdentityMarker != nil && *s.IdentityMarker != "" &&
		s.PanePID != nil && *s.PanePID > 1 &&
		s.ProcessGroupID != nil && *s.ProcessGroupID > 1 &&
		s.StartTicks != nil && *s.StartTicks > 0 &&
		s.ExpectedExecutable != nil && *s.ExpectedExecutable != "" &&
		s.BootEpoch != nil && *s.BootEpoch != ""
}
