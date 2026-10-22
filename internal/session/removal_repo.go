package session

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/noisefloor-oss/deck/internal/db"
)

type RemovalAttempt struct {
	SessionID            string
	Nonce                string
	ControllerVerified   bool
	State                string
	ExecutorToken        *string
	ProcessStopCompleted bool
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

const (
	removalStarting  = "starting"
	removalExecuting = "executing"
	removalCompleted = "completed"
)

// BeginRemovalAttempt retires the exact row before any runtime effect. A
// concurrent recovery or controller mutation wins the compare-and-swap and
// forces the caller to re-observe; once this claim wins, no successor can
// publish over it. Record-only removals complete in this same transaction.
func (r *Repository) BeginRemovalAttempt(ctx context.Context, s *Session, nonce string,
	controllerVerified bool) (bool, error) {
	if s == nil || nonce == "" {
		return false, ErrInvalidRecoveryCandidate
	}
	applied := false
	err := r.db.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		now := db.FormatTime(time.Now())
		res, err := tx.ExecContext(ctx, `
			UPDATE sessions SET removed_at = ?, updated_at = ?,
				status = CASE WHEN status IN (?, ?) THEN ? ELSE status END
			WHERE id = ? AND removed_at IS NULL AND controller_revision = ?
			  AND NOT EXISTS (
				SELECT 1 FROM session_stop_attempts a
				WHERE a.session_id = sessions.id AND a.state = ?
			  )`,
			now, now, StatusStarting, StatusRunning, StatusInterrupted,
			s.ID, s.ControllerRevision, stopExecuting)
		if err != nil {
			return fmt.Errorf("session %s: claim removal: %w", s.ID, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			var executing int
			err := tx.QueryRowContext(ctx, `
				SELECT 1 FROM session_stop_attempts
				WHERE session_id = ? AND state = ?`, s.ID, stopExecuting).Scan(&executing)
			if err == nil {
				return ErrControllerActionInProgress
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			var one int
			if err := tx.QueryRowContext(ctx, `SELECT 1 FROM sessions WHERE id = ?`, s.ID).Scan(&one); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return ErrNotFound
				}
				return err
			}
			return nil
		}
		if controllerVerified {
			var prior int
			if err := tx.QueryRowContext(ctx, `
				SELECT EXISTS (
				  SELECT 1 FROM session_successor_slots
				  WHERE session_id = ?
				)`, s.ID).Scan(&prior); err != nil {
				return err
			}
			// Once any recovery action targeted this generation its exact-looking
			// predecessor is historical, never the current controller.
			controllerVerified = prior == 0
		}

		state := removalCompleted
		verified := 0
		if controllerVerified {
			state = removalStarting
			verified = 1
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO session_removal_attempts (
				session_id, nonce, controller_verified, state, executor_token,
				process_stop_completed,
				created_at, updated_at)
			VALUES (?, ?, ?, ?, NULL, 0, ?, ?)`,
			s.ID, nonce, verified, state, now, now); err != nil {
			return fmt.Errorf("session %s: record removal claim: %w", s.ID, err)
		}
		applied = true
		detail := "removed by user; no verified current controller, record-only"
		if controllerVerified {
			detail = "removed by user; exact current-controller stop durably claimed"
		}
		if err := db.AppendAudit(ctx, tx, db.AuditEvent{
			SessionID: &s.ID, Type: AuditRemoved, Details: detail,
		}); err != nil {
			return err
		}
		if !controllerVerified {
			return db.AppendAudit(ctx, tx, db.AuditEvent{
				SessionID: &s.ID,
				Type:      AuditRemovalCompleted,
				Details:   "record-only removal completed without a runtime effect",
			})
		}
		return nil
	})
	return applied, err
}

func (r *Repository) OpenRemovalAttempt(ctx context.Context, sessionID string) (*RemovalAttempt, error) {
	row := r.db.Reader().QueryRowContext(ctx, removalAttemptSelect+`
		WHERE session_id = ? AND state IN (?, ?)`, sessionID, removalStarting,
		removalExecuting)
	attempt, err := scanRemovalAttempt(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return attempt, err
}

func (r *Repository) ListOpenRemovalAttempts(ctx context.Context) ([]*RemovalAttempt, error) {
	rows, err := r.db.Reader().QueryContext(ctx, removalAttemptSelect+`
		WHERE state IN (?, ?) ORDER BY created_at`, removalStarting,
		removalExecuting)
	if err != nil {
		return nil, fmt.Errorf("session: list removal claims: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []*RemovalAttempt
	for rows.Next() {
		attempt, err := scanRemovalAttempt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, attempt)
	}
	return out, rows.Err()
}

// ClaimRemovalExecution admits one effects executor. A stale executing owner
// may be replaced only after its bounded action lease elapsed; the new token
// fences the old executor from every later state transition.
func (r *Repository) ClaimRemovalExecution(ctx context.Context, attempt *RemovalAttempt,
	executorToken string, staleBefore *time.Time) (bool, error) {
	if attempt == nil || executorToken == "" {
		return false, ErrInvalidRecoveryCandidate
	}
	applied := false
	err := r.db.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		now := time.Now()
		query := `
			UPDATE session_removal_attempts
			SET state = ?, executor_token = ?, updated_at = ?
			WHERE session_id = ? AND nonce = ? AND state = ?`
		args := []any{removalExecuting, executorToken, db.FormatTime(now),
			attempt.SessionID, attempt.Nonce, removalStarting}
		if staleBefore != nil {
			query = `
				UPDATE session_removal_attempts
				SET state = ?, executor_token = ?, updated_at = ?
				WHERE session_id = ? AND nonce = ?
				  AND (state = ? OR (state = ? AND updated_at <= ?))`
			args = append(args, removalExecuting, db.FormatTime(*staleBefore))
		}
		res, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("session %s: claim removal execution: %w", attempt.SessionID, err)
		}
		n, err := res.RowsAffected()
		if err != nil || n == 0 {
			return err
		}
		applied = true
		return db.AppendAudit(ctx, tx, db.AuditEvent{
			SessionID: &attempt.SessionID, Type: AuditRemovalExecuting,
			Details: "exact current-controller removal execution claimed",
		})
	})
	if applied && err == nil {
		attempt.State = removalExecuting
		attempt.ExecutorToken = &executorToken
		attempt.UpdatedAt = time.Now()
	}
	return applied, err
}

func (r *Repository) RefreshRemovalExecution(ctx context.Context, attempt *RemovalAttempt) (bool, error) {
	if attempt == nil || attempt.ExecutorToken == nil {
		return false, ErrInvalidRecoveryCandidate
	}
	now := time.Now()
	applied := false
	err := r.db.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE session_removal_attempts SET updated_at = ?
			WHERE session_id = ? AND nonce = ? AND state = ? AND executor_token = ?`,
			db.FormatTime(now), attempt.SessionID, attempt.Nonce, removalExecuting,
			*attempt.ExecutorToken)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		applied = n == 1
		return err
	})
	if err == nil && applied {
		attempt.UpdatedAt = now
	}
	return applied, err
}

func (r *Repository) RecordRemovalProcessStopped(ctx context.Context,
	attempt *RemovalAttempt) (bool, error) {
	if attempt == nil || attempt.ExecutorToken == nil {
		return false, ErrInvalidRecoveryCandidate
	}
	applied := false
	err := r.db.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE session_removal_attempts
			SET process_stop_completed = 1, updated_at = ?
			WHERE session_id = ? AND nonce = ? AND state = ?
			  AND executor_token = ? AND process_stop_completed = 0`,
			db.FormatTime(time.Now()), attempt.SessionID, attempt.Nonce,
			removalExecuting, *attempt.ExecutorToken)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil || n == 0 {
			return err
		}
		applied = true
		return db.AppendAudit(ctx, tx, db.AuditEvent{
			SessionID: &attempt.SessionID, Type: AuditRemovalProcessStopped,
			Details: "exact current controller process stop durably observed complete",
		})
	})
	if applied && err == nil {
		attempt.ProcessStopCompleted = true
	}
	return applied, err
}

func (r *Repository) FinishRemovalAttempt(ctx context.Context, attempt *RemovalAttempt,
	stopped bool) (bool, error) {
	if attempt == nil || attempt.ExecutorToken == nil || *attempt.ExecutorToken == "" {
		return false, ErrInvalidRecoveryCandidate
	}
	applied := false
	err := r.db.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE session_removal_attempts SET state = ?, updated_at = ?
			WHERE session_id = ? AND nonce = ? AND state = ? AND executor_token = ?`,
			removalCompleted, db.FormatTime(time.Now()), attempt.SessionID,
			attempt.Nonce, removalExecuting, *attempt.ExecutorToken)
		if err != nil {
			return fmt.Errorf("session %s: finish removal claim: %w", attempt.SessionID, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
		applied = true
		detail := "removal completed without replaying an unverifiable runtime effect"
		if stopped {
			detail = "removal completed after exact current controller stop was established"
		}
		return db.AppendAudit(ctx, tx, db.AuditEvent{
			SessionID: &attempt.SessionID,
			Type:      AuditRemovalCompleted,
			Details:   detail,
		})
	})
	return applied, err
}

const removalAttemptSelect = `SELECT session_id, nonce, controller_verified,
	state, executor_token, process_stop_completed, created_at, updated_at
	FROM session_removal_attempts `

func scanRemovalAttempt(row scanner) (*RemovalAttempt, error) {
	var attempt RemovalAttempt
	var verified, processStopped int
	var created, updated string
	if err := row.Scan(&attempt.SessionID, &attempt.Nonce, &verified,
		&attempt.State, &attempt.ExecutorToken, &processStopped,
		&created, &updated); err != nil {
		return nil, err
	}
	attempt.ControllerVerified = verified == 1
	attempt.ProcessStopCompleted = processStopped == 1
	if !attempt.ControllerVerified && (attempt.State != removalCompleted ||
		attempt.ExecutorToken != nil || attempt.ProcessStopCompleted) {
		return nil, errors.New("session: record-only removal has invalid execution state")
	}
	if attempt.ControllerVerified &&
		(attempt.State == removalExecuting || attempt.State == removalCompleted) &&
		(attempt.ExecutorToken == nil || *attempt.ExecutorToken == "") {
		return nil, errors.New("session: removal attempt is missing its executor fence")
	}
	var err error
	if attempt.CreatedAt, err = time.Parse(db.TimeFormat, created); err != nil {
		return nil, err
	}
	if attempt.UpdatedAt, err = time.Parse(db.TimeFormat, updated); err != nil {
		return nil, err
	}
	return &attempt, nil
}
