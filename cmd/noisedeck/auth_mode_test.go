package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/noisefloor-oss/deck/internal/auth"
	"github.com/noisefloor-oss/deck/internal/config"
	"github.com/noisefloor-oss/deck/internal/db"
	"github.com/noisefloor-oss/deck/internal/lock"
)

func TestAuthModeAdoptPreservesPreReceiptOIDCInstallation(t *testing.T) {
	data := configureAuthModeCommand(t)
	if err := os.Unsetenv("NOISEDECK_AUTH_MODE"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NOISEDECK_AUTH_SESSION_SECRET", strings.Repeat("s", 32))
	t.Setenv("NOISEDECK_AUTH_GOOGLE_CLIENT_ID", "operator.apps.googleusercontent.com")
	t.Setenv("NOISEDECK_AUTH_GOOGLE_CLIENT_SECRET", "client-secret")
	t.Setenv("NOISEDECK_AUTH_GOOGLE_REDIRECT_URL", "https://deck.example/auth/callback")
	t.Setenv("NOISEDECK_AUTH_GOOGLE_ALLOWED_EMAIL", "operator@example.com")
	t.Setenv("NOISEDECK_SERVER_ALLOWED_ORIGINS", "https://deck.example")
	// Current main is the last released pre-receipt deployment. Materialize
	// that real schema while deliberately leaving the new receipt absent.
	legacy, err := db.Open(context.Background(), db.Config{Path: filepath.Join(data, "control.db")})
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(""); err == nil || !strings.Contains(err.Error(), "refuses OIDC-only configuration") {
		t.Fatalf("pre-receipt legacy OIDC env silently fell back instead of refusing: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if code := run([]string{"auth-mode", "adopt", "--mode", "oidc"}, &stdout, &stderr); code != 0 {
		t.Fatalf("adopt code = %d, stderr = %q", code, stderr.String())
	}
	if got, err := auth.ReadInstallationMode(filepath.Join(data, "auth-mode.json")); err != nil || got != "oidc" {
		t.Fatalf("adopted mode = %q, %v", got, err)
	}
	if err := auth.EnsureInstallationMode(filepath.Join(data, "auth-mode.json"), "password", true); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("post-adoption password fallback was not fenced: %v", err)
	}
	if err := auth.EnsureInstallationMode(filepath.Join(data, "auth-mode.json"), "oidc", true); err != nil {
		t.Fatalf("post-adoption intended OIDC mode was not preserved: %v", err)
	}
	if got := stdout.String(); !strings.Contains(got, "oidc") {
		t.Fatalf("adopt output = %q", got)
	}
}

func TestAuthModeTransitionRequiresOfflineLockAndExactSource(t *testing.T) {
	data := configureAuthModeCommand(t)
	t.Setenv("NOISEDECK_AUTH_MODE", "password")
	if err := os.WriteFile(filepath.Join(data, "control.db"), []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(data, "auth-mode.json")
	if err := auth.AdoptInstallationMode(path, "oidc", true); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := run([]string{"auth-mode", "transition", "--from", "password", "--to", "password"}, &stdout, &stderr); code != 1 {
		t.Fatalf("wrong-source transition code = %d", code)
	}
	if got, _ := auth.ReadInstallationMode(path); got != "oidc" {
		t.Fatalf("failed transition changed mode to %q", got)
	}

	guard, err := lock.Acquire(filepath.Join(data, "backend.lock"))
	if err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"auth-mode", "transition", "--from", "oidc", "--to", "password"}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "while Deck is stopped") {
		t.Fatalf("online transition code = %d, stderr = %q", code, stderr.String())
	}
	if err := guard.Release(); err != nil {
		t.Fatal(err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"auth-mode", "transition", "--from", "oidc", "--to", "password"}, &stdout, &stderr); code != 0 {
		t.Fatalf("transition code = %d, stderr = %q", code, stderr.String())
	}
	if got, err := auth.ReadInstallationMode(path); err != nil || got != "password" {
		t.Fatalf("transitioned mode = %q, %v", got, err)
	}
}

func configureAuthModeCommand(t *testing.T) string {
	t.Helper()
	data := t.TempDir()
	t.Setenv("NOISEDECK_PROJECTS_ALLOWED_ROOTS", t.TempDir())
	t.Setenv("NOISEDECK_DATABASE_PATH", filepath.Join(data, "control.db"))
	t.Setenv("NOISEDECK_DATABASE_LOCK_PATH", filepath.Join(data, "backend.lock"))
	for _, key := range []string{
		"NOISEDECK_AUTH_MODE", "NOISEDECK_AUTH_SESSION_SECRET",
		"NOISEDECK_AUTH_GOOGLE_CLIENT_ID", "NOISEDECK_AUTH_GOOGLE_CLIENT_SECRET",
		"NOISEDECK_AUTH_GOOGLE_REDIRECT_URL", "NOISEDECK_AUTH_GOOGLE_ALLOWED_EMAIL",
	} {
		t.Setenv(key, "")
	}
	return data
}
