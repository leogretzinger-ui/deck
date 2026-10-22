//go:build !linux && !darwin

package tmux

import (
	"fmt"
	"os"
	"runtime"
)

// Unsupported hosts cannot skip socket ownership verification.
func verifyOwnedByRuntimeUser(path string, _ os.FileInfo) error {
	return fmt.Errorf(
		"tmux: socket ownership verification is not implemented on %s; cannot "+
			"establish that %q is owned by the runtime user", runtime.GOOS, path)
}
