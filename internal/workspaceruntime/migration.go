package workspaceruntime

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/noisefloor-oss/deck/internal/workspacemigrate"
)

var ErrMigrationRequired = errors.New("workspace runtime: verified data import required before startup")

type MigrationConfig struct {
	SnapshotDir    string
	ManifestSHA256 string
}

func validateMigration(cfg Config) error {
	m := cfg.Migration
	if m == nil {
		return nil
	}
	if !filepath.IsAbs(m.SnapshotDir) || filepath.Clean(m.SnapshotDir) != m.SnapshotDir || strings.ContainsAny(m.SnapshotDir, "\x00\r\n") ||
		len(m.ManifestSHA256) != 64 || strings.Trim(m.ManifestSHA256, "0123456789abcdef") != "" {
		return ErrConfiguration
	}
	for _, name := range []string{cfg.CipherDir, cfg.MountPoint, cfg.ControlDir, cfg.HomeDir, cfg.RuntimeDir, cfg.ComposeEnvFile, cfg.SealedComposePath} {
		if within(m.SnapshotDir, name) || within(name, m.SnapshotDir) {
			return ErrConfiguration
		}
	}
	// Retaining plaintext staging is unnecessary after a completed import.
	if _, err := os.Lstat(m.SnapshotDir); err == nil {
		if !privateDirectory(m.SnapshotDir, cfg.OwnerUID) {
			return ErrConfiguration
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return ErrConfiguration
	}
	return nil
}

func (c *Controller) migrationReady(ctx context.Context) error {
	if c.cfg.Migration == nil {
		return nil
	}
	var manifest, cipher string
	err := c.db.QueryRowContext(ctx, `SELECT manifest_digest,cipher_digest FROM workspace_import WHERE id=1`).Scan(&manifest, &cipher)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrMigrationRequired
	}
	if err != nil {
		return ErrJournal
	}
	if manifest != c.cfg.Migration.ManifestSHA256 || cipher == "" || cipher != c.configDigest() {
		return ErrUnsafeStorage
	}
	return nil
}

// Import is admin-only. The host wrapper must own the same lifetime lock as
// the gateway; no browser operation exposes snapshot paths or starts copying.
func (c *Controller) Import(ctx context.Context) error {
	if err := c.acquire(ctx); err != nil {
		return err
	}
	defer c.release()
	if c.cfg.Migration == nil {
		return ErrPrecondition
	}
	if err := c.migrationReady(ctx); !errors.Is(err, ErrMigrationRequired) {
		return err
	}
	state, err := c.observe(ctx)
	if err != nil || !state.Configured || state.Storage != "open" || state.Runtime != "stopped" {
		return ErrPrecondition
	}
	cipher := c.configDigest()
	var enrolled int
	if c.db.QueryRowContext(ctx, `SELECT count(*) FROM operations WHERE kind='initialize' AND status='succeeded' AND config_digest=?`, cipher).Scan(&enrolled) != nil {
		return ErrJournal
	}
	if cipher == "" || enrolled != 1 {
		return ErrPrecondition
	}
	if err := c.projectGone(ctx); err != nil {
		return err
	}
	target, err := c.importRoot()
	if err != nil {
		return err
	}
	defer func() { _ = target.Close() }()
	// Until the native journal commits, recovery must verify publication against
	// the pinned snapshot manifest. Only a successful command permits removing
	// plaintext staging; the receipt alone cannot establish that data survived.
	var source *os.Root
	if privateDirectory(c.cfg.Migration.SnapshotDir, c.cfg.OwnerUID) {
		source, err = os.OpenRoot(c.cfg.Migration.SnapshotDir)
		if err != nil {
			return ErrUnsafeStorage
		}
		defer func() { _ = source.Close() }()
	}
	if err := workspacemigrate.Import(ctx, source, target, c.cfg.Migration.ManifestSHA256); err != nil {
		return err
	}
	after, err := c.observe(ctx)
	if err != nil || after.Epoch != state.Epoch || after.Storage != "open" || after.Runtime != "stopped" || cipher != c.configDigest() {
		return ErrUnsafeStorage
	}
	if err := c.projectGone(ctx); err != nil {
		return err
	}
	if _, err := c.db.ExecContext(ctx, `INSERT INTO workspace_import(id,manifest_digest,cipher_digest) VALUES(1,?,?)`, c.cfg.Migration.ManifestSHA256, cipher); err != nil {
		return ErrJournal
	}
	return nil
}
