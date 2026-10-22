package acceptance

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/noisefloor-oss/deck/internal/session"
	"github.com/noisefloor-oss/deck/internal/tmux"
)

// TestSameNativeIDLivePredecessorDoesNotGateRecovery is the real-CLI proof
// that a provider-native ID is opaque argv, never a Deck-wide lock. An
// out-of-band pane-marker replacement makes the original attachment foreign
// while its exact Claude process stays live. Deck must launch one separately
// fenced successor with the same ID and leave that predecessor untouched.
func TestSameNativeIDLivePredecessorDoesNotGateRecovery(t *testing.T) {
	if os.Getenv(strictAcceptanceEnv) != "1" {
		t.Skip("real-Claude same-id concurrency gate is opt-in; run make acceptance")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		acceptanceUnavailable(t, "claude not installed")
	}
	e := newEnv(t)
	projectID := e.registerProject()
	created := e.createSession(projectID, "claude")
	created = e.awaitSessionFor(created.ID, "complete running Claude identity", func(s sessionJSON) bool {
		return s.Status == "running" && s.NativeSessionID != nil && s.PanePID != nil &&
			s.StartTicks != nil && s.TmuxPaneID != nil && s.IdentityMarker != nil
	}, 120*time.Second)
	before := requireRuntimeIdentity(t, created)

	// Change only tmux's local marker. The old CLI remains live and receives no
	// input or signal; it is now foreign to the recorded complete identity.
	runIsolatedTmux(t, e.cfg.Tmux.SocketPath, "set-option", "-p", "-t",
		before.TmuxPaneID, tmux.OptMarker, "foreign-live-predecessor")

	afterJSON := e.awaitSessionFor(created.ID, "same-id fenced successor", func(s sessionJSON) bool {
		return s.Status == "running" && s.Generation == before.Generation+1 &&
			s.NativeSessionID != nil && *s.NativeSessionID == before.NativeSessionID &&
			s.PanePID != nil
	}, 120*time.Second)
	after := requireRuntimeIdentity(t, afterJSON)
	if after.TmuxSessionName == before.TmuxSessionName || after.TmuxPaneID == before.TmuxPaneID ||
		after.IdentityMarker == before.IdentityMarker {
		t.Fatalf("successor reused predecessor identity\nbefore: %+v\n after: %+v", before, after)
	}
	alive, err := tmux.ProcAlive(before.PanePID, before.StartTicks)
	if err != nil || !alive {
		t.Fatalf("live predecessor was affected: alive=%v err=%v", alive, err)
	}
	oldPane, err := e.h.Tmux.PaneInfoByID(context.Background(), before.TmuxPaneID)
	if err != nil || oldPane.Dead || oldPane.PID != before.PanePID ||
		oldPane.StartTicks != before.StartTicks || oldPane.Marker != "foreign-live-predecessor" {
		t.Fatalf("foreign predecessor pane changed: info=%+v err=%v", oldPane, err)
	}

	rawArgv, err := os.ReadFile("/proc/" + strconv.Itoa(after.PanePID) + "/cmdline")
	if err != nil {
		t.Fatalf("read successor argv: %v", err)
	}
	argv := strings.Split(strings.TrimSuffix(string(rawArgv), "\x00"), "\x00")
	foundExactResume := false
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == "--resume" && argv[i+1] == before.NativeSessionID {
			foundExactResume = true
			break
		}
	}
	if !foundExactResume {
		t.Fatalf("successor argv did not carry the exact shared native ID: %q", argv)
	}
	var predecessorEffects int
	if err := e.h.DB.Reader().QueryRow(`
		SELECT count(*) FROM audit_events
		WHERE session_id = ? AND event_type IN (?, ?)`,
		created.ID, session.AuditStopRequested, session.AuditRemoved).
		Scan(&predecessorEffects); err != nil {
		t.Fatalf("count predecessor effects: %v", err)
	}
	if predecessorEffects != 0 {
		t.Fatalf("same-id recovery recorded %d predecessor stop/removal effects", predecessorEffects)
	}
}
