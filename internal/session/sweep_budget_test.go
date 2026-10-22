package session

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/noisefloor-oss/deck/internal/inputgate"
	"github.com/noisefloor-oss/deck/internal/lease"
	"github.com/noisefloor-oss/deck/internal/provider"
	"github.com/noisefloor-oss/deck/internal/tmux"
)

// gatingAvailabilityDescriptor holds the recovery between admission and
// launch once armed, so a sweep's context can expire exactly where a bounded
// pass aborts a candidate mid-flight. Availability is also consulted at
// launch, so blocking starts only when the test arms it.
type gatingAvailabilityDescriptor struct {
	*resumableDescriptor
	armed   atomic.Bool
	release <-chan struct{}
	err     error
}

func (d *gatingAvailabilityDescriptor) Availability() error {
	if d.armed.Load() {
		<-d.release
	}
	return d.err
}

// The incident this guards: a sweep pass whose budget expires after a
// candidate was admitted but before anything was launched. The admission
// must come back — slot gone, opportunity returned, series count kept — or
// the row is excluded from every later sweep and the session never comes
// back unattended.
func TestSweepBudgetAbortedAdmissionReturnsTheOpportunity(t *testing.T) {
	release := make(chan struct{})
	working := &resumableDescriptor{
		fakeDescriptor: sleeperDescriptor(),
		resumeArgv:     []string{"-c", "sleep 300; exit 0"},
	}
	desc := &gatingAvailabilityDescriptor{
		resumableDescriptor: working,
		release:             release,
	}
	mgr, repo, _, projectID := newTestEnv(t, desc)
	mgr.SetResumeDeps(lease.NewManager(repo.db), inputgate.New(), testWorkDirResolver(t))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, err := mgr.Create(ctx, CreateSpec{
		ProjectID: projectID, Provider: provider.Claude, WorkDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	crashAndObserve(t, mgr, s)
	// Create consulted Availability while the gate was disarmed; from here
	// on, the recovery's availability check is where the flight parks.
	desc.armed.Store(true)

	sweepErr := make(chan error, 1)
	go func() { sweepErr <- mgr.AutoResumeSweep(ctx) }()

	// Wait until the flight is admitted, then end its schedule.
	deadline := time.Now().Add(10 * time.Second)
	for {
		slot, err := repo.SuccessorSlot(ctx, s.ID)
		if err != nil {
			t.Fatal(err)
		}
		if slot != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the sweep never admitted the candidate")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	desc.err = context.Canceled
	close(release)
	if err := <-sweepErr; err != nil {
		t.Fatalf("AutoResumeSweep: %v", err)
	}

	if slot, err := repo.SuccessorSlot(context.Background(), s.ID); err != nil || slot != nil {
		t.Fatalf("slot after abort = %v err=%v, want the admission unwound", slot, err)
	}
	row, err := repo.Get(context.Background(), s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.ResumeAttempts != 0 {
		t.Fatalf("resume_attempts = %d, want the opportunity returned", row.ResumeAttempts)
	}
	if row.AutomaticRecoveries != 1 {
		t.Fatalf("automatic_recoveries = %d, want the aborted attempt still counted", row.AutomaticRecoveries)
	}
	if !terminalRecoverySource(row) {
		t.Fatalf("status = %s, want the row left terminal", row.Status)
	}
	if !strings.Contains(strings.Join(auditDetails(t, repo, s.ID, AuditResumeAttempt), "\n"),
		"automatic recovery aborted before launch") {
		t.Fatal("the abort was not recorded in the audit trail")
	}

	// The next pass must be able to ask again, and with a healthy provider
	// it succeeds: the sweep converges across passes.
	mgr.providers[provider.Claude] = working
	if err := mgr.AutoResumeSweep(context.Background()); err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	row, err = repo.Get(context.Background(), s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != StatusRunning {
		t.Fatalf("status = %s, want the session resumed by the next pass", row.Status)
	}
}

// A pass that cannot finish another candidate leaves it untouched for the
// next reconciliation pass instead of starting a flight it will abort.
func TestSweepDefersCandidatesItCannotFinish(t *testing.T) {
	mgr, repo, projectID, _, _ := newResumeEnv(t, []string{"-c", "sleep 300; exit 0"})

	ctx := context.Background()
	s, err := mgr.Create(ctx, CreateSpec{
		ProjectID: projectID, Provider: provider.Claude, WorkDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	crashAndObserve(t, mgr, s)

	mgr.resumeSweepBound = time.Millisecond
	if err := mgr.AutoResumeSweep(ctx); err != nil {
		t.Fatalf("starved sweep: %v", err)
	}
	row, err := repo.Get(ctx, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.ResumeAttempts != 0 {
		t.Fatalf("resume_attempts = %d, want no admission without budget", row.ResumeAttempts)
	}
	if slot, err := repo.SuccessorSlot(ctx, s.ID); err != nil || slot != nil {
		t.Fatalf("slot = %v err=%v, want nothing admitted", slot, err)
	}

	mgr.resumeSweepBound = 30 * time.Second
	if err := mgr.AutoResumeSweep(ctx); err != nil {
		t.Fatalf("funded sweep: %v", err)
	}
	if row, err = repo.Get(ctx, s.ID); err != nil || row.Status != StatusRunning {
		t.Fatalf("status = %s err=%v, want the deferred candidate resumed", row.Status, err)
	}
}

// The backlog is drained newest first: whatever a bounded pass cannot reach
// must be the oldest rows, never the sessions the user just lost.
func TestResumeCandidatesListNewestFirst(t *testing.T) {
	mgr, _, _, projectID := newTestEnv(t, sleeperDescriptor())
	mgr.SetResumeDeps(lease.NewManager(mgr.repo.db), inputgate.New(), testWorkDirResolver(t))

	ctx := context.Background()
	older, err := mgr.Create(ctx, CreateSpec{
		ProjectID: projectID, Provider: provider.Claude, WorkDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Create older: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	newer, err := mgr.Create(ctx, CreateSpec{
		ProjectID: projectID, Provider: provider.Claude, WorkDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Create newer: %v", err)
	}
	crashAndObserve(t, mgr, older)
	crashAndObserve(t, mgr, newer)

	candidates, err := mgr.repo.ListResumeCandidates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 2 {
		t.Fatalf("candidates = %d, want both sessions", len(candidates))
	}
	if candidates[0].ID != newer.ID || candidates[1].ID != older.ID {
		t.Fatalf("candidate order = [%s, %s], want newest first", candidates[0].ID, candidates[1].ID)
	}
}

// A budget that expires while an attested candidate is alive and verified
// commits that candidate instead of unwinding it: attestation is
// confirmation, not permission, and discarding a healthy pane to satisfy the
// schedule would destroy the very work recovery exists to preserve.
func TestSweepBudgetExpiryDuringAttestationCommitsTheLiveCandidate(t *testing.T) {
	mgr, repo, desc, _, projectID, _ := newLadderEnv(t)
	desc.resumeLife = "300"
	mgr.resumeAttestationBound = 30 * time.Second

	setup := context.Background()
	s, err := mgr.Create(setup, CreateSpec{
		ProjectID: projectID, Provider: provider.Claude, WorkDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	crashAndObserve(t, mgr, s)

	// The flight runs under a schedule shorter than the attestation wait,
	// so the budget — not the user and not the provider — is what ends
	// it. A cancellation is a deliberate stop and deliberately leaves the
	// provisional candidate to the converger; that contract has its own
	// test in the workspace-lock suite.
	ctx, cancel := context.WithTimeout(setup, 2*time.Second)
	defer cancel()
	var (
		recovered *Session
		sweepErr  error
		done      = make(chan struct{})
	)
	go func() {
		defer close(done)
		recovered, sweepErr = mgr.recover(ctx, s.ID, "", false)
	}()

	// Confirm the candidate was actually launched before the budget ends.
	launchDeadline := time.Now().Add(10 * time.Second)
	for {
		slot, err := repo.SuccessorSlot(setup, s.ID)
		if err != nil {
			t.Fatal(err)
		}
		if slot != nil && slot.CandidatePanePID != nil {
			break
		}
		if time.Now().After(launchDeadline) {
			t.Fatal("the recovery never launched its candidate")
		}
		time.Sleep(10 * time.Millisecond)
	}
	<-done

	if sweepErr != nil {
		t.Fatalf("recover: %v", sweepErr)
	}
	if recovered == nil || recovered.Status != StatusRunning {
		t.Fatalf("recovered = %v, want the live candidate committed as running", recovered)
	}
	row, err := repo.Get(context.Background(), s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != StatusRunning || row.Generation != s.Generation+1 || !row.GenerationResumed {
		t.Fatalf("row = status %s gen %d resumed=%v, want the live candidate committed",
			row.Status, row.Generation, row.GenerationResumed)
	}
	if slot, err := repo.SuccessorSlot(context.Background(), s.ID); err != nil || slot != nil {
		t.Fatalf("slot = %v err=%v, want the commit to consume the admission", slot, err)
	}
	if info, err := mgr.tmux.PaneInfoByID(context.Background(), *row.TmuxPaneID); err != nil || info.Dead {
		t.Fatalf("the committed pane is not alive (dead=%v err=%v)", info.Dead, err)
	}
}

// A schedule that expires while a pre-launch step is parked must hand the
// admission back even when the unwinding error carries no context chain:
// tmux formats command failures into plain strings, so the flight's own
// context — not the error — names the abort. The gate below returns nil
// after the deadline, and the next context-checked step fails with a
// wrapped tmux error.
func TestSweepBudgetExpiryBeforeLaunchReturnsOpportunityDespiteWrappedErrors(t *testing.T) {
	release := make(chan struct{})
	desc := &gatingAvailabilityDescriptor{
		resumableDescriptor: &resumableDescriptor{
			fakeDescriptor: sleeperDescriptor(),
			resumeArgv:     []string{"-c", "sleep 300; exit 0"},
		},
		release: release,
	}
	mgr, repo, _, projectID := newTestEnv(t, desc)
	mgr.SetResumeDeps(lease.NewManager(repo.db), inputgate.New(), testWorkDirResolver(t))
	mgr.resumeSweepBound = 3 * time.Second
	mgr.resumeAttestationBound = 10 * time.Millisecond

	ctx := context.Background()
	s, err := mgr.Create(ctx, CreateSpec{
		ProjectID: projectID, Provider: provider.Claude, WorkDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	crashAndObserve(t, mgr, s)
	desc.armed.Store(true)

	sweepErr := make(chan error, 1)
	go func() { sweepErr <- mgr.AutoResumeSweep(ctx) }()
	admitted := time.Now()
	deadline := time.Now().Add(10 * time.Second)
	for {
		slot, err := repo.SuccessorSlot(ctx, s.ID)
		if err != nil {
			t.Fatal(err)
		}
		if slot != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the sweep never admitted the candidate")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Let the pass's whole budget lapse before the parked step returns nil:
	// every later context-checked step then fails with a wrapped error.
	if wait := mgr.resumeSweepBound - time.Since(admitted) + 200*time.Millisecond; wait > 0 {
		time.Sleep(wait)
	}
	close(release)
	if err := <-sweepErr; err != nil {
		t.Fatalf("AutoResumeSweep: %v", err)
	}

	row, err := repo.Get(ctx, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.ResumeAttempts != 0 || row.AutomaticRecoveries != 1 {
		t.Fatalf("attempts=%d recoveries=%d, want the opportunity returned with the attempt counted",
			row.ResumeAttempts, row.AutomaticRecoveries)
	}
	if slot, err := repo.SuccessorSlot(ctx, s.ID); err != nil || slot != nil {
		t.Fatalf("slot = %v err=%v, want the admission unwound", slot, err)
	}
	if !strings.Contains(strings.Join(auditDetails(t, repo, s.ID, AuditResumeAttempt), "\n"),
		"automatic recovery aborted before launch") {
		t.Fatal("the abort was not recorded in the audit trail")
	}
}

// A candidate that dies while its flight is parked at the spawn hook still
// gets its refusal recorded when the schedule expires: the corpse is proven
// by the spawn observation when the slot has no recorded tuple, the
// provider's answer is committed as the row's newest generation, and the
// pane with its final output is kept for the user.
func TestSweepBudgetExpiryWithDeadCandidateRecordsTheRefusal(t *testing.T) {
	mgr, repo, desc, _, projectID, _ := newLadderEnv(t)
	desc.resumeLife = "300"
	mgr.resumeAttestationBound = 30 * time.Second

	setup := context.Background()
	s, err := mgr.Create(setup, CreateSpec{
		ProjectID: projectID, Provider: provider.Claude, WorkDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	crashAndObserve(t, mgr, s)

	ctx, cancel := context.WithTimeout(setup, 3*time.Second)
	defer cancel()
	spawned := make(chan *tmux.PaneInfo, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	mgr.lifecycleHooks = &lifecycleTestHooks{resumeSpawned: func(info *tmux.PaneInfo) {
		spawned <- info
		<-release
	}}
	var (
		recovered *Session
		sweepErr  error
		done      = make(chan struct{})
	)
	go func() {
		defer close(done)
		recovered, sweepErr = mgr.recover(ctx, s.ID, "", false)
	}()

	var candidate *tmux.PaneInfo
	select {
	case candidate = <-spawned:
	case <-time.After(10 * time.Second):
		t.Fatal("the recovery never spawned its candidate")
	}
	// The provider answers by dying while the flight is parked.
	if err := syscall.Kill(-candidate.ProcessGroupID, syscall.SIGKILL); err != nil {
		t.Fatalf("kill candidate: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		info, err := mgr.tmux.PaneInfoByID(setup, candidate.PaneID)
		if err != nil {
			t.Fatal(err)
		}
		if info.Dead {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the killed candidate remained live in tmux")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Let the caller's schedule lapse, then let the flight unwind.
	time.Sleep(3 * time.Second)
	releaseOnce.Do(func() { close(release) })
	<-done
	mgr.lifecycleHooks = nil

	if sweepErr == nil || !errors.Is(sweepErr, ErrResumeRefusedByProvider) {
		t.Fatalf("recover err = %v, want the provider's refusal recorded", sweepErr)
	}
	if recovered != nil {
		t.Fatalf("recovered = %v, want no committed session for a refused resume", recovered.Status)
	}
	row, err := repo.Get(setup, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != StatusExited || row.Generation != s.Generation+1 || !row.GenerationResumed {
		t.Fatalf("row = status %s gen %d resumed=%v, want the refusal recorded as the newest generation",
			row.Status, row.Generation, row.GenerationResumed)
	}
	if slot, err := repo.SuccessorSlot(setup, s.ID); err != nil || slot != nil {
		t.Fatalf("slot = %v err=%v, want the admission consumed by the recorded refusal", slot, err)
	}
	if info, err := mgr.tmux.PaneInfoByID(setup, *row.TmuxPaneID); err != nil || !info.Dead {
		t.Fatalf("the refused attempt's pane is gone (%v): its output is the user's only explanation", err)
	}
}

// An aborted fresh-launch admission must not leave the assigned uuid the
// flight allocated: assigned ids are single-use, and a row carrying an id
// the provider never saw would RESUME it on the next pass instead of
// relaunching fresh. The rollback restores the identity the row had before
// the admission — none, for a fresh candidate.
func TestSweepBudgetAbortedFreshLaunchKeepsTheRowIdentityLess(t *testing.T) {
	release := make(chan struct{})
	base := &resumableDescriptor{fakeDescriptor: sleeperDescriptor()}
	desc := &gatingAvailabilityDescriptor{
		resumableDescriptor: base,
		release:             release,
	}
	mgr, repo, _, projectID := newTestEnv(t, desc)
	mgr.SetResumeDeps(lease.NewManager(repo.db), inputgate.New(), testWorkDirResolver(t))

	ctx := context.Background()
	s, err := mgr.Create(ctx, CreateSpec{
		ProjectID: projectID, Provider: provider.Claude, WorkDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// A row with no recorded identity, the shape a pre-tracking legacy row
	// brings to recovery.
	if err := repo.db.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE sessions SET native_session_id = NULL WHERE id = ?`, s.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	crashAndObserve(t, mgr, s)
	desc.armed.Store(true)

	sweepCtx, cancelSweep := context.WithCancel(ctx)
	sweepErr := make(chan error, 1)
	go func() { sweepErr <- mgr.AutoResumeSweep(sweepCtx) }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		slot, err := repo.SuccessorSlot(ctx, s.ID)
		if err != nil {
			t.Fatal(err)
		}
		if slot != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the sweep never admitted the candidate")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancelSweep()
	desc.err = context.Canceled
	close(release)
	if err := <-sweepErr; err != nil {
		t.Fatalf("AutoResumeSweep: %v", err)
	}

	row, err := repo.Get(ctx, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.ResumeAttempts != 0 || row.NativeSessionID != nil {
		t.Fatalf("attempts=%d native=%v, want the opportunity and the identity-less shape returned",
			row.ResumeAttempts, row.NativeSessionID)
	}
	if slot, err := repo.SuccessorSlot(ctx, s.ID); err != nil || slot != nil {
		t.Fatalf("slot = %v err=%v, want the admission unwound", slot, err)
	}

	// The next pass relaunches FRESH: a new uuid is minted, the provider is
	// never asked to resume an id it has not seen, and the row runs.
	mgr.providers[provider.Claude] = base
	if err := mgr.AutoResumeSweep(ctx); err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	row, err = repo.Get(ctx, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != StatusRunning {
		t.Fatalf("status = %s, want a fresh relaunch on the next pass", row.Status)
	}
	if row.NativeSessionID == nil || row.GenerationResumed {
		t.Fatalf("native=%v resumed=%v, want a fresh conversation with its own identity",
			row.NativeSessionID, row.GenerationResumed)
	}
}
