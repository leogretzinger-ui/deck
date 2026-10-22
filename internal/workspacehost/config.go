// Package workspacehost assembles the trusted, always-on workspace gateway.
// The encrypted workload and its real CLIs remain separate native processes.
package workspacehost

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/noisefloor-oss/deck/internal/auth"
	"github.com/noisefloor-oss/deck/internal/workspacegateway"
	"github.com/noisefloor-oss/deck/internal/workspacekeys"
	"github.com/noisefloor-oss/deck/internal/workspaceplain"
	"github.com/noisefloor-oss/deck/internal/workspaceruntime"
)

var ErrConfiguration = errors.New("workspace gateway configuration refused")

type Config struct {
	Bind             string                         `json:"bind"`
	Origin           string                         `json:"origin"`
	GoogleClient     string                         `json:"google_client_id"`
	AllowedEmail     string                         `json:"allowed_email"`
	EncryptionPolicy workspacekeys.EncryptionPolicy `json:"encryption_policy,omitempty"`
	Native           workspaceruntime.Config        `json:"native"`
	Plain            *workspaceplain.Config         `json:"plain,omitempty"`
	Auth             auth.Config                    `json:"-"`
}

// Load uses a separate fixed installation file so the locked gateway need not
// read workload configuration, provider homes, or an encrypted database.
func Load(path string) (Config, error) {
	if !canonical(path) || !privateFile(path, os.Geteuid()) {
		return Config{}, ErrConfiguration
	}
	// #nosec G304 -- Fixed admin-owned, canonical, owner-only installation file.
	file, err := os.Open(path)
	if err != nil {
		return Config{}, ErrConfiguration
	}
	defer func() { _ = file.Close() }()
	decoder := json.NewDecoder(io.LimitReader(file, 64*1024+1))
	decoder.DisallowUnknownFields()
	var cfg Config
	if decoder.Decode(&cfg) != nil || decoder.Decode(new(any)) != io.EOF {
		return Config{}, ErrConfiguration
	}
	cfg.Auth = auth.Config{Mode: "oidc", ClientID: cfg.GoogleClient,
		ClientSecret:  os.Getenv("NOISEDECK_AUTH_GOOGLE_CLIENT_SECRET"),
		SessionSecret: []byte(os.Getenv("NOISEDECK_AUTH_SESSION_SECRET")),
		AllowedEmail:  cfg.AllowedEmail, RedirectURL: cfg.Origin + "/auth/callback", SecureCookies: true}
	if cfg.Validate() != nil || nested(path, cfg.Native.MountPoint) || nested(path, cfg.Native.CipherDir) ||
		(cfg.Plain != nil && nested(path, cfg.Plain.StateRoot)) {
		return Config{}, ErrConfiguration
	}
	return cfg, nil
}

func canonical(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && !strings.ContainsAny(path, "\x00\r\n")
}

func (c Config) Gateway() workspacegateway.Config {
	u, _ := url.Parse(c.Origin)
	rpid := ""
	if u != nil {
		rpid = u.Hostname()
	}
	return workspacegateway.Config{InstanceID: c.Native.InstanceID, Origin: c.Origin, RPID: rpid, Auth: c.Auth}
}

func (c Config) Socket() string {
	return filepath.Join(c.Native.MountPoint, "data", "gateway", "backend.sock")
}
func (c Config) control() string { return filepath.Join(c.Native.ControlDir, "workspace.sqlite") }

func (c Config) policy() workspacekeys.EncryptionPolicy {
	if c.EncryptionPolicy == "" {
		return workspacekeys.EncryptionRequired
	}
	return c.EncryptionPolicy
}

func (c Config) Validate() error {
	host, port, err := net.SplitHostPort(c.Bind)
	ip := net.ParseIP(host)
	p, portErr := strconv.Atoi(port)
	if err != nil || ip == nil || !ip.IsLoopback() || portErr != nil || p < 1 || p > 65535 ||
		runtime.GOOS != "linux" || c.Native.OwnerUID <= 0 || c.Native.OwnerUID != os.Geteuid() ||
		!c.Native.WorkspaceGateway || len(c.Socket()) > 103 ||
		c.Auth.ClientID != c.GoogleClient || c.Auth.AllowedEmail != c.AllowedEmail || c.Gateway().Validate() != nil {
		return ErrConfiguration
	}
	policy := c.policy()
	if policy != workspacekeys.EncryptionRequired && policy != workspacekeys.EncryptionOptional && policy != workspacekeys.EncryptionUnavailable {
		return ErrConfiguration
	}
	if c.Native.Migration != nil && policy != workspacekeys.EncryptionRequired {
		return ErrConfiguration
	}
	paths := []string{c.Native.ControlDir, c.Native.RuntimeDir}
	if policy != workspacekeys.EncryptionUnavailable {
		if c.Native.FusermountPath == "" {
			return ErrConfiguration
		}
		paths = append(paths, c.Native.MountPoint, c.Native.CipherDir)
	}
	for _, path := range paths {
		if !canonical(path) || !privateDirectory(path, c.Native.OwnerUID) {
			return ErrConfiguration
		}
	}
	if policy != workspacekeys.EncryptionRequired {
		plain := c.Plain
		if plain == nil || workspaceplain.ValidateConfig(*plain) != nil ||
			plain.InstanceID != c.Native.InstanceID || plain.OwnerUID != c.Native.OwnerUID ||
			plain.ControlDir != c.Native.ControlDir || plain.RuntimeDir != c.Native.RuntimeDir ||
			plain.PublicOrigin != c.Origin || len(filepath.Join(plain.StateRoot, "data", "gateway", "backend.sock")) > 103 {
			return ErrConfiguration
		}
		for _, root := range []string{c.Native.MountPoint, c.Native.CipherDir} {
			for _, path := range []string{plain.StateRoot, plain.HomeDir, plain.PodmanPath, plain.ComposePath, plain.ComposeEnvFile} {
				if root != "" && (nested(path, root) || nested(root, path)) {
					return ErrConfiguration
				}
			}
		}
		if policy == workspacekeys.EncryptionOptional &&
			(plain.ComposeProject == c.Native.ComposeProject || plain.AppContainer == c.Native.AppContainer) {
			return ErrConfiguration
		}
	} else if c.Plain != nil {
		return ErrConfiguration
	}
	for _, pair := range [][2]string{{c.Native.ControlDir, c.Native.MountPoint}, {c.Native.ControlDir, c.Native.CipherDir},
		{c.Native.RuntimeDir, c.Native.MountPoint}, {c.Native.RuntimeDir, c.Native.CipherDir}} {
		if pair[1] == "" {
			continue
		}
		if nested(pair[0], pair[1]) || nested(pair[1], pair[0]) {
			return ErrConfiguration
		}
	}
	return nil
}

func nested(path, root string) bool {
	return root != "" && (path == root || strings.HasPrefix(path, root+string(filepath.Separator)))
}
