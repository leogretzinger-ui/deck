package main

import (
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/noisefloor-oss/deck/internal/auth"
	"github.com/noisefloor-oss/deck/internal/config"
)

func runPassword(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "reset" {
		writeln(stderr, "usage: noise-deck password reset [--config <path>]")
		return 2
	}
	flags, path := authModeFlags("noise-deck password reset", stderr)
	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		writeln(stderr, "password reset does not accept a password or positional arguments")
		return 2
	}
	cfg, err := config.Load(*path)
	if err != nil {
		writef(stderr, "password reset: %v\n", err)
		return 1
	}
	if cfg.Auth.Mode != "password" {
		writeln(stderr, "password reset requires password authentication mode")
		return 1
	}
	if cfg.Auth.PasswordHash != "" {
		writeln(stderr, "Remove NOISEDECK_AUTH_PASSWORD_HASH from your service configuration before resetting your password.")
		return 1
	}
	var link string
	err = withOfflineAuthModeLock(cfg, func(_ bool) error {
		mode, err := auth.ReadInstallationMode(auth.InstallationModePath(cfg.Database.Path))
		if err != nil {
			return err
		}
		if mode != "password" {
			return fmt.Errorf("password reset requires a password-mode installation receipt")
		}
		link, err = auth.ResetPassword(filepath.Join(filepath.Dir(cfg.Database.Path), "auth-state.json"), cfg.Server.AllowedOrigins[0], time.Now())
		return err
	})
	if err != nil {
		writef(stderr, "password reset: %v\n", err)
		return 1
	}
	writef(stdout, "Start Deck, then create your password (link expires after 30 minutes): %s\n", link)
	return 0
}
