package acceptance

import (
	"bytes"
	"strings"
	"syscall"
	"time"
)

// Criterion 7, the crash row of the failure matrix: kill the real CLI
// outside the control API, let reconciliation observe the preserved
// terminal evidence, then let the automatic bounded recovery resume the
// exact native ID into a new generation with no user action, and export
// native history to prove continuity and no prompt replay.
func (v *verticalSlice) crashAndResumeNativeSession() {
	t := v.e.t
	t.Helper()
	t.Log("criterion 7: observed CLI crash and exact native-session resume without replay")

	beforeHistory := v.exportConversation("criterion-7-before")
	v.assertNativeWitnessCounts(beforeHistory)

	before := requireRuntimeIdentity(t, v.e.getSession(v.session.ID))
	v.browser.closeAndWait(5 * time.Second)
	if err := syscall.Kill(-before.ProcessGroupID, syscall.SIGKILL); err != nil {
		t.Fatalf("crash Claude process group %d: %v", before.ProcessGroupID, err)
	}

	// No user action — the reconciler observes the crash and its
	// bounded automatic recovery resumes the exact native id itself. The
	// transient exited state can be shorter than a poll, so the wait targets
	// the recovered generation and the audit trail is the witness that the
	// exit was observed and recorded first.
	v.session = v.e.awaitSession(v.session.ID, "automatically resumed generation", func(s sessionJSON) bool {
		return s.Status == "running" && s.Generation == before.Generation+1 && s.PanePID != nil
	})
	if processAlive(before.PanePID) {
		t.Fatalf("old Claude pid %d is still alive after automatic resume", before.PanePID)
	}
	v.e.requireAuditEvent(t, v.session.ID, "session.exited")
	v.e.requireAuditEvent(t, v.session.ID, "session.resume_attempt")
	v.e.requireAuditEvent(t, v.session.ID, "session.resumed")
	after := requireRuntimeIdentity(t, v.session)
	if after.Generation != before.Generation+1 {
		t.Fatalf("resume generation = %d, want %d", after.Generation, before.Generation+1)
	}
	if after.NativeSessionID != before.NativeSessionID {
		t.Fatalf("resume native id = %q, want exact %q", after.NativeSessionID, before.NativeSessionID)
	}
	if after.TmuxSocketPath != before.TmuxSocketPath {
		t.Fatalf("native resume changed the isolated tmux socket: before %q after %q",
			before.TmuxSocketPath, after.TmuxSocketPath)
	}
	if after.TmuxSessionName == before.TmuxSessionName ||
		after.TmuxSessionID == before.TmuxSessionID ||
		after.TmuxPaneID == before.TmuxPaneID {
		t.Fatalf("native resume reused the predecessor tmux identity\nbefore: %+v\n after: %+v", before, after)
	}
	if after.IdentityMarker == before.IdentityMarker {
		t.Fatal("native resume reused the prior generation's identity marker")
	}
	if after.PanePID == before.PanePID && after.StartTicks == before.StartTicks {
		t.Fatalf("native resume reused the complete old process identity: before pid/ticks=%d/%d after=%d/%d",
			before.PanePID, before.StartTicks, after.PanePID, after.StartTicks)
	}
	if !processAlive(after.PanePID) {
		t.Fatalf("resumed Claude pid %d is not alive", after.PanePID)
	}
	cwd, err := processCWD(after.PanePID)
	if err != nil || cwd != v.e.dir {
		t.Fatalf("resumed Claude cwd = %q (%v), want %q", cwd, err, v.e.dir)
	}
	assertClaudeOpusArg(t, after.PanePID)

	v.browser = v.e.attach(v.session.ID)
	v.waitForClaudeReady()
	afterHistory := v.exportConversation("criterion-7-after")
	v.assertNativeWitnessCounts(afterHistory)
}

// assertNativeWitnessCounts cross-checks the exported native conversation after
// every lifecycle phase. Markers must appear exactly as many times as they
// were genuinely delivered: rejected markers zero times, delivered ones once.
func (v *verticalSlice) assertNativeWitnessCounts(history []byte) {
	v.e.t.Helper()
	for name, marker := range map[string]string{
		"continuity reply": v.continuityReply,
		"active-turn body": v.activeMarker,
	} {
		if got := bytes.Count(history, []byte(marker)); got != 1 {
			v.e.t.Fatalf("native history contains %s %d times, want exactly 1", name, got)
		}
	}
	if v.faultDelivered != "" {
		// Claude's reply may quote the re-issued marker back (observed with
		// 2.1.220), so a whole-export count is not the delivered-once claim.
		// What must appear exactly once is the user's input itself: count
		// user-turn lines (export prefixes them with "❯ ") only.
		got := 0
		for line := range bytes.Lines(history) {
			if bytes.HasPrefix(line, []byte("❯ ")) {
				got += bytes.Count(line, []byte(v.faultDelivered))
			}
		}
		if got != 1 {
			v.e.t.Fatalf("native history contains re-issued post-fault marker %q %d times across user turns, want exactly 1",
				v.faultDelivered, got)
		}
	}
	for _, marker := range v.faultAbsent {
		if got := bytes.Count(history, []byte(marker)); got != 0 {
			v.e.t.Fatalf("native history contains rejected marker %q %d times, want 0", marker, got)
		}
	}
}

func (v *verticalSlice) awaitNotVisible(marker string, timeout time.Duration) bool {
	v.e.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !strings.Contains(v.captureVisible(), marker) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}
