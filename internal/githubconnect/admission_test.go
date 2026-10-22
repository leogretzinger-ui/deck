package githubconnect

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/noisefloor-oss/deck/internal/inputgate"
	"github.com/noisefloor-oss/deck/internal/tmux"
)

func TestStartCancelledBeforeAdmissionDoesNotReserveAttempt(t *testing.T) {
	m := newTestManager(t)
	m.statusFn = noAccount
	fakeGhOnPath(t, "sleep 300\n")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.Start(ctx, "operator-a"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled request: %v", err)
	}
	if m.current != nil || m.admitted != 0 {
		t.Fatal("cancelled request reserved an attempt")
	}
	if sessions, err := m.tmux.ListSessions(context.Background()); (err != nil && !errors.Is(err, tmux.ErrNoServer)) || len(sessions) != 0 {
		t.Fatalf("cancelled request sessions: %v %v", sessions, err)
	}
}

func TestStartBrowserDisconnectAfterSpawnPreservesIdentity(t *testing.T) {
	m := newTestManager(t)
	m.statusFn = noAccount
	fakeGhOnPath(t, "sleep 300\n")
	realTmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	spawned, release := filepath.Join(dir, "spawned"), filepath.Join(dir, "release")
	wrapper := fmt.Sprintf(`#!/bin/sh
for arg do
 if [ "$arg" = new-session ]; then
  '%s' "$@" || exit $?
  : > '%s'
  while [ ! -e '%s' ]; do sleep 0.01; done
  exit 0
 fi
done
exec '%s' "$@"
`, realTmux, spawned, release, realTmux)
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte(wrapper), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0600) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		meta AttemptMetadata
		err  error
	}
	completed := make(chan result, 1)
	go func() {
		meta, err := m.Start(ctx, "operator-a")
		completed <- result{meta, err}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(spawned); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("spawn barrier not reached")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	// Locking the workspace cannot finish while the admitted spawn is held.
	drainCtx, drainCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer drainCancel()
	drainErr := m.gate.FenceAndDrain(drainCtx)
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	var got result
	select {
	case got = <-completed:
	case <-time.After(5 * time.Second):
		t.Fatal("admitted spawn did not complete")
	}
	if got.err != nil || got.meta.Status != AttemptRunning {
		t.Fatalf("browser disconnect interrupted admitted spawn: %+v %v", got.meta, got.err)
	}
	if !errors.Is(drainErr, context.DeadlineExceeded) {
		t.Fatalf("launch did not retain workspace admission: %v", drainErr)
	}
	if _, _, err := m.Verify(context.Background(), got.meta.ID); err != nil {
		t.Fatalf("captured identity cannot be verified: %v", err)
	}
	if err := m.gate.FenceAndDrain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start(context.Background(), "operator-a"); !errors.Is(err, inputgate.ErrWorkspaceLocking) {
		t.Fatalf("fenced workspace allowed another launch: %v", err)
	}
	panes, err := m.tmux.ListPanes(context.Background())
	if err != nil || len(panes) != 1 || panes[0].Dead {
		t.Fatalf("admitted process was lost or duplicated: %+v %v", panes, err)
	}
}

func TestStartDifferentOwnerAfterTerminalAttempt(t *testing.T) {
	for _, terminal := range []string{"cancelled", "removed"} {
		t.Run(terminal, func(t *testing.T) {
			m := newTestManager(t)
			m.statusFn = noAccount
			fakeGhOnPath(t, "sleep 300\n")
			first, err := m.Start(context.Background(), "operator-a")
			if err != nil {
				t.Fatal(err)
			}
			if terminal == "cancelled" {
				if err := m.Cancel(context.Background(), "operator-a", first.ID); err != nil {
					t.Fatal(err)
				}
			} else {
				removeAttemptSession(t, m)
			}
			next, err := m.Start(context.Background(), "operator-b")
			if err != nil || next.ID == first.ID || next.Status != AttemptRunning {
				t.Fatalf("fresh principal after terminal attempt: %+v %v", next, err)
			}
			if _, ok := m.Current(context.Background(), "operator-a"); ok {
				t.Fatal("old owner can see new attempt")
			}
		})
	}
}

func TestStartDifferentOwnerCannotReplaceUncertainAttempt(t *testing.T) {
	m := newTestManager(t)
	m.statusFn = noAccount
	fakeGhOnPath(t, "sleep 300\n")
	first, err := m.Start(context.Background(), "operator-a")
	if err != nil {
		t.Fatal(err)
	}
	// A lost observation must not establish that the foreign process exited.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if meta, err := m.Start(ctx, "operator-b"); !errors.Is(err, ErrNotOwner) || meta.ID != "" {
		t.Fatalf("foreign uncertain attempt disclosed or replaced: %+v %v", meta, err)
	}
	m.current.state = AttemptUnavailable
	if meta, err := m.Start(context.Background(), "operator-b"); !errors.Is(err, ErrNotOwner) || meta.ID != "" {
		t.Fatalf("foreign unavailable attempt disclosed or replaced: %+v %v", meta, err)
	}
	if m.current.id != first.ID || m.admitted != 1 {
		t.Fatal("foreign attempt replaced")
	}
}

func TestCancelRemovedSessionRecordsExit(t *testing.T) {
	m := newTestManager(t)
	m.statusFn = noAccount
	fakeGhOnPath(t, "sleep 300\n")
	meta, err := m.Start(context.Background(), "operator-a")
	if err != nil {
		t.Fatal(err)
	}
	attachment, err := m.AttachContext("operator-a", meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	removeAttemptSession(t, m)
	if err := m.Cancel(context.Background(), "operator-a", meta.ID); !errors.Is(err, ErrAttemptNotRunning) {
		t.Fatalf("removed session cancellation: %v", err)
	}
	if m.current.state != AttemptExited || attachment.Err() == nil {
		t.Fatal("removed session was not recorded exited with attachment revoked")
	}
}

func removeAttemptSession(t *testing.T, m *Manager) {
	t.Helper()
	cmd := exec.Command("tmux", "-S", m.tmux.SocketPath(), "kill-session", "-t", m.current.info.SessionID)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("remove test session: %v %s", err, out)
	}
}
