//go:build linux

package workspacehost

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/noisefloor-oss/deck/internal/auth"
	"github.com/noisefloor-oss/deck/internal/workspacegateway"
	"github.com/noisefloor-oss/deck/internal/workspacekeys"
	"github.com/noisefloor-oss/deck/internal/workspaceruntime"
)

func hostConfig(t *testing.T) Config {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("production gateway refuses root")
	}
	root, err := os.MkdirTemp("/tmp", "deck-host-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	for _, name := range []string{"cipher", "mount", "control", "run"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	cfg := Config{Bind: "127.0.0.1:18443", Origin: "https://workspace.example.test", GoogleClient: "fixture-client", AllowedEmail: "owner@example.test",
		Native: workspaceruntime.Config{InstanceID: "installation", OwnerUID: os.Geteuid(), CipherDir: filepath.Join(root, "cipher"), MountPoint: filepath.Join(root, "mount"),
			ControlDir: filepath.Join(root, "control"), RuntimeDir: filepath.Join(root, "run"), FusermountPath: "/usr/bin/fusermount3", WorkspaceGateway: true}}
	cfg.Auth = auth.Config{Mode: "oidc", ClientID: cfg.GoogleClient, ClientSecret: "fixture-google-secret", AllowedEmail: cfg.AllowedEmail,
		RedirectURL: cfg.Origin + "/auth/callback", SessionSecret: bytes.Repeat([]byte{7}, 32), SecureCookies: true}
	return cfg
}

func TestGatewayConfigRefusesBypassesBeforeBinding(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Config)
	}{
		{"public bind", func(c *Config) { c.Bind = "0.0.0.0:18443" }},
		{"dns bind", func(c *Config) { c.Bind = "localhost:18443" }},
		{"ephemeral port", func(c *Config) { c.Bind = "127.0.0.1:0" }},
		{"root", func(c *Config) { c.Native.OwnerUID = 0 }},
		{"manual mode", func(c *Config) { c.Native.WorkspaceGateway = false }},
		{"no unmount", func(c *Config) { c.Native.FusermountPath = "" }},
		{"http origin", func(c *Config) { c.Origin = "http://workspace.example.test" }},
		{"proxy auth", func(c *Config) { c.Auth.Mode = "trusted_proxy" }},
		{"different account", func(c *Config) { c.Auth.AllowedEmail = "another@example.test" }},
		{"control inside mount", func(c *Config) { c.Native.ControlDir = c.Native.MountPoint }},
		{"public control", func(c *Config) {
			if err := os.Chmod(c.Native.ControlDir, 0755); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := hostConfig(t)
			if cfg.Validate() != nil {
				t.Fatal("fixture refused")
			}
			tc.change(&cfg)
			if cfg.Validate() == nil {
				t.Fatal("unsafe configuration accepted")
			}
		})
	}
}

func TestConfigLoadRejectsUnknownFieldsWithoutSecretOutput(t *testing.T) {
	cfg := hostConfig(t)
	t.Setenv("NOISEDECK_AUTH_GOOGLE_CLIENT_SECRET", cfg.Auth.ClientSecret)
	t.Setenv("NOISEDECK_AUTH_SESSION_SECRET", string(cfg.Auth.SessionSecret))
	path := filepath.Join(cfg.Native.ControlDir, "gateway.json")
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(cfg.Auth.ClientSecret)) {
		t.Fatal("serialized Google secret")
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Fatal(err)
	}
	raw = append(raw[:len(raw)-1], []byte(`,"unexpected":"private-input-canary"}`)...)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || strings.Contains(err.Error(), "private-input-canary") {
		t.Fatal("unknown input accepted or echoed")
	}
}

func TestInvitationExportsOnlyToPrivateTmpfsWithoutResettingInstallation(t *testing.T) {
	cfg := hostConfig(t)
	runtime, err := os.MkdirTemp("/dev/shm", "deck-invite-")
	if err != nil {
		t.Skip("RAM-backed test directory unavailable")
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtime) })
	if !volatileDirectory(runtime, os.Geteuid()) {
		t.Skip("RAM-backed test directory unavailable")
	}
	cfg.Native.RuntimeDir = runtime
	ctx := context.Background()
	db, err := workspacegateway.OpenControl(cfg.control())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	keyCfg := workspacekeys.Config{InstanceID: cfg.Native.InstanceID, RPID: cfg.Gateway().RPID, Origin: cfg.Origin}
	keys, err := workspacekeys.New(ctx, db, keyCfg)
	if err != nil {
		t.Fatal(err)
	}
	path, expires, err := Invite(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if expires.IsZero() || filepath.Dir(path) != runtime || !privateFile(path, os.Geteuid()) {
		t.Fatal("unsafe invitation export")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	token := strings.TrimSuffix(strings.TrimPrefix(string(raw), cfg.Origin+"/#invite="), "\n")
	if len(token) != 43 {
		t.Fatal("missing canonical one-time link")
	}
	invites, err := workspacegateway.NewInvitations(ctx, db, keys, cfg.AllowedEmail, nil)
	if err != nil {
		t.Fatal(err)
	}
	login := auth.Login{ID: strings.Repeat("a", 64), Issuer: "https://accounts.google.com", Subject: "fixture-owner", Email: cfg.AllowedEmail}
	if _, err := invites.Redeem(ctx, login, token); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Invite(ctx, cfg); err == nil {
		t.Fatal("invitation reset the bound owner")
	}
	var leaked int
	if err := db.QueryRow(`SELECT count(*) FROM workspace_invitation WHERE CAST(hash AS TEXT)=?`, token).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatal("stored invitation bearer", err)
	}
	state, err := keys.State(ctx, login)
	if err != nil || !state.OwnerBound {
		t.Fatal("admin command lost live owner")
	}
	cfg.Native.RuntimeDir = filepath.Dir(cfg.Native.ControlDir)
	if _, _, err := Invite(ctx, cfg); err == nil {
		t.Fatal("invitation exported to durable storage")
	}
}

func TestOptionalConfigurationRequiresSeparateInstalledPlainRuntime(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Config)
	}{
		{"missing plain controller", func(c *Config) { c.Plain = nil }},
		{"different owner", func(c *Config) { c.Plain.OwnerUID++ }},
		{"different instance", func(c *Config) { c.Plain.InstanceID = "another" }},
		{"different origin", func(c *Config) { c.Plain.PublicOrigin = "https://another.example.test" }},
		{"cipher root reused", func(c *Config) { c.Plain.StateRoot = c.Native.CipherDir }},
		{"mount root reused", func(c *Config) { c.Plain.StateRoot = c.Native.MountPoint }},
		{"compose project reused", func(c *Config) { c.Native.ComposeProject = c.Plain.ComposeProject }},
		{"app container reused", func(c *Config) { c.Native.AppContainer = c.Plain.AppContainer }},
		{"required with plain controller", func(c *Config) { c.EncryptionPolicy = workspacekeys.EncryptionRequired }},
		{"unknown policy", func(c *Config) { c.EncryptionPolicy = "fallback" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := plainHostConfig(t, workspacekeys.EncryptionOptional)
			requireOK(t, cfg.Validate())
			tc.change(&cfg)
			if cfg.Validate() == nil {
				t.Fatal("incompatible storage configuration accepted")
			}
		})
	}
	// Lack of encryption is an explicit installation policy, not a failed
	// encrypted observation. No encrypted executable or mount is required.
	requireOK(t, plainHostConfig(t, workspacekeys.EncryptionUnavailable).Validate())
}
