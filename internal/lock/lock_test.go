package lock

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// helperEnv makes the test binary re-exec as a lock-holding child process.
// Real two-process contention needs a real second process; everything else
// is covered in-process because flock excludes across separate descriptions
// even within one process (measured; see doc.go).
const helperEnv = "NOISEDECK_LOCK_TEST_HOLD"

func TestMain(m *testing.M) {
	if path := os.Getenv(helperEnv); path != "" {
		runHolder(path)
		return
	}
	os.Exit(m.Run())
}

// runHolder acquires the lock, reports on stdout, and blocks until killed.
func runHolder(path string) {
	l, err := Acquire(path)
	if err != nil {
		fmt.Println("acquire failed:", err)
		os.Exit(2)
	}
	defer func() { _ = l.Release() }()
	fmt.Println("held")
	// Block until killed. A bare select{} would trip the runtime's deadlock
	// detector and exit, silently releasing the lock mid-test.
	for {
		time.Sleep(time.Hour)
	}
}

func lockPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "backend.lock")
}

// startHolder spawns a child process that holds the lock at path, and
// returns once the child has confirmed acquisition.
func startHolder(t *testing.T, path string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), helperEnv+"="+path)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start holder: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "held" {
		t.Fatalf("holder did not confirm acquisition: line=%q err=%v", line, err)
	}
	return cmd
}

func TestAcquireReleaseReacquire(t *testing.T) {
	path := lockPath(t)

	l, err := Acquire(path)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if l.Path() != path {
		t.Fatalf("Path() = %q, want %q", l.Path(), path)
	}
	if err := l.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}

	l2, err := Acquire(path)
	if err != nil {
		t.Fatalf("reacquire after release: %v", err)
	}
	if err := l2.Release(); err != nil {
		t.Fatalf("second release: %v", err)
	}
}

func TestSecondAcquireFailsWhileHeld(t *testing.T) {
	path := lockPath(t)

	l, err := Acquire(path)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer func() { _ = l.Release() }()

	if _, err := Acquire(path); !errors.Is(err, ErrHeld) {
		t.Fatalf("second acquire: got %v, want ErrHeld", err)
	}

	if err := l.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	l3, err := Acquire(path)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	_ = l3.Release()
}

func TestReleaseIdempotent(t *testing.T) {
	l, err := Acquire(lockPath(t))
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := l.Release(); err != nil {
		t.Fatalf("first release: %v", err)
	}
	if err := l.Release(); err != nil {
		t.Fatalf("second release should be a no-op, got: %v", err)
	}
}

// The PID in the file is a diagnostic hint for a human, not an input; this
// asserts the hint is written and current, and is the only place anything is
// permitted to read it.
func TestAcquireWritesPIDHint(t *testing.T) {
	path := lockPath(t)
	l, err := Acquire(path)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer func() { _ = l.Release() }()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read lock file: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("lock file content %q is not a pid: %v", raw, err)
	}
	if pid != os.Getpid() {
		t.Fatalf("lock file pid = %d, want %d", pid, os.Getpid())
	}
}

func TestAcquireMissingDirectoryFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "no-such-dir", "backend.lock")
	if _, err := Acquire(path); err == nil {
		t.Fatal("acquire in a missing directory should fail")
	}
}

func TestReleaseKeepsLockFile(t *testing.T) {
	path := lockPath(t)
	l, err := Acquire(path)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := l.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("lock file must survive release (see doc.go): %v", err)
	}
}

func TestHeldByAnotherProcess(t *testing.T) {
	path := lockPath(t)
	holder := startHolder(t, path)

	if _, err := Acquire(path); !errors.Is(err, ErrHeld) {
		t.Fatalf("acquire against live holder: got %v, want ErrHeld", err)
	}

	// Once the holder is gone, the next backend acquires.
	_ = holder.Process.Kill()
	_, _ = holder.Process.Wait()
	l, err := Acquire(path)
	if err != nil {
		t.Fatalf("acquire after holder exit: %v", err)
	}
	_ = l.Release()
}

// The property the whole design rests on: a SIGKILLed holder — no userspace
// cleanup of any kind — leaves no stale lock behind.
func TestKilledHolderLeavesNoStaleLock(t *testing.T) {
	path := lockPath(t)
	holder := startHolder(t, path)

	if err := holder.Process.Kill(); err != nil {
		t.Fatalf("kill holder: %v", err)
	}
	if _, err := holder.Process.Wait(); err != nil {
		t.Fatalf("wait holder: %v", err)
	}

	// Wait() has reaped the child, so its descriptors are already gone. The
	// very next acquire must succeed with no grace period and no retry — a
	// retry loop here would mask exactly the property this test exists to
	// pin down.
	l, err := Acquire(path)
	if err != nil {
		t.Fatalf("acquire immediately after SIGKILL of holder: %v", err)
	}
	_ = l.Release()
}
