// Package audit is the structured audit-event system: the
// canonical event taxonomy for lifecycle transitions, input operations,
// and security events, plus the typed append/query repository over the
// audit_events table.
//
// The invariants come with the table (migration 001): every state change
// appends its event in the SAME transaction — Append exists
// only on a *sql.Tx so an event cannot be committed independently of the
// change it describes — and Details carries identifiers and transitions
// only, never prompt bodies, terminal contents, environment secrets, or
// authentication material. The autoincrementing id is the
// total order.
package audit

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/noisefloor-oss/deck/internal/db"
)

// Type is one canonical audit event type. The names are the contract:
// they are what the trail is queried by, so they are centralized here
// rather than scattered as package-local strings.
type Type string

const (
	// Workspace lifecycle.
	WorkspaceStopRequested Type = "workspace.stop_requested"

	// Session lifecycle.
	SessionCreated                 Type = "session.created"
	SessionRunning                 Type = "session.running"
	SessionLaunchFailed            Type = "session.launch_failed"
	SessionStopRequested           Type = "session.stop_requested"
	SessionStopExecuting           Type = "session.stop_executing"
	SessionStopCompleted           Type = "session.stop_completed"
	SessionStopUnobserved          Type = "session.stop_unobserved"
	SessionStopSurvived            Type = "session.stop_survived"
	SessionExited                  Type = "session.exited"
	SessionInterrupted             Type = "session.interrupted"
	SessionResumed                 Type = "session.resumed"
	SessionResumeAttempt           Type = "session.resume_attempt"
	SessionResumeProbationCleared  Type = "session.resume_probation_cleared"
	SessionRecoveryLegacyConverged Type = "session.recovery_legacy_converged"
	SessionRemoved                 Type = "session.removed"
	SessionRemovalExecuting        Type = "session.removal_executing"
	SessionRemovalProcessStopped   Type = "session.removal_process_stopped"
	SessionRemovalCompleted        Type = "session.removal_completed"
	SessionRecordForgotten         Type = "session.record_forgotten"
	SessionStaleRecordDiscarded    Type = "session.stale_record_discarded"
	SessionRenamed                 Type = "session.renamed"
	SessionIdentityCaptured        Type = "session.identity_captured"
	SessionIdentityConfirmed       Type = "session.identity_confirmed"
	SessionIdentityCaptureRefused  Type = "session.identity_capture_refused"
	SessionIdentityCaptureLost     Type = "session.identity_capture_lost"
	SessionIdentityCaptureCleared  Type = "session.identity_capture_cleared"
	SessionAttention               Type = "session.needs_attention"

	// Controller lease — control takeover is auditable.
	LeaseAcquired    Type = "lease.acquired"
	LeaseTakeover    Type = "lease.takeover"
	LeaseReleased    Type = "lease.released"
	LeaseExpired     Type = "lease.expired_reclaimed"
	LeaseInvalidated Type = "lease.invalidated"

	// Project registry — project removal is auditable.
	ProjectCreated                Type = "project.created"
	ProjectRemoved                Type = "project.removed"
	ProjectRenamed                Type = "project.renamed"
	GitIdentityCreated            Type = "git_identity.created"
	GitIdentityUpdated            Type = "git_identity.updated"
	GitIdentityDeleted            Type = "git_identity.deleted"
	GitIdentityDefaultChanged     Type = "git_identity.default_changed"
	ProjectGitIdentityChanged     Type = "project.git_identity_changed"
	ProjectSecurityDomainCreated  Type = "project.security_domain_created"
	ProjectPolicyRevisionAppended Type = "project.policy_revision_appended"

	// Reconciler observations.
	ReconUnknownPane Type = "recon.unknown_pane"

	// Taskmaster generation jobs.
	TaskmasterRunStarted Type = "taskmaster.run_started"
	TaskmasterRunExited  Type = "taskmaster.run_exited"
)

// Event is one audit_events row.
type Event struct {
	ID        int64   `json:"id"`
	Timestamp string  `json:"timestamp"`
	Type      string  `json:"type"`
	SessionID *string `json:"session_id,omitempty"`
	Details   *string `json:"details,omitempty"`
}

// Repository appends and queries audit events.
type Repository struct {
	db *db.DB
}

// NewRepository wires the repository.
func NewRepository(d *db.DB) *Repository {
	return &Repository{db: d}
}

// Append writes an event inside an existing transaction — the same
// transaction as the state change it describes. There is
// deliberately no non-transactional append.
func (r *Repository) Append(ctx context.Context, tx *sql.Tx, sessionID *string, t Type, details string) error {
	return db.AppendAudit(ctx, tx, db.AuditEvent{
		SessionID: sessionID,
		Type:      string(t),
		Details:   details,
	})
}

// Query filters an event listing.
type Query struct {
	SessionID string
	Type      string
	// Limit caps the result; zero means the default page.
	Limit int
}

const defaultLimit = 200

// List returns events newest-last in id order (the total order), capped
// by the query's limit. An empty filter lists the recent global trail —
// this is a single-user product, and the trail exists to be inspected.
func (r *Repository) List(ctx context.Context, q Query) ([]Event, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	where := "1=1"
	args := []any{}
	if q.SessionID != "" {
		where += " AND session_id = ?"
		args = append(args, q.SessionID)
	}
	if q.Type != "" {
		where += " AND event_type = ?"
		args = append(args, q.Type)
	}
	args = append(args, limit)
	rows, err := r.db.Reader().QueryContext(ctx, `
		SELECT id, session_id, event_type, details, created_at
		FROM audit_events WHERE `+where+` ORDER BY id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("audit: list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var events []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.SessionID, &e.Type, &e.Details, &e.Timestamp); err != nil {
			return nil, fmt.Errorf("audit: scan: %w", err)
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("audit: rows: %w", err)
	}
	// The page was taken newest-first for the LIMIT; present it in the
	// trail's own order.
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}
	if events == nil {
		events = []Event{}
	}
	return events, nil
}
