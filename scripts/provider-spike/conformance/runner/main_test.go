//go:build linux

package main

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestHasIdentityRejectsPriorBootPIDReuse(t *testing.T) {
	bootID, err := currentBootID()
	if err != nil {
		t.Fatal(err)
	}
	identity, ok := identityForPID(os.Getpid(), bootID)
	if !ok {
		t.Fatal("read test process identity")
	}
	identity.BootID = differentBootID(bootID)
	if hasIdentity(identity, bootID) {
		t.Fatal("a PID/start-tick match from another boot was accepted")
	}
}

func TestCleanupRunSkipsPartialPriorBootIdentity(t *testing.T) {
	bootID, err := currentBootID()
	if err != nil {
		t.Fatal(err)
	}
	priorBootID := differentBootID(bootID)
	dir := filepath.Join(t.TempDir(), testRunName(t, priorBootID, "123"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "interrupt.identity.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cleanupRun(dir, bootID); err != nil {
		t.Fatalf("clean prior-boot run without parsing stale PID files: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("prior-boot run directory survived cleanup")
	}
}

func TestLoadRunMetadataFallsBackToBootScopedDirectoryName(t *testing.T) {
	bootID, err := currentBootID()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), testRunName(t, bootID, "456"))
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	metadata, err := loadRunMetadata(dir)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.BootID != bootID || metadata.EphemeralHome != ephemeralHomeForRun(dir) {
		t.Fatal("directory-scoped boot identity was not recovered")
	}
}

func TestRemovePriorBootRunRefusesLiveSocket(t *testing.T) {
	bootID, err := currentBootID()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("", testRunName(t, differentBootID(bootID), ""))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socketPath := filepath.Join(dir, "tmux", "ac.sock")
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	metadata := runMetadata{
		BootID:        differentBootID(bootID),
		EphemeralHome: ephemeralHomeForRun(dir),
	}
	if err := removePriorBootRun(dir, metadata); err == nil {
		t.Fatal("cleanup removed a prior-boot path with a live socket listener")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatal("refused prior-boot run directory was removed")
	}
}

func TestCleanupRunRetainsUncertifiedCurrentBootEvidence(t *testing.T) {
	bootID, err := currentBootID()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), testRunName(t, bootID, "uncertified"))
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := cleanupRun(dir, bootID); err == nil {
		t.Fatal("uncertified current-boot run was accepted")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatal("uncertified current-boot evidence was removed")
	}
}

func TestCleanupRunReapsValidIdentityDespiteInvalidSiblingMetadata(t *testing.T) {
	bootID, err := currentBootID()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), testRunName(t, bootID, "mixed-identities"))
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waitDone := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(waitDone)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-waitDone
	})
	identity, ok := identityForPID(cmd.Process.Pid, bootID)
	if !ok {
		t.Fatal("read test process identity")
	}
	data, err := json.Marshal([]processIdentity{identity})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "processes.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.identity.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cleanupRun(dir, bootID); err == nil {
		t.Fatal("uncertified run with invalid metadata was accepted")
	}
	if hasIdentity(identity, bootID) {
		t.Fatal("valid process identity survived best-effort cleanup")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatal("uncertified mixed-identity evidence was removed")
	}
}

func TestCleanupRunRemovesRevalidatedCertification(t *testing.T) {
	bootID, err := currentBootID()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), testRunName(t, bootID, "certified"))
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	metadata := runMetadata{BootID: bootID, EphemeralHome: ephemeralHomeForRun(dir)}
	data, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"run.json", "cleanup-certified.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := cleanupRun(dir, bootID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("cleanup-certified run directory survived")
	}
}

func TestStopPrivateServerRemovesSameBootRefusedSocket(t *testing.T) {
	dir, err := os.MkdirTemp("", "noisedeck-socket-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socketPath := filepath.Join(dir, "ac.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if err := stopPrivateServer(socketPath); err != nil {
		t.Fatalf("remove refused same-boot socket: %v", err)
	}
	if _, err := os.Lstat(socketPath); !os.IsNotExist(err) {
		t.Fatal("refused same-boot socket survived cleanup")
	}
}

func TestEnsurePrivateDirRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := ensurePrivateDir(link); err == nil {
		t.Fatal("private scratch validation followed a symlink")
	}
}

func TestReadPrivateFileRejectsSymlinkAndInsecureMode(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target", filepath.Join(dir, "linked.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivateFile(dir, "linked.json"); err == nil {
		t.Fatal("private metadata reader followed a symlink")
	}

	insecure := filepath.Join(dir, "insecure.json")
	if err := os.WriteFile(insecure, []byte("{}"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivateFile(dir, "insecure.json"); err == nil {
		t.Fatal("private metadata reader accepted group-readable identity data")
	}
}

func TestLoadIdentitiesRejectsSymlinkedAndInsecureMetadata(t *testing.T) {
	bootID, err := currentBootID()
	if err != nil {
		t.Fatal(err)
	}
	identity, ok := identityForPID(os.Getpid(), bootID)
	if !ok {
		t.Fatal("read test process identity")
	}
	data, err := json.Marshal(identity)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{
			name: "symlinked manifest",
			setup: func(t *testing.T, dir string) {
				t.Helper()
				target := filepath.Join(dir, "target.json")
				if err := os.WriteFile(target, []byte("[]"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("target.json", filepath.Join(dir, "processes.json")); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "symlinked tool identity",
			setup: func(t *testing.T, dir string) {
				t.Helper()
				target := filepath.Join(dir, "target.json")
				if err := os.WriteFile(target, data, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("target.json", filepath.Join(dir, "tool.identity.json")); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "insecure tool identity",
			setup: func(t *testing.T, dir string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, "tool.identity.json"), data, 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			tc.setup(t, dir)
			if _, err := loadIdentities(dir, bootID); err == nil {
				t.Fatal("unsafe process identity metadata was accepted")
			}
		})
	}
}

func TestLoadIdentitiesAcceptsPrivateMetadata(t *testing.T) {
	bootID, err := currentBootID()
	if err != nil {
		t.Fatal(err)
	}
	identity, ok := identityForPID(os.Getpid(), bootID)
	if !ok {
		t.Fatal("read test process identity")
	}
	data, err := json.Marshal([]processIdentity{identity})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "processes.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	identities, err := loadIdentities(dir, bootID)
	if err != nil {
		t.Fatal(err)
	}
	if len(identities) != 1 || identities[0] != identity {
		t.Fatalf("private identity mismatch: got=%v want=%v", identities, identity)
	}
}

func TestLoadIdentitiesReturnsValidIdentityAlongsideInvalidMetadata(t *testing.T) {
	bootID, err := currentBootID()
	if err != nil {
		t.Fatal(err)
	}
	identity, ok := identityForPID(os.Getpid(), bootID)
	if !ok {
		t.Fatal("read test process identity")
	}
	data, err := json.Marshal([]processIdentity{identity})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "processes.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.identity.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	identities, err := loadIdentities(dir, bootID)
	if err == nil {
		t.Fatal("invalid identity metadata was not reported")
	}
	if len(identities) != 1 || identities[0] != identity {
		t.Fatalf("valid identity was discarded: got=%v want=%v", identities, identity)
	}
}

func TestInheritedLockSurvivesParentExit(t *testing.T) {
	if os.Getenv("NOISEDECK_LOCK_HELPER_ROLE") != "" {
		t.Skip("orchestrator-only lock assertion")
	}
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "lock")
	readyPath := filepath.Join(dir, "ready")
	releasePath := filepath.Join(dir, "release")
	t.Cleanup(func() { _ = os.WriteFile(releasePath, nil, 0o600) })
	parent := exec.Command(os.Args[0], "-test.run=^TestRunnerLockHelper$", "-test.count=1")
	parent.Env = append(os.Environ(),
		"NOISEDECK_LOCK_HELPER_ROLE=parent", "NOISEDECK_LOCK_PATH="+lockPath,
		"NOISEDECK_LOCK_READY="+readyPath, "NOISEDECK_LOCK_RELEASE="+releasePath)
	if out, err := parent.CombinedOutput(); err != nil {
		t.Fatalf("lock-parent helper failed: %v output_bytes=%d", err, len(out))
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(readyPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("inherited-lock child did not become ready")
		}
		time.Sleep(20 * time.Millisecond)
	}
	probe, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = probe.Close() }()
	if err := syscall.Flock(int(probe.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
		t.Fatal("lock released when the parent exited while its child remained alive")
	} else if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
		t.Fatal(err)
	}
	if err := os.WriteFile(releasePath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(30 * time.Second)
	for {
		err := syscall.Flock(int(probe.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			_ = syscall.Flock(int(probe.Fd()), syscall.LOCK_UN)
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("inherited lock did not release after the child exited")
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func TestRunnerLockHelper(t *testing.T) {
	switch os.Getenv("NOISEDECK_LOCK_HELPER_ROLE") {
	case "":
		t.Skip("subprocess helper")
	case "parent":
		lock, err := os.OpenFile(os.Getenv("NOISEDECK_LOCK_PATH"), os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
			t.Fatal(err)
		}
		child := exec.Command(os.Args[0], "-test.run=^TestRunnerLockHelper$", "-test.count=1")
		child.Env = append(os.Environ(), "NOISEDECK_LOCK_HELPER_ROLE=child")
		child.ExtraFiles = []*os.File{lock}
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		return
	case "child":
		if err := os.WriteFile(os.Getenv("NOISEDECK_LOCK_READY"), []byte("ready"), 0o600); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(30 * time.Second)
		for {
			if _, err := os.Stat(os.Getenv("NOISEDECK_LOCK_RELEASE")); err == nil {
				return
			} else if !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
			if time.Now().After(deadline) {
				t.Fatal("inherited-lock child was not released")
			}
			time.Sleep(20 * time.Millisecond)
		}
	default:
		t.Fatal("unknown lock-helper role")
	}
}

func identityForPID(pid int, bootID string) (processIdentity, bool) {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return processIdentity{}, false
	}
	right := strings.LastIndexByte(string(data), ')')
	if right < 0 {
		return processIdentity{}, false
	}
	fields := strings.Fields(string(data)[right+1:])
	if len(fields) <= 19 {
		return processIdentity{}, false
	}
	ticks, err := strconv.ParseInt(fields[19], 10, 64)
	if err != nil {
		return processIdentity{}, false
	}
	return processIdentity{PID: pid, StartTicks: ticks, BootID: bootID}, true
}

func differentBootID(bootID string) string {
	const zero = "00000000-0000-0000-0000-000000000000"
	if bootID != zero {
		return zero
	}
	return "11111111-1111-1111-1111-111111111111"
}

func testRunName(t *testing.T, bootID, suffix string) string {
	t.Helper()
	raw, err := hex.DecodeString(strings.ReplaceAll(bootID, "-", ""))
	if err != nil {
		t.Fatal(err)
	}
	return "c-" + base64.RawURLEncoding.EncodeToString(raw) + "-" + suffix
}
