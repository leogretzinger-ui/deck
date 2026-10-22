//go:build linux

package tmux

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func childTicks(t *testing.T, pid int) int64 {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		if err == nil {
			s := string(raw)
			fields := strings.Fields(s[strings.LastIndexByte(s, ')')+1:])
			if len(fields) >= 20 {
				ticks, perr := strconv.ParseInt(fields[19], 10, 64)
				if perr != nil {
					t.Fatalf("parse ticks: %v", perr)
				}
				return ticks
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("child stat never readable: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestProcGroupReturnsTheVerifiedProcessGroup(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "sleep 30; exit 0")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	ticks := childTicks(t, pid)

	pgid, err := ProcGroup(pid, ticks)
	if err != nil {
		t.Fatalf("ProcGroup: %v", err)
	}
	want, err := syscall.Getpgid(pid)
	if err != nil {
		t.Fatalf("getpgid: %v", err)
	}
	if pgid != want {
		t.Fatalf("pgid = %d, want the child's live group %d", pgid, want)
	}

	// A recycled-pid imposter (right pid, wrong ticks) is refused before
	// any group is handed out for signalling.
	if _, err := ProcGroup(pid, ticks+1); err == nil {
		t.Fatal("ProcGroup accepted a wrong start-ticks tuple")
	}
}

// ProcAlive's answers are load-bearing for automatic recovery: `false, nil`
// authorizes a replacement CLI, so it may only mean POSITIVELY absent — a
// live tuple is true, a recycled-pid mismatch and a nonexistent pid are
// false, and anything unverifiable must be an error, never silent proof of
// death.
func TestProcAliveDistinguishesAbsentFromMismatch(t *testing.T) {
	pid := os.Getpid()
	ticks := childTicks(t, pid) // reads /proc/<pid>/stat; works for self too

	alive, err := ProcAlive(pid, ticks)
	if err != nil || !alive {
		t.Fatalf("own process: alive=%v err=%v, want true", alive, err)
	}

	// Same pid, wrong start ticks: a recycled pid. Not the recorded
	// process — positively not alive AS RECORDED.
	alive, err = ProcAlive(pid, ticks+1)
	if err != nil || alive {
		t.Fatalf("tick mismatch: alive=%v err=%v, want false", alive, err)
	}

	// A pid that cannot exist: /proc reports no such process, which is
	// positive absence (ENOENT), not an unverifiable read.
	alive, err = ProcAlive(1<<22+12345, ticks)
	if err != nil || alive {
		t.Fatalf("nonexistent pid: alive=%v err=%v, want false, nil", alive, err)
	}
}
