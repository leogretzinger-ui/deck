package acceptance

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/noisefloor-oss/deck/internal/tmux"
)

// verticalSlice owns the one real Claude session that must demonstrate all
// seven vertical-slice criteria. Splitting the criteria across fresh sessions
// would miss failures that appear only after restart, write failure, or
// resume.
type verticalSlice struct {
	e       *env
	session sessionJSON
	browser *browser

	continuityPrompt string
	continuityReply  string
	activeMarker     string
	faultAbsent      []string
	faultDelivered   string
}

// TestVerticalSlice_Claude is the vertical-slice release gate.
// It is intentionally linear and non-parallel: a failure in an earlier
// lifecycle phase invalidates every later claim.
func TestVerticalSlice_Claude(t *testing.T) {
	if os.Getenv(strictAcceptanceEnv) != "1" {
		t.Skip("real-Claude release gate is opt-in; run make acceptance")
	}
	e := newEnv(t)
	e.requireCLI("claude")
	v := &verticalSlice{e: e}

	v.launchAndDriveFromBrowser()
	v.detachAndReattach()
	v.restartPreservesIdentityAndModalInput()
	v.writeFailureMatrix()
	v.sendNowDuringActiveTurnExactlyOnce()
	v.crashAndResumeNativeSession()
}

// Criteria 1 and 2: launch in the selected directory, then prove raw browser
// input, Enter, output, and resize against the real CLI. The generated reply
// becomes the continuity witness used again after native resume.
func (v *verticalSlice) launchAndDriveFromBrowser() {
	t := v.e.t
	t.Helper()
	t.Log("criterion 1-2: launch in project and full interactive browser control")

	projectID := v.e.registerProject()
	v.session = v.e.createSession(projectID, "claude")
	v.session = v.e.awaitSession(v.session.ID, "running with complete identity", func(s sessionJSON) bool {
		return s.Status == "running" && s.PanePID != nil && s.TmuxPaneID != nil
	})
	identity := requireRuntimeIdentity(t, v.session)
	if !processAlive(identity.PanePID) {
		t.Fatalf("recorded CLI pid %d is not alive", identity.PanePID)
	}
	cwd, err := processCWD(identity.PanePID)
	if err != nil {
		t.Fatalf("read cwd of pid %d: %v", identity.PanePID, err)
	}
	if cwd != v.e.dir {
		t.Fatalf("CLI cwd = %q, want project dir %q", cwd, v.e.dir)
	}
	if identity.NativeSessionID != v.session.ID {
		t.Fatalf("Claude native session id = %q, want assigned id %q",
			identity.NativeSessionID, v.session.ID)
	}
	assertClaudeOpusArg(t, identity.PanePID)

	v.browser = v.e.attach(v.session.ID)
	v.waitForClaudeReady()
	v.browser.resize(132, 43)
	// The status line is off, so the pane takes the full 43 rows.
	v.e.awaitPaneSize(identity.TmuxPaneID, 132, 43, 10*time.Second)

	v.continuityPrompt = "Reply with exactly these chunks joined by hyphens: " +
		"VERTICAL, CONTINUITY, and the digits 9 1 c 4 without spaces. Add nothing else."
	v.continuityReply = "VERTICAL-CONTINUITY-91c4"
	if strings.Contains(v.continuityPrompt, v.continuityReply) {
		t.Fatal("continuity witness must not occur verbatim in its prompt")
	}

	// Text and Enter travel as separate real WS frames so a broken control-key
	// path cannot hide behind a printable echo.
	v.browser.input([]byte(v.continuityPrompt))
	if !v.browser.outputContains("VERTICAL, CONTINUITY", 10*time.Second) {
		t.Fatal("raw browser text never appeared in Claude's composer")
	}
	v.browser.input([]byte{'\r'})
	v.awaitOutput(v.continuityReply, 90*time.Second)

	history := v.exportConversation("criterion-2")
	if got := bytes.Count(history, []byte(v.continuityReply)); got != 1 {
		t.Fatalf("native history contains continuity reply %d times, want 1", got)
	}
}

// A fresh temp project can trigger Claude's documented native trust picker.
// The acceptance browser answers it exactly as a user would; input sent
// before this point would be silently discarded by the measured CLI contract.
func (v *verticalSlice) waitForClaudeReady() {
	v.e.t.Helper()
	trustVisible := func(pane string) bool {
		return strings.Contains(pane, "project you created or one you trust") ||
			strings.Contains(pane, "trust the files in this folder") ||
			strings.Contains(pane, "trust this folder")
	}
	const stableReadySamples = 15
	deadline := time.Now().Add(20 * time.Second)
	readySamples := 0
	var answeredTrust time.Time
	var last string
	for time.Now().Before(deadline) {
		last = v.captureVisible()
		lower := strings.ToLower(last)
		v.e.skipIfOutOfCredits(last)
		if trustVisible(lower) {
			readySamples = 0
			// 2.1.261 places the cursor on "No, exit" first; Enter there
			// ends the process instead of trusting the folder. Each pass
			// reads where the cursor is and sends the one key that moves
			// the picker forward: Down while it sits on the exit row, Enter
			// once it sits on the affirmative one. A key that lands before
			// the picker accepts input is simply sent again next pass.
			// One key per second: a capture can lag the key that moved the
			// cursor, and a second Down read off a stale screen would wrap
			// the picker back onto the exit row just before Enter.
			if time.Since(answeredTrust) > time.Second {
				if trustCursorOnExit(last) {
					v.browser.input([]byte{0x1b, '[', 'B'})
				} else {
					v.e.t.Log("answering Claude's native startup trust picker")
					v.browser.input([]byte{'\r'})
				}
				answeredTrust = time.Now()
			}
			time.Sleep(100 * time.Millisecond)
			continue
		}

		// The pinned Claude UI identifies both itself and the forced model in
		// its ready screen. Requiring a stable interval prevents the prompt
		// from racing a trust picker that has not rendered yet.
		if strings.Contains(lower, "for shortcuts") ||
			(strings.Contains(lower, "claude code") && strings.Contains(lower, "opus")) {
			readySamples++
			if readySamples >= stableReadySamples {
				return
			}
		} else {
			readySamples = 0
		}
		time.Sleep(100 * time.Millisecond)
	}
	v.e.t.Fatalf("Claude never reached a stable ready screen; visible length=%d states=%v",
		len(last), terminalStateLabels(last))
}

// trustCursorOnExit reports whether the trust picker's cursor row is the
// exit choice, which is the row Enter would confirm.
func trustCursorOnExit(pane string) bool {
	for _, line := range strings.Split(pane, "\n") {
		if strings.Contains(line, "❯") {
			return strings.Contains(strings.ToLower(line), "no, exit")
		}
	}
	return false
}

// Criterion 3: closing the browser must end only that tmux client. The full
// runtime identity tuple, not merely a reusable numeric PID, stays unchanged.
func (v *verticalSlice) detachAndReattach() {
	t := v.e.t
	t.Helper()
	t.Log("criterion 3: browser detach and reattach preserve the full CLI identity")

	before := requireRuntimeIdentity(t, v.e.getSession(v.session.ID))
	v.browser.closeAndWait(5 * time.Second)
	if !processAlive(before.PanePID) {
		t.Fatalf("CLI pid %d died with browser detach", before.PanePID)
	}
	afterDetach := requireRuntimeIdentity(t, v.e.getSession(v.session.ID))
	assertSameRuntimeIdentity(t, before, afterDetach, "browser detach")

	v.browser = v.e.attach(v.session.ID)
	v.awaitOutput(v.continuityReply, 20*time.Second)
	afterAttach := requireRuntimeIdentity(t, v.e.getSession(v.session.ID))
	assertSameRuntimeIdentity(t, before, afterAttach, "browser reattach")
}

func (v *verticalSlice) awaitOutput(marker string, timeout time.Duration) {
	v.e.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		out := v.browser.outputSnapshot()
		visible := v.captureVisible()
		v.e.skipIfOutOfCredits(visible)
		if strings.Contains(out, marker) || strings.Contains(visible, marker) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	out := v.browser.outputSnapshot()
	visible := v.captureVisible()
	v.e.t.Fatalf("terminal output never contained %q; raw length=%d visible length=%d states=%v",
		marker, len(out), len(visible), terminalStateLabels(out+"\n"+visible))
}

func terminalStateLabels(output string) []string {
	lower := strings.ToLower(output)
	markers := []struct {
		label  string
		needle string
	}{
		{"trust-picker", "trust"},
		{"usage", "usage"},
		{"limit", "limit"},
		{"login", "login"},
		{"authentication", "authenticat"},
		{"invalid-model", "invalid model"},
		{"opus", "opus"},
		{"thinking", "thinking"},
		{"interrupt-hint", "to interrupt"},
		{"api-error", "api error"},
		{"continuity-words", "vertical"},
		{"idle-shortcut-hint", "for shortcuts"},
		{"approval", "do you want to proceed?"},
		{"export-command", "/export"},
		{"unknown-command", "unknown command"},
		{"loading", "loading"},
		{"resume", "resum"},
	}
	labels := make([]string, 0, len(markers))
	for _, marker := range markers {
		if strings.Contains(lower, marker.needle) {
			labels = append(labels, marker.label)
		}
	}
	return labels
}

// exportConversation invokes Claude's documented /export command through the
// real terminal. The returned file is an explicit native-history observation,
// not provider state-file parsing and not Deck's SQLite record.
func (v *verticalSlice) exportConversation(label string) []byte {
	t := v.e.t
	t.Helper()
	v.waitForClaudeIdle(20 * time.Second)
	path := filepath.Join(v.e.dir, "native-history-"+label+".txt")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove stale native export: %v", err)
	}
	identity := requireRuntimeIdentity(t, v.e.getSession(v.session.ID))
	var writeAttempts atomic.Int64
	v.e.h.Tmux.SetWriteObserverForTest(func(observation tmux.WriteObservation) {
		if observation.PaneID == identity.TmuxPaneID {
			writeAttempts.Add(1)
		}
	})
	defer v.e.h.Tmux.SetWriteObserverForTest(nil)

	v.browser.input([]byte("/export " + path))
	v.browser.input([]byte{'\r'})

	deadline := time.Now().Add(90 * time.Second)
	stable := 0
	var previous []byte
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		switch {
		case err == nil && len(data) > 0:
			if bytes.Equal(data, previous) {
				stable++
			} else {
				stable = 0
				previous = append(previous[:0], data...)
			}
			if stable >= 5 {
				return data
			}
		case err != nil && !os.IsNotExist(err):
			t.Fatalf("read native export: %v", err)
		}
		v.e.skipIfOutOfCredits(v.captureVisible())
		time.Sleep(100 * time.Millisecond)
	}
	visible := v.captureVisible()
	t.Fatalf("Claude did not create a stable native export %q; write attempts=%d "+
		"command visible=%t path visible=%t visible length=%d states=%v",
		filepath.Base(path), writeAttempts.Load(),
		strings.Contains(visible, "/export"), strings.Contains(visible, filepath.Base(path)),
		len(visible), terminalStateLabels(visible))
	return nil
}

// waitForClaudeIdle observes the native idle footer for a stable interval.
// Seeing a response marker is not enough: Claude may still be finishing a
// turn, in which case a slash command is queued as prompt input instead of
// executed as a command.
func (v *verticalSlice) waitForClaudeIdle(timeout time.Duration) {
	v.e.t.Helper()
	deadline := time.Now().Add(timeout)
	stable := 0
	var last string
	for time.Now().Before(deadline) {
		last = v.captureVisible()
		v.e.skipIfOutOfCredits(last)
		lower := strings.ToLower(last)
		if strings.Contains(lower, "for shortcuts") &&
			!strings.Contains(lower, "do you want to proceed?") {
			stable++
			if stable >= 10 {
				return
			}
		} else {
			stable = 0
		}
		time.Sleep(100 * time.Millisecond)
	}
	v.e.t.Fatalf("Claude never reached a stable idle prompt; visible length=%d states=%v",
		len(last), terminalStateLabels(last))
}

type runtimeIdentity struct {
	TmuxSocketPath     string
	TmuxSessionName    string
	TmuxSessionID      string
	TmuxPaneID         string
	Generation         int
	IdentityMarker     string
	PanePID            int
	ProcessGroupID     int
	StartTicks         int64
	ExpectedExecutable string
	NativeSessionID    string
}

func requireRuntimeIdentity(t *testing.T, s sessionJSON) runtimeIdentity {
	t.Helper()
	missing := make([]string, 0, 8)
	if s.TmuxSessionID == nil {
		missing = append(missing, "tmux_session_id")
	}
	if s.TmuxPaneID == nil {
		missing = append(missing, "tmux_pane_id")
	}
	if s.IdentityMarker == nil {
		missing = append(missing, "identity_marker")
	}
	if s.PanePID == nil {
		missing = append(missing, "pane_pid")
	}
	if s.ProcessGroupID == nil {
		missing = append(missing, "process_group_id")
	}
	if s.StartTicks == nil {
		missing = append(missing, "start_ticks")
	}
	if s.ExpectedExecutable == nil {
		missing = append(missing, "expected_executable")
	}
	if s.NativeSessionID == nil {
		missing = append(missing, "native_session_id")
	}
	if len(missing) > 0 {
		t.Fatalf("running session has incomplete identity tuple: %s", strings.Join(missing, ", "))
	}
	return runtimeIdentity{
		TmuxSocketPath:     s.TmuxSocketPath,
		TmuxSessionName:    s.TmuxSessionName,
		TmuxSessionID:      *s.TmuxSessionID,
		TmuxPaneID:         *s.TmuxPaneID,
		Generation:         s.Generation,
		IdentityMarker:     *s.IdentityMarker,
		PanePID:            *s.PanePID,
		ProcessGroupID:     *s.ProcessGroupID,
		StartTicks:         *s.StartTicks,
		ExpectedExecutable: *s.ExpectedExecutable,
		NativeSessionID:    *s.NativeSessionID,
	}
}

func assertSameRuntimeIdentity(t *testing.T, want, got runtimeIdentity, phase string) {
	t.Helper()
	if got != want {
		t.Fatalf("runtime identity changed across %s\nwant: %+v\n got: %+v", phase, want, got)
	}
}

func assertClaudeOpusArg(t *testing.T, pid int) {
	t.Helper()
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		t.Fatalf("read Claude argv: %v", err)
	}
	if !bytes.Contains(raw, []byte("--model\x00opus\x00")) {
		t.Fatal("acceptance Claude was not launched with --model opus")
	}
}
