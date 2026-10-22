package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"filippo.io/age"

	"github.com/noisefloor-oss/deck/internal/config"
	"github.com/noisefloor-oss/deck/internal/db"
)

// runBackup implements `noisedeck backup -config <path> -dest <path>`, the
// online half of the supported backup/restore procedure. It is safe against a
// running backend: db.Backup reads through the WAL, so the service does not
// stop and no committed row still in the sidecar is lost.
//
// The command deliberately does not take the exclusive backend lock — the
// lock exists to refuse a second backend, and a backup reader is not one. It
// opens the source read-only via db.OpenBackupSource, which requires an
// existing database and verifies schema compatibility without creating or
// migrating, so a mistyped path is an error rather than a fresh empty backup.
// Restore is the reverse operation and is the `noisedeck restore` subcommand
// (docs/backup-restore.md): it fences the tmux namespace and installs the
// backup before the next boot opens it.
func runBackup(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "path to config file")
	dest := fs.String("dest", "", "backup destination path (must not already exist)")
	recipientFlag := fs.String("recipient", "", "age X25519 public key to encrypt the backup to")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dest == "" {
		return errors.New("-dest is required")
	}
	var recipient age.Recipient
	if *recipientFlag != "" {
		r, err := age.ParseX25519Recipient(*recipientFlag)
		if err != nil {
			return fmt.Errorf("-recipient: %w", err)
		}
		recipient = r
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}

	ctx := context.Background()
	d, err := db.OpenBackupSource(ctx, db.Config{
		Path:        cfg.Database.Path,
		BusyTimeout: cfg.Database.BusyTimeout.Std(),
	})
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()

	if recipient == nil {
		// Said once, before the file exists, so a user who meant to
		// encrypt learns it before the copy leaves the host.
		writeln(stderr, "backup is plaintext; pass -recipient <age public key> to encrypt it")
		if err := d.Backup(ctx, *dest); err != nil {
			return err
		}
		writef(stdout, "backup written to %s\n", *dest)
		return nil
	}
	if err := d.BackupSealed(ctx, *dest, recipient); err != nil {
		return err
	}
	writef(stdout, "sealed backup written to %s\n", *dest)
	return nil
}
