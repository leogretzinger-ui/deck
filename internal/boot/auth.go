package boot

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/noisefloor-oss/deck/internal/auth"
	"github.com/noisefloor-oss/deck/internal/config"
)

func installationStateExists(databasePath string) (bool, error) {
	for _, path := range []string{databasePath, filepath.Join(filepath.Dir(databasePath), "auth-state.json")} {
		if _, err := os.Lstat(path); err == nil {
			return true, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	}
	return false, nil
}

// authConfig maps the validated application config onto the auth package's
// input. SessionSecret becomes bytes; the rest is a straight copy.
func authConfig(cfg *config.Config) auth.Config {
	return auth.Config{
		Mode:           cfg.Auth.Mode,
		ClientID:       cfg.Auth.Google.ClientID,
		ClientSecret:   cfg.Auth.Google.ClientSecret,
		RedirectURL:    cfg.Auth.Google.RedirectURL,
		AllowedEmail:   cfg.Auth.Google.AllowedEmail,
		SessionSecret:  []byte(cfg.Auth.SessionSecret),
		PasswordHash:   []byte(cfg.Auth.PasswordHash),
		SecureCookies:  cfg.Auth.SecureCookies,
		ProxyAddresses: cfg.Auth.TrustedProxy.Addresses,
		IdentityHeader: cfg.Auth.TrustedProxy.IdentityHeader,
	}
}
