package lease

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/noisefloor-oss/deck/internal/db"
)

// Manager owns lease transitions. Every one runs in a single transaction
// with its audit event, so a takeover and its record cannot separate.
type Manager struct {
	db  *db.DB
	ttl time.Duration
	now func() time.Time
}

// NewManager wires the lease manager against the control database.
func NewManager(d *db.DB) *Manager {
	return &Manager{db: d, ttl: TTL, now: time.Now}
}

// Acquire claims the lease for controllerID.
//
// It succeeds when the session has no lease, when the caller already holds
// it (a renewal), or when the recorded holder's lease has expired. It
// refuses a live lease held by someone else with ErrHeld — a second browser
// gets a takeover screen, never silent control.
//
// deliberate=true is the user explicitly taking control from a live
// holder: it always increments the fencing token, instantly invalidating
// every frame the displaced controller sends.
func (m *Manager) Acquire(ctx context.Context, sessionID, controllerID string, deliberate bool) (*Lease, error) {
	if sessionID == "" || controllerID == "" {
		return nil, errors.New("lease: session and controller ids are required")
	}
	var out *Lease
	err := m.db.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		cur, err := getTx(ctx, tx, sessionID)
		if errors.Is(err, ErrNotFound) {
			cur = nil
		} else if err != nil {
			return err
		}
		t, err := decideAcquire(cur, sessionID, controllerID, deliberate, m.now(), m.ttl)
		if err != nil {
			return err
		}
		out, err = m.write(ctx, tx, t.next, t.auditType, t.details)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Heartbeat renews a lease the caller still holds, extending expiry by the
// TTL. A caller whose token no longer matches has been fenced out, and a
// caller whose lease already EXPIRED is told so rather than reviving it. The
// fifteen-second bound is a promise to every other browser that the lease
// became claimable, and a late heartbeat must not silently retract it
// (another controller may already be mid-reclaim). The returning holder
// calls Acquire, which records the reclaim honestly.
func (m *Manager) Heartbeat(ctx context.Context, sessionID, controllerID string, token int64) (*Lease, error) {
	var out *Lease
	err := m.db.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		cur, err := getTx(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		t, err := decideHeartbeat(cur, controllerID, token, m.now(), m.ttl)
		if err != nil {
			return err
		}
		out, err = m.write(ctx, tx, t.next, t.auditType, t.details)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Validate is the write-path check: it reports nil only when this
// controller still holds the lease with this exact token and the lease has
// not expired. Every raw-input frame passes through here under the session
// write gate before anything reaches the terminal.
func (m *Manager) Validate(ctx context.Context, sessionID, controllerID string, token int64) error {
	cur, err := m.Get(ctx, sessionID)
	if err != nil {
		return err
	}
	return decideValidate(cur, controllerID, token, m.now())
}

// Get reads the current lease.
func (m *Manager) Get(ctx context.Context, sessionID string) (*Lease, error) {
	row := m.db.Reader().QueryRowContext(ctx,
		`SELECT session_id, controller_id, fencing_token, last_heartbeat, expires_at
		 FROM controller_leases WHERE session_id = ?`, sessionID)
	return scan(row)
}

// Invalidate voids a session's lease unconditionally: no controller ID, no
// fencing token. Native resume calls it BEFORE the pane is respawned, so
// from that moment no outstanding token can put bytes into the new process.
// The caller orders it first precisely so a crash anywhere later in resume
// cannot leave a live token pointing at the new generation. An
// absent lease is a no-op, not an error: the goal state is "no valid token
// exists".
func (m *Manager) Invalidate(ctx context.Context, sessionID, reason string) error {
	return m.db.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM controller_leases WHERE session_id = ?`, sessionID); err != nil {
			return fmt.Errorf("lease: invalidate %s: %w", sessionID, err)
		}
		return db.AppendAudit(ctx, tx, db.AuditEvent{
			SessionID: &sessionID, Type: AuditInvalidated, Details: reason,
		})
	})
}

// write upserts the decided lease and, when auditType is non-empty, records
// the event in the same transaction. Renewals pass an empty type: a
// heartbeat every five seconds is not an auditable action, and recording
// each one would bury the takeovers that matter.
func (m *Manager) write(ctx context.Context, tx *sql.Tx, l Lease,
	auditType, details string) (*Lease, error) {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO controller_leases (session_id, controller_id, fencing_token, last_heartbeat, expires_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(session_id) DO UPDATE SET
			controller_id = excluded.controller_id,
			fencing_token = excluded.fencing_token,
			last_heartbeat = excluded.last_heartbeat,
			expires_at = excluded.expires_at`,
		l.SessionID, l.ControllerID, l.FencingToken,
		db.FormatTime(l.LastHeartbeat), db.FormatTime(l.ExpiresAt),
	); err != nil {
		return nil, fmt.Errorf("lease: write %s: %w", l.SessionID, err)
	}
	if auditType != "" {
		if err := db.AppendAudit(ctx, tx, db.AuditEvent{
			SessionID: &l.SessionID, Type: auditType,
			Details: fmt.Sprintf("controller=%s token=%d %s", l.ControllerID, l.FencingToken, details),
		}); err != nil {
			return nil, err
		}
	}
	return &l, nil
}

func getTx(ctx context.Context, tx *sql.Tx, sessionID string) (*Lease, error) {
	row := tx.QueryRowContext(ctx,
		`SELECT session_id, controller_id, fencing_token, last_heartbeat, expires_at
		 FROM controller_leases WHERE session_id = ?`, sessionID)
	return scan(row)
}

func scan(row *sql.Row) (*Lease, error) {
	var l Lease
	var hb, exp string
	err := row.Scan(&l.SessionID, &l.ControllerID, &l.FencingToken, &hb, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lease: scan: %w", err)
	}
	if l.LastHeartbeat, err = time.Parse(db.TimeFormat, hb); err != nil {
		return nil, fmt.Errorf("lease %s: last_heartbeat: %w", l.SessionID, err)
	}
	if l.ExpiresAt, err = time.Parse(db.TimeFormat, exp); err != nil {
		return nil, fmt.Errorf("lease %s: expires_at: %w", l.SessionID, err)
	}
	return &l, nil
}
