//go:build linux

package workspaceruntime

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func (c *Controller) importRoot() (*os.Root, error) {
	fd, err := unix.Open(c.cfg.MountPoint, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, ErrUnsafeStorage
	}
	defer func() { _ = unix.Close(fd) }()
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || int(st.Uid) != c.cfg.OwnerUID || st.Mode&0077 != 0 {
		return nil, ErrUnsafeStorage
	}
	facts, err := c.kernel()
	if err != nil {
		return nil, ErrUnsafeStorage
	}
	mount, open, err := c.volumeMount(facts)
	device := fmt.Sprintf("%d:%d", unix.Major(uint64(st.Dev)), unix.Minor(uint64(st.Dev)))
	if err != nil || !open || mount.Device != device {
		return nil, ErrUnsafeStorage
	}
	// OpenRoot duplicates the already validated mount descriptor. Every copy,
	// rename and sync stays on that filesystem if its pathname is detached.
	root, err := os.OpenRoot(fmt.Sprintf("/proc/self/fd/%d", fd))
	if err != nil {
		return nil, ErrUnsafeStorage
	}
	return root, nil
}
