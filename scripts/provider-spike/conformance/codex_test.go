//go:build linux

package conformance

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/noisefloor-oss/deck/internal/tmux"
	"golang.org/x/sys/unix"
)

const (
	realCodexEnv      = "NOISEDECK_REAL_CODEX"
	codexExecutable   = "/usr/local/bin/codex"
	codexVersion      = "codex-cli 0.145.0"
	codexModel        = "gpt-5.6-sol"
	initialReply      = "NOISEDECK_CODEX_READY_314159"
	bracketReply      = "NOISEDECK_BRACKET_REPLY_271828"
	literalReply      = "NOISEDECK_LITERAL_REPLY_161803"
	literalMultiReply = "NOISEDECK_LITERAL_MULTI_REPLY_141421"
	activeReply       = "NOISEDECK_ACTIVE_QUEUED_173205"
	activeBarrier     = "NOISEDECK_ACTIVE_BARRIER_187083"
	modalReply        = "NOISEDECK_MODAL_RECOVERED_193649"
	modalReplySubmit  = "NOISEDECK_MODAL_SUBMIT_RECOVERED_212132"
	interruptReply    = "NOISEDECK_INTERRUPT_RECOVERED_223606"
	interruptBarrier  = "NOISEDECK_INTERRUPT_BARRIER_231840"
	continuityWord    = "NOISEDECK_CONTINUITY_244949"
	continuityAck     = "NOISEDECK_CONTINUITY_STORED_264575"
)

var canonicalUUID = regexp.MustCompile(
	`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`,
)

type hookEvent struct {
	SessionID string `json:"session_id"`
	CWD       string `json:"cwd"`
	Source    string `json:"source"`
}

type processIdentity struct {
	PID        int    `json:"pid"`
	StartTicks int64  `json:"start_ticks"`
	BootID     string `json:"boot_id"`
}

type runMetadata struct {
	BootID        string `json:"boot_id"`
	EphemeralHome string `json:"ephemeral_home"`
}

type codexHarness struct {
	t            *testing.T
	root         string
	dir          string
	home         string
	project      string
	hookSocket   string
	hookListener net.Listener
	hookMu       sync.Mutex
	hookEvents   []hookEvent
	hookErr      error
	manifest     string
	bootID       string
	authFile     *os.File
	client       *tmux.Client
	paneID       string
	tracked      []processIdentity
	// cleanupCertifiable is false during any window in which a provider or
	// test-owned helper may exist before its exact identity is published.
	cleanupCertifiable bool
}

// TestCodexProviderContract is the executable Phase 0 contract for the real
// pinned Codex TUI. Ordinary unit-test runs compile but skip it; the Makefile
// target opts in and therefore fails closed on every missing prerequisite.
//
// This test may observe the VISIBLE terminal as black-box compatibility
// evidence. Product code may not use these observations to infer identity,
// busy/idle state, modal state, or delivery.
func TestCodexProviderContract(t *testing.T) {
	if os.Getenv(realCodexEnv) != "1" {
		t.Skip("real Codex contract probe is opt-in; run make provider-spike-codex")
	}

	h := newCodexHarness(t)
	ctx := context.Background()
	h.provisionNativeTrust()

	h.cleanupCertifiable = false
	launch, err := h.client.CreateSession(ctx, tmux.SessionSpec{
		Name:       "codex-contract",
		WorkDir:    h.project,
		Argv:       []string{codexExecutable},
		Env:        []string{"CODEX_HOME=" + h.home},
		Generation: 1,
		Marker:     "codex-contract-generation-1",
	})
	if err != nil {
		t.Fatalf("launch real Codex in the private tmux namespace: %v", sanitizedErr(err))
	}
	h.paneID = launch.PaneID
	h.track(launch)
	h.cleanupCertifiable = true
	h.waitReady(30*time.Second, false)
	h.assertNoHookEvents(10 * time.Second)

	h.submitPaste(initialPrompt())
	startup := h.waitHookEvents(1, 10*time.Second)
	requireStartupEvent(t, startup[0], h.project)
	if strings.Contains(h.captureVisible(), "• "+initialReply) {
		t.Fatal("first reply was already visible when the startup hook was first observed")
	}
	h.waitReply(initialReply, 90*time.Second)

	t.Log("codex_contract_phase=submit_sequence")
	h.measureSubmitSequence()
	t.Log("codex_contract_phase=bracketed_multiline")
	h.measureBracketedMultiline()
	t.Log("codex_contract_phase=literal_input")
	h.measureLiteralInput()
	t.Log("codex_contract_phase=literal_multiline")
	h.measureLiteralMultiline()
	t.Log("codex_contract_phase=active_turn_queue")
	h.measureActiveTurnQueue()
	t.Log("codex_contract_phase=modal_discard")
	h.measureModalDiscard()
	t.Log("codex_contract_phase=escape_interrupt")
	h.measureEscapeInterrupt()

	h.submitPaste("Remember the code word " + continuityWord +
		" for this conversation, then reply only with " + continuityAck + ".")
	h.waitReply(continuityAck, 90*time.Second)
	h.waitIdle(10 * time.Second)

	preExit := h.requireLivePane()
	t.Log("codex_contract_phase=idle_ctrl_d")
	h.sendKey("C-d")
	normalExit := h.waitDead(h.paneID, 15*time.Second)
	requireExitShape(t, "idle Ctrl-D", normalExit, 0, nil)

	h.cleanupCertifiable = false
	resumed, err := h.client.RespawnPane(ctx, h.paneID, tmux.SessionSpec{
		WorkDir:    h.project,
		Argv:       []string{codexExecutable, "resume", startup[0].SessionID},
		Env:        []string{"CODEX_HOME=" + h.home},
		Generation: 2,
		Marker:     "codex-contract-generation-2",
	})
	if err != nil {
		// The exact native id is intentionally absent from this diagnostic.
		t.Fatal("exact native resume failed in the recorded dead pane (detail suppressed)")
	}
	h.track(resumed)
	h.cleanupCertifiable = true
	if resumed.PaneID != preExit.PaneID || resumed.PID == preExit.PID ||
		resumed.StartTicks == preExit.StartTicks {
		t.Fatalf("respawn identity mismatch: same_pane=%t new_pid=%t new_start_ticks=%t",
			resumed.PaneID == preExit.PaneID, resumed.PID != preExit.PID,
			resumed.StartTicks != preExit.StartTicks)
	}
	h.waitReady(30*time.Second, false)

	t.Log("codex_contract_phase=exact_resume")
	h.submitPaste("Reply only with the code word I asked you to remember immediately before exit.")
	h.waitReply(continuityWord, 90*time.Second)
	resumeEvents := h.waitHookEvents(2, 10*time.Second)
	requireResumeEvents(t, resumeEvents, h.project)

	h.cleanupCertifiable = false
	unknown, err := h.client.CreateSession(ctx, tmux.SessionSpec{
		Name:       "codex-unknown-id",
		WorkDir:    h.project,
		Argv:       []string{codexExecutable, "resume", "00000000-0000-0000-0000-000000000000"},
		Env:        []string{"CODEX_HOME=" + h.home},
		Generation: 1,
		Marker:     "codex-contract-unknown-id",
	})
	if err != nil {
		t.Fatalf("launch unknown-id refusal probe: %v", sanitizedErr(err))
	}
	if !unknown.Dead {
		h.track(unknown)
	}
	h.cleanupCertifiable = true
	unknownExit := h.waitDead(unknown.PaneID, 15*time.Second)
	requireExitShape(t, "unknown exact id", unknownExit, 1, nil)
	unknownQuietUntil := time.Now().Add(2 * time.Second)
	for time.Now().Before(unknownQuietUntil) {
		if got := len(h.readHookEvents()); got != 2 {
			t.Fatalf("unknown exact id changed hook event count: got=%d want=2", got)
		}
		time.Sleep(100 * time.Millisecond)
	}

	preTerm := h.requireLivePane()
	t.Log("codex_contract_phase=sigterm")
	if !processHasIdentity(h.paneIdentity(preTerm)) || preTerm.ProcessGroupID <= 1 ||
		preTerm.ProcessGroupID != preTerm.PID {
		t.Fatalf("refusing SIGTERM: validated_private_group=%t",
			processHasIdentity(h.paneIdentity(preTerm)) && preTerm.ProcessGroupID > 1 &&
				preTerm.ProcessGroupID == preTerm.PID)
	}
	if err := syscall.Kill(-preTerm.ProcessGroupID, syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM the exact private pane process group: %v", err)
	}
	termExit := h.waitDead(h.paneID, 15*time.Second)
	requireExitShape(t, "SIGTERM", termExit, 0, nil)

	t.Log(`codex_contract={"version":"0.145.0","launch":"bare","hook_identity":true,` +
		`"exact_resume":true,"unknown_id_fails":true,"bracketed_paste":true,` +
		`"literal_input":true,"multiline":true,"active_turn":"queued_once",` +
		`"modal_input":"silently_discarded","interrupt":"escape_conversation_only",` +
		`"ctrl_d_status":0,"sigterm_status":0,"same_pane_respawn":true}`)
}

func newCodexHarness(t *testing.T) *codexHarness {
	t.Helper()
	requireCommand(t, "tmux")
	requireCommand(t, "git")
	if os.Getenv("NOISEDECK_SPIKE_HOOK_COMMAND") == "" {
		requireCommand(t, "python3")
	}
	if info, err := os.Stat(codexExecutable); err != nil || info.IsDir() {
		t.Fatalf("required pinned Codex executable unavailable at %s", codexExecutable)
	}
	version, err := exec.Command(codexExecutable, "--version").Output()
	if err != nil || strings.TrimSpace(string(version)) != codexVersion {
		t.Fatalf("Codex version mismatch: exact_0_145_0=%t",
			err == nil && strings.TrimSpace(string(version)) == codexVersion)
	}

	root := repositoryRoot(t)
	if err := ensurePrivateDir(filepath.Join(root, ".review")); err != nil {
		t.Fatalf("secure review scratch root: %v", err)
	}
	base := filepath.Join(root, ".review", "ps")
	if err := ensurePrivateDir(base); err != nil {
		t.Fatalf("secure provider-spike scratch root: %v", err)
	}
	bootID := requireBootID(t)
	bootToken, ok := bootIDToken(bootID)
	if !ok {
		t.Fatal("encode Linux boot identity for private run name")
	}
	dir, err := os.MkdirTemp(base, "c-"+bootToken+"-")
	if err != nil {
		t.Fatalf("create provider-spike scratch directory: %v", err)
	}
	if err := ensurePrivateDir(dir); err != nil {
		t.Fatalf("secure provider-spike scratch directory: %v", err)
	}
	if err := requireTmpfs("/dev/shm"); err != nil {
		t.Fatalf("secure in-memory provider home prerequisite: %v", err)
	}
	home := ephemeralHomeForRun(dir)
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatalf("create in-memory provider home: %v", err)
	}
	if err := ensurePrivateDir(home); err != nil {
		t.Fatalf("secure in-memory provider home: %v", err)
	}
	metadata, err := json.Marshal(runMetadata{BootID: bootID, EphemeralHome: home})
	if err != nil {
		t.Fatalf("marshal private run metadata: %v", err)
	}
	metadataPath := filepath.Join(dir, "run.json")
	if err := os.WriteFile(metadataPath+".new", metadata, 0o600); err != nil {
		t.Fatalf("write private run metadata: %v", err)
	}
	if err := os.Rename(metadataPath+".new", metadataPath); err != nil {
		t.Fatalf("publish private run metadata: %v", err)
	}

	h := &codexHarness{
		t:                  t,
		root:               root,
		dir:                dir,
		home:               home,
		project:            filepath.Join(dir, "project"),
		hookSocket:         filepath.Join(dir, "hook.sock"),
		manifest:           filepath.Join(dir, "processes.json"),
		bootID:             bootID,
		cleanupCertifiable: true,
	}
	t.Cleanup(h.cleanup)
	h.prepareFixture()

	h.client, err = tmux.NewClient(tmux.Config{SocketPath: filepath.Join(dir, "tmux", "ac.sock")})
	if err != nil {
		t.Fatalf("construct private tmux client: %v", err)
	}
	return h
}

func (h *codexHarness) prepareFixture() {
	h.t.Helper()
	for _, dir := range []string{h.project, filepath.Join(h.dir, "tmux")} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			h.t.Fatalf("create isolated fixture directory: %v", err)
		}
		if err := ensurePrivateDir(dir); err != nil {
			h.t.Fatalf("secure isolated fixture directory: %v", err)
		}
	}
	cmd := exec.Command("git", "init", "--quiet", h.project)
	if out, err := cmd.CombinedOutput(); err != nil {
		h.t.Fatalf("initialize test-owned project: %v (output_bytes=%d)", err, len(out))
	}

	auth := os.Getenv("CODEX_SPIKE_AUTH_JSON")
	if auth == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			h.t.Fatal("resolve provider credential home")
		}
		auth = filepath.Join(userHome, ".codex", "auth.json")
	}
	if info, err := os.Stat(auth); err != nil || info.IsDir() {
		h.t.Fatal("Codex auth prerequisite unavailable; set CODEX_SPIKE_AUTH_JSON")
	}
	source, err := os.Open(auth)
	if err != nil {
		h.t.Fatal("open provider-owned auth prerequisite")
	}
	defer func() { _ = source.Close() }()
	fd, err := unix.MemfdCreate("noisedeck-codex-auth", unix.MFD_CLOEXEC)
	if err != nil {
		h.t.Fatalf("create process-owned in-memory auth fixture: %v", err)
	}
	h.authFile = os.NewFile(uintptr(fd), "noisedeck-codex-auth")
	if _, err := io.Copy(h.authFile, source); err != nil {
		h.t.Fatal("copy provider auth into process-owned memory")
	}
	if _, err := h.authFile.Seek(0, 0); err != nil {
		h.t.Fatal("rewind process-owned in-memory auth fixture")
	}
	if err := h.authFile.Chmod(0o600); err != nil {
		h.t.Fatalf("secure process-owned in-memory auth fixture: %v", err)
	}
	authLink := fmt.Sprintf("/proc/%d/fd/%d", os.Getpid(), fd)
	if err := os.Symlink(authLink, filepath.Join(h.home, "auth.json")); err != nil {
		h.t.Fatalf("reference process-owned in-memory auth from isolated home: %v", err)
	}

	listener, err := net.Listen("unix", h.hookSocket)
	if err != nil {
		h.t.Fatalf("open test-owned in-memory hook sink: %v", err)
	}
	h.hookListener = listener
	if err := os.Chmod(h.hookSocket, 0o600); err != nil {
		h.t.Fatalf("secure test-owned hook socket: %v", err)
	}
	go h.serveHookEvents()

	config := fmt.Sprintf(`model = %s
model_reasoning_effort = "low"
service_tier = "fast"
approval_policy = "never"
sandbox_mode = "danger-full-access"
check_for_update_on_startup = false

[features]
hooks = true

[projects.%s]
trust_level = "trusted"
`, tomlQuote(codexModel), tomlQuote(h.project))
	if err := os.WriteFile(filepath.Join(h.home, "config.toml"), []byte(config), 0o600); err != nil {
		h.t.Fatalf("write isolated Codex config: %v", err)
	}

	capturePath := filepath.Join(h.dir, "capture_hook.py")
	if err := os.WriteFile(capturePath, []byte(hookCaptureProgram), 0o700); err != nil {
		h.t.Fatalf("write test-owned hook receiver: %v", err)
	}
	hookCommand := "python3 " + shellQuote(capturePath) + " " + shellQuote(h.hookSocket)
	if measured := os.Getenv("NOISEDECK_SPIKE_HOOK_COMMAND"); measured != "" {
		hookCommand = measured
	}
	hooks := map[string]any{
		"hooks": map[string]any{
			"SessionStart": []any{map[string]any{
				"hooks": []any{map[string]any{
					"type":    "command",
					"command": hookCommand,
				}},
			}},
		},
	}
	hookJSON, err := json.MarshalIndent(hooks, "", "  ")
	if err != nil {
		h.t.Fatalf("marshal test-owned hook config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(h.home, "hooks.json"), hookJSON, 0o600); err != nil {
		h.t.Fatalf("write test-owned hook config: %v", err)
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}
	root := strings.TrimSpace(string(out))
	if !filepath.IsAbs(root) || root == string(filepath.Separator) {
		t.Fatalf("refusing unsafe repository root: absolute_nonroot=%t",
			filepath.IsAbs(root) && root != string(filepath.Separator))
	}
	return root
}

func requireBootID(t *testing.T) string {
	t.Helper()
	bootID, ok := currentBootID()
	if !ok {
		t.Fatal("read canonical Linux boot identity for exact process cleanup")
	}
	return bootID
}

func currentBootID() (string, bool) {
	raw, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", false
	}
	bootID := strings.TrimSpace(string(raw))
	return bootID, canonicalUUID.MatchString(bootID)
}

func bootIDToken(bootID string) (string, bool) {
	raw, err := hex.DecodeString(strings.ReplaceAll(bootID, "-", ""))
	if err != nil || len(raw) != 16 {
		return "", false
	}
	return base64.RawURLEncoding.EncodeToString(raw), true
}

func ephemeralHomeForRun(dir string) string {
	return filepath.Join("/dev/shm", "noisedeck-"+filepath.Base(dir))
}

func requireTmpfs(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("in-memory scratch path is not a real directory")
	}
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil || stat.Type != unix.TMPFS_MAGIC {
		return errors.New("in-memory scratch path is not tmpfs")
	}
	return nil
}

func ensurePrivateDir(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(path, 0o700); err != nil {
			return err
		}
		return validatePrivateDir(path)
	}
	if err != nil {
		return err
	}
	return validatePrivateDirInfo(path, info)
}

func validatePrivateDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	return validatePrivateDirInfo(path, info)
}

func validatePrivateDirInfo(path string, info os.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("private scratch path is not a real directory")
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return errors.New("open private scratch directory without following links")
	}
	defer func() { _ = unix.Close(fd) }()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Uid != uint32(os.Geteuid()) ||
		stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return errors.New("private scratch directory owner or type is unsafe")
	}
	if err := unix.Fchmod(fd, 0o700); err != nil {
		return errors.New("secure private scratch directory permissions")
	}
	return nil
}

func removeEphemeralHome(path, runDir string) error {
	want := ephemeralHomeForRun(runDir)
	if path != want || !strings.HasPrefix(path, "/dev/shm/noisedeck-c-") {
		return errors.New("refusing unsafe in-memory provider-home cleanup")
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return os.Remove(path)
	}
	return os.RemoveAll(path)
}

func requireCommand(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Fatalf("required executable unavailable: %s", name)
	}
}

func tomlQuote(value string) string { return strconv.Quote(value) }

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

const hookCaptureProgram = `#!/usr/bin/env python3
import json
import socket
import sys

payload = json.load(sys.stdin)
if payload.get("hook_event_name") != "SessionStart":
    raise SystemExit(2)
record = {
    "session_id": payload.get("session_id"),
    "cwd": payload.get("cwd"),
    "source": payload.get("source"),
}
if not isinstance(record["session_id"], str) or not record["session_id"]:
    raise SystemExit(3)
wire = (json.dumps(record, separators=(",", ":")) + "\n").encode("utf-8")
with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as sink:
    sink.connect(sys.argv[1])
    sink.sendall(wire)
`

func initialPrompt() string {
	return "Reply only with " + initialReply + "."
}

// provisionNativeTrust is fixture setup, not a product launch. Provisioning
// owns Codex hook installation/trust; Deck must never answer native
// modals. The measured launch below starts only after this test-owned setup
// process has persisted Codex's trust decision in the isolated home. It
// reports whether the native hook-trust modal was observed and answered, so
// trust measurements can require the modal path rather than assume it.
func (h *codexHarness) provisionNativeTrust() bool {
	h.t.Helper()
	h.cleanupCertifiable = false
	provision, err := h.client.CreateSession(context.Background(), tmux.SessionSpec{
		Name:       "codex-provision",
		WorkDir:    h.project,
		Argv:       []string{codexExecutable},
		Env:        []string{"CODEX_HOME=" + h.home},
		Generation: 1,
		Marker:     "codex-contract-provisioning",
	})
	if err != nil {
		h.t.Fatalf("launch isolated Codex provisioning pass: %v", sanitizedErr(err))
	}
	h.paneID = provision.PaneID
	h.track(provision)
	h.cleanupCertifiable = true
	modalAnswered := h.waitReady(30*time.Second, true)
	if !processHasIdentity(h.paneIdentity(provision)) || provision.ProcessGroupID <= 1 ||
		provision.ProcessGroupID != provision.PID {
		h.t.Fatal("provisioning process group identity changed before bounded stop")
	}
	if err := syscall.Kill(-provision.ProcessGroupID, syscall.SIGTERM); err != nil {
		h.t.Fatalf("stop isolated provisioning process: %v", sanitizedErr(err))
	}
	exit := h.waitDead(provision.PaneID, 15*time.Second)
	requireExitShape(h.t, "provisioning SIGTERM", exit, 0, nil)
	if got := len(h.readHookEvents()); got != 0 {
		h.t.Fatalf("provisioning pass unexpectedly emitted hook events: got=%d", got)
	}
	return modalAnswered
}

// waitReady reports whether the native hook-trust modal was observed and
// answered during this launch.
func (h *codexHarness) waitReady(timeout time.Duration, allowProvisioningModals bool) bool {
	h.t.Helper()
	deadline := time.Now().Add(timeout)
	stable := 0
	answeredHookTrust := false
	answeredProjectTrust := false
	var updateAnsweredAt time.Time
	for time.Now().Before(deadline) {
		info, err := h.client.PaneInfoByID(context.Background(), h.paneID)
		if err != nil {
			h.t.Fatalf("observe Codex startup pane: %v", sanitizedErr(err))
		}
		if info.Dead {
			h.t.Fatalf("Codex exited before ready: %s", exitLabel(info))
		}
		screen := h.captureVisible()
		lower := strings.ToLower(screen)
		h.failOnProviderUnavailable(lower)

		switch {
		case strings.Contains(lower, "hooks need review") &&
			strings.Contains(lower, "trust all and continue"):
			stable = 0
			if !allowProvisioningModals {
				h.t.Fatal("measured launch reached unprovisioned native hook-trust modal")
			}
			if answeredHookTrust {
				h.t.Fatal("Codex hook trust modal remained after the bounded answer")
			}
			h.sendKey("Down")
			h.sendKey("Enter")
			answeredHookTrust = true
		case projectTrustVisible(lower):
			stable = 0
			if !allowProvisioningModals {
				h.t.Fatal("measured launch reached unprovisioned project-trust modal")
			}
			if answeredProjectTrust {
				h.t.Fatal("Codex project trust modal remained after the bounded answer")
			}
			h.sendKey("Enter")
			answeredProjectTrust = true
		case strings.Contains(lower, "skip until next version"):
			stable = 0
			if !allowProvisioningModals {
				h.t.Fatal("measured launch reached an unhandled native update picker")
			}
			if !updateAnsweredAt.IsZero() && time.Since(updateAnsweredAt) > 3*time.Second {
				h.t.Fatal("Codex update picker remained after selecting the skip action")
			}
			if updateAnsweredAt.IsZero() {
				// The measured 0.145.0 picker starts on "Update now"; two Down keys
				// select "Skip until next version". This cannot install a newer
				// binary and invalidate the version-pinned run.
				h.sendKey("Down")
				h.sendKey("Down")
				h.sendKey("Enter")
				updateAnsweredAt = time.Now()
			}
		case strings.Contains(lower, codexModel) &&
			!strings.Contains(lower, "hooks need review"):
			stable++
			if stable >= 5 {
				return answeredHookTrust
			}
		default:
			stable = 0
		}
		time.Sleep(100 * time.Millisecond)
	}
	screen := h.captureVisible()
	h.t.Fatalf("Codex never reached a stable ready screen: visible_bytes=%d states=%v",
		len(screen), screenStates(screen))
	return false
}

func projectTrustVisible(lower string) bool {
	return strings.Contains(lower, "project you created or one you trust") ||
		strings.Contains(lower, "trust the files in this folder") ||
		strings.Contains(lower, "trust this folder")
}

func (h *codexHarness) failOnProviderUnavailable(lower string) {
	h.t.Helper()
	for _, needle := range []string{
		"out of credits", "usage limit", "rate limit", "not logged in",
		"authentication required", "invalid model",
	} {
		if strings.Contains(lower, needle) {
			h.t.Fatalf("Codex prerequisite unavailable: state=%s", needle)
		}
	}
}

func (h *codexHarness) captureVisible() string {
	h.t.Helper()
	screen, err := h.client.CapturePane(context.Background(), h.paneID)
	if err != nil {
		h.t.Fatalf("capture visible test pane: %v", sanitizedErr(err))
	}
	return screen
}

func (h *codexHarness) sendKey(key string) {
	h.t.Helper()
	if err := h.client.SendKey(context.Background(), h.paneID, key); err != nil {
		h.t.Fatalf("send measured key %s: %v", key, sanitizedErr(err))
	}
}

func (h *codexHarness) submitPaste(body string) {
	h.t.Helper()
	if err := h.client.SendBracketedPaste(context.Background(), h.paneID, body); err != nil {
		h.t.Fatalf("send bracketed test input: %v", sanitizedErr(err))
	}
	h.sendKey("Enter")
}

func (h *codexHarness) eraseComposer(body string) {
	h.t.Helper()
	for range []rune(body) {
		h.sendKey("BSpace")
	}
}

func (h *codexHarness) waitReply(marker string, timeout time.Duration) string {
	h.t.Helper()
	return h.waitVisible(timeout, func(screen string) bool {
		return strings.Contains(screen, "• "+marker)
	})
}

func (h *codexHarness) waitIdle(timeout time.Duration) {
	h.t.Helper()
	stable := 0
	h.waitVisible(timeout, func(screen string) bool {
		lower := strings.ToLower(screen)
		idle := strings.Contains(screen, "›") &&
			!strings.Contains(lower, "working (") &&
			!strings.Contains(lower, "update model permissions") &&
			!strings.Contains(lower, "select model") &&
			!strings.Contains(lower, "choose model") &&
			!strings.Contains(lower, "hooks need review") &&
			!strings.Contains(lower, "skip until next version")
		if idle {
			stable++
		} else {
			stable = 0
		}
		// Codex can paint the composer before the modal's input handler has
		// finished yielding control. A half-second visual window was observed
		// to accept and then drop the next recovery submission after confirming
		// /permissions. Require two seconds of continuously idle presentation
		// before the harness sends the next measured input.
		return stable >= 20
	})
}

func (h *codexHarness) waitVisible(timeout time.Duration, predicate func(string) bool) string {
	h.t.Helper()
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		info, err := h.client.PaneInfoByID(context.Background(), h.paneID)
		if err != nil {
			h.t.Fatalf("observe exact test pane: %v", sanitizedErr(err))
		}
		if info.Dead {
			h.t.Fatalf("Codex exited during contract scenario: %s", exitLabel(info))
		}
		last = h.captureVisible()
		h.failOnProviderUnavailable(strings.ToLower(last))
		if predicate(last) {
			return last
		}
		time.Sleep(100 * time.Millisecond)
	}
	h.t.Fatalf("visible contract condition timed out: visible_bytes=%d states=%v",
		len(last), screenStates(last))
	return ""
}

// measureSubmitSequence uses a native slash command as a zero-model-latency
// witness. If bracketed paste submitted on its own, /permissions would open
// immediately; it remains composer text until the separate Enter arrives.
func (h *codexHarness) measureSubmitSequence() {
	h.t.Helper()
	h.waitIdle(10 * time.Second)
	if err := h.client.SendBracketedPaste(context.Background(), h.paneID, "/permissions"); err != nil {
		h.t.Fatalf("send submit-sequence witness: %v", sanitizedErr(err))
	}
	h.waitVisible(5*time.Second, func(screen string) bool {
		return strings.Contains(screen, "/permissions") &&
			!strings.Contains(screen, "Update Model Permissions")
	})
	time.Sleep(750 * time.Millisecond)
	if strings.Contains(h.captureVisible(), "Update Model Permissions") {
		h.t.Fatal("bracketed paste invoked a slash command before Enter")
	}
	h.sendKey("Enter")
	h.waitVisible(5*time.Second, func(screen string) bool {
		return strings.Contains(screen, "Update Model Permissions")
	})
	h.sendKey("Escape")
	h.waitIdle(10 * time.Second)
}

func (h *codexHarness) measureBracketedMultiline() {
	h.t.Helper()
	// A slash command in the first pasted line is a deterministic newline
	// witness: if the embedded newline submitted that line, the modal would
	// open before the explicitly measured Enter key.
	const probe = "/permissions\nNOISEDECK_BRACKET_NEWLINE_PROBE"
	if err := h.client.SendBracketedPaste(context.Background(), h.paneID, probe); err != nil {
		h.t.Fatalf("send bracketed newline witness: %v", sanitizedErr(err))
	}
	h.waitVisible(5*time.Second, func(screen string) bool {
		return strings.Contains(screen, "/permissions") &&
			strings.Contains(screen, "NOISEDECK_BRACKET_NEWLINE_PROBE") &&
			!strings.Contains(screen, "Update Model Permissions")
	})
	time.Sleep(750 * time.Millisecond)
	if strings.Contains(h.captureVisible(), "Update Model Permissions") {
		h.t.Fatal("embedded bracketed-paste newline submitted the first line")
	}
	h.eraseComposer(probe)
	h.waitVisible(5*time.Second, func(screen string) bool {
		return !strings.Contains(screen, "/permissions") &&
			!strings.Contains(screen, "NOISEDECK_BRACKET_NEWLINE_PROBE")
	})

	body := "NOISEDECK_BRACKET_LINE_ONE\nNOISEDECK_BRACKET_LINE_TWO\nReply only with " + bracketReply + "."
	if err := h.client.SendBracketedPaste(context.Background(), h.paneID, body); err != nil {
		h.t.Fatalf("send bracketed multiline body: %v", sanitizedErr(err))
	}
	h.waitVisible(5*time.Second, func(screen string) bool {
		return strings.Contains(screen, "NOISEDECK_BRACKET_LINE_ONE") &&
			strings.Contains(screen, "NOISEDECK_BRACKET_LINE_TWO") &&
			strings.Contains(screen, bracketReply)
	})
	time.Sleep(750 * time.Millisecond)
	if strings.Contains(h.captureVisible(), "• "+bracketReply) {
		h.t.Fatal("bracketed multiline body submitted before the separate Enter")
	}
	h.sendKey("Enter")
	h.waitReply(bracketReply, 90*time.Second)
	h.waitIdle(10 * time.Second)
}

func (h *codexHarness) measureLiteralInput() {
	h.t.Helper()
	body := "Reply only with " + literalReply + "."
	if err := h.client.SendLiteral(context.Background(), h.paneID, body); err != nil {
		h.t.Fatalf("send literal input: %v", sanitizedErr(err))
	}
	// Literal bytes have no paste-end marker to give the TUI a natural batch
	// boundary. Observe them in the composer before sending the separately
	// measured submit key; the manual probe exposed this ordering requirement.
	h.waitVisible(5*time.Second, func(screen string) bool {
		return strings.Contains(screen, literalReply) &&
			!strings.Contains(screen, "• "+literalReply)
	})
	h.sendKey("Enter")
	h.waitReply(literalReply, 90*time.Second)
	h.waitIdle(10 * time.Second)
}

func (h *codexHarness) measureLiteralMultiline() {
	h.t.Helper()
	const probe = "/permissions\nNOISEDECK_LITERAL_NEWLINE_PROBE"
	if err := h.client.SendLiteral(context.Background(), h.paneID, probe); err != nil {
		h.t.Fatalf("send literal newline witness: %v", sanitizedErr(err))
	}
	h.waitVisible(5*time.Second, func(screen string) bool {
		return strings.Contains(screen, "/permissions") &&
			strings.Contains(screen, "NOISEDECK_LITERAL_NEWLINE_PROBE") &&
			!strings.Contains(screen, "Update Model Permissions")
	})
	time.Sleep(750 * time.Millisecond)
	if strings.Contains(h.captureVisible(), "Update Model Permissions") {
		h.t.Fatal("embedded literal newline submitted the first line")
	}
	h.eraseComposer(probe)
	h.waitVisible(5*time.Second, func(screen string) bool {
		return !strings.Contains(screen, "/permissions") &&
			!strings.Contains(screen, "NOISEDECK_LITERAL_NEWLINE_PROBE")
	})

	body := "NOISEDECK_LITERAL_LINE_ONE\nNOISEDECK_LITERAL_LINE_TWO\nReply only with " + literalMultiReply + "."
	if err := h.client.SendLiteral(context.Background(), h.paneID, body); err != nil {
		h.t.Fatalf("send literal multiline body: %v", sanitizedErr(err))
	}
	h.waitVisible(5*time.Second, func(screen string) bool {
		return strings.Contains(screen, "NOISEDECK_LITERAL_LINE_ONE") &&
			strings.Contains(screen, "NOISEDECK_LITERAL_LINE_TWO") &&
			strings.Contains(screen, literalMultiReply)
	})
	time.Sleep(750 * time.Millisecond)
	if strings.Contains(h.captureVisible(), "• "+literalMultiReply) {
		h.t.Fatal("literal multiline body submitted before the separate Enter")
	}
	h.sendKey("Enter")
	h.waitReply(literalMultiReply, 90*time.Second)
	h.waitIdle(10 * time.Second)
}

func (h *codexHarness) measureActiveTurnQueue() {
	h.t.Helper()
	start := filepath.Join(h.dir, "active-start")
	end := filepath.Join(h.dir, "active-end")
	queuedCount := filepath.Join(h.dir, "active-queued-count")
	command := sentinelCommand(start, end, 8)
	h.cleanupCertifiable = false
	h.submitPaste("Run exactly this one shell command and wait for it to finish: " + command +
		". Do not modify repository files. After it finishes reply only NOISEDECK_ACTIVE_FIRST_DONE.")
	h.waitFile(start, true, 90*time.Second)
	h.trackSentinelProcess(start)
	h.cleanupCertifiable = true
	if fileExists(end) {
		h.t.Fatal("active-turn sentinel completed before queued-input observation")
	}
	h.submitPaste("After the current command finishes, run exactly this one shell command: " +
		appendCommand(queuedCount, "queued\n") + ". Then reply only with " + activeReply + ".")
	h.waitVisible(5*time.Second, func(screen string) bool {
		return strings.Contains(strings.ToLower(screen), "submitted after next tool call")
	})
	h.waitFile(end, true, 20*time.Second)
	h.waitFile(queuedCount, true, 90*time.Second)
	screen := h.waitReply(activeReply, 90*time.Second)
	if got := strings.Count(screen, "• "+activeReply); got != 1 {
		h.t.Fatalf("active-turn queued reply count=%d want=1", got)
	}
	count, err := os.ReadFile(queuedCount)
	if err != nil || string(count) != "queued\n" {
		h.t.Fatalf("active-turn execution count mismatch: readable=%t exactly_once=%t",
			err == nil, err == nil && string(count) == "queued\n")
	}
	h.waitIdle(10 * time.Second)
	h.submitPaste("Reply only with " + activeBarrier + ".")
	h.waitReply(activeBarrier, 90*time.Second)
	h.waitIdle(10 * time.Second)
	time.Sleep(time.Second)
	count, err = os.ReadFile(queuedCount)
	if err != nil || string(count) != "queued\n" {
		h.t.Fatalf("active-turn post-barrier count mismatch: readable=%t exactly_once=%t",
			err == nil, err == nil && string(count) == "queued\n")
	}
}

func (h *codexHarness) measureModalDiscard() {
	h.t.Helper()
	h.waitIdle(10 * time.Second)
	h.t.Log("codex_contract_phase=modal_discard_open_unsubmitted")
	h.submitPaste("/permissions")
	h.waitVisible(10*time.Second, func(screen string) bool {
		return strings.Contains(screen, "Update Model Permissions") &&
			strings.Contains(screen, "Full Access")
	})
	const unexpected = "NOISEDECK_MODAL_UNEXPECTED_282842"
	sentinel := filepath.Join(h.dir, "modal-unexpected")
	body := "Run exactly this one shell command: " + appendCommand(sentinel, "executed\n") +
		". Then reply only with " + unexpected + "."
	if err := h.client.SendBracketedPaste(context.Background(), h.paneID, body); err != nil {
		h.t.Fatalf("send modal discard marker: %v", sanitizedErr(err))
	}
	time.Sleep(500 * time.Millisecond)
	if strings.Contains(h.captureVisible(), unexpected) {
		h.t.Fatal("modal displayed input that should be discarded")
	}
	h.sendKey("Escape")
	h.waitIdle(10 * time.Second)
	h.t.Log("codex_contract_phase=modal_discard_recovery_unsubmitted")
	h.submitPaste("Reply only with " + modalReply + ".")
	screen := h.waitReply(modalReply, 90*time.Second)
	h.waitIdle(10 * time.Second)
	time.Sleep(time.Second)
	if fileExists(sentinel) || strings.Contains(screen, "• "+unexpected) ||
		strings.Contains(h.captureVisible(), unexpected) {
		h.t.Fatal("modal input survived into the composer or queue")
	}

	// Exercise the complete production paste+submit sequence while the modal
	// owns the terminal. Enter confirms the current permissions choice; the
	// pasted /model command must not survive and open a second picker later.
	h.t.Log("codex_contract_phase=modal_discard_open_submitted")
	h.submitPaste("/permissions")
	h.waitVisible(10*time.Second, func(screen string) bool {
		return strings.Contains(screen, "Update Model Permissions")
	})
	h.t.Log("codex_contract_phase=modal_discard_open_submitted_confirmed")
	if err := h.client.SendBracketedPaste(context.Background(), h.paneID, "/model"); err != nil {
		h.t.Fatalf("send full modal injection witness: %v", sanitizedErr(err))
	}
	h.sendKey("Enter")
	h.t.Log("codex_contract_phase=modal_discard_submit_sent")
	h.waitIdle(10 * time.Second)
	if strings.Contains(h.captureVisible(), "› /model") {
		h.t.Fatal("full modal paste+submit left the pasted command visible")
	}
	// Enter confirms the modal choice, but 0.145.0 was observed twice to
	// repaint an apparently idle composer that discarded the next paste. An
	// explicit Escape returns the native TUI to an input-accepting composer;
	// record that recovery requirement rather than treating presentation as
	// proof that the input handler has yielded.
	h.t.Log("codex_contract_phase=modal_discard_escape_recovery")
	h.sendKey("Escape")
	h.waitIdle(10 * time.Second)
	h.t.Log("codex_contract_phase=modal_discard_recovery_submitted")
	h.submitPaste("Reply only with " + modalReplySubmit + ".")
	h.waitReply(modalReplySubmit, 90*time.Second)
	h.waitIdle(10 * time.Second)
}

func (h *codexHarness) measureEscapeInterrupt() {
	h.t.Helper()
	start := filepath.Join(h.dir, "interrupt-start")
	end := filepath.Join(h.dir, "interrupt-end")
	command := sentinelCommand(start, end, 10)
	h.cleanupCertifiable = false
	h.submitPaste("Run exactly this one shell command and wait for it to finish: " + command +
		". Do not modify repository files. Then reply only NOISEDECK_INTERRUPT_UNEXPECTED.")
	h.waitFile(start, true, 90*time.Second)
	h.trackSentinelProcess(start)
	h.cleanupCertifiable = true
	before := h.requireLivePane()
	h.sendKey("Escape")
	h.waitVisible(10*time.Second, func(screen string) bool {
		return strings.Contains(screen, "Conversation interrupted")
	})
	after := h.requireLivePane()
	if before.PID != after.PID || before.StartTicks != after.StartTicks {
		h.t.Fatal("Escape replaced or killed the Codex pane process")
	}
	h.submitPaste("Reply only with " + interruptReply + ".")
	h.waitReply(interruptReply, 90*time.Second)
	// Codex cancels the conversation turn, not its independently managed
	// background terminal. The command therefore reaches its end sentinel.
	h.waitFile(end, true, 20*time.Second)
	h.waitIdle(10 * time.Second)
	h.submitPaste("Reply only with " + interruptBarrier + ".")
	h.waitReply(interruptBarrier, 90*time.Second)
	h.waitIdle(10 * time.Second)
	replyVisible := strings.Contains(h.captureVisible(), "• NOISEDECK_INTERRUPT_UNEXPECTED")
	if replyVisible {
		h.t.Fatal("Escape-interrupted turn replied after its running tool exited")
	}
}

func TestSentinelCommandPublishesPrivateIdentity(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is required to exercise the test-owned sentinel writer")
	}
	dir := t.TempDir()
	start := filepath.Join(dir, "start")
	end := filepath.Join(dir, "end")
	if out, err := exec.Command("sh", "-c", sentinelCommand(start, end, 0)).CombinedOutput(); err != nil {
		t.Fatalf("run test-owned sentinel: %v output_bytes=%d", err, len(out))
	}
	path := start + ".identity.json"
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("sentinel identity mode = %v, want regular 0600", info.Mode())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var identity processIdentity
	bootID, ok := currentBootID()
	if err := json.Unmarshal(data, &identity); err != nil || identity.PID <= 1 ||
		identity.StartTicks <= 0 || !ok || identity.BootID != bootID {
		t.Fatal("sentinel published an invalid private process identity")
	}
}

func sentinelCommand(start, end string, seconds int) string {
	identity := start + ".identity.json"
	code := fmt.Sprintf(
		"from pathlib import Path; import json,os,time; pid=os.getpid(); "+
			"boot_id=Path('/proc/sys/kernel/random/boot_id').read_text().strip(); "+
			"raw=Path('/proc/'+str(pid)+'/stat').read_text(); "+
			"ticks=int(raw[raw.rfind(')')+2:].split()[19]); "+
			"identity=Path(%s); staging=Path(str(identity)+'.new'); "+
			"staging.write_text(json.dumps({'pid':pid,'start_ticks':ticks,'boot_id':boot_id})); "+
			"os.chmod(staging,0o600); "+
			"os.replace(staging,identity); "+
			"Path(%s).write_text('1'); time.sleep(%d); Path(%s).write_text('1')",
		pythonQuote(identity), pythonQuote(start), seconds, pythonQuote(end))
	return "python3 -c " + shellQuote(code)
}

func appendCommand(path, value string) string {
	code := fmt.Sprintf("from pathlib import Path; p=Path(%s); p.open('a').write(%s)",
		pythonQuote(path), pythonQuote(value))
	return "python3 -c " + shellQuote(code)
}

func pythonQuote(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `'`, `\'`)
	value = strings.ReplaceAll(value, "\r", `\r`)
	value = strings.ReplaceAll(value, "\n", `\n`)
	return "'" + value + "'"
}

func (h *codexHarness) waitFile(path string, want bool, timeout time.Duration) {
	h.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fileExists(path) == want {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	h.t.Fatalf("test-owned sentinel condition timed out: want_exists=%t", want)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func (h *codexHarness) waitHookEvents(count int, timeout time.Duration) []hookEvent {
	h.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		events := h.readHookEvents()
		if len(events) >= count {
			if len(events) != count {
				h.t.Fatalf("hook event count=%d want=%d", len(events), count)
			}
			return events
		}
		time.Sleep(100 * time.Millisecond)
	}
	h.t.Fatalf("structured SessionStart hook timed out: event_count=%d want=%d",
		len(h.readHookEvents()), count)
	return nil
}

func (h *codexHarness) assertNoHookEvents(duration time.Duration) {
	h.t.Helper()
	deadline := time.Now().Add(duration)
	for time.Now().Before(deadline) {
		if got := len(h.readHookEvents()); got != 0 {
			h.t.Fatalf("SessionStart fired before first submitted input: event_count=%d", got)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (h *codexHarness) serveHookEvents() {
	for {
		conn, err := h.hookListener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			h.setHookError(errors.New("hook socket accept failed"))
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		var event hookEvent
		decoder := json.NewDecoder(io.LimitReader(conn, 4097))
		err = decoder.Decode(&event)
		_ = conn.Close()
		if err != nil {
			h.setHookError(errors.New("hook socket received invalid JSON"))
			continue
		}
		h.hookMu.Lock()
		h.hookEvents = append(h.hookEvents, event)
		h.hookMu.Unlock()
	}
}

func (h *codexHarness) setHookError(err error) {
	h.hookMu.Lock()
	if h.hookErr == nil {
		h.hookErr = err
	}
	h.hookMu.Unlock()
}

func (h *codexHarness) readHookEvents() []hookEvent {
	h.t.Helper()
	h.hookMu.Lock()
	defer h.hookMu.Unlock()
	if h.hookErr != nil {
		h.t.Fatalf("test-owned hook sink failed: %v", h.hookErr)
	}
	return append([]hookEvent(nil), h.hookEvents...)
}

func requireStartupEvent(t *testing.T, event hookEvent, project string) {
	t.Helper()
	if !canonicalUUID.MatchString(event.SessionID) || event.CWD != project || event.Source != "startup" {
		t.Fatalf("startup hook mismatch: uuid=%t cwd=%t source_startup=%t",
			canonicalUUID.MatchString(event.SessionID), event.CWD == project, event.Source == "startup")
	}
}

func requireResumeEvents(t *testing.T, events []hookEvent, project string) {
	t.Helper()
	if len(events) != 2 {
		t.Fatalf("resume hook event count=%d want=2", len(events))
	}
	if !canonicalUUID.MatchString(events[1].SessionID) || events[1].CWD != project ||
		events[1].Source != "resume" || events[1].SessionID != events[0].SessionID {
		t.Fatalf("resume hook mismatch: uuid=%t cwd=%t source_resume=%t same_id=%t",
			canonicalUUID.MatchString(events[1].SessionID), events[1].CWD == project,
			events[1].Source == "resume", events[1].SessionID == events[0].SessionID)
	}
}

func (h *codexHarness) requireLivePane() *tmux.PaneInfo {
	h.t.Helper()
	info, err := h.client.PaneInfoByID(context.Background(), h.paneID)
	if err != nil {
		h.t.Fatalf("read exact live pane identity: %v", sanitizedErr(err))
	}
	if info.Dead || info.PID <= 1 || info.StartTicks <= 0 {
		h.t.Fatalf("exact pane identity incomplete: live=%t pid_valid=%t start_ticks_valid=%t",
			!info.Dead, info.PID > 1, info.StartTicks > 0)
	}
	return info
}

func (h *codexHarness) waitDead(paneID string, timeout time.Duration) *tmux.PaneInfo {
	h.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		info, err := h.client.PaneInfoByID(context.Background(), paneID)
		if err != nil {
			h.t.Fatalf("read preserved pane exit evidence: %v", sanitizedErr(err))
		}
		if info.Dead {
			return info
		}
		time.Sleep(100 * time.Millisecond)
	}
	screen := h.captureVisible()
	h.t.Fatalf("pane did not preserve bounded exit evidence: visible_bytes=%d states=%v",
		len(screen), screenStates(screen))
	return nil
}

func requireExitShape(t *testing.T, label string, info *tmux.PaneInfo, status int, signal *int) {
	t.Helper()
	statusMatches := info.ExitStatus != nil && *info.ExitStatus == status
	signalMatches := (signal == nil && info.ExitSignal == nil) ||
		(signal != nil && info.ExitSignal != nil && *signal == *info.ExitSignal)
	if !info.Dead || !statusMatches || !signalMatches {
		t.Fatalf("%s exit mismatch: dead=%t status_matches=%t signal_matches=%t shape=%s",
			label, info.Dead, statusMatches, signalMatches, exitLabel(info))
	}
}

func exitLabel(info *tmux.PaneInfo) string {
	status := "empty"
	if info.ExitStatus != nil {
		status = "present:" + strconv.Itoa(*info.ExitStatus)
	}
	signal := "empty"
	if info.ExitSignal != nil {
		signal = "present:" + strconv.Itoa(*info.ExitSignal)
	}
	return "dead=" + strconv.FormatBool(info.Dead) + ",status=" + status + ",signal=" + signal
}

func (h *codexHarness) track(info *tmux.PaneInfo) {
	if info == nil || info.Dead || info.PID <= 1 || info.StartTicks <= 0 {
		return
	}
	h.recordIdentity(h.paneIdentity(info))
}

func (h *codexHarness) paneIdentity(info *tmux.PaneInfo) processIdentity {
	return processIdentity{PID: info.PID, StartTicks: info.StartTicks, BootID: h.bootID}
}

func (h *codexHarness) trackSentinelProcess(path string) {
	h.t.Helper()
	raw, err := os.ReadFile(path + ".identity.json")
	if err != nil {
		h.t.Fatalf("read test-owned sentinel process identity: %v", err)
	}
	var identity processIdentity
	if err := json.Unmarshal(raw, &identity); err != nil || identity.PID <= 1 ||
		identity.StartTicks <= 0 || identity.BootID != h.bootID {
		h.t.Fatal("test-owned sentinel reported an invalid process identity")
	}
	if !processHasIdentity(identity) {
		h.t.Fatal("test-owned sentinel process identity disappeared before recording")
	}
	h.recordIdentity(identity)
}

func (h *codexHarness) recordIdentity(identity processIdentity) {
	if identity.BootID == "" || identity.BootID != h.bootID {
		h.t.Fatal("refusing to persist a process identity from another boot")
	}
	for _, existing := range h.tracked {
		if existing == identity {
			return
		}
	}
	h.tracked = append(h.tracked, identity)
	data, err := json.Marshal(h.tracked)
	if err != nil {
		h.t.Fatalf("marshal sanitized process manifest: %v", err)
	}
	staging := h.manifest + ".new"
	if err := os.WriteFile(staging, data, 0o600); err != nil {
		h.t.Fatalf("write sanitized process manifest: %v", err)
	}
	if err := os.Rename(staging, h.manifest); err != nil {
		h.t.Fatalf("publish sanitized process manifest: %v", err)
	}
}

func (h *codexHarness) cleanup() {
	cleanupOK := h.cleanupCertifiable
	if !cleanupOK {
		h.t.Errorf("cleanup cannot certify an unpublished process-identity window; preserving evidence")
	}
	if h.client != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		panes, err := h.client.ListPanes(ctx)
		cancel()
		if err == nil {
			for _, pane := range panes {
				if pane.Dead {
					continue
				}
				info, readErr := h.client.PaneInfoByID(context.Background(), pane.PaneID)
				if readErr != nil {
					cleanupOK = false
					h.t.Errorf("read private pane identity during cleanup: %v", sanitizedErr(readErr))
					continue
				}
				if info.Dead {
					continue
				}
				if !processHasIdentity(h.paneIdentity(info)) || info.ProcessGroupID <= 1 ||
					info.ProcessGroupID != info.PID {
					cleanupOK = false
					h.t.Errorf("cleanup refused unvalidated process group: private_group=%t",
						processHasIdentity(h.paneIdentity(info)) && info.ProcessGroupID > 1 &&
							info.ProcessGroupID == info.PID)
					continue
				}
				if err := syscall.Kill(-info.ProcessGroupID, syscall.SIGTERM); err != nil &&
					!errors.Is(err, syscall.ESRCH) {
					cleanupOK = false
					h.t.Errorf("signal private process group during cleanup")
				}
				deadline := time.Now().Add(2 * time.Second)
				identity := h.paneIdentity(info)
				for processHasIdentity(identity) && time.Now().Before(deadline) {
					time.Sleep(50 * time.Millisecond)
				}
				if processHasIdentity(identity) {
					if err := syscall.Kill(-info.ProcessGroupID, syscall.SIGKILL); err != nil &&
						!errors.Is(err, syscall.ESRCH) {
						cleanupOK = false
						h.t.Errorf("force private process group during cleanup")
					}
					deadline = time.Now().Add(time.Second)
					for processHasIdentity(identity) && time.Now().Before(deadline) {
						time.Sleep(25 * time.Millisecond)
					}
				}
				if processHasIdentity(identity) {
					cleanupOK = false
					h.t.Errorf("private pane process survived cleanup")
				}
			}
		} else if errors.Is(err, tmux.ErrNoServer) {
			cleanupOK = false
			h.t.Errorf("private tmux server disappeared before cleanup inventory")
		} else if !errors.Is(err, tmux.ErrNoSession) {
			cleanupOK = false
			h.t.Errorf("inventory private tmux namespace during cleanup: %v", sanitizedErr(err))
		}
		ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
		if err := h.client.KillServer(ctx); err != nil && !errors.Is(err, tmux.ErrNoServer) {
			cleanupOK = false
			h.t.Errorf("remove private tmux server: %v", sanitizedErr(err))
		}
		cancel()
	}
	if h.hookListener != nil {
		_ = h.hookListener.Close()
	}
	if h.authFile != nil {
		_ = h.authFile.Close()
	}

	for _, identity := range h.tracked {
		terminateExactIdentity(identity, 2*time.Second)
		if processHasIdentity(identity) {
			cleanupOK = false
			h.t.Errorf("recorded provider process identity survived cleanup: pid=%d", identity.PID)
		}
	}
	if err := removeEphemeralHome(h.home, h.dir); err != nil {
		cleanupOK = false
		h.t.Errorf("remove in-memory provider home: %v", err)
	}

	base := filepath.Join(h.root, ".review", "ps")
	rel, err := filepath.Rel(base, h.dir)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) ||
		filepath.IsAbs(rel) {
		h.t.Errorf("refusing unsafe provider-spike scratch cleanup")
		return
	}
	if err := validatePrivateDir(base); err != nil {
		h.t.Errorf("refusing changed provider-spike scratch root: %v", err)
		return
	}
	if err := validatePrivateDir(h.dir); err != nil {
		h.t.Errorf("refusing changed provider-spike run directory: %v", err)
		return
	}
	if !cleanupOK {
		h.t.Errorf("private run cleanup was incomplete; evidence retained")
		return
	}
	certification, err := json.Marshal(runMetadata{BootID: h.bootID, EphemeralHome: h.home})
	if err != nil {
		h.t.Errorf("marshal cleanup certification: %v", err)
		return
	}
	certificationPath := filepath.Join(h.dir, "cleanup-certified.json")
	if err := os.WriteFile(certificationPath+".new", certification, 0o600); err != nil {
		h.t.Errorf("write cleanup certification: %v", err)
		return
	}
	if err := os.Rename(certificationPath+".new", certificationPath); err != nil {
		h.t.Errorf("publish cleanup certification: %v", err)
		return
	}
	if err := os.RemoveAll(h.dir); err != nil {
		h.t.Errorf("remove provider-spike scratch directory: %v", err)
	}
}

func processHasIdentity(want processIdentity) bool {
	if want.PID <= 1 || want.StartTicks <= 0 || want.BootID == "" {
		return false
	}
	identity, ok := readProcessIdentity(want.PID)
	return ok && identity.StartTicks == want.StartTicks && identity.BootID == want.BootID
}

func readProcessIdentity(pid int) (processIdentity, bool) {
	bootID, ok := currentBootID()
	if !ok {
		return processIdentity{}, false
	}
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return processIdentity{}, false
	}
	right := strings.LastIndexByte(string(data), ')')
	if right < 0 {
		return processIdentity{}, false
	}
	fields := strings.Fields(string(data)[right+1:])
	// Fields starts at stat field 3 (state), so field 22 is index 19.
	if len(fields) <= 19 {
		return processIdentity{}, false
	}
	ticks, err := strconv.ParseInt(fields[19], 10, 64)
	if err != nil {
		return processIdentity{}, false
	}
	return processIdentity{PID: pid, StartTicks: ticks, BootID: bootID}, true
}

func terminateExactIdentity(identity processIdentity, timeout time.Duration) {
	if identity.PID <= 1 || !processHasIdentity(identity) {
		return
	}
	_ = syscall.Kill(identity.PID, syscall.SIGTERM)
	deadline := time.Now().Add(timeout)
	for processHasIdentity(identity) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if processHasIdentity(identity) {
		_ = syscall.Kill(identity.PID, syscall.SIGKILL)
		deadline = time.Now().Add(time.Second)
		for processHasIdentity(identity) && time.Now().Before(deadline) {
			time.Sleep(25 * time.Millisecond)
		}
	}
}

func screenStates(screen string) []string {
	lower := strings.ToLower(screen)
	checks := []struct{ label, needle string }{
		{"composer", "›"},
		{"hook-trust", "hooks need review"},
		{"project-trust", "trust"},
		{"update", "update available"},
		{"ready", "openai codex"},
		{"working", "working"},
		{"queued", "submitted after next tool call"},
		{"permissions", "update model permissions"},
		{"interrupted", "conversation interrupted"},
		{"usage", "usage"},
		{"auth", "authenticat"},
		{"login", "login"},
		{"error", "error"},
	}
	var labels []string
	for _, check := range checks {
		if strings.Contains(lower, check.needle) {
			labels = append(labels, check.label)
		}
	}
	return labels
}

func sanitizedErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, tmux.ErrNoServer):
		return errors.New("private tmux server unavailable")
	case errors.Is(err, tmux.ErrNoSession):
		return errors.New("private tmux pane unavailable")
	default:
		return errors.New("operation failed; provider output suppressed")
	}
}
