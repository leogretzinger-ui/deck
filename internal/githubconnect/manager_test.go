package githubconnect

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/noisefloor-oss/deck/internal/inputgate"
	"github.com/noisefloor-oss/deck/internal/terminal"
	"github.com/noisefloor-oss/deck/internal/tmux"
)

// The manager tests run a real tmux server in a temporary namespace and a
// fake gh executable; no real gh auth or OAuth ever executes, and no
// credential file is read.

func fakeGhOnPath(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "gh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	client, err := tmux.NewClient(tmux.Config{
		SocketPath: filepath.Join(dir, "ghconnect-test.sock"), Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("tmux.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = client.KillServer(context.Background()) })
	return NewManager(client, inputgate.New(), zerolog.Nop())
}

func noAccount(ctx context.Context) Result {
	return Result{Outcome: OutcomeNoAccount}
}

func TestStartRequiresPrincipal(t *testing.T) {
	m := newTestManager(t)
	if _, err := m.Start(context.Background(), ""); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("err = %v, want ErrUnauthenticated", err)
	}
	if sessions, _ := m.tmux.ListSessions(context.Background()); len(sessions) != 0 {
		t.Fatalf("unauthenticated start spawned %d sessions", len(sessions))
	}
}

func TestStartRefusesExistingCredentialsWithoutReplacement(t *testing.T) {
	for _, tc := range []struct {
		name string
		res  Result
		want error
	}{
		{"authenticated-stored", Result{Outcome: OutcomeAuthenticated, Status: &Status{Account: Account{
			Login: "alice", Source: SourceStored, SourceWriteable: true, State: StateSuccess, Active: true}}}, ErrAlreadyConnected},
		{"authenticated-env", Result{Outcome: OutcomeAuthenticated, Status: &Status{Account: Account{
			Login: "alice", Source: SourceEnvironment, State: StateSuccess, Active: true}}}, ErrAlreadyConnected},
		{"authenticated-unknown-source", Result{Outcome: OutcomeAuthenticated, Status: &Status{Account: Account{
			Login: "alice", Source: SourceUnknown, State: StateSuccess, Active: true}}}, ErrAlreadyConnected},
		{"env-owned-auth-issue", Result{Outcome: OutcomeAuthIssue, Status: &Status{Account: Account{
			Source: SourceEnvironment, State: StateError}}}, ErrExistingAccount},
		{"missing-cli", Result{Outcome: OutcomeMissingCLI}, ErrMissingCLI},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestManager(t)
			m.statusFn = func(context.Context) Result { return tc.res }
			if _, err := m.Start(context.Background(), "operator-a"); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if sessions, _ := m.tmux.ListSessions(context.Background()); len(sessions) != 0 {
				t.Fatalf("refused start spawned %d sessions", len(sessions))
			}
		})
	}
}

func TestStartLaunchesOnlyExplicitlyAndStatusNeverStartsLogin(t *testing.T) {
	m := newTestManager(t)
	m.statusFn = noAccount
	fakeGhOnPath(t, "sleep 300\n")

	// A read never launches anything.
	if (m.Status(context.Background()) != Result{Outcome: OutcomeNoAccount}) {
		t.Fatal("statusFn passthrough broken")
	}
	if sessions, _ := m.tmux.ListSessions(context.Background()); len(sessions) != 0 {
		t.Fatalf("status read spawned %d sessions", len(sessions))
	}

	meta, err := m.Start(context.Background(), "operator-a")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if meta.ID == "" || meta.Status != AttemptRunning || meta.Generation != 1 {
		t.Fatalf("meta = %+v", meta)
	}
	sessions, err := m.tmux.ListSessions(context.Background())
	if err != nil || len(sessions) != 1 {
		t.Fatalf("sessions = %v err = %v, want exactly one", sessions, err)
	}
	if sessions[0].Name != m.current.info.SessionName {
		t.Fatalf("session name = %q, want the attempt's", sessions[0].Name)
	}

	// One current attempt: a second explicit start is refused, not a
	// replacement.
	if _, err := m.Start(context.Background(), "operator-a"); !errors.Is(err, ErrAttemptInProgress) {
		t.Fatalf("second start: err = %v, want ErrAttemptInProgress", err)
	}
}

func TestCurrentAndOwnerEnforcement(t *testing.T) {
	m := newTestManager(t)
	m.statusFn = noAccount
	fakeGhOnPath(t, "sleep 300\n")
	meta, err := m.Start(context.Background(), "operator-a")
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := m.Current(context.Background(), "operator-a"); !ok || got.ID != meta.ID {
		t.Fatalf("Current(owner) = %+v %v", got, ok)
	}
	if _, ok := m.Current(context.Background(), "operator-b"); ok {
		t.Fatal("foreign owner learned about the attempt")
	}
	if err := m.Cancel(context.Background(), "operator-b", meta.ID); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("foreign cancel: err = %v, want ErrNotOwner", err)
	}
	if _, err := m.AttachContext("operator-b", meta.ID); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("foreign attach: err = %v, want ErrNotOwner", err)
	}
}

func TestCancelUnknownIDRefusedWithoutEffect(t *testing.T) {
	m := newTestManager(t)
	if err := m.Cancel(context.Background(), "operator-a", "no-such-id"); !errors.Is(err, ErrUnknownAttempt) {
		t.Fatalf("err = %v, want ErrUnknownAttempt", err)
	}
}

func TestCancelKillsExactOwnedPaneOnly(t *testing.T) {
	m := newTestManager(t)
	m.statusFn = noAccount
	fakeGhOnPath(t, "sleep 300\n")
	meta, err := m.Start(context.Background(), "operator-a")
	if err != nil {
		t.Fatal(err)
	}

	// An unrelated pane in the same namespace must survive the cancel.
	other, err := m.tmux.CreateSession(context.Background(), tmux.SessionSpec{
		Name: "unrelated", Argv: []string{"/bin/sh", "-c", "sleep 300"},
		WorkDir: t.TempDir(), Generation: 1, Marker: "unrelated-marker",
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := m.Cancel(context.Background(), "operator-a", meta.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	// The exact pane is gone.
	if _, err := m.tmux.PaneInfoByID(context.Background(), m.current.info.PaneID); !errors.Is(err, tmux.ErrNoSession) {
		t.Fatalf("owned pane still observable: %v", err)
	}
	// The unrelated pane was not touched.
	live, err := m.tmux.PaneInfoByID(context.Background(), other.PaneID)
	if err != nil || live.PID != other.PID {
		t.Fatalf("unrelated pane affected: %v %v", live, err)
	}

	// Cancellation is terminal: same ID again is unknown, not repeated.
	if err := m.Cancel(context.Background(), "operator-a", meta.ID); !errors.Is(err, ErrUnknownAttempt) {
		t.Fatalf("repeat cancel: err = %v, want ErrUnknownAttempt", err)
	}
	if got, ok := m.Current(context.Background(), "operator-a"); !ok || got.Status != AttemptCancelled {
		t.Fatalf("Current after cancel = %+v %v", got, ok)
	}
	// A cancelled attempt has no attachment context.
	if _, err := m.AttachContext("operator-a", meta.ID); !errors.Is(err, ErrAttemptNotRunning) {
		t.Fatalf("attach after cancel: err = %v", err)
	}

	// After the terminal attempt, a fresh start works, and cancelling the
	// OLD id is refused while the new one owns the slot.
	meta2, err := m.Start(context.Background(), "operator-a")
	if err != nil {
		t.Fatalf("restart attempt: %v", err)
	}
	if meta2.ID == meta.ID {
		t.Fatal("attempt ID was recycled")
	}
	if err := m.Cancel(context.Background(), "operator-a", meta.ID); !errors.Is(err, ErrUnknownAttempt) {
		t.Fatalf("stale-id cancel: err = %v, want ErrUnknownAttempt", err)
	}
}

func TestAttemptAdmissionBudget(t *testing.T) {
	m := newTestManager(t)
	m.statusFn = noAccount
	m.maxAttempts = 2
	fakeGhOnPath(t, "sleep 300\n")
	for i := 0; i < 2; i++ {
		meta, err := m.Start(context.Background(), "operator-a")
		if err != nil {
			t.Fatalf("start %d: %v", i, err)
		}
		if err := m.Cancel(context.Background(), "operator-a", meta.ID); err != nil {
			t.Fatalf("cancel %d: %v", i, err)
		}
	}
	if _, err := m.Start(context.Background(), "operator-a"); !errors.Is(err, ErrAttemptBudget) {
		t.Fatalf("err = %v, want ErrAttemptBudget", err)
	}
}

func TestWorkspaceFenceBlocksStartAndCancel(t *testing.T) {
	m := newTestManager(t)
	m.statusFn = noAccount
	fakeGhOnPath(t, "sleep 300\n")
	meta, err := m.Start(context.Background(), "operator-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.gate.FenceAndDrain(context.Background()); err != nil {
		t.Fatalf("FenceAndDrain: %v", err)
	}
	if _, err := m.Start(context.Background(), "operator-a"); !errors.Is(err, inputgate.ErrWorkspaceLocking) {
		t.Fatalf("fenced start: err = %v, want workspace locking", err)
	}
	if err := m.Cancel(context.Background(), "operator-a", meta.ID); err == nil ||
		!strings.Contains(err.Error(), inputgate.ErrWorkspaceLocking.Error()) {
		t.Fatalf("fenced cancel: err = %v, want workspace locking", err)
	}
}

// Browser close must not cancel the attempt; explicit cancel must revoke
// the joined attachment context. This is the join the terminal handler
// performs because the proxy has no Close.
func TestAttachmentContextJoinSemantics(t *testing.T) {
	m := newTestManager(t)
	m.statusFn = noAccount
	fakeGhOnPath(t, "sleep 300\n")
	meta, err := m.Start(context.Background(), "operator-a")
	if err != nil {
		t.Fatal(err)
	}
	actx, err := m.AttachContext("operator-a", meta.ID)
	if err != nil {
		t.Fatal(err)
	}

	reqCtx, closeBrowser := context.WithCancel(context.Background())
	joined, cancelJoin := context.WithCancel(actx)
	stop := context.AfterFunc(reqCtx, cancelJoin)
	defer stop()

	closeBrowser()
	select {
	case <-joined.Done():
	case <-time.After(time.Second):
		t.Fatal("browser close left the attachment context live")
	}
	if actx.Err() != nil {
		t.Fatal("browser close cancelled the attempt itself")
	}

	if err := m.Cancel(context.Background(), "operator-a", meta.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-joined.Done():
	case <-time.After(time.Second):
		t.Fatal("explicit cancel did not revoke the attachment context")
	}
}

func TestVerifyUsesFullCapturedIdentity(t *testing.T) {
	m := newTestManager(t)
	m.statusFn = noAccount
	fakeGhOnPath(t, "sleep 300\n")
	meta, err := m.Start(context.Background(), "operator-a")
	if err != nil {
		t.Fatal(err)
	}

	// Unknown and stale IDs never verify.
	if err := m.Open(context.Background(), "no-such-id"); !errors.Is(err, terminal.ErrTargetAbsent) {
		t.Fatalf("open unknown: %v", err)
	}

	if err := m.Open(context.Background(), meta.ID); err != nil {
		t.Fatalf("open current: %v", err)
	}
	target, live, err := m.Verify(context.Background(), meta.ID)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if target.TmuxSession != m.current.info.SessionName || target.Generation != 1 {
		t.Fatalf("target = %+v", target)
	}
	if live.PID != m.current.info.PID || live.SessionID != m.current.info.SessionID {
		t.Fatalf("live identity drift: %+v vs %+v", live, m.current.info)
	}

	// A tampered captured tuple must not verify: full-identity check.
	m.mu.Lock()
	saved := m.current.info.ProcessGroupID
	m.current.info.ProcessGroupID = saved + 1
	m.mu.Unlock()
	if _, _, err := m.Verify(context.Background(), meta.ID); !errors.Is(err, terminal.ErrTargetIdentityMismatch) {
		t.Fatalf("tampered record verified: %v", err)
	}
	m.mu.Lock()
	m.current.info.ProcessGroupID = saved
	m.mu.Unlock()

	// Once cancelled, the target is not running and cannot be attached.
	if err := m.Cancel(context.Background(), "operator-a", meta.ID); err != nil {
		t.Fatal(err)
	}
	if err := m.Open(context.Background(), meta.ID); !errors.Is(err, terminal.ErrTargetNotRunning) {
		t.Fatalf("open cancelled: %v", err)
	}
}

// A restart drops authority: a fresh manager over the same namespace adopts
// nothing and kills nothing.
func TestRestartDropsAuthority(t *testing.T) {
	m := newTestManager(t)
	m.statusFn = noAccount
	fakeGhOnPath(t, "sleep 300\n")
	if _, err := m.Start(context.Background(), "operator-a"); err != nil {
		t.Fatal(err)
	}
	fresh := NewManager(m.tmux, m.gate, zerolog.Nop())
	fresh.statusFn = noAccount
	if _, ok := fresh.Current(context.Background(), "operator-a"); ok {
		t.Fatal("fresh manager adopted the leftover attempt")
	}
	if err := fresh.Cancel(context.Background(), "operator-a", "any-id"); !errors.Is(err, ErrUnknownAttempt) {
		t.Fatalf("fresh manager acted on unknown id: %v", err)
	}
	// The leftover pane was not killed by the fresh incarnation.
	sessions, err := m.tmux.ListSessions(context.Background())
	if err != nil || len(sessions) == 0 {
		t.Fatalf("leftover pane was killed or unobservable: %v %v", sessions, err)
	}
}
