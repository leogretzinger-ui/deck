package main

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/noisefloor-oss/deck/internal/db"
)

func TestRunRestoreRequiresFrom(t *testing.T) {
	if err := runRestore(nil, io.Discard, io.Discard); err == nil {
		t.Fatal("runRestore accepted a missing -from")
	}
}

func TestRunRestoreInstallsBackup(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "run", "control.db")
	lockPath := filepath.Join(dir, "run", "backend.lock")
	socketPath := filepath.Join(dir, "tmux", "ac.sock")

	ctx := context.Background()
	d, err := db.Open(ctx, db.Config{Path: dbPath})
	if err != nil {
		t.Fatalf("open source: %v", err)
	}
	now := db.FormatTime(time.Now())
	if err := d.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO projects (id, name, canonical_path, created_at, last_used_at)
			 VALUES ('p1','one',?, ?, ?)`, dir, now, now)
		return err
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	backupPath := filepath.Join(dir, "backups", "control-backup.db")
	if err := d.Backup(ctx, backupPath); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("close source: %v", err)
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
  lock_path: "` + lockPath + `"
tmux:
  socket_path: "` + socketPath + `"
projects:
  allowed_roots: ["` + dir + `"]
`
	if err := os.WriteFile(cfgPath, []byte(cfgBody), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("NOISEDECK_DATABASE_PATH", dbPath)
	t.Setenv("NOISEDECK_DATABASE_LOCK_PATH", lockPath)
	t.Setenv("NOISEDECK_TMUX_SOCKET_PATH", socketPath)

	var stdout, stderr bytes.Buffer
	if err := runRestore([]string{"-config", cfgPath, "-from", backupPath}, &stdout, &stderr); err != nil {
		t.Fatalf("runRestore: %v (stderr: %s)", err, stderr.String())
	}
	// The command names what it will replace before it does, and what it
	// fenced and restored after: a person's proof that a synthetic run
	// targeted the synthetic paths, since the environment decides them.
	for _, want := range []string{
		"restore target: database " + dbPath + ", lock " + lockPath + ", tmux socket " + socketPath + "\n",
		"fenced tmux socket " + socketPath + "; database " + dbPath + " restored from " + backupPath + ";",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout.String())
		}
	}

	info, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("stat restored db: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("restored db mode = %04o, want 0600", perm)
	}
	restored, err := db.Open(ctx, db.Config{Path: dbPath})
	if err != nil {
		t.Fatalf("reopen restored: %v", err)
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
