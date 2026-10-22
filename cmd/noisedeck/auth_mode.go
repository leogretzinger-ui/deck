package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/noisefloor-oss/deck/internal/auth"
	"github.com/noisefloor-oss/deck/internal/config"
	"github.com/noisefloor-oss/deck/internal/lock"
)

func runAuthMode(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printAuthModeUsage(stderr)
		return 2
	}
	switch args[0] {
	case "adopt":
		return runAuthModeAdopt(args[1:], stdout, stderr)
	case "transition":
		return runAuthModeTransition(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		printAuthModeUsage(stdout)
		return 0
	default:
		writef(stderr, "noise deck auth-mode: unknown command %q\n", args[0])
		printAuthModeUsage(stderr)
		return 2
	}
}

func printAuthModeUsage(w io.Writer) {
	writeln(w, "usage: noise deck auth-mode <command> [args...]")
	writeln(w, "\ncommands:")
	writeln(w, "  adopt       record the mode of a pre-receipt installation")
	writeln(w, "  transition  change an installation's recorded mode while offline")
}

func authModeFlags(name string, stderr io.Writer) (*flag.FlagSet, *string) {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	return flags, flags.String("config", "", "path to config file")
}

func runAuthModeAdopt(args []string, stdout, stderr io.Writer) int {
	flags, configPath := authModeFlags("noise deck auth-mode adopt", stderr)
	mode := flags.String("mode", "", "existing installation mode")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *mode == "" {
		writeln(stderr, "noise deck auth-mode adopt: --mode is required and positional arguments are not accepted")
		return 2
	}
	cfg, err := config.LoadForAuthModeOperation(*configPath, *mode)
	if err != nil {
		writef(stderr, "noise deck auth-mode adopt: %v\n", err)
		return 1
	}
	if err := withOfflineAuthModeLock(cfg, func(existing bool) error {
		return auth.AdoptInstallationMode(auth.InstallationModePath(cfg.Database.Path), cfg.Auth.Mode, existing)
	}); err != nil {
		writef(stderr, "noise deck auth-mode adopt: %v\n", err)
		return 1
	}
	writef(stdout, "adopted installation auth mode %s\n", cfg.Auth.Mode)
	return 0
}

func runAuthModeTransition(args []string, stdout, stderr io.Writer) int {
	flags, configPath := authModeFlags("noise deck auth-mode transition", stderr)
	from := flags.String("from", "", "currently recorded mode")
	to := flags.String("to", "", "new configured mode")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *from == "" || *to == "" {
		writeln(stderr, "noise deck auth-mode transition: --from and --to are required and positional arguments are not accepted")
		return 2
	}
	cfg, err := config.LoadForAuthModeOperation(*configPath, *to)
	if err != nil {
		writef(stderr, "noise deck auth-mode transition: %v\n", err)
		return 1
	}
	if err := withOfflineAuthModeLock(cfg, func(existing bool) error {
		return auth.TransitionInstallationMode(auth.InstallationModePath(cfg.Database.Path), *from, cfg.Auth.Mode)
	}); err != nil {
		writef(stderr, "noise deck auth-mode transition: %v\n", err)
		return 1
	}
	writef(stdout, "transitioned installation auth mode from %s to %s\n", *from, cfg.Auth.Mode)
	return 0
}

func withOfflineAuthModeLock(cfg *config.Config, operation func(existing bool) error) error {
	if err := os.MkdirAll(filepath.Dir(cfg.Database.LockPath), 0o700); err != nil {
		return fmt.Errorf("create lock directory: %w", err)
	}
	guard, err := lock.Acquire(cfg.Database.LockPath)
	if err != nil {
		return fmt.Errorf("auth mode operation must be performed while Deck is stopped: %w", err)
	}
	defer func() { _ = guard.Release() }()
	existing, err := installationStatePresent(cfg.Database.Path)
	if err != nil {
		return err
	}
	return operation(existing)
}

func installationStatePresent(databasePath string) (bool, error) {
	for _, path := range []string{databasePath, filepath.Join(filepath.Dir(databasePath), "auth-state.json")} {
		if _, err := os.Lstat(path); err == nil {
			return true, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, fmt.Errorf("inspect installation state: %w", err)
		}
	}
	return false, nil
}
