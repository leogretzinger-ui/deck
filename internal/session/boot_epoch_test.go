package session

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/noisefloor-oss/deck/internal/tmux"
)

// staleBootEpoch simulates a host restart: a recorded epoch that cannot be
// the current boot (boot_id is a random per-boot uuid).
const staleBootEpoch = "00000000-0000-0000-0000-000000000000"

// bootEpochFixture pairs a complete recorded identity tuple with a live
// pane that matches it in every member.
func bootEpochFixture(epoch *string) (*Session, *tmux.PaneInfo) {
	sessionID, paneID, marker := "$1", "%1", "marker"
	pid, pgid := 4242, 4242
	ticks := int64(99887766)
	exe := "/usr/local/bin/claude"
	s := &Session{
		TmuxSessionID: &sessionID, TmuxPaneID: &paneID, Generation: 1,
		IdentityMarker: &marker, PanePID: &pid, ProcessGroupID: &pgid,
		StartTicks: &ticks, BootEpoch: epoch, ExpectedExecutable: &exe,
	}
	info := &tmux.PaneInfo{
		SessionID: sessionID, PaneID: paneID, Marker: marker, Generation: 1,
		PID: pid, ProcessGroupID: pgid, StartTicks: ticks, Executable: exe,
	}
	return s, info
}

// A pid/start-ticks tuple is boot-relative, so a boot-epoch change
// invalidates a tuple that matches in every other member.
func TestVerifyIdentityBootEpoch(t *testing.T) {
	current, err := tmux.BootID()
	if err != nil {
		t.Skipf("host boot epoch unavailable on this platform: %v", err)
	}

	t.Run("matching epoch verifies", func(t *testing.T) {
		s, info := bootEpochFixture(&current)
		if err := VerifyIdentity(s, info); err != nil {
			t.Fatalf("VerifyIdentity: %v", err)
		}
	})

	// The executable path identifies a build, not a process. A user who
	// upgrades a runtime under a live session changes the path without
	// changing which process the row describes, so identity must survive it.
	t.Run("executable divergence does not invalidate a live tuple", func(t *testing.T) {
		s, info := bootEpochFixture(&current)
		info.Executable = ""
		if err := VerifyIdentity(s, info); err != nil {
			t.Fatalf("VerifyIdentity with no observed executable = %v, want nil", err)
		}

		s, info = bootEpochFixture(&current)
		info.Executable = "/opt/homebrew/bin/node"
		if err := VerifyIdentity(s, info); err != nil {
			t.Fatalf("VerifyIdentity after a runtime move = %v, want nil", err)
		}
	})

	t.Run("stale epoch invalidates a matching tuple", func(t *testing.T) {
		stale := staleBootEpoch
		s, info := bootEpochFixture(&stale)
		err := VerifyIdentity(s, info)
		if !errors.Is(err, ErrIdentityMismatch) || !errors.Is(err, ErrBootEpochStale) {
			t.Fatalf("VerifyIdentity = %v, want ErrIdentityMismatch wrapping ErrBootEpochStale", err)
		}
	})

	t.Run("nil epoch refuses a matching tuple", func(t *testing.T) {
		s, info := bootEpochFixture(nil)
		err := VerifyIdentity(s, info)
		if !errors.Is(err, ErrIdentityMismatch) || !errors.Is(err, ErrBootEpochUnknown) {
			t.Fatalf("VerifyIdentity with no recorded epoch = %v, want ErrIdentityMismatch wrapping ErrBootEpochUnknown", err)
		}
	})

	t.Run("stale epoch invalidates a dead pane", func(t *testing.T) {
		stale := staleBootEpoch
		s, info := bootEpochFixture(&stale)
		info.Dead = true
		err := VerifyIdentity(s, info)
		if !errors.Is(err, ErrIdentityMismatch) || !errors.Is(err, ErrBootEpochStale) {
			t.Fatalf("VerifyIdentity on a stale-epoch dead pane = %v, want ErrIdentityMismatch wrapping ErrBootEpochStale", err)
		}
	})

	t.Run("nil epoch refuses a dead pane", func(t *testing.T) {
		s, info := bootEpochFixture(nil)
		info.Dead = true
		err := VerifyIdentity(s, info)
		if !errors.Is(err, ErrIdentityMismatch) || !errors.Is(err, ErrBootEpochUnknown) {
			t.Fatalf("VerifyIdentity on a dead pane with no recorded epoch = %v, want ErrIdentityMismatch wrapping ErrBootEpochUnknown", err)
		}
	})
}

// recordBootEpoch rewrites a session's recorded epoch directly, simulating
// a host restart between the recording and the next observation.
func recordBootEpoch(t *testing.T, repo *Repository, id, epoch string) {
	t.Helper()
	if err := repo.db.WithTx(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE sessions SET boot_epoch = ? WHERE id = ?`, epoch, id)
		return err
	}); err != nil {
		t.Fatalf("record stale boot epoch: %v", err)
	}
}

func clearBootEpoch(t *testing.T, repo *Repository, id string) {
	t.Helper()
	if err := repo.db.WithTx(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE sessions SET boot_epoch = NULL WHERE id = ?`, id)
		return err
	}); err != nil {
		t.Fatalf("clear boot epoch: %v", err)
	}
}

// A row written before the epoch column cannot verify even a matching live
// pane. The lost attachment is recorded as interrupted without signalling
// the process.
func TestLegacyBootEpochInterrupts(t *testing.T) {
	if _, err := tmux.BootID(); err != nil {
		t.Skipf("host boot epoch unavailable on this platform: %v", err)
	}
	mgr, repo, _, projectID := newTestEnv(t, sleeperDescriptor())
	ctx := context.Background()

	s, err := mgr.Create(ctx, CreateSpec{ProjectID: projectID, Provider: "claude", WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	clearBootEpoch(t, repo, s.ID)
	legacy, err := repo.Get(ctx, s.ID)
	if err != nil {
		t.Fatalf("Get legacy row: %v", err)
	}
	observed, err := mgr.Observe(ctx, legacy)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if observed.Status != StatusInterrupted {
		t.Fatalf("status = %s, want interrupted", observed.Status)
	}
	alive, err := tmux.ProcAlive(*s.PanePID, *s.StartTicks)
	if err != nil || !alive {
		t.Fatalf("legacy process signalled or gone: alive=%v err=%v", alive, err)
	}
}

// The launch records the host boot epoch with the rest of the tuple, and a
// later epoch mismatch records the lost attachment as interrupted without
// signalling the process.
func TestBootEpochRecordedAtLaunchAndStaleEpochInterrupts(t *testing.T) {
	current, err := tmux.BootID()
	if err != nil {
		t.Skipf("host boot epoch unavailable on this platform: %v", err)
	}
	mgr, repo, _, projectID := newTestEnv(t, sleeperDescriptor())
	ctx := context.Background()

	s, err := mgr.Create(ctx, CreateSpec{ProjectID: projectID, Provider: "claude", WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := repo.Get(ctx, s.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.BootEpoch == nil || *got.BootEpoch != current {
		t.Fatalf("boot epoch recorded at launch = %v, want %q", got.BootEpoch, current)
	}

	recordBootEpoch(t, repo, s.ID, staleBootEpoch)
	staleRow, err := repo.Get(ctx, s.ID)
	if err != nil {
		t.Fatalf("Get stale row: %v", err)
	}
	observed, err := mgr.Observe(ctx, staleRow)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if observed.Status != StatusInterrupted {
		t.Fatalf("status = %s, want interrupted", observed.Status)
	}
	// The process is never signalled.
	alive, err := tmux.ProcAlive(*got.PanePID, *got.StartTicks)
	if err != nil || !alive {
		t.Fatalf("recorded process signalled or gone: alive=%v err=%v", alive, err)
	}
}

// A pane gone under a stale epoch means the recorded process is proven
// gone (it predates the current boot and cannot be alive), so the session
// is recorded interrupted for recovery, never parked.
func TestObserveFromPriorBootInterruptsForRecovery(t *testing.T) {
	if _, err := tmux.BootID(); err != nil {
		t.Skipf("host boot epoch unavailable on this platform: %v", err)
	}
	mgr, repo, tc, projectID := newTestEnv(t, sleeperDescriptor())
	ctx := context.Background()

	s, err := mgr.Create(ctx, CreateSpec{ProjectID: projectID, Provider: "claude", WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	recordBootEpoch(t, repo, s.ID, staleBootEpoch)
	if err := tc.KillSession(ctx, s.TmuxSessionName); err != nil {
		t.Fatalf("kill tmux session: %v", err)
	}

	staleRow, err := repo.Get(ctx, s.ID)
	if err != nil {
		t.Fatalf("Get stale row: %v", err)
	}
	observed, err := mgr.Observe(ctx, staleRow)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if observed.Status != StatusInterrupted {
		t.Fatalf("status = %s, want interrupted (recover fresh, not park)", observed.Status)
	}
}

// Recovery from a recorded death whose pane is gone proceeds when the
// recorded process is proven gone by a stale epoch (it predates the
// current boot and cannot have survived): the automatic recovery helper may
// spawn one replacement without touching the old runtime.
func TestResumeFromPriorBootRecovers(t *testing.T) {
	if _, err := tmux.BootID(); err != nil {
		t.Skipf("host boot epoch unavailable on this platform: %v", err)
	}
	mgr, repo, projectID, tc, _ := newResumeEnv(t, []string{"-c", "sleep 300; exit 0"})
	ctx := context.Background()
	s := createExplicitRecoverySource(t, mgr, projectID)

	recordBootEpoch(t, repo, s.ID, staleBootEpoch)
	if err := tc.KillSession(ctx, s.TmuxSessionName); err != nil {
		t.Fatalf("kill tmux session: %v", err)
	}

	if _, err := mgr.resume(ctx, s.ID, t.TempDir()); err != nil {
		t.Fatalf("resume: %v", err)
	}
	got, err := repo.Get(ctx, s.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Generation <= s.Generation {
		t.Fatalf("no replacement spawned (no recovery): generation = %d, want > %d", got.Generation, s.Generation)
	}
}

// A preserved dead pane whose recorded epoch predates the current boot
// cannot be the recorded workload's honest corpse — VerifyIdentity refuses
// the epoch, so the pane is treated as foreign: it is left untouched and a
// fresh successor generation is created in a new pane.
func TestResumePreservedDeadPaneFromPriorBootGetsFencedSuccessor(t *testing.T) {
	if _, err := tmux.BootID(); err != nil {
		t.Skipf("host boot epoch unavailable on this platform: %v", err)
	}
	mgr, repo, projectID, tc, _ := newResumeEnv(t, []string{"-c", "sleep 300; exit 0"})
	ctx := context.Background()
	s := createExplicitRecoverySource(t, mgr, projectID)

	recordBootEpoch(t, repo, s.ID, staleBootEpoch)

	fresh, err := mgr.resume(ctx, s.ID, t.TempDir())
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if fresh.Generation <= s.Generation {
		t.Fatalf("generation = %d, want > %d (fenced successor)", fresh.Generation, s.Generation)
	}
	// The prior-boot pane was left alone: it still carries the old options.
	info, perr := tc.PaneInfoByID(ctx, *s.TmuxPaneID)
	if perr != nil {
		t.Fatalf("pane info: %v", perr)
	}
	if info.Generation != s.Generation || info.Marker != *s.IdentityMarker {
		t.Fatalf("prior-boot pane was touched: gen=%d marker=%q", info.Generation, info.Marker)
	}
}
