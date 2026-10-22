package deploy

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleaseAcceptanceRunsNativeLoginInTheBuiltImage(t *testing.T) {
	body := string(readWorkflow(t, "release-acceptance.yaml"))
	_, step, ok := strings.Cut(body, "      - name: Qualify native login against the built runtime packages\n")
	if !ok {
		t.Fatal("native login acceptance is missing")
	}
	step, _, _ = strings.Cut(step, "      - name:")
	for _, required := range []string{
		"shell: bash", "set -euo pipefail", "git archive HEAD",
		"go test -c -o /out/native-login.test ./internal/harnesslogin",
		"--network none", "--cap-drop ALL", "--security-opt no-new-privileges",
		"--env NOISEDECK_TEST_CODEX_EXECUTABLE=/usr/local/bin/codex",
		"--env NOISEDECK_TEST_PI_EXECUTABLE=/usr/local/bin/pi",
		`--entrypoint /opt/native-login.test "deck:ci-${GITHUB_RUN_ID}"`,
		"^TestNative(Codex|Pi)BrowserRegistration$", "if grep -q -- '--- SKIP:'",
		"TestNativeCodexBrowserRegistration", "TestNativePiBrowserRegistration/anthropic",
		"TestNativePiBrowserRegistration/openai-codex",
		`grep -Eq "^[[:space:]]*--- PASS: ${test} [(]"`,
	} {
		if !strings.Contains(step, required) {
			t.Errorf("native acceptance missing %q", required)
		}
	}
	for _, forbidden := range []string{"secrets.", "AUTH_DIR", "auth.json", "docker.sock", "--network host"} {
		if strings.Contains(step, forbidden) {
			t.Errorf("native acceptance exposes %q", forbidden)
		}
	}
}

func TestNativeLoginAcceptanceRequiresEveryProbeToPass(t *testing.T) {
	body := string(readWorkflow(t, "release-acceptance.yaml"))
	_, step, ok := strings.Cut(body, "      - name: Qualify native login against the built runtime packages\n")
	if !ok {
		t.Fatal("native login acceptance is missing")
	}
	step, _, _ = strings.Cut(step, "      - name:")
	_, assertions, ok := strings.Cut(step, "          if grep -q -- '--- SKIP:'")
	if !ok {
		t.Fatal("native login result assertions are missing")
	}
	assertions = "if grep -q -- '--- SKIP:'" + assertions
	const codex = "--- PASS: TestNativeCodexBrowserRegistration (0.60s)\n"
	const anthropic = "    --- PASS: TestNativePiBrowserRegistration/anthropic (3.25s)\n"
	const openai = "    --- PASS: TestNativePiBrowserRegistration/openai-codex (3.28s)\n"
	for _, tc := range []struct {
		name, output string
		wantPass     bool
	}{
		{"all probes passed", codex + anthropic + openai + "PASS\n", true},
		{"no tests matched", "testing: warning: no tests to run\nPASS\n", false},
		{"missing Codex", anthropic + openai + "PASS\n", false},
		{"missing Pi Anthropic", codex + openai + "PASS\n", false},
		{"missing Pi OpenAI", codex + anthropic + "PASS\n", false},
		{"parent test only", codex + "--- PASS: TestNativePiBrowserRegistration (0.0s)\nPASS\n", false},
		{"skipped probe", codex + anthropic + openai + "--- SKIP: TestNativePiBrowserRegistration/extra (0.0s)\nPASS\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "results.txt"), []byte(tc.output), 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("bash", "-eu", "-c", assertions)
			cmd.Env = append(os.Environ(), "probe_dir="+dir)
			out, err := cmd.CombinedOutput()
			if (err == nil) != tc.wantPass {
				t.Fatalf("result gate passed=%v, want %v: %v\n%s", err == nil, tc.wantPass, err, out)
			}
		})
	}
}
