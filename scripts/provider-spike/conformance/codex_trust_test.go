//go:build linux

package conformance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/noisefloor-oss/deck/internal/tmux"
)

// This probe measures how Codex 0.145.0 persists native SessionStart hook
// trust, and whether provisioning can establish that trust headlessly. It is
// fixture measurement only: Deck runtime must never answer provider
// modals, so the answer here constrains the provisioning design instead.
//
// Three phases, all in test-owned tmpfs homes with the standard fixture:
//
//  1. Answer the native hook-trust modal once and diff the Codex home before
//     and after, identifying the persisted trust state by relative path.
//  2. Relaunch a fresh process in the same home with modals forbidden,
//     proving the observed write is the persisted trust decision.
//  3. Build a second, independent home, seed only the observed trust state
//     plus identical hook command bytes, and launch with modals forbidden:
//     ready means headless provisioning by file seeding is sufficient; the
//     hook-trust modal means it is not.
//
// Only structural facts (relative paths, kinds, sizes, content digests) are
// reported. Provider-owned file contents are never logged or persisted.

type homeEntry struct {
	kind   string
	size   int64
	digest string
}

func snapshotHome(t *testing.T, home string) map[string]homeEntry {
	t.Helper()
	entries := map[string]homeEntry{}
	root := filepath.Clean(home)
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == "." {
			return err
		}
		switch {
		case d.Type()&os.ModeSymlink != 0:
			entries[rel] = homeEntry{kind: "symlink"}
		case d.IsDir():
			entries[rel] = homeEntry{kind: "dir"}
		case d.Type().IsRegular():
			digest, size, err := hashHomeFile(path)
			if err != nil {
				return err
			}
			entries[rel] = homeEntry{kind: "file", size: size, digest: digest}
		default:
			entries[rel] = homeEntry{kind: "other"}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot test-owned Codex home: %v", err)
	}
	return entries
}

func hashHomeFile(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

func diffHomeSnapshots(
	before, after map[string]homeEntry,
) (added []string, changed []string) {
	for rel, afterEntry := range after {
		beforeEntry, ok := before[rel]
		if !ok {
			added = append(added, rel)
			continue
		}
		if beforeEntry != afterEntry {
			changed = append(changed, rel)
		}
	}
	sort.Strings(added)
	sort.Strings(changed)
	return added, changed
}

func seedHomeFile(t *testing.T, srcHome, dstHome, rel string) {
	t.Helper()
	src := filepath.Join(srcHome, rel)
	dst := filepath.Join(dstHome, rel)
	info, err := os.Stat(src)
	if err != nil {
		t.Fatalf("stat observed trust state: %v", err)
	}
	if info.IsDir() {
		if err := os.MkdirAll(dst, 0o700); err != nil {
			t.Fatalf("create seeded trust directory: %v", err)
		}
		return
	}
	if !info.Mode().IsRegular() {
		return
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		t.Fatalf("create seeded trust parent: %v", err)
	}
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read observed trust state: %v", err)
	}
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		t.Fatalf("write seeded trust state: %v", err)
	}
}

// addedConfigLines returns the lines Codex appended to the fixture config.toml
// when the trust modal was answered. Those lines are provider-generated
// persisted state: they are only ever inspected structurally (shape and
// counts), never logged.
func addedConfigLines(before, after string) []string {
	present := map[string]bool{}
	for _, line := range strings.Split(before, "\n") {
		present[line] = true
	}
	var added []string
	for _, line := range strings.Split(after, "\n") {
		if line != "" && !present[line] {
			added = append(added, line)
		}
	}
	return added
}

// requireTrustBlock asserts the exact measured shape of the state Codex
// writes when the trust modal is answered: one [hooks.state] header, one
// table keyed by this home's absolute hooks.json path with session_start and
// numeric group/hook indices, and one sha256 trusted_hash line — and nothing
// else. It returns the recorded hash for preimage checks without ever
// logging provider-owned lines.
func requireTrustBlock(t *testing.T, home string, added []string) string {
	t.Helper()
	hookPath := filepath.Join(home, "hooks.json")
	tableHeader := regexp.MustCompile(
		`^\[hooks\.state\."` + regexp.QuoteMeta(hookPath) +
			`:session_start:\d+:\d+"\]$`,
	)
	hashLine := regexp.MustCompile(`^trusted_hash = "sha256:([0-9a-f]{64})"$`)
	stateHeaders, tableHeaders, hashLines := 0, 0, 0
	observed := ""
	for _, line := range added {
		switch {
		case line == "[hooks.state]":
			stateHeaders++
		case tableHeader.MatchString(line):
			tableHeaders++
		default:
			if match := hashLine.FindStringSubmatch(line); match != nil {
				hashLines++
				observed = match[1]
			} else {
				t.Fatalf("trust answer wrote %d config lines, including an unexpected shape", len(added))
			}
		}
	}
	if stateHeaders != 1 || tableHeaders != 1 || hashLines != 1 {
		t.Fatalf(
			"trust block shape drifted: state_headers=%d table_headers=%d hash_lines=%d",
			stateHeaders, tableHeaders, hashLines,
		)
	}
	return observed
}

// trustedHashPreimage identifies what the measured trusted_hash covers by
// testing candidate preimages derived from the fixture's own hooks.json.
// Knowing the algorithm lets provisioning seed trust headlessly for hook
// content it authors itself.
func trustedHashPreimage(t *testing.T, observed string, hookBytes []byte) string {
	t.Helper()
	var parsed struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(hookBytes, &parsed); err != nil {
		t.Fatalf("parse fixture hook config: %v", err)
	}
	if len(parsed.Hooks["SessionStart"]) != 1 ||
		len(parsed.Hooks["SessionStart"][0].Hooks) != 1 {
		t.Fatal("fixture hook config shape changed; extend the preimage candidates")
	}
	entry := parsed.Hooks["SessionStart"][0].Hooks[0]

	candidates := map[string][]byte{
		"command_string": []byte(entry.Command),
		"type_command":   []byte(entry.Type + entry.Command),
		"command_type":   []byte(entry.Command + entry.Type),
	}
	if entryJSON, err := json.Marshal(entry); err == nil {
		candidates["hook_entry_json"] = entryJSON
	}
	if groupJSON, err := json.Marshal(parsed.Hooks["SessionStart"][0]); err == nil {
		candidates["hook_group_json"] = groupJSON
	}
	if eventJSON, err := json.Marshal(parsed.Hooks["SessionStart"]); err == nil {
		candidates["event_list_json"] = eventJSON
	}
	if hooksJSON, err := json.Marshal(parsed.Hooks); err == nil {
		candidates["hooks_map_json"] = hooksJSON
	}
	matched := []string{}
	for name, preimage := range candidates {
		sum := sha256.Sum256(preimage)
		if hex.EncodeToString(sum[:]) == observed {
			matched = append(matched, name)
		}
	}
	if len(matched) == 0 {
		return "unmatched"
	}
	sort.Strings(matched)
	return strings.Join(matched, ",")
}

// waitReadyOutcome mirrors waitReady but never answers a modal and reports
// the hook-trust modal as an outcome instead of failing, so the probe can
// record the headless verdict either way.
func (h *codexHarness) waitReadyOutcome(timeout time.Duration) string {
	h.t.Helper()
	deadline := time.Now().Add(timeout)
	stable := 0
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
			return "hook_trust_modal"
		case projectTrustVisible(lower):
			h.t.Fatal("trust probe reached an unprovisioned project-trust modal")
		case strings.Contains(lower, "skip until next version"):
			h.t.Fatal("trust probe reached an unhandled native update picker")
		case strings.Contains(lower, codexModel) &&
			!strings.Contains(lower, "hooks need review"):
			stable++
			if stable >= 5 {
				return "ready"
			}
		default:
			stable = 0
		}
		time.Sleep(100 * time.Millisecond)
	}
	h.t.Fatalf("Codex never reached a stable ready screen: visible_bytes=%d",
		len(h.captureVisible()))
	return ""
}

func (h *codexHarness) launchTrustProbe(name, marker string) {
	h.t.Helper()
	h.cleanupCertifiable = false
	launch, err := h.client.CreateSession(context.Background(), tmux.SessionSpec{
		Name:       name,
		WorkDir:    h.project,
		Argv:       []string{codexExecutable},
		Env:        []string{"CODEX_HOME=" + h.home},
		Generation: 1,
		Marker:     marker,
	})
	if err != nil {
		h.t.Fatalf("launch isolated Codex trust probe: %v", sanitizedErr(err))
	}
	h.paneID = launch.PaneID
	h.track(launch)
	h.cleanupCertifiable = true
}

func (h *codexHarness) stopTrustProbe(name string) {
	h.t.Helper()
	info, err := h.client.PaneInfoByID(context.Background(), h.paneID)
	if err != nil {
		h.t.Fatalf("observe trust probe before stop: %v", sanitizedErr(err))
	}
	if info.ProcessGroupID <= 1 || info.ProcessGroupID != info.PID {
		h.t.Fatal("trust probe process group identity changed before bounded stop")
	}
	if err := syscall.Kill(-info.ProcessGroupID, syscall.SIGTERM); err != nil {
		h.t.Fatalf("stop isolated trust probe: %v", sanitizedErr(err))
	}
	exit := h.waitDead(h.paneID, 15*time.Second)
	requireExitShape(h.t, name+" SIGTERM", exit, 0, nil)
}

func TestCodexHookTrustPersistence(t *testing.T) {
	if os.Getenv(realCodexEnv) != "1" {
		t.Skip("real Codex trust probe is opt-in; run make provider-spike-codex")
	}

	// Phase 1: identify the trust write.
	h1 := newCodexHarness(t)
	before := snapshotHome(t, h1.home)
	configBefore, err := os.ReadFile(filepath.Join(h1.home, "config.toml"))
	if err != nil {
		t.Fatalf("read fixture config before provisioning: %v", err)
	}
	if !h1.provisionNativeTrust() {
		t.Fatal("provisioning reached ready without answering the hook-trust modal; the measured write cannot be attributed to the modal answer")
	}
	after := snapshotHome(t, h1.home)
	configAfter, err := os.ReadFile(filepath.Join(h1.home, "config.toml"))
	if err != nil {
		t.Fatalf("read fixture config after provisioning: %v", err)
	}
	configAdded := addedConfigLines(string(configBefore), string(configAfter))
	observedHash := requireTrustBlock(t, h1.home, configAdded)
	t.Logf("trusted_hash=sha256:%s", observedHash)
	// The block must be an append onto the byte-unchanged fixture config:
	// the pre-answer bytes are an exact prefix, and the remainder is only
	// blank lines plus exactly the block lines, in order.
	if !strings.HasPrefix(string(configAfter), string(configBefore)) {
		t.Fatal("codex changed fixture config bytes before the trust block")
	}
	remainder := []string{}
	for _, line := range strings.Split(string(configAfter)[len(configBefore):], "\n") {
		if strings.TrimSpace(line) != "" {
			remainder = append(remainder, line)
		}
	}
	if strings.Join(remainder, "\n") != strings.Join(configAdded, "\n") {
		t.Fatal("content beyond blank lines and the trust block follows the config prefix")
	}
	hookBytes, err := os.ReadFile(filepath.Join(h1.home, "hooks.json"))
	if err != nil {
		t.Fatalf("read provisioned hook config: %v", err)
	}
	fileHash := sha256.Sum256(hookBytes)
	if observedHash == hex.EncodeToString(fileHash[:]) {
		t.Fatal("trusted_hash equals the hooks.json file sha256; contract record says it does not")
	}
	if preimage := trustedHashPreimage(t, observedHash, hookBytes); preimage != "unmatched" {
		t.Fatalf("trusted_hash preimage measured as %s; contract record says unmeasured", preimage)
	}
	added, changed := diffHomeSnapshots(before, after)
	for _, rel := range added {
		entry := after[rel]
		t.Logf("trust_write=added rel=%s kind=%s bytes=%d sha256=%s",
			rel, entry.kind, entry.size, entry.digest)
	}
	for _, rel := range changed {
		entry := after[rel]
		t.Logf("trust_write=changed rel=%s kind=%s bytes=%d sha256=%s",
			rel, entry.kind, entry.size, entry.digest)
	}
	if len(added) == 0 && len(changed) == 0 {
		t.Fatal("hook-trust answer left no observable state in the Codex home")
	}

	// Phase 2: the observed write must be the persisted trust decision.
	h1.launchTrustProbe("codex-trust-persist", "codex-trust-persist-g1")
	if outcome := h1.waitReadyOutcome(30 * time.Second); outcome != "ready" {
		t.Fatalf("same-home relaunch outcome=%s, want ready", outcome)
	}
	t.Log("same_home_relaunch_outcome=ready")
	h1.stopTrustProbe("codex-trust-persist")

	// Phase 3: headless provisioning verdicts. Each independent home gets
	// identical hook command bytes (provisioning controls the hook script
	// path) plus one candidate trust store, then launches with modals
	// forbidden. ready means that candidate alone carries the trust decision.
	probe := func(name string, seed func(t *testing.T, home string)) string {
		t.Helper()
		h := newCodexHarness(t)
		if err := os.WriteFile(filepath.Join(h.home, "hooks.json"), hookBytes, 0o600); err != nil {
			t.Fatalf("align headless home hook config: %v", err)
		}
		seed(t, h.home)
		h.launchTrustProbe(name, name+"-g1")
		outcome := h.waitReadyOutcome(30 * time.Second)
		t.Logf("headless_probe=%s outcome=%s", name, outcome)
		h.stopTrustProbe(name)
		return outcome
	}

	configOutcome := probe("codex-trust-seed-config", func(t *testing.T, home string) {
		t.Helper()
		configPath := filepath.Join(home, "config.toml")
		base, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("read headless fixture config: %v", err)
		}
		// The measured trust key embeds the absolute hooks.json path, so the
		// seeded lines must be rebound to this home's path — exactly what
		// provisioning does when it installs hooks.json at a fixed location.
		rebound := make([]string, 0, len(configAdded))
		for _, line := range configAdded {
			rebound = append(rebound, strings.ReplaceAll(line,
				filepath.Join(h1.home, "hooks.json"),
				filepath.Join(home, "hooks.json")))
		}
		merged := string(base) + strings.Join(rebound, "\n") + "\n"
		if err := os.WriteFile(configPath, []byte(merged), 0o600); err != nil {
			t.Fatalf("seed headless config trust lines: %v", err)
		}
	})
	stateOutcome := probe("codex-trust-seed-state", func(t *testing.T, home string) {
		t.Helper()
		// The negative control must be non-vacuous: prove the control home
		// actually received every SQLite database observed at trust time
		// before its modal outcome can mean "no SQLite DB carries trust".
		stateRels := []string{}
		for _, rel := range append(append([]string{}, added...), changed...) {
			if strings.Contains(rel, ".sqlite") {
				stateRels = append(stateRels, rel)
			}
		}
		if len(stateRels) == 0 {
			t.Fatal("no sqlite artifacts observed at trust time; negative control is vacuous")
		}
		copied := 0
		for _, rel := range stateRels {
			seedHomeFile(t, h1.home, home, rel)
			info, err := os.Stat(filepath.Join(home, rel))
			if err == nil && info.Mode().IsRegular() && strings.HasSuffix(rel, ".sqlite") {
				copied++
			}
		}
		if copied == 0 {
			t.Fatal("no SQLite state DB reached the control home; negative control is vacuous")
		}
		t.Logf("negative_control_state_files=%d sqlite_dbs=%d", len(stateRels), copied)
	})

	if configOutcome != "ready" {
		t.Fatalf("config-seeded headless launch outcome=%s, want ready", configOutcome)
	}
	if stateOutcome != "hook_trust_modal" {
		t.Fatalf("state-seeded headless launch outcome=%s, want hook_trust_modal",
			stateOutcome)
	}
}
