//go:build linux

package tmux

import (
	"fmt"
	"io/fs"
	"syscall"
	"testing"
)

// Match processStatIdentity: a wrapped os.ReadFile PathError.
func statReadError(op string, errno syscall.Errno) error {
	path := "/proc/4242/stat"
	return fmt.Errorf("tmux: read %s: %w", path, &fs.PathError{Op: op, Path: path, Err: errno})
}

func TestVanishedDuringScanAcceptsOnlyPositiveAbsence(t *testing.T) {
	cases := []struct {
		name string
		err  error
		skip bool
	}{
		{
			// A process reaped after the stat file was opened: the kernel
			// answers the read itself with "no such process".
			name: "reaped between open and read",
			err:  statReadError("read", syscall.ESRCH),
			skip: true,
		},
		{
			name: "entry gone before the open",
			err:  statReadError("open", syscall.ENOENT),
			skip: true,
		},
		{
			name: "reaped under the enumeration wrap",
			err: fmt.Errorf("tmux: inspect process 4242 while enumerating group 99: %w",
				statReadError("read", syscall.ESRCH)),
			skip: true,
		},
		{
			name: "permission denied",
			err:  statReadError("open", syscall.EACCES),
			skip: false,
		},
		{
			name: "malformed stat line",
			err:  fmt.Errorf("tmux: malformed /proc/4242/stat"),
			skip: false,
		},
		{
			name: "unexpected procfs failure",
			err:  statReadError("read", syscall.EIO),
			skip: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := vanishedDuringScan(tc.err); got != tc.skip {
				t.Fatalf("vanishedDuringScan(%v) = %v, want skip=%v", tc.err, got, tc.skip)
			}
		})
	}
}
