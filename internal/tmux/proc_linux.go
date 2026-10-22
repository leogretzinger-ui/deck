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

// processIdentity reads the parts of a process's identity that tmux does
// not expose: its process-group id, its start time, and the executable
// actually running.
//
// Start time is the field that makes the tuple trustworthy. PIDs are reused,
// so "a process with this PID exists" proves nothing; "a process with this PID
// that started at this exact tick" is what distinguishes the provider we
// launched from an unrelated process that inherited its number.
//
// CAVEAT for durable identity: these ticks are measured from BOOT, not from an
// absolute epoch (see `btime` in /proc/stat). They therefore repeat across a
// host restart, so anything persisting this value as identity across reboots
// must also record a boot epoch. PRD §13.3 requires a staging host reboot to
// demonstrate exact native resume, which is where that would surface.
func processIdentity(pid int) (pgid int, startTicks int64, executable string, err error) {
	pgid, startTicks, err = processStatIdentity(pid)
	if err != nil {
		return 0, 0, "", err
	}

	// The executable is read from /proc/<pid>/exe, not from the command
	// line: a process can rewrite its own argv, so the command line is a
	// claim while the exe link is the kernel's own record of what is
	// running. Failure is an incomplete identity tuple, never a successful
	// partial observation.
	executable, err = os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil {
		return 0, 0, "", fmt.Errorf("tmux: read process %d executable: %w", pid, err)
	}
	return pgid, startTicks, executable, nil
}

// processStatIdentity is the PGID/start subset used while enumerating a
// process group. Group enumeration does not claim controller identity and
// must not silently omit a member merely because its executable link is
// unreadable.
func processStatIdentity(pid int) (pgid int, startTicks int64, err error) {
	statPath := fmt.Sprintf("/proc/%d/stat", pid)
	// #nosec G304 -- the path is /proc/<int>/stat, built from an int argument.
	// No caller-supplied string reaches it, so there is no traversal surface.
	raw, err := os.ReadFile(statPath)
	if err != nil {
		return 0, 0, fmt.Errorf("tmux: read %s: %w", statPath, err)
	}

	// Field 2 (comm) is parenthesised and may itself contain spaces and
	// parentheses, so the remainder must be located from the LAST ')' rather
	// than by splitting the whole line.
	end := strings.LastIndexByte(string(raw), ')')
	if end < 0 || end+2 >= len(raw) {
		return 0, 0, fmt.Errorf("tmux: malformed %s", statPath)
	}
	fields := strings.Fields(string(raw[end+2:]))

	// After comm, fields are numbered from 3: index 0 is state (field 3), so
	// pgrp (field 5) is index 2 and starttime (field 22) is index 19.
	const (
		pgrpIndex      = 2
		starttimeIndex = 19
	)
	if len(fields) <= starttimeIndex {
		return 0, 0, fmt.Errorf(
			"tmux: %s has %d fields after comm, need at least %d",
			statPath, len(fields), starttimeIndex+1)
	}

	pgid, err = strconv.Atoi(fields[pgrpIndex])
	if err != nil {
		return 0, 0, fmt.Errorf("tmux: parse pgrp from %s: %w", statPath, err)
	}
	startTicks, err = strconv.ParseInt(fields[starttimeIndex], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("tmux: parse starttime from %s: %w", statPath, err)
	}
	return pgid, startTicks, nil
}

// GroupMembers returns the pids of live processes currently in the given
// process group.
//
// A dead pane proves only that tmux reaped the pane LEADER. Providers spawn
// children — tool invocations, language servers, helper processes — and
// those inherit the group. If the leader exits while a child keeps running,
// the session looks stopped while a process still holds the project
// directory and the provider's credentials, which is exactly what stopping
// a session must prevent.
//
// This scans /proc rather than tracking children, because children can be
// spawned at any time by a process we do not control.
//
// It reports an OBSERVATION, not a verdict. In particular an empty result does
// not mean "every process this provider started is gone": a process group is a
// signal-routing construct, not a supervision boundary, and a child that calls
// setsid(2) or setpgid(2) leaves the group while continuing to run. Deciding
// what an empty group licenses is the session layer's problem, not this
// package's.
//
// The scan is non-atomic and skips only entries that positively vanished
// during enumeration. Any other unreadable entry fails the observation so an
// incomplete inventory can never masquerade as an empty group. Zombies are
// excluded because they cannot execute or receive a signal.
func GroupMembers(pgid int) ([]int, error) {
	if pgid <= 1 {
		return nil, fmt.Errorf("tmux: refusing to scan for process group %d", pgid)
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, fmt.Errorf("tmux: read /proc: %w", err)
	}

	var members []int
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue // not a pid directory
		}
		got, _, err := processStatIdentity(pid)
		if err != nil {
			if vanishedDuringScan(err) {
				continue // exited while the /proc inventory was being read
			}
			return nil, fmt.Errorf("tmux: inspect process %d while enumerating group %d: %w",
				pid, pgid, err)
		}
		if got == pgid && !processZombie(pid) {
			members = append(members, pid)
		}
	}
	return members, nil
}

// ESRCH means the process was reaped between opening and reading its stat
// file. Other read failures leave the inventory incomplete and must fail it.
func vanishedDuringScan(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH)
}

func processZombie(pid int) bool {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	end := strings.LastIndexByte(string(raw), ')')
	if end < 0 || end+2 >= len(raw) {
		return false
	}
	fields := strings.Fields(string(raw[end+2:]))
	return len(fields) > 0 && fields[0] == "Z"
}
