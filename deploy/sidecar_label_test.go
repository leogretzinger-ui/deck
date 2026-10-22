package deploy

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestProductionResolverActualSELinuxLabel(t *testing.T) {
	file := filepath.Join(t.TempDir(), "resolver")
	if err := os.WriteFile(file, []byte("nameserver 127.0.0.11\n"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, label string
		enabled, ok bool
	}{
		{"shared", "system_u:object_r:container_file_t:s0", true, true},
		{"wrong type", "unconfined_u:object_r:var_t:s0", true, false},
		{"private", "system_u:object_r:container_file_t:s0:c1,c2", true, false},
		{"unreadable", "error", true, false},
		{"malformed", "s0", true, false},
		{"no SELinux", "error", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := productionFixture()
			browser := fixture["services"].(map[string]any)["chromium"].(map[string]any)
			browser["volumes"].([]any)[0].(map[string]any)["source"] = file
			payload, err := json.Marshal(fixture)
			if err != nil {
				t.Fatal(err)
			}
			enabled := "False"
			if tc.enabled {
				enabled = "True"
			}
			script := `import os, pathlib, runpy, sys
from unittest.mock import patch
original = pathlib.Path.exists
def exists(path):
    return ` + enabled + ` if str(path) == '/sys/fs/selinux/enforce' else original(path)
def label(path, name):
    if sys.argv[1] == 'error': raise PermissionError('synthetic unreadable label')
    return sys.argv[1].encode() + b'\0'
with patch.object(pathlib.Path, 'exists', exists), patch.object(os, 'getxattr', label):
    sys.argv.append('--check-host-files')
    runpy.run_path('verify-production-compose.py', run_name='__main__')
`
			command := exec.Command("python3", "-O", "-c", script, tc.label)
			command.Stdin = bytes.NewReader(payload)
			out, err := command.CombinedOutput()
			if (err == nil) != tc.ok {
				t.Fatalf("label preflight: %v\n%s", err, out)
			}
			if !tc.ok && !strings.Contains(string(out), "resolver") {
				t.Fatalf("wrong rejection: %s", out)
			}
		})
	}
}

func TestProductionBrowserWaitsForHydration(t *testing.T) {
	raw, err := os.ReadFile("production-compose.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, navigation := range []string{
		`await page.goto("http://127.0.0.1:8837", {waitUntil: "domcontentloaded"});`,
		`await page.goto(process.env.NOISEDECK_VERIFY_ORIGIN, {waitUntil: "domcontentloaded"});`,
	} {
		_, after, ok := strings.Cut(string(raw), navigation)
		if !ok {
			t.Fatalf("missing navigation: %s", navigation)
		}
		if !strings.HasPrefix(strings.TrimSpace(after), `await page.getByText("Sign in with Google", {exact: true}).waitFor({state: "visible", timeout: 15000});`) {
			t.Fatal("login assertion must await visible hydrated UI after navigation")
		}
	}
}
