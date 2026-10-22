package main

import (
	"bytes"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/noisefloor-oss/deck/internal/auth"
	"github.com/noisefloor-oss/deck/internal/lock"
	"golang.org/x/crypto/bcrypt"
)

func passwordInstallation(t *testing.T) (string, []byte) {
	t.Helper()
	data := configureAuthModeCommand(t)
	t.Setenv("NOISEDECK_AUTH_MODE", "password")
	t.Setenv("NOISEDECK_PROJECT_PREVIEWS_UNCONFINED", "false")
	t.Setenv("NOISEDECK_PROJECT_PREVIEWS_LOOPBACK", "false")
	t.Setenv("NOISEDECK_PROJECT_PREVIEWS_TARGETS", "[]")
	for _, key := range []string{"NOISEDECK_PROJECT_PREVIEWS_BIND", "NOISEDECK_PROJECT_PREVIEWS_METADATA_SOCKET"} {
		t.Setenv(key, "")
	}
	t.Setenv("NOISEDECK_AUTH_PASSWORD_HASH", "")
	t.Setenv("NOISEDECK_SERVER_ALLOWED_ORIGINS", "http://127.0.0.1:8837")
	if err := auth.EnsureInstallationMode(filepath.Join(data, "auth-mode.json"), "password", false); err != nil {
		t.Fatal(err)
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte("old password"), bcrypt.MinCost)
	result, err := auth.EnsureBootstrapState(filepath.Join(data, "auth-state.json"), string(hash))
	if err != nil {
		t.Fatal(err)
	}
	return data, result.State.SessionSecret
}

func TestPasswordResetIssuesPrivateLinkAndInvalidatesOldSessions(t *testing.T) {
	data, oldSecret := passwordInstallation(t)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"password", "reset"}, &stdout, &stderr); code != 0 {
		t.Fatalf("reset = %d: %s", code, stderr.String())
	}
	path := filepath.Join(data, "auth-state.json")
	result, err := auth.EnsureBootstrapState(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.State.PasswordHash) != 0 || bytes.Equal(oldSecret, result.State.SessionSecret) {
		t.Fatal("reset retained password or session signing secret")
	}
	raw, err := os.ReadFile(auth.SetupURLPath(path))
	if err != nil {
		t.Fatal(err)
	}
	link := strings.TrimSpace(string(raw))
	parsed, err := url.Parse(link)
	if err != nil || !strings.Contains(parsed.Fragment, "token=") || parsed.RawQuery != "" {
		t.Fatal("reset link does not carry a fragment token")
	}
	if !strings.Contains(stdout.String(), link) || stderr.Len() != 0 {
		t.Fatal("reset did not print its link")
	}
	info, _ := os.Stat(auth.SetupURLPath(path))
	if info.Mode().Perm() != 0o600 {
		t.Fatal("reset URL is not private")
	}
	preserved, err := auth.EnsureSetupURL(path, "http://127.0.0.1:8837", time.Now())
	if err != nil || preserved != link {
		t.Fatal("next startup invalidated the reset link")
	}
	if mode, err := auth.ReadInstallationMode(filepath.Join(data, "auth-mode.json")); err != nil || mode != "password" {
		t.Fatal("reset changed installation mode")
	}
}

func TestPasswordResetRefusesRunningServiceAndPasswordsInArguments(t *testing.T) {
	data, _ := passwordInstallation(t)
	path := filepath.Join(data, "auth-state.json")
	before, _ := os.ReadFile(path)
	guard, err := lock.Acquire(filepath.Join(data, "backend.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = guard.Release() }()
	for _, args := range [][]string{{"password", "reset"}, {"password", "reset", "private-password"}, {"password", "reset", "--password", "private-password"}} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code == 0 {
			t.Fatal("unsafe reset accepted")
		}
		after, _ := os.ReadFile(path)
		if !bytes.Equal(before, after) {
			t.Fatal("refused reset changed password state")
		}
		if stdout.Len() != 0 || strings.Contains(stderr.String(), "private-password") {
			t.Fatal("refused reset disclosed a credential")
		}
	}
}

func TestPasswordResetRefusesOtherModesAndPinnedPasswords(t *testing.T) {
	for _, mode := range []string{"none", "password"} {
		t.Run(mode, func(t *testing.T) {
			data, _ := passwordInstallation(t)
			path := filepath.Join(data, "auth-state.json")
			before, _ := os.ReadFile(path)
			t.Setenv("NOISEDECK_AUTH_MODE", mode)
			if mode == "password" {
				t.Setenv("NOISEDECK_AUTH_PASSWORD_HASH", "pinned")
			}
			var stdout, stderr bytes.Buffer
			if code := run([]string{"password", "reset"}, &stdout, &stderr); code != 1 {
				t.Fatalf("reset = %d", code)
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(before, after) || stdout.Len() != 0 {
				t.Fatal("refused reset changed or disclosed auth state")
			}
		})
	}
}

func TestPasswordResetRequiresOwnedPrivateReceiptAndState(t *testing.T) {
	for _, name := range []string{"auth-mode.json", "auth-state.json"} {
		t.Run(name, func(t *testing.T) {
			data, _ := passwordInstallation(t)
			if err := os.Chmod(filepath.Join(data, name), 0o644); err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			if code := run([]string{"password", "reset"}, &stdout, &stderr); code != 1 || stdout.Len() != 0 {
				t.Fatalf("reset = %d", code)
			}
		})
	}
}

func TestPasswordResetRefusesFilesOwnedByAnotherUser(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("changing fixture ownership requires root")
	}
	for _, name := range []string{"auth-mode.json", "auth-state.json"} {
		t.Run(name, func(t *testing.T) {
			data, _ := passwordInstallation(t)
			path := filepath.Join(data, name)
			if err := os.Chown(path, 65534, -1); err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			if code := run([]string{"password", "reset"}, &stdout, &stderr); code != 1 || stdout.Len() != 0 {
				t.Fatalf("reset = %d", code)
			}
		})
	}
}
