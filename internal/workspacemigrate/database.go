package workspacemigrate

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"time"

	"github.com/noisefloor-oss/deck/internal/db"
)

var ErrDatabase = errors.New("workspace migration: copied Deck database requires reconciliation")

// prepareDatabase changes only the verified snapshot copy. Source shutdown is
// independently established through the engine before this function is called.
func prepareDatabase(ctx context.Context, path string) (result error) {
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		st, err := os.Lstat(path + suffix)
		if suffix != "" && errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !st.Mode().IsRegular() {
			return ErrDatabase
		}
	}
	database, err := db.Open(ctx, db.Config{Path: path})
	if err != nil {
		return ErrDatabase
	}
	defer func() {
		if database.Close() != nil {
			result = ErrDatabase
		}
	}()
	var integrity string
	if database.Reader().QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity) != nil || integrity != "ok" {
		return ErrDatabase
	}
	violations, err := database.Reader().QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return ErrDatabase
	}
	defer func() {
		if violations.Close() != nil {
			result = ErrDatabase
		}
	}()
	invalid := violations.Next() || violations.Err() != nil
	if invalid {
		return ErrDatabase
	}
	err = database.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		// A provisional recovery belongs to the old runtime. Have that runtime
		// reconcile it before snapshotting, rather than adopting a stale PID here.
		// Confined backing and approvals cannot move to a new installation by
		// copying rows. This migration supports the deployed unconfined profile;
		// a future confined restore must use its own authority quarantine path.
		var domains int
		if tx.QueryRowContext(ctx, `SELECT count(*) FROM project_security_domains WHERE invalidated_reason IS NULL`).Scan(&domains) != nil || domains != 0 {
			return ErrDatabase
		}
		var successors int
		if tx.QueryRowContext(ctx, `SELECT count(*) FROM session_successor_slots`).Scan(&successors) != nil || successors != 0 {
			return ErrDatabase
		}
		now := db.FormatTime(time.Now())
		if _, err := tx.ExecContext(ctx, `UPDATE sessions SET stop_requested_at=?,workspace_stop_generation=generation,updated_at=? WHERE removed_at IS NULL`, now, now); err != nil {
			return ErrDatabase
		}
		return db.AppendAudit(ctx, tx, db.AuditEvent{Type: "workspace.migration_prepared", Details: "offline snapshot recorded intentional session stops before encrypted import"})
	})
	if err != nil {
		return ErrDatabase
	}
	return nil
}
