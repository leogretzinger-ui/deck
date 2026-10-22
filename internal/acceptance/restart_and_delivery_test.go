package acceptance

import (
	"bytes"
	"context"
	"strings"
	"sync/atomic"
	"time"

	"github.com/noisefloor-oss/deck/internal/tmux"
)

// Criteria 4 and 5 compose deliberately: while a real Claude approval modal
// is open, direct WS keystrokes flow straight through to the native TUI —
// Deck neither holds nor suppresses input — then both the modal and
// the live tmux identity survive a Go service restart with NO replay of
// anything typed.
func (v *verticalSlice) restartPreservesIdentityAndModalInput() {
	t := v.e.t
	t.Helper()
	t.Log("criteria 4-5: service restart preserves CLI identity and direct modal input is never held or replayed")

	before := requireRuntimeIdentity(t, v.e.getSession(v.session.ID))

	// The native Bash approval modal is opened by typing the command directly
	// through the live fenced WS — the only input path that exists now.
	v.browser.input([]byte("printf modal-probe-4e91 > modal-probe.txt\r"))
	if !v.awaitVisible("Do you want to proceed?", 90*time.Second) {
		acceptanceUnavailable(t, "Claude did not present the required native Bash approval modal")
	}

	// While the modal is open, keystrokes reach the native TUI immediately.
	// The tmux write observer counts the writes they cause: the writer MUST
	// fire, because nothing is held or buffered.
	const modalMarker = "MODAL-KEYSTROKES-88cf"
	var writes atomic.Int64
	observeWrites := func(observation tmux.WriteObservation) {
		if observation.PaneID == before.TmuxPaneID {
			writes.Add(1)
		}
	}
	v.e.h.Tmux.SetWriteObserverForTest(observeWrites)
	v.browser.input([]byte(modalMarker))
	deadline := time.Now().Add(5 * time.Second)
	for writes.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if writes.Load() == 0 {
		t.Fatal("keystrokes typed during the approval modal never reached the tmux writer (input was held)")
	}
	// The modal consumed the marker letters without echoing them — that is the
	// native TUI's business (Claude 2.1.220's Yes/No dialog has no text
	// field). The observable proof that keystrokes reach the native TUI is
	// the dialog itself reacting: an arrow key moves its selection. The "❯
	// 2. No" layout is 2.1.220's; a CLI upgrade that changes the dialog must
	// re-derive this assertion from a live capture.
	v.browser.input([]byte{0x1b, '[', 'B'}) // Down arrow
	if !v.awaitVisible("❯ 2. No", 10*time.Second) {
		t.Fatal("keystrokes typed during the approval modal never reached the native TUI")
	}

	// The user does NOT answer the modal yet. Restart ONLY Deck
	// with the modal still open: the pane, provider PID, and modal state are
	// owned by tmux and the CLI, not the service, so they must survive.
	// Nothing typed is replayed: the write count must not move while
	// the service is down or coming up.
	v.browser.closeAndWait(5 * time.Second)
	writesBefore := writes.Load()
	v.e.stopService()
	if !processAlive(before.PanePID) {
		t.Fatalf("CLI pid %d died when only Deck stopped", before.PanePID)
	}
	v.e.startService(observeWrites)
	time.Sleep(2 * time.Second)
	if writes.Load() != writesBefore {
		t.Fatalf("restart re-injected %d writes of prior direct input", writes.Load()-writesBefore)
	}

	v.session = v.e.awaitSession(v.session.ID, "running after service restart", func(s sessionJSON) bool {
		return s.Status == "running" && s.PanePID != nil
	})
	after := requireRuntimeIdentity(t, v.session)
	assertSameRuntimeIdentity(t, before, after, "service restart")
	v.e.h.Tmux.SetWriteObserverForTest(nil)

	v.browser = v.e.attach(v.session.ID)
	if !v.awaitVisible("Do you want to proceed?", 20*time.Second) {
		t.Fatal("native approval modal did not survive the service restart")
	}
	// The user re-issues anything lost after inspecting the native TUI.
	// Dismiss the surviving modal, then confirm the modal-only keystrokes
	// never entered native history.
	v.browser.input([]byte{0x1b})
	if !v.awaitNotVisible("Do you want to proceed?", 10*time.Second) {
		t.Fatal("user Escape did not dismiss Claude's approval modal after restart")
	}
	v.waitForClaudeIdle(90 * time.Second)
	history := v.exportConversation("criterion-5")
	if bytes.Contains(history, []byte(modalMarker)) {
		t.Fatal("modal-only keystrokes entered native history (dismissal or replay bug)")
	}
}

// Criterion 5's active-turn claim: while Claude is already generating a long
// turn, keystrokes and a bracketed paste sent through the live fenced WS reach
// the native TUI immediately. Claude's OWN queue handles input submitted during
// an active turn — Deck does not buffer it. The native queue
// indicator plus the unique marker's single appearance in the complete exported
// conversation prove both timing and no-buffering.
func (v *verticalSlice) sendNowDuringActiveTurnExactlyOnce() {
	t := v.e.t
	t.Helper()
	t.Log("criterion 5: active-turn keystrokes and paste appear exactly once with no Deck buffering")

	const activeStart = "ACTIVE-TURN-START-7c9a"
	const activeEnd = "ACTIVE-TURN-END-7c9a"
	activePrompt := "Write at least 700 words about terminal multiplexers. " +
		"Begin with the words ACTIVE TURN START 7c9a joined by hyphens. " +
		"Finish with the words ACTIVE TURN END 7c9a joined by hyphens."
	if strings.Contains(activePrompt, activeStart) || strings.Contains(activePrompt, activeEnd) {
		t.Fatal("active-turn witnesses must not occur verbatim in their prompt")
	}
	// The long turn is launched by direct WS typing — no /messages API exists.
	v.browser.input([]byte(activePrompt))
	v.browser.input([]byte{'\r'})
	deadline := time.Now().Add(90 * time.Second)
	for {
		out := v.browser.outputSnapshot()
		visible := v.captureVisible()
		v.e.skipIfOutOfCredits(visible)
		if strings.Contains(out, activeEnd) || strings.Contains(visible, activeEnd) {
			t.Fatal("Claude completed the setup turn before an active-turn window was observed")
		}
		if strings.Contains(out, activeStart) || strings.Contains(visible, activeStart) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Claude never generated the active-turn start witness")
		}
		time.Sleep(100 * time.Millisecond)
	}

	v.activeMarker = "ACTIVE-INPUT-MARKER-3d9c"
	// Keystrokes first: the unique marker is typed character by character.
	for _, b := range []byte(v.activeMarker) {
		v.browser.input([]byte{b})
	}
	if !v.awaitVisible(v.activeMarker, 10*time.Second) {
		t.Fatal("active-turn keystrokes never appeared in Claude's native composer")
	}
	// Then the instruction arrives as one bracketed paste while Claude is still
	// working — both paths share the single serialized writer.
	paste := "\x1b[200~" + "\nAfter the current turn, do not repeat the first line. " +
		"Reply only with the word QUEUED, a hyphen, and the digits 3 d 9 c without spaces." +
		"\x1b[201~"
	v.browser.input([]byte(paste))
	v.browser.input([]byte{'\r'})

	// Claude's native queue indicator is its own admission that input arrived
	// during the active turn; Deck never queue-ed it.
	v.awaitOutput("Press up to edit queued messages", 15*time.Second)
	v.awaitOutput("QUEUED-3d9c", 180*time.Second)

	history := v.exportConversation("criterion-5-active")
	if got := bytes.Count(history, []byte(v.activeMarker)); got != 1 {
		t.Fatalf("native history contains active-turn marker %d times, want exactly 1", got)
	}
	if got := bytes.Count(history, []byte("QUEUED-3d9c")); got != 1 {
		t.Fatalf("native history contains queued reply %d times, want exactly 1", got)
	}
}

func (v *verticalSlice) captureVisible() string {
	v.e.t.Helper()
	s := v.e.getSession(v.session.ID)
	if s.TmuxPaneID == nil {
		v.e.t.Fatal("running session has no tmux pane id")
	}
	pane, err := v.e.h.Tmux.CapturePane(context.Background(), *s.TmuxPaneID)
	if err != nil {
		v.e.t.Fatalf("capture visible native terminal: %v", err)
	}
	return pane
}

func (v *verticalSlice) awaitVisible(marker string, timeout time.Duration) bool {
	v.e.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		visible := v.captureVisible()
		v.e.skipIfOutOfCredits(visible)
		if strings.Contains(visible, marker) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}
