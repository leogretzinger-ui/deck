package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"filippo.io/age"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/noisefloor-oss/deck/internal/db"
)

// The command must produce a file the normal Open path accepts and that
// carries the source's rows — the same contract the documented restore
// procedure relies on.
func TestRunBackupWritesRestorableCopy(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "run", "control.db")

	ctx := context.Background()
	d, err := db.Open(ctx, db.Config{Path: dbPath})
	if err != nil {
		t.Fatalf("open source: %v", err)
	}
	now := db.FormatTime(time.Now())
	if err := d.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO projects (id, name, canonical_path, created_at, last_used_at)
			 VALUES ('p1','one','/workspace/one',?,?)`, now, now)
		return err
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	cfgPath := filepath.Join(dir, "config.yaml")
	cfgBody := `
server:
  allowed_origins: ["http://console.test"]
auth:
  mode: "trusted_proxy"
  trusted_proxy:
    addresses: ["127.0.0.1"]
database:
  path: "` + dbPath + `"
projects:
  allowed_roots: ["` + dir + `"]
`
	if err := os.WriteFile(cfgPath, []byte(cfgBody), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	// Environment overrides the file; pin the path so a host NOISEDECK_*
	// variable cannot redirect the command at a different database.
	t.Setenv("NOISEDECK_DATABASE_PATH", dbPath)

	// The source stays open with a live writer for the whole command — the
	// online-backup contract.
	dest := filepath.Join(dir, "backups", "control-backup.db")
	var stdout, stderr bytes.Buffer
	if err := runBackup([]string{"-config", cfgPath, "-dest", dest}, &stdout, &stderr); err != nil {
		t.Fatalf("runBackup: %v (stderr: %s)", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), dest) {
		t.Errorf("stdout %q does not name the destination %q", stdout.String(), dest)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("close source: %v", err)
	}

	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("stat backup: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("backup mode = %04o, want 0600", perm)
	}

	restored, err := db.Open(ctx, db.Config{Path: dest})
	if err != nil {
		t.Fatalf("reopen backup: %v", err)
	}
	t.Cleanup(func() { _ = restored.Close() })
	var n int
	if err := restored.Reader().QueryRow(
		`SELECT count(*) FROM projects WHERE id='p1'`).Scan(&n); err != nil {
		t.Fatalf("read restored rows: %v", err)
	}
	if n != 1 {
		t.Errorf("restored projects = %d, want 1", n)
	}
}

func TestRunBackupRequiresDest(t *testing.T) {
	if err := runBackup(nil, io.Discard, io.Discard); err == nil {
		t.Fatal("runBackup accepted a missing -dest")
	}
}

// The backup command must open its source read-only, never creating it: a
// missing database path is a typo, not a fresh empty backup.
func TestRunBackupRefusesMissingSource(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "run", "does-not-exist.db")
	cfgPath := filepath.Join(dir, "config.yaml")
	cfgBody := `
server:
  allowed_origins: ["http://console.test"]
auth:
  mode: "trusted_proxy"
  trusted_proxy:
    addresses: ["127.0.0.1"]
database:
  path: "` + dbPath + `"
projects:
  allowed_roots: ["` + dir + `"]
`
	if err := os.WriteFile(cfgPath, []byte(cfgBody), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("NOISEDECK_DATABASE_PATH", dbPath)

	dest := filepath.Join(dir, "backup.db")
	var stdout, stderr bytes.Buffer
	if err := runBackup([]string{"-config", cfgPath, "-dest", dest}, &stdout, &stderr); err == nil {
		t.Fatal("runBackup accepted a missing source database")
	}
	if _, err := os.Stat(dbPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("runBackup created %q (stat: %v)", dbPath, err)
	}
}

// -recipient seals the file to an age recipient; without it the command
// writes plaintext and says so once on stderr.
func TestRunBackupRecipientSealsAndPlaintextWarns(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "run", "control.db")
	ctx := context.Background()
	d, err := db.Open(ctx, db.Config{Path: dbPath})
	if err != nil {
		t.Fatalf("open source: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	cfgPath := filepath.Join(dir, "config.yaml")
	cfgBody := `
server:
  allowed_origins: ["http://console.test"]
auth:
  mode: "trusted_proxy"
  trusted_proxy:
    addresses: ["127.0.0.1"]
database:
  path: "` + dbPath + `"
projects:
  allowed_roots: ["` + dir + `"]
`
	if err := os.WriteFile(cfgPath, []byte(cfgBody), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("NOISEDECK_DATABASE_PATH", dbPath)

	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("generate identity: %v", err)
	}
	sealedDest := filepath.Join(dir, "backups", "control.db.age")
	var stdout, stderr bytes.Buffer
	if err := runBackup([]string{"-config", cfgPath, "-dest", sealedDest, "-recipient", id.Recipient().String()}, &stdout, &stderr); err != nil {
		t.Fatalf("runBackup -recipient: %v (stderr: %s)", err, stderr.String())
	}
	if strings.Contains(stderr.String(), "plaintext") {
		t.Errorf("sealed backup warned about plaintext: %s", stderr.String())
	}
	sealed, err := db.IsSealedBackup(sealedDest)
	if err != nil || !sealed {
		t.Fatalf("IsSealedBackup = %v, %v; want true", sealed, err)
	}

	stdout.Reset()
	stderr.Reset()
	plainDest := filepath.Join(dir, "backups", "control.db")
	if err := runBackup([]string{"-config", cfgPath, "-dest", plainDest}, &stdout, &stderr); err != nil {
		t.Fatalf("runBackup plain: %v (stderr: %s)", err, stderr.String())
	}
	if !strings.Contains(stderr.String(), "plaintext") || !strings.Contains(stderr.String(), "-recipient") {
		t.Errorf("plain backup did not warn on stderr: %q", stderr.String())
	}

	stderr.Reset()
	if err := runBackup([]string{"-config", cfgPath, "-dest", plainDest + ".2", "-recipient", "not-a-key"}, &stdout, &stderr); err == nil {
		t.Fatal("runBackup accepted a malformed recipient")
	}
	if _, err := os.Stat(plainDest + ".2"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("malformed recipient still produced a file (stat: %v)", err)
	}
}
