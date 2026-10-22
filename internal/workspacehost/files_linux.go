//go:build linux

package workspacehost

import (
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

func privatePath(path string, uid int, directory bool) bool {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return false
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode().Perm()&0077 != 0 || info.IsDir() != directory || (!directory && !info.Mode().IsRegular()) {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int64(stat.Uid) == int64(uid)
}

func privateDirectory(path string, uid int) bool { return privatePath(path, uid, true) }
func privateFile(path string, uid int) bool      { return privatePath(path, uid, false) }

func volatileDirectory(path string, uid int) bool {
	var stat unix.Statfs_t
	return privateDirectory(path, uid) && unix.Statfs(path, &stat) == nil && stat.Type == unix.TMPFS_MAGIC
}
