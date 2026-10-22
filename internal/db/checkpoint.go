package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/mattn/go-sqlite3"
)

// ErrUnreadable marks a checkpoint that failed because SQLite cannot open the
// file or it is not a database, as opposed to a readable database that is busy.
var ErrUnreadable = errors.New("db: database is unreadable")

// CheckpointTruncate folds an EXISTING database's WAL into the main file and
// truncates it, then closes every connection so SQLite removes the sidecars.
// Like OpenBackupSource it never creates or migrates: a missing path is
// reported as os.ErrNotExist. A checkpoint blocked by a reader is an error,
// since the WAL would not be empty.
func CheckpointTruncate(ctx context.Context, cfg Config) (err error) {
	cfg = cfg.withDefaults()
	if strings.TrimSpace(cfg.Path) == "" {
		return errors.New("db: Config.Path is required")
	}
	abs, err := filepath.Abs(cfg.Path)
	if err != nil {
		return fmt.Errorf("db: resolve %q: %w", cfg.Path, err)
	}
	if _, err := os.Stat(abs); err != nil {
		return fmt.Errorf("db: checkpoint %q: %w", abs, err)
	}

	q := url.Values{}
	q.Set("_busy_timeout", fmt.Sprintf("%d", cfg.BusyTimeout.Milliseconds()))
	uri := url.URL{Scheme: "file", Path: abs, RawQuery: q.Encode()}
	pool, err := sql.Open("sqlite3", uri.String())
	if err != nil {
		return fmt.Errorf("db: open %q: %w", abs, err)
	}
	defer func() {
		if cerr := pool.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("db: close %q: %w", abs, cerr)
		}
	}()
	pool.SetMaxOpenConns(1)

	var busy, logFrames, checkpointed int
	if err := pool.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &checkpointed); err != nil {
		var sqErr sqlite3.Error
		if errors.As(err, &sqErr) {
			switch sqErr.Code {
			case sqlite3.ErrNotADB, sqlite3.ErrCorrupt, sqlite3.ErrCantOpen:
				return fmt.Errorf("db: checkpoint %q: %w: %w", abs, ErrUnreadable, err)
			}
		}
		return fmt.Errorf("db: checkpoint %q: %w", abs, err)
	}
	if busy != 0 {
		return fmt.Errorf("db: checkpoint %q: blocked by another connection", abs)
	}
	return nil
}
