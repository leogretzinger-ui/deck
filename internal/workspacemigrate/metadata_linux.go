//go:build linux

package workspacemigrate

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// Refuse metadata this portable snapshot cannot reproduce. Silently flattening
// hard links or dropping ACLs/capabilities would change the migrated workspace.
func portableMetadata(root *os.Root, name string, info fs.FileInfo) error {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || info.Mode()&(fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky) != 0 || ((info.Mode().IsRegular() || info.Mode()&fs.ModeSymlink != 0) && st.Nlink != 1) {
		return ErrSnapshot
	}
	// The destination owner opens every entry, so a mode it cannot read or traverse
	// would pass a root export and then fail every import.
	if (info.IsDir() && info.Mode().Perm()&0500 != 0500) || (info.Mode().IsRegular() && info.Mode().Perm()&0400 == 0) {
		return ErrSnapshot
	}
	dir, err := root.Open(".")
	if err != nil {
		return ErrSnapshot
	}
	defer func() { _ = dir.Close() }()
	var parent unix.Stat_t
	if unix.Fstat(int(dir.Fd()), &parent) != nil || uint64(st.Dev) != uint64(parent.Dev) {
		return ErrSnapshot
	}
	// A Linux proc-fd path anchors the xattr query to the opened root. Llistxattr
	// examines the link itself; absolute provider symlinks are never followed.
	path := fmt.Sprintf("/proc/self/fd/%d/%s", dir.Fd(), name)
	n, err := unix.Llistxattr(path, nil)
	if err != nil && !errors.Is(err, unix.ENOTSUP) {
		return ErrSnapshot
	}
	if errors.Is(err, unix.ENOTSUP) {
		return nil
	}
	if n < 0 || n > 1024*1024 {
		return ErrSnapshot
	}
	attrs := make([]byte, n)
	size, err := unix.Llistxattr(path, attrs)
	if err != nil || size != n {
		return ErrSnapshot
	}
	for _, attr := range strings.Split(string(attrs), "\x00") {
		// SELinux labels belong to the destination host's mount policy. Copying
		// a Docker source label into a rootless FUSE volume would retain the wrong
		// security context. Every other unsupported attribute refuses migration.
		if attr != "" && attr != "security.selinux" {
			return ErrSnapshot
		}
	}
	return nil
}
