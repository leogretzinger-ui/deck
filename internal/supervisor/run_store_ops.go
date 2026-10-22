package supervisor

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"time"
)

// insertRunSQL creates a fresh prepared reservation. All authority fields are
// bound in the same transaction that allocates the run identity.
const insertRunSQL = `
	INSERT INTO runs (
		run_id, sandbox_generation,
		application_session_id, application_session_generation,
		launch_request_digest, launch_request_json,
		pty_binding_digest, pty_binding_json,
		project_policy_revision, provider_profile_revision,
		unit_name, unit_property_digest, boot_id,
		launch_ticket_digest, launch_ticket_json,
		phase, needs_attention, needs_attention_reason,
		terminal_outcome, empty_cgroup_evidence_digest, cleanup_state,
		created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

// ReserveInput names the exact immutable bound facts for one run. The launch
// request and PTY binding are the package's canonical revisions. The store
// generates the run identities and derives the transient unit contract.
type ReserveInput struct {
	ApplicationSessionID         string
	ApplicationSessionGeneration int64
	LaunchRequest                *LaunchRequestRevision
	PTYBinding                   *PTYBindingRevision
}

// Reserve atomically reserves exactly one run for the application-session
// pair, before any external launch side effect. A losing or replaying call
// returns the existing record only when every caller-bound immutable fact
// agrees. Any disagreement is a stable
// ErrRunConflict that never mutates, parks, widens, or replaces the existing
// run; parking is a deliberate supervisor decision made through
// MarkNeedsAttention, never a side effect of untrusted input.
func (s *Store) Reserve(ctx context.Context, in ReserveInput) (*Run, error) {
	request, binding, err := validateReserveInput(in)
	if err != nil {
		return nil, err
	}
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return nil, err
	}

	runID, sandboxGeneration, err := newRunIdentities()
	if err != nil {
		_ = tx.Rollback()
		return nil, ErrStoreUnavailable
	}
	unit, err := DeriveTransientUnitProperties(TransientUnitPropertiesBinding{
		RunID:             runID,
		SandboxGeneration: sandboxGeneration,
	})
	if err != nil {
		_ = tx.Rollback()
		return nil, ErrStoreUnavailable
	}
	now := formatStoreTime(time.Now())
	_, err = tx.ExecContext(ctx, insertRunSQL,
		runID, sandboxGeneration,
		request.Request.ApplicationSessionID, request.Request.ApplicationSessionGeneration,
		request.Digest, request.CanonicalJSON,
		binding.Digest, binding.CanonicalJSON,
		request.Request.ProjectPolicyRevision, request.Request.ProviderProfileRevision,
		unit.UnitName, unit.Digest,
		binding.Binding.BootID,
		nil, nil,
		string(PhasePrepared), 0, nil, nil, nil, string(CleanupNone),
		now, now)
	if err == nil {
		if err := tx.Commit(); err != nil {
			return nil, mapStoreError(err)
		}
		return &Run{
			RunID:                        runID,
			SandboxGeneration:            sandboxGeneration,
			ApplicationSessionID:         request.Request.ApplicationSessionID,
			ApplicationSessionGeneration: request.Request.ApplicationSessionGeneration,
			LaunchRequestDigest:          request.Digest,
			LaunchRequestJSON:            request.CanonicalJSON,
			PTYBindingDigest:             binding.Digest,
			PTYBindingJSON:               binding.CanonicalJSON,
			ProjectPolicyRevision:        request.Request.ProjectPolicyRevision,
			ProviderProfileRevision:      request.Request.ProviderProfileRevision,
			UnitName:                     unit.UnitName,
			UnitPropertyDigest:           unit.Digest,
			BootID:                       binding.Binding.BootID,
			Phase:                        PhasePrepared,
			CleanupState:                 CleanupNone,
			CreatedAt:                    now,
			UpdatedAt:                    now,
		}, nil
	}
	if !isUniqueConstraint(err) {
		_ = tx.Rollback()
		return nil, mapStoreError(err)
	}

	// The reservation lost to an existing row for this key, or for this
	// deterministic unit name. A lost reservation never mutates or parks the
	// incumbent: the winner's authority is untouched, and an exact replay
	// still returns the existing record.
	existing, err := scanRunByKey(ctx, tx, request.Request.ApplicationSessionID, request.Request.ApplicationSessionGeneration)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	if existing != nil {
		_ = tx.Rollback()
		if existing.NeedsAttention {
			return nil, ErrNeedsAttention
		}
		if reservationFactsMatch(existing, request, binding) {
			return existing, nil
		}
		return nil, ErrRunConflict
	}
	unitOwner, err := scanRunByUnitName(ctx, tx, unit.UnitName)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	if unitOwner != nil {
		_ = tx.Rollback()
		return nil, ErrRunConflict
	}
	// A uniqueness violation we cannot attribute to an existing row (for
	// example a generated run id collision) is corruption, not a recoverable
	// condition.
	_ = tx.Rollback()
	return nil, ErrStoreCorrupt
}

func validateReserveInput(in ReserveInput) (*LaunchRequestRevision, *PTYBindingRevision, error) {
	if in.LaunchRequest == nil || in.PTYBinding == nil {
		return nil, nil, ErrInvalidArgument
	}
	if !isCanonicalUUIDv4(in.ApplicationSessionID) || in.ApplicationSessionGeneration < 1 {
		return nil, nil, ErrInvalidArgument
	}
	request, err := VerifyLaunchRequest(in.LaunchRequest.Digest, in.LaunchRequest.CanonicalJSON)
	if err != nil {
		return nil, nil, ErrInvalidArgument
	}
	if request.Request.ApplicationSessionID != in.ApplicationSessionID ||
		request.Request.ApplicationSessionGeneration != in.ApplicationSessionGeneration {
		return nil, nil, ErrInvalidArgument
	}
	binding, err := VerifyPTYBinding(in.PTYBinding.Digest, in.PTYBinding.CanonicalJSON)
	if err != nil {
		return nil, nil, ErrInvalidArgument
	}
	return request, binding, nil
}

func reservationFactsMatch(existing *Run, request *LaunchRequestRevision, binding *PTYBindingRevision) bool {
	return existing.LaunchRequestDigest == request.Digest &&
		existing.PTYBindingDigest == binding.Digest &&
		existing.ProjectPolicyRevision == request.Request.ProjectPolicyRevision &&
		existing.ProviderProfileRevision == request.Request.ProviderProfileRevision &&
		existing.BootID == binding.Binding.BootID
}

// Get returns the run reserved for the exact application-session pair.
func (s *Store) Get(ctx context.Context, sessionID string, generation int64) (*Run, error) {
	if !isCanonicalUUIDv4(sessionID) || generation < 1 {
		return nil, ErrInvalidArgument
	}
	row := s.db.QueryRowContext(ctx,
		`SELECT `+runsSelectColumns+` FROM runs WHERE application_session_id = ? AND application_session_generation = ?`,
		sessionID, generation)
	return scanStoreRow(row)
}

// GetByRunID returns the run with the exact supervisor-generated run identity.
func (s *Store) GetByRunID(ctx context.Context, runID string) (*Run, error) {
	if !isCanonicalUUIDv4(runID) {
		return nil, ErrInvalidArgument
	}
	row := s.db.QueryRowContext(ctx,
		`SELECT `+runsSelectColumns+` FROM runs WHERE run_id = ?`, runID)
	return scanStoreRow(row)
}

// GetByUnitName returns the run bound to the exact deterministic unit name.
func (s *Store) GetByUnitName(ctx context.Context, unitName string) (*Run, error) {
	if !validDeterministicUnitName(unitName) {
		return nil, ErrInvalidArgument
	}
	row := s.db.QueryRowContext(ctx,
		`SELECT `+runsSelectColumns+` FROM runs WHERE unit_name = ?`, unitName)
	return scanStoreRow(row)
}

// ListForReconciliation returns every run a supervisor restart must reconcile
// before new launch admission: every nonterminal row and every terminal row
// whose cleanup is not complete.
func (s *Store) ListForReconciliation(ctx context.Context) ([]*Run, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+runsSelectColumns+` FROM runs
		 WHERE phase <> 'terminal' OR cleanup_state = 'pending'
		 ORDER BY created_at`)
	if err != nil {
		return nil, mapStoreError(err)
	}
	defer func() { _ = rows.Close() }()

	var runs []*Run
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, mapStoreError(err)
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, mapStoreError(err)
	}
	return runs, nil
}

// TransitionToStarting durably crosses the one-way point before any
// service-manager call may be issued for the recorded unit.
func (s *Store) TransitionToStarting(ctx context.Context, runID string) (*Run, error) {
	return s.advancePhase(ctx, runID, PhasePrepared, PhaseStarting)
}

// TransitionToAuthorized is the one-way crash-safe compare-and-swap that
// durably persists the exact canonical launch ticket before any workload
// release. It cross-binds the ticket to the row's protected run, sandbox,
// launch-request, PTY, unit-name, and unit-property identities; the ticket
// carries its own opaque sandbox-plan digest and launcher-invocation identity.
// The row is ground truth, so a ticket canonicalized against any other binding
// fails closed. A parked row is checked first and returns ErrNeedsAttention
// before any replay, so exact retry is idempotent only for an unparked
// authorized row; a different ticket on an already-authorized run is a
// conflict that never mutates the incumbent.
func (s *Store) TransitionToAuthorized(ctx context.Context, runID string, ticket *LaunchTicketRevision) (*Run, error) {
	if !isCanonicalUUIDv4(runID) || ticket == nil {
		return nil, ErrInvalidArgument
	}
	var result *Run
	err := s.withWrite(ctx, func(tx *sql.Tx) error {
		current, err := scanRunByID(ctx, tx, runID)
		if err != nil {
			return err
		}
		if current == nil {
			return ErrRunNotFound
		}
		if current.NeedsAttention {
			return ErrNeedsAttention
		}
		if current.Phase == PhaseAuthorized {
			if current.LaunchTicketDigest == ticket.Digest && current.LaunchTicketJSON == ticket.CanonicalJSON {
				result = current
				return nil
			}
			return ErrRunConflict
		}
		if err := classifyPhaseTransition(current.Phase, PhaseStarting, PhaseAuthorized); err != nil {
			return err
		}
		if err := verifyLaunchTicketForRun(current, ticket.Digest, ticket.CanonicalJSON); err != nil {
			return err
		}
		if err := casRows(ctx, tx,
			`UPDATE runs SET phase = 'authorized',
			        launch_ticket_digest = ?, launch_ticket_json = ?, updated_at = ?
			 WHERE run_id = ? AND phase = 'starting' AND needs_attention = 0`,
			ticket.Digest, ticket.CanonicalJSON, formatStoreTime(time.Now()),
			runID); err != nil {
			return err
		}
		updated, err := scanRunByID(ctx, tx, runID)
		if err != nil {
			return err
		}
		result = updated
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// TransitionToRunning records that the exact unit, cgroup, launcher, request,
// PTY, and sandbox facts match the record. Running is reachable only from an
// authorized run: no workload may observe release before the ticket is
// durably persisted.
func (s *Store) TransitionToRunning(ctx context.Context, runID string) (*Run, error) {
	return s.advancePhase(ctx, runID, PhaseAuthorized, PhaseRunning)
}

// TransitionToTerminal records the content-free outcome together with the
// positive empty-cgroup evidence. The empty-cgroup evidence digest is
// required: terminal cannot be recorded on uncertainty, and cleanup cannot
// complete without it. A parked run may still record an observed terminal —
// that is bookkeeping, not resuming or replacing the run — so the attention
// flag survives and the run is then eligible for cleanup.
func (s *Store) TransitionToTerminal(ctx context.Context, runID string, outcome Outcome, emptyCGroupEvidenceDigest string) (*Run, error) {
	if !isCanonicalUUIDv4(runID) || !isValidOutcome(outcome) || !isLowerSHA256(emptyCGroupEvidenceDigest) {
		return nil, ErrInvalidArgument
	}
	var result *Run
	err := s.withWrite(ctx, func(tx *sql.Tx) error {
		current, err := scanRunByID(ctx, tx, runID)
		if err != nil {
			return err
		}
		if current == nil {
			return ErrRunNotFound
		}
		if err := classifyPhaseTransition(current.Phase, PhaseStarting, PhaseTerminal); err != nil {
			return err
		}
		if err := casRows(ctx, tx,
			`UPDATE runs SET phase = 'terminal', terminal_outcome = ?,
			        empty_cgroup_evidence_digest = ?, cleanup_state = 'pending', updated_at = ?
			 WHERE run_id = ? AND phase = ?`,
			string(outcome), emptyCGroupEvidenceDigest, formatStoreTime(time.Now()),
			runID, string(current.Phase)); err != nil {
			return err
		}
		updated, err := scanRunByID(ctx, tx, runID)
		if err != nil {
			return err
		}
		result = updated
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// MarkCleanupComplete records that the retained unit was released only after
// the terminal result and positive empty-cgroup evidence were durably stored
// first. Cleanup bookkeeping may finish on a parked terminal row.
func (s *Store) MarkCleanupComplete(ctx context.Context, runID string) (*Run, error) {
	if !isCanonicalUUIDv4(runID) {
		return nil, ErrInvalidArgument
	}
	var result *Run
	err := s.withWrite(ctx, func(tx *sql.Tx) error {
		current, err := scanRunByID(ctx, tx, runID)
		if err != nil {
			return err
		}
		if current == nil {
			return ErrRunNotFound
		}
		if current.Phase != PhaseTerminal {
			return ErrIllegalTransition
		}
		if current.CleanupState != CleanupPending {
			return ErrStaleTransition
		}
		if err := casRows(ctx, tx,
			`UPDATE runs SET cleanup_state = 'complete', updated_at = ?
			 WHERE run_id = ? AND phase = 'terminal' AND cleanup_state = 'pending'`,
			formatStoreTime(time.Now()), runID); err != nil {
			return err
		}
		updated, err := scanRunByID(ctx, tx, runID)
		if err != nil {
			return err
		}
		result = updated
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// MarkNeedsAttention is the deliberate supervisor parking decision. Reserve
// never parks a run on its own; the reconciler parks one whose workload state
// it cannot verify. The flag is monotonic: it is never cleared, a re-mark with
// the same reason is idempotent, and a different reason is a conflict. A
// parked run refuses launch resumption but can still record an observed
// terminal and complete cleanup.
func (s *Store) MarkNeedsAttention(ctx context.Context, runID string, reason AttentionReason) (*Run, error) {
	if !isCanonicalUUIDv4(runID) || !isValidAttentionReason(reason) {
		return nil, ErrInvalidArgument
	}
	var result *Run
	err := s.withWrite(ctx, func(tx *sql.Tx) error {
		current, err := scanRunByID(ctx, tx, runID)
		if err != nil {
			return err
		}
		if current == nil {
			return ErrRunNotFound
		}
		if current.NeedsAttention {
			if current.NeedsAttentionReason == string(reason) {
				result = current
				return nil
			}
			return ErrRunConflict
		}
		if err := casRows(ctx, tx,
			`UPDATE runs SET needs_attention = 1, needs_attention_reason = ?, updated_at = ?
			 WHERE run_id = ? AND needs_attention = 0`,
			string(reason), formatStoreTime(time.Now()), runID); err != nil {
			return err
		}
		updated, err := scanRunByID(ctx, tx, runID)
		if err != nil {
			return err
		}
		result = updated
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Store) advancePhase(ctx context.Context, runID string, minSource, target Phase) (*Run, error) {
	if !isCanonicalUUIDv4(runID) {
		return nil, ErrInvalidArgument
	}
	var result *Run
	err := s.withWrite(ctx, func(tx *sql.Tx) error {
		current, err := scanRunByID(ctx, tx, runID)
		if err != nil {
			return err
		}
		if current == nil {
			return ErrRunNotFound
		}
		if current.NeedsAttention {
			return ErrNeedsAttention
		}
		if err := classifyPhaseTransition(current.Phase, minSource, target); err != nil {
			return err
		}
		if err := casRows(ctx, tx,
			`UPDATE runs SET phase = ?, updated_at = ?
			 WHERE run_id = ? AND phase = ? AND needs_attention = 0`,
			string(target), formatStoreTime(time.Now()), runID, string(current.Phase)); err != nil {
			return err
		}
		updated, err := scanRunByID(ctx, tx, runID)
		if err != nil {
			return err
		}
		result = updated
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// classifyPhaseTransition is the closed lifecycle edge check. Reaching the
// target already, or moving backward, is stale; jumping over a required phase
// is illegal.
func classifyPhaseTransition(current, minSource, target Phase) error {
	switch {
	case current == target:
		return ErrStaleTransition
	case phaseOrder(current) > phaseOrder(target):
		return ErrStaleTransition
	case phaseOrder(current) < phaseOrder(minSource):
		return ErrIllegalTransition
	}
	return nil
}

func phaseOrder(p Phase) int {
	switch p {
	case PhasePrepared:
		return 0
	case PhaseStarting:
		return 1
	case PhaseAuthorized:
		return 2
	case PhaseRunning:
		return 3
	case PhaseTerminal:
		return 4
	}
	return -1
}

func isValidOutcome(o Outcome) bool {
	switch o {
	case OutcomeSuccess, OutcomeFailed, OutcomeStopped:
		return true
	}
	return false
}

func isValidAttentionReason(r AttentionReason) bool {
	switch r {
	case ReasonUnverifiedState:
		return true
	}
	return false
}

var deterministicUnitNameRE = regexp.MustCompile(`^[a-z0-9-]+\.service$`)

func validDeterministicUnitName(name string) bool {
	if len(name) > 255 || !deterministicUnitNameRE.MatchString(name) {
		return false
	}
	base := name[:len(name)-len(".service")]
	return len(base) > 0 && base[0] != '-' && base[len(base)-1] != '-'
}

func (s *Store) beginWrite(ctx context.Context) (*sql.Tx, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, mapStoreError(err)
	}
	return tx, nil
}

func (s *Store) withWrite(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.beginWrite(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return mapStoreError(err)
	}
	return nil
}

func casRows(ctx context.Context, tx *sql.Tx, query string, args ...any) error {
	res, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return mapStoreError(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return mapStoreError(err)
	}
	// Under BEGIN IMMEDIATE the row cannot change beneath the transaction, so
	// zero rows means the precondition was re-read wrong — a lost CAS, not an
	// unavailable store. Retrying can never succeed; the state is stale.
	if n != 1 {
		return ErrStaleTransition
	}
	return nil
}

func scanStoreRow(row *sql.Row) (*Run, error) {
	run, err := scanRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrRunNotFound
	}
	if err != nil {
		return nil, mapStoreError(err)
	}
	return run, nil
}

func scanRunByID(ctx context.Context, tx *sql.Tx, runID string) (*Run, error) {
	row := tx.QueryRowContext(ctx,
		`SELECT `+runsSelectColumns+` FROM runs WHERE run_id = ?`, runID)
	run, err := scanRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, mapStoreError(err)
	}
	return run, nil
}

func scanRunByKey(ctx context.Context, tx *sql.Tx, sessionID string, generation int64) (*Run, error) {
	row := tx.QueryRowContext(ctx,
		`SELECT `+runsSelectColumns+` FROM runs
		 WHERE application_session_id = ? AND application_session_generation = ?`,
		sessionID, generation)
	run, err := scanRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, mapStoreError(err)
	}
	return run, nil
}

func scanRunByUnitName(ctx context.Context, tx *sql.Tx, unitName string) (*Run, error) {
	row := tx.QueryRowContext(ctx,
		`SELECT `+runsSelectColumns+` FROM runs WHERE unit_name = ?`, unitName)
	run, err := scanRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, mapStoreError(err)
	}
	return run, nil
}

// newRunIdentities generates the run and sandbox identities together before
// the authority row and its derived unit contract are committed.
func newRunIdentities() (string, string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	b[22] = (b[22] & 0x0f) | 0x40
	b[24] = (b[24] & 0x3f) | 0x80
	return formatUUIDv4(b[:16]), formatUUIDv4(b[16:]), nil
}

func formatUUIDv4(b []byte) string {
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
