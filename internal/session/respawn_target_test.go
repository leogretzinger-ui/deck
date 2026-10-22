package session

import (
	"context"
	"testing"

	"github.com/noisefloor-oss/deck/internal/inputgate"
	"github.com/noisefloor-oss/deck/internal/lease"
	"github.com/noisefloor-oss/deck/internal/provider"
	"github.com/noisefloor-oss/deck/internal/tmux"
)

// A user who reclaims a dead pane for their own command keeps it.
// Marker and generation are pane OPTIONS that survive an out-of-band respawn,
// so they alone cannot tell the recorded corpse from someone else's live work;
// only the pane pid tmux retained from the exit does. Recovery must give the
// row a new home rather than respawn over a process it does not own.
func TestRecoveryWillNotRespawnOverAReclaimedPane(t *testing.T) {
	desc := sleeperDescriptor()
	desc.contract = provider.IdentityFreshRelaunch
	mgr, repo, client, projectID := newTestEnv(t, desc)
	mgr.SetResumeDeps(lease.NewManager(repo.db), inputgate.New(), testWorkDirResolver(t))

	ctx := context.Background()
	s, err := mgr.Create(ctx, CreateSpec{
		ProjectID: projectID, Provider: provider.Claude, WorkDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	crashAndObserve(t, mgr, s)
	source, err := repo.Get(ctx, s.ID)
	if err != nil {
		t.Fatal(err)
	}

	// The user reclaims the dead pane, keeping its recorded marker and
	// generation and putting a live process of their own in it.
	reclaimed, err := client.RespawnPane(ctx, *source.TmuxPaneID, tmux.SessionSpec{
		WorkDir: t.TempDir(), Argv: []string{"/bin/sh", "-c", "sleep 300"},
		Generation: source.Generation, Marker: *source.IdentityMarker,
	})
	if err != nil {
		// RespawnPane refuses to reuse the same generation and marker, so
		// stamp the user's pane through a distinct pair and restore the
		// recorded options underneath it — the state an out-of-band respawn
		// leaves behind.
		reclaimed, err = client.RespawnPane(ctx, *source.TmuxPaneID, tmux.SessionSpec{
			WorkDir: t.TempDir(), Argv: []string{"/bin/sh", "-c", "sleep 300"},
			Generation: source.Generation + 41, Marker: "operator-own-work",
		})
		if err != nil {
			t.Fatalf("simulate user respawn: %v", err)
		}
		if err := client.SetPaneOption(ctx, reclaimed.PaneID,
			tmux.OptMarker, *source.IdentityMarker); err != nil {
			t.Fatal(err)
		}
		if err := client.SetPaneOption(ctx, reclaimed.PaneID,
			tmux.OptGeneration, "1"); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_ = client.KillSession(context.Background(), source.TmuxSessionName)
	})

	if pane := mgr.reusableRecoveryPane(ctx, source); pane != nil {
		t.Fatalf("recovery selected a reclaimed live pane as a respawn target: %+v", pane)
	}

	// Recovery still succeeds — it simply does not do it here.
	revived, _, err := mgr.Recover(ctx, s.ID)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if revived.Status != StatusRunning {
		t.Fatalf("status = %s, want running", revived.Status)
	}
	if revived.TmuxPaneID != nil && *revived.TmuxPaneID == reclaimed.PaneID {
		t.Fatal("recovery took over the user's pane")
	}
	after, err := client.PaneInfoByID(ctx, reclaimed.PaneID)
	if err != nil {
		t.Fatalf("observe the user's pane: %v", err)
	}
	if after.Dead || after.PID != reclaimed.PID {
		t.Fatalf("user's live process was destroyed: dead=%v pid=%d want %d",
			after.Dead, after.PID, reclaimed.PID)
	}
}

// The convergence sweep must not adopt a process a user put in the
// candidate's pane. Marker, generation, and the launch marker all survive an
// out-of-band respawn, so they cannot tell this recovery's spawn from a later
// occupant; only the slot's recorded pid can. Adopting the wrong process would
// write its pid into the row, after which every identity check defends it
// perfectly — all the way to kill(-pgid).
func TestConvergenceWillNotAdoptAReclaimedCandidatePane(t *testing.T) {
	slot := &SuccessorSlot{
		SessionID: "s-1", CandidateGeneration: 2,
		CandidateIdentityMarker: "candidate-marker",
	}
	spawned := &tmux.PaneInfo{
		SessionName: "actest-s1", Generation: 2, Marker: "candidate-marker",
		LaunchMarker: "candidate-marker", PID: 4242, ProcessGroupID: 4242,
		StartTicks: 99,
	}
	// Nothing recorded yet: the slot proves nothing and adopts nothing.
	if err := verifySuccessorPane(slot, "actest-s1", spawned); err == nil {
		t.Fatal("a slot with no recorded process adopted a pane")
	}

	pid, pgid, ticks := 4242, 4242, int64(99)
	slot.CandidatePanePID, slot.CandidateProcessGroupID = &pid, &pgid
	slot.CandidateStartTicks = &ticks
	if err := verifySuccessorPane(slot, "actest-s1", spawned); err != nil {
		t.Fatalf("the recorded spawn was refused: %v", err)
	}

	// The user reclaims the pane: identical marker, generation and launch
	// marker, different process.
	reclaimed := *spawned
	reclaimed.PID, reclaimed.ProcessGroupID, reclaimed.StartTicks = 5151, 5151, 777
	if err := verifySuccessorPane(slot, "actest-s1", &reclaimed); err == nil {
		t.Fatal("convergence adopted a user's process as the successor")
	}

	// A pid reused with different start ticks is also a different process.
	recycled := *spawned
	recycled.StartTicks = 12345
	if err := verifySuccessorPane(slot, "actest-s1", &recycled); err == nil {
		t.Fatal("convergence adopted a recycled pid as the successor")
	}
}
