package doctor

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/noisefloor-oss/deck/internal/config"
	"github.com/noisefloor-oss/deck/internal/taskengine"
)

func fake(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func healthy(t *testing.T) (*config.Config, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	fake(t, dir, "tmux", "[ \"$*\" = '-V' ] || exit 2; printf 'tmux 3.6a\\n'")
	fake(t, dir, "git", "exit 99")
	fake(t, dir, "gh", "exit 99")
	noise := fake(t, dir, "noise", "[ \"$*\" = 'tasks version' ] || exit 2; printf 'noise-tasks "+taskengine.Version+"\\n'")
	a2a := fake(t, dir, "a2a", "exit 99")
	cfg := config.Defaults()
	cfg.Server.Bind = "127.0.0.1:0"
	cfg.Database.Path = filepath.Join(t.TempDir(), "new", "deck.db")
	cfg.Taskmaster.Executable = noise
	cfg.Chats.Executable = a2a
	cfg.Providers = config.Providers{}
	return &cfg, dir
}

func find(t *testing.T, report Report, name string) Check {
	t.Helper()
	for _, check := range report.Checks {
		if check.Name == name {
			return check
		}
	}
	t.Fatalf("missing check %s", name)
	return Check{}
}

func TestRequiredToolsAndOptionalProviders(t *testing.T) {
	cfg, _ := healthy(t)
	report := Run(context.Background(), cfg, false)
	if !report.OK {
		t.Fatalf("healthy tools: %+v", report)
	}
	check := find(t, report, "provider:claude")
	if check.Required || !check.OK || check.Message != "Not configured; optional." {
		t.Fatalf("absent optional provider: %+v", check)
	}
	if _, err := os.Stat(filepath.Dir(cfg.Database.Path)); !os.IsNotExist(err) {
		t.Fatalf("doctor left a data directory behind: %v", err)
	}
}

func TestMissingTools(t *testing.T) {
	for _, name := range []string{"tmux", "git", "gh", "noise", "a2a"} {
		t.Run(name, func(t *testing.T) {
			cfg, dir := healthy(t)
			if err := os.Remove(filepath.Join(dir, name)); err != nil {
				t.Fatal(err)
			}
			if name == "noise" {
				name = "tasks"
			}
			if name == "a2a" {
				name = "chats"
			}
			report := Run(context.Background(), cfg, false)
			check := find(t, report, name)
			if report.OK || check.OK || check.Fix == "" || !check.Required {
				t.Fatalf("missing required tool: %+v", check)
			}
		})
	}
}

func TestWrongVersionsAndFailedProbes(t *testing.T) {
	for _, tc := range []struct{ name, output string }{
		{"tmux", "tmux 3.3a"},
		{"tmux", "unknown"},
		{"noise", "noise-tasks 2.6.0"},
		{"noise", "noise 2.7.0"},
		{"noise", "noise-tasks 2.7.0 extra"},
	} {
		t.Run(tc.output, func(t *testing.T) {
			cfg, dir := healthy(t)
			fake(t, dir, tc.name, "printf '%s\\n' '"+tc.output+"'")
			name := tc.name
			if name == "noise" {
				name = "tasks"
			}
			report := Run(context.Background(), cfg, false)
			check := find(t, report, name)
			if report.OK || check.OK || check.Fix == "" {
				t.Fatalf("wrong version: %+v", check)
			}
		})
	}
	for _, name := range []string{"tmux", "noise"} {
		t.Run(name+" exits nonzero", func(t *testing.T) {
			cfg, dir := healthy(t)
			fake(t, dir, name, "exit 1")
			if Run(context.Background(), cfg, false).OK {
				t.Fatal("accepted failed version probe")
			}
		})
	}
}

func TestProviderProbeIsIsolatedAndNeverLaunchesASession(t *testing.T) {
	cfg, dir := healthy(t)
	t.Setenv("ANTHROPIC_API_KEY", "must-not-be-inherited")
	t.Setenv("DOCTOR_SECRET", "must-not-be-inherited")
	realHome := t.TempDir()
	t.Setenv("HOME", realHome)
	path := fake(t, dir, "claude", `
[ "$*" = '--version' ] || exit 2
[ -z "$DOCTOR_SECRET$ANTHROPIC_API_KEY" ] || exit 3
[ "$HOME" = "$XDG_CONFIG_HOME" ] || exit 4
[ "$HOME" = "$XDG_CACHE_HOME" ] || exit 5
[ "$HOME" = "$PWD" ] || exit 6
printf version > "$HOME/probe-state"
printf 'claude version\n'`)
	cfg.Providers.Claude.Executable = path
	if report := Run(context.Background(), cfg, false); !report.OK {
		t.Fatalf("isolated probe failed: %+v", find(t, report, "provider:claude"))
	}
	entries, err := os.ReadDir(realHome)
	if err != nil || len(entries) != 0 {
		t.Fatalf("probe wrote to your home: %v, %v", entries, err)
	}
	fake(t, dir, "claude", "exit 1")
	if report := Run(context.Background(), cfg, false); report.OK || find(t, report, "provider:claude").OK {
		t.Fatal("accepted a provider that cannot run")
	}
	cfg.Providers.Claude.Executable = filepath.Join(dir, "absent")
	if Run(context.Background(), cfg, false).OK {
		t.Fatal("accepted a missing configured provider")
	}
	fake(t, dir, "claude", "exit 0")
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.Providers.Claude.Executable = path
	if Run(context.Background(), cfg, false).OK {
		t.Fatal("accepted a non-executable provider")
	}
}

func TestSlowProviderAndTimeoutWarning(t *testing.T) {
	cfg, dir := healthy(t)
	cfg.Providers.Claude.Executable = fake(t, dir, "claude", "[ \"$*\" = '--version' ] || exit 2; exec /bin/sleep 6")
	cfg.Providers.Cline.Executable = fake(t, dir, "cline", "[ \"$*\" = '--version' ] || exit 2; exec /bin/sleep 60")
	report := Run(context.Background(), cfg, false)
	if !report.OK || !find(t, report, "provider:claude").OK {
		t.Fatalf("slow healthy provider failed: %+v", report)
	}
	check := find(t, report, "provider:cline")
	if check.OK || check.Required || !strings.Contains(check.Message, "slow to answer") || check.Fix == "" {
		t.Fatalf("timeout did not produce a warning: %+v", check)
	}
	var text bytes.Buffer
	if err := report.WriteText(&text); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text.String(), "provider:cline: warning:") || strings.Contains(text.String(), ": error:") {
		t.Fatalf("timeout text: %s", &text)
	}
	fake(t, dir, "cline", "exit 1")
	fake(t, dir, "claude", "exit 0")
	report = Run(context.Background(), cfg, false)
	check = find(t, report, "provider:cline")
	if report.OK || check.OK || !check.Required {
		t.Fatalf("non-zero exit did not fail required check: %+v", check)
	}
}

func TestSuccessfulProbeWithDelayedPipeClose(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name string
		code string
		ok   bool
	}{
		{"success", "0", true},
		{"failure", "1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := fake(t, dir, tc.name, "/bin/sleep 2 &\nprintf 'version\\n'\nexit "+tc.code)
			out, err := probe(context.Background(), path, []string{"--version"}, true)
			if (err == nil) != tc.ok || strings.TrimSpace(out) != "version" {
				t.Fatalf("output=%q, err=%v", out, err)
			}
		})
	}
}

func TestProviderProbesRunConcurrentlyWithFourSlots(t *testing.T) {
	cfg, dir := healthy(t)
	state := t.TempDir()
	body := `
acquired=
for slot in 1 2 3 4; do
  if /bin/mkdir '` + state + `'/slot$slot 2>/dev/null; then
    acquired=$slot
    break
  fi
done
[ -n "$acquired" ] || exit 3
trap '/bin/rmdir "` + state + `/slot$acquired"' EXIT
printf started > '` + state + `'/started-$$
while [ ! -f '` + state + `/release' ]; do /bin/sleep 0.01; done
`
	path := fake(t, dir, "provider", body)
	for _, entry := range cfg.Providers.Entries() {
		entry.Config.Executable = path
	}
	done := make(chan Report, 1)
	go func() { done <- Run(context.Background(), cfg, false) }()
	deadline := time.Now().Add(5 * time.Second)
	var started []string
	for time.Now().Before(deadline) {
		started, _ = filepath.Glob(filepath.Join(state, "started-*"))
		if len(started) >= 4 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := os.WriteFile(filepath.Join(state, "release"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	report := <-done
	if len(started) != 4 || !report.OK {
		t.Fatalf("wanted four concurrent probes, got %d; report=%+v", len(started), report)
	}
	for i, entry := range cfg.Providers.Entries() {
		if check := report.Checks[5+i]; check.Name != "provider:"+entry.Name {
			t.Fatalf("provider check order changed: %+v", report.Checks)
		}
	}
}

func TestListenAddressFreeOwnedAndOccupied(t *testing.T) {
	cfg, _ := healthy(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	cfg.Server.Bind = ln.Addr().String()
	if report := Run(context.Background(), cfg, false); report.OK || find(t, report, "listen-address").OK {
		t.Fatal("accepted an unrelated listener")
	}
	if report := Run(context.Background(), cfg, true); !report.OK {
		t.Fatalf("refused the serving Deck's own listener: %+v", report)
	}
}

func TestPrivateSocketAndUnwritableData(t *testing.T) {
	cfg, _ := healthy(t)
	cfg.Server.UnixSocket = filepath.Join(t.TempDir(), "deck.sock")
	ln, err := net.Listen("unix", cfg.Server.UnixSocket)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if listener(cfg, false).OK {
		t.Fatal("accepted an unrelated private socket")
	}
	if _, err := os.Stat(cfg.Server.UnixSocket); err != nil {
		t.Fatalf("doctor removed a live socket: %v", err)
	}
	cfg.Server.UnixSocket = filepath.Join(t.TempDir(), "absent", "deck.sock")
	if !listener(cfg, false).OK {
		t.Fatal("refused a fresh private socket location")
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.Database.Path = filepath.Join(file, "deck.db")
	report := Run(context.Background(), cfg, false)
	if report.OK || find(t, report, "data-directory").OK {
		t.Fatal("accepted data directory that is a file")
	}
}

func TestJSONShapeAndPlainText(t *testing.T) {
	cfg, dir := healthy(t)
	if err := os.Remove(filepath.Join(dir, "gh")); err != nil {
		t.Fatal(err)
	}
	report := Run(context.Background(), cfg, false)
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 2 || string(decoded["ok"]) != "false" {
		t.Fatalf("report shape: %s", raw)
	}
	var checks []map[string]any
	if err := json.Unmarshal(decoded["checks"], &checks); err != nil {
		t.Fatal(err)
	}
	for _, check := range checks {
		for _, key := range []string{"name", "required", "ok", "message"} {
			if _, ok := check[key]; !ok {
				t.Fatalf("missing %s: %s", key, raw)
			}
		}
		if check["ok"] == false && check["fix"] == nil {
			t.Fatalf("missing fix: %s", raw)
		}
	}
	var text bytes.Buffer
	if err := report.WriteText(&text); err != nil {
		t.Fatal(err)
	}
	if strings.Count(text.String(), "\n") != len(report.Checks) || !strings.Contains(text.String(), "brew install gh") {
		t.Fatalf("text report: %s", text.String())
	}
}
