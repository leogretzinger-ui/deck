//go:build darwin

package tasks

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

const nativeTaskExecCommand = "internal-task-exec-v1"

func newTaskCommand(ctx context.Context, executable string, root *taskRoot, args ...string) (*exec.Cmd, error) {
	if !filepath.IsAbs(executable) || filepath.Clean(executable) != executable {
		return nil, errors.New("Tasks executable must be an absolute canonical path")
	}
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	argv := append([]string{nativeTaskExecCommand, executable}, args...)
	cmd := exec.CommandContext(ctx, self, argv...)
	cmd.Dir = "/"
	cmd.ExtraFiles = []*os.File{root.anchor}
	return cmd, nil
}

// RunNativeTaskExec binds the engine's cwd in the child only. The parent
// retains its cwd, and a renamed project never redirects engine writes.
func RunNativeTaskExec(args []string) error {
	if len(args) == 0 || !filepath.IsAbs(args[0]) || filepath.Clean(args[0]) != args[0] {
		return errors.New("Tasks executable must be an absolute canonical path")
	}
	dir := os.NewFile(3, "task-project-root")
	defer dir.Close()
	info, err := dir.Stat()
	if err != nil || !info.IsDir() {
		return errors.New("Tasks working-directory descriptor is unavailable or not a directory")
	}
	if err := unix.Fchdir(int(dir.Fd())); err != nil {
		return err
	}
	if err := dir.Close(); err != nil {
		return err
	}
	return syscall.Exec(args[0], args, os.Environ())
}
