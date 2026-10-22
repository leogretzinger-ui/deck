package reconciler

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"

	"github.com/noisefloor-oss/deck/internal/session"
	"github.com/noisefloor-oss/deck/internal/tmux"
)

// Recovery is the automatic resume sweep: after each observation pass,
// freshly recorded deaths get their resume attempts through the session
// manager's gated path. The reconciler drives it because the
// reconciliation cadence IS the recovery trigger; the policy itself lives
// with the session manager.
type Recovery interface {
	AutoResumeSweep(ctx context.Context) error
	SweepTemporaryDirs(ctx context.Context) error
	// SampleActivity records each live session's content-free tmux activity
	// timestamp. It rides the same cadence because it is the same
	// kind of work: observing the world rather than being told about it.
	SampleActivity(ctx context.Context) error
}

// Reconciler derives session status from observed tmux/process reality.
// See the package documentation for the sweep and its boundaries.
type Reconciler struct {
	repo     *session.Repository
	tmux     *tmux.Client
	interval time.Duration
	// startingGrace covers tmux's bounded launch plus the eager provider-ID
	// hook window. Cadence may be much shorter in tests or deployments and is
	// not authority to retire an in-flight launch.
	startingGrace time.Duration
	// recovery, when set, runs after every pass. Boot leaves it
	// unset for the startup pass — readiness must not wait on provider
	// CLIs spawning — and wires it before the interval loop starts.
	recovery Recovery
	// log carries pass failures out of the cadence loop:
	// recovery-critical errors must not vanish in `_, _ =`. Nop by
	// default; boot injects the service logger.
	log zerolog.Logger

	discrepancies atomic.Int64
	lastPass      atomic.Int64 // unix nanos of the last completed pass

	// audited dedups unknown-pane audit events: a foreign pane that lingers
	// in the namespace is COUNTED on every pass but written to the audit
	// trail once, not every fifteen seconds forever. Keys are pruned to the
	// conditions observed in the latest pass, so the map is bounded by the
	// live pane population and a condition that goes away and recurs is
	// audited again as the new occurrence it is.
	mu      sync.Mutex
	audited map[string]bool

	// runMu serializes RunOnce: the startup pass, the interval loop, and
	// attachment-error triggers may otherwise overlap, and both the dedup
	// check-then-write and the prune assume one sweep at a time.
	runMu sync.Mutex

	now func() time.Time
}

// Report summarizes one pass, for logs and the status endpoint.
type Report struct {
	PanesObserved           int
	SessionsActive          int
	ServerDown              bool // no tmux server: inventory empty, rows resolved by recorded pid
	ObservationUnavailable  bool // inventory could not establish exact attachments
	ExitsRecorded           int
	Interrupted             int
	UnknownPanes            int
	ForbiddenAlive          int // live identity-matched pane on a terminal row
	OrphanProcesses         int // recorded process alive with no attachable pane
	StaleStarting           int
	LegacyRecoveryConverged int // released retry/probation residue cleared from an exact-live row
	Discrepancies           int // sum of everything above that contradicts a record
	ObservationSkip         int // panes that vanished between inventory and probe
}

// New wires a reconciler. interval is the reconciliation cadence (config
// reconciler.interval, 15s default). Starting launches have a separate bound
// that covers tmux launch and eager provider identity capture.
func New(repo *session.Repository, t *tmux.Client, interval time.Duration) *Reconciler {
	return &Reconciler{
		repo:          repo,
		tmux:          t,
		interval:      interval,
		startingGrace: 45 * time.Second,
		audited:       make(map[string]bool),
		log:           zerolog.Nop(),
		now:           time.Now,
	}
}

// auditOrphanOnce writes the unknown-pane audit event the first time this
// reconciler sees the condition, and only counts it afterwards. The key is
// marked audited strictly AFTER the write succeeds — marking first would
// let one failed write suppress the condition's audit forever. keys
// collects every condition observed this pass, for pruning.
func (r *Reconciler) auditOrphanOnce(ctx context.Context, keys map[string]bool, key, details string) error {
	keys[key] = true
	r.mu.Lock()
	done := r.audited[key]
	r.mu.Unlock()
	if done {
		return nil
	}
	if err := r.repo.AppendOrphanEvent(ctx, details); err != nil {
		return err
	}
	r.mu.Lock()
	r.audited[key] = true
	r.mu.Unlock()
	return nil
}

// pruneAudited drops dedup keys whose condition was not observed this pass.
// That bounds the map by the live pane population, and a condition that
// genuinely goes away and later recurs is audited again as the new
// occurrence it is.
func (r *Reconciler) pruneAudited(keys map[string]bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for k := range r.audited {
		if !keys[k] {
			delete(r.audited, k)
		}
	}
}

// SetRecovery wires the automatic resume sweep. Call before Run;
// passes already in flight are not retrofitted.
func (r *Reconciler) SetRecovery(rec Recovery) { r.recovery = rec }

// SetLogger injects the service logger for cadence-loop pass failures.
// Call before Run.
func (r *Reconciler) SetLogger(log zerolog.Logger) { r.log = log }

// Discrepancies reports the total contradictions observed since start.
func (r *Reconciler) Discrepancies() int64 { return r.discrepancies.Load() }

// Interval reports the configured reconciliation cadence.
func (r *Reconciler) Interval() time.Duration { return r.interval }

// LastPass reports when the last complete pass finished, zero if none has.
func (r *Reconciler) LastPass() time.Time {
	n := r.lastPass.Load()
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n)
}

// Run reconciles immediately, then on every interval tick until ctx ends.
// Pass errors do not stop the loop — an unreachable tmux server
// is a condition to keep observing, not a reason to stop reconciling —
// but they are never discarded silently either: a failed
// pass, including a failed recovery sweep, is logged with its cause.
func (r *Reconciler) Run(ctx context.Context) {
	tick := time.NewTicker(r.interval)
	defer tick.Stop()
	r.runLogged(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			r.runLogged(ctx)
		}
	}
}

func (r *Reconciler) runLogged(ctx context.Context) {
	if _, err := r.RunOnce(ctx); err != nil && ctx.Err() == nil {
		r.log.Warn().Err(err).Msg("reconciliation pass failed")
	}
}

// RunOnce performs one full sweep.
//
// No tmux server is not an outage to abort on: it is the normal state at
// first boot and after a container restart, and startup reconciliation
// depends on this pass resolving the rows a previous life
// left behind. With no server there are no panes — every pane object died
// with it — so pane-facing classification is skipped and active rows are
// resolved from the one observation still available: the recorded pid and
// start time against the host process table. Any other inventory error likewise
// proves no death; it makes every attachment unavailable for this pass so the
// separately fenced recovery policy can converge the rows without touching an
// unverified runtime.
func (r *Reconciler) RunOnce(ctx context.Context) (*Report, error) {
	r.runMu.Lock()
	defer r.runMu.Unlock()

	// ORDER MATTERS: rows first, inventory second. Absence from the
	// inventory is only meaningful for rows recorded BEFORE it was taken —
	// a session launched between the two snapshots must find its pane in
	// the newer inventory, not be declared pane-less by an older one. The
	// reverse order deterministically misclassified a healthy concurrent
	// launch. (A pane launched after ListActive still resolves: its
	// per-pane GetByMarker lookup reads current rows.)
	active, err := r.repo.ListActive(ctx)
	if err != nil {
		return nil, err
	}
	panes, err := r.tmux.ListPanes(ctx)
	serverDown := errors.Is(err, tmux.ErrNoServer)
	observationUnavailable := err != nil
	if observationUnavailable {
		// A failed inventory establishes no death, but it does mean Deck cannot
		// verify any recorded attachment. Rows converge to interrupted metadata
		// below and separately fenced recovery remains available.
		panes = nil
	}

	rep := &Report{PanesObserved: len(panes), SessionsActive: len(active),
		ServerDown: serverDown, ObservationUnavailable: observationUnavailable}
	seen := make(map[string]bool, len(panes)) // markers claimed by observed panes
	orphanKeys := make(map[string]bool)       // audit-dedup conditions observed this pass

	for _, pane := range panes {
		if err := r.reconcilePane(ctx, pane, seen, orphanKeys, rep); err != nil {
			return nil, err
		}
	}
	if err := r.reconcileAbsent(ctx, active, seen, rep); err != nil {
		return nil, err
	}
	r.pruneAudited(orphanKeys)

	// ForbiddenAlive, OrphanProcesses, and StaleStarting are breakdowns
	// of Interrupted, not additional discrepancies.
	rep.Discrepancies = rep.ExitsRecorded + rep.Interrupted + rep.UnknownPanes
	r.discrepancies.Add(int64(rep.Discrepancies))
	r.lastPass.Store(r.now().UnixNano())

	// The recovery sweep runs AFTER the observation pass, still under
	// runMu, so a death recorded above gets its first resume attempt in
	// the same cycle. Its outcomes are recorded on the sessions
	// themselves; only an infrastructural failure surfaces here, and it
	// does not void the completed observation pass.
	if r.recovery != nil {
		if err := r.recovery.AutoResumeSweep(ctx); err != nil {
			return rep, fmt.Errorf("reconciler: recovery sweep: %w", err)
		}
		if err := r.recovery.SweepTemporaryDirs(ctx); err != nil {
			return rep, fmt.Errorf("reconciler: temporary-directory sweep: %w", err)
		}
		// Last-activity sampling is presentation only, so it runs last and
		// its failure does not void a completed observation pass.
		if err := r.recovery.SampleActivity(ctx); err != nil {
			return rep, fmt.Errorf("reconciler: activity sweep: %w", err)
		}
	}
	return rep, nil
}

// reconcilePane classifies one observed pane against recorded state.
func (r *Reconciler) reconcilePane(ctx context.Context, pane tmux.PaneRecord, seen, orphanKeys map[string]bool, rep *Report) error {
	if pane.Marker == "" {
		rep.UnknownPanes++
		return r.auditOrphanOnce(ctx, orphanKeys, pane.PaneID+"/unmarked",
			fmt.Sprintf("unmarked pane %s in session %q pid=%d", pane.PaneID, pane.SessionName, pane.PID))
	}
	s, err := r.repo.GetByMarker(ctx, pane.Marker)
	if errors.Is(err, session.ErrNotFound) {
		rep.UnknownPanes++
		return r.auditOrphanOnce(ctx, orphanKeys, pane.PaneID+"/"+pane.Marker,
			fmt.Sprintf("pane %s in session %q carries marker %q no session recorded", pane.PaneID, pane.SessionName, pane.Marker))
	}
	if err != nil {
		return err
	}
	if s.TmuxSocketPath != r.tmux.SocketPath() {
		rep.UnknownPanes++
		return r.auditOrphanOnce(ctx, orphanKeys, pane.PaneID+"/"+pane.Marker+"/socket",
			fmt.Sprintf("pane %s carries marker for session %s recorded on another tmux socket",
				pane.PaneID, s.ID))
	}
	seen[pane.Marker] = true

	// Re-observe in full: the inventory line has no start ticks, process
	// group, or executable, and the tuple comparison needs them.
	info, err := r.tmux.PaneInfoByID(ctx, pane.PaneID)
	if errors.Is(err, tmux.ErrNoSession) {
		// Gone between inventory and probe. A live server denying the pane
		// PROVES it is gone, so this is not left dangling: clearing `seen`
		// hands the session to the absent pass in this same sweep, which
		// classifies it from the recorded pid like any other pane-less row.
		rep.ObservationSkip++
		seen[pane.Marker] = false
		return nil
	}
	if err != nil {
		if s.Status == session.StatusStarting {
			return r.convergeStarting(ctx, s, rep)
		}
		if isActive(s.Status) {
			applied, transitionErr := r.repo.MarkInterruptedObserved(ctx, s,
				"reconciler: exact pane attachment could not be verified")
			if applied {
				rep.Interrupted++
			}
			return transitionErr
		}
		return nil
	}
	// A starting row deliberately carries only its generation and marker so
	// eager provider hooks can be fenced before spawn. It has no committed
	// pane/process tuple to verify. Give the launch one reconciliation interval
	// to publish that tuple; after the bound, retire it as never verified-live.
	if s.Status == session.StatusStarting {
		return r.convergeStarting(ctx, s, rep)
	}

	if verr := session.VerifyIdentity(s, info); verr != nil {
		if isActive(s.Status) {
			// The recorded generation lost its attachment: the pane that
			// carries this row's marker no longer matches the recorded
			// identity tuple. Record the loss honestly as interrupted;
			// the old runtime is left untouched.
			applied, err := r.repo.MarkInterruptedObserved(ctx, s,
				"reconciler: observed pane mismatches the recorded identity: "+verr.Error())
			if applied {
				rep.Interrupted++
			}
			return err
		}
		// A terminal row whose pane mismatches is an unknown pane:
		// whatever now lives there is not what the row described. An
		// interrupted row skips the audit — its transition already
		// recorded exactly this loss, and repeating it on every pass
		// would churn.
		rep.UnknownPanes++
		if s.Status == session.StatusInterrupted {
			return nil
		}
		return r.auditOrphanOnce(ctx, orphanKeys, pane.PaneID+"/"+pane.Marker+"/mismatch",
			fmt.Sprintf("pane %s carries marker of terminal session %s but mismatches its tuple: %v", pane.PaneID, s.ID, verr))
	}

	switch {
	case isActive(s.Status) && info.Dead:
		return r.recordDeadPane(ctx, s, info, rep)
	case isTerminal(s.Status) && !info.Dead:
		// When the exact recorded pane verifies live, restore
		// the row as running and attach normally. This covers both an
		// exited/interrupted row whose pane is alive and frozen legacy
		// detached rows converging in place.
		applied, err := r.repo.RestoreRunningObserved(ctx, s)
		if applied {
			rep.ForbiddenAlive++
		}
		return err
	default:
		// active+alive: healthy. terminal+dead: settled history.
		//
		// Exact-live observation clears retry/probation residue written by an
		// earlier release. New recovery commits never create this state.
		if isActive(s.Status) && !info.Dead && s.ResumeProbationUntil != nil {
			applied, err := r.repo.ConvergeLegacyRecoveryResidue(ctx, s)
			if err != nil {
				return err
			}
			if applied {
				rep.LegacyRecoveryConverged++
			}
		}
		// A healthy observation is also the honest proof that a stop
		// request did not end in an exit: alive past the attribution
		// window means the CLI survived it, and the stamp is cleared so
		// a later genuine crash still recovers.
		if isActive(s.Status) && !info.Dead && s.StopRequestedAt != nil &&
			r.now().Sub(*s.StopRequestedAt) > session.StopAttributionWindow {
			_, err := r.repo.ExpireStopRequest(ctx, s.ID,
				r.now().Add(-session.StopAttributionWindow))
			return err
		}
		return nil
	}
}

func (r *Reconciler) convergeStarting(ctx context.Context, s *session.Session, rep *Report) error {
	if r.now().Sub(s.CreatedAt) < r.startingGrace {
		return nil
	}
	applied, err := r.repo.MarkInterruptedFromStarting(ctx, s,
		"reconciler: starting without a verified controller after a full interval")
	if applied {
		rep.StaleStarting++
		rep.Interrupted++
	}
	return err
}

// recordDeadPane records the exit evidence a dead pane preserves. A dead pane
// whose tmux version omitted the cause is interrupted: death is proven, only
// the exit status is unknown, so automatic recovery remains safe.
func (r *Reconciler) recordDeadPane(ctx context.Context, s *session.Session, info *tmux.PaneInfo, rep *Report) error {
	if info.ExitStatus == nil && info.ExitSignal == nil {
		applied, err := r.repo.MarkInterruptedObserved(ctx, s,
			"reconciler: pane is dead but tmux reported no exit status or signal")
		if applied {
			rep.Interrupted++
		}
		return err
	}
	var code *int
	var sig *string
	if info.ExitSignal != nil {
		v := fmt.Sprintf("%d", *info.ExitSignal)
		sig = &v
	} else {
		code = info.ExitStatus
	}
	applied, err := r.repo.RecordExitObserved(ctx, s, code, sig, r.now())
	if applied {
		rep.ExitsRecorded++
	}
	return err
}

// reconcileAbsent handles active sessions whose marker no observed pane
// carried.
func (r *Reconciler) reconcileAbsent(ctx context.Context, active []*session.Session, seen map[string]bool, rep *Report) error {
	for _, s := range active {
		if s.IdentityMarker != nil && seen[*s.IdentityMarker] {
			continue
		}
		switch s.Status {
		case session.StatusRunning:
			// The recorded pane is gone: the verified-live committed
			// generation lost its Deck attachment. Old process liveness
			// — alive, dead, unknown, or foreign — never gates a
			// separately fenced successor, so the row records the loss
			// honestly as interrupted and the automatic recovery sweep
			// consumes its one opportunity if applicable. No
			// side effect reaches the old runtime.
			applied, err := r.repo.MarkInterruptedObserved(ctx, s,
				"reconciler: recorded pane is gone; attachment lost")
			if err != nil {
				return err
			}
			if applied {
				rep.Interrupted++
				rep.OrphanProcesses++
			}
		case session.StatusStarting:
			// A launch in flight has no marker committed yet; give it one
			// interval to finish. A crash mid-launch leaves this row, and
			// its uncommitted pane (if any) surfaces separately as an
			// unknown pane. A generation that never reached verified-live
			// commitment consumes its (non-existent) auto opportunity:
			// MarkInterruptedFromStarting sets resume_attempts = 1.
			if err := r.convergeStarting(ctx, s, rep); err != nil {
				return err
			}
		}
	}
	return nil
}

func isActive(st session.Status) bool {
	return st == session.StatusStarting || st == session.StatusRunning
}

func isTerminal(st session.Status) bool {
	return st == session.StatusExited || st == session.StatusInterrupted ||
		st == session.StatusNeedsAttention
}
