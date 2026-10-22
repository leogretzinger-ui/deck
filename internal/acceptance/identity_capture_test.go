package acceptance

import (
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/noisefloor-oss/deck/internal/config"
	"github.com/noisefloor-oss/deck/internal/provider"
	"github.com/noisefloor-oss/deck/internal/tmux"
)

// Retrieved-identity providers (codex, kimi) must capture their native
// session id through the provisioned SessionStart hook and resume with it.
// Everything this exercises is real: the installed CLIs, the production
// provisioner (cmd/noisedeck-provision) run against a test-owned home, the
// durable capture endpoint, service restart during capture, and the automatic
// bounded resume.
//
// Test-owned homes carry copied user credentials and are removed by
// t.TempDir cleanup; the user's provider homes are never modified. Each
// provider costs one minimal real submission because Codex's measured
// contract fires SessionStart only after the first user submission.

var (
	canonicalUUID = regexp.MustCompile(
		`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	kimiSessionID = regexp.MustCompile(
		`^session_[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

func TestRetrievedIdentityCapture(t *testing.T) {
	if os.Getenv(strictAcceptanceEnv) != "1" {
		t.Skip("real-CLI acceptance is opt-in; run make acceptance")
	}
	for _, name := range []string{"codex", "kimi"} {
		if _, err := exec.LookPath(name); err != nil {
			acceptanceUnavailable(t, "%s not installed", name)
		}
	}

	home := filepath.Join(t.TempDir(), "home")
	stageProviderAuth(t, home)
	requireProductionHookScripts(t)
	runProductionProvisioner(t, home)
	runMempalaceConfigurator(t, home)
	runProductionProvisioner(t, home)
	assertCodexStartsWithoutHookReview(t, home)

	setenv(t, "CODEX_HOME", filepath.Join(home, ".codex"))
	setenv(t, "KIMI_CODE_HOME", filepath.Join(home, ".kimi-code"))

	e := newEnvWith(t, func(cfg *config.Config) {
		cfg.Providers.Codex.HookConfig = filepath.Join(home, ".codex", "hooks.json")
		cfg.Providers.Codex.HookReceiver = provider.CodexIdentityHookPath
		cfg.Providers.Kimi.HookConfig = filepath.Join(home, ".kimi-code", "config.toml")
		cfg.Providers.Kimi.HookReceiver = provider.HookReceiverPath
	})
	projectID := e.registerProject()

	t.Run("codex", func(t *testing.T) {
		running := e.runIdentityCapture(t, projectID, "codex", canonicalUUID, true)
		e.verifyCodexColdRedeploy(t, running, home)
	})
	t.Run("kimi", func(t *testing.T) {
		e.runIdentityCapture(t, projectID, "kimi", kimiSessionID, false)
	})
}

func runMempalaceConfigurator(t *testing.T, home string) {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve acceptance source path")
	}
	root := filepath.Join(filepath.Dir(file), "..", "..")
	cmd := exec.Command("python3", filepath.Join(root, "deploy", "mempalace", "configure.py"))
	cmd.Env = append(os.Environ(), "HOME="+home)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("configure composed provider hooks: %v (%s)", err, out)
	}
}

func requireProductionHookScripts(t *testing.T) {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve acceptance source path")
	}
	root := filepath.Join(filepath.Dir(file), "..", "..")
	for installed, source := range map[string]string{
		provider.HookReceiverPath:      filepath.Join(root, "deploy", "provider-hook", "noisedeck-provider-hook"),
		provider.CodexIdentityHookPath: filepath.Join(root, "deploy", "provider-hook", "noisedeck-codex-identity-hook"),
	} {
		got, err := os.ReadFile(installed)
		if err != nil {
			acceptanceUnavailable(t, "production hook receiver %s unavailable", installed)
		}
		want, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Fatalf("production hook receiver %s does not match this checkout", installed)
		}
	}
}

// assertCodexStartsWithoutHookReview proves the static trust tuple seeded by
// production provisioning matches the real pinned CLI. Directory trust is an
// independent project gesture and may be accepted in this private fixture;
// hook review is a provisioning failure and is never answered here.
func assertCodexStartsWithoutHookReview(t *testing.T, home string) {
	t.Helper()
	dir := t.TempDir()
	sock := filepath.Join(dir, "trust.sock")
	project := filepath.Join(dir, "project")
	if err := os.Mkdir(project, 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := "env -i HOME=" + home + " CODEX_HOME=" + filepath.Join(home, ".codex") +
		" PATH=" + os.Getenv("PATH") +
		" TERM=xterm-256color LANG=C.UTF-8 LC_ALL=C.UTF-8 codex"
	if out, err := exec.Command("tmux", "-S", sock, "new-session", "-d", "-s", "fixture",
		"-x", "200", "-y", "50", "-c", project, cmd).CombinedOutput(); err != nil {
		t.Fatalf("launch trust fixture: %v (%s)", err, out)
	}
	stop := func() { _ = exec.Command("tmux", "-S", sock, "kill-server").Run() }
	t.Cleanup(stop)

	deadline := time.Now().Add(45 * time.Second)
	last := ""
	stable := 0
	for time.Now().Before(deadline) {
		out, err := exec.Command("tmux", "-S", sock, "capture-pane", "-p", "-t", "fixture").Output()
		if err != nil {
			t.Fatalf("observe trust fixture: %v", err)
		}
		snap := string(out)
		lower := strings.ToLower(snap)
		if strings.Contains(lower, "hooks need review") {
			t.Fatal("production Codex hook trust opened a native review modal")
		}
		if strings.Contains(lower, "do you trust the contents of this directory") {
			if out, err := exec.Command("tmux", "-S", sock, "send-keys", "-t", "fixture", "Enter").CombinedOutput(); err != nil {
				t.Fatalf("accept private fixture directory: %v (%s)", err, out)
			}
			stable = 0
			time.Sleep(500 * time.Millisecond)
			continue
		}
		if strings.Contains(lower, "openai codex") && snap == last {
			stable++
			if stable >= 4 {
				stop()
				return
			}
		} else {
			stable = 0
		}
		last = snap
		time.Sleep(400 * time.Millisecond)
	}
	t.Fatal("Codex did not reach ready with the production hook trust tuple")
}

// setenv points a process-wide variable at the test-owned value and restores
// it after every other cleanup, so teardown still sees the isolated homes.
func setenv(t *testing.T, key, value string) {
	t.Helper()
	prev, had := os.LookupEnv(key)
	if err := os.Setenv(key, value); err != nil {
		t.Fatalf("set %s: %v", key, err)
	}
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(key, prev)
		} else {
			_ = os.Unsetenv(key)
		}
	})
}

// stageProviderAuth copies only the credential files the CLIs need into the
// test-owned home. Contents are never logged.
func stageProviderAuth(t *testing.T, home string) {
	t.Helper()
	operatorHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("resolve user home: %v", err)
	}
	for _, rel := range []string{
		filepath.Join(".codex", "auth.json"),
		filepath.Join(".kimi-code", "credentials", "kimi-code.json"),
	} {
		src := filepath.Join(operatorHome, rel)
		data, err := os.ReadFile(src)
		if err != nil {
			acceptanceUnavailable(t, "user provider credential %s unavailable", rel)
		}
		dst := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			t.Fatalf("create test-owned provider dir: %v", err)
		}
		if err := os.WriteFile(dst, data, 0o600); err != nil {
			t.Fatalf("stage provider credential: %v", err)
		}
	}
}

// runProductionProvisioner runs the shipped provisioning binary against the
// test-owned home, so acceptance covers the exact artifact the deployment
// boots with — hook registration and codex's measured trust block included.
func runProductionProvisioner(t *testing.T, home string) {
	t.Helper()
	goEnv := func(key string) string {
		out, err := exec.Command("go", "env", key).Output()
		if err != nil {
			t.Fatalf("resolve Go %s for provisioner: %v", key, err)
		}
		return strings.TrimSpace(string(out))
	}
	cmd := exec.Command("go", "run", "github.com/noisefloor-oss/deck/cmd/noisedeck-provision")
	cmd.Env = append(os.Environ(), "HOME="+home,
		"GOMODCACHE="+goEnv("GOMODCACHE"), "GOCACHE="+goEnv("GOCACHE"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("noisedeck-provision against test-owned home: %v (%s)", err, out)
	}
}

// runIdentityCapture drives one provider through launch, late-identity
// capture, crash, and automatic exact-id resume.
func (e *env) runIdentityCapture(
	t *testing.T, projectID, name string, idShape *regexp.Regexp, expectLateIdentity bool,
) sessionJSON {
	t.Helper()
	s := e.createSession(projectID, name)
	s = e.awaitSession(s.ID, "running with a live pane", func(s sessionJSON) bool {
		return s.Status == "running" && s.PanePID != nil && s.TmuxPaneID != nil
	})
	if s.NativeSessionID != nil {
		t.Fatalf("%s recorded a native id at launch; nothing has submitted yet", name)
	}

	b := e.attach(s.ID)
	defer func() { b.closeAndWait(2 * time.Second) }()
	e.settleProviderUI(t, b, name)

	if expectLateIdentity {
		// Measured contract (docs/provider-contracts/codex.yaml): no
		// SessionStart event during an idle window after a ready launch.
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if got := e.getSession(s.ID); got.NativeSessionID != nil {
				t.Fatalf("%s captured identity without a submission; "+
					"the measured late-identity contract changed", name)
			}
			time.Sleep(250 * time.Millisecond)
		}

		// The raw bearer exists only in this still-running CLI's launch
		// environment. Restart Deck after the durable grant commit but before
		// Codex emits SessionStart, then submit through a fresh attachment. The
		// reconstructed receiver must accept the original bearer.
		beforeRestart := s
		b.closeAndWait(5 * time.Second)
		e.stopService()
		if beforeRestart.PanePID == nil || !processAlive(*beforeRestart.PanePID) {
			t.Fatalf("%s provider did not survive backend restart during capture", name)
		}
		e.startService(nil)
		s = e.awaitSession(s.ID, "same live pane after capture-service restart", func(current sessionJSON) bool {
			return current.Status == "running" && current.PanePID != nil &&
				beforeRestart.PanePID != nil && *current.PanePID == *beforeRestart.PanePID &&
				current.NativeSessionID == nil
		})
		b = e.attach(s.ID)
	}

	prompt := "Reply with exactly: NOISEDECK-CAPTURE-OK."
	b.input([]byte(prompt))
	b.input([]byte{'\r'})

	captureDeadline := time.Now().Add(45 * time.Second)
	for s.NativeSessionID == nil {
		if time.Now().After(captureDeadline) {
			t.Fatalf("%s never captured its native id; session=%+v",
				name, e.getSession(s.ID))
		}
		time.Sleep(250 * time.Millisecond)
		s = e.getSession(s.ID)
	}
	captured := *s.NativeSessionID
	if !idShape.MatchString(captured) {
		t.Fatalf("%s native id shape mismatch: %q", name, captured)
	}
	e.requireAuditEvent(t, s.ID, "session.identity_captured")
	if name == "codex" {
		e.requireAuditDetail(t, s.ID, "session.identity_confirmed", "mechanism=structured_stop_hook")
	}

	// A user stop is a requested exit and must stick, so exact-id
	// recovery is exercised the way it actually triggers — a crash outside
	// the control API, recovered automatically with no user action.
	before := s
	if before.ProcessGroupID == nil {
		t.Fatalf("%s session has no recorded process group to crash", name)
	}
	if err := syscall.Kill(-*before.ProcessGroupID, syscall.SIGKILL); err != nil {
		t.Fatalf("crash %s process group %d: %v", name, *before.ProcessGroupID, err)
	}
	s = e.awaitSession(s.ID, "resumed automatically with the captured id", func(s sessionJSON) bool {
		return s.Status == "running" && s.Generation == before.Generation+1 &&
			s.PanePID != nil
	})
	if s.NativeSessionID == nil || *s.NativeSessionID != captured {
		t.Fatalf("%s resume lost the captured id: before %q after %v",
			name, captured, s.NativeSessionID)
	}
	if !processAlive(*s.PanePID) {
		t.Fatalf("%s resumed pane pid %d is not alive", name, *s.PanePID)
	}
	// The row alone is not proof the CLI received the id: check the resumed
	// process's argv carries it (codex `resume <id>`, kimi `-S <id>`).
	cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", *s.PanePID))
	if err != nil {
		t.Fatalf("read resumed %s process argv: %v", name, err)
	}
	if !strings.Contains(string(cmdline), captured) {
		t.Fatalf("%s resumed process argv does not carry the captured id %q",
			name, captured)
	}
	return s
}

func (e *env) verifyCodexColdRedeploy(t *testing.T, before sessionJSON, home string) {
	t.Helper()
	if before.NativeSessionID == nil || before.PanePID == nil || before.ProcessGroupID == nil ||
		before.StartTicks == nil || before.IdentityMarker == nil || before.TmuxPaneID == nil {
		t.Fatalf("incomplete Codex controller before cold redeploy: %+v", before)
	}
	if before.BootEpoch == nil || *before.BootEpoch == "" {
		t.Fatalf("Codex controller has no boot epoch before cold redeploy: %+v", before)
	}
	captured := *before.NativeSessionID
	resumedBefore := e.auditCount(t, before.ID, "session.resumed")
	e.stopService()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve acceptance source path")
	}
	root := filepath.Join(filepath.Dir(file), "..", "..")
	asset := func(name string) []byte {
		raw, err := os.ReadFile(filepath.Join(root, "cmd", "noisedeck-provision", "assets", name))
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	hooksPath := filepath.Join(home, ".codex", "hooks.json")
	if err := os.WriteFile(hooksPath, asset("codex-hooks-v1.json"), 0o600); err != nil {
		t.Fatal(err)
	}
	runMempalaceConfigurator(t, home)
	composed, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(composed) != string(asset("codex-hooks-mempalace-v1.json")) {
		t.Fatal("released configurator output does not match the recognized composed hook bytes")
	}
	runProductionProvisioner(t, home)
	upgraded, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(upgraded) != string(asset("codex-hooks-mempalace.json")) {
		t.Fatal("production provisioner did not upgrade the released composed Codex hooks")
	}
	if out, err := exec.Command("tmux", "-S", e.cfg.Tmux.SocketPath, "kill-server").CombinedOutput(); err != nil && !strings.Contains(string(out), "no server running") {
		t.Fatalf("destroy isolated tmux server: %v (%s)", err, out)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		alive, err := tmux.ProcAlive(*before.PanePID, *before.StartTicks)
		if err == nil && !alive {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("old Codex process %d survived its isolated tmux server", *before.PanePID)
		}
		time.Sleep(50 * time.Millisecond)
	}

	e.startService(nil)
	after := e.awaitSessionFor(before.ID, "cold-redeployed exact Codex successor",
		func(s sessionJSON) bool {
			return s.Status == "running" && s.Generation == before.Generation+1 &&
				s.PanePID != nil && s.ProcessGroupID != nil && s.StartTicks != nil &&
				s.IdentityMarker != nil && s.TmuxSessionID != nil && s.TmuxPaneID != nil &&
				s.ExpectedExecutable != nil && s.BootEpoch != nil && *s.BootEpoch != "" &&
				s.NativeSessionID != nil
		}, 60*time.Second)
	if *after.NativeSessionID != captured {
		t.Fatalf("cold redeploy changed native identity: got %q want %q",
			*after.NativeSessionID, captured)
	}
	if *after.IdentityMarker == *before.IdentityMarker ||
		(*after.PanePID == *before.PanePID && *after.StartTicks == *before.StartTicks) {
		t.Fatalf("cold redeploy reused predecessor identity: before=%+v after=%+v", before, after)
	}
	if alive, err := tmux.ProcAlive(*after.PanePID, *after.StartTicks); err != nil || !alive {
		t.Fatalf("cold Codex successor process is not alive: alive=%v err=%v", alive, err)
	}
	argvRaw, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", *after.PanePID))
	if err != nil {
		t.Fatal(err)
	}
	argv := strings.Split(strings.TrimRight(string(argvRaw), "\x00"), "\x00")
	for _, arg := range argv {
		if arg == "--last" {
			t.Fatal("cold Codex recovery used non-exact --last")
		}
	}
	foundExactResume := false
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == "resume" && argv[i+1] == captured {
			foundExactResume = true
		}
	}
	if !foundExactResume {
		t.Fatalf("cold Codex argv did not contain adjacent resume and captured ID: %v", argv)
	}
	if got := e.auditCount(t, before.ID, "session.resumed"); got != resumedBefore+1 {
		t.Fatalf("session.resumed count = %d, want %d", got, resumedBefore+1)
	}
	database := openControlDB(t, e.cfg.Database.Path)
	defer func() { _ = database.Close() }()
	var slots, attempts int
	if err := database.QueryRow(`SELECT COUNT(*) FROM session_successor_slots
		WHERE session_id = ?`, before.ID).Scan(&slots); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT resume_attempts FROM sessions WHERE id = ?`,
		before.ID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if slots != 0 || attempts != 0 {
		t.Fatalf("cold Codex recovery residue: slots=%d attempts=%d", slots, attempts)
	}
}

func (e *env) auditCount(t *testing.T, sessionID, eventType string) int {
	t.Helper()
	database := openControlDB(t, e.cfg.Database.Path)
	defer func() { _ = database.Close() }()
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM audit_events
		WHERE session_id = ? AND event_type = ?`, sessionID, eventType).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// settleProviderUI gets a freshly launched CLI to a stable interactive
// screen. Codex's per-directory trust prompt ("Do you trust the contents of
// this directory?") is answered with Enter — selecting the default "Yes,
// continue" — exactly as a user would in their own terminal; the
// application runtime never answers it. Readiness is a stable, non-empty
// pane, not a guess at either provider's layout.
func (e *env) settleProviderUI(t *testing.T, b *browser, name string) {
	t.Helper()
	const codexDirTrust = "Do you trust the contents of this directory"
	deadline := time.Now().Add(30 * time.Second)
	last := ""
	stable := 0
	for time.Now().Before(deadline) {
		snap := b.outputSnapshot()
		if strings.Contains(snap, codexDirTrust) {
			b.input([]byte{'\r'})
			stable = 0
			time.Sleep(500 * time.Millisecond)
			continue
		}
		if snap == last && len(strings.TrimSpace(snap)) > 0 {
			stable++
			if stable >= 6 {
				return
			}
		} else {
			stable = 0
		}
		last = snap
		time.Sleep(400 * time.Millisecond)
	}
	t.Fatalf("%s never reached a stable interactive screen", name)
}

// requireAuditEvent proves the capture was recorded as a durable audit event,
// not just a row mutation.
func (e *env) requireAuditEvent(t *testing.T, sessionID, eventType string) {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+e.cfg.Database.Path+"?mode=ro")
	if err != nil {
		t.Fatalf("open control database: %v", err)
	}
	defer func() { _ = db.Close() }()
	var count int
	err = db.QueryRow(
		"SELECT COUNT(*) FROM audit_events WHERE session_id = ? AND event_type = ?",
		sessionID, eventType,
	).Scan(&count)
	if err != nil {
		t.Fatalf("query audit events: %v", err)
	}
	if count != 1 {
		// Naming the events that did occur turns "the expected one is
		// missing" into the actual transition the session took.
		rows, qerr := db.Query(
			"SELECT event_type, COALESCE(details,'') FROM audit_events "+
				"WHERE session_id = ? ORDER BY id", sessionID)
		observed := ""
		if qerr == nil {
			defer func() { _ = rows.Close() }()
			for rows.Next() {
				var kind, detail string
				if rows.Scan(&kind, &detail) == nil {
					observed += " " + kind + "(" + detail + ")"
				}
			}
			// A truncated read would otherwise read as a shorter history
			// than the session actually recorded.
			if rows.Err() != nil {
				observed += " (audit read incomplete)"
			}
		}
		t.Fatalf("%s audit events for session %s: %d, want exactly 1; observed:%s",
			eventType, sessionID, count, observed)
	}
}

func (e *env) requireAuditDetail(t *testing.T, sessionID, eventType, fragment string) {
	t.Helper()
	database, err := sql.Open("sqlite3", "file:"+e.cfg.Database.Path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		var count int
		if err := database.QueryRow(`SELECT COUNT(*) FROM audit_events
			WHERE session_id = ? AND event_type = ? AND details LIKE ?`,
			sessionID, eventType, "%"+fragment+"%").Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count > 0 {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("%s audit details never contained %q", eventType, fragment)
}
