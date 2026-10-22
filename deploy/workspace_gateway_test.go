package deploy

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestRenderedGatewayRemovesEveryPublishedPort(t *testing.T) {
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Skip("docker compose renderer unavailable")
	}
	env := renderEnv(t)
	for key, value := range map[string]string{
		"COMPOSE_PROJECT_NAME": "gateway-fixture", "NOISEDECK_PUBLIC_ORIGIN": "https://workspace.example.test",
		"NOISEDECK_MEMPALACE_HOST_PATH": "/fixture/state/home/.mempalace",
		"NOISEDECK_SEALED_STATE_ROOT":   "/fixture/state", "NOISEDECK_SEALED_REPORT_DIR": "/fixture/report",
		"NOISEDECK_SEALED_HOSTNAME": "gateway-fixture", "NOISEDECK_RUNTIME_UID": "2345", "NOISEDECK_RUNTIME_GID": "2345",
	} {
		env[key] = value
	}
	var lines []string
	for key, value := range env {
		lines = append(lines, key+"="+value)
	}
	path := filepath.Join(t.TempDir(), "compose.env")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(docker, "compose", "--project-name", "gateway-fixture", "--file", "compose.yaml", "--file", "compose.mempalace.yaml", "--file", "compose.sealed.yaml", "--file", "compose.sealed-podman.yaml", "--file", "compose.workspace-gateway.yaml", "--env-file", path, "config")
	cmd.Dir = ".."
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Compose rendering failed: %v: %s", err, raw)
	}
	var rendered struct {
		Services map[string]struct {
			Ports       []any          `yaml:"ports"`
			Environment map[string]any `yaml:"environment"`
			UsernsMode  string         `yaml:"userns_mode"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(raw, &rendered); err != nil {
		t.Fatal(err)
	}
	for name, service := range rendered.Services {
		if len(service.Ports) != 0 {
			t.Fatalf("%s bypasses the gateway through published ports", name)
		}
	}
	app, ok := rendered.Services["app"]
	if !ok {
		t.Fatal("missing app")
	}
	for key, want := range map[string]string{
		"NOISEDECK_SERVER_BIND": "", "NOISEDECK_SERVER_UNIX_SOCKET": "/data/gateway/backend.sock",
		"NOISEDECK_SERVER_GATEWAY_UID": "2345", "NOISEDECK_AUTH_MODE": "oidc",
		"NOISEDECK_PROJECT_PREVIEWS_BIND": "", "NOISEDECK_EXPERIMENTS_UNCONFINED_CAPACITY_ROUTING": "false",
		"NOISEDECK_SERVER_ALLOWED_ORIGINS": "https://workspace.example.test",
	} {
		if fmt.Sprint(app.Environment[key]) != want {
			t.Errorf("%s did not retain private gateway setting", key)
		}
	}
	if app.UsernsMode != "keep-id:uid=2345,gid=2345" {
		t.Fatal("lost keep-id UID mapping")
	}
}

func TestGatewayWrapperRejectsIncompatibleProfileBeforeNativeCommands(t *testing.T) {
	for _, value := range []string{"true", "invalid"} {
		t.Run(value, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "compose.env")
			body := "COMPOSE_PROJECT_NAME=gateway-fixture\nNOISEDECK_SEALED_STATE_ROOT=/fixture/state\nNOISEDECK_SEALED_REPORT_DIR=/fixture/report\nNOISEDECK_SEALED_PROFILE=own-host\nNOISEDECK_WORKSPACE_GATEWAY=" + value + "\n"
			if err := os.WriteFile(path, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("sh", "sealed-compose.sh", path, "down")
			cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}
			raw, err := cmd.CombinedOutput()
			if err == nil || (!strings.Contains(string(raw), "requires the shared-host profile") && !strings.Contains(string(raw), "must be true or false")) {
				t.Fatalf("profile was not refused first: %v %s", err, raw)
			}
		})
	}
}
