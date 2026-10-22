package deploy

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBrowserScreenshotSkillStopsWithoutOverlay(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is unavailable")
	}
	script, err := filepath.Abs(filepath.Join("skills", "browser-screenshot", "screenshot.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	command := exec.Command(node, script, "http://127.0.0.2:8840")
	command.Dir = project
	command.Env = []string{"PATH=" + os.Getenv("PATH")}
	output, err := command.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("screenshot without NOISE_BROWSER_WS: err=%v\n%s", err, output)
	}
	if !strings.Contains(string(output), "browser overlay is not enabled") {
		t.Errorf("screenshot without NOISE_BROWSER_WS did not say the overlay is off:\n%s", output)
	}
	if _, err := os.Stat(filepath.Join(project, ".deck")); !os.IsNotExist(err) {
		t.Errorf("screenshot without NOISE_BROWSER_WS touched .deck: %v", err)
	}
}

// The skill runs in the app container, not the sidecar, so every app image
// must carry the client at the path the script loads.
func TestBrowserScreenshotClientShipsInAppImage(t *testing.T) {
	script, err := os.ReadFile(filepath.Join("skills", "browser-screenshot", "screenshot.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	const client = "/opt/noise-browser/node_modules"
	if !strings.Contains(string(script), `const CLIENT = "`+client+`/playwright";`) {
		t.Fatalf("screenshot.mjs no longer loads the client from %s", client)
	}
	dockerfile, err := os.ReadFile(filepath.Join("..", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	_, core, ok := strings.Cut(string(dockerfile), " AS core\n")
	if !ok {
		t.Fatal("Dockerfile has no core stage")
	}
	core, _, _ = strings.Cut(core, "\nFROM ")
	if !strings.Contains(core, "install -d -m 0755 "+client) ||
		!strings.Contains(core, "node -e \"require('"+client+"/playwright')\"") {
		t.Errorf("core stage does not install and load the Playwright client at %s", client)
	}
}
