//go:build darwin

package project

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

func rootAtDescriptor(dir *os.File) (*os.Root, error) {
	expected, err := dir.Stat()
	if err != nil {
		return nil, err
	}
	buffer := make([]byte, 4096)
	_, _, errno := syscall.Syscall(unix.SYS_FCNTL, dir.Fd(), unix.F_GETPATH,
		uintptr(unsafe.Pointer(&buffer[0])))
	runtime.KeepAlive(dir)
	if errno != 0 {
		return nil, errno
	}
	end := bytes.IndexByte(buffer, 0)
	if end <= 0 {
		return nil, errors.New("clone directory path is unavailable")
	}
	root, err := openProjectRoot(string(buffer[:end]), nil)
	if err != nil {
		return nil, err
	}
	actual, err := root.Stat(".")
	if err != nil || !os.SameFile(expected, actual) {
		root.Close()
		return nil, errors.New("clone directory identity changed")
	}
	return root, nil
}

func newCloneCommand(ctx context.Context, git, url, _ string, dir *os.File) (*exec.Cmd, error) {
	gitPath, err := exec.LookPath(git)
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(gitPath) {
		return nil, fmt.Errorf("clone executable must be absolute")
	}
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, self, "internal-clone-exec-v1",
		gitPath, "clone", "--progress", "--", url, ".")
	cmd.Dir = "/"
	cmd.ExtraFiles = []*os.File{dir}
	return cmd, nil
}

// RunNativeCloneExec runs only in the child process. fchdir keeps git rooted
// in the held destination even if its pathname is swapped before launch.
func RunNativeCloneExec(args []string) error {
	if len(args) == 0 || !filepath.IsAbs(args[0]) {
		return errors.New("clone executable must be absolute")
	}
	dir := os.NewFile(3, "clone-destination")
	if dir == nil {
		return errors.New("clone directory descriptor is unavailable")
	}
	defer dir.Close()
	info, err := dir.Stat()
	if err != nil || !info.IsDir() {
		return errors.New("clone directory descriptor is not a directory")
	}
	if err := unix.Fchdir(int(dir.Fd())); err != nil {
		return err
	}
	if err := dir.Close(); err != nil {
		return err
	}
	return syscall.Exec(args[0], args, os.Environ())
}
