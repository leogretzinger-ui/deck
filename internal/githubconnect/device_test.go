package githubconnect

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

// Screens captured from gh 2.102.0 `auth login --hostname github.com
// --git-protocol https --web` in an 80x24 tmux pane, after Enter answered
// the Git question. The second ran with --clipboard=true and no clipboard.
const (
	gh2102Question = "? Authenticate Git with your GitHub credentials? (Y/n)\n\n\n"
	gh2102Code     = `? Authenticate Git with your GitHub credentials? Yes

! First copy your one-time code: 2DFA-5170
Press Enter to open https://github.com/login/device in your browser...

`
	gh2102ClipboardWarning = `? Authenticate Git with your GitHub credentials? Yes

! Failed to copy one-time code to clipboard
  No clipboard utilities available. Please install xsel, xclip, wl-clipboard or
Termux:API add-on for termux-clipboard-get/set.
! First copy your one-time code: 47B6-D760
Press Enter to open https://github.com/login/device in your browser...

`
)

func TestDeviceCodeFromGh2102Output(t *testing.T) {
	for _, tc := range []struct {
		name, screen, code string
		question           bool
	}{
		{"question", gh2102Question, "", true},
		{"code", gh2102Code, "2DFA-5170", false},
		{"clipboard-warning", gh2102ClipboardWarning, "47B6-D760", false},
		{"no-code-yet", "? Authenticate Git with your GitHub credentials? Yes\n", "", false},
		{"lowercase-is-not-a-code", "! First copy your one-time code: abcd-efgh\n", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := deviceCode(tc.screen); got != tc.code {
				t.Fatalf("deviceCode = %q, want %q", got, tc.code)
			}
			if got := awaitsGitAnswer(tc.screen); got != tc.question {
				t.Fatalf("awaitsGitAnswer = %v, want %v", got, tc.question)
			}
		})
	}
}

// The fake gh replays gh 2.102.0's prompts and reads the tty like gh does:
// sign-in only completes if Deck answered both prompts on its own.
func TestSignInFinishesWithoutTerminalInput(t *testing.T) {
	m := newTestManager(t)
	var logs bytes.Buffer
	m.log = zerolog.New(&logs)
	done := filepath.Join(t.TempDir(), "approved")
	t.Setenv("DECK_TEST_GH_DONE", done)
	m.statusFn = func(context.Context) Result {
		if _, err := os.Stat(done); err == nil {
			return Result{Outcome: OutcomeAuthenticated, Status: &Status{Account: Account{
				Login: "octo", State: StateSuccess, Active: true, Source: SourceStored, SourceWriteable: true}}}
		}
		return Result{Outcome: OutcomeNoAccount}
	}
	fakeGhOnPath(t, `test "$GH_BROWSER" = true || exit 97
printf '? Authenticate Git with your GitHub credentials? (Y/n) '
read answer
printf '? Authenticate Git with your GitHub credentials? Yes\n'
printf '! First copy your one-time code: 2DFA-5170\n'
printf 'Press Enter to open https://github.com/login/device in your browser... '
read enter
sleep 1
printf approved > "$DECK_TEST_GH_DONE"
`)
	meta, err := m.Start(context.Background(), "operator-a")
	if err != nil {
		t.Fatal(err)
	}
	if meta.Code != "" {
		t.Fatalf("code before gh printed one: %q", meta.Code)
	}
	sawCode := false
	deadline := time.Now().Add(10 * time.Second)
	for {
		cur, ok := m.Current(context.Background(), "operator-a")
		if !ok {
			t.Fatal("attempt vanished")
		}
		sawCode = sawCode || cur.Code == "2DFA-5170"
		// A tmux read can fail while the pane exits; only exited ends the wait.
		if cur.Status != AttemptRunning && cur.Status != AttemptUnavailable {
			if cur.Status != AttemptExited || cur.Code != "" {
				t.Fatalf("ended attempt = %+v", cur)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("sign-in never exited: %+v", cur)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !sawCode {
		t.Fatal("the running attempt never reported the device code")
	}
	if res := m.Status(context.Background()); res.Outcome != OutcomeAuthenticated || res.Status.Account.Login != "octo" {
		t.Fatalf("status after sign-in = %+v", res)
	}
	if strings.Contains(logs.String(), "2DFA-5170") {
		t.Fatalf("device code reached the log: %s", logs.String())
	}
}

// Show details on a phone narrows the pane before gh prints anything; both
// prompts then wrap across rows and must still be recognised.
func TestSignInFinishesInANarrowPane(t *testing.T) {
	m := newTestManager(t)
	m.statusFn = noAccount
	done := filepath.Join(t.TempDir(), "approved")
	t.Setenv("DECK_TEST_GH_DONE", done)
	fakeGhOnPath(t, `until [ "$(stty size | cut -d' ' -f2)" = 32 ]; do sleep 0.05; done
printf '? Authenticate Git with your GitHub credentials? (Y/n) '
read answer
printf '? Authenticate Git with your GitHub credentials? Yes\n'
printf '! First copy your one-time code: 2DFA-5170\n'
printf 'Press Enter to open https://github.com/login/device in your browser... '
read enter
printf approved > "$DECK_TEST_GH_DONE"
sleep 300
`)
	if _, err := m.Start(context.Background(), "operator-a"); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	pane := m.current.info.PaneID
	m.mu.Unlock()
	if out, err := exec.Command("tmux", "-S", m.tmux.SocketPath(), "resize-window", "-t", pane, "-x", "32", "-y", "24").CombinedOutput(); err != nil {
		t.Fatalf("resize-window: %v: %s", err, out)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(done); err == nil {
			break
		}
		if time.Now().After(deadline) {
			screen, _ := m.tmux.CapturePane(context.Background(), pane)
			t.Fatalf("sign-in stalled in a 32-column pane:\n%s", screen)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if cur, _ := m.Current(context.Background(), "operator-a"); cur.Code != "2DFA-5170" {
		t.Fatalf("code = %q, want 2DFA-5170", cur.Code)
	}
}

// A lost acknowledgement leaves the question on screen while gh already has
// the answer; Deck must not answer it again or press on by itself.
func TestLostGitAnswerAcknowledgementIsNeverResent(t *testing.T) {
	m := newTestManager(t)
	m.statusFn = noAccount
	done := filepath.Join(t.TempDir(), "approved")
	t.Setenv("DECK_TEST_GH_DONE", done)
	var mu sync.Mutex
	sends := 0
	m.sendKey = func(ctx context.Context, paneID, key string) error {
		mu.Lock()
		sends++
		mu.Unlock()
		if err := m.tmux.SendKey(ctx, paneID, key); err != nil {
			return err
		}
		return errors.New("acknowledgement lost")
	}
	fakeGhOnPath(t, `printf '? Authenticate Git with your GitHub credentials? (Y/n) '
read answer
sleep 1
printf '\n! First copy your one-time code: 2DFA-5170\n'
printf 'Press Enter to open https://github.com/login/device in your browser... '
read enter
printf approved > "$DECK_TEST_GH_DONE"
sleep 300
`)
	if _, err := m.Start(context.Background(), "operator-a"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2500 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if sends != 1 {
		t.Fatalf("Enter sent %d times, want 1", sends)
	}
	if _, err := os.Stat(done); err == nil {
		t.Fatal("Deck pressed on after an unconfirmed answer")
	}
}
