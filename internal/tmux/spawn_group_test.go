package tmux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Before setsid, a pane child belongs to tmux's process group. Recording
// that group would make a later provider stop kill the tmux server too.
func TestSpawnDoesNotRecordInheritedProcessGroup(t *testing.T) {
	child := exec.Command("/bin/sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = child.Process.Kill()
		_ = child.Wait()
	})
	pid := child.Process.Pid
	pgid, ticks, _, err := processIdentity(pid)
	if err != nil {
		t.Fatal(err)
	}
	if pgid == pid {
		t.Fatal("fixture must inherit its parent's process group")
	}

	t.Run("creation", func(t *testing.T) {
		info, err := createdPaneInfo(fmt.Sprintf("$1\t%%1\t%d", pid), spec("starting", "/bin/sleep", "30"))
		if err != nil {
			t.Fatal(err)
		}
		if info.ProcessGroupID != 0 || info.SpawnProcessGroupID != 0 {
			t.Fatalf("recorded inherited process group: %+v", info)
		}
		if info.StartTicks != ticks || info.SpawnStartTicks != ticks {
			t.Fatalf("lost the child's start time: %+v", info)
		}
	})

	t.Run("observation", func(t *testing.T) {
		bin := t.TempDir()
		record := fmt.Sprintf("$1\tstarting\t%%1\t%d\t0\t\t\tsleep\t1\tmarker\t0\tmarker", pid)
		script := "#!/bin/sh\nprintf '%s\\n' '" + record + "'\n"
		if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
		dir := t.TempDir()
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Fatalf("chmod socket dir: %v", err)
		}
		client, err := NewClient(Config{SocketPath: filepath.Join(dir, "test.sock")})
		if err != nil {
			t.Fatal(err)
		}
		if info, err := client.PaneInfoByID(context.Background(), "%1"); info != nil || !errors.Is(err, errPaneGroupPending) {
			t.Fatalf("inherited group observation = %+v, %v; want an unsettled group", info, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		info, err := client.observeSpawnedPane(ctx, "%1")
		if info != nil || err == nil {
			t.Fatalf("unsettled pane observation = %+v, %v; want a bounded refusal", info, err)
		}
	})
}
