package acceptance

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/noisefloor-oss/deck/internal/config"
	ndb "github.com/noisefloor-oss/deck/internal/db"
	"github.com/noisefloor-oss/deck/internal/tmux"
)

// TestLegacyRecoveryResidueConvergesOnRedeploy proves that an upgraded row
// carrying the released retry/probation fields is reconciled in place against
// one exact live real-CLI identity. The service replacement must neither spawn
// a duplicate nor retain obsolete retry state that would suppress the next
// verified generation's single automatic opportunity.
func TestLegacyRecoveryResidueConvergesOnRedeploy(t *testing.T) {
	if os.Getenv(strictAcceptanceEnv) != "1" {
		t.Skip("real-Claude redeploy gate is opt-in; run make acceptance")
	}
	claudePath, err := exec.LookPath("claude")
	if err != nil {
		acceptanceUnavailable(t, "claude not installed")
	}

	dir := t.TempDir()
	writeClaudeProjectSettings(t, dir)
	port := reservePort(t)
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	cfg := config.Defaults()
	cfg.Server.Bind = fmt.Sprintf("127.0.0.1:%d", port)
	cfg.Server.AllowedOrigins = []string{base}
	cfg.Database.Path = filepath.Join(dir, "run", "control.db")
	cfg.Database.LockPath = filepath.Join(dir, "run", "backend.lock")
	cfg.Tmux.SocketPath = tempTmuxSocket(t, "redeploy.sock")
	preparePrivateRuntimeDirs(t, &cfg)
	cfg.Projects.AllowedRoots = []string{dir}
	cfg.Auth.Mode = "trusted_proxy"
	cfg.Auth.SessionSecret = "test-secret"
	cfg.Auth.TrustedProxy.Addresses = []string{"127.0.0.1"}
	cfg.Auth.TrustedProxy.IdentityHeader = "X-Acceptance-User"
	cfg.Reconciler.Interval = config.Duration(200 * time.Millisecond)
	cfg.Providers.Claude.Executable = claudePath
	configPath := filepath.Join(dir, "service-config.json")
	writeRedeployServiceConfig(t, configPath, &cfg)
	socket := cfg.Tmux.SocketPath
	t.Cleanup(func() { _ = exec.Command("tmux", "-S", socket, "kill-server").Run() })

	first := startServiceProcess(t, configPath, base)
	t.Cleanup(func() { first.cleanup(t) })
	e := &env{t: t, base: base, dir: dir, cfg: &cfg}

	// Launch real Claude through Deck and wait for the complete recorded
	// identity — the tuple the redeployed service must reconcile unchanged.
	projectID := e.registerProject()
	launched := e.createSession(projectID, "claude")
	launched = e.awaitSessionFor(launched.ID, "running Claude with complete identity",
		func(s sessionJSON) bool {
			return s.Status == "running" && s.PanePID != nil && s.TmuxPaneID != nil &&
				s.IdentityMarker != nil && s.NativeSessionID != nil &&
				s.ProcessGroupID != nil && s.StartTicks != nil
		}, 120*time.Second)
	if !processAlive(*launched.PanePID) {
		t.Fatalf("launched Claude pid %d is not alive", *launched.PanePID)
	}
	// kill-server tears the pane down; this backstop SIGKILLs a Claude that
	// somehow outlives its tmux server, checking start ticks so a reused PID
	// is never targeted.
	t.Cleanup(func() {
		alive, err := tmux.ProcAlive(*launched.PanePID, *launched.StartTicks)
		if err != nil || !alive {
			return
		}
		pgid, err := tmux.ProcGroup(*launched.PanePID, *launched.StartTicks)
		if err == nil {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		}
	})

	// The redeploy boundary: hard-stop ONLY the service. tmux and the
	// provider it owns survive, which is what makes the preserved
	// control data and provider home reconcilable afterward.
	if !processAlive(*launched.PanePID) {
		t.Fatalf("Claude pid %d died before the redeploy", *launched.PanePID)
	}
	first.kill(t)
	if !processAlive(*launched.PanePID) {
		t.Fatalf("hard service stop killed the provider pid %d it does not own", *launched.PanePID)
	}

	// Simulate an upgraded database that still carries the released retry
	// series fields on an otherwise exact live controller.
	seedLegacyRecoveryResidue(t, cfg.Database.Path, launched.ID, 3)

	second := startServiceProcess(t, configPath, base)
	t.Cleanup(func() { second.cleanup(t) })

	// Before the first response, startup reconciliation verifies the exact
	// same controller and clears the obsolete retry state without launching.
	converged := e.awaitSessionFor(launched.ID, "legacy recovery state converged after redeploy",
		func(s sessionJSON) bool {
			return s.Status == "running" &&
				readResumeAttempts(t, cfg.Database.Path, launched.ID) == 0 &&
				!hasResumeProbation(t, cfg.Database.Path, launched.ID)
		}, 30*time.Second)
	if !sameRecordedIdentity(converged, launched) {
		t.Fatalf("redeploy changed the recorded provider identity\nlaunched: %+v\nretained: %+v",
			launched, converged)
	}
	assertNoDuplicateProvider(t, socket, converged)
	if converged.PanePID == nil || !processAlive(*converged.PanePID) {
		t.Fatalf("provider pid %v died across the redeploy", converged.PanePID)
	}
	e.requireAuditEvent(t, launched.ID, "session.recovery_legacy_converged")
}

// awaitSessionFor is awaitSession with a caller-chosen timeout, for the one
// step (real Claude launch) whose latency exceeds the shared helper's bound.
func (e *env) awaitSessionFor(id, what string, ok func(sessionJSON) bool, timeout time.Duration) sessionJSON {
	e.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		s := e.getSession(id)
		if ok(s) {
			return s
		}
		time.Sleep(250 * time.Millisecond)
	}
	e.t.Fatalf("session %s never reached %s", id, what)
	return sessionJSON{}
}

// writeClaudeProjectSettings installs the same project-local permission rules
// the acceptance harness uses, so a real Claude launches deterministically
// without touching the user's global preferences.
func writeClaudeProjectSettings(t *testing.T, dir string) {
	t.Helper()
	claudeDir := filepath.Join(dir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o700); err != nil {
		t.Fatalf("create Claude settings directory: %v", err)
	}
	const settings = `{
  "permissions": {"defaultMode": "default", "ask": ["Bash"]},
  "sandbox": {"enabled": false, "autoAllowBashIfSandboxed": false}
}`
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.local.json"),
		[]byte(settings), 0o600); err != nil {
		t.Fatalf("write Claude settings: %v", err)
	}
}

func writeRedeployServiceConfig(t *testing.T, path string, cfg *config.Config) {
	t.Helper()
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("encode service config: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write service config: %v", err)
	}
}

// seedLegacyRecoveryResidue simulates the released retry-series fields on an
// exact live row. The service must be stopped when this runs.
func seedLegacyRecoveryResidue(t *testing.T, dbPath, sessionID string, attempts int) {
	t.Helper()
	deadline := ndb.FormatTime(time.Now().Add(time.Hour))
	series := ndb.FormatTime(time.Now().Add(-5 * time.Minute))
	execDB(t, dbPath, `UPDATE sessions SET resume_attempts = ?,
		resume_next_at = ?, resume_series_started_at = ?,
		resume_probation_until = ? WHERE id = ?`,
		attempts, deadline, series, deadline, sessionID)
}

func readResumeAttempts(t *testing.T, dbPath, sessionID string) int {
	t.Helper()
	ldb := openControlDB(t, dbPath)
	defer func() { _ = ldb.Close() }()
	var attempts int
	if err := ldb.QueryRow(
		`SELECT resume_attempts FROM sessions WHERE id = ?`, sessionID).Scan(&attempts); err != nil {
		t.Fatalf("read resume attempts: %v", err)
	}
	return attempts
}

func hasResumeProbation(t *testing.T, dbPath, sessionID string) bool {
	t.Helper()
	ldb := openControlDB(t, dbPath)
	defer func() { _ = ldb.Close() }()
	var probation sql.NullString
	if err := ldb.QueryRow(
		`SELECT resume_probation_until FROM sessions WHERE id = ?`, sessionID).Scan(&probation); err != nil {
		t.Fatalf("read probation deadline: %v", err)
	}
	return probation.Valid && probation.String != ""
}

// assertNoDuplicateProvider proves no second provider pane or process was
// spawned across the redeploy: the recorded session owns the single pane in
// its tmux session, and that pane's recorded process is alive.
func assertNoDuplicateProvider(t *testing.T, socket string, s sessionJSON) {
	t.Helper()
	out := runIsolatedTmux(t, socket, "list-panes", "-s", "-t", s.TmuxSessionName,
		"-F", "#{pane_id}")
	panes := strings.Fields(out)
	if len(panes) != 1 {
		t.Fatalf("tmux session %s has %d panes after redeploy, want 1: %v",
			s.TmuxSessionName, len(panes), panes)
	}
	if s.TmuxPaneID == nil || panes[0] != *s.TmuxPaneID {
		t.Fatalf("recorded pane %v is not the single live pane %q", s.TmuxPaneID, panes[0])
	}
	if s.PanePID == nil || !processAlive(*s.PanePID) {
		t.Fatalf("recorded provider pid %v is not alive after redeploy", s.PanePID)
	}
}

// sameRecordedIdentity holds when every field the reconciler compares —
// the full runtime identity tuple plus generation and native id — is
// unchanged, so a respawn or replacement (which would mint a new tuple)
// cannot hide.
func sameRecordedIdentity(a, b sessionJSON) bool {
	return a.Generation == b.Generation &&
		eqStrPtr(a.IdentityMarker, b.IdentityMarker) &&
		eqStrPtr(a.TmuxSessionID, b.TmuxSessionID) &&
		eqStrPtr(a.TmuxPaneID, b.TmuxPaneID) &&
		eqIntPtr(a.PanePID, b.PanePID) &&
		eqIntPtr(a.ProcessGroupID, b.ProcessGroupID) &&
		eqI64Ptr(a.StartTicks, b.StartTicks) &&
		eqStrPtr(a.NativeSessionID, b.NativeSessionID)
}

func eqStrPtr(a, b *string) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }
func eqIntPtr(a, b *int) bool    { return (a == nil) == (b == nil) && (a == nil || *a == *b) }
func eqI64Ptr(a, b *int64) bool  { return (a == nil) == (b == nil) && (a == nil || *a == *b) }

func openControlDB(t *testing.T, dbPath string) *sql.DB {
	t.Helper()
	ldb, err := sql.Open("sqlite3", "file:"+dbPath+"?mode=ro&_busy_timeout=5000")
	if err != nil {
		t.Fatalf("open control database: %v", err)
	}
	return ldb
}

func execDB(t *testing.T, dbPath, query string, args ...any) {
	t.Helper()
	ldb, err := sql.Open("sqlite3", "file:"+dbPath+"?_busy_timeout=5000")
	if err != nil {
		t.Fatalf("open control database: %v", err)
	}
	defer func() { _ = ldb.Close() }()
	if _, err := ldb.Exec(query, args...); err != nil {
		t.Fatalf("exec control database: %v", err)
	}
}
