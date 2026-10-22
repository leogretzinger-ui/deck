// Package acceptance is the end-to-end acceptance suite: the
// release gates run against the REAL installed CLIs — never fakes, never
// stand-ins. A release claim about a CLI is only worth what a real CLI and
// a real interruption prove.
//
// Every test boots a real backend on a per-test temp namespace — real
// flock, real SQLite, real tmux socket, real HTTP socket, every namespace
// a per-test temp dir — launches the real CLI through the API, and drives
// it over a real WebSocket browser client. Ordinary go test and make verify
// runs skip this model-consuming suite; make acceptance is the opt-in,
// fail-closed release gate and treats every unavailable real-CLI
// prerequisite as failure.
package acceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/noisefloor-oss/deck/internal/boot"
	"github.com/noisefloor-oss/deck/internal/config"
	"github.com/noisefloor-oss/deck/internal/provider"
	"github.com/noisefloor-oss/deck/internal/tmux"
)

const strictAcceptanceEnv = "NOISEDECK_ACCEPTANCE_REQUIRED"

// env is one acceptance scenario's world.
type env struct {
	t    *testing.T
	h    *boot.Handles
	base string
	dir  string
	cfg  *config.Config

	current *service

	sessionIDs []string
}

// service is one generation of the HTTP server (a restart makes a new one).
// stopService is the sole owner of consuming done; per-generation cleanup
// callbacks would race teardown for that single result after a restart.
type service struct {
	cancel context.CancelFunc
	done   chan error
}

// newEnv boots a backend with every provider that exists on this host
// wired to its real executable. Skips on missing tmux.
func newEnv(t *testing.T) *env {
	return newEnvWith(t, nil)
}

// newEnvWith is newEnv plus a pre-boot configuration hook for tests that
// must point providers at test-owned homes.
func newEnvWith(t *testing.T, mutate func(*config.Config)) *env {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		acceptanceUnavailable(t, "tmux not installed")
	}
	dir := t.TempDir()
	// Project-local rules make the required native Bash approval deterministic
	// without changing the user's global Claude preferences. Ask rules win
	// over allow rules, and disabling sandbox auto-approval keeps the actual
	// permission modal in the path exercised by criterion 5.
	claudeDir := filepath.Join(dir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o700); err != nil {
		t.Fatalf("create Claude test settings directory: %v", err)
	}
	const claudeSettings = `{
  "permissions": {"defaultMode": "default", "ask": ["Bash"]},
  "sandbox": {"enabled": false, "autoAllowBashIfSandboxed": false}
}`
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.local.json"),
		[]byte(claudeSettings), 0o600); err != nil {
		t.Fatalf("write Claude test settings: %v", err)
	}
	cfg := config.Defaults()
	// A fixed free port: the WS Origin check matches the allowlist
	// exactly, so the port must be known before boot.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	cfg.Server.Bind = fmt.Sprintf("127.0.0.1:%d", port)
	cfg.Server.AllowedOrigins = []string{fmt.Sprintf("http://127.0.0.1:%d", port)}
	cfg.Database.Path = filepath.Join(dir, "run", "control.db")
	cfg.Database.LockPath = filepath.Join(dir, "run", "backend.lock")
	cfg.Tmux.SocketPath = tempTmuxSocket(t, "ac.sock")
	preparePrivateRuntimeDirs(t, &cfg)
	cfg.Projects.AllowedRoots = []string{dir}
	cfg.Auth.SessionSecret = "test-secret"
	// trusted_proxy auth: the acceptance client authenticates with the
	// identity header from a loopback peer, which is the only shape a
	// trusted-proxy identity is accepted in.
	cfg.Auth.Mode = "trusted_proxy"
	cfg.Auth.TrustedProxy.Addresses = []string{"127.0.0.1"}
	cfg.Auth.TrustedProxy.IdentityHeader = "X-Acceptance-User"
	cfg.Reconciler.Interval = config.Duration(200 * time.Millisecond)
	if mutate != nil {
		mutate(&cfg)
	}
	for name, p := range map[string]*config.Provider{
		"claude": &cfg.Providers.Claude,
		"codex":  &cfg.Providers.Codex,
		"kimi":   &cfg.Providers.Kimi,
	} {
		if path, err := exec.LookPath(name); err == nil {
			p.Executable = path
		}
	}
	h, err := boot.Up(context.Background(), &cfg, zerolog.Nop())
	if err != nil {
		t.Fatalf("boot.Up: %v", err)
	}
	pinAcceptanceClaude(h)
	e := &env{t: t, h: h, dir: dir, cfg: &cfg}
	// Register the one owner of service shutdown before serve: if serve fails
	// after Run starts, teardown still closes the handles and tmux namespace.
	t.Cleanup(e.teardown)
	e.base = e.serve()
	return e
}

// tempTmuxSocket keeps the Unix socket path well below sockaddr_un's
// platform limit even when a long subtest name makes testing.T.TempDir long.
// The random private directory is still one test's dedicated namespace.
func tempTmuxSocket(t *testing.T, name string) string {
	t.Helper()
	// The acceptance suite is Linux-only (/proc and tmux are part of its
	// contract). Use the configured process temp root rather than testing.T's
	// long test-qualified path; build farms may need a short task-private
	// TMPDIR to stay within sockaddr_un's path limit.
	dir, err := os.MkdirTemp("", "nd-tmux-")
	if err != nil {
		t.Fatalf("create acceptance tmux namespace: %v", err)
	}
	// #nosec G302 -- this is a directory; owner-only traversal requires 0700.
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("secure acceptance tmux namespace: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Errorf("remove acceptance tmux namespace: %v", err)
		}
	})
	return filepath.Join(dir, name)
}

// preparePrivateRuntimeDirs makes the acceptance namespace private before
// boot verifies it. testing.T.TempDir's numbered child is deliberately 0777
// on Go 1.26, and some test hosts propagate that mode to newly materialized
// paths. The production boot still owns validation; the harness supplies the
// secure deployment precondition: an owner-only runtime directory.
func preparePrivateRuntimeDirs(t *testing.T, cfg *config.Config) {
	t.Helper()
	// Lifecycle acceptance must not register names in the user's a2a state.
	cfg.Chats.Executable = filepath.Join(filepath.Dir(cfg.Database.Path), "no-a2a")
	for _, dir := range []string{
		filepath.Dir(cfg.Database.Path),
		filepath.Dir(cfg.Database.LockPath),
		filepath.Dir(cfg.Tmux.SocketPath),
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("create private acceptance runtime directory: %v", err)
		}
		// #nosec G302 -- these are directories; owner-only traversal requires 0700.
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Fatalf("secure acceptance runtime directory: %v", err)
		}
	}
}

// serve runs the backend and returns its live base URL.
func (e *env) serve() string {
	e.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	svc := &service{cancel: cancel, done: make(chan error, 1)}
	e.current = svc
	go func() { svc.done <- e.h.Run(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for e.h.Server.BoundAddr() == "" {
		select {
		case err := <-svc.done:
			e.current = nil
			e.t.Fatalf("server stopped before binding: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			e.t.Fatal("server never bound")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return "http://" + e.h.Server.BoundAddr()
}

func (e *env) startService(writeObserver func(tmux.WriteObservation)) {
	e.t.Helper()
	h, err := boot.Up(context.Background(), e.cfg, zerolog.Nop())
	if err != nil {
		e.t.Fatalf("reboot: %v", err)
	}
	pinAcceptanceClaude(h)
	// Install the boundary observer before Run starts any background work or
	// exposes HTTP. This closes the restart interval in criterion 5.
	h.Tmux.SetWriteObserverForTest(writeObserver)
	e.h = h
	e.base = e.serve()
}

// stopService terminates ONLY Deck: the HTTP server first,
// then the handles. The tmux namespace and every CLI it runs are
// untouched — that is the point of the restart criterion.
func (e *env) stopService() {
	e.t.Helper()
	if e.current != nil {
		e.current.cancel()
		select {
		case err := <-e.current.done:
			if err != nil {
				e.t.Errorf("backend stopped with error: %v", err)
			}
		case <-time.After(5 * time.Second):
			// Teardown must continue to Handles.Close and kill-server even if
			// Run wedges; Error records the failure without stranding the CLI.
			e.t.Error("backend did not stop")
		}
		e.current = nil
	}
	e.h.Close()
}

// requireCLI skips when the named CLI is not installed — a missing
// provider is an environment fact, not a failure. The product's own
// failure contract governs the product, not the test matrix.
func (e *env) requireCLI(name string) string {
	e.t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		acceptanceUnavailable(e.t, "%s not installed", name)
	}
	return path
}

// acceptanceUnavailable keeps ordinary development runs convenient while
// making the release gate fail closed. A formal gate sets
// NOISEDECK_ACCEPTANCE_REQUIRED=1 and therefore cannot report success after skipping
// a missing real-CLI prerequisite.
func acceptanceUnavailable(t *testing.T, format string, args ...any) {
	t.Helper()
	if os.Getenv(strictAcceptanceEnv) == "1" {
		t.Fatalf("required acceptance prerequisite unavailable: "+format, args...)
	}
	t.Skipf(format, args...)
}

// acceptanceClaudeDescriptor changes only test invocation options. The real
// executable, identity, environment, and measured injection contract remain
// the production descriptor's. Pinning Opus makes the gate independent of a
// user's saved default model; manual permission mode makes the required
// native approval-modal scenario reproducible.
type acceptanceClaudeDescriptor struct {
	provider.Descriptor
}

func (d *acceptanceClaudeDescriptor) NewSessionArgs(spec provider.LaunchSpec) ([]string, error) {
	argv, err := d.Descriptor.NewSessionArgs(spec)
	if err != nil {
		return nil, err
	}
	return append(argv, "--model", "opus", "--permission-mode", "manual"), nil
}

func (d *acceptanceClaudeDescriptor) ResumeSessionArgs(spec provider.ResumeSpec) ([]string, error) {
	argv, err := d.Descriptor.ResumeSessionArgs(spec)
	if err != nil {
		return nil, err
	}
	return append(argv, "--model", "opus", "--permission-mode", "manual"), nil
}

func pinAcceptanceClaude(h *boot.Handles) {
	d, ok := h.Providers[provider.Claude]
	if !ok {
		return
	}
	if _, pinned := d.(*acceptanceClaudeDescriptor); pinned {
		return
	}
	h.Providers[provider.Claude] = &acceptanceClaudeDescriptor{Descriptor: d}
}

// api is a minimal JSON client for the control API.
func (e *env) api(method, path string, body any) (int, []byte) {
	e.t.Helper()
	var req *http.Request
	var err error
	if body != nil {
		var buf []byte
		buf, err = json.Marshal(body)
		if err != nil {
			e.t.Fatalf("marshal: %v", err)
		}
		req, err = http.NewRequest(method, e.base+path, bytes.NewReader(buf))
	} else {
		req, err = http.NewRequest(method, e.base+path, nil)
	}
	if err != nil {
		e.t.Fatalf("request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Acceptance-User", "acceptance")
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		e.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func (e *env) apiJSON(method, path string, body any, out any) {
	e.t.Helper()
	code, b := e.api(method, path, body)
	if code >= 300 {
		e.t.Fatalf("%s %s = %d: %s", method, path, code, b)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			e.t.Fatalf("decode %s %s: %v (%s)", method, path, err, b)
		}
	}
}

// registerProject makes the temp dir a project and returns its id.
func (e *env) registerProject() string {
	e.t.Helper()
	var resp struct {
		ID string `json:"id"`
	}
	e.apiJSON(http.MethodPost, "/api/projects",
		map[string]string{
			"name": "acceptance", "path": e.dir,
			"git_author_name":  "Acceptance Test",
			"git_author_email": "acceptance@example.invalid",
		}, &resp)
	return resp.ID
}

// createSession launches a provider in the project and returns the
// session record.
func (e *env) createSession(projectID, provider string) sessionJSON {
	e.t.Helper()
	var s sessionJSON
	e.apiJSON(http.MethodPost, "/api/projects/"+projectID+"/sessions",
		map[string]string{"provider": provider}, &s)
	e.sessionIDs = append(e.sessionIDs, s.ID)
	return s
}

// teardown stops every created session it still can, stops the
// service, and kills the test's tmux server — no real CLI process or
// pane survives an acceptance test. A test that stopped its own service
// mid-run (the restart criterion) leaves nothing for the API path;
// kill-server is the honest teardown for what remains.
func (e *env) teardown() {
	for _, id := range e.sessionIDs {
		// Best effort, transport errors included: a test that stopped
		// its own service mid-run (the restart criterion) has no API to
		// stop through — kill-server below is the honest teardown.
		raw, _ := json.Marshal(map[string]any{"force": true, "confirmed": true})
		req, err := http.NewRequest(http.MethodPost,
			e.base+"/api/sessions/"+id+"/stop", bytes.NewReader(raw))
		if err != nil {
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Acceptance-User", "acceptance")
		if resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req); err == nil {
			_ = resp.Body.Close()
		}
	}
	e.stopService()
	if out, err := exec.Command("tmux", "-S", e.cfg.Tmux.SocketPath,
		"kill-server").CombinedOutput(); err != nil && len(out) > 0 {
		e.t.Logf("tmux kill-server: %v: %s", err, out)
	}
}

type sessionJSON struct {
	ID                 string  `json:"id"`
	Status             string  `json:"status"`
	TmuxSocketPath     string  `json:"tmux_socket_path"`
	TmuxSessionName    string  `json:"tmux_session_name"`
	TmuxSessionID      *string `json:"tmux_session_id,omitempty"`
	TmuxPaneID         *string `json:"tmux_pane_id,omitempty"`
	Generation         int     `json:"generation"`
	IdentityMarker     *string `json:"identity_marker,omitempty"`
	PanePID            *int    `json:"pane_pid,omitempty"`
	ProcessGroupID     *int    `json:"process_group_id,omitempty"`
	StartTicks         *int64  `json:"start_ticks,omitempty"`
	ExpectedExecutable *string `json:"expected_executable,omitempty"`
	BootEpoch          *string `json:"boot_epoch,omitempty"`
	NativeSessionID    *string `json:"native_session_id,omitempty"`
	ExitCode           *int    `json:"exit_code,omitempty"`
	ExitSignal         *string `json:"exit_signal,omitempty"`
	PresentationState  string  `json:"presentation_state"`
}

func (e *env) getSession(id string) sessionJSON {
	e.t.Helper()
	var s sessionJSON
	e.apiJSON(http.MethodGet, "/api/sessions/"+id, nil, &s)
	return s
}

// awaitSession polls until the predicate holds or the deadline passes.
func (e *env) awaitSession(id string, what string, ok func(sessionJSON) bool) sessionJSON {
	e.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
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

// awaitPaneSize observes tmux in this test's dedicated namespace. Sending a
// resize frame is not itself proof that the terminal applied it.
func (e *env) awaitPaneSize(paneID string, cols, rows int, timeout time.Duration) {
	e.t.Helper()
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		out, err := exec.Command("tmux", "-S", e.cfg.Tmux.SocketPath,
			"display-message", "-p", "-t", paneID, "-F",
			"#{pane_width} #{pane_height}").CombinedOutput()
		last = strings.TrimSpace(string(out))
		if err == nil {
			var gotCols, gotRows int
			if _, scanErr := fmt.Sscanf(last, "%d %d", &gotCols, &gotRows); scanErr == nil &&
				gotCols == cols && gotRows == rows {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	e.t.Fatalf("pane %s size never reached %dx%d (last observation %q)",
		paneID, cols, rows, last)
}

// processAlive checks /proc directly — the acceptance bar is observed
// process reality, matching the product's own rule that status is derived
// from runtime, not from the last write.
func processAlive(pid int) bool {
	_, err := os.Stat(fmt.Sprintf("/proc/%d", pid))
	return err == nil
}

// skipIfOutOfCredits reports a Claude native UI that cannot run turns because
// the plan is exhausted. Callers pass the CURRENT visible pane, never the
// accumulated conversation: model prose can legitimately discuss limits, and
// must not be mistaken for a quota banner.
func (e *env) skipIfOutOfCredits(output string) {
	lower := strings.ToLower(output)
	for _, line := range strings.Split(lower, "\n") {
		line = strings.Join(strings.Fields(line), " ")
		quotaLine := strings.Contains(line, "opus") && strings.Contains(line, "limit")
		for _, marker := range []string{
			"out of usage credits",
			"usage limit reached",
			"you've hit your usage limit",
			"you've hit your limit",
			"not enough usage left",
		} {
			quotaLine = quotaLine || strings.Contains(line, marker)
		}
		if quotaLine {
			acceptanceUnavailable(e.t,
				"claude has no usable Opus allocation on this host — model-dependent scenario cannot run")
		}
	}
}

// processCWD resolves the observed process's working directory through
// /proc — criterion 1's "launches in the selected project directory" is
// a filesystem fact, not a pane observation.
func processCWD(pid int) (string, error) {
	return os.Readlink(fmt.Sprintf("/proc/%d/cwd", pid))
}
