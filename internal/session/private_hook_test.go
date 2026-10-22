package session

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/noisefloor-oss/deck/internal/hookcapture"
	"github.com/noisefloor-oss/deck/internal/inputgate"
	"github.com/noisefloor-oss/deck/internal/lease"
	"github.com/noisefloor-oss/deck/internal/provider"
	"github.com/noisefloor-oss/deck/internal/tmux"
)

type privateHookDescriptor struct {
	*attestedResumeDescriptor
	launchFile string
	resumeFile string
}

func (d *privateHookDescriptor) captureArgs(path string) []string {
	return []string{d.executable, "-c", `printf '%s\n' "$NOISEDECK_SESSION_HOOK_URL" "$NOISEDECK_SESSION_HOOK_SOCKET" > "$1"; sleep 300; exit 0`, "capture", path}
}

func (d *privateHookDescriptor) NewSessionArgs(provider.LaunchSpec) ([]string, error) {
	return d.captureArgs(d.launchFile), nil
}

func (d *privateHookDescriptor) ResumeSessionArgs(spec provider.ResumeSpec) ([]string, error) {
	if _, err := d.attestedResumeDescriptor.ResumeSessionArgs(spec); err != nil {
		return nil, err
	}
	return d.captureArgs(d.resumeFile), nil
}

func TestPrivateHookTransportSurvivesLaunchAndResume(t *testing.T) {
	dir := t.TempDir()
	base := sleeperDescriptor()
	base.name = provider.Codex
	base.extraEnv = []string{"NOISEDECK_SESSION_HOOK_SOCKET=/untrusted/descriptor.sock"}
	desc := &privateHookDescriptor{
		attestedResumeDescriptor: &attestedResumeDescriptor{fakeDescriptor: base},
		launchFile:               filepath.Join(dir, "launch-metadata"),
		resumeFile:               filepath.Join(dir, "resume-metadata"),
	}
	mgr, repo, _, projectID := newTestEnv(t, desc)
	mgr.SetResumeDeps(lease.NewManager(repo.db), inputgate.New(), testWorkDirResolver(t))
	captures, err := hookcapture.NewService(repo, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	mgr.SetHookCaptureDeps(captures, "http://localhost")
	mgr.SetHookCaptureSocket("/data/gateway/backend.sock")
	ctx := context.Background()
	session, err := mgr.Create(ctx, CreateSpec{ProjectID: projectID, Provider: provider.Codex, WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	checkMetadata := func(path string) {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for {
			raw, err := os.ReadFile(path)
			if err == nil && strings.TrimSpace(string(raw)) == "http://localhost/api/hooks/capture\n/data/gateway/backend.sock" {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("provider did not receive the configured private capture transport: %v", err)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	checkMetadata(desc.launchFile)
	crashAndObserve(t, mgr, session)
	resumeCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	mgr.lifecycleHooks = &lifecycleTestHooks{resumeSpawned: func(*tmux.PaneInfo) { cancel() }}
	if _, err := mgr.resume(resumeCtx, session.ID, t.TempDir()); err == nil {
		t.Fatal("canceled unconfirmed resume completed")
	}
	checkMetadata(desc.resumeFile)
}
