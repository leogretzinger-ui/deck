package db

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// OpenBackupSource opens an EXISTING control database read-only, for backup.
//
// db.Open creates the database and its parent directory when they are absent
// and then applies migrations. That is the right behavior for a backend
// owning its store, and the wrong behavior for a backup reader: a mistyped
// path would back up a fresh empty database as if it were the control store,
// and a newer binary would migrate a live older backend's database out from
// under it. A reader must do neither.
//
// OpenBackupSource therefore requires the path to already exist, opens both
// pools read-only, and verifies that the database's migration ledger exactly
// matches the migrations embedded in THIS binary — every applied row known
// and unmodified, and every embedded migration present in the ledger. A
// foreign file, an empty file, or a database migrated by a different version
// fails here before backup begins.
func OpenBackupSource(ctx context.Context, cfg Config) (*DB, error) {
	cfg = cfg.withDefaults()
	if strings.TrimSpace(cfg.Path) == "" {
		return nil, errors.New("db: Config.Path is required")
	}
	abs, err := filepath.Abs(cfg.Path)
	if err != nil {
		return nil, fmt.Errorf("db: resolve %q: %w", cfg.Path, err)
	}
	if _, err := os.Stat(abs); err != nil {
		// A backup source must already exist: a missing path is a typo or an
		// absent store, never something to create or migrate.
		return nil, fmt.Errorf("db: backup source %q: %w", abs, err)
	}

	write, err := openPool(abs, cfg, true, true)
	if err != nil {
		return nil, err
	}
	read, err := openPool(abs, cfg, false, true)
	if err != nil {
		_ = write.Close()
		return nil, err
	}

	d := &DB{read: read, write: write, path: abs, cfg: cfg}

	if err := write.PingContext(ctx); err != nil {
		_ = d.Close()
		return nil, fmt.Errorf("db: open %q: %w", abs, err)
	}
	if err := verifyPragmas(ctx, write, cfg, "write", false); err != nil {
		_ = d.Close()
		return nil, err
	}
	if err := verifyPragmas(ctx, read, cfg, "read", false); err != nil {
		_ = d.Close()
		return nil, err
	}
	if err := VerifySchemaCurrent(ctx, write); err != nil {
		_ = d.Close()
		return nil, err
	}
	return d, nil
}
