package session

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/noisefloor-oss/deck/internal/provider"
)

func codexLoginHelperHint(launchEnvironment []string, workDir string) bool {
	path, ok := codexAuthMetadataPath(launchEnvironment, workDir)
	return ok && codexAuthMetadataMissing(path)
}

func TestCodexLoginHintUsesOnlyLaunchMetadata(t *testing.T) {
	work := t.TempDir()
	home := filepath.Join(work, "home")
	custom := filepath.Join(work, "custom")
	for _, dir := range []string{filepath.Join(home, ".codex"), custom} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	// Deliberately invalid and unreadable: presence is merely a suppression
	// hint and must not parse, validate, or expose credential contents.
	if err := os.WriteFile(filepath.Join(home, ".codex", "auth.json"), []byte("not credential JSON"), 0000); err != nil {
		t.Fatal(err)
	}
	base := []string{"/usr/bin/env", "-i", "HOME=" + home, "NOISEDECK_LOGIN_PROVIDER=codex", "NOISEDECK_LOGIN_SOCKET=/fixture/socket"}
	for _, tc := range []struct {
		name string
		env  []string
		want bool
	}{
		{"present metadata suppresses", base, false},
		{"custom home missing auth stays unknown", append(append([]string{}, base...), "CODEX_HOME="+custom), true},
		{"last explicit override wins", append(append([]string{}, base...), "CODEX_HOME="+custom, "CODEX_HOME="+filepath.Join(home, ".codex")), false},
		{"relative home uses launch directory", append(append([]string{}, base...), "CODEX_HOME=home/.codex"), false},
		{"unknown home offers guidance", []string{"NOISEDECK_LOGIN_PROVIDER=codex", "NOISEDECK_LOGIN_SOCKET=/fixture/socket"}, true},
		{"unsupported native launcher", []string{"HOME=" + home}, false},
		{"other harness", []string{"NOISEDECK_LOGIN_PROVIDER=pi", "NOISEDECK_LOGIN_SOCKET=/fixture/socket"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := codexLoginHelperHint(tc.env, work); got != tc.want {
				t.Fatalf("guidance hint = %v, want %v", got, tc.want)
			}
		})
	}
	if err := os.Mkdir(filepath.Join(custom, "auth.json"), 0700); err != nil {
		t.Fatal(err)
	}
	if !codexLoginHelperHint(append(base, "CODEX_HOME="+custom), work) {
		t.Fatal("a directory named auth.json must not suppress guidance")
	}
}

func TestNewCodexLoginHintIsTransientAndUsesLaunchOverrides(t *testing.T) {
	desc := sleeperDescriptor()
	desc.name = provider.Codex
	desc.permittedEnv = append(desc.permittedEnv, "CODEX_HOME")
	mgr, repo, _, projectID := newTestEnv(t, desc)
	oldHome := t.TempDir()
	if err := os.WriteFile(filepath.Join(oldHome, "auth.json"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", oldHome)
	newHome := t.TempDir()
	mgr.SetHarnessLoginEnvironment(func(context.Context, provider.Descriptor) []string {
		return []string{"NOISEDECK_LOGIN_PROVIDER=codex", "NOISEDECK_LOGIN_SOCKET=/fixture/socket", "CODEX_HOME=" + newHome}
	})
	created, err := mgr.Create(context.Background(), CreateSpec{ProjectID: projectID, Provider: provider.Codex, WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if !created.CodexLoginHelper {
		t.Fatal("new session ignored effective launch override")
	}
	stored, err := repo.Get(context.Background(), created.ID)
	if err != nil || stored.CodexLoginHelper {
		t.Fatal("first-login guidance became durable authentication state")
	}
}

func TestCodexLoginHintFollowsAuthMetadataOnEveryRead(t *testing.T) {
	desc := sleeperDescriptor()
	desc.name = provider.Codex
	mgr, repo, _, projectID := newTestEnv(t, desc)
	work := t.TempDir()
	mgr.SetHarnessLoginEnvironment(func(context.Context, provider.Descriptor) []string {
		return []string{"NOISEDECK_LOGIN_PROVIDER=codex", "NOISEDECK_LOGIN_SOCKET=/fixture/socket", "CODEX_HOME=codex-home"}
	})
	ctx := context.Background()
	created, err := mgr.Create(ctx, CreateSpec{ProjectID: projectID, Provider: provider.Codex, WorkDir: work})
	if err != nil {
		t.Fatal(err)
	}
	present := func() *Session {
		t.Helper()
		stored, err := repo.Get(ctx, created.ID)
		if err != nil {
			t.Fatal(err)
		}
		presented, err := mgr.Present(ctx, stored)
		if err != nil {
			t.Fatal(err)
		}
		return presented
	}
	if s := present(); s.Status != StatusRunning || !s.CodexLoginHelper {
		t.Fatalf("running session without auth metadata: status %s, hint %v", s.Status, s.CodexLoginHelper)
	}
	// A relative CODEX_HOME resolves against the launch directory, not the
	// server's working directory.
	if err := os.MkdirAll(filepath.Join(work, "codex-home"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "codex-home", "auth.json"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	s := present()
	if s.CodexLoginHelper {
		t.Fatal("auth metadata written after launch left the hint on")
	}
	if err := os.Remove(filepath.Join(work, "codex-home", "auth.json")); err != nil {
		t.Fatal(err)
	}
	s = present()
	for _, tc := range []struct {
		name   string
		change func(*Session)
	}{
		{"other harness", func(s *Session) { s.Provider = string(provider.Claude) }},
		{"not running", func(s *Session) { s.Status = StatusExited }},
		{"other generation", func(s *Session) { s.Generation++ }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := *s
			tc.change(&changed)
			if mgr.codexLoginHelper(&changed) {
				t.Fatal("hint reported outside the launch it was recorded for")
			}
		})
	}
	if !mgr.codexLoginHelper(s) {
		t.Fatal("control: the recorded running generation still offers guidance")
	}
}
