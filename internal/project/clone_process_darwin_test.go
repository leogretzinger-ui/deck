//go:build darwin

package project

import (
	"errors"
	"fmt"
	"syscall"

	"golang.org/x/sys/unix"
)

func cloneHelperRunning(pid int) (bool, error) {
	rows, err := unix.SysctlKinfoProcSlice("kern.proc.pid", pid)
	if errors.Is(err, syscall.ESRCH) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if len(rows) == 0 {
		return false, nil
	}
	for _, row := range rows {
		if int(row.Proc.P_pid) == pid {
			return row.Proc.P_stat != 5, nil
		}
	}
	return false, fmt.Errorf("process observation omitted requested pid")
}
