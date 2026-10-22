//go:build linux

package tmux

import (
	"fmt"
	"os"
	"strings"
)

// BootID reads the kernel's boot epoch: the random uuid generated once per
// host boot. Process start ticks are boot-relative, so a recorded
// pid/start-ticks tuple is only meaningful while the boot it was recorded
// under IS the current one — after a host restart the same tuple can name
// an unrelated process, and this value is what proves the boot changed.
//
// An empty or unreadable boot id is an error, never a fabricated epoch:
// callers treat an unverifiable epoch as no answer in either direction.
func BootID() (string, error) {
	raw, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", fmt.Errorf("boot id: %w", err)
	}
	id := strings.TrimSpace(string(raw))
	if id == "" {
		return "", fmt.Errorf("boot id: empty")
	}
	return id, nil
}
