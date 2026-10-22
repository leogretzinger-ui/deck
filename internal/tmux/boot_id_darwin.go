//go:build darwin

package tmux

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// BootID identifies the current macOS boot from the kernel's kern.boottime
// value. Process start timestamps cannot be compared across boots without
// this epoch: a different boot time positively proves the recorded process
// cannot still exist, while a sysctl failure remains unverifiable.
func BootID() (string, error) {
	boot, err := unix.SysctlTimeval("kern.boottime")
	if err != nil {
		return "", fmt.Errorf("boot id: kern.boottime: %w", err)
	}
	if boot.Sec <= 0 || boot.Usec < 0 {
		return "", fmt.Errorf("boot id: invalid kern.boottime %d.%06d", boot.Sec, boot.Usec)
	}
	return fmt.Sprintf("darwin:%d:%06d", boot.Sec, boot.Usec), nil
}
