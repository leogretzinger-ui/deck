//go:build darwin

package tmux

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDarwinProcessPathMatchesExecutable(t *testing.T) {
	want, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	want, err = filepath.EvalSymlinks(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := darwinProcessPath(os.Getpid())
	if err != nil || got != want {
		t.Fatalf("process path = %q, %v; want %q", got, err, want)
	}
	if _, err := darwinProcessPath(1<<22 + 12345); err == nil {
		t.Fatal("absent process path accepted")
	}
}

func childTicks(t *testing.T, pid int) int64 {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		p, found, err := darwinProcess(pid)
		if err == nil && found {
			return darwinStartTime(p)
		}
		if time.Now().After(deadline) {
			t.Fatalf("child process record never readable: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestDarwinProcessIdentitySupportsLiveness(t *testing.T) {
	pid := os.Getpid()
	pgid, started, executable, err := processIdentity(pid)
	if err != nil {
		t.Fatalf("processIdentity: %v", err)
	}
	if pgid <= 1 || started <= 0 || executable == "" {
		t.Fatalf("incomplete identity: pgid=%d started=%d executable=%q", pgid, started, executable)
	}
	if alive, err := ProcAlive(pid, started); err != nil || !alive {
		t.Fatalf("ProcAlive(self) = %v, %v", alive, err)
	}
	if alive, err := ProcAlive(pid, started+1); err != nil || alive {
		t.Fatalf("ProcAlive(reused tuple) = %v, %v, want false, nil", alive, err)
	}
	if alive, err := ProcAlive(1<<22+12345, started); err != nil || alive {
		t.Fatalf("ProcAlive(absent pid) = %v, %v, want false, nil", alive, err)
	}
	if got, err := ProcGroup(pid, started); err != nil || got != pgid {
		t.Fatalf("ProcGroup = %d, %v, want %d", got, err, pgid)
	}
	members, err := GroupMembers(pgid)
	if err != nil {
		t.Fatalf("GroupMembers: %v", err)
	}
	foundSelf := false
	for _, member := range members {
		if member == pid {
			foundSelf = true
			break
		}
	}
	if !foundSelf {
		t.Fatalf("GroupMembers(%d) omitted self pid %d: %v", pgid, pid, members)
	}
}

func TestDarwinBootIDIsStable(t *testing.T) {
	first, err := BootID()
	if err != nil {
		t.Fatalf("BootID: %v", err)
	}
	second, err := BootID()
	if err != nil || second != first {
		t.Fatalf("second BootID = %q, %v, want %q", second, err, first)
	}
}
