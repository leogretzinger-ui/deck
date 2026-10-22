package db

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
)

func newIdentity(t *testing.T) *age.X25519Identity {
	t.Helper()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("generate identity: %v", err)
	}
	return id
}

func seedSealedProject(t *testing.T, d *DB) {
	t.Helper()
	now := FormatTime(time.Now())
	if err := d.WithTx(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO projects (id, name, canonical_path, created_at, last_used_at)
			 VALUES ('p1','one','/workspace/one',?,?)`, now, now)
		return err
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

// A sealed backup is ciphertext on disk and, once unsealed with the matching
// identity, a database that opens clean and holds the committed rows.
func TestBackupSealedRoundTrip(t *testing.T) {
	d := openTestDB(t)
	seedSealedProject(t, d)
	id := newIdentity(t)
	dest := filepath.Join(t.TempDir(), "backup.db.age")

	if err := d.BackupSealed(context.Background(), dest, id.Recipient()); err != nil {
		t.Fatalf("BackupSealed: %v", err)
	}
	raw, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read sealed backup: %v", err)
	}
	if bytes.Contains(raw, []byte("SQLite format 3")) {
		t.Fatal("sealed backup carries the SQLite header in the clear")
	}
	if !bytes.HasPrefix(raw, []byte(ageHeader)) {
		t.Fatalf("sealed backup does not start with the age header: %q", raw[:32])
	}
	sealed, err := IsSealedBackup(dest)
	if err != nil || !sealed {
		t.Fatalf("IsSealedBackup = %v, %v; want true", sealed, err)
	}
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != dbFileMode {
		t.Errorf("sealed backup has mode %04o, want %04o", perm, dbFileMode)
	}

	plain, err := Unseal(dest, t.TempDir(), id)
	if err != nil {
		t.Fatalf("Unseal: %v", err)
	}
	restored, err := Open(context.Background(), Config{Path: plain})
	if err != nil {
		t.Fatalf("open unsealed backup: %v", err)
	}
	t.Cleanup(func() { _ = restored.Close() })
	var n int
	if err := restored.Reader().QueryRow(`SELECT count(*) FROM projects WHERE id='p1'`).Scan(&n); err != nil {
		t.Fatalf("read restored rows: %v", err)
	}
	if n != 1 {
		t.Errorf("restored projects = %d, want 1", n)
	}
}

// observingRecipient is an age recipient that runs a probe at the moment the
// file key is wrapped. age wraps the key when Encrypt is called, which is
// after VACUUM INTO has produced the plaintext and after the ciphertext
// partial has been created but before any ciphertext is written. That is
// the instant both staging artifacts exist, so the probe sees the layout the
// feature promises and a regression would break: plaintext beside the
// database, nothing but a partial beside the destination.
type observingRecipient struct {
	inner age.Recipient
	probe func()
	fail  error
}

func (r observingRecipient) Wrap(fileKey []byte) ([]*age.Stanza, error) {
	if r.probe != nil {
		r.probe()
	}
	if r.fail != nil {
		return nil, r.fail
	}
	return r.inner.Wrap(fileKey)
}

// The destination directory is the medium that leaves the host. It must
// receive ciphertext and nothing else: no staging directory, no plaintext
// partial, at any point. The plaintext stages beside the live database.
func TestBackupSealedLeavesNoPlaintextBesideDestination(t *testing.T) {
	d := openTestDB(t)
	seedSealedProject(t, d)
	id := newIdentity(t)
	destDir := t.TempDir()
	dest := filepath.Join(destDir, "backup.db.age")
	dbDir := filepath.Dir(d.Path())

	probed := false
	rec := observingRecipient{inner: id.Recipient(), probe: func() {
		probed = true
		// Beside the destination: exactly one entry, the ciphertext partial,
		// a regular file, and no directory of any kind.
		entries, err := os.ReadDir(destDir)
		if err != nil {
			t.Fatalf("read destination directory during backup: %v", err)
		}
		if len(entries) != 1 {
			t.Fatalf("destination directory during backup holds %d entries, want only the partial: %v", len(entries), entries)
		}
		e := entries[0]
		if e.IsDir() {
			t.Fatalf("a directory %q appeared beside the destination during backup", e.Name())
		}
		if !strings.HasPrefix(e.Name(), "."+filepath.Base(dest)+".partial-") {
			t.Fatalf("unexpected entry %q beside the destination during backup", e.Name())
		}
		body, err := os.ReadFile(filepath.Join(destDir, e.Name()))
		if err != nil {
			t.Fatalf("read partial: %v", err)
		}
		if len(body) != 0 && !bytes.HasPrefix(body, []byte(ageHeader)) {
			t.Fatalf("partial beside the destination holds plaintext: %q", body[:min(32, len(body))])
		}
		// Beside the database: the staging directory with the plaintext copy.
		var staged []string
		dbEntries, err := os.ReadDir(dbDir)
		if err != nil {
			t.Fatalf("read database directory during backup: %v", err)
		}
		for _, e := range dbEntries {
			if e.IsDir() && strings.HasPrefix(e.Name(), ".backup-stage-") {
				staged = append(staged, e.Name())
			}
		}
		if len(staged) != 1 {
			t.Fatalf("plaintext staging directory beside the database: %v, want exactly one", staged)
		}
		plain, err := os.ReadFile(filepath.Join(dbDir, staged[0], "backup.db"))
		if err != nil {
			t.Fatalf("read staged plaintext: %v", err)
		}
		if !bytes.HasPrefix(plain, []byte("SQLite format 3")) {
			t.Fatal("staged copy beside the database is not the plaintext database")
		}
	}}
	if err := d.BackupSealed(context.Background(), dest, rec); err != nil {
		t.Fatalf("BackupSealed: %v", err)
	}
	if !probed {
		t.Fatal("the observing recipient was never asked to wrap; the probe did not run")
	}
	entries, err := os.ReadDir(destDir)
	if err != nil {
		t.Fatalf("read destination directory: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(dest) {
		t.Errorf("destination directory holds %v, want only the sealed backup", entries)
	}
	assertNoStaging(t, dbDir)
}

// Encryption that fails after the plaintext has been staged leaves neither
// the plaintext nor a partial behind, and never a destination file.
func TestBackupSealedEncryptionFailureAfterStagingCleansUp(t *testing.T) {
	d := openTestDB(t)
	seedSealedProject(t, d)
	id := newIdentity(t)
	destDir := t.TempDir()
	dest := filepath.Join(destDir, "backup.db.age")
	dbDir := filepath.Dir(d.Path())

	stagedSeen := false
	rec := observingRecipient{inner: id.Recipient(), fail: errors.New("recipient refused"), probe: func() {
		dbEntries, _ := os.ReadDir(dbDir)
		for _, e := range dbEntries {
			if e.IsDir() && strings.HasPrefix(e.Name(), ".backup-stage-") {
				stagedSeen = true
			}
		}
	}}
	err := d.BackupSealed(context.Background(), dest, rec)
	if err == nil || !strings.Contains(err.Error(), "recipient refused") {
		t.Fatalf("BackupSealed = %v, want the recipient failure", err)
	}
	if !stagedSeen {
		t.Fatal("the failure was injected before plaintext staging existed; the test does not cover the cleanup boundary")
	}
	if _, err := os.Stat(dest); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("failed backup left %q behind (stat: %v)", dest, err)
	}
	entries, _ := os.ReadDir(destDir)
	if len(entries) != 0 {
		t.Errorf("failed backup left artifacts beside the destination: %v", entries)
	}
	assertNoStaging(t, dbDir)
}

func assertNoStaging(t *testing.T, dbDir string) {
	t.Helper()
	dbEntries, err := os.ReadDir(dbDir)
	if err != nil {
		t.Fatalf("read database directory: %v", err)
	}
	for _, e := range dbEntries {
		if strings.HasPrefix(e.Name(), ".backup-stage-") {
			t.Errorf("staging directory %q survived", e.Name())
		}
	}
}

func TestBackupSealedRefusesExistingDestination(t *testing.T) {
	d := openTestDB(t)
	id := newIdentity(t)
	dest := filepath.Join(t.TempDir(), "backup.db.age")
	if err := os.WriteFile(dest, []byte("keep me"), 0o600); err != nil {
		t.Fatalf("pre-create destination: %v", err)
	}
	if err := d.BackupSealed(context.Background(), dest, id.Recipient()); err == nil {
		t.Fatal("BackupSealed overwrote an existing file")
	}
	body, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	if string(body) != "keep me" {
		t.Errorf("existing destination was clobbered: %q", body)
	}
	entries, _ := os.ReadDir(filepath.Dir(dest))
	if len(entries) != 1 {
		t.Errorf("refused backup left artifacts beside the destination: %v", entries)
	}
}

func TestBackupSealedCleansUpAfterFailure(t *testing.T) {
	d := openTestDB(t)
	id := newIdentity(t)
	if err := d.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	dest := filepath.Join(t.TempDir(), "backup.db.age")
	if err := d.BackupSealed(context.Background(), dest, id.Recipient()); err == nil {
		t.Fatal("BackupSealed succeeded on a closed database")
	}
	if _, err := os.Stat(dest); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("failed backup left %q behind (stat: %v)", dest, err)
	}
	entries, _ := os.ReadDir(filepath.Dir(dest))
	if len(entries) != 0 {
		t.Errorf("failed backup left artifacts: %v", entries)
	}
}

func TestBackupSealedRequiresRecipient(t *testing.T) {
	d := openTestDB(t)
	if err := d.BackupSealed(context.Background(), filepath.Join(t.TempDir(), "b"), nil); err == nil {
		t.Fatal("BackupSealed accepted a nil recipient")
	}
}

// The wrong identity, an altered ciphertext, and a plain file all fail to
// unseal, and none of them leaves a staging file behind.
func TestUnsealRefusesWrongIdentityTamperAndPlainInput(t *testing.T) {
	d := openTestDB(t)
	seedSealedProject(t, d)
	id := newIdentity(t)
	dest := filepath.Join(t.TempDir(), "backup.db.age")
	if err := d.BackupSealed(context.Background(), dest, id.Recipient()); err != nil {
		t.Fatalf("BackupSealed: %v", err)
	}

	cases := map[string]func(t *testing.T) (string, age.Identity){
		"wrong identity": func(t *testing.T) (string, age.Identity) {
			return dest, newIdentity(t)
		},
		"tampered ciphertext": func(t *testing.T) (string, age.Identity) {
			raw, err := os.ReadFile(dest)
			if err != nil {
				t.Fatal(err)
			}
			raw[len(raw)-8] ^= 0xff
			p := filepath.Join(t.TempDir(), "tampered.age")
			if err := os.WriteFile(p, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			return p, id
		},
		"truncated ciphertext": func(t *testing.T) (string, age.Identity) {
			raw, err := os.ReadFile(dest)
			if err != nil {
				t.Fatal(err)
			}
			p := filepath.Join(t.TempDir(), "truncated.age")
			if err := os.WriteFile(p, raw[:len(raw)-64], 0o600); err != nil {
				t.Fatal(err)
			}
			return p, id
		},
		"plain file": func(t *testing.T) (string, age.Identity) {
			p := filepath.Join(t.TempDir(), "plain.db")
			if err := os.WriteFile(p, []byte("SQLite format 3\x00 not really"), 0o600); err != nil {
				t.Fatal(err)
			}
			return p, id
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			src, identity := setup(t)
			stage := t.TempDir()
			if _, err := Unseal(src, stage, identity); err == nil {
				t.Fatalf("Unseal accepted %s", name)
			}
			entries, _ := os.ReadDir(stage)
			if len(entries) != 0 {
				t.Errorf("failed unseal left staging artifacts: %v", entries)
			}
		})
	}
}

func TestIsSealedBackupOnPlainBackup(t *testing.T) {
	d := openTestDB(t)
	dest := filepath.Join(t.TempDir(), "backup.db")
	if err := d.Backup(context.Background(), dest); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	sealed, err := IsSealedBackup(dest)
	if err != nil || sealed {
		t.Fatalf("IsSealedBackup(plain) = %v, %v; want false", sealed, err)
	}
}
