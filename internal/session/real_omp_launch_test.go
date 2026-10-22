package session

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/noisefloor-oss/deck/internal/hookcapture"
	"github.com/noisefloor-oss/deck/internal/provider"
)

type isolatedOmpDescriptor struct {
	*provider.OmpDescriptor
	testHome string
}

func (d *isolatedOmpDescriptor) Environment() []string {
	return append(d.OmpDescriptor.Environment(), "HOME="+d.testHome,
		"XDG_DATA_HOME="+d.testHome, "XDG_STATE_HOME="+d.testHome,
		"XDG_CACHE_HOME="+d.testHome)
}

// Run explicitly against the installed CLI; no credentials or prompt are needed.
func TestRealOmpLaunchWithoutIdentityWait(t *testing.T) {
	if os.Getenv("NOISEDECK_REAL_OMP_LAUNCH") != "1" {
		t.Skip("set NOISEDECK_REAL_OMP_LAUNCH=1 to exercise the installed omp CLI")
	}
	cli, err := exec.LookPath("omp")
	if err != nil {
		t.Fatal(err)
	}
	testHome := t.TempDir()
	hook := filepath.Join(testHome, "silent-hook.ts")
	if err := os.WriteFile(hook, []byte("export default function () {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	desc, err := provider.NewOmp(cli, hook)
	if err != nil {
		t.Fatal(err)
	}
	mgr, repo, tc, projectID := newTestEnv(t, &isolatedOmpDescriptor{desc, testHome})
	captures, err := hookcapture.NewService(repo, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	mgr.SetHookCaptureDeps(captures, "http://127.0.0.1:1")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	started := time.Now()
	created, err := mgr.Create(ctx, CreateSpec{ProjectID: projectID, Provider: provider.Omp, WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("real omp process attached in %s with identity hook withheld", time.Since(started))
	if created.Status != StatusRunning || created.NativeSessionID != nil {
		t.Fatal("expected a running session with native identity pending")
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer stopCancel()
	info, err := tc.PaneInfoByID(stopCtx, *created.TmuxPaneID)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyIdentity(created, info); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(-*created.ProcessGroupID, syscall.SIGKILL); err != nil {
		t.Fatalf("interrupt real omp process group: %v", err)
	}
	for {
		info, err = tc.PaneInfoByID(stopCtx, *created.TmuxPaneID)
		if err == nil && info.Dead {
			break
		}
		select {
		case <-stopCtx.Done():
			t.Fatalf("real omp interruption was not observed: %v", err)
		case <-time.After(20 * time.Millisecond):
		}
	}
}
