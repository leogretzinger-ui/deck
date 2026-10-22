//go:build darwin

package tmux

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	darwinZombieState       = 5
	darwinProcInfoCallPID   = 2
	darwinProcPIDPathInfo   = 11
	darwinProcPIDPathMaxLen = 4 * 1024
)

// darwinProcess returns one kernel process record. The slice form preserves
// the documented distinction between an absent PID (zero records) and a
// failed sysctl observation (error). Only positive absence authorizes a
// replacement CLI; a failed observation parks the row instead.
func darwinProcess(pid int) (*unix.KinfoProc, bool, error) {
	if pid <= 0 {
		return nil, false, fmt.Errorf("invalid pid %d", pid)
	}
	rows, err := unix.SysctlKinfoProcSlice("kern.proc.pid", pid)
	if err != nil {
		return nil, false, err
	}
	if len(rows) == 0 {
		return nil, false, nil
	}
	for i := range rows {
		if rows[i].Proc.P_pid == int32(pid) {
			return &rows[i], true, nil
		}
	}
	return nil, false, fmt.Errorf("kern.proc.pid returned no record for pid %d", pid)
}

func darwinStartTime(p *unix.KinfoProc) int64 {
	return unix.TimevalToNsec(p.Proc.P_starttime)
}

func darwinProcessPath(pid int) (string, error) {
	buf := make([]byte, darwinProcPIDPathMaxLen)
	_, _, errno := syscall.Syscall6(syscall.SYS_PROC_INFO,
		darwinProcInfoCallPID, uintptr(pid), darwinProcPIDPathInfo, 0,
		uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if errno != 0 {
		return "", errno
	}
	// PROC_PIDPATHINFO reports success independently of the path length;
	// libproc's proc_pidpath derives that length from the terminated buffer.
	end := bytes.IndexByte(buf, 0)
	if end <= 0 {
		return "", errors.New("empty or unterminated process path")
	}
	buf = buf[:end]
	path := string(buf)
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		path = resolved
	}
	return path, nil
}

// processIdentity reads the process group, process start timestamp, and
// kernel executable path from macOS process APIs. The timestamp is stored in
// the existing process_start_ticks column as nanoseconds since the Unix epoch;
// only equality matters, and BootID separately binds it to this boot.
func processIdentity(pid int) (pgid int, startTicks int64, executable string, err error) {
	p, found, err := darwinProcess(pid)
	if err != nil {
		return 0, 0, "", fmt.Errorf("tmux: read process %d identity: %w", pid, err)
	}
	if !found {
		return 0, 0, "", fmt.Errorf("tmux: read process %d identity: %w", pid, os.ErrNotExist)
	}
	if p.Proc.P_stat == darwinZombieState {
		return 0, 0, "", fmt.Errorf("tmux: process %d is a zombie", pid)
	}
	pgid = int(p.Eproc.Pgid)
	startTicks = darwinStartTime(p)
	if pgid <= 1 || startTicks <= 0 {
		return 0, 0, "", fmt.Errorf("tmux: process %d has incomplete identity", pid)
	}
	executable, err = darwinProcessPath(pid)
	if err != nil {
		return 0, 0, "", fmt.Errorf("tmux: read process %d executable: %w", pid, err)
	}
	return pgid, startTicks, executable, nil
}

// ProcAlive positively distinguishes the exact recorded process from an
// absent PID, a zombie, and a reused PID. Observation failures remain errors.
func ProcAlive(pid int, startTicks int64) (bool, error) {
	p, found, err := darwinProcess(pid)
	if err != nil {
		return false, fmt.Errorf("proc %d: liveness unverifiable: %w", pid, err)
	}
	if !found || p.Proc.P_stat == darwinZombieState {
		return false, nil
	}
	return darwinStartTime(p) == startTicks, nil
}

// ProcGroup verifies the recorded start timestamp and returns the process's
// current group from the same kernel record.
func ProcGroup(pid int, startTicks int64) (int, error) {
	p, found, err := darwinProcess(pid)
	if err != nil {
		return 0, fmt.Errorf("proc %d: process group unverifiable: %w", pid, err)
	}
	if !found || p.Proc.P_stat == darwinZombieState || darwinStartTime(p) != startTicks {
		return 0, fmt.Errorf("proc %d: not the recorded live process", pid)
	}
	pgid := int(p.Eproc.Pgid)
	if pgid <= 1 {
		return 0, fmt.Errorf("proc %d: invalid process group %d", pid, pgid)
	}
	return pgid, nil
}

// GroupMembers returns live members of one process group from a single macOS
// kernel inventory. A failed inventory is unknown, never an empty group.
func GroupMembers(pgid int) ([]int, error) {
	if pgid <= 1 {
		return nil, fmt.Errorf("tmux: refusing to scan for process group %d", pgid)
	}
	rows, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, fmt.Errorf("tmux: enumerate processes: %w", err)
	}
	members := make([]int, 0)
	for i := range rows {
		if int(rows[i].Eproc.Pgid) == pgid && rows[i].Proc.P_stat != darwinZombieState {
			members = append(members, int(rows[i].Proc.P_pid))
		}
	}
	return members, nil
}
