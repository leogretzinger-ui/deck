package acceptance

import (
	"context"
	"database/sql"
	"net/http"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestClaudeRefusedResumeIsPresentedNotRepeated is the canary failure: a
// Claude session assigned an id at launch and interrupted before its first
// submission has no persisted conversation, so `claude --resume <id>` exits 1
// with "No conversation found". Deck must ask once, keep the record and the
// identity, present the refusal with Resume and Start fresh session as the
// user's explicit choices, and never relaunch a fresh conversation in the
// row on its own.
func TestClaudeRefusedResumeIsPresentedNotRepeated(t *testing.T) {
	if os.Getenv(strictAcceptanceEnv) != "1" {
		t.Skip("real-Claude refused-resume gate is opt-in; run make acceptance")
	}
	e := newEnv(t)
	e.requireCLI("claude")
	v := &verticalSlice{e: e}

	projectID := e.registerProject()
	v.session = e.createSession(projectID, "claude")
	v.session = e.awaitSessionFor(v.session.ID, "running Claude with complete identity",
		func(s sessionJSON) bool {
			return s.Status == "running" && s.PanePID != nil && s.TmuxPaneID != nil &&
				s.NativeSessionID != nil && s.ProcessGroupID != nil && s.StartTicks != nil
		}, 120*time.Second)
	birth := requireRuntimeIdentity(t, v.session)

	// Reach the prompt — answering the trust picker on the way, as a
	// user would — and submit nothing: Claude writes no ledger before
	// the first submission, so the assigned id names no conversation yet.
	v.browser = e.attach(v.session.ID)
	v.waitForClaudeReady()
	v.browser.closeAndWait(5 * time.Second)
	if err := syscall.Kill(-birth.ProcessGroupID, syscall.SIGKILL); err != nil {
		t.Fatalf("interrupt Claude process group %d: %v", birth.ProcessGroupID, err)
	}

	// No user action: the reconciler observes the crash, the sweep
	// resumes the recorded id, and Claude refuses it — after asking for
	// workspace trust again when the earlier answer never reached disk,
	// which the user answers from the pane. The refused attempt is the
	// row's newest generation, presented for the user.
	e.awaitSessionFor(v.session.ID, "automatic resume launched",
		func(s sessionJSON) bool { return s.Generation == birth.Generation+1 }, 60*time.Second)
	v.answerTrustOnResume(v.session.ID)
	// One attempt is the rule; a second is allowed only when tmux reported
	// the first dead without an exit for the sweep to judge, and never more
	// than the unattended budget.
	refused := e.awaitSessionOrExplain(v.session.ID, "refused resume presented",
		func(s sessionJSON) bool {
			return s.Status == "exited" && s.Generation > birth.Generation &&
				s.PresentationState == "resume_ended"
		}, 90*time.Second)
	if refused.Generation > birth.Generation+2 {
		t.Fatalf("the sweep made %d attempts, more than its budget", refused.Generation-birth.Generation)
	}
	if refused.NativeSessionID == nil || *refused.NativeSessionID != birth.NativeSessionID {
		t.Fatalf("identity after refusal = %v, want %s kept", refused.NativeSessionID, birth.NativeSessionID)
	}
	if refused.ExitCode == nil || *refused.ExitCode != 1 {
		t.Fatalf("refused resume exit = code %v signal %v, want Claude's exit 1", refused.ExitCode, refused.ExitSignal)
	}
	if refused.PanePID != nil && processAlive(*refused.PanePID) {
		t.Fatalf("refused resume pid %d is still alive", *refused.PanePID)
	}
	e.requireAuditDetail(t, v.session.ID, "session.resume_attempt", "the resumed generation ended on the way up")
	// The refused pane is retained and its screen served to the card, so
	// the user reads Claude's own words about why. The test reads the
	// words; the product only carries them.
	status, screen := e.api(http.MethodGet, "/api/sessions/"+v.session.ID+"/screen", nil)
	if status != http.StatusOK || !strings.Contains(string(screen), "No conversation found") {
		t.Fatalf("retained screen = %d %q, want Claude's refusal served verbatim", status, screen)
	}

	// Asked once. Several reconciler intervals later the row has not moved:
	// no second resume, no fresh conversation.
	time.Sleep(3 * time.Second)
	settled := e.getSession(v.session.ID)
	if settled.Generation != refused.Generation || settled.Status != "exited" ||
		settled.PresentationState != "resume_ended" {
		t.Fatalf("row moved on its own after the refusal: %+v", settled)
	}

	// The user's explicit Resume asks again. Claude answers within the
	// attestation wait on a quiet host, and the click gets the refusal
	// sentence; under load it answers later, the resume commits, and the
	// reconciler's judgement presents the same card seconds after. Either
	// way the record is still there and nothing fresh was launched.
	status, body := e.api(http.MethodPost, "/api/sessions/"+v.session.ID+"/recover", nil)
	switch {
	case status == http.StatusConflict && strings.Contains(string(body), `"resume_refused"`):
	case status == http.StatusOK && strings.Contains(string(body), `"outcome":"resumed"`):
		t.Log("user resume committed before Claude answered; the reconciler judges it")
	default:
		t.Fatalf("user recover = %d %s, want 409 resume_refused or a committed resume", status, body)
	}
	v.answerTrustOnResume(v.session.ID)
	again := e.awaitSessionOrExplain(v.session.ID, "user's refused resume presented",
		func(s sessionJSON) bool {
			return s.Status == "exited" && s.Generation > refused.Generation &&
				s.PresentationState == "resume_ended"
		}, 90*time.Second)
	if again.NativeSessionID == nil || *again.NativeSessionID != birth.NativeSessionID {
		t.Fatalf("identity after the user's refusal = %v, want %s kept", again.NativeSessionID, birth.NativeSessionID)
	}

	// Starting fresh is a separate, explicit act: a sibling session with its
	// own id, the refused row untouched beside it.
	fresh := e.createSession(projectID, "claude")
	fresh = e.awaitSessionFor(fresh.ID, "fresh Claude running",
		func(s sessionJSON) bool { return s.Status == "running" && s.NativeSessionID != nil },
		120*time.Second)
	if fresh.ID == v.session.ID || *fresh.NativeSessionID == birth.NativeSessionID {
		t.Fatalf("fresh session reused the refused row or identity: %+v", fresh)
	}
	kept := e.getSession(v.session.ID)
	if kept.PresentationState != "resume_ended" || kept.NativeSessionID == nil ||
		*kept.NativeSessionID != birth.NativeSessionID {
		t.Fatalf("refused row changed when a fresh session started: %+v", kept)
	}
}

// TestClaudePersistedConversationStillResumes is the ordinary case the
// refusal handling must not disturb: a conversation with one persisted
// exchange is resumed exactly, unattended, and stays up.
func TestClaudePersistedConversationStillResumes(t *testing.T) {
	if os.Getenv(strictAcceptanceEnv) != "1" {
		t.Skip("real-Claude resume gate is opt-in; run make acceptance")
	}
	e := newEnv(t)
	e.requireCLI("claude")
	v := &verticalSlice{e: e}

	projectID := e.registerProject()
	v.session = e.createSession(projectID, "claude")
	v.session = e.awaitSessionFor(v.session.ID, "running Claude with complete identity",
		func(s sessionJSON) bool {
			return s.Status == "running" && s.PanePID != nil && s.TmuxPaneID != nil &&
				s.NativeSessionID != nil && s.ProcessGroupID != nil && s.StartTicks != nil
		}, 120*time.Second)
	birth := requireRuntimeIdentity(t, v.session)

	v.browser = e.attach(v.session.ID)
	v.waitForClaudeReady()
	const witness = "PERSISTED-RESUME-4c1e"
	v.browser.input([]byte("Reply with exactly these chunks joined by hyphens: " +
		"PERSISTED, RESUME, and the characters 4 c 1 e without spaces. Add nothing else."))
	if !v.browser.outputContains("PERSISTED, RESUME", 10*time.Second) {
		t.Fatal("raw browser text never appeared in Claude's composer")
	}
	// 2.1.261 reads bytes that follow typed text within its paste window as
	// part of the paste: an Enter sent on the echo's heels lands as a newline
	// in the composer and submits nothing. Send it as a user would, a
	// beat later.
	time.Sleep(750 * time.Millisecond)
	v.browser.input([]byte{'\r'})
	if !v.browser.outputContains(witness, 90*time.Second) {
		t.Fatalf("Claude never replied with %q; visible pane:\n%s", witness, v.captureVisible())
	}
	// The reply is on screen before it is in the ledger: a kill on its heels
	// resumes a conversation holding only the prompt. Wait for the idle
	// footer and a beat for the write, as a crash mid-idle would find it.
	v.waitForClaudeIdle(60 * time.Second)
	time.Sleep(2 * time.Second)
	v.browser.closeAndWait(5 * time.Second)

	if err := syscall.Kill(-birth.ProcessGroupID, syscall.SIGKILL); err != nil {
		t.Fatalf("crash Claude process group %d: %v", birth.ProcessGroupID, err)
	}
	resumed := e.awaitSessionFor(v.session.ID, "automatically resumed generation",
		func(s sessionJSON) bool {
			return s.Status == "running" && s.Generation == birth.Generation+1 && s.PanePID != nil
		}, 60*time.Second)
	after := requireRuntimeIdentity(t, resumed)
	if after.NativeSessionID != birth.NativeSessionID {
		t.Fatalf("resume native id = %q, want exact %q", after.NativeSessionID, birth.NativeSessionID)
	}
	e.requireAuditEvent(t, v.session.ID, "session.resumed")

	// The resumed process outlives the refusal window's first seconds and
	// the row stays running: nothing judged it a refusal.
	time.Sleep(5 * time.Second)
	still := e.getSession(v.session.ID)
	if still.Status != "running" || still.Generation != resumed.Generation ||
		still.PresentationState != "running" || !processAlive(after.PanePID) {
		t.Fatalf("resumed conversation did not stay up: %+v alive=%v", still, processAlive(after.PanePID))
	}
	v.session = still
	v.browser = e.attach(v.session.ID)
	if !v.browser.outputContains(witness, 30*time.Second) {
		t.Fatalf("the resumed pane never showed the earlier reply %q; visible pane:\n%s", witness, v.captureVisible())
	}
}

// awaitSessionOrExplain is awaitSessionFor that, on timeout, names the row
// the session did reach and what its pane shows, so a real-CLI timing
// failure is diagnosable from the log alone.
func (e *env) awaitSessionOrExplain(id, what string, ok func(sessionJSON) bool, timeout time.Duration) sessionJSON {
	e.t.Helper()
	deadline := time.Now().Add(timeout)
	var last sessionJSON
	for time.Now().Before(deadline) {
		last = e.getSession(id)
		if ok(last) {
			return last
		}
		time.Sleep(250 * time.Millisecond)
	}
	pane := "(no pane recorded)"
	if last.TmuxPaneID != nil {
		if captured, err := e.h.Tmux.CapturePane(context.Background(), *last.TmuxPaneID); err == nil {
			pane = captured
		}
	}
	e.t.Fatalf("session %s never reached %s; last row: status=%s gen=%d presentation=%q exit=%v/%v native=%v\naudit:%s\npane:\n%s",
		id, what, last.Status, last.Generation, last.PresentationState, last.ExitCode, last.ExitSignal, last.NativeSessionID,
		e.auditTrail(id), pane)
	return sessionJSON{}
}

// auditTrail lists a session's audit events in order, for diagnostics.
func (e *env) auditTrail(sessionID string) string {
	db, err := sql.Open("sqlite3", "file:"+e.cfg.Database.Path+"?mode=ro")
	if err != nil {
		return " (audit unavailable)"
	}
	defer func() { _ = db.Close() }()
	rows, err := db.Query("SELECT event_type, COALESCE(details,'') FROM audit_events WHERE session_id = ? ORDER BY id", sessionID)
	if err != nil {
		return " (audit unavailable)"
	}
	defer func() { _ = rows.Close() }()
	out := ""
	for rows.Next() {
		var kind, detail string
		if rows.Scan(&kind, &detail) == nil {
			out += "\n  " + kind + "(" + detail + ")"
		}
	}
	if rows.Err() != nil {
		out += "\n  (audit read incomplete)"
	}
	return out
}

// answerTrustOnResume answers Claude's workspace-trust picker when a resumed
// generation shows it again — trust accepted moments before an interruption
// is not always on disk yet — exactly as the user would from the card's
// terminal. The refusal Claude prints afterwards is what the test is after.
// It returns once the picker is gone or the pane has died.
func (v *verticalSlice) answerTrustOnResume(sessionID string) {
	t := v.e.t
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	var attached *browser
	defer func() {
		if attached != nil {
			attached.closeAndWait(5 * time.Second)
		}
	}()
	var lastKey time.Time
	for time.Now().Before(deadline) {
		s := v.e.getSession(sessionID)
		if s.Status != "running" || s.TmuxPaneID == nil {
			return
		}
		pane, err := v.e.h.Tmux.CapturePane(context.Background(), *s.TmuxPaneID)
		if err != nil {
			return
		}
		lower := strings.ToLower(pane)
		if !strings.Contains(lower, "project you created or one you trust") &&
			!strings.Contains(lower, "trust this folder") {
			time.Sleep(250 * time.Millisecond)
			continue
		}
		if attached == nil {
			v.session = s
			attached = v.e.attach(sessionID)
			t.Log("the resumed Claude asks for workspace trust again; answering it")
		}
		if time.Since(lastKey) > time.Second {
			if trustCursorOnExit(pane) {
				attached.input([]byte{0x1b, '[', 'B'})
			} else {
				attached.input([]byte{'\r'})
			}
			lastKey = time.Now()
		}
		time.Sleep(100 * time.Millisecond)
	}
}
