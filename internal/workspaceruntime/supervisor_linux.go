//go:build linux

package workspaceruntime

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// pfForkNoExec is the /proc/<pid>/stat flag bit PF_FORKNOEXEC: set at fork,
// cleared at exec. golang.org/x/sys/unix does not export it.
const pfForkNoExec = 0x40

type processSnapshot struct {
	pid, parent, uid int
	start            uint64
	forkWithoutExec  bool
}

// systemd's protected user manager and its forked PAM helper can deny exe
// reads even to their own UID. Only the root supervisor can attest the
// manager; process names and user-controlled cgroups cannot establish it.
type protectedSupervisor struct{ manager processSnapshot }

func systemUserManagerPID(ctx context.Context, uid int) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/systemctl", "--system", "show", "--property=MainPID", "--value", "user@"+strconv.Itoa(uid)+".service")
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "SYSTEMD_PAGER=cat"}
	out, err := cmd.Output()
	if err != nil {
		return 0, ErrUnavailable
	}
	return parseManagerPID(string(out))
}

func parseManagerPID(out string) (int, error) {
	out = strings.TrimSuffix(out, "\n")
	if out == "" {
		return 0, ErrUnavailable
	}
	for _, ch := range out {
		if ch < '0' || ch > '9' {
			return 0, ErrUnavailable
		}
	}
	pid, err := strconv.Atoi(out)
	if err != nil || pid <= 1 {
		return 0, ErrUnavailable
	}
	return pid, nil
}

func readProcessSnapshot(procRoot string, pid int) (processSnapshot, error) {
	root := filepath.Join(procRoot, strconv.Itoa(pid))
	var st unix.Stat_t
	if unix.Stat(root, &st) != nil {
		return processSnapshot{}, ErrUnavailable
	}
	// Only kernel process identity is read, never provider arguments.
	b, err := os.ReadFile(filepath.Join(root, "stat")) // #nosec G304 -- root contains only a numeric PID beneath the admin-owned proc root; stat is a fixed kernel identity file.
	if err != nil {
		return processSnapshot{}, ErrUnavailable
	}
	s := string(b)
	lp, rp := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
	if lp < 1 || rp <= lp || strings.TrimSpace(s[:lp]) != strconv.Itoa(pid) {
		return processSnapshot{}, ErrUnavailable
	}
	fields := strings.Fields(s[rp+1:])
	if len(fields) < 20 {
		return processSnapshot{}, ErrUnavailable
	}
	parent, e1 := strconv.Atoi(fields[1])
	flags, e2 := strconv.ParseUint(fields[6], 10, 64)
	start, e3 := strconv.ParseUint(fields[19], 10, 64)
	if e1 != nil || e2 != nil || e3 != nil || parent < 1 || start == 0 {
		return processSnapshot{}, ErrUnavailable
	}
	return processSnapshot{pid: pid, parent: parent, uid: int(st.Uid), start: start, forkWithoutExec: flags&pfForkNoExec != 0}, nil
}

func attestSupervisor(ctx context.Context, procRoot string, uid int, query func(context.Context, int) (int, error)) (*protectedSupervisor, error) {
	pid, err := query(ctx, uid)
	if err != nil || pid <= 1 {
		return nil, ErrUnavailable
	}
	m, err := readProcessSnapshot(procRoot, pid)
	if err != nil || m.uid != uid || m.parent != 1 {
		return nil, ErrUnavailable
	}
	s := &protectedSupervisor{manager: m}
	if err := s.revalidate(ctx, procRoot, query); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *protectedSupervisor) contains(p processSnapshot) bool {
	if p == s.manager {
		return true
	}
	// PF_FORKNOEXEC plus the current PPID prove only that this process has
	// not exec'd since its own fork — not that it inherited the manager
	// executable. The user manager is a subreaper, so an orphan forked by an
	// unrelated process is adopted with its PPID rewritten to the manager
	// while its fork flag survives; that adopted-orphan residual is admitted
	// here. A helper that execs receives no exemption.
	return p.uid == s.manager.uid && p.parent == s.manager.pid && p.forkWithoutExec && p.start >= s.manager.start
}

func (s *protectedSupervisor) revalidate(ctx context.Context, procRoot string, query func(context.Context, int) (int, error)) error {
	pid, err := query(ctx, s.manager.uid)
	if err != nil || pid != s.manager.pid {
		return ErrUnavailable
	}
	m, err := readProcessSnapshot(procRoot, pid)
	if err != nil || m != s.manager {
		return ErrUnavailable
	}
	return nil
}
