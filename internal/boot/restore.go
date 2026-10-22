package boot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"filippo.io/age"
	"github.com/rs/zerolog"

	"github.com/noisefloor-oss/deck/internal/config"
	"github.com/noisefloor-oss/deck/internal/db"
	"github.com/noisefloor-oss/deck/internal/lock"
	"github.com/noisefloor-oss/deck/internal/tmux"
)

// restoredDBFileMode matches the control database: owner-only, since a
// restored database carries the same fencing tokens and session records as
// the live one.
const restoredDBFileMode os.FileMode = 0o600

// RestoreSealed is Restore for a backup that may be age-encrypted. With
// identities, an encrypted backup is decrypted into an owner-only staging
// file beside the live database, under the same state root, and that file is
// verified exactly as a plain backup would be before anything is fenced or
// replaced. A wrong identity or an altered ciphertext leaves the host
// untouched. Without identities an encrypted backup is refused by name, so
// a forgotten flag never presents ciphertext as an incompatible database.
func RestoreSealed(ctx context.Context, cfg *config.Config, backupPath string, identities []age.Identity, log zerolog.Logger) error {
	if strings.TrimSpace(backupPath) == "" {
		return fmt.Errorf("boot: restore: backup path is required")
	}
	abs, err := filepath.Abs(backupPath)
	if err != nil {
		return fmt.Errorf("boot: restore: resolve %q: %w", backupPath, err)
	}

	// Maintenance must refuse while a backend owns the namespace, and hold
	// the lock so a backend cannot start mid-restore.
	held, err := lock.Acquire(cfg.Database.LockPath)
	if err != nil {
		return fmt.Errorf("boot: restore: %w", err)
	}
	defer func() { _ = held.Release() }()

	sealed, err := db.IsSealedBackup(abs)
	if err != nil {
		return fmt.Errorf("boot: restore: read backup %q: %w", abs, err)
	}
	if sealed && len(identities) == 0 {
		return fmt.Errorf("boot: restore: backup %q is encrypted; pass the age identity that can open it", abs)
	}
	if sealed {
		dbDir := filepath.Dir(cfg.Database.Path)
		if err := os.MkdirAll(dbDir, 0o700); err != nil {
			return fmt.Errorf("boot: restore: create database directory: %w", err)
		}
		unsealed, err := db.Unseal(abs, dbDir, identities...)
		if err != nil {
			return fmt.Errorf("boot: restore: %w", err)
		}
		defer func() { _ = os.Remove(unsealed) }()
		log.Info().Str("backup", abs).Msg("restore unsealed backup beside the database")
		abs = unsealed
	}

	// Verify the backup before fencing anything. An incompatible or missing
	// backup leaves the host untouched.
	source, err := db.OpenBackupSource(ctx, db.Config{
		Path:        abs,
		BusyTimeout: cfg.Database.BusyTimeout.Std(),
	})
	if err != nil {
		return fmt.Errorf("boot: restore: %w", err)
	}
	if err := source.Close(); err != nil {
		return fmt.Errorf("boot: restore: close backup source: %w", err)
	}

	// Empty the old database's WAL before anything is replaced. Every
	// committed row then lives in the main file and no sidecar can be replayed
	// over the restored one, so a crash before the rename below leaves the
	// complete old database and a crash after it the complete restored one.
	if err := emptyOldWAL(ctx, cfg, log); err != nil {
		return err
	}
	atBoundary("checkpointed")

	// Fence the whole namespace: killing the server on the dedicated socket
	// terminates every session and workload in it — including ones started
	// after the backup that no restored row accounts for.
	tmuxClient, err := tmux.NewClient(tmux.Config{
		SocketPath:      cfg.Tmux.SocketPath,
		Timeout:         10 * time.Second,
		ScrollbackLines: cfg.Tmux.ScrollbackLines,
	})
	if err != nil {
		return fmt.Errorf("boot: restore: %w", err)
	}
	if err := tmuxClient.KillServer(ctx); err != nil {
		return fmt.Errorf("boot: restore: fence tmux namespace: %w", err)
	}
	log.Info().Str("socket", cfg.Tmux.SocketPath).Msg("restore fenced tmux namespace")

	// Install the backup atomically and owner-only: copy into a temp file in
	// the database's directory, then rename over the current database. Both
	// paths share one local filesystem, so the rename is atomic.
	dbDir := filepath.Dir(cfg.Database.Path)
	if err := os.MkdirAll(dbDir, 0o700); err != nil {
		return fmt.Errorf("boot: restore: create database directory: %w", err)
	}
	tmp, err := os.CreateTemp(dbDir, ".restore-*")
	if err != nil {
		return fmt.Errorf("boot: restore: create staging file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }() // no-op once renamed into place

	src, err := os.Open(abs)
	if err != nil {
		_ = tmp.Close()
		return fmt.Errorf("boot: restore: open backup %q: %w", abs, err)
	}
	if err := tmp.Chmod(restoredDBFileMode); err != nil {
		_ = src.Close()
		_ = tmp.Close()
		return fmt.Errorf("boot: restore: restrict staging file: %w", err)
	}
	if _, err := io.Copy(tmp, src); err != nil {
		_ = src.Close()
		_ = tmp.Close()
		return fmt.Errorf("boot: restore: copy backup: %w", err)
	}
	_ = src.Close()
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("boot: restore: sync staging file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("boot: restore: close staging file: %w", err)
	}
	if err := os.Rename(tmpPath, cfg.Database.Path); err != nil {
		return fmt.Errorf("boot: restore: install database: %w", err)
	}
	atBoundary("installed")
	log.Info().Str("path", cfg.Database.Path).Str("backup", abs).Msg("restore installed database")
	return nil
}

// restoreBoundary lets a test interrupt RestoreSealed at a named step.
var restoreBoundary func(name string)

func atBoundary(name string) {
	if restoreBoundary != nil {
		restoreBoundary(name)
	}
}

// emptyOldWAL leaves the current database file with no sidecars. A readable
// database is checkpointed, and a busy one refuses the restore. A missing or
// unreadable one has nothing to checkpoint, so its sidecars are removed: left
// beside the restored file, SQLite would replay them over it.
func emptyOldWAL(ctx context.Context, cfg *config.Config, log zerolog.Logger) error {
	err := db.CheckpointTruncate(ctx, db.Config{
		Path:        cfg.Database.Path,
		BusyTimeout: cfg.Database.BusyTimeout.Std(),
	})
	switch {
	case err == nil:
		return nil
	case errors.Is(err, db.ErrUnreadable):
		log.Warn().Err(err).Str("path", cfg.Database.Path).Msg("restore: old database was unreadable; discarding its sidecars")
	case !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("boot: restore: checkpoint current database: %w", err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Remove(cfg.Database.Path + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("boot: restore: remove stale %s file: %w", suffix, err)
		}
	}
	return nil
}
