package capacity

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/noisefloor-oss/deck/internal/provider"
)

// The live probes run the real collector against the installed, logged-in
// claude and codex executables. They are opt-in because CI has no provider
// credentials: the default suite skips them, and a machine that does have
// the CLIs fails loudly the moment the parsers stop recognizing real output.
// Log output below carries normalized Evidence fields only; raw provider
// stdout/stderr must never reach test output.

const liveProbeCommandTimeout = 2 * time.Minute

func requireLiveCapacityProbe(t *testing.T) {
	t.Helper()
	if os.Getenv("NOISEDECK_LIVE_CAPACITY_PROBE") != "1" {
		t.Skip("set NOISEDECK_LIVE_CAPACITY_PROBE=1 to probe the installed provider CLIs")
	}
}

func TestLiveProbeClaude(t *testing.T) {
	requireLiveCapacityProbe(t)
	evidence := collectLiveEvidence(t, provider.Claude)
	logLiveEvidence(t, evidence)
	assertLiveEvidence(t, evidence)
	assertLiveModel(t, evidence, "fable")
}

func TestLiveProbeCodex(t *testing.T) {
	requireLiveCapacityProbe(t)
	evidence := collectLiveEvidence(t, provider.Codex)
	logLiveEvidence(t, evidence)
	assertLiveEvidence(t, evidence)
}

func collectLiveEvidence(t *testing.T, name provider.Name) Evidence {
	t.Helper()
	collector := New(liveProbeDescriptors(t))
	ctx, cancel := context.WithTimeout(context.Background(), liveProbeCommandTimeout)
	defer cancel()
	return collector.Collect(ctx, name)
}

// liveProbeDescriptors wires the real executables the way boot does: PATH
// discovery plus the codex hook paths the config defaults install. Capacity
// collection reads only the executable; the hook paths keep the descriptor
// in its production shape.
func liveProbeDescriptors(t *testing.T) map[provider.Name]provider.Descriptor {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("live probe: home directory: %v", err)
	}
	claude, err := provider.NewClaude(liveProbeExecutable(t, "claude"))
	if err != nil {
		t.Fatalf("live probe: claude descriptor: %v", err)
	}
	codex, err := provider.NewCodex(liveProbeExecutable(t, "codex"),
		filepath.Join(home, ".codex", "hooks.json"), provider.CodexIdentityHookPath)
	if err != nil {
		t.Fatalf("live probe: codex descriptor: %v", err)
	}
	return map[provider.Name]provider.Descriptor{
		provider.Claude: claude,
		provider.Codex:  codex,
	}
}

func liveProbeExecutable(t *testing.T, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		t.Fatalf("live probe: %s executable not found: %v", name, err)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("live probe: %s path %q: %v", name, path, err)
	}
	return absolute
}

func assertLiveEvidence(t *testing.T, evidence Evidence) {
	t.Helper()
	if evidence.State == "unknown" {
		t.Fatalf("provider %s state = %q, diagnostic %q",
			evidence.Provider, evidence.State, evidence.Diagnostic)
	}
	if len(evidence.Windows) == 0 {
		t.Fatalf("provider %s reported no usage windows, diagnostic %q",
			evidence.Provider, evidence.Diagnostic)
	}
	for _, window := range evidence.Windows {
		if window.UsedPercent < 0 || window.UsedPercent > 100 {
			t.Fatalf("provider %s window %s/%s used_percent = %v",
				evidence.Provider, window.Name, window.Scope, window.UsedPercent)
		}
		if window.ResetsAt == nil {
			continue
		}
		if window.ResetsAt.IsZero() {
			t.Fatalf("provider %s window %s/%s resets at the zero instant",
				evidence.Provider, window.Name, window.Scope)
		}
		if !window.ResetsAt.After(evidence.ObservedAt) {
			t.Fatalf("provider %s window %s/%s resets at %s, observed at %s",
				evidence.Provider, window.Name, window.Scope,
				window.ResetsAt.Format(time.RFC3339), evidence.ObservedAt.Format(time.RFC3339))
		}
	}
}

func assertLiveModel(t *testing.T, evidence Evidence, id string) {
	t.Helper()
	for _, model := range evidence.Models {
		if model.ID == id {
			return
		}
	}
	t.Fatalf("provider %s model %q absent from models %v",
		evidence.Provider, id, liveModelIDs(evidence))
}

// logLiveEvidence emits normalized collector output only: provider,
// window names, scopes, percentages, reset instants, and model ids. The
// diagnostic is one of the collector's fixed labels, never provider bytes.
func logLiveEvidence(t *testing.T, evidence Evidence) {
	t.Helper()
	t.Logf("provider=%s source=%s state=%s diagnostic=%q observed_at=%s",
		evidence.Provider, evidence.Source, evidence.State, evidence.Diagnostic,
		evidence.ObservedAt.Format(time.RFC3339))
	for _, window := range evidence.Windows {
		reset := "none"
		if window.ResetsAt != nil {
			reset = window.ResetsAt.Format(time.RFC3339)
		}
		t.Logf("window name=%s scope=%s used_percent=%v resets=%s",
			window.Name, window.Scope, window.UsedPercent, reset)
	}
	t.Logf("models=%s", strings.Join(liveModelIDs(evidence), ","))
}

func liveModelIDs(evidence Evidence) []string {
	ids := make([]string, 0, len(evidence.Models))
	for _, model := range evidence.Models {
		ids = append(ids, model.ID)
	}
	return ids
}
