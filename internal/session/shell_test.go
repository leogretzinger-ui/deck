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

func mustShellDescriptor(t *testing.T) *provider.ShellDescriptor {
	t.Helper()
	d, err := provider.NewShell("/bin/bash")
	if err != nil {
		t.Fatalf("NewShell: %v", err)
	}
	return d
}

// A shell session launches with NO identity machinery — no assigned
// id in argv, no launch gate, no capture env — and the row records exactly
// that: provider shell, NULL native_session_id. The manager here has no
// capture pipeline wired, which a retrieved-identity provider would refuse;
// identity-none must not.
func TestCreateShellRecordsNoIdentity(t *testing.T) {
	mgr, repo, _, projectID := newTestEnv(t, mustShellDescriptor(t))
	ctx := context.Background()

	s, err := mgr.Create(ctx, CreateSpec{ProjectID: projectID, Provider: "shell", WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if s.Provider != "shell" {
		t.Errorf("provider = %q, want shell", s.Provider)
	}
	if s.NativeSessionID != nil {
		t.Errorf("native session id = %q, want NULL for an identity-none provider", *s.NativeSessionID)
	}

	got, err := repo.Get(ctx, s.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.NativeSessionID != nil {
		t.Errorf("recorded native session id = %q, want NULL", *got.NativeSessionID)
	}
}

// A shell's NULL native identity never creates a capture grant.
func TestBootReconcileLeavesShellSessionsAlone(t *testing.T) {
	mgr, repo, _, projectID := newTestEnv(t, mustShellDescriptor(t))
	ctx := context.Background()

	s, err := mgr.Create(ctx, CreateSpec{ProjectID: projectID, Provider: "shell", WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := repo.Get(ctx, s.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	// The session may have advanced to running (the pane is live); what it
	// must never be is needs_attention over an identity it does not have.
	if got.Status != StatusStarting && got.Status != StatusRunning {
		t.Errorf("status = %s, want starting/running — reconciliation swept an identity-none session", got.Status)
	}
}

// Recovery of an identity-none session is a fresh
// RELAUNCH, never a resume. It takes the same gated path — the same lease
// invalidation, the same marker round-trip — and differs only in building
// a launch argv where a resumable provider builds a resume argv.
func TestResumeShellRelaunchesIdentityNone(t *testing.T) {
	mgr, repo, _, projectID := newTestEnv(t, mustShellDescriptor(t))
	mgr.SetResumeDeps(lease.NewManager(repo.db), inputgate.New(), testWorkDirResolver(t))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s, err := mgr.Create(ctx, CreateSpec{ProjectID: projectID, Provider: "shell", WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() {
		c, cc := context.WithTimeout(context.Background(), 5*time.Second)
		defer cc()
		_ = mgr.Stop(c, s.ID, true)
	})
	dead := crashShellAndSettle(t, mgr, repo, s)

	got, err := mgr.resume(ctx, s.ID, t.TempDir())
	if err != nil {
		t.Fatalf("relaunch: %v", err)
	}
	if got.Generation != dead.Generation+1 {
		t.Fatalf("generation = %d, want %d", got.Generation, dead.Generation+1)
	}
	after, err := repo.Get(ctx, s.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if after.Status != StatusRunning {
		t.Fatalf("status = %s, want running", after.Status)
	}
	// A relaunch never invents the identity the provider does not have.
	if after.NativeSessionID != nil {
		t.Errorf("native session id = %q, want none", *after.NativeSessionID)
	}
}

// A removed session is never recovered, whatever its provider: its pane
// and scrollback were deliberately destroyed. The refusal is the same one
// a resumable provider gets.
func TestResumeShellRefusesRemoved(t *testing.T) {
	mgr, repo, _, projectID := newTestEnv(t, mustShellDescriptor(t))
	mgr.SetResumeDeps(lease.NewManager(repo.db), inputgate.New(), testWorkDirResolver(t))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	s, err := mgr.Create(ctx, CreateSpec{ProjectID: projectID, Provider: "shell", WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := mgr.Remove(ctx, s.ID); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := mgr.resume(ctx, s.ID, t.TempDir()); !errors.Is(err, ErrNotResumable) {
		t.Fatalf("resume error = %v, want ErrNotResumable", err)
	}
}
