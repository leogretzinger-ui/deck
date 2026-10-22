package boot

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	"github.com/noisefloor-oss/deck/internal/config"
	"github.com/noisefloor-oss/deck/internal/provider"
)

// Harness availability is a property of the image, not of the build: the
// slim distribution ships Claude Code and Codex, the full one ships every
// harness, and the same binary serves both. What makes that possible is
// that a harness which is simply not installed resolves to no executable,
// is skipped with a warning, and never reaches the registry — so a slim
// image is a console with two providers, not a console with fifteen broken
// ones.
//
// This is the contract that lets a variant exist without a build flag, a
// capability list, or a second code path, so it is pinned here.
func TestProvidersResolveFromPathSoAnUninstalledHarnessIsSimplyAbsent(t *testing.T) {
	pathDir := t.TempDir()
	// Exactly what a slim image puts on PATH.
	for _, name := range []string{"claude", "codex", "bash"} {
		script := filepath.Join(pathDir, name)
		if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", pathDir)

	// Loading is what resolves an unset executable from PATH, so the test
	// goes through it rather than reaching for the resolver directly.
	t.Setenv("NOISEDECK_AUTH_MODE", "none")
	t.Setenv("NOISEDECK_DATABASE_PATH", filepath.Join(t.TempDir(), "noisedeck.db"))
	t.Setenv("NOISEDECK_PROJECTS_ALLOWED_ROOTS", t.TempDir())
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if got := cfg.Providers.Claude.Executable; got != filepath.Join(pathDir, "claude") {
		t.Fatalf("claude executable = %q, want the one on PATH", got)
	}

	var logs bytes.Buffer
	registry := buildProviders(cfg, zerolog.New(&logs))

	// The whole registry, not a sample of it: a slim image's console offers
	// these three and advertises nothing it cannot launch.
	// The shell provider is named "shell"; bash is only its binary.
	want := []string{"claude", "codex", "shell"}
	if got := names(registry); !slices.Equal(got, want) {
		t.Errorf("slim registry = %v, want exactly %v", got, want)
	}

	// Absence is a warning, never a failure: an image variant must not be
	// able to make the console refuse to boot.
	if !strings.Contains(logs.String(), "provider not configured; unavailable") {
		t.Errorf("uninstalled providers were not logged as unavailable: %s", logs.String())
	}
	for _, level := range []string{`"level":"error"`, `"level":"fatal"`} {
		if strings.Contains(logs.String(), level) {
			t.Errorf("missing harness produced %s: %s", level, logs.String())
		}
	}
}

func names(registry map[provider.Name]provider.Descriptor) []string {
	out := make([]string, 0, len(registry))
	for name := range registry {
		out = append(out, string(name))
	}
	sort.Strings(out)
	return out
}

// The registry is only half the fix. The browser cannot see it, and until it
// can, a slim image still draws chips for harnesses it does not have — so the
// mounted endpoint that publishes it is part of the same contract and is
// pinned beside it.
//
// It must also be behind the browser's authentication: which harnesses a
// console has is a fact about the user's machine, and nothing about it is
// public.
func TestBootPublishesTheRegistryToTheBrowserBehindAuth(t *testing.T) {
	cfg := testConfig(t)
	cfg.Auth.Mode = "trusted_proxy"
	cfg.Auth.TrustedProxy.Addresses = []string{"127.0.0.1"}
	// A deliberately narrow image: one harness and the terminal. Codex is
	// left out because it needs a provisioned hook to construct, and what is
	// under test here is that the endpoint publishes whatever boot built.
	shell := filepath.Join(t.TempDir(), "bash")
	if err := os.WriteFile(shell, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg.Providers = config.Providers{
		Claude: config.Provider{Executable: "/usr/local/bin/claude"},
		Shell:  config.Provider{Executable: shell},
	}
	h := upOrFail(t, cfg)
	base := serveServer(t, h)

	if code, _ := get(t, base+"/api/providers"); code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated providers = %d, want 401", code)
	}
	code, body := authenticatedGet(t, base+"/api/providers")
	if code != http.StatusOK {
		t.Fatalf("providers = %d (%s)", code, body)
	}
	var served struct {
		Providers []string `json:"providers"`
	}
	if err := json.Unmarshal([]byte(body), &served); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	// The endpoint answers the registry this very boot built — not a list
	// written down anywhere, which is the drift the endpoint exists to end.
	if want := names(h.Providers); !slices.Equal(served.Providers, want) {
		t.Fatalf("served %v, want the booted registry %v", served.Providers, want)
	}
	if !slices.Equal(served.Providers, []string{"claude", "shell"}) {
		t.Fatalf("narrow image published %v, want claude, shell", served.Providers)
	}
}

// Harness usage is the same class of fact as the registry — what this
// user launches, and how often — so it is mounted behind the same
// authentication, and a fresh installation answers with empty history rather
// than an error.
func TestBootServesHarnessUsageBehindAuth(t *testing.T) {
	cfg := testConfig(t)
	cfg.Auth.Mode = "trusted_proxy"
	cfg.Auth.TrustedProxy.Addresses = []string{"127.0.0.1"}
	shell := filepath.Join(t.TempDir(), "bash")
	if err := os.WriteFile(shell, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg.Providers = config.Providers{Shell: config.Provider{Executable: shell}}
	base := serveServer(t, upOrFail(t, cfg))

	if code, _ := get(t, base+"/api/harness-usage"); code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated harness usage = %d, want 401", code)
	}
	code, body := authenticatedGet(t, base+"/api/harness-usage?project=absent")
	if code != http.StatusOK {
		t.Fatalf("harness usage = %d (%s)", code, body)
	}
	// Both scopes must be present and empty, which a length check alone
	// would also accept from a null or a missing field.
	if body != `{"global":[],"project":[]}`+"\n" {
		t.Fatalf("a fresh installation answered %q, want two empty scopes", body)
	}
}
