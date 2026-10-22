package db

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCheckpointTruncateEmptiesTheWAL(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "control.db")
	d, err := Open(ctx, Config{Path: path})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if info, err := os.Stat(path + "-wal"); err != nil || info.Size() == 0 {
		t.Fatalf("fixture WAL is empty (%v); the checkpoint would prove nothing", err)
	}
	if err := CheckpointTruncate(ctx, Config{Path: path}); err != nil {
		t.Fatalf("CheckpointTruncate: %v", err)
	}
	if info, err := os.Stat(path + "-wal"); err != nil || info.Size() != 0 {
		t.Fatalf("WAL after checkpoint: %v, size %d, want empty", err, info.Size())
	}
}

func TestCheckpointTruncateRefusesMissingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.db")
	err := CheckpointTruncate(context.Background(), Config{Path: path})
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("error = %v, want os.ErrNotExist", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("checkpoint created %s", path)
	}
}

func TestCheckpointTruncateReportsABlockedCheckpoint(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "control.db")
	d, err := Open(ctx, Config{Path: path})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	tx, err := d.Reader().BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin read transaction: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatalf("pin a read snapshot: %v", err)
	}
	if err := CheckpointTruncate(ctx, Config{Path: path, BusyTimeout: 50 * time.Millisecond}); err == nil {
		t.Fatal("CheckpointTruncate succeeded with a reader pinning the WAL")
	}
}

func TestCheckpointTruncateReportsAnUnreadableDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.db")
	if err := os.WriteFile(path, []byte("this is not a sqlite database, it is long enough to look like a header"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := CheckpointTruncate(context.Background(), Config{Path: path})
	if !errors.Is(err, ErrUnreadable) {
		t.Fatalf("error = %v, want ErrUnreadable", err)
	}
}

func TestCheckpointTruncateBusyIsNotUnreadable(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "control.db")
	d, err := Open(ctx, Config{Path: path})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	tx, err := d.Reader().BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin read transaction: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatalf("pin a read snapshot: %v", err)
	}
	err = CheckpointTruncate(ctx, Config{Path: path, BusyTimeout: 50 * time.Millisecond})
	if err == nil || errors.Is(err, ErrUnreadable) {
		t.Fatalf("error = %v, want a refusal that is not ErrUnreadable", err)
	}
}
