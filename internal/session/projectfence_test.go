package session

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/noisefloor-oss/deck/internal/inputgate"
	"github.com/noisefloor-oss/deck/internal/lease"
	"github.com/noisefloor-oss/deck/internal/provider"
)

func TestFenceWaitsForCreationsAlreadyAdmittedThenRefusesNewOnes(t *testing.T) {
	f := newProjectFence()
	end, err := f.admit("p")
	if err != nil {
		t.Fatal(err)
	}
	held := make(chan func())
	go func() { held <- f.close("p") }()
	select {
	case <-held:
		t.Fatal("the fence was taken while a creation was still being recorded")
	case <-time.After(50 * time.Millisecond):
	}
	end()
	var release func()
	select {
	case release = <-held:
	case <-time.After(2 * time.Second):
		t.Fatal("the fence was not taken after the creation finished")
	}
	if _, err := f.admit("p"); !errors.Is(err, ErrProjectClosing) {
		t.Fatalf("admit while closing = %v, want ErrProjectClosing", err)
	}
	if other, err := f.admit("q"); err != nil {
		t.Fatalf("another project must be unaffected: %v", err)
	} else {
		other()
	}
	release()
	again, err := f.admit("p")
	if err != nil {
		t.Fatalf("admit after release: %v", err)
	}
	again()
}

func TestManagerCreateIsRefusedInAHeldProject(t *testing.T) {
	m := NewManager(nil, nil, nil, "")
	release := m.HoldProject("p")
	defer release()
	if _, err := m.Create(t.Context(), CreateSpec{ProjectID: "p"}); !errors.Is(err, ErrProjectClosing) {
		t.Fatalf("Create = %v, want ErrProjectClosing", err)
	}
}

// A stopped session in a held project is not recovered, by the user or by
// the automatic sweep, and recovers normally once the hold is lifted.
func TestRecoveryIsRefusedInAHeldProject(t *testing.T) {
	desc := sleeperDescriptor()
	desc.contract = provider.IdentityFreshRelaunch
	mgr, repo, client, projectID := newTestEnv(t, desc)
	mgr.SetResumeDeps(lease.NewManager(repo.db), inputgate.New(), testWorkDirResolver(t))
	ctx := context.Background()
	s, err := mgr.Create(ctx, CreateSpec{ProjectID: projectID, Provider: provider.Claude, WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { _ = client.KillSession(context.Background(), s.TmuxSessionName) })
	crashAndObserve(t, mgr, s)
	before, err := repo.Get(ctx, s.ID)
	if err != nil {
		t.Fatal(err)
	}

	release := mgr.HoldProject(projectID)
	if _, _, err := mgr.Recover(ctx, s.ID); !errors.Is(err, ErrProjectClosing) {
		t.Fatalf("Recover in a held project = %v, want ErrProjectClosing", err)
	}
	if err := mgr.AutoResumeSweep(ctx); err != nil {
		t.Fatalf("the sweep must skip a held project quietly: %v", err)
	}
	held, err := repo.Get(ctx, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if held.Generation != before.Generation {
		t.Fatalf("generation %d -> %d: a process was started in a held project", before.Generation, held.Generation)
	}

	release()
	revived, _, err := mgr.Recover(ctx, s.ID)
	if err != nil {
		t.Fatalf("Recover after the hold: %v", err)
	}
	if revived.Status != StatusRunning {
		t.Fatalf("status = %s, want running", revived.Status)
	}
}
