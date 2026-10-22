//go:build linux

package tmux

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// ProcAlive reports whether the RECORDED process — pid plus its recorded
// start ticks — is still running. It reads exactly one /proc entry for
// exactly one recorded pid; it is tuple verification, not process-tree
// scanning (which the containment decision forbids as compensation).
//
// The start-ticks comparison is what makes a recycled pid compare unequal.
// A zombie or dead process still has a /proc entry but is NOT alive.
//
// ABSENT and UNVERIFIABLE are distinct answers, and the distinction is
// load-bearing: a `false, nil` from this function is what authorizes
// automatic recovery to start a replacement CLI — positive proof of death —
// so it is returned only when /proc positively reports no such process
// (ENOENT/ESRCH). Any other read failure — permissions, an unexpected
// procfs error — proves nothing in either direction and is returned as an
// error for the caller's park-on-unverifiable branch.
func ProcAlive(pid int, startTicks int64) (bool, error) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
		// No such pid: the process is positively absent. (ESRCH covers a
		// process reaped between the open and the read.)
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("proc %d: liveness unverifiable: %w", pid, err)
	}
	s := string(raw)
	// comm may contain spaces and parens; fields resume after the last ')'.
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return false, fmt.Errorf("proc %d: unreadable stat", pid)
	}
	fields := strings.Fields(s[i+1:])
	if len(fields) < 20 {
		return false, fmt.Errorf("proc %d: short stat", pid)
	}
	switch fields[0] {
	case "Z", "X", "x":
		return false, nil
	}
	ticks, err := strconv.ParseInt(fields[19], 10, 64)
	if err != nil {
		return false, fmt.Errorf("proc %d: bad start ticks: %w", pid, err)
	}
	return ticks == startTicks, nil
}

// ProcGroup verifies the recorded process exactly as ProcAlive does and,
// from the SAME /proc read, returns its current process group id for
// signalling. One read means the group signalled is provably the verified
// process's own — never a recorded value the row cannot prove still names
// its group, and never a pid recycled between two reads.
func ProcGroup(pid int, startTicks int64) (int, error) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, fmt.Errorf("proc %d: %w", pid, err)
	}
	s := string(raw)
	// comm may contain spaces and parens; fields resume after the last ')'.
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, fmt.Errorf("proc %d: unreadable stat", pid)
	}
	fields := strings.Fields(s[i+1:])
	if len(fields) < 20 {
		return 0, fmt.Errorf("proc %d: short stat", pid)
	}
	switch fields[0] {
	case "Z", "X", "x":
		return 0, fmt.Errorf("proc %d: not the recorded live process", pid)
	}
	ticks, err := strconv.ParseInt(fields[19], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("proc %d: bad start ticks: %w", pid, err)
	}
	if ticks != startTicks {
		return 0, fmt.Errorf("proc %d: not the recorded live process", pid)
	}
	pgid, err := strconv.Atoi(fields[2])
	if err != nil {
		return 0, fmt.Errorf("proc %d: bad process group: %w", pid, err)
	}
	return pgid, nil
}
