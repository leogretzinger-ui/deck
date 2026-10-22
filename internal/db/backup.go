package db

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Backup writes a consistent online copy of the control database to destPath.
//
// The mechanism is VACUUM INTO, not a file copy. A raw copy of the main file
// taken while the backend is live misses every committed row still sitting in
// the -wal sidecar — the backup would look intact and silently lose the most
// recent control state. VACUUM INTO reads through the WAL under a read
// transaction, so the copy is complete and consistent at a single point in
// time even with a writer active, and it arrives as one self-contained,
// checkpointed file with no sidecars: exactly the artifact a stopped-backend
// restore wants. schema_migrations travels with everything else, so the
// restored file passes Open's verification unchanged.
//
// The destination must not exist: overwriting whatever already sits at
// destPath would destroy exactly the older recovery point a backup exists to
// preserve. This is enforced as a no-replace publish rather than a
// stat-then-create check. The backup is built inside an owner-only staging
// directory in the destination's own directory and then hard-linked into
// place; link(2) fails with EEXIST if a file has appeared at destPath since,
// so an existing destination is never clobbered and no other actor's file is
// ever removed. No partial output is ever visible at destPath.
//
// The copy is owner-only throughout creation: VACUUM INTO writes into a
// staging directory created 0700 by os.MkdirTemp, so the process umask cannot
// expose the sensitive file while it is being populated, and the published
// file is verified to be exactly 0600 before it is linked into place. Cleanup
// only ever removes the staging directory — it never touches the destination
// path.
//
// Where backups may live is your responsibility, not something a
// path check here can prove: outside project workspaces and source control,
// on a local filesystem.
func (d *DB) Backup(ctx context.Context, destPath string) error {
	if strings.TrimSpace(destPath) == "" {
		return errors.New("db: backup destination path is required")
	}
	dest, err := filepath.Abs(destPath)
	if err != nil {
		return fmt.Errorf("db: resolve backup destination %q: %w", destPath, err)
	}
	if dest == d.path {
		return fmt.Errorf("db: backup destination %q is the live database", dest)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return fmt.Errorf("db: create directory for backup %q: %w", dest, err)
	}

	// Build the backup inside an owner-only staging directory in the
	// destination's own directory, so the file is never exposed under the
	// process umask while VACUUM INTO populates it. Cleanup only ever targets
	// this staging artifact — never the destination.
	stageDir, err := os.MkdirTemp(filepath.Dir(dest), ".backup-stage-*")
	if err != nil {
		return fmt.Errorf("db: create staging directory for backup %q: %w", dest, err)
	}
	defer func() { _ = os.RemoveAll(stageDir) }()

	staged := filepath.Join(stageDir, "backup.db")
	// The backup runs on the write pool, which serializes it against
	// in-process writes. VACUUM INTO writes only the staging file; it does so
	// even when the pool is opened mode=ro (OpenBackupSource opens both pools
	// read-only for the backup command), because mode=ro governs the source
	// database, not the side file SQLite creates.
	if _, err := d.write.ExecContext(ctx, `VACUUM INTO ?`, staged); err != nil {
		return fmt.Errorf("db: backup to %q: %w", dest, err)
	}

	// The copy carries the same fencing tokens and session/audit records as
	// the live database, so it is equally credential-sensitive.
	if err := os.Chmod(staged, dbFileMode); err != nil {
		return fmt.Errorf("db: restrict permissions on backup %q: %w", dest, err)
	}
	info, err := os.Stat(staged)
	if err != nil {
		return fmt.Errorf("db: verify backup permissions %q: %w", dest, err)
	}
	if info.Mode().Perm() != dbFileMode {
		return fmt.Errorf("db: backup %q has mode %04o, want %04o",
			dest, info.Mode().Perm(), dbFileMode)
	}

	// Publish with no-replace semantics: a hard link fails with EEXIST if the
	// destination appeared since, so an existing destination is never
	// clobbered and no other actor's file is ever removed. Link and remove
	// are same-directory, so both paths are on one local filesystem.
	if err := os.Link(staged, dest); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("db: backup destination %q already exists", dest)
		}
		return fmt.Errorf("db: publish backup to %q: %w", dest, err)
	}
	_ = os.Remove(staged)
	return nil
}
