package session

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/noisefloor-oss/deck/internal/provider"
	"github.com/noisefloor-oss/deck/internal/tmux"
)

// PaneObservation is the result of one non-mutating runtime probe. It keeps
// absence distinct from an observation failure: only a live tmux server that
// denies the pane proves it absent, while a stopped server proves the pane is
// unavailable but says nothing by itself about the recorded process.
type PaneObservation string

const (
	PaneNotRecorded       PaneObservation = "not_recorded"
	PaneAlive             PaneObservation = "alive"
	PaneDead              PaneObservation = "dead"
	PaneAbsent            PaneObservation = "absent"
	PaneServerDown        PaneObservation = "tmux_server_down"
	PaneObservationFailed PaneObservation = "observation_failed"
)

// ProcessObservation reports only what the host process table or a
// successfully observed pane established. Unknown is not collapsed into dead.
type ProcessObservation string

const (
	ProcessNotRecorded       ProcessObservation = "not_recorded"
	ProcessAlive             ProcessObservation = "alive"
	ProcessDead              ProcessObservation = "dead"
	ProcessObservationFailed ProcessObservation = "observation_failed"
)

// RuntimeObservation is the read-only recorded-versus-observed evidence used
// by diagnostics. Issue is safe for structured logs but is never serialized
// to clients; it may contain local process or socket detail.
type RuntimeObservation struct {
	Pane             PaneObservation
	Process          ProcessObservation
	PaneInfo         *tmux.PaneInfo
	IdentityVerified *bool
	Issue            error
}

// ObserveRuntime probes one session without changing its recorded state.
// Normal session reads use Observe, which records newly established lifecycle
// facts. Diagnostics instead need both sides of the comparison intact so an
// user can see the discrepancy that reconciliation still has to settle.
func (m *Manager) ObserveRuntime(ctx context.Context, s *Session) RuntimeObservation {
	observed := RuntimeObservation{Pane: PaneNotRecorded, Process: ProcessNotRecorded}
	if s.TmuxPaneID == nil {
		return observed
	}

	if s.TmuxSocketPath != m.tmux.SocketPath() {
		verified := false
		observed.IdentityVerified = &verified
		observed.Pane = PaneObservationFailed
		observed.Issue = fmt.Errorf("%w: recorded tmux socket is not the active controller socket",
			ErrIdentityMismatch)
	} else {
		info, err := m.readPaneSettling(ctx, *s.TmuxPaneID)
		if err == nil {
			observed.PaneInfo = info
			verified := VerifyIdentity(s, info) == nil
			observed.IdentityVerified = &verified
			if info.Dead {
				observed.Pane = PaneDead
				observed.Process = ProcessDead
			} else {
				observed.Pane = PaneAlive
				observed.Process = ProcessAlive
			}
			return observed
		}

		switch {
		case errors.Is(err, tmux.ErrNoSession):
			observed.Pane = PaneAbsent
		case errors.Is(err, tmux.ErrNoServer):
			observed.Pane = PaneServerDown
			observed.Issue = err
		default:
			observed.Pane = PaneObservationFailed
			observed.Issue = err
		}
	}

	if s.PanePID == nil || s.StartTicks == nil {
		return observed
	}
	if epochErr := VerifyBootEpoch(s); epochErr != nil {
		if errors.Is(epochErr, ErrBootEpochStale) {
			// Start ticks are boot-relative. A stale epoch positively excludes
			// the recorded process without probing a recycled current-boot PID.
			observed.Process = ProcessDead
		} else {
			observed.Process = ProcessObservationFailed
			observed.Issue = errors.Join(observed.Issue, epochErr)
		}
		return observed
	}
	alive, procErr := tmux.ProcAlive(*s.PanePID, *s.StartTicks)
	if procErr != nil {
		observed.Process = ProcessObservationFailed
		observed.Issue = errors.Join(observed.Issue, procErr)
	} else if alive {
		observed.Process = ProcessAlive
	} else {
		observed.Process = ProcessDead
	}
	return observed
}

// RuntimeDiscrepancy compares a recorded lifecycle claim with a read-only
// observation. nil means the observation was insufficient to decide; callers
// must surface that uncertainty rather than report a clean session.
func RuntimeDiscrepancy(s *Session, observed RuntimeObservation) *bool {
	result := func(v bool) *bool { return &v }
	if observed.IdentityVerified != nil && !*observed.IdentityVerified {
		return result(true)
	}
	if observed.Process == ProcessAlive &&
		(observed.Pane == PaneAbsent || observed.Pane == PaneServerDown) {
		return result(true)
	}

	switch s.Status {
	case StatusRunning:
		switch observed.Pane {
		case PaneAlive:
			return result(false)
		case PaneDead, PaneAbsent, PaneServerDown:
			return result(true)
		case PaneObservationFailed:
			if observed.Process == ProcessDead {
				return result(true)
			}
			return nil
		default:
			return nil
		}
	case StatusStarting:
		if observed.Pane == PaneDead {
			return result(true)
		}
		return result(false)
	case StatusExited, StatusInterrupted:
		switch observed.Pane {
		case PaneAlive:
			return result(true)
		case PaneDead, PaneAbsent, PaneServerDown:
			return result(false)
		default:
			return nil
		}
	default:
		return nil
	}
}

// Observe reconciles positive local facts into the durable row. Observation
// uncertainty is not an error and never becomes fabricated death; callers that
// need the browser projection use Present.
func (m *Manager) Observe(ctx context.Context, s *Session) (*Session, error) {
	fresh, _, err := m.observeAndReconcile(ctx, s)
	return fresh, err
}

// Present returns the content-free browser contract from one fresh runtime
// observation and the durable recovery fence. Legacy attention values are
// converged before they can be serialized.
func (m *Manager) Present(ctx context.Context, s *Session) (*Session, error) {
	for legacyCAS := 0; s.Status == StatusNeedsAttention; legacyCAS++ {
		if legacyCAS == 8 {
			return nil, fmt.Errorf("session %s: legacy lifecycle changed repeatedly during convergence", s.ID)
		}
		if err := m.ConvergeLegacyRow(ctx, s); err != nil {
			return nil, err
		}
		var err error
		s, err = m.repo.Get(ctx, s.ID)
		if err != nil {
			return nil, err
		}
	}
	fresh, observed, err := m.observeAndReconcile(ctx, s)
	if err != nil {
		return nil, err
	}
	for range 2 {
		snapshot, open, err := m.repo.RecoveryPresentationSnapshot(ctx, fresh.ID)
		if err != nil {
			return nil, err
		}
		if snapshot.ControllerRevision == fresh.ControllerRevision {
			return m.decoratePresentation(snapshot, observed, open), nil
		}
		fresh, observed, err = m.observeAndReconcile(ctx, snapshot)
		if err != nil {
			return nil, err
		}
	}

	// Sustained concurrent lifecycle churn can outrun the bounded re-observe.
	// Return one internally consistent durable snapshot with explicitly unknown
	// attachment metadata instead of mixing generations or failing the read.
	snapshot, open, err := m.repo.RecoveryPresentationSnapshot(ctx, fresh.ID)
	if err != nil {
		return nil, err
	}
	unknown := RuntimeObservation{Pane: PaneObservationFailed, Process: ProcessObservationFailed}
	return m.decoratePresentation(snapshot, unknown, open), nil
}

// PresentObserved decorates one already coherent recorded/action snapshot with
// a caller's read-only runtime observation. Diagnostics uses it to preserve
// the raw recorded-versus-observed comparison without running reconciliation.
func (m *Manager) PresentObserved(s *Session, observed RuntimeObservation,
	slot *SuccessorSlot) *Session {
	if s == nil {
		return nil
	}
	projected := *s
	return m.decoratePresentation(&projected, observed, slot)
}

func (m *Manager) observeAndReconcile(ctx context.Context, s *Session) (*Session, RuntimeObservation, error) {
	observed := m.ObserveRuntime(ctx, s)
	if s.RemovedAt != nil || s.TmuxPaneID == nil {
		return s, observed, nil
	}
	reload := func() (*Session, RuntimeObservation, error) {
		fresh, err := m.repo.Get(ctx, s.ID)
		if err != nil {
			return s, observed, fmt.Errorf("session %s: read back reconciled state: %w", s.ID, err)
		}
		return fresh, observed, nil
	}

	if observed.PaneInfo == nil {
		if observed.Pane != PaneNotRecorded && s.Status == StatusRunning {
			if _, err := m.repo.MarkInterruptedObserved(ctx, s,
				"read: recorded pane attachment is unavailable; predecessor liveness was not inferred"); err != nil {
				return s, observed, fmt.Errorf("session %s: record interrupted: %w", s.ID, err)
			}
			return reload()
		}
		return s, observed, nil
	}

	info := observed.PaneInfo
	if observed.IdentityVerified == nil || !*observed.IdentityVerified {
		if s.Status == StatusRunning {
			if _, err := m.repo.MarkInterruptedObserved(ctx, s,
				"read: recorded pane identity no longer matches; attachment lost"); err != nil {
				return s, observed, fmt.Errorf("session %s: record interrupted: %w", s.ID, err)
			}
			return reload()
		}
		return s, observed, nil
	}

	if !info.Dead {
		if s.Status == StatusExited || s.Status == StatusInterrupted {
			if _, err := m.repo.RestoreRunningObserved(ctx, s); err != nil {
				return s, observed, err
			}
			return reload()
		}
		if s.Status == StatusRunning && s.ResumeProbationUntil != nil {
			if _, err := m.repo.ConvergeLegacyRecoveryResidue(ctx, s); err != nil {
				return s, observed, err
			}
			return reload()
		}
		if s.Status == StatusRunning && m.stopRequestSurvived(s) {
			if _, err := m.repo.ExpireStopRequest(ctx, s.ID,
				m.now().Add(-StopAttributionWindow)); err != nil {
				return s, observed, err
			}
			return reload()
		}
		return s, observed, nil
	}

	if s.Status != StatusRunning && s.Status != StatusStarting {
		return s, observed, nil
	}
	if info.ExitStatus == nil && info.ExitSignal == nil {
		// tmux reports a pane dead when its pty closes and fills in the exit
		// a reap later; a read in between would record a judged death as an
		// unjudged interruption, and a resumed generation's refusal would be
		// replayed by the sweep. Give the reap a moment before deciding.
		info = m.awaitDeadPaneEvidence(ctx, info)
	}
	if info.ExitStatus == nil && info.ExitSignal == nil {
		if _, err := m.repo.MarkDeadInterruptedObserved(ctx, s,
			"pane is dead but tmux reported no exit status or signal"); err != nil {
			return s, observed, err
		}
	} else {
		code, sig := evidence(info)
		if _, err := m.repo.RecordExitObserved(ctx, s, code, sig, m.now()); err != nil {
			return s, observed, err
		}
	}
	return reload()
}

// readPaneSettling reads a pane, retrying briefly when the read fails for a
// reason other than the pane or server being gone. A process on its way out
// makes its own process-table entries unreadable for a moment, and one such
// read landing at the reconciler's cadence would record a live row as
// interrupted, hand its resumed conversation to the sweep for another attempt,
// and leave the exit it was about to report unjudged.
func (m *Manager) readPaneSettling(ctx context.Context, paneID string) (*tmux.PaneInfo, error) {
	var info *tmux.PaneInfo
	var err error
	for attempt := 0; attempt < 4; attempt++ {
		info, err = m.tmux.PaneInfoByID(ctx, paneID)
		if err == nil || errors.Is(err, tmux.ErrNoSession) || errors.Is(err, tmux.ErrNoServer) {
			return info, err
		}
		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(100 * time.Millisecond):
		}
	}
	return info, err
}

func (m *Manager) decoratePresentation(s *Session, observed RuntimeObservation, slot *SuccessorSlot) *Session {
	s.RecoveryCapability = m.RecoveryCapabilityFor(s)
	s.CodexLoginHelper = m.codexLoginHelper(s)
	s.RecoveryState = RecoveryUnavailable
	if slot != nil {
		s.RecoveryState = RecoveryRecovering
		s.PresentationState = PresentationRecovering
		return s
	}
	desc, configured := m.providers[provider.Name(s.Provider)]
	if automaticRecoveryOpportunity(s, desc, configured, m.now()) {
		s.RecoveryState = RecoveryAutomaticPending
	}

	if s.Status == StatusRunning && observed.IdentityVerified != nil && *observed.IdentityVerified &&
		observed.Pane == PaneAlive {
		s.PresentationState = PresentationRunning
		return s
	}
	// An interrupted row without a complete committed controller tuple has no
	// attachment whose availability Deck can truthfully characterize. This is
	// the passive legacy/never-verified outcome, not an unavailable attachment.
	if s.Status == StatusInterrupted && !completeVerifiedGeneration(s) {
		s.PresentationState = PresentationInactive
		return s
	}
	if observed.Pane == PaneServerDown || observed.Pane == PaneObservationFailed ||
		(observed.IdentityVerified != nil && !*observed.IdentityVerified) ||
		(observed.Pane == PaneAbsent && observed.Process == ProcessAlive) {
		s.PresentationState = PresentationAttachmentUnavailable
		return s
	}
	if refusal, refused := recoveryRefusalForOperator(s, desc, configured); refused {
		s.PresentationState = PresentationResumeEnded
		s.RecoveryRefusal = refusal
		return s
	}
	switch s.Status {
	case StatusExited:
		s.PresentationState = PresentationEnded
	case StatusStarting, StatusRunning:
		s.PresentationState = PresentationAttachmentUnavailable
	default:
		s.PresentationState = PresentationInactive
	}
	return s
}

func (m *Manager) stopRequestSurvived(s *Session) bool {
	return s.StopRequestedAt != nil &&
		m.now().Sub(*s.StopRequestedAt) > StopAttributionWindow
}

// recoveryRefusalForOperator reports a terminal row whose recorded
// conversation the user must decide about: the last generation resumed
// the identity and failed on its own, or the conversation's recorded store
// is gone by name so no resume can be launched. A clean exit is the user
// leaving, a requested stop is theirs, and a row the sweep is about to pick
// up presents as ended until it does — so only the refusal the user must
// answer reaches the card.
func recoveryRefusalForOperator(s *Session, desc provider.Descriptor, configured bool) (RecoveryRefusal, bool) {
	if !terminalRecoverySource(s) || s.NativeSessionID == nil || *s.NativeSessionID == "" ||
		s.StopRequestedAt != nil || workspaceStopped(s) || s.RecoveryState == RecoveryAutomaticPending {
		return "", false
	}
	if configured && recordedStoreGone(desc, s) {
		return RecoveryRefusalStoreGone, true
	}
	if !s.GenerationResumed {
		return "", false
	}
	// A resumed generation that ended with a failure status, or that is
	// gone with no evidence and no unattended attempt left, was not
	// continued by the provider as far as anyone can tell.
	if s.Status == StatusInterrupted ||
		s.ExitSignal != nil || (s.ExitCode != nil && *s.ExitCode != 0) {
		return RecoveryRefusalProvider, true
	}
	return "", false
}
