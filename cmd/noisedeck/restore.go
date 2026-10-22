package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"filippo.io/age"
	"github.com/rs/zerolog"

	"github.com/noisefloor-oss/deck/internal/boot"
	"github.com/noisefloor-oss/deck/internal/config"
)

// runRestore implements `noisedeck restore -config <path> -from <path>`, the
// only supported way to restore a backup. The backend must be stopped first:
// the command refuses while a backend holds the exclusive lock. It verifies
// the backup's migration ledger matches this binary exactly, fences the entire
// tmux namespace, and installs the backup atomically before the next boot
// opens the restored state.
func runRestore(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "path to config file")
	from := fs.String("from", "", "backup file to restore from")
	identityPath := fs.String("identity", "", "age identity file that opens an encrypted backup")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *from == "" {
		return errors.New("-from is required")
	}
	var identities []age.Identity
	if *identityPath != "" {
		f, err := os.Open(*identityPath)
		if err != nil {
			return fmt.Errorf("-identity: %w", err)
		}
		identities, err = age.ParseIdentities(f)
		_ = f.Close()
		if err != nil {
			return fmt.Errorf("-identity: %w", err)
		}
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}

	// The targets are printed before anything is touched: a restore replaces
	// the configured database and fences the configured tmux namespace, and
	// a person checking a synthetic run reads here which ones those are.
	writef(stdout, "restore target: database %s, lock %s, tmux socket %s\n", cfg.Database.Path, cfg.Database.LockPath, cfg.Tmux.SocketPath)
	if err := boot.RestoreSealed(context.Background(), cfg, *from, identities, zerolog.Nop()); err != nil {
		return err
	}
	writef(stdout, "fenced tmux socket %s; database %s restored from %s; start the service to open it\n", cfg.Tmux.SocketPath, cfg.Database.Path, *from)
	return nil
}
