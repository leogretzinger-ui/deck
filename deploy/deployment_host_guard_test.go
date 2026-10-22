package deploy

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeploymentHostGuardFailsClosed(t *testing.T) {
	identity := filepath.Join(t.TempDir(), "machine-id")
	if err := os.WriteFile(identity, []byte("authorized-host\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	expected := fmt.Sprintf("%x", sha256.Sum256([]byte("authorized-host\n")))
	script := filepath.Join("provider-hook", "noisedeck-deployment-host-guard")

	tests := []struct {
		name     string
		expected string
		file     string
		wantOK   bool
		want     string
	}{
		{name: "match", expected: expected, file: identity, wantOK: true},
		{name: "malformed fingerprint", expected: strings.Repeat("A", 64), file: identity, want: "must be exactly 64 lowercase hexadecimal characters"},
		{name: "missing identity", expected: expected, file: filepath.Join(t.TempDir(), "missing"), want: "identity is not a readable regular file"},
		{name: "directory identity", expected: expected, file: t.TempDir(), want: "identity is not a readable regular file"},
		{name: "wrong host", expected: strings.Repeat("0", 64), file: identity, want: "refusing to start on an unauthorized host"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			receipt := filepath.Join(t.TempDir(), "deployment-host.sha256")
			cmd := exec.Command("/bin/sh", script)
			cmd.Env = []string{
				"PATH=" + os.Getenv("PATH"),
				"NOISEDECK_DEPLOYMENT_HOST_SHA256=" + tt.expected,
				"NOISEDECK_DEPLOYMENT_HOST_ID_FILE=" + tt.file,
				"NOISEDECK_DEPLOYMENT_HOST_RECEIPT=" + receipt,
			}
			out, err := cmd.CombinedOutput()
			if tt.wantOK {
				if err != nil {
					t.Fatalf("guard failed: %v\n%s", err, out)
				}
				return
			}
			if err == nil {
				t.Fatalf("guard unexpectedly passed: %s", out)
			}
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 78 {
				t.Fatalf("guard exit = %v, want 78; output: %s", err, out)
			}
			if !strings.Contains(string(out), tt.want) {
				t.Fatalf("guard output = %q, want %q", out, tt.want)
			}
		})
	}
}

func TestDeploymentHostGuardCreatesDurableAuthorizationReceipt(t *testing.T) {
	dir := t.TempDir()
	identity := filepath.Join(dir, "machine-id")
	receipt := filepath.Join(dir, "deployment-host.sha256")
	if err := os.WriteFile(identity, []byte("first-host\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join("provider-hook", "noisedeck-deployment-host-guard")
	run := func() ([]byte, error) {
		cmd := exec.Command("/bin/sh", script)
		cmd.Env = []string{
			"PATH=" + os.Getenv("PATH"),
			"NOISEDECK_DEPLOYMENT_HOST_ID_FILE=" + identity,
			"NOISEDECK_DEPLOYMENT_HOST_RECEIPT=" + receipt,
		}
		return cmd.CombinedOutput()
	}
	if out, err := run(); err != nil {
		t.Fatalf("first boot: %v\n%s", err, out)
	}
	want := fmt.Sprintf("%x\n", sha256.Sum256([]byte("first-host\n")))
	got, err := os.ReadFile(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("receipt = %q, want %q", got, want)
	}
	info, err := os.Stat(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("receipt mode = %04o, want 0600", info.Mode().Perm())
	}
	if out, err := run(); err != nil {
		t.Fatalf("restart: %v\n%s", err, out)
	}
	if err := os.Chmod(identity, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(identity, []byte("moved-host\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	if out, err := run(); err == nil || !strings.Contains(string(out), "unauthorized host") {
		t.Fatalf("moved volume result = %v, output %q", err, out)
	}
}

func TestDeploymentHostGuardConcurrentFirstBootsAgree(t *testing.T) {
	dir := t.TempDir()
	identity := filepath.Join(dir, "machine-id")
	receipt := filepath.Join(dir, "deployment-host.sha256")
	if err := os.WriteFile(identity, []byte("one-host\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join("provider-hook", "noisedeck-deployment-host-guard")
	const workers = 8
	errs := make(chan error, workers)
	for range workers {
		go func() {
			cmd := exec.Command("/bin/sh", script)
			cmd.Env = []string{
				"PATH=" + os.Getenv("PATH"),
				"NOISEDECK_DEPLOYMENT_HOST_ID_FILE=" + identity,
				"NOISEDECK_DEPLOYMENT_HOST_RECEIPT=" + receipt,
			}
			if out, err := cmd.CombinedOutput(); err != nil {
				errs <- fmt.Errorf("%w: %s", err, out)
				return
			}
			errs <- nil
		}()
	}
	for range workers {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	want := fmt.Sprintf("%x\n", sha256.Sum256([]byte("one-host\n")))
	if got, err := os.ReadFile(receipt); err != nil || string(got) != want {
		t.Fatalf("receipt = %q, %v; want %q", got, err, want)
	}
}

func TestDeploymentHostGuardExplicitPinDoesNotCreateReceipt(t *testing.T) {
	dir := t.TempDir()
	identity := filepath.Join(dir, "machine-id")
	receipt := filepath.Join(dir, "deployment-host.sha256")
	body := []byte("explicit-host\n")
	if err := os.WriteFile(identity, body, 0o444); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", filepath.Join("provider-hook", "noisedeck-deployment-host-guard"))
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"NOISEDECK_DEPLOYMENT_HOST_SHA256=" + fmt.Sprintf("%x", sha256.Sum256(body)),
		"NOISEDECK_DEPLOYMENT_HOST_ID_FILE=" + identity,
		"NOISEDECK_DEPLOYMENT_HOST_RECEIPT=" + receipt,
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("explicit pin: %v\n%s", err, out)
	}
	if _, err := os.Stat(receipt); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("explicit pin created receipt: %v", err)
	}
}

func TestDeploymentHostGuardRejectsInvalidReceipt(t *testing.T) {
	dir := t.TempDir()
	identity := filepath.Join(dir, "machine-id")
	if err := os.WriteFile(identity, []byte("host\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join("provider-hook", "noisedeck-deployment-host-guard")
	for _, tt := range []struct {
		name string
		body string
		mode os.FileMode
		want string
	}{
		{name: "malformed", body: "not-a-hash\n", mode: 0o600, want: "malformed"},
		{name: "extra line", body: strings.Repeat("a", 64) + "\nextra\n", mode: 0o600, want: "malformed"},
		{name: "public permissions", body: strings.Repeat("a", 64) + "\n", mode: 0o644, want: "not private"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			receipt := filepath.Join(t.TempDir(), "receipt")
			if err := os.WriteFile(receipt, []byte(tt.body), tt.mode); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("/bin/sh", script)
			cmd.Env = []string{
				"PATH=" + os.Getenv("PATH"),
				"NOISEDECK_DEPLOYMENT_HOST_ID_FILE=" + identity,
				"NOISEDECK_DEPLOYMENT_HOST_RECEIPT=" + receipt,
			}
			out, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(out), tt.want) {
				t.Fatalf("result = %v, output %q, want %q", err, out, tt.want)
			}
		})
	}
	t.Run("symlink", func(t *testing.T) {
		target := filepath.Join(t.TempDir(), "target")
		if err := os.WriteFile(target, []byte(strings.Repeat("a", 64)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		receipt := filepath.Join(t.TempDir(), "receipt")
		if err := os.Symlink(target, receipt); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("/bin/sh", script)
		cmd.Env = []string{
			"PATH=" + os.Getenv("PATH"),
			"NOISEDECK_DEPLOYMENT_HOST_ID_FILE=" + identity,
			"NOISEDECK_DEPLOYMENT_HOST_RECEIPT=" + receipt,
		}
		out, err := cmd.CombinedOutput()
		if err == nil || !strings.Contains(string(out), "regular file") {
			t.Fatalf("result = %v, output %q", err, out)
		}
	})
}

func TestBootstrapStopsBeforeMutationOnUnauthorizedHost(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("provider-hook", "noisedeck-bootstrap"))
	if err != nil {
		t.Fatal(err)
	}

	testDir := t.TempDir()
	reached := filepath.Join(testDir, "reached")
	guard := writeExecutable(t, testDir, "guard", "#!/bin/sh\nexit 78\n")
	mutation := writeExecutable(t, testDir, "mutation", "#!/bin/sh\ntouch "+reached+"\nexit 0\n")
	bootstrap := strings.NewReplacer(
		"/usr/local/bin/noisedeck-deployment-host-guard", guard,
		"/usr/local/bin/noisedeck-provision", mutation,
		"/opt/mempalace/venv/bin/python", mutation,
		"/opt/mempalace/venv/bin/mempalace-mcp", mutation,
		"/usr/local/bin/noise-deck", mutation,
	).Replace(string(raw))
	bootstrapPath := filepath.Join(testDir, "bootstrap")
	if err := os.WriteFile(bootstrapPath, []byte(bootstrap), 0o555); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("/bin/sh", bootstrapPath)
	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &out
	err = cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 78 {
		t.Fatalf("bootstrap exit = %v, want 78; output: %s", err, out.String())
	}
	if _, err := os.Stat(reached); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("bootstrap reached a mutating command after guard failure: %v", err)
	}
}

func writeExecutable(t *testing.T, dir, name, contents string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(contents), 0o555); err != nil {
		t.Fatal(err)
	}
	return path
}
