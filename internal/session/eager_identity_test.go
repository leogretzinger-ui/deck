package session

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/noisefloor-oss/deck/internal/hookcapture"
	"github.com/noisefloor-oss/deck/internal/inputgate"
	"github.com/noisefloor-oss/deck/internal/lease"
	"github.com/noisefloor-oss/deck/internal/provider"
)

const silentKimiHookConfig = `[[hooks]]
event = "SessionStart"
matcher = "startup|resume"
command = "/usr/local/bin/noisedeck-provider-hook"
timeout = 300
`

// silentEagerCLI stays alive without calling the identity receiver.
func silentEagerCLI(t *testing.T, version string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "silentcli")
	body := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--version\" ]; then echo \"silentcli " + version + "\"; exit 0; fi\n" +
		"sleep 300; exit 0\n"
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write silent cli: %v", err)
	}
	return path
}

// A retrieved-identity provider whose eager hook never fires must still run.
// Record the live pane immediately and accept its native id when the hook
// eventually arrives.
func TestSilentEagerHookDoesNotDelayRunningSession(t *testing.T) {
	const version = "9.9.9"
	cli := silentEagerCLI(t, version)
	desc := &fakeDescriptor{
		name: provider.Kimi, executable: cli, contract: provider.IdentityRetrieved,
		graceful: syscall.SIGTERM, force: syscall.SIGKILL,
		permittedEnv: []string{"PATH", "HOME"},
		execIdentity: shellScriptIdentity(t),
		hook: &provider.IdentityHook{
			Event: "SessionStart", SessionIDField: "session_id",
			ReceiverPath: provider.HookReceiverPath,
			Timing:       provider.IdentityHookAtProcessLaunch,
			ConfigPath:   writeHookConfig(t, silentKimiHookConfig),
		},
	}
	mgr, repo, _, projectID := newTestEnv(t, desc)
	mgr.SetResumeDeps(lease.NewManager(repo.db), inputgate.New(), testWorkDirResolver(t))
	captures, err := hookcapture.NewService(repo, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	mgr.SetHookCaptureDeps(captures, "http://127.0.0.1:8837")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	started := time.Now()
	workDir := t.TempDir()
	created, err := mgr.Create(ctx, CreateSpec{
		ProjectID: projectID, Provider: provider.Kimi, WorkDir: workDir,
	})
	if err != nil {
		t.Fatalf("Create refused a live provider over a silent hook: %v", err)
	}
	t.Logf("live session returned in %s without an identity hook", time.Since(started))
	stored, err := repo.Get(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != StatusRunning {
		t.Fatalf("status = %s, want running", stored.Status)
	}
	if stored.NativeSessionID != nil && *stored.NativeSessionID != "" {
		t.Fatalf("native id = %q, want pending", *stored.NativeSessionID)
	}
	if got := mgr.RecoveryCapabilityFor(stored); got != RecoveryCapabilityNone {
		t.Fatalf("recovery capability = %v, want none while the id is pending", got)
	}

	// The pane must be the live provider, not a corpse left behind.
	info, err := mgr.tmux.PaneInfoForSession(ctx, stored.TmuxSessionName)
	if err != nil {
		t.Fatalf("observe launched pane: %v", err)
	}
	if info.Dead {
		t.Fatal("launched pane is dead: the identity wait killed a working provider")
	}

	// The grant stays live so a late hook can still publish the id.
	pending, err := repo.IdentityCapturePending(ctx, stored.ID, stored.Generation,
		*stored.IdentityMarker, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !pending {
		t.Fatal("capture grant was closed, so a late identity can never land")
	}

	var tokenHash string
	if err := repo.db.Reader().QueryRow(`SELECT token_sha256
		FROM provider_identity_capture_grants WHERE session_id = ?`, stored.ID).Scan(&tokenHash); err != nil {
		t.Fatal(err)
	}
	if err := repo.AcceptIdentityCapture(ctx, tokenHash, hookcapture.Event{
		HookEventName: "SessionStart", SessionID: "session_" + capturedCodexID,
		CWD: workDir, Source: hookcapture.ExpectedSourceStartup,
	}, time.Now()); err != nil {
		t.Fatalf("identity delivered after launch: %v", err)
	}
	delivered, err := repo.Get(ctx, stored.ID)
	if err != nil {
		t.Fatal(err)
	}
	if delivered.NativeSessionID == nil || *delivered.NativeSessionID != "session_"+capturedCodexID {
		t.Fatal("late identity was not recorded")
	}
	if delivered.Status != StatusRunning || mgr.RecoveryCapabilityFor(delivered) != RecoveryCapabilityNone {
		t.Fatal("identity delivery changed the live session into a recovery candidate")
	}
}
