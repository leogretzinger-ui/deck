package lock

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"syscall"
)

// ErrHeld reports that the lock is already held — by another backend, or by
// this process acquiring twice. Callers match it with errors.Is.
var ErrHeld = errors.New("already held by a running backend")

// lockFileMode is owner-only, matching the control database the lock guards.
// It applies only when Acquire creates the file.
const lockFileMode os.FileMode = 0o600

// ExclusiveLock is a held backend lock. The zero value is not usable; obtain
// one from Acquire and keep it for the life of the process.
type ExclusiveLock struct {
	path string
	mu   sync.Mutex
	file *os.File
}

// Acquire takes the exclusive backend lock at path, creating the file if
// needed, and fails immediately with ErrHeld if any process — including this
// one — already holds it. It never blocks: the second backend's job is to
// exit loudly, not to queue.
func Acquire(path string) (*ExclusiveLock, error) {
	// #nosec G304 -- the path comes from the backend's own configuration,
	// never from a request; opening the configured lock file is the
	// entire job of this function.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, lockFileMode)
	if err != nil {
		return nil, fmt.Errorf("lock: open %q: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("lock: %q: %w", path, ErrHeld)
		}
		return nil, fmt.Errorf("lock: flock %q: %w", path, err)
	}

	// Diagnostic only, written while holding the lock. The kernel owns
	// liveness; nothing reads this back (see doc.go).
	if err := writePIDHint(f); err != nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
		return nil, fmt.Errorf("lock: write pid hint to %q: %w", path, err)
	}

	return &ExclusiveLock{path: path, file: f}, nil
}

// writePIDHint replaces the file's content with this process's PID. A write
// failure here means the directory holding the control state is sick, which
// startup must surface rather than continue past.
func writePIDHint(f *os.File) error {
	if err := f.Truncate(0); err != nil {
		return err
	}
	if _, err := f.Seek(0, 0); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(f, "%d\n", os.Getpid()); err != nil {
		return err
	}
	return f.Sync()
}

// Path reports the lock file's path, for logs and readiness diagnostics.
func (l *ExclusiveLock) Path() string { return l.path }

// IsHeld reports whether this handle still owns its acquired lock. The kernel
// remains the authority for exclusivity; this is the readiness check that the
// backend has not released its own lifetime guard.
func (l *ExclusiveLock) IsHeld() bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.file != nil
}

// Release drops the lock and closes the file. It is idempotent, and it
// deliberately leaves the lock file in place — see doc.go for why unlinking
// here would let two backends hold locks on different inodes of the same
// path.
func (l *ExclusiveLock) Release() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	f := l.file
	l.file = nil
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != nil {
		_ = f.Close()
		return fmt.Errorf("lock: unlock %q: %w", l.path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("lock: close %q: %w", l.path, err)
	}
	return nil
}
