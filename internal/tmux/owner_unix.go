//go:build linux || darwin

package tmux

import (
	"fmt"
	"os"
	"syscall"
)

// verifyOwnedByRuntimeUser fails unless path is owned by the uid this
// process runs as.
//
// Mode alone is not sufficient: a 0700 directory owned by another user is
// fully accessible to that user, who can replace the socket inside it and
// drive the user's agent sessions.
func verifyOwnedByRuntimeUser(path string, info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("tmux: cannot read ownership of %q on this platform", path)
	}
	if uid := os.Getuid(); int(stat.Uid) != uid {
		return fmt.Errorf(
			"tmux: %q is owned by uid %d but this process runs as uid %d: its "+
				"owner can replace the socket and control agent sessions",
			path, stat.Uid, uid)
	}
	return nil
}
