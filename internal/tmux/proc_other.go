//go:build !linux && !darwin

package tmux

import (
	"fmt"
	"runtime"
)

// processIdentity is unavailable outside Linux and macOS.
//
// It fails rather than returning a partial tuple. A name, PID, or command
// line alone is insufficient identity, so a build that silently reconciled
// on fewer fields would be weaker than the rule while looking like it
// satisfied it. Deck targets a Linux container; this exists so a
// non-Linux build fails loudly instead of subtly.
func processIdentity(pid int) (int, int64, string, error) {
	return 0, 0, "", fmt.Errorf(
		"tmux: process identity is not implemented on %s: the identity "+
			"tuple requires process-group id and start time, which this build "+
			"cannot read", runtime.GOOS)
}

// GroupMembers fails closed for the same reason as processIdentity.
func GroupMembers(pgid int) ([]int, error) {
	return nil, fmt.Errorf(
		"tmux: process group enumeration is not implemented on %s; cannot "+
			"confirm process group %d is empty", runtime.GOOS, pgid)
}
