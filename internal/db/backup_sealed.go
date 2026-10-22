package db

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"filippo.io/age"
)

// ageHeader opens every age-encrypted file. A backup that starts with it is
// ciphertext and needs an identity to restore; one that does not is the
// plain SQLite file Backup writes.
const ageHeader = "age-encryption.org/v1"

// IsSealedBackup reports whether the file at path is an age-encrypted backup.
// It reads only the header, so it is safe on a file of any size.
func IsSealedBackup(path string) (bool, error) {
	f, err := os.Open(path) // #nosec G304 -- the caller's backup path, opened read-only to inspect its header.
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()
	head := make([]byte, len(ageHeader))
	n, err := io.ReadFull(f, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return false, err
	}
	return bytes.Equal(head[:n], []byte(ageHeader)), nil
}

// BackupSealed writes a consistent online copy of the control database to
// destPath encrypted to recipient.
//
// The plaintext copy exists only inside the database's own directory: VACUUM
// INTO stages there, under the same encrypted state root that holds the live
// database, and the destination directory receives ciphertext and nothing
// else. Backup, the plaintext variant, stages beside its destination; that is
// exactly what a sealed backup must not do, because the destination is the
// medium that leaves the host.
//
// Publication keeps Backup's no-replace rule. The ciphertext is written to an
// owner-only partial file in the destination directory, synced, then hard
// linked to destPath; link(2) fails with EEXIST if a file has appeared there
// since, so an older recovery point is never clobbered. If encryption fails,
// the disk fills, or the process dies, the destination holds at most a
// ciphertext partial and never a plaintext byte. The partial is removed on
// every failure this function sees.
func (d *DB) BackupSealed(ctx context.Context, destPath string, recipient age.Recipient) error {
	if recipient == nil {
		return errors.New("db: sealed backup requires a recipient")
	}
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

	// Stage the plaintext next to the live database, never next to the
	// destination.
	stageDir, err := os.MkdirTemp(filepath.Dir(d.path), ".backup-stage-*")
	if err != nil {
		return fmt.Errorf("db: create staging directory for backup %q: %w", dest, err)
	}
	defer func() { _ = os.RemoveAll(stageDir) }()

	staged := filepath.Join(stageDir, "backup.db")
	if _, err := d.write.ExecContext(ctx, `VACUUM INTO ?`, staged); err != nil {
		return fmt.Errorf("db: backup to %q: %w", dest, err)
	}
	plain, err := os.Open(staged) // #nosec G304 -- the staging file this function just created beside the database.
	if err != nil {
		return fmt.Errorf("db: open staged backup for %q: %w", dest, err)
	}
	defer func() { _ = plain.Close() }()

	partial, err := os.CreateTemp(filepath.Dir(dest), "."+filepath.Base(dest)+".partial-*")
	if err != nil {
		return fmt.Errorf("db: create ciphertext partial for backup %q: %w", dest, err)
	}
	partialPath := partial.Name()
	// Every exit before the link removes the partial. After a successful link
	// the same call removes the second name of the published file.
	defer func() { _ = os.Remove(partialPath) }()
	if err := partial.Chmod(dbFileMode); err != nil {
		_ = partial.Close()
		return fmt.Errorf("db: restrict permissions on backup %q: %w", dest, err)
	}

	sealed, err := age.Encrypt(partial, recipient)
	if err != nil {
		_ = partial.Close()
		return fmt.Errorf("db: seal backup %q: %w", dest, err)
	}
	if _, err := io.Copy(sealed, plain); err != nil {
		_ = partial.Close()
		return fmt.Errorf("db: seal backup %q: %w", dest, err)
	}
	if err := sealed.Close(); err != nil {
		_ = partial.Close()
		return fmt.Errorf("db: finish sealing backup %q: %w", dest, err)
	}
	if err := partial.Sync(); err != nil {
		_ = partial.Close()
		return fmt.Errorf("db: sync sealed backup %q: %w", dest, err)
	}
	if err := partial.Close(); err != nil {
		return fmt.Errorf("db: close sealed backup %q: %w", dest, err)
	}
	info, err := os.Stat(partialPath)
	if err != nil {
		return fmt.Errorf("db: verify backup permissions %q: %w", dest, err)
	}
	if info.Mode().Perm() != dbFileMode {
		return fmt.Errorf("db: backup %q has mode %04o, want %04o",
			dest, info.Mode().Perm(), dbFileMode)
	}

	if err := os.Link(partialPath, dest); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("db: backup destination %q already exists", dest)
		}
		return fmt.Errorf("db: publish backup to %q: %w", dest, err)
	}
	return nil
}

// Unseal decrypts an age-encrypted backup at srcPath into an owner-only file
// created inside dir, and returns that file's path. The caller chooses dir so
// the plaintext lands under the encrypted state root, and removes the file
// when done. A wrong identity, a truncated or altered ciphertext, or a file
// that is not an age backup fails before any byte of plaintext is kept.
func Unseal(srcPath, dir string, identities ...age.Identity) (string, error) {
	if len(identities) == 0 {
		return "", errors.New("db: unseal requires at least one identity")
	}
	src, err := os.Open(srcPath) // #nosec G304 -- the caller's backup path, opened read-only for decryption.
	if err != nil {
		return "", fmt.Errorf("db: open sealed backup %q: %w", srcPath, err)
	}
	defer func() { _ = src.Close() }()

	plain, err := age.Decrypt(src, identities...)
	if err != nil {
		return "", fmt.Errorf("db: unseal backup %q: %w", srcPath, err)
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("db: create directory for unsealed backup: %w", err)
	}
	out, err := os.CreateTemp(dir, ".unseal-*")
	if err != nil {
		return "", fmt.Errorf("db: create unsealed staging file: %w", err)
	}
	outPath := out.Name()
	fail := func(err error) (string, error) {
		_ = out.Close()
		_ = os.Remove(outPath)
		return "", err
	}
	if err := out.Chmod(dbFileMode); err != nil {
		return fail(fmt.Errorf("db: restrict unsealed staging file: %w", err))
	}
	// age authenticates the stream as it is read; a tampered or truncated
	// ciphertext surfaces here, and the partial plaintext is removed.
	if _, err := io.Copy(out, plain); err != nil {
		return fail(fmt.Errorf("db: unseal backup %q: %w", srcPath, err))
	}
	if err := out.Sync(); err != nil {
		return fail(fmt.Errorf("db: sync unsealed staging file: %w", err))
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(outPath)
		return "", fmt.Errorf("db: close unsealed staging file: %w", err)
	}
	return outPath, nil
}
