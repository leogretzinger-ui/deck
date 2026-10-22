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

	"github.com/noisefloor-oss/deck/internal/terminal"
	"github.com/noisefloor-oss/deck/internal/tmux"
)

func TestReconnectFailedStoredCredential(t *testing.T) {
	m := newTestManager(t)
	fakeGhOnPath(t, "sleep 300\n")
	m.statusFn = func(context.Context) Result {
		return Result{Outcome: OutcomeAuthIssue, Status: &Status{Account: Account{Login: "alice", Active: true, State: StateError, Source: SourceStored, SourceWriteable: true}}}
	}
	if _, err := m.Start(context.Background(), "operator-a"); err != nil {
		t.Fatal(err)
	}
}

func TestExitedNativeProcessIsNotReportedRunning(t *testing.T) {
	m := newTestManager(t)
	fakeGhOnPath(t, "sleep 0.2\n")
	m.statusFn = noAccount
	meta, err := m.Start(context.Background(), "operator-a")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		got, ok := m.Current(context.Background(), "operator-a")
		if ok && got.Status == AttemptExited {
			if _, _, err := m.Verify(context.Background(), meta.ID); !errors.Is(err, terminal.ErrTargetNotRunning) {
				t.Fatalf("dead target verified: %v", err)
			}
			if m.Status(context.Background()).Outcome != OutcomeNoAccount {
				t.Fatal("process exit was treated as authentication")
			}
			next, err := m.Start(context.Background(), "operator-a")
			if err != nil || next.ID == meta.ID {
				t.Fatalf("explicit fresh attempt: %+v %v", next, err)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("native exit remained reported as running")
}

func TestCancelRefusesRespawnedProcess(t *testing.T) {
	m := newTestManager(t)
	fakeGhOnPath(t, "sleep 300\n")
	m.statusFn = noAccount
	meta, err := m.Start(context.Background(), "operator-a")
	if err != nil {
		t.Fatal(err)
	}
	pane := m.current.info.PaneID
	cmd := exec.Command("tmux", "-S", m.tmux.SocketPath(), "respawn-pane", "-k", "-t", pane, "/bin/sleep", "300")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("replace test pane: %v %s", err, out)
	}
	if err := m.Cancel(context.Background(), "operator-a", meta.ID); !errors.Is(err, ErrIdentityChanged) {
		t.Fatalf("replacement cancel = %v", err)
	}
	live, err := m.tmux.PaneInfoByID(context.Background(), pane)
	if err != nil || live.Dead {
		t.Fatalf("replacement was killed: %+v %v", live, err)
	}
	got, ok := m.Current(context.Background(), "operator-a")
	if !ok || got.Status != AttemptUnavailable || !got.Final {
		t.Fatalf("mismatch reported as cancelled or still pending: %+v", got)
	}
}

func TestCloseRevokesAttachmentWithoutKillingNativeProcess(t *testing.T) {
	m := newTestManager(t)
	fakeGhOnPath(t, "sleep 300\n")
	m.statusFn = noAccount
	meta, err := m.Start(context.Background(), "operator-a")
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := m.AttachContext("operator-a", meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	pane := m.current.info.PaneID
	m.Close()
	if ctx.Err() == nil {
		t.Fatal("shutdown retained attachment authority")
	}
	live, err := m.tmux.PaneInfoByID(context.Background(), pane)
	if err != nil || live.Dead {
		t.Fatalf("shutdown killed native process: %+v %v", live, err)
	}
	if _, err := m.Start(context.Background(), "operator-a"); !errors.Is(err, ErrAttemptNotRunning) {
		t.Fatalf("closed manager launched: %v", err)
	}
}

func TestUncertainSpawnRetainsNativeProcessUntilItsOwnerStartsAgain(t *testing.T) {
	m := newTestManager(t)
	fakeGhOnPath(t, "sleep 300\n")
	m.statusFn = noAccount
	realTmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	wrapper := fmt.Sprintf(`#!/bin/sh
for arg do
 if [ "$arg" = new-session ]; then
  '%s' "$@"
  exit 1
 fi
done
exec '%s' "$@"
`, realTmux, realTmux)
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(wrapper), 0700); err != nil {
		t.Fatal(err)
	}
	path := os.Getenv("PATH")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+path)
	if _, err := m.Start(context.Background(), "operator-a"); err == nil {
		t.Fatal("lost spawn result reported success")
	}
	got, ok := m.Current(context.Background(), "operator-a")
	if !ok || got.Status != AttemptUnavailable || !got.Final {
		t.Fatalf("uncertain attempt forgotten or still pending: %+v", got)
	}
	panes, err := m.tmux.ListPanes(context.Background())
	if err != nil || len(panes) != 1 || panes[0].Dead {
		t.Fatalf("uncertain launch destroyed process: %+v %v", panes, err)
	}
	if _, err := m.Start(context.Background(), "operator-b"); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("another owner replaced the uncertain attempt: %v", err)
	}
	t.Setenv("PATH", path)
	next, err := m.Start(context.Background(), "operator-a")
	if err != nil || next.ID == got.ID || next.Status != AttemptRunning {
		t.Fatalf("owner could not start again: %+v %v", next, err)
	}
	panes, err = m.tmux.ListPanes(context.Background())
	if err != nil || len(panes) != 2 || panes[0].Dead || panes[1].Dead {
		t.Fatalf("starting again touched the uncertain process: %+v %v", panes, err)
	}
}

func TestUnreadableAttemptIsNotFinalAndBlocksAnotherStart(t *testing.T) {
	m := newTestManager(t)
	fakeGhOnPath(t, "sleep 300\n")
	m.statusFn = noAccount
	first, err := m.Start(context.Background(), "operator-a")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, ok := m.Current(ctx, "operator-a")
	if !ok || got.Status != AttemptUnavailable || got.Final {
		t.Fatalf("a failed observation reported as final: %+v", got)
	}
	if _, err := m.Start(ctx, "operator-a"); !errors.Is(err, ErrAttemptInProgress) {
		t.Fatalf("a failed observation allowed another start: %v", err)
	}
	if m.current.id != first.ID || m.admitted != 1 {
		t.Fatal("unreadable attempt replaced")
	}
}

func TestNativeLoginInheritsEnvironmentWithoutCopyingItToArguments(t *testing.T) {
	m := newTestManager(t)
	m.statusFn = noAccount
	markerPath := filepath.Join(t.TempDir(), "inherited")
	t.Setenv("DECK_TEST_GH_INHERIT", "synthetic-marker")
	t.Setenv("DECK_TEST_GH_MARKER_PATH", markerPath)
	fakeGhOnPath(t, `test "$DECK_TEST_GH_INHERIT" = synthetic-marker || exit 98
printf inherited > "$DECK_TEST_GH_MARKER_PATH"
sleep 300
`)
	realTmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	wrapper := fmt.Sprintf(`#!/bin/sh
for arg do
 case "$arg" in
  DECK_TEST_GH_INHERIT=*) exit 99 ;;
 esac
done
exec '%s' "$@"
`, realTmux)
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(wrapper), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if _, err := m.Start(context.Background(), "operator-a"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(markerPath); err == nil && string(data) == "inherited" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("native login did not inherit its environment")
}

func TestLoginUsesCurrentHomeWithSurvivingTmuxServer(t *testing.T) {
	for _, home := range []string{"/Users/Your Home", "/home/agent"} {
		for _, customConfig := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/custom-config=%t", home, customConfig), func(t *testing.T) {
				t.Setenv("HOME", t.TempDir())
				t.Setenv("GH_CONFIG_DIR", t.TempDir())
				t.Setenv("XDG_CONFIG_HOME", t.TempDir())
				marker := filepath.Join(t.TempDir(), "environment")
				t.Setenv("DECK_TEST_GH_MARKER_PATH", marker)
				fakeGhOnPath(t, `printf 'old tool\n' > "$DECK_TEST_GH_MARKER_PATH"
sleep 300
`)
				m := newTestManager(t)
				m.statusFn = noAccount
				ctx := context.Background()
				if _, err := m.tmux.CreateSession(ctx, tmux.SessionSpec{
					Name: "deck-home-fixture", WorkDir: t.TempDir(), Argv: []string{"/bin/sleep", "300"},
					Generation: 1, Marker: "home-fixture",
				}); err != nil {
					t.Fatal(err)
				}
				t.Setenv("HOME", home)
				config := ""
				if customConfig {
					config = t.TempDir()
				}
				t.Setenv("GH_CONFIG_DIR", config)
				t.Setenv("XDG_CONFIG_HOME", "")
				fakeGhOnPath(t, `printf '%s\n' "$HOME" "$GH_CONFIG_DIR" "$XDG_CONFIG_HOME" > "$DECK_TEST_GH_MARKER_PATH"
sleep 300
`)
				if _, err := m.Start(ctx, "you"); err != nil {
					t.Fatal(err)
				}
				deadline := time.Now().Add(3 * time.Second)
				for time.Now().Before(deadline) {
					if data, err := os.ReadFile(marker); err == nil && len(data) > 0 {
						if got, want := string(data), home+"\n"+config+"\n\n"; got != want {
							t.Fatalf("login environment = %q, want %q", got, want)
						}
						return
					}
					time.Sleep(10 * time.Millisecond)
				}
				t.Fatal("login did not record its environment")
			})
		}
	}
}
