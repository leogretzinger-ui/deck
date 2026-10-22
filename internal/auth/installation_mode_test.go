package auth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallationModeFreshCreateAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "auth-mode.json")
	if err := EnsureInstallationMode(path, "password", false); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("receipt mode = %04o, want 0600", got)
	}
	if err := EnsureInstallationMode(path, "password", true); err != nil {
		t.Fatalf("matching restart: %v", err)
	}
	if err := EnsureInstallationMode(path, "oidc", true); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("mismatch error = %v", err)
	}
}

func TestInstallationModeExistingInstallRequiresAdoption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth-mode.json")
	if err := EnsureInstallationMode(path, "password", true); err == nil || !strings.Contains(err.Error(), "auth-mode adopt") {
		t.Fatalf("missing receipt error = %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("refusal created receipt: %v", err)
	}
	if err := AdoptInstallationMode(path, "oidc", true); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadInstallationMode(path); err != nil || got != "oidc" {
		t.Fatalf("adopted receipt = %q, %v", got, err)
	}
}

func TestInstallationModeTransitionIsExplicitAndFenced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth-mode.json")
	if err := EnsureInstallationMode(path, "oidc", false); err != nil {
		t.Fatal(err)
	}
	if err := TransitionInstallationMode(path, "password", "trusted_proxy"); err == nil || !strings.Contains(err.Error(), "not requested source") {
		t.Fatalf("wrong-source transition error = %v", err)
	}
	if err := TransitionInstallationMode(path, "oidc", "password"); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadInstallationMode(path); err != nil || got != "password" {
		t.Fatalf("transitioned receipt = %q, %v", got, err)
	}
}

func TestInstallationModeRejectsUnsafeOrNoncanonicalReceipt(t *testing.T) {
	for name, tc := range map[string]struct {
		body string
		mode os.FileMode
	}{
		"alias":       {`{"version":1,"mode":"builtin"}`, 0o600},
		"unknown":     {`{"version":1,"mode":"oidc","extra":true}`, 0o600},
		"permissions": {`{"version":1,"mode":"oidc"}`, 0o644},
		"unreadable":  {`{"version":1,"mode":"oidc"}`, 0o000},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "auth-mode.json")
			if err := os.WriteFile(path, []byte(tc.body), tc.mode); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadInstallationMode(path); err == nil {
				t.Fatal("unsafe receipt accepted")
			}
		})
	}
}
