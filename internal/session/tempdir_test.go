package session

import (
	"bytes"
	"context"
	"errors"
	"github.com/noisefloor-oss/deck/internal/provider"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestGenerationTemporaryPathLeavesUnixSocketBudget(t *testing.T) {
	name := generationTempName("session-id", 1, "identity-marker")
	if !validGenerationTempName(name) {
		t.Fatalf("generated name %q is not valid", name)
	}
	path := filepath.Join("/tmp", "noisedeck-sessions",
		strings.Repeat("a", temporarySocketDigestSize*2), name)
	if len(path) > 72 {
		t.Fatalf("generation temporary path length = %d, want at most 72", len(path))
	}
}

func TestGenerationTemporaryDirectoryIsInheritedAndReclaimed(t *testing.T) {
	t.Setenv("NOISEDECK_HOOK_DIAGNOSTICS", "true")
	mgr, repo, _, projectID := newTestEnv(t, sleeperDescriptor())
	base := t.TempDir()
	if err := mgr.SetTemporaryRoot(base); err != nil {
		t.Fatalf("SetTemporaryRoot: %v", err)
	}
	s, err := mgr.Create(context.Background(), CreateSpec{
		ProjectID: projectID, Provider: "claude", WorkDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if s.IdentityMarker == nil || s.PanePID == nil {
		t.Fatalf("running tuple = marker %v pid %v", s.IdentityMarker, s.PanePID)
	}
	tempDir := filepath.Join(mgr.tempRoot,
		generationTempName(s.ID, s.Generation, *s.IdentityMarker))
	info, err := os.Lstat(tempDir)
	if err != nil {
		t.Fatalf("generation temporary directory: %v", err)
	}
	if !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("generation temporary directory mode = %v", info.Mode())
	}
	environ, err := os.ReadFile("/proc/" + strconv.Itoa(*s.PanePID) + "/environ")
	if err != nil {
		t.Fatalf("read provider environment: %v", err)
	}
	if !bytes.Contains(environ, []byte("TMPDIR="+tempDir+"\x00")) {
		t.Fatalf("provider environment does not contain its exact TMPDIR")
	}
	if !bytes.Contains(environ, []byte("NOISEDECK_HOOK_DIAGNOSTICS=true\x00")) {
		t.Fatal("private launch did not inherit the explicit hook diagnostics opt-in")
	}
	restarted := NewManager(repo, mgr.tmux, mgr.providers, mgr.prefix)
	if err := restarted.SetTemporaryRoot(base); err != nil {
		t.Fatalf("restarted SetTemporaryRoot: %v", err)
	}
	if err := restarted.SweepTemporaryDirs(context.Background()); err != nil {
		t.Fatalf("SweepTemporaryDirs while live: %v", err)
	}
	if _, err := os.Stat(tempDir); err != nil {
		t.Fatalf("live generation temporary directory was reclaimed: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := mgr.Stop(ctx, s.ID, true); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := mgr.SweepTemporaryDirs(context.Background()); err != nil {
		t.Fatalf("SweepTemporaryDirs after stop: %v", err)
	}
	if _, err := os.Lstat(tempDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stopped generation temporary directory remains: %v", err)
	}
}

func TestHookDiagnosticsRequiresExplicitOptInAndPrivateLaunchDirectory(t *testing.T) {
	for _, value := range []string{"", "false", "1", "TRUE", "true"} {
		t.Setenv("NOISEDECK_HOOK_DIAGNOSTICS", value)
		for _, tempDir := range []string{"", "/private-generation"} {
			want := "NOISEDECK_HOOK_DIAGNOSTICS=false"
			if value == "true" && tempDir != "" {
				want = "NOISEDECK_HOOK_DIAGNOSTICS=true"
			}
			got := hookDiagnosticsEnvironment(tempDir)
			if len(got) != 1 || got[0] != want {
				t.Fatalf("diagnostics environment = %v, want %q", got, want)
			}
			desc := &fakeDescriptor{permittedEnv: []string{"NOISEDECK_HOOK_DIAGNOSTICS"},
				extraEnv: []string{"NOISEDECK_HOOK_DIAGNOSTICS=descriptor"}}
			argv := launchArgv(desc, []string{"provider"}, "xterm", got)
			var assignments []string
			for _, arg := range argv {
				if strings.HasPrefix(arg, "NOISEDECK_HOOK_DIAGNOSTICS=") {
					assignments = append(assignments, arg)
				}
			}
			if len(assignments) != 1 || assignments[0] != want {
				t.Fatalf("launch diagnostics assignments = %v", assignments)
			}
		}
	}
}

func TestFailedSpawnLeavesNoGenerationTemporaryDirectory(t *testing.T) {
	mgr, _, _, projectID := newTestEnv(t, sleeperDescriptor())
	if err := mgr.SetTemporaryRoot(t.TempDir()); err != nil {
		t.Fatalf("SetTemporaryRoot: %v", err)
	}
	missing := filepath.Join(t.TempDir(), "missing")
	if _, err := mgr.Create(context.Background(), CreateSpec{
		ProjectID: projectID, Provider: "claude", WorkDir: missing,
	}); err == nil {
		t.Fatal("Create with missing working directory succeeded")
	}
	if err := mgr.SweepTemporaryDirs(context.Background()); err != nil {
		t.Fatalf("SweepTemporaryDirs: %v", err)
	}
	entries, err := os.ReadDir(mgr.tempRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if validGenerationTempName(entry.Name()) {
			t.Fatalf("failed spawn left generation temporary directory %q", entry.Name())
		}
	}
}

func TestTemporarySweepTouchesOnlyDeckOwnedNamesAndDoesNotFollowSymlinks(t *testing.T) {
	mgr, _, _, _ := newTestEnv(t, sleeperDescriptor())
	if err := mgr.SetTemporaryRoot(t.TempDir()); err != nil {
		t.Fatalf("SetTemporaryRoot: %v", err)
	}
	stale := filepath.Join(mgr.tempRoot, generationTempName("stale", 1, "marker"))
	if err := os.Mkdir(stale, 0o700); err != nil {
		t.Fatal(err)
	}
	operatorDir := filepath.Join(mgr.tempRoot, "operator-owned")
	if err := os.Mkdir(operatorDir, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	sentinel := filepath.Join(outside, "sentinel")
	if err := os.WriteFile(sentinel, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(mgr.tempRoot, generationTempName("linked", 2, "marker"))
	if err := os.Symlink(outside, linked); err != nil {
		t.Fatal(err)
	}
	if err := mgr.SweepTemporaryDirs(context.Background()); err != nil {
		t.Fatalf("SweepTemporaryDirs: %v", err)
	}
	for _, removed := range []string{stale, linked} {
		if _, err := os.Lstat(removed); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("Deck-owned entry %q remains: %v", removed, err)
		}
	}
	if _, err := os.Stat(operatorDir); err != nil {
		t.Errorf("unrecognized entry was touched: %v", err)
	}
	if got, err := os.ReadFile(sentinel); err != nil || string(got) != "preserve" {
		t.Errorf("symlink target changed: contents=%q err=%v", got, err)
	}
}

func TestTemporarySweepRefusesChangedRoot(t *testing.T) {
	mgr, _, _, _ := newTestEnv(t, sleeperDescriptor())
	if err := mgr.SetTemporaryRoot(t.TempDir()); err != nil {
		t.Fatalf("SetTemporaryRoot: %v", err)
	}
	realRoot := mgr.tempRoot + ".real"
	if err := os.Rename(mgr.tempRoot, realRoot); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, mgr.tempRoot); err != nil {
		t.Fatal(err)
	}
	if err := mgr.SweepTemporaryDirs(context.Background()); err == nil {
		t.Fatal("SweepTemporaryDirs accepted a substituted root")
	}
}

func TestTemporaryRootRefusesSymlinkedAnchor(t *testing.T) {
	mgr, _, _, _ := newTestEnv(t, sleeperDescriptor())
	base := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(base, "noisedeck-sessions")); err != nil {
		t.Fatal(err)
	}
	if err := mgr.SetTemporaryRoot(base); err == nil {
		t.Fatal("SetTemporaryRoot accepted a symlinked Deck anchor")
	}
}

func TestGenerationTemporaryDirectoryOverridesAmbientProviderTMPDIR(t *testing.T) {
	t.Setenv("TMPDIR", "/ambient")
	desc := sleeperDescriptor()
	desc.permittedEnv = append(desc.permittedEnv, "TMPDIR")
	desc.extraEnv = append(desc.extraEnv, "TMPDIR=/descriptor")
	argv := launchArgv(desc, []string{"/bin/true"}, "xterm-256color",
		[]string{"TMPDIR=/generation"})
	var assignments []string
	for _, arg := range argv {
		if strings.HasPrefix(arg, "TMPDIR=") {
			assignments = append(assignments, arg)
		}
	}
	if len(assignments) != 1 || assignments[0] != "TMPDIR=/generation" {
		t.Fatalf("TMPDIR assignments = %q, want exact generation override", assignments)
	}
}

func TestTemporarySweepPreservesProvisionalSuccessorSlot(t *testing.T) {
	mgr, repo, projectID, _, _ := newResumeEnv(t, []string{"-c", "sleep 300"})
	if err := mgr.SetTemporaryRoot(t.TempDir()); err != nil {
		t.Fatalf("SetTemporaryRoot: %v", err)
	}
	source := createInterrupted(t, mgr, projectID)
	marker, err := newUUIDv4()
	if err != nil {
		t.Fatal(err)
	}
	slot, err := repo.BeginSuccessorSlot(context.Background(), source, marker, nil, "")
	if err != nil {
		t.Fatalf("BeginSuccessorSlot: %v", err)
	}
	tempDir, err := mgr.prepareGenerationTemp(slot.SessionID, slot.CandidateGeneration,
		slot.CandidateIdentityMarker)
	if err != nil {
		t.Fatalf("prepareGenerationTemp: %v", err)
	}
	if err := mgr.SweepTemporaryDirs(context.Background()); err != nil {
		t.Fatalf("SweepTemporaryDirs with slot: %v", err)
	}
	if _, err := os.Stat(tempDir); err != nil {
		t.Fatalf("provisional successor temporary directory was reclaimed: %v", err)
	}
	retired, err := repo.RetireSuccessorSlot(context.Background(), slot, "test retirement")
	if err != nil || !retired {
		t.Fatalf("RetireSuccessorSlot: retired=%v err=%v", retired, err)
	}
	if err := mgr.SweepTemporaryDirs(context.Background()); err != nil {
		t.Fatalf("SweepTemporaryDirs after retirement: %v", err)
	}
	if _, err := os.Lstat(tempDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retired successor temporary directory remains: %v", err)
	}
}

func TestRecoveredGenerationInheritsItsOwnTemporaryDirectory(t *testing.T) {
	t.Setenv("NOISEDECK_HOOK_DIAGNOSTICS", "true")
	mgr, _, projectID, _, _ := newResumeEnv(t, []string{"-c", "sleep 300"})
	if err := mgr.SetTemporaryRoot(t.TempDir()); err != nil {
		t.Fatalf("SetTemporaryRoot: %v", err)
	}
	source := createInterrupted(t, mgr, projectID)
	mgr.SetHarnessLoginEnvironment(func(context.Context, provider.Descriptor) []string {
		return []string{"NOISEDECK_LOGIN_SOCKET=/fixture/recovered.sock"}
	})
	recovered, err := mgr.recover(context.Background(), source.ID, "", false)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if recovered.Generation != source.Generation+1 || recovered.IdentityMarker == nil ||
		recovered.PanePID == nil {
		t.Fatalf("recovered tuple = generation %d marker %v pid %v",
			recovered.Generation, recovered.IdentityMarker, recovered.PanePID)
	}
	tempDir := filepath.Join(mgr.tempRoot, generationTempName(recovered.ID,
		recovered.Generation, *recovered.IdentityMarker))
	environ, err := os.ReadFile("/proc/" + strconv.Itoa(*recovered.PanePID) + "/environ")
	if err != nil {
		t.Fatalf("read recovered provider environment: %v", err)
	}
	if !bytes.Contains(environ, []byte("TMPDIR="+tempDir+"\x00")) {
		t.Fatal("recovered provider environment does not contain its generation TMPDIR")
	}
	if !bytes.Contains(environ, []byte("NOISEDECK_HOOK_DIAGNOSTICS=true\x00")) {
		t.Fatal("recovered provider did not inherit the explicit hook diagnostics opt-in")
	}
	if !bytes.Contains(environ, []byte("NOISEDECK_LOGIN_SOCKET=/fixture/recovered.sock\x00")) {
		t.Fatal("recovered native CLI missing browser registration environment")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := mgr.Stop(ctx, recovered.ID, true); err != nil {
		t.Fatalf("Stop recovered generation: %v", err)
	}
}

func TestTemporarySweepKeepsConcurrentLiveGenerationsIndependent(t *testing.T) {
	mgr, _, _, projectID := newTestEnv(t, sleeperDescriptor())
	if err := mgr.SetTemporaryRoot(t.TempDir()); err != nil {
		t.Fatalf("SetTemporaryRoot: %v", err)
	}
	var sessions []*Session
	var paths []string
	for range 2 {
		s, err := mgr.Create(context.Background(), CreateSpec{
			ProjectID: projectID, Provider: "claude", WorkDir: t.TempDir(),
		})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		sessions = append(sessions, s)
		paths = append(paths, filepath.Join(mgr.tempRoot,
			generationTempName(s.ID, s.Generation, *s.IdentityMarker)))
	}
	if paths[0] == paths[1] {
		t.Fatalf("concurrent generations share temporary directory %q", paths[0])
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := mgr.Stop(ctx, sessions[0].ID, true); err != nil {
		t.Fatalf("Stop first generation: %v", err)
	}
	if err := mgr.SweepTemporaryDirs(context.Background()); err != nil {
		t.Fatalf("SweepTemporaryDirs: %v", err)
	}
	if _, err := os.Lstat(paths[0]); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stopped generation directory remains: %v", err)
	}
	if _, err := os.Stat(paths[1]); err != nil {
		t.Errorf("concurrent live generation directory was reclaimed: %v", err)
	}
	if err := mgr.Stop(ctx, sessions[1].ID, true); err != nil {
		t.Fatalf("Stop second generation: %v", err)
	}
}
