package tasks

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	"github.com/noisefloor-oss/deck/internal/provider"
)

func TestGenerationHarnessSelection(t *testing.T) {
	rootPath := t.TempDir()
	writeStore(t, rootPath, "PRD.md")
	root, err := openEngineTaskRoot(context.Background(), writeStub(t, "exit 0"), rootPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	r := &Runner{}
	for _, tc := range []struct {
		harness, key, selector string
	}{
		{"claude", "", "claude-cli"},
		{"claude", "test-key", ""},
		{"codex", "", "codex"},
		{"codex", "test-key", "codex"},
		{"kimi", "", "kimi"},
		{"omp", "", "omp"},
	} {
		t.Run(tc.harness+"/"+tc.selector, func(t *testing.T) {
			t.Setenv("ANTHROPIC_API_KEY", tc.key)
			selection, err := r.generationHarness(root, tc.harness)
			if err != nil || selection == nil || selection.selector != tc.selector {
				t.Fatalf("selection = %+v, err = %v", selection, err)
			}
		})
	}
	if selection, err := r.generationHarness(root, ""); err != nil || selection != nil {
		t.Fatalf("omitted harness = %+v, %v", selection, err)
	}
	for _, harness := range []string{"shell", "terminal", "opencode", "pi", "future-agent"} {
		var unsupported *unsupportedGenerationHarness
		if _, err := r.generationHarness(root, harness); !errors.As(err, &unsupported) {
			t.Fatalf("%s: expected unsupported harness, got %v", harness, err)
		}
	}
	if err := os.WriteFile(filepath.Join(rootPath, ".tasks", "preferences.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, harness := range []string{"claude", "codex", "kimi", "omp", "shell", "future-agent"} {
		if selection, err := r.generationHarness(root, harness); err != nil || selection != nil {
			t.Fatalf("preferences should bypass %s: %+v, %v", harness, selection, err)
		}
	}
}

func TestGenerationPreferencesStayInsideAnchoredRoot(t *testing.T) {
	requireGenerationSupport(t)
	rootPath := t.TempDir()
	writeStore(t, rootPath, "PRD.md")
	root, err := openEngineTaskRoot(context.Background(), writeStub(t, "exit 0"), rootPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	outside := filepath.Join(t.TempDir(), "preferences.json")
	if err := os.WriteFile(outside, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(rootPath, ".tasks", "preferences.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Runner{}).generationHarness(root, "codex"); !errors.Is(err, ErrUnsafeTree) {
		t.Fatalf("escaping preferences: %v", err)
	}
}

func TestGenerationHarnessEnvironmentIsPerRun(t *testing.T) {
	requireGenerationSupport(t)
	t.Setenv("NOISE_TASKS_PROVIDER", "api")
	t.Setenv("ANTHROPIC_API_KEY", "")
	binDir := t.TempDir()
	binary := filepath.Join(binDir, "codex")
	stub := writeStub(t, fmt.Sprintf(`printf 'selector=%%s\n' "${NOISE_TASKS_PROVIDER-unset}"
if [ "$NOISE_TASKS_PROVIDER" = codex ]; then
  [ "$(command -v codex)" = %q ] || { printf 'wrong executable: %%s\n' "$(command -v codex)"; exit 8; }
  codex
fi
exit 0`, binary))
	data, err := os.ReadFile(stub)
	if err != nil {
		t.Fatal(err)
	}
	guard := `if [ "$2" = context ] && [ "$NOISE_TASKS_PROVIDER" != api ]; then exit 9; fi`
	if err := os.WriteFile(stub, []byte(strings.Replace(string(data), "#!/bin/sh\n", "#!/bin/sh\n"+guard+"\n", 1)), 0o755); err != nil {
		t.Fatal(err)
	}
	e := newJobEnv(t, stub)
	if err := os.WriteFile(binary, []byte("#!/bin/sh\necho configured-codex\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	codex, err := provider.NewCodex(binary, "/unused/hooks", "/unused/receiver")
	if err != nil {
		t.Fatal(err)
	}
	e.runner.WithProviders(map[provider.Name]provider.Descriptor{provider.Codex: codex})
	servicePath := os.Getenv("PATH")
	for _, tc := range []struct {
		harness, want string
	}{
		{"codex", "selector=codex"},
		{"", "selector=api"},
		{"kimi", "selector=kimi"},
		{"omp", "selector=omp"},
		{"claude", "selector=claude-cli"},
	} {
		t.Run(tc.harness, func(t *testing.T) {
			run, err := e.runner.Generate(context.Background(), e.project.ID, "PRD.md", 0, tc.harness)
			if err != nil {
				t.Fatal(err)
			}
			final := e.awaitRun(t, run.ID)
			if final.ExitCode == nil || final.Stdout == nil {
				t.Fatalf("%s: missing exit or output: %+v", tc.harness, final)
			}
			if *final.ExitCode != 0 || !strings.Contains(*final.Stdout, tc.want) {
				t.Fatalf("%s: exit = %v, output = %q", tc.harness, *final.ExitCode, *final.Stdout)
			}
			if tc.harness == "codex" && !strings.Contains(*final.Stdout, "configured-codex") {
				t.Fatalf("configured executable was not found: %s", *final.Stdout)
			}
			if os.Getenv("NOISE_TASKS_PROVIDER") != "api" || os.Getenv("PATH") != servicePath {
				t.Fatal("generation changed the service environment")
			}
		})
	}
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	run, err := e.runner.Generate(context.Background(), e.project.ID, "PRD.md", 2, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if final := e.awaitRun(t, run.ID); final.Stdout == nil || !strings.Contains(*final.Stdout, "selector=unset") {
		t.Fatalf("Claude API selection did not unset the selector: %+v", final)
	}
}

func TestGenerationHarnessEndpoints(t *testing.T) {
	requireGenerationSupport(t)
	t.Setenv("NOISE_TASKS_PROVIDER", "")
	e := newJobEnv(t, writeStub(t, `printf 'selector=%s\n' "$NOISE_TASKS_PROVIDER"`))
	h := NewHandler(e.projects, e.runner.executable, zerolog.Nop()).WithRunner(e.runner)
	post := func(harness string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(fmt.Sprintf(`{"prd":"PRD.md","harness":%q}`, harness)))
		req.SetPathValue("id", e.project.ID)
		rec := httptest.NewRecorder()
		h.Generate(rec, req)
		return rec
	}
	for _, harness := range []string{"shell", "terminal", "opencode", "unknown"} {
		rec := post(harness)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "can't generate tasks yet. Add the first task by hand.") {
			t.Fatalf("%s: %d %s", harness, rec.Code, rec.Body)
		}
	}
	runs, err := e.runs.ListRuns(context.Background(), e.project.ID, 20)
	if err != nil || len(runs) != 0 {
		t.Fatalf("unsupported requests created runs: %v, %v", runs, err)
	}
	if err := os.WriteFile(filepath.Join(e.project.CanonicalPath, ".tasks", "preferences.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if rec := post("shell"); rec.Code != http.StatusAccepted {
		t.Fatalf("preferences did not bypass harness validation: %d %s", rec.Code, rec.Body)
	}
}
