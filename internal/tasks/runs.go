package tasks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/noisefloor-oss/deck/internal/audit"
	"github.com/noisefloor-oss/deck/internal/db"
)

// Run statuses (taskmaster_runs.status CHECK constraint).
const (
	RunRunning = "running"
	RunExited  = "exited"
)

// Audit event types for the generation lifecycle.
const (
	AuditRunStarted = string(audit.TaskmasterRunStarted)
	AuditRunExited  = string(audit.TaskmasterRunExited)
)

// ErrGenerationRunning reports a second generation requested while one is
// already running for the project. The partial
// unique index idx_taskmaster_one_running is the race-safe authority.
var ErrGenerationRunning = errors.New("taskmaster: a generation is already running for this project")

// ErrRunNotFound reports an unknown run id.
var ErrRunNotFound = errors.New("taskmaster: run not found")

// Run is one recorded generation execution. The row is the durable record:
// it is committed BEFORE the tmux session is created, and it is
// what a restart reconciles against observed tmux state.
type Run struct {
	ID        string   `json:"id"`
	ProjectID string   `json:"project_id"`
	Argv      []string `json:"argv"`
	// PRD is the user-selected filename under the engine-selected docs root.
	PRD *string `json:"prd"`
	// Status is "running" or "exited" — observed process states only.
	Status   string  `json:"status"`
	ExitCode *int    `json:"exit_code"`
	Stdout   *string `json:"stdout,omitempty"`
	Stderr   *string `json:"stderr,omitempty"`
	// StartedAt and FinishedAt are db.TimeFormat strings; DurationMS is
	// their difference, populated on read so the API reports duration
	// explicitly rather than leaving every client to recompute.
	StartedAt  string  `json:"started_at"`
	FinishedAt *string `json:"finished_at,omitempty"`
	DurationMS *int64  `json:"duration_ms,omitempty"`
	// TmuxSessionName identifies the managed job session (attach rides the
	// terminal transport). Internal: it is a namespace detail, not API.
	TmuxSessionName *string `json:"-"`
}

// RunsRepository is the taskmaster_runs table access. Every state change
// appends its audit event in the same transaction.
type RunsRepository struct {
	db *db.DB
}

// NewRunsRepository wires the repository.
func NewRunsRepository(d *db.DB) *RunsRepository {
	return &RunsRepository{db: d}
}

// CreateRun records a run as running. ErrGenerationRunning when the
// project already has one (the partial unique index decides races).
func (r *RunsRepository) CreateRun(ctx context.Context, run *Run) error {
	argv, err := json.Marshal(run.Argv)
	if err != nil {
		return fmt.Errorf("taskmaster: encode argv: %w", err)
	}
	err = r.db.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO taskmaster_runs
			  (id, project_id, argv, prd_path, tmux_session_name, status, started_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			run.ID, run.ProjectID, string(argv), run.PRD, run.TmuxSessionName,
			RunRunning, run.StartedAt); err != nil {
			// The driver names the column, not the partial index, in the
			// violation message — same documented-brittle translation the
			// project registry uses.
			if strings.Contains(err.Error(), "UNIQUE constraint failed: taskmaster_runs.project_id") {
				return ErrGenerationRunning
			}
			return fmt.Errorf("taskmaster: insert run: %w", err)
		}
		return db.AppendAudit(ctx, tx, db.AuditEvent{
			Type:    AuditRunStarted,
			Details: fmt.Sprintf("run=%s project=%s", run.ID, run.ProjectID),
		})
	})
	return err
}

// MarkExited records the observed exit: code (nullable when unobserved),
// captured output, and the finish time — with the audit event in the same
// transaction. The bool reports whether the row was still running; a lost
// race with another finalizer is not an error.
func (r *RunsRepository) MarkExited(ctx context.Context, id string, exitCode *int, stdout, stderr *string, finishedAt time.Time) (bool, error) {
	var changed bool
	err := r.db.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE taskmaster_runs
			SET status = ?, exit_code = ?, stdout = ?, stderr = ?, finished_at = ?
			WHERE id = ? AND status = ?`,
			RunExited, exitCode, stdout, stderr,
			db.FormatTime(finishedAt), id, RunRunning)
		if err != nil {
			return fmt.Errorf("taskmaster: mark exited: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("taskmaster: mark exited count: %w", err)
		}
		changed = n == 1
		if !changed {
			return nil
		}
		code := "unobserved"
		if exitCode != nil {
			code = fmt.Sprintf("%d", *exitCode)
		}
		return db.AppendAudit(ctx, tx, db.AuditEvent{
			Type:    AuditRunExited,
			Details: fmt.Sprintf("run=%s exit=%s", id, code),
		})
	})
	return changed, err
}

const runColumns = `id, project_id, argv, prd_path, tmux_session_name,
	status, exit_code, stdout, stderr, started_at, finished_at`

// GetRun loads one run.
func (r *RunsRepository) GetRun(ctx context.Context, id string) (*Run, error) {
	row := r.db.Reader().QueryRowContext(ctx,
		`SELECT `+runColumns+` FROM taskmaster_runs WHERE id = ?`, id)
	run, err := scanRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrRunNotFound
	}
	return run, err
}

// ListRuns returns a project's runs, newest first, capped by limit.
func (r *RunsRepository) ListRuns(ctx context.Context, projectID string, limit int) ([]*Run, error) {
	rows, err := r.db.Reader().QueryContext(ctx,
		`SELECT `+runColumns+` FROM taskmaster_runs
		 WHERE project_id = ? ORDER BY started_at DESC, id DESC LIMIT ?`,
		projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("taskmaster: list runs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanRuns(rows)
}

// ListRunning returns every run still recorded as running — the startup
// reconciliation set: jobs survive a service restart.
func (r *RunsRepository) ListRunning(ctx context.Context) ([]*Run, error) {
	rows, err := r.db.Reader().QueryContext(ctx,
		`SELECT `+runColumns+` FROM taskmaster_runs WHERE status = ?`, RunRunning)
	if err != nil {
		return nil, fmt.Errorf("taskmaster: list running: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanRuns(rows)
}

type runScanner interface {
	Scan(dest ...any) error
}

func scanRun(s runScanner) (*Run, error) {
	var run Run
	var argv string
	err := s.Scan(&run.ID, &run.ProjectID, &argv, &run.PRD, &run.TmuxSessionName,
		&run.Status, &run.ExitCode, &run.Stdout, &run.Stderr,
		&run.StartedAt, &run.FinishedAt)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(argv), &run.Argv); err != nil {
		return nil, fmt.Errorf("taskmaster: decode argv of run %s: %w", run.ID, err)
	}
	if run.FinishedAt != nil {
		started, serr := time.Parse(db.TimeFormat, run.StartedAt)
		finished, ferr := time.Parse(db.TimeFormat, *run.FinishedAt)
		if serr == nil && ferr == nil {
			d := finished.Sub(started).Milliseconds()
			run.DurationMS = &d
		}
	}
	return &run, nil
}

func scanRuns(rows *sql.Rows) ([]*Run, error) {
	var runs []*Run
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("taskmaster: scan runs: %w", err)
	}
	if runs == nil {
		runs = []*Run{}
	}
	return runs, nil
}
