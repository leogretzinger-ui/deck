//go:build unix

package project

import (
	"os/exec"
	"syscall"
)

// leafOpenFlags opens a directory by name without following a link in
// its place: a name swapped for a link is refused, not followed.
const leafOpenFlags = syscall.O_NOFOLLOW | syscall.O_DIRECTORY

// killProcessGroup runs the command in its own process group and, on
// cancel, signals the group: git's helpers die with it.
func killProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
