//go:build linux

package tmux

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestPaneObservationNonDumpableChild(t *testing.T) {
	if os.Getenv("DECK_TEST_NON_DUMPABLE") != "1" {
		return
	}
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintln(os.Stdout, "ready"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Hour)
}

func TestPaneObservationBoundsPersistentPermissionError(t *testing.T) {
	child := exec.Command(os.Args[0], "-test.run=^TestPaneObservationNonDumpableChild$")
	child.Env = append(os.Environ(), "DECK_TEST_NON_DUMPABLE=1")
	ready, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = child.Process.Kill()
		_ = child.Wait()
	})
	if line, err := bufio.NewReader(ready).ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("non-dumpable child readiness = %q, %v", line, err)
	}
	pid := child.Process.Pid
	if _, _, _, err := processIdentity(pid); !errors.Is(err, syscall.EACCES) {
		t.Fatalf("non-dumpable process identity error = %v, want EACCES", err)
	}
	live := fmt.Sprintf("$1\tstarting\t%%1\t%d\t0\t\t\tchild\t1\tmarker\t0\tmarker", pid)
	dead := fmt.Sprintf("$1\tstarting\t%%1\t%d\t1\t7\t\tchild\t1\tmarker\t0\tmarker", pid)
	for _, tc := range []struct {
		name    string
		observe func(*Client, context.Context) (*PaneInfo, error)
		recover bool
	}{
		{name: "pane", observe: func(c *Client, ctx context.Context) (*PaneInfo, error) {
			return c.PaneInfoByID(ctx, "%1")
		}},
		{name: "session", observe: func(c *Client, ctx context.Context) (*PaneInfo, error) {
			return c.PaneInfoForSession(ctx, "starting")
		}},
		{name: "spawn", observe: func(c *Client, ctx context.Context) (*PaneInfo, error) {
			return c.observeSpawnedPane(ctx, "%1")
		}},
		{name: "spawn transition recovers", recover: true, observe: func(c *Client, ctx context.Context) (*PaneInfo, error) {
			return c.observeSpawnedPane(ctx, "%1")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin := t.TempDir()
			script := "#!/bin/sh\nprintf '%s\\n' '" + live + "'\n"
			if tc.recover {
				first := filepath.Join(bin, "first")
				script = fmt.Sprintf("#!/bin/sh\nif [ ! -e '%s' ]; then\n  touch '%s'\n  printf '%%s\\n' '%s'\nelse\n  printf '%%s\\n' '%s'\nfi\n", first, first, live, dead)
			}
			if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			dir := t.TempDir()
			if err := os.Chmod(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			client, err := NewClient(Config{SocketPath: filepath.Join(dir, "test.sock")})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			started := time.Now()
			info, err := tc.observe(client, ctx)
			if elapsed := time.Since(started); elapsed > spawnTransitionLimit+250*time.Millisecond {
				t.Errorf("observation took %v, want at most the transition budget plus 250ms scheduling allowance", elapsed)
			}
			if tc.recover {
				if err != nil || info == nil || !info.Dead || info.ExitStatus == nil || *info.ExitStatus != 7 {
					t.Fatalf("recovered observation = %+v, %v; want a confirmed exit", info, err)
				}
				return
			}
			if info != nil || !errors.Is(err, syscall.EACCES) || !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("observation = %+v, %v; want no identity, EACCES and deadline exceeded", info, err)
			}
			if ctx.Err() != nil {
				t.Fatal("observation exhausted the caller's deadline")
			}
		})
	}
}

func TestPaneInfoRechecksExitingProcess(t *testing.T) {
	child := exec.Command("/bin/true")
	if err := child.Run(); err != nil {
		t.Fatal(err)
	}
	pid := child.Process.Pid
	live := fmt.Sprintf("$1\texiting\t%%1\t%d\t0\t\t\tsh\t1\tmarker\t0\tmarker", pid)
	dead := fmt.Sprintf("$1\texiting\t%%1\t%d\t1\t7\t\tsh\t1\tmarker\t0\tmarker", pid)
	for _, tc := range []struct {
		name string
		next string
		want error
	}{
		{name: "tmux confirms exit", next: "printf '%s\\n' '" + dead + "'"},
		{name: "pane removed", next: "echo 'no such pane' >&2; exit 1", want: ErrNoSession},
		{name: "server unreachable", next: "echo 'no server running' >&2; exit 1", want: ErrNoServer},
		{name: "identity remains unreadable", next: "printf '%s\\n' '" + live + "'", want: context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin := t.TempDir()
			first := filepath.Join(bin, "first")
			script := fmt.Sprintf("#!/bin/sh\nif [ ! -e '%s' ]; then\n  touch '%s'\n  printf '%%s\\n' '%s'\nelse\n  %s\nfi\n", first, first, live, tc.next)
			if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			dir := t.TempDir()
			if err := os.Chmod(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			client, err := NewClient(Config{SocketPath: filepath.Join(dir, "test.sock"), Timeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			info, err := client.PaneInfoByID(context.Background(), "%1")
			if tc.want != nil {
				if info != nil || !errors.Is(err, tc.want) {
					t.Fatalf("observation = %+v, %v; want no identity and %v", info, err, tc.want)
				}
				return
			}
			if err != nil || info == nil || !info.Dead || info.ExitStatus == nil || *info.ExitStatus != 7 {
				t.Fatalf("exit observation = %+v, %v", info, err)
			}
			if info.ProcessGroupID != 0 || info.StartTicks != 0 || info.Executable != "" {
				t.Fatalf("dead pane has a process identity: %+v", info)
			}
		})
	}
}
