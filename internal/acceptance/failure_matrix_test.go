package acceptance

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/noisefloor-oss/deck/internal/tmux"
)

// writeFailureMatrix proves write honesty on the real WS -> terminal ->
// tmux -> Claude path. Each outcome is observed on the wire and checked
// against what really happened at the tmux boundary:
//
//   - SENT: a normal write is acknowledged only after tmux took the bytes.
//   - FAILED: a provable pre-write rejection. A frame carrying a generation
//     other than the live session's is refused under the session write gate
//     BEFORE any byte crosses the tmux boundary. The marker
//     never reaches the native terminal.
//   - UNCERTAIN: an unverifiable live write. A test-only fault makes the
//     single serialized writer fail at the write boundary. The browser is told
//     the input may not have reached the CLI — never that it was delivered —
//     and nothing is replayed. The user inspects the native
//     TUI, then re-issues on a fresh attachment and the marker lands once.
//   - FENCED: a deliberate takeover displaces the live controller; the
//     displaced browser's keystrokes are refused. The marker never
//     reaches the native terminal.
func (v *verticalSlice) writeFailureMatrix() {
	t := v.e.t
	t.Helper()
	t.Log("criterion 6: sent/failed/uncertain/fenced outcomes stay honest and are never replayed")

	// --- Sent: tmux acknowledged the write before the frame is emitted.
	const sentMarker = "SENT-WRITE-3f17"
	v.browser.input([]byte(sentMarker))
	f := v.browser.awaitStatus(outcomeSent, 10*time.Second)
	if f.Code != outcomeSent {
		t.Fatalf("write outcome = %q, want %q", f.Code, outcomeSent)
	}
	if !v.awaitVisible(sentMarker, 10*time.Second) {
		t.Fatal("acknowledged marker never reached the native terminal")
	}
	// Submit and let the turn finish so the marker cannot linger in the
	// composer and pollute the markers typed by later steps.
	v.browser.input([]byte{'\r'})
	v.waitForClaudeIdle(90 * time.Second)

	// --- Failed: a provable pre-write rejection, refused before any byte
	// crosses the tmux boundary. The frame carries a stale
	// generation, so the attachment ends with a failed outcome, not a claim.
	const failedMarker = "STALE-GENERATION-9b0d"
	raw, _ := json.Marshal(clientFrame{
		Type:       frameInput,
		Token:      v.browser.token,
		Generation: v.browser.gen + 1,
		Data:       base64.StdEncoding.EncodeToString([]byte(failedMarker)),
	})
	if err := v.browser.writeMessage(raw); err != nil {
		t.Fatalf("send stale-generation frame: %v", err)
	}
	f = v.browser.awaitStatus(outcomeFailed, 10*time.Second)
	if f.Code != outcomeFailed {
		t.Fatalf("write outcome = %q, want %q", f.Code, outcomeFailed)
	}
	if strings.Contains(f.Message, "delivered") || strings.Contains(f.Message, "sent") {
		t.Fatalf("failed frame overclaims delivery: %q", f.Message)
	}
	time.Sleep(500 * time.Millisecond)
	if strings.Contains(v.captureVisible(), failedMarker) {
		t.Fatal("refused marker crossed the tmux boundary")
	}
	v.faultAbsent = append(v.faultAbsent, failedMarker)

	// The rejection ended the attachment (server closes after a refusal).
	v.browser.closeAndWait(5 * time.Second)

	// --- Uncertain: an unverifiable live write. The fault is consumed by the
	// single serialized writer at the tmux boundary and returns an error
	// without proving zero bytes crossed (a partial write did).
	const uncertainMarker = "UNCERTAIN-WRITE-c42e"
	v.browser = v.e.attach(v.session.ID)
	v.e.h.Terminal.SetWriteFaultForTest(func(att *tmux.Attachment, data []byte) (int, error) {
		// A genuine prefix reaches the PTY, so this failure cannot prove
		// zero bytes crossed — exactly the case that must be UNCERTAIN.
		n, _ := att.WriteWithin(data[:len(data)/2], 5*time.Second)
		return n, fmt.Errorf("injected unverifiable write")
	})
	v.browser.input([]byte(uncertainMarker))

	// The fault is consumed one-shot by the writer, but only when the server
	// processes the frame — disarming before the outcome arrives would race
	// that write and turn an uncertain outcome into a clean sent.
	f = v.browser.awaitStatus(outcomeUncertain, 10*time.Second)
	v.e.h.Terminal.SetWriteFaultForTest(nil)
	if f.Code != outcomeUncertain {
		t.Fatalf("write outcome = %q, want %q", f.Code, outcomeUncertain)
	}
	if strings.Contains(f.Message, "delivered") || strings.Contains(f.Message, "sent") {
		t.Fatalf("uncertain frame overclaims delivery: %q", f.Message)
	}
	// The attachment ended with the failed writer. Nothing is replayed and the
	// full marker must not have landed on its own.
	time.Sleep(300 * time.Millisecond)
	if strings.Contains(v.captureVisible(), uncertainMarker) {
		t.Fatal("uncertain write still reached the native terminal")
	}

	// User inspection ends with re-issuing the input on a fresh
	// attachment. From here the marker lands exactly once.
	v.browser = v.e.attach(v.session.ID)
	v.browser.input([]byte(uncertainMarker))
	if !v.awaitVisible(uncertainMarker, 10*time.Second) {
		t.Fatal("re-issued marker never reached the native terminal")
	}
	v.browser.input([]byte{'\r'})
	v.waitForClaudeIdle(90 * time.Second)
	v.faultDelivered = uncertainMarker

	// --- Fenced: a deliberate takeover displaces the live controller before
	// its keystrokes can cross.
	const fencedMarker = "FENCED-REJECTED-8ad1"
	takeover := v.e.attachTakeover(v.session.ID)
	f = v.browser.awaitFrame(frameError, 10*time.Second)
	if f.Code != errFenced {
		t.Fatalf("displaced browser error code = %q, want %q", f.Code, errFenced)
	}
	// The displaced controller's keystrokes cannot cross once control moved:
	// either the server refuses them (failed) or the socket is already gone.
	// Either way the marker must not land.
	v.browser.tryInput([]byte(fencedMarker))
	time.Sleep(500 * time.Millisecond)
	if strings.Contains(v.captureVisible(), fencedMarker) {
		t.Fatal("displaced controller's keystroke crossed the tmux boundary after takeover")
	}
	v.faultAbsent = append(v.faultAbsent, fencedMarker)

	// The takeover browser is the user's view now.
	old := v.browser
	v.browser = takeover
	old.closeAndWait(5 * time.Second)
}
