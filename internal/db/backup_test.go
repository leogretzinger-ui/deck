package db

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The restore half of the round trip is just Open: a backup must come back
// as a database that migrates clean (verifyApplied included), proves
// writable, and holds every row the source had committed — including the
// lease, task-run, and audit state that rides through the snapshot.
func TestBackupRoundTrip(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	now := FormatTime(time.Now())

	if err := d.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO projects (id, name, canonical_path, created_at, last_used_at)
			 VALUES ('p1','one','/workspace/one',?,?)`, now, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO sessions (id, project_id, provider, tmux_socket_path,
			     tmux_session_name, status, created_at, updated_at)
			 VALUES ('s1','p1','claude','/run/s','noisedeck-s1','starting',?,?)`, now, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO controller_leases (session_id, controller_id, fencing_token, last_heartbeat, expires_at)
			 VALUES ('s1','c1',1,?,?)`, now, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO taskmaster_runs (id, project_id, argv, status, exit_code, started_at, finished_at)
			 VALUES ('tr1','p1','["noise","tasks","next"]','exited',0,?,?)`, now, now); err != nil {
			return err
		}
		sid := "s1"
		return AppendAudit(ctx, tx, AuditEvent{SessionID: &sid, Type: "test.backup"})
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	var migrations int
	if err := d.Reader().QueryRow(
		`SELECT count(*) FROM schema_migrations`).Scan(&migrations); err != nil {
		t.Fatalf("count source migrations: %v", err)
	}

	dest := filepath.Join(t.TempDir(), "backup.db")
	if err := d.Backup(ctx, dest); err != nil {
		t.Fatalf("Backup: %v", err)
	}

	restored, err := Open(ctx, Config{Path: dest})
	if err != nil {
		t.Fatalf("reopen backup: %v", err)
	}
	t.Cleanup(func() { _ = restored.Close() })

	if err := restored.VerifyWritable(ctx); err != nil {
		t.Errorf("VerifyWritable on restored backup: %v", err)
	}
	var restoredMigrations int
	if err := restored.Reader().QueryRow(
		`SELECT count(*) FROM schema_migrations`).Scan(&restoredMigrations); err != nil {
		t.Fatalf("count restored migrations: %v", err)
	}
	if restoredMigrations != migrations {
		t.Errorf("restored schema_migrations = %d rows, want %d", restoredMigrations, migrations)
	}
	for table, want := range map[string]int{
		"projects": 1, "sessions": 1,
		"controller_leases": 1, "taskmaster_runs": 1, "audit_events": 1,
	} {
		var n int
		if err := restored.Reader().QueryRow(
			`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatalf("count restored %s: %v", table, err)
		}
		if n != want {
			t.Errorf("restored %s = %d rows, want %d", table, n, want)
		}
	}
}

// The backup is taken while the backend is live, so it must not require
// quiescing the writer — and it must see only committed state.
func TestBackupSucceedsWhileAWriteIsHeld(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	now := FormatTime(time.Now())

	if err := d.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO projects (id, name, canonical_path, created_at, last_used_at)
			 VALUES ('committed','c','/workspace/c',?,?)`, now, now)
		return err
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Hold a write transaction open on the sole writer connection, then back
	// up from a second connection — the same shape as a person running the
	// backup command against a live backend.
	held, err := d.write.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("hold write: %v", err)
	}
	defer func() { _ = held.Rollback() }()
	if _, err := held.ExecContext(ctx,
		`INSERT INTO projects (id, name, canonical_path, created_at, last_used_at)
		 VALUES ('uncommitted','u','/workspace/u',?,?)`, now, now); err != nil {
		t.Fatalf("write inside held transaction: %v", err)
	}

	other, err := Open(ctx, Config{Path: d.Path()})
	if err != nil {
		t.Fatalf("second opener: %v", err)
	}
	t.Cleanup(func() { _ = other.Close() })

	dest := filepath.Join(t.TempDir(), "backup.db")
	if err := other.Backup(ctx, dest); err != nil {
		t.Fatalf("Backup during an open write: %v", err)
	}

	restored, err := Open(ctx, Config{Path: dest})
	if err != nil {
		t.Fatalf("reopen backup: %v", err)
	}
	t.Cleanup(func() { _ = restored.Close() })
	var committed, uncommitted int
	if err := restored.Reader().QueryRow(
		`SELECT count(*) FROM projects WHERE id='committed'`).Scan(&committed); err != nil {
		t.Fatalf("count committed: %v", err)
	}
	if err := restored.Reader().QueryRow(
		`SELECT count(*) FROM projects WHERE id='uncommitted'`).Scan(&uncommitted); err != nil {
		t.Fatalf("count uncommitted: %v", err)
	}
	if committed != 1 || uncommitted != 0 {
		t.Errorf("backup carried committed=%d, uncommitted=%d; want 1 and 0",
			committed, uncommitted)
	}
}

// The copy is as credential-sensitive as the live database.
func TestBackupEnforcesOwnerOnlyPermissions(t *testing.T) {
	d := openTestDB(t)
	dest := filepath.Join(t.TempDir(), "backup.db")
	if err := d.Backup(context.Background(), dest); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("stat backup: %v", err)
	}
	if perm := info.Mode().Perm(); perm != dbFileMode {
		t.Errorf("backup has mode %04o, want %04o", perm, dbFileMode)
	}
}

// A pre-existing world-traversable parent must not expose the file while it
// is populated: the staging directory is owner-only regardless of its parent.
func TestBackupPublishesIntoExistingWorldTraversableParent(t *testing.T) {
	d := openTestDB(t)
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o755); err != nil {
		t.Fatalf("chmod parent traversable: %v", err)
	}
	dest := filepath.Join(parent, "backup.db")
	if err := d.Backup(context.Background(), dest); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("stat backup: %v", err)
	}
	if perm := info.Mode().Perm(); perm != dbFileMode {
		t.Errorf("backup has mode %04o, want %04o", perm, dbFileMode)
	}
}

// An existing destination is an older backup or a user's file; either
// way it must survive a mistaken backup run untouched.
func TestBackupRefusesExistingDestination(t *testing.T) {
	d := openTestDB(t)
	dest := filepath.Join(t.TempDir(), "backup.db")
	if err := os.WriteFile(dest, []byte("keep me"), 0o600); err != nil {
		t.Fatalf("pre-create destination: %v", err)
	}
	if err := d.Backup(context.Background(), dest); err == nil {
		t.Fatal("Backup overwrote an existing file")
	}
	body, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	if string(body) != "keep me" {
		t.Errorf("existing destination was clobbered: %q", body)
	}
}

func TestBackupRefusesLiveDatabasePath(t *testing.T) {
	d := openTestDB(t)
	if err := d.Backup(context.Background(), d.Path()); err == nil {
		t.Fatal("Backup accepted the live database as its destination")
	}
}

func TestBackupRequiresDestination(t *testing.T) {
	d := openTestDB(t)
	if err := d.Backup(context.Background(), "  "); err == nil {
		t.Fatal("Backup accepted a blank destination")
	}
}

// A failed backup must not leave a partial file behind that a later restore
// could mistake for a good one — neither at the destination nor as a
// staging-directory leftover.
func TestBackupCleansUpAfterFailure(t *testing.T) {
	d := openTestDB(t)
	if err := d.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	dest := filepath.Join(t.TempDir(), "backup.db")
	if err := d.Backup(context.Background(), dest); err == nil {
		t.Fatal("Backup succeeded on a closed database")
	}
	if _, err := os.Stat(dest); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("failed backup left %q behind (stat: %v)", dest, err)
	}
	entries, err := os.ReadDir(filepath.Dir(dest))
	if err != nil {
		t.Fatalf("read destination directory: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("failed backup left staging artifacts: %v", entries)
	}
}

// A backup reader must never create the source: a missing path is a typo or
// an absent store, never a fresh empty database.
func TestOpenBackupSourceRefusesMissingPath(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "nope.db")
	if _, err := OpenBackupSource(context.Background(), Config{Path: missing}); err == nil {
		t.Fatal("OpenBackupSource accepted a missing path")
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("OpenBackupSource created %q (stat: %v)", missing, err)
	}
}

// A database whose ledger was tampered with after migration must be refused,
// so a backup never certifies a schema this binary did not produce.
func TestOpenBackupSourceRefusesTamperedLedger(t *testing.T) {
	d := openTestDB(t)
	if err := d.WithTx(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE schema_migrations SET checksum = 'bogus' WHERE version = 1`)
		return err
	}); err != nil {
		t.Fatalf("tamper ledger: %v", err)
	}
	if _, err := OpenBackupSource(context.Background(), Config{Path: d.Path()}); err == nil {
		t.Fatal("OpenBackupSource accepted a database whose ledger was tampered with")
	}
}

// A healthy database opens read-only, and the handle's Backup produces a file
// the normal Open path accepts.
func TestOpenBackupSourceHealthyBackup(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	now := FormatTime(time.Now())
	if err := d.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO projects (id, name, canonical_path, created_at, last_used_at)
			 VALUES ('p1','one','/workspace/one',?,?)`, now, now)
		return err
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	src, err := OpenBackupSource(ctx, Config{Path: d.Path()})
	if err != nil {
		t.Fatalf("OpenBackupSource: %v", err)
	}
	t.Cleanup(func() { _ = src.Close() })

	dest := filepath.Join(t.TempDir(), "backup.db")
	if err := src.Backup(ctx, dest); err != nil {
		t.Fatalf("Backup: %v", err)
	}

	restored, err := Open(ctx, Config{Path: dest})
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
