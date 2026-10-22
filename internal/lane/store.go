package lane

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/noisefloor-oss/deck/internal/db"
)

// Phase names how far a lane has come. Open is the only phase a session may
// work in; Removed means the engine already deleted the worktree and only the
// run's closing write is left.
const (
	PhaseClaimed = "claimed"
	PhaseRunning = "running"
	PhaseBound   = "bound"
	PhaseOpen    = "open"
	PhaseRemoved = "removed"
	PhaseFailed  = "failed"
)

// ErrSourceGone reports a lane whose source project is no longer registered.
var ErrSourceGone = errors.New("lane: the project is no longer registered")

// ErrNoLane reports that no lane is recorded for the task.
var ErrNoLane = errors.New("lane: no lane for this task")

// Lane is one stored lane. The two tokens are the engine's write
// capabilities for the claim and the run attempt: they are read and written
// only by this package and never marshal.
type Lane struct {
	ID         int64
	ProjectID  string
	Tag        string
	TaskID     int64
	Phase      string
	FailedStep string
	Agent      string
	// PriorStatus is the task's status when the lane claimed it. A lane that
	// moved the task to in-progress puts PriorStatus back when it is abandoned.
	PriorStatus string
	// StatusDone is set once the in-progress write has been made or found
	// unnecessary.
	StatusDone bool

	// PendingOp names an engine command that may have committed without its
	// response being stored: a renewal or the run finish. Its request id and
	// inputs are written before dispatch, and a retry replays exactly them.
	PendingOp        string
	PendingRequestID string
	PendingRevision  int64

	ClaimToken   int64
	RunID        int64
	Attempt      int64
	AttemptToken int64

	Branch        string
	WorktreePath  string
	BaseCommit    string
	LaneProjectID string

	ReviewAgent       string
	ReviewRequestedAt time.Time

	RenewedAt time.Time
	CreatedAt time.Time
}

// Store persists lanes in the control database.
type Store struct {
	db *db.DB
}

func NewStore(d *db.DB) *Store { return &Store{db: d} }

const laneColumns = `id, project_id, tag, task_id, phase, failed_step, agent, prior_status, status_done,
	pending_op, pending_request_id, pending_revision,
	claim_token, run_id, attempt, attempt_token, branch, worktree_path, base_commit,
	COALESCE(lane_project_id, ''), review_agent, review_requested_at, renewed_at, created_at`

type rowScanner interface{ Scan(...any) error }

func scanLane(s rowScanner) (*Lane, error) {
	var l Lane
	var requested, renewed, created string
	var statusDone int
	if err := s.Scan(&l.ID, &l.ProjectID, &l.Tag, &l.TaskID, &l.Phase, &l.FailedStep, &l.Agent, &l.PriorStatus, &statusDone, &l.PendingOp, &l.PendingRequestID, &l.PendingRevision,
		&l.ClaimToken, &l.RunID, &l.Attempt, &l.AttemptToken, &l.Branch, &l.WorktreePath, &l.BaseCommit,
		&l.LaneProjectID, &l.ReviewAgent, &requested, &renewed, &created); err != nil {
		return nil, err
	}
	l.StatusDone = statusDone != 0
	var err error
	if requested != "" {
		if l.ReviewRequestedAt, err = time.Parse(db.TimeFormat, requested); err != nil {
			return nil, fmt.Errorf("lane %d: review time: %w", l.ID, err)
		}
	}
	if l.RenewedAt, err = time.Parse(db.TimeFormat, renewed); err != nil {
		return nil, fmt.Errorf("lane %d: renewed time: %w", l.ID, err)
	}
	if l.CreatedAt, err = time.Parse(db.TimeFormat, created); err != nil {
		return nil, fmt.Errorf("lane %d: created time: %w", l.ID, err)
	}
	return &l, nil
}

// Create records a new lane in the claimed phase, before the engine is
// asked for anything, so a crash between a claim and its bookkeeping leaves
// a row to resume from.
func (s *Store) Create(ctx context.Context, l *Lane) error {
	return s.db.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		// Project removal counts lanes inside its own transaction, so a lane
		// is recorded only while its source is registered, and a source with
		// a lane cannot be removed.
		var registered int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects WHERE id = ? AND removed_at IS NULL`, l.ProjectID).Scan(&registered); err != nil {
			return err
		}
		if registered == 0 {
			return ErrSourceGone
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO tutorial_lanes (project_id, tag, task_id, phase, agent, renewed_at, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			l.ProjectID, l.Tag, l.TaskID, PhaseClaimed, l.Agent,
			db.FormatTime(l.RenewedAt), db.FormatTime(l.CreatedAt))
		if err != nil {
			return fmt.Errorf("lane: record: %w", err)
		}
		l.ID, err = res.LastInsertId()
		l.Phase = PhaseClaimed
		return err
	})
}

// Save writes every mutable field of the lane.
func (s *Store) Save(ctx context.Context, l *Lane) error {
	requested := ""
	if !l.ReviewRequestedAt.IsZero() {
		requested = db.FormatTime(l.ReviewRequestedAt)
	}
	return s.db.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var laneProject any
		if l.LaneProjectID != "" {
			laneProject = l.LaneProjectID
		}
		res, err := tx.ExecContext(ctx, `
			UPDATE tutorial_lanes SET phase = ?, failed_step = ?, agent = ?, prior_status = ?, status_done = ?,
				pending_op = ?, pending_request_id = ?, pending_revision = ?,
				claim_token = ?, run_id = ?, attempt = ?, attempt_token = ?,
				branch = ?, worktree_path = ?, base_commit = ?, lane_project_id = ?,
				review_agent = ?, review_requested_at = ?, renewed_at = ?
			WHERE id = ?`,
			l.Phase, l.FailedStep, l.Agent, l.PriorStatus, boolInt(l.StatusDone),
			l.PendingOp, l.PendingRequestID, l.PendingRevision,
			l.ClaimToken, l.RunID, l.Attempt, l.AttemptToken,
			l.Branch, l.WorktreePath, l.BaseCommit, laneProject,
			l.ReviewAgent, requested, db.FormatTime(l.RenewedAt), l.ID)
		if err != nil {
			return fmt.Errorf("lane %d: save: %w", l.ID, err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNoLane
		}
		return nil
	})
}

// Delete forgets a lane and, with it, the tokens.
func (s *Store) Delete(ctx context.Context, id int64) error {
	return s.db.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM tutorial_lanes WHERE id = ?`, id)
		return err
	})
}

// Get reads the lane recorded for one task, or ErrNoLane.
func (s *Store) Get(ctx context.Context, projectID, tag string, taskID int64) (*Lane, error) {
	l, err := scanLane(s.db.Reader().QueryRowContext(ctx,
		`SELECT `+laneColumns+` FROM tutorial_lanes WHERE project_id = ? AND tag = ? AND task_id = ?`,
		projectID, tag, taskID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoLane
	}
	return l, err
}

// ListProject reads every lane a project takes part in, as the project the
// lane works on or as the lane's own worktree project, oldest first.
func (s *Store) ListProject(ctx context.Context, projectID string) ([]*Lane, error) {
	return s.query(ctx, `SELECT `+laneColumns+` FROM tutorial_lanes
		WHERE project_id = ? OR lane_project_id = ? ORDER BY id`, projectID, projectID)
}

// ListRenewable reads every lane that holds a live run lease.
func (s *Store) ListRenewable(ctx context.Context) ([]*Lane, error) {
	return s.query(ctx, `SELECT `+laneColumns+` FROM tutorial_lanes
		WHERE run_id > 0 AND phase != ? ORDER BY id`, PhaseFailed)
}

func (s *Store) query(ctx context.Context, q string, args ...any) ([]*Lane, error) {
	rows, err := s.db.Reader().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("lane: list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []*Lane
	for rows.Next() {
		l, err := scanLane(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
