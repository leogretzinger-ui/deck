package acceptance

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/noisefloor-oss/deck/internal/boot"
	"github.com/noisefloor-oss/deck/internal/config"
	"github.com/noisefloor-oss/deck/internal/tmux"
)

const acceptanceServiceConfigEnv = "NOISEDECK_ACCEPTANCE_SERVICE_CONFIG"

// TestDirectTerminalFailureMatrix exercises the write-honesty failure
// matrix against a real backend, WebSocket, tmux server, PTY attachment,
// and interactive shell. The shell is the credential-free real CLI;
// provider-specific native history and resume remain in the separate
// real-provider gates.
func TestDirectTerminalFailureMatrix(t *testing.T) {
	if os.Getenv(strictAcceptanceEnv) != "1" {
		t.Skip("real-terminal failure gate is opt-in; run make acceptance")
	}

	t.Run("preflight rejections cross zero bytes", testPreflightRejections)
	t.Run("unverifiable write is uncertain and never replayed", testUncertainWrite)
	t.Run("browser disconnect permits zero or one and never replays", testBrowserDisconnect)
	t.Run("service SIGKILL permits zero or one and never replays", testServiceKill)
	t.Run("dead pane rejects input without a shell fallback", testDeadPane)
	t.Run("takeover fences the old controller", testTakeoverFence)
}

type failureSetup struct {
	e       *env
	session sessionJSON
	browser *browser
}

func newFailureSetup(t *testing.T) *failureSetup {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		acceptanceUnavailable(t, "bash not installed")
	}
	e := newEnvWith(t, func(cfg *config.Config) {
		cfg.Providers.Shell.Executable = bash
		// These cases deliberately make runtime identity disagree with the
		// row. Keep periodic reconciliation out of that controlled window;
		// boot still performs the required startup pass.
		cfg.Reconciler.Interval = config.Duration(time.Hour)
	})
	s := e.createSession(e.registerProject(), "shell")
	s = e.awaitSession(s.ID, "running shell with complete pane identity", func(s sessionJSON) bool {
		return s.Status == "running" && s.TmuxPaneID != nil && s.PanePID != nil &&
			s.IdentityMarker != nil && s.ProcessGroupID != nil && s.StartTicks != nil
	})
	b := e.attach(s.ID)
	awaitShellReady(t, e, b)
	return &failureSetup{e: e, session: s, browser: b}
}

func awaitShellReady(t *testing.T, e *env, b *browser) {
	t.Helper()
	path := filepath.Join(e.dir, ".failure-matrix-ready")
	b.input([]byte("printf ready > .failure-matrix-ready\r"))
	b.awaitStatus(outcomeSent, 10*time.Second)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil && string(data) == "ready" {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("interactive shell never executed the readiness probe")
}

type writeLog struct {
	mu           sync.Mutex
	observations []tmux.WriteObservation
}

func (w *writeLog) observe(observation tmux.WriteObservation) {
	w.mu.Lock()
	observation.Data = append([]byte(nil), observation.Data...)
	w.observations = append(w.observations, observation)
	w.mu.Unlock()
}

func (w *writeLog) snapshot() []tmux.WriteObservation {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]tmux.WriteObservation, len(w.observations))
	copy(out, w.observations)
	return out
}

func testPreflightRejections(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*failureSetup)
		send   func(*browser, []byte)
	}{
		{
			name: "fencing token mismatch",
			send: func(b *browser, marker []byte) {
				sendRawInput(t, b, b.token+1, b.gen, marker)
			},
		},
		{
			name: "generation mismatch",
			send: func(b *browser, marker []byte) {
				sendRawInput(t, b, b.token, b.gen+1, marker)
			},
		},
		{
			name: "identity mismatch",
			mutate: func(s *failureSetup) {
				if err := s.e.h.Tmux.SetPaneOption(context.Background(),
					*s.session.TmuxPaneID, tmux.OptMarker, "foreign-marker"); err != nil {
					t.Fatalf("replace pane identity marker: %v", err)
				}
			},
		},
		{
			name: "recorded pane gone",
			mutate: func(s *failureSetup) {
				replaceRecordedPane(t, s)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newFailureSetup(t)
			if tc.mutate != nil {
				tc.mutate(s)
			}
			writes := &writeLog{}
			s.e.h.Tmux.SetWriteObserverForTest(writes.observe)
			defer s.e.h.Tmux.SetWriteObserverForTest(nil)

			marker := []byte("PREFLIGHT-MUST-NOT-CROSS-" + strings.ReplaceAll(tc.name, " ", "-"))
			if tc.send != nil {
				tc.send(s.browser, marker)
			} else {
				s.browser.input(marker)
			}
			status := s.browser.awaitStatus(outcomeFailed, 10*time.Second)
			assertNoDeliveryClaim(t, status)
			if got := writes.snapshot(); len(got) != 0 {
				t.Fatalf("provably rejected input produced %d tmux-boundary writes: %+v", len(got), got)
			}
		})
	}
}

func sendRawInput(t *testing.T, b *browser, token int64, generation int, data []byte) {
	t.Helper()
	raw, err := json.Marshal(clientFrame{
		Type:       frameInput,
		Token:      token,
		Generation: generation,
		Data:       base64.StdEncoding.EncodeToString(data),
	})
	if err != nil {
		t.Fatalf("marshal raw input frame: %v", err)
	}
	if err := b.writeMessage(raw); err != nil {
		t.Fatalf("write raw input frame: %v", err)
	}
}

func replaceRecordedPane(t *testing.T, s *failureSetup) {
	t.Helper()
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Fatalf("resolve sleep fixture: %v", err)
	}
	foreignPane := runIsolatedTmux(t, s.e.cfg.Tmux.SocketPath,
		"new-window", "-P", "-F", "#{pane_id}", "-t", "="+s.session.TmuxSessionName+":",
		"--", sleep, "600")
	if foreignPane == "" {
		t.Fatal("tmux did not report the replacement pane id")
	}
	info, err := s.e.h.Tmux.PaneInfoForSession(context.Background(), s.session.TmuxSessionName)
	if err != nil {
		t.Fatalf("observe replacement pane: %v", err)
	}
	if info.PaneID != foreignPane {
		runIsolatedTmux(t, s.e.cfg.Tmux.SocketPath, "select-window", "-t", foreignPane)
	}
	runIsolatedTmux(t, s.e.cfg.Tmux.SocketPath, "kill-pane", "-t", *s.session.TmuxPaneID)
	if _, err := s.e.h.Tmux.PaneInfoByID(context.Background(), *s.session.TmuxPaneID); !errors.Is(err, tmux.ErrNoSession) {
		t.Fatalf("recorded pane still resolves after removal: %v", err)
	}
}

func runIsolatedTmux(t *testing.T, socket string, args ...string) string {
	t.Helper()
	cmdArgs := append([]string{"-S", socket}, args...)
	out, err := exec.Command("tmux", cmdArgs...).CombinedOutput()
	if err != nil {
		t.Fatalf("isolated tmux %v: %v (%s)", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func testUncertainWrite(t *testing.T) {
	s := newFailureSetup(t)
	writes := &writeLog{}
	s.e.h.Tmux.SetWriteObserverForTest(writes.observe)
	defer s.e.h.Tmux.SetWriteObserverForTest(nil)

	data := []byte("printf 'must-not-run\\n' >> uncertain.log\r")
	prefix := append([]byte(nil), data[:len(data)/2]...)
	s.e.h.Terminal.SetWriteFaultForTest(func(att *tmux.Attachment, _ []byte) (int, error) {
		n, err := att.WriteWithin(prefix, 5*time.Second)
		if err != nil {
			return n, err
		}
		return n, errors.New("acceptance: terminal acknowledgement lost")
	})
	defer s.e.h.Terminal.SetWriteFaultForTest(nil)

	s.browser.input(data)
	status := s.browser.awaitStatus(outcomeUncertain, 10*time.Second)
	assertNoDeliveryClaim(t, status)
	got := writes.snapshot()
	if len(got) != 1 || !bytes.Equal(got[0].Data, prefix) {
		t.Fatalf("uncertain write boundary = %+v, want one exact prefix %q", got, prefix)
	}
	if _, err := os.Stat(filepath.Join(s.e.dir, "uncertain.log")); !os.IsNotExist(err) {
		t.Fatalf("partial write unexpectedly executed before reconnect: %v", err)
	}

	s.browser.closeAndWait(5 * time.Second)
	s.browser = s.e.attach(s.session.ID)
	time.Sleep(750 * time.Millisecond)
	if after := writes.snapshot(); len(after) != 1 {
		t.Fatalf("reconnect replayed the uncertain write: boundary writes grew from 1 to %d", len(after))
	}
	assertNoWriteStatus(t, s.browser, 500*time.Millisecond)
	// Clear the shell's deliberately partial edit buffer after all no-replay
	// assertions. This is a fresh user gesture, not recovery behavior.
	s.e.h.Tmux.SetWriteObserverForTest(nil)
	s.browser.input([]byte{0x15}) // Ctrl-U
	s.browser.awaitStatus(outcomeSent, 10*time.Second)
}

func testBrowserDisconnect(t *testing.T) {
	s := newFailureSetup(t)
	writes := &writeLog{}
	s.e.h.Tmux.SetWriteObserverForTest(writes.observe)
	defer s.e.h.Tmux.SetWriteObserverForTest(nil)

	data := []byte("printf 'once\\n' >> browser-disconnect.log\r")
	entered := make(chan struct{})
	release := make(chan struct{})
	s.e.h.Terminal.SetWriteFaultForTest(func(att *tmux.Attachment, body []byte) (int, error) {
		close(entered)
		<-release
		return att.WriteWithin(body, 5*time.Second)
	})
	defer s.e.h.Terminal.SetWriteFaultForTest(nil)

	s.browser.input(data)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("write never entered the controlled boundary")
	}
	s.browser.closeAndWait(5 * time.Second)
	if s.browser.tryInput([]byte("printf lost >> typed-after-disconnect.log\r")) {
		t.Fatal("the closed browser accepted input typed after disconnect")
	}
	close(release)

	deadline := time.Now().Add(30 * time.Second)
	for len(writes.snapshot()) == 0 && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
	}
	gotWrites := writes.snapshot()
	if len(gotWrites) != 1 || !bytes.Equal(gotWrites[0].Data, data) {
		t.Fatalf("browser-disconnect boundary = %+v, want zero-or-one test's one exact attempt", gotWrites)
	}
	path := filepath.Join(s.e.dir, "browser-disconnect.log")
	if got := awaitStableLineCount(t, path, 5*time.Second); got > 1 {
		t.Fatalf("in-flight browser-disconnect write executed %d times, want zero or one", got)
	}
	before := countFileLines(t, path)

	time.Sleep(500 * time.Millisecond)
	s.browser = s.e.attach(s.session.ID)
	assertNoWriteStatus(t, s.browser, 500*time.Millisecond)
	time.Sleep(750 * time.Millisecond)
	if got := countFileLines(t, path); got != before {
		t.Fatalf("browser reconnect replayed input: executions changed from %d to %d", before, got)
	}
	if _, err := os.Stat(filepath.Join(s.e.dir, "typed-after-disconnect.log")); !os.IsNotExist(err) {
		t.Fatalf("input typed after disconnect reached the shell: %v", err)
	}
}

func testDeadPane(t *testing.T) {
	s := newFailureSetup(t)
	oldPID := *s.session.PanePID
	s.browser.input([]byte("exit\r"))
	s.browser.awaitStatus(outcomeSent, 10*time.Second)

	deadline := time.Now().Add(10 * time.Second)
	var dead *tmux.PaneInfo
	for time.Now().Before(deadline) {
		info, err := s.e.h.Tmux.PaneInfoByID(context.Background(), *s.session.TmuxPaneID)
		if err == nil && info.Dead {
			dead = info
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if dead == nil {
		t.Fatal("shell exit did not leave the required dead remain-on-exit pane")
	}
	processDeadline := time.Now().Add(30 * time.Second)
	alive, err := tmux.ProcAlive(oldPID, *s.session.StartTicks)
	for err == nil && alive && time.Now().Before(processDeadline) {
		time.Sleep(25 * time.Millisecond)
		alive, err = tmux.ProcAlive(oldPID, *s.session.StartTicks)
	}
	if err != nil {
		t.Fatalf("verify exited shell identity: %v", err)
	}
	if alive {
		t.Fatalf("shell pid %d is still alive behind a dead pane", oldPID)
	}

	writes := &writeLog{}
	s.e.h.Tmux.SetWriteObserverForTest(writes.observe)
	defer s.e.h.Tmux.SetWriteObserverForTest(nil)
	s.browser.input([]byte("DEAD-PANE-MUST-NOT-CROSS"))
	status := s.browser.awaitStatus(outcomeFailed, 10*time.Second)
	assertNoDeliveryClaim(t, status)
	if got := writes.snapshot(); len(got) != 0 {
		t.Fatalf("dead pane produced %d tmux-boundary writes: %+v", len(got), got)
	}
	panes, err := s.e.h.Tmux.ListPanes(context.Background())
	if err != nil {
		t.Fatalf("inventory dead pane: %v", err)
	}
	if len(panes) != 1 || panes[0].PaneID != *s.session.TmuxPaneID || !panes[0].Dead {
		t.Fatalf("dead payload was replaced by a fallback pane: %+v", panes)
	}
}

func testTakeoverFence(t *testing.T) {
	s := newFailureSetup(t)
	writes := &writeLog{}
	s.e.h.Tmux.SetWriteObserverForTest(writes.observe)
	defer s.e.h.Tmux.SetWriteObserverForTest(nil)

	oldToken := s.browser.token
	started := time.Now()
	takeover := s.e.attachTakeover(s.session.ID)
	fenced := s.browser.awaitFrame(frameError, 10*time.Second)
	if fenced.Code != errFenced {
		t.Fatalf("displaced browser error = %q, want %q", fenced.Code, errFenced)
	}
	if elapsed := time.Since(started); elapsed >= 15*time.Second {
		t.Fatalf("takeover fenced the old controller after %s, want under 15s", elapsed)
	}
	if takeover.token <= oldToken {
		t.Fatalf("takeover token = %d, want greater than displaced token %d", takeover.token, oldToken)
	}

	marker := []byte("STALE-TAKEOVER-TOKEN-MUST-NOT-CROSS")
	sendRawInput(t, takeover, oldToken, takeover.gen, marker)
	status := takeover.awaitStatus(outcomeFailed, 10*time.Second)
	assertNoDeliveryClaim(t, status)
	if got := writes.snapshot(); len(got) != 0 {
		t.Fatalf("stale takeover token produced %d tmux-boundary writes: %+v", len(got), got)
	}
}

func assertNoDeliveryClaim(t *testing.T, status serverFrame) {
	t.Helper()
	lower := strings.ToLower(status.Message)
	for _, word := range []string{"delivered", "arrived", "accepted", "executed"} {
		if strings.Contains(lower, word) {
			t.Fatalf("%s write outcome overclaims terminal delivery: %q", status.Code, status.Message)
		}
	}
}

func assertNoWriteStatus(t *testing.T, b *browser, duration time.Duration) {
	t.Helper()
	deadline := time.Now().Add(duration)
	for time.Now().Before(deadline) {
		b.mu.Lock()
		status := b.status
		b.mu.Unlock()
		if status != nil {
			t.Fatalf("new attachment received a per-write classification for prior input: %+v", *status)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func countFileLines(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatalf("read execution witness: %v", err)
	}
	return len(bytes.Fields(data))
}

func awaitStableLineCount(t *testing.T, path string, timeout time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(timeout)
	last, stable := -1, 0
	for time.Now().Before(deadline) {
		got := countFileLines(t, path)
		if got == last {
			stable++
			if stable >= 10 {
				return got
			}
		} else {
			last, stable = got, 0
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("execution witness never stabilized: %s", path)
	return -1
}

// TestAcceptanceServiceProcess is a subprocess-only entry point used to make
// service loss a real SIGKILL boundary. It runs the same boot and server path
// as the production process; the parent owns its isolated tmux/DB cleanup.
func TestAcceptanceServiceProcess(t *testing.T) {
	path := os.Getenv(acceptanceServiceConfigEnv)
	if path == "" {
		t.Skip("acceptance service helper")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read helper config: %v", err)
	}
	var cfg config.Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("decode helper config: %v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	h, err := boot.Up(ctx, &cfg, zerolog.Nop())
	if err != nil {
		t.Fatalf("boot helper service: %v", err)
	}
	defer h.Close()
	if err := h.Run(ctx); err != nil {
		t.Fatalf("run helper service: %v", err)
	}
}

type serviceProcess struct {
	cmd    *exec.Cmd
	done   chan error
	logs   *bytes.Buffer
	exited atomic.Bool
}

func startServiceProcess(t *testing.T, configPath, base string) *serviceProcess {
	t.Helper()
	logs := &bytes.Buffer{}
	cmd := exec.Command(os.Args[0], "-test.run=^TestAcceptanceServiceProcess$", "-test.v")
	cmd.Env = append(os.Environ(), acceptanceServiceConfigEnv+"="+configPath)
	cmd.Stdout, cmd.Stderr = logs, logs
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper service: %v", err)
	}
	p := &serviceProcess{cmd: cmd, done: make(chan error, 1), logs: logs}
	go func() {
		err := cmd.Wait()
		p.exited.Store(true)
		p.done <- err
	}()

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-p.done:
			t.Fatalf("helper service exited before readiness: %v\n%s", err, logs.String())
		default:
		}
		resp, err := (&http.Client{Timeout: 250 * time.Millisecond}).Get(base + "/readyz")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return p
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
	<-p.done
	t.Fatalf("helper service never became ready\n%s", logs.String())
	return nil
}

func (p *serviceProcess) cleanup(t *testing.T) {
	t.Helper()
	if p.exited.Load() {
		return
	}
	_ = p.cmd.Process.Kill()
	select {
	case <-p.done:
	case <-time.After(10 * time.Second):
		t.Error("helper service cleanup did not observe process exit")
	}
}

func (p *serviceProcess) kill(t *testing.T) {
	t.Helper()
	if err := p.cmd.Process.Kill(); err != nil {
		t.Fatalf("SIGKILL helper service: %v", err)
	}
	select {
	case err := <-p.done:
		if err == nil {
			t.Fatal("SIGKILLed helper service exited successfully")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("SIGKILLed helper service did not exit")
	}
}

func (p *serviceProcess) stop(t *testing.T) {
	t.Helper()
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("stop helper service: %v", err)
	}
	select {
	case err := <-p.done:
		if err != nil {
			t.Fatalf("helper service stopped with error: %v\n%s", err, p.logs.String())
		}
	case <-time.After(15 * time.Second):
		_ = p.cmd.Process.Kill()
		t.Fatal("helper service did not stop after SIGTERM")
	}
}

func testServiceKill(t *testing.T) {
	dir := t.TempDir()
	bash, err := exec.LookPath("bash")
	if err != nil {
		acceptanceUnavailable(t, "bash not installed")
	}
	port := reservePort(t)
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	cfg := config.Defaults()
	cfg.Server.Bind = fmt.Sprintf("127.0.0.1:%d", port)
	cfg.Server.AllowedOrigins = []string{base}
	cfg.Database.Path = filepath.Join(dir, "run", "control.db")
	cfg.Database.LockPath = filepath.Join(dir, "run", "backend.lock")
	cfg.Tmux.SocketPath = tempTmuxSocket(t, "acceptance.sock")
	preparePrivateRuntimeDirs(t, &cfg)
	cfg.Projects.AllowedRoots = []string{dir}
	cfg.Auth.Mode = "trusted_proxy"
	cfg.Auth.SessionSecret = "test-secret"
	cfg.Auth.TrustedProxy.Addresses = []string{"127.0.0.1"}
	cfg.Auth.TrustedProxy.IdentityHeader = "X-Acceptance-User"
	cfg.Reconciler.Interval = config.Duration(200 * time.Millisecond)
	cfg.Providers.Shell.Executable = bash
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("encode helper config: %v", err)
	}
	configPath := filepath.Join(dir, "service-config.json")
	if err := os.WriteFile(configPath, raw, 0o600); err != nil {
		t.Fatalf("write helper config: %v", err)
	}

	cleanupTmux := func() {
		_ = exec.Command("tmux", "-S", cfg.Tmux.SocketPath, "kill-server").Run()
	}
	t.Cleanup(cleanupTmux)

	first := startServiceProcess(t, configPath, base)
	t.Cleanup(func() { first.cleanup(t) })
	e := &env{t: t, base: base, dir: dir, cfg: &cfg}
	s := e.createSession(e.registerProject(), "shell")
	s = e.awaitSession(s.ID, "running external shell", func(s sessionJSON) bool {
		return s.Status == "running" && s.PanePID != nil && s.TmuxPaneID != nil
	})
	b := e.attach(s.ID)
	awaitShellReady(t, e, b)
	before := requirePaneIdentity(t, s)
	t.Cleanup(func() {
		alive, err := tmux.ProcAlive(before.PanePID, before.StartTicks)
		if err != nil || !alive {
			return
		}
		pgid, err := tmux.ProcGroup(before.PanePID, before.StartTicks)
		if err == nil {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		}
	})

	path := filepath.Join(dir, "service-kill.log")
	b.input([]byte("printf 'once\\n' >> service-kill.log\r"))
	first.kill(t)
	b.closeAndWait(5 * time.Second)
	alive, err := tmux.ProcAlive(before.PanePID, before.StartTicks)
	if err != nil {
		t.Fatalf("verify shell process after service SIGKILL: %v", err)
	}
	if !alive {
		t.Fatalf("shell pid %d died with the service process", before.PanePID)
	}
	beforeCount := awaitStableLineCount(t, path, 5*time.Second)
	if beforeCount < 0 || beforeCount > 1 {
		t.Fatalf("in-flight service-kill write executed %d times, want zero or one", beforeCount)
	}

	second := startServiceProcess(t, configPath, base)
	t.Cleanup(func() { second.cleanup(t) })
	s = e.awaitSession(s.ID, "running after hard service restart", func(s sessionJSON) bool {
		return s.Status == "running" && s.PanePID != nil
	})
	after := requirePaneIdentity(t, s)
	if after != before {
		t.Fatalf("hard service restart changed runtime identity\nwant: %+v\n got: %+v", before, after)
	}
	b = e.attach(s.ID)
	assertNoWriteStatus(t, b, 500*time.Millisecond)
	time.Sleep(750 * time.Millisecond)
	if got := countFileLines(t, path); got != beforeCount {
		t.Fatalf("service restart replayed in-flight input: executions changed from %d to %d", beforeCount, got)
	}

	b.closeAndWait(5 * time.Second)
	second.stop(t)
	cleanupTmux()
	deadline := time.Now().Add(30 * time.Second)
	alive, err = tmux.ProcAlive(before.PanePID, before.StartTicks)
	for err == nil && alive && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
		alive, err = tmux.ProcAlive(before.PanePID, before.StartTicks)
	}
	if err != nil {
		t.Fatalf("verify external shell cleanup: %v", err)
	}
	if alive {
		t.Fatalf("test-owned shell pid %d survived tmux cleanup", before.PanePID)
	}
}

func reservePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve service port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		t.Fatalf("release service port: %v", err)
	}
	return port
}

type paneIdentity struct {
	TmuxSocketPath  string
	TmuxSessionName string
	TmuxSessionID   string
	TmuxPaneID      string
	Generation      int
	IdentityMarker  string
	PanePID         int
	ProcessGroupID  int
	StartTicks      int64
}

func requirePaneIdentity(t *testing.T, s sessionJSON) paneIdentity {
	t.Helper()
	if s.TmuxSessionID == nil || s.TmuxPaneID == nil || s.IdentityMarker == nil ||
		s.PanePID == nil || s.ProcessGroupID == nil || s.StartTicks == nil {
		t.Fatalf("session has incomplete pane identity: %+v", s)
	}
	return paneIdentity{
		TmuxSocketPath:  s.TmuxSocketPath,
		TmuxSessionName: s.TmuxSessionName,
		TmuxSessionID:   *s.TmuxSessionID,
		TmuxPaneID:      *s.TmuxPaneID,
		Generation:      s.Generation,
		IdentityMarker:  *s.IdentityMarker,
		PanePID:         *s.PanePID,
		ProcessGroupID:  *s.ProcessGroupID,
		StartTicks:      *s.StartTicks,
	}
}
