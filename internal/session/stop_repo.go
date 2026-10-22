package session

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/noisefloor-oss/deck/internal/db"
)

type StopAttempt struct {
	SessionID                string
	Nonce                    string
	SourceGeneration         int
	SourceIdentityMarker     string
	SourceControllerRevision int64
	Force                    bool
	State                    string
}

const (
	stopClaimed   = "claimed"
	stopExecuting = "executing"
	stopCompleted = "completed"
)

// BeginStopAttempt binds a user stop to one exact current controller and
// stamps the requested-exit exclusion before any signal. Successor admission
// advances the row away from running while holding the same row-local gate, so
// no second recovery-table interlock is needed.
func (r *Repository) BeginStopAttempt(ctx context.Context, s *Session, nonce string,
	force bool) (*StopAttempt, error) {
	if s == nil || nonce == "" || s.IdentityMarker == nil {
		return nil, ErrIdentityMismatch
	}
	attempt := &StopAttempt{
		SessionID: s.ID, Nonce: nonce, SourceGeneration: s.Generation,
		SourceIdentityMarker: *s.IdentityMarker, Force: force, State: stopClaimed,
	}
	err := r.db.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var open string
		err := tx.QueryRowContext(ctx, `
			SELECT nonce FROM session_stop_attempts
			WHERE session_id = ? AND state IN (?, ?)`,
			s.ID, stopClaimed, stopExecuting).Scan(&open)
		switch {
		case err == nil:
			return ErrControllerActionInProgress
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}

		now := db.FormatTime(time.Now())
		res, err := tx.ExecContext(ctx, `
			UPDATE sessions SET stop_requested_at = ?, updated_at = ?
			WHERE id = ? AND removed_at IS NULL AND status = ?
			  AND generation = ? AND identity_marker = ?
			  AND controller_revision = ?`,
			now, now, s.ID, StatusRunning, s.Generation, *s.IdentityMarker,
			s.ControllerRevision)
		if err != nil {
			return fmt.Errorf("session %s: claim stop: %w", s.ID, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrIdentityMismatch
		}
		attempt.SourceControllerRevision = s.ControllerRevision + 1
		forceValue := 0
		if force {
			forceValue = 1
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO session_stop_attempts (
				session_id, nonce, source_generation, source_identity_marker,
				source_controller_revision, force, state, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			s.ID, nonce, s.Generation, *s.IdentityMarker,
			attempt.SourceControllerRevision, forceValue, stopClaimed, now, now); err != nil {
			return fmt.Errorf("session %s: record stop claim: %w", s.ID, err)
		}
		return db.AppendAudit(ctx, tx, db.AuditEvent{
			SessionID: &s.ID, Type: AuditStopRequested,
			Details: fmt.Sprintf("exact controller stop claimed: force=%t", force),
		})
	})
	if err != nil {
		return nil, err
	}
	return attempt, nil
}

func (r *Repository) MarkStopExecuting(ctx context.Context, attempt *StopAttempt) (bool, error) {
	if attempt == nil {
		return false, ErrIdentityMismatch
	}
	applied := false
	err := r.db.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE session_stop_attempts SET state = ?, updated_at = ?
			WHERE session_id = ? AND nonce = ? AND state = ?
			  AND EXISTS (
				SELECT 1 FROM sessions s
				WHERE s.id = session_stop_attempts.session_id
				  AND s.removed_at IS NULL AND s.status = ?
				  AND s.generation = ? AND s.identity_marker = ?
				  AND s.controller_revision = ?
			  )`,
			stopExecuting, db.FormatTime(time.Now()), attempt.SessionID,
			attempt.Nonce, stopClaimed, StatusRunning,
			attempt.SourceGeneration, attempt.SourceIdentityMarker,
			attempt.SourceControllerRevision)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		applied = n == 1
		if !applied {
			return nil
		}
		return db.AppendAudit(ctx, tx, db.AuditEvent{
			SessionID: &attempt.SessionID, Type: AuditStopExecuting,
			Details: "exact controller stop crossed the durable signal boundary",
		})
	})
	if applied && err == nil {
		attempt.State = stopExecuting
	}
	return applied, err
}

func (r *Repository) FinishStopAttempt(ctx context.Context, attempt *StopAttempt, outcome string,
	preserveStopRequest bool) error {
	if attempt == nil {
		return ErrIdentityMismatch
	}
	return r.db.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE session_stop_attempts SET state = ?, updated_at = ?
			WHERE session_id = ? AND nonce = ? AND state IN (?, ?)`,
			stopCompleted, db.FormatTime(time.Now()), attempt.SessionID,
			attempt.Nonce, stopClaimed, stopExecuting)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil || n == 0 {
			return err
		}
		if !preserveStopRequest {
			res, err := tx.ExecContext(ctx, `
				UPDATE sessions SET stop_requested_at = NULL, updated_at = ?
				WHERE id = ? AND removed_at IS NULL AND generation = ?
				  AND identity_marker = ?
				  AND stop_requested_at IS NOT NULL`,
				db.FormatTime(time.Now()), attempt.SessionID,
				attempt.SourceGeneration, attempt.SourceIdentityMarker)
			if err != nil {
				return err
			}
			if n, err := res.RowsAffected(); err != nil {
				return err
			} else if n > 1 {
				return fmt.Errorf("session %s: cleared %d stop request rows", attempt.SessionID, n)
			}
		}
		return db.AppendAudit(ctx, tx, db.AuditEvent{
			SessionID: &attempt.SessionID, Type: AuditStopCompleted,
			Details: outcome,
		})
	})
}

func (r *Repository) ListOpenStopAttempts(ctx context.Context) ([]*StopAttempt, error) {
	rows, err := r.db.Reader().QueryContext(ctx, `
		SELECT session_id, nonce, source_generation, source_identity_marker,
			source_controller_revision, force, state
		FROM session_stop_attempts WHERE state IN (?, ?) ORDER BY created_at`,
		stopClaimed, stopExecuting)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*StopAttempt
	for rows.Next() {
		var attempt StopAttempt
		var force int
		if err := rows.Scan(&attempt.SessionID, &attempt.Nonce,
			&attempt.SourceGeneration, &attempt.SourceIdentityMarker,
			&attempt.SourceControllerRevision, &force, &attempt.State); err != nil {
			return nil, err
		}
		attempt.Force = force == 1
		out = append(out, &attempt)
	}
	return out, rows.Err()
}
