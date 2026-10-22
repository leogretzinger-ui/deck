//go:build linux

package harnesslogin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeBrowserFallbackRunsWithoutDeckLoginEnvironment(t *testing.T) {
	dir := t.TempDir()
	opener := filepath.Join(dir, "original")
	marker := filepath.Join(dir, "called")
	// Marker bytes establish execution without recording any URL or arguments.
	script := "#!/bin/sh\n[ -z \"$NOISEDECK_LOGIN_SOCKET\" ] && [ -z \"$BROWSER\" ] && [ \"$PATH\" = /fixture/path ] || exit 1\nprintf ran > '" + strings.ReplaceAll(marker, "'", "'\\''") + "'\n"
	if err := os.WriteFile(opener, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NOISEDECK_LOGIN_SOCKET", filepath.Join(dir, "unavailable.sock"))
	t.Setenv("NOISEDECK_LOGIN_ORIGINAL_OPENER", opener)
	t.Setenv("NOISEDECK_LOGIN_ORIGINAL_PATH", "/fixture/path")
	t.Setenv("BROWSER", "/fixture/deck-opener")
	if BrowserOpener([]string{"https://example.invalid/native-device-login"}) != 0 {
		t.Fatal("native browser fallback failed")
	}
	if b, err := os.ReadFile(marker); err != nil || string(b) != "ran" {
		t.Fatal("original opener did not run")
	}
}

func TestLocalRegistrationNeverReplacesAnOrdinaryFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registration.sock")
	if err := os.WriteFile(path, []byte("existing-data"), 0600); err != nil {
		t.Fatal(err)
	}
	if s, err := StartLocal(path, New(nil)); err == nil {
		s.Close()
		t.Fatal("ordinary file was replaced")
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != "existing-data" {
		t.Fatal("ordinary file changed")
	}
}
