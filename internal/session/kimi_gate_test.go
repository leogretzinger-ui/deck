package session

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/noisefloor-oss/deck/internal/hookcapture"
	"github.com/noisefloor-oss/deck/internal/inputgate"
	"github.com/noisefloor-oss/deck/internal/lease"
	"github.com/noisefloor-oss/deck/internal/provider"
)

const kimiGateHelp = "Options:\n  -S, --session [id]  Resume exact session\n  --auto  Skip prompts\n"

// Kimi with a missing hook config launches. The hook buys auto-resume; losing
// it costs auto-resume and nothing else, so the launch proceeds and the
// session runs with a pending identity. Taking a working CLI away from the
// user to punish a file they can repair later is the failure this path
// exists to stop.
func TestCreateLaunchesKimiWithoutItsHookConfig(t *testing.T) {
	executable := kimiGateCLI(t, kimiGateHelp, false, true)
	d, err := provider.NewKimi(executable, filepath.Join(t.TempDir(), "missing.toml"), provider.HookReceiverPath)
	if err != nil {
		t.Fatalf("NewKimi: %v", err)
	}
	mgr, repo, _, projectID := newTestEnv(t, d)
	captures, err := hookcapture.NewService(repo, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	mgr.SetHookCaptureDeps(captures, "http://127.0.0.1:8837")

	created, err := mgr.Create(context.Background(), CreateSpec{
		ProjectID: projectID,
		Provider:  provider.Kimi,
		WorkDir:   t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Create refused a working kimi over its hook config: %v", err)
	}
	stored, err := repo.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != StatusRunning {
		t.Fatalf("status = %s, want running", stored.Status)
	}
	if stored.NativeSessionID != nil && *stored.NativeSessionID != "" {
		t.Fatalf("native id = %q, want pending without a provisioned hook", *stored.NativeSessionID)
	}
}

// Recovery does not consult --help. A CLI that reworded its options, or whose
// --help fails outright, still gets to try the resume: the provider's own
// answer is the authority on whether it can resume, and a stale measurement
// here would strand the user's work behind our bookkeeping.
func TestKimiRecoveryIgnoresCapabilityDrift(t *testing.T) {
	tests := []struct {
		name     string
		help     string
		failHelp bool
	}{
		{
			name: "reworded options still attempt recovery",
			help: "Options:\n  --auto  Skip prompts\n",
		},
		{
			name:     "unrunnable help still attempts recovery",
			help:     kimiGateHelp,
			failHelp: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			initial := sleeperDescriptor()
			initial.name = provider.Kimi
			mgr, repo, _, projectID := newTestEnv(t, initial)
			mgr.SetResumeDeps(lease.NewManager(repo.db), inputgate.New(), testWorkDirResolver(t))

			ctx := context.Background()
			s, err := mgr.Create(ctx, CreateSpec{
				ProjectID: projectID,
				Provider:  provider.Kimi,
				WorkDir:   t.TempDir(),
			})
			if err != nil {
				t.Fatalf("create seed session: %v", err)
			}
			crashAndObserve(t, mgr, s)
			source, err := repo.Get(ctx, s.ID)
			if err != nil {
				t.Fatalf("get recovery source: %v", err)
			}

			executable := kimiGateCLI(t, tc.help, tc.failHelp, false)
			kimi, err := provider.NewKimi(executable, filepath.Join(t.TempDir(), "missing.toml"), provider.HookReceiverPath)
			if err != nil {
				t.Fatalf("NewKimi: %v", err)
			}
			mgr.providers[provider.Kimi] = kimi

			if err := mgr.AutoResumeSweep(ctx); err != nil {
				t.Fatalf("automatic recovery sweep: %v", err)
			}
			got, getErr := repo.Get(ctx, s.ID)
			if getErr != nil {
				t.Fatalf("get session: %v", getErr)
			}
			if got.Generation != source.Generation+1 {
				t.Fatalf("generation = %d, want %d: recovery never reached the provider",
					got.Generation, source.Generation+1)
			}
			// The fake kimi sleeps for any argv it does not special-case, so a
			// recovery that actually reached the CLI is now running.
			if got.Status != StatusRunning {
				t.Fatalf("status = %s, want running: capability drift blocked recovery",
					got.Status)
			}
		})
	}
}

func kimiGateCLI(t *testing.T, help string, failHelp, rejectConfig bool) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "kimi")
	helpPath := filepath.Join(dir, "help")
	if err := os.WriteFile(helpPath, []byte(help), 0o600); err != nil {
		t.Fatalf("write Kimi help fixture: %v", err)
	}
	helpCase := "/bin/cat \"" + helpPath + "\""
	if failHelp {
		helpCase = "exit 1"
	}
	configCase := "exit 0"
	if rejectConfig {
		configCase = "exit 1"
	}
	script := "#!/bin/sh\ncase \"$1\" in\n  --version) printf '0.99.0\\n' ;;\n  --help) " + helpCase + " ;;\n  doctor) " + configCase + " ;;\n  *) sleep 300 ;;\nesac\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake Kimi: %v", err)
	}
	return path
}
