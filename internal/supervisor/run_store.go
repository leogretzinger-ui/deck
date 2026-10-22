package supervisor

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mattn/go-sqlite3"
)

// storeSchemaVersion is the only schema this build understands. A store
// recorded at any other version is refused on open rather than normalized.
// v2 carried the untyped v1 unit-property vector; v3 binds the typed
// transient-unit-properties.v2 contract; v4 adds the PhaseAuthorized edge and
// persists the exact authorized launch ticket. No migration bridges them: a
// v3 store is refused as ErrStoreVersion so its absent ticket columns and
// stale phase enum can never be mistaken for a corrupt v4 row.
const storeSchemaVersion = 4

// storeTimeFormat matches the control database's ISO-8601 UTC timestamps so
// ordering and tooling stay uniform across both stores.
const storeTimeFormat = "2006-01-02T15:04:05.000Z"

// storeDefaultBusyTimeout bounds how long a statement waits on a lock; an
// unbounded wait would turn a stuck writer into a hung supervisor.
const storeDefaultBusyTimeout = 5 * time.Second

// storeFileMode is owner-only: the store is supervisor launch authority.
const storeFileMode os.FileMode = 0o600

const createMetaTableSQL = `
CREATE TABLE meta (
  schema_version INTEGER NOT NULL
) STRICT`

const createRunsTableSQL = `
CREATE TABLE runs (
  run_id                          TEXT PRIMARY KEY,
  sandbox_generation              TEXT NOT NULL,
  application_session_id          TEXT NOT NULL,
  application_session_generation  INTEGER NOT NULL,
  launch_request_digest           TEXT NOT NULL,
  launch_request_json             TEXT NOT NULL,
  pty_binding_digest              TEXT NOT NULL,
  pty_binding_json                TEXT NOT NULL,
  project_policy_revision         TEXT NOT NULL,
  provider_profile_revision       TEXT NOT NULL,
  unit_name                       TEXT NOT NULL,
  unit_property_digest            TEXT NOT NULL,
  boot_id                         TEXT NOT NULL,
  launch_ticket_digest            TEXT,
  launch_ticket_json              TEXT,
  phase                           TEXT NOT NULL,
  needs_attention                 INTEGER NOT NULL,
  needs_attention_reason          TEXT,
  terminal_outcome                TEXT,
  empty_cgroup_evidence_digest    TEXT,
  cleanup_state                   TEXT NOT NULL,
  created_at                      TEXT NOT NULL,
  updated_at                      TEXT NOT NULL,
  UNIQUE (application_session_id, application_session_generation),
  UNIQUE (unit_name),
  CONSTRAINT chk_runs_generation CHECK (application_session_generation >= 1),
  CONSTRAINT chk_runs_phase CHECK (phase IN ('prepared','starting','authorized','running','terminal')),
  CONSTRAINT chk_runs_needs_attention CHECK (needs_attention IN (0,1)),
  CONSTRAINT chk_runs_cleanup_state CHECK (cleanup_state IN ('none','pending','complete')),
  CONSTRAINT chk_runs_cleanup_phase CHECK (
    (phase <> 'terminal' AND cleanup_state = 'none')
    OR (phase = 'terminal' AND cleanup_state IN ('pending','complete'))
  ),
  CONSTRAINT chk_runs_terminal_evidence CHECK (
    phase <> 'terminal'
    OR (terminal_outcome IS NOT NULL AND empty_cgroup_evidence_digest IS NOT NULL)
  ),
  CONSTRAINT chk_runs_terminal_fields CHECK (
    phase = 'terminal'
    OR (terminal_outcome IS NULL AND empty_cgroup_evidence_digest IS NULL)
  ),
  CONSTRAINT chk_runs_attention_reason CHECK (
    (needs_attention = 0 AND needs_attention_reason IS NULL)
    OR (needs_attention = 1 AND needs_attention_reason IS NOT NULL)
  ),
  CONSTRAINT chk_runs_outcome_value CHECK (
    terminal_outcome IS NULL OR terminal_outcome IN ('success','failed','stopped')
  ),
  CONSTRAINT chk_runs_attention_value CHECK (
    needs_attention_reason IS NULL OR needs_attention_reason IN ('unverified_state')
  ),
  CONSTRAINT chk_runs_ticket_prepared_starting CHECK (
    phase NOT IN ('prepared','starting')
    OR (launch_ticket_digest IS NULL AND launch_ticket_json IS NULL)
  ),
  CONSTRAINT chk_runs_ticket_authorized_running CHECK (
    phase NOT IN ('authorized','running')
    OR (launch_ticket_digest IS NOT NULL AND launch_ticket_json IS NOT NULL)
  ),
  CONSTRAINT chk_runs_ticket_one_sided CHECK (
    (launch_ticket_digest IS NULL AND launch_ticket_json IS NULL)
    OR (launch_ticket_digest IS NOT NULL AND launch_ticket_json IS NOT NULL)
  )
) STRICT`

// runsSelectColumns is the fixed scan order for every run row.
const runsSelectColumns = `
	run_id, sandbox_generation,
	application_session_id, application_session_generation,
	launch_request_digest, launch_request_json,
	pty_binding_digest, pty_binding_json,
	project_policy_revision, provider_profile_revision,
	unit_name, unit_property_digest, boot_id,
	launch_ticket_digest, launch_ticket_json,
	phase, needs_attention, needs_attention_reason,
	terminal_outcome, empty_cgroup_evidence_digest, cleanup_state,
	created_at, updated_at`

var (
	// ErrStoreNotFound marks a store path that has never been initialized; it
	// must be created explicitly through InitializeStore.
	ErrStoreNotFound = errors.New("supervisor run store is not initialized")
	// ErrStoreExists marks an already-initialized store path; an existing
	// store is never recreated.
	ErrStoreExists = errors.New("supervisor run store is already initialized")
	// ErrStoreVersion marks a store whose schema version this build does not
	// support, older or newer.
	ErrStoreVersion = errors.New("supervisor run store schema version is unsupported")
	// ErrStoreCorrupt marks any schema, row, or database content that cannot
	// be verified; a corrupt store is never repaired or recreated.
	ErrStoreCorrupt = errors.New("supervisor run store is corrupt")
	// ErrStoreUnavailable marks an operational SQLite or filesystem failure.
	ErrStoreUnavailable = errors.New("supervisor run store is unavailable")
	// ErrInvalidArgument marks a malformed operation input.
	ErrInvalidArgument = errors.New("supervisor run store: invalid argument")
	// ErrRunNotFound marks a run that was never reserved.
	ErrRunNotFound = errors.New("supervisor run store: run not found")
	// ErrRunConflict marks reuse of an idempotency key or deterministic unit
	// name with different immutable bound facts. The conflict never mutates or
	// parks the incumbent and never creates a duplicate; parking is a separate,
	// deliberate supervisor decision.
	ErrRunConflict = errors.New("supervisor run store: run conflict")
	// ErrStaleTransition marks a transition whose precondition phase is not
	// the recorded current phase.
	ErrStaleTransition = errors.New("supervisor run store: stale transition")
	// ErrIllegalTransition marks a transition edge the lifecycle never admits.
	ErrIllegalTransition = errors.New("supervisor run store: illegal transition")
	// ErrNeedsAttention marks a run parked for user attention; it never
	// resumes, changes identity, or spawns a replacement.
	ErrNeedsAttention = errors.New("supervisor run store: run needs attention")
)

// Phase is the closed lifecycle phase of one run. needs_attention is a
// monotonic flag on the row, never a phase.
type Phase string

const (
	PhasePrepared   Phase = "prepared"
	PhaseStarting   Phase = "starting"
	PhaseAuthorized Phase = "authorized"
	PhaseRunning    Phase = "running"
	PhaseTerminal   Phase = "terminal"
)

// CleanupState tracks retained-unit collection after terminal.
type CleanupState string

const (
	CleanupNone     CleanupState = "none"
	CleanupPending  CleanupState = "pending"
	CleanupComplete CleanupState = "complete"
)

// Outcome is the content-free terminal result. No exit code, signal, service
// result, or provider text is ever stored.
type Outcome string

const (
	OutcomeSuccess Outcome = "success"
	OutcomeFailed  Outcome = "failed"
	OutcomeStopped Outcome = "stopped"
)

// AttentionReason is a closed content-free reason code for a parked run.
type AttentionReason string

const (
	// ReasonUnverifiedState marks a run the reconciler parks because it
	// cannot verify the workload or unit state, the condition MarkNeedsAttention
	// exists for.
	ReasonUnverifiedState AttentionReason = "unverified_state"
)

// Run is one immutable run-authority row. Fields after the immutable bound
// facts are lifecycle state; nothing here can carry terminal or provider
// content.
type Run struct {
	RunID                        string
	SandboxGeneration            string
	ApplicationSessionID         string
	ApplicationSessionGeneration int64
	LaunchRequestDigest          string
	LaunchRequestJSON            string
	PTYBindingDigest             string
	PTYBindingJSON               string
	ProjectPolicyRevision        string
	ProviderProfileRevision      string
	UnitName                     string
	UnitPropertyDigest           string
	BootID                       string
	LaunchTicketDigest           string
	LaunchTicketJSON             string
	Phase                        Phase
	NeedsAttention               bool
	NeedsAttentionReason         string
	TerminalOutcome              Outcome
	EmptyCGroupEvidenceDigest    string
	CleanupState                 CleanupState
	CreatedAt                    string
	UpdatedAt                    string
}

// Config parameterizes the supervisor-owned run store. Path is trusted
// supervisor configuration; a caller-supplied path never appears in any
// authority record or digest.
type Config struct {
	Path        string
	BusyTimeout time.Duration
}

func (c Config) withDefaults() Config {
	if c.BusyTimeout <= 0 {
		c.BusyTimeout = storeDefaultBusyTimeout
	} else if c.BusyTimeout < time.Millisecond {
		// Milliseconds() truncates, so a sub-millisecond timeout would become
		// _busy_timeout=0 — no busy wait at all. Round up to one millisecond
		// instead of silently disabling the wait.
		c.BusyTimeout = time.Millisecond
	}
	return c
}

// Store is one protected supervisor-owned run store, separate from Deck
// SQLite and from Deck's migration lifecycle.
type Store struct {
	db          *sql.DB
	path        string
	busyTimeout time.Duration
}

// InitializeStore creates a new store at cfg.Path. It refuses an existing
// path so a genuinely absent store is the only thing it ever creates; an
// existing file, valid or not, is left untouched.
func InitializeStore(ctx context.Context, cfg Config) (*Store, error) {
	cfg = cfg.withDefaults()
	abs, err := resolveStorePath(cfg.Path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return nil, ErrStoreUnavailable
	}
	// O_EXCL makes concurrent initialization atomic: exactly one caller wins
	// the file, so a loser can never destroy the winner's fresh store.
	// #nosec G304 -- the path is trusted supervisor configuration resolved
	// by resolveStorePath; creating the supervisor's own store is the point.
	file, err := os.OpenFile(abs, os.O_WRONLY|os.O_CREATE|os.O_EXCL, storeFileMode)
	if errors.Is(err, os.ErrExist) {
		return nil, ErrStoreExists
	}
	if err != nil {
		return nil, ErrStoreUnavailable
	}
	if err := file.Close(); err != nil {
		removeStoreFiles(abs)
		return nil, ErrStoreUnavailable
	}
	if err := refuseSidecarSymlinks(abs); err != nil {
		removeStoreFiles(abs)
		return nil, err
	}

	s, err := openStoreFile(ctx, cfg, abs)
	if err != nil {
		removeStoreFiles(abs)
		return nil, err
	}
	if err := s.initSchema(ctx); err != nil {
		_ = s.Close()
		removeStoreFiles(abs)
		return nil, err
	}
	if err := s.secureFiles(); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

// OpenStore opens and verifies an existing store at cfg.Path. It fails closed
// on an absent path, an unsupported schema version, a corrupt schema or row,
// or any SQLite error; it never recreates or normalizes what it finds.
func OpenStore(ctx context.Context, cfg Config) (*Store, error) {
	cfg = cfg.withDefaults()
	abs, err := resolveStorePath(cfg.Path)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(abs)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrStoreNotFound
	}
	if err != nil {
		return nil, ErrStoreUnavailable
	}
	// A symlink at the store path is refused without being followed, so the
	// target it points at is never opened, verified, or chmodded through it.
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, ErrStoreCorrupt
	}
	// Refuse a sidecar symlink before the first statement, which is what
	// creates and writes the WAL/SHM files; a post-hoc refusal would leave the
	// write already done.
	if err := refuseSidecarSymlinks(abs); err != nil {
		return nil, err
	}
	s, err := openStoreFile(ctx, cfg, abs)
	if err != nil {
		return nil, err
	}
	if err := s.verifyOpen(ctx); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) initSchema(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return mapStoreError(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, createMetaTableSQL); err != nil {
		return mapStoreError(err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO meta (schema_version) VALUES (?)`, storeSchemaVersion); err != nil {
		return mapStoreError(err)
	}
	if _, err := tx.ExecContext(ctx, createRunsTableSQL); err != nil {
		return mapStoreError(err)
	}
	if err := tx.Commit(); err != nil {
		return mapStoreError(err)
	}
	return nil
}

func (s *Store) verifyOpen(ctx context.Context) error {
	if err := s.verifySchema(ctx); err != nil {
		return err
	}
	if err := s.validateAllRuns(ctx); err != nil {
		return err
	}
	if err := s.secureFiles(); err != nil {
		return err
	}
	return s.verifyPragmas(ctx)
}

func (s *Store) verifySchema(ctx context.Context) error {
	if err := s.requireTable(ctx, "meta"); err != nil {
		return err
	}
	var version int
	err := s.db.QueryRowContext(ctx, `SELECT schema_version FROM meta`).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrStoreCorrupt
	}
	if err != nil {
		return mapStoreError(err)
	}
	if version != storeSchemaVersion {
		return ErrStoreVersion
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM meta`).Scan(&count); err != nil {
		return mapStoreError(err)
	}
	if count != 1 {
		return ErrStoreCorrupt
	}
	return s.verifyTableDDL(ctx)
}

func (s *Store) requireTable(ctx context.Context, table string) error {
	var name string
	err := s.db.QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrStoreCorrupt
	}
	if err != nil {
		return mapStoreError(err)
	}
	return nil
}

// verifyTableDDL compares the stored CREATE TABLE statements against the
// compiled schema. A whitespace-normalized token comparison catches a
// recreated table that kept the column names but dropped the UNIQUE
// constraints or CHECKs that actually carry the authority invariant.
func (s *Store) verifyTableDDL(ctx context.Context) error {
	for _, want := range []struct {
		name string
		ddl  string
	}{
		{"meta", createMetaTableSQL},
		{"runs", createRunsTableSQL},
	} {
		var stored string
		err := s.db.QueryRowContext(ctx,
			`SELECT sql FROM sqlite_master WHERE type = 'table' AND name = ?`, want.name).Scan(&stored)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrStoreCorrupt
		}
		if err != nil {
			return mapStoreError(err)
		}
		if normalizeSQL(stored) != normalizeSQL(want.ddl) {
			return ErrStoreCorrupt
		}
	}
	return nil
}

func normalizeSQL(sql string) string {
	return strings.Join(strings.Fields(sql), " ")
}

// validateAllRuns fails the open on any row whose stored fields cannot be
// verified: bad enums, digests, UUIDs, generations, boot identity, digest/JSON
// disagreement, or terminal/cleanup invariants.
func (s *Store) validateAllRuns(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT `+runsSelectColumns+` FROM runs`)
	if err != nil {
		return mapStoreError(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return ErrStoreCorrupt
		}
		if err := validateRun(run); err != nil {
			return ErrStoreCorrupt
		}
	}
	if err := rows.Err(); err != nil {
		return mapStoreError(err)
	}
	return nil
}

// scanRun reads one row in runsSelectColumns order. Nullable columns map to
// their empty value.
func scanRun(row interface{ Scan(...any) error }) (*Run, error) {
	var r Run
	var attention int
	var phase, cleanup string
	var reason, outcome, evidence, ticketDigest, ticketJSON sql.NullString
	err := row.Scan(
		&r.RunID, &r.SandboxGeneration,
		&r.ApplicationSessionID, &r.ApplicationSessionGeneration,
		&r.LaunchRequestDigest, &r.LaunchRequestJSON,
		&r.PTYBindingDigest, &r.PTYBindingJSON,
		&r.ProjectPolicyRevision, &r.ProviderProfileRevision,
		&r.UnitName, &r.UnitPropertyDigest, &r.BootID,
		&ticketDigest, &ticketJSON,
		&phase, &attention, &reason, &outcome, &evidence, &cleanup,
		&r.CreatedAt, &r.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	r.Phase = Phase(phase)
	r.NeedsAttention = attention != 0
	r.NeedsAttentionReason = reason.String
	r.LaunchTicketDigest = ticketDigest.String
	r.LaunchTicketJSON = ticketJSON.String
	r.TerminalOutcome = Outcome(outcome.String)
	r.EmptyCGroupEvidenceDigest = evidence.String
	r.CleanupState = CleanupState(cleanup)
	return &r, nil
}

// validateRun verifies every invariant a stored run must satisfy. Any
// disagreement means the store is corrupt.
func validateRun(r *Run) error {
	if !isCanonicalUUIDv4(r.RunID) {
		return ErrStoreCorrupt
	}
	if !isCanonicalUUIDv4(r.SandboxGeneration) {
		return ErrStoreCorrupt
	}
	if !isCanonicalUUIDv4(r.ApplicationSessionID) {
		return ErrStoreCorrupt
	}
	if r.ApplicationSessionGeneration < 1 {
		return ErrStoreCorrupt
	}
	request, err := VerifyLaunchRequest(r.LaunchRequestDigest, r.LaunchRequestJSON)
	if err != nil {
		return ErrStoreCorrupt
	}
	if request.Request.ApplicationSessionID != r.ApplicationSessionID ||
		request.Request.ApplicationSessionGeneration != r.ApplicationSessionGeneration ||
		request.Request.ProjectPolicyRevision != r.ProjectPolicyRevision ||
		request.Request.ProviderProfileRevision != r.ProviderProfileRevision {
		return ErrStoreCorrupt
	}
	binding, err := VerifyPTYBinding(r.PTYBindingDigest, r.PTYBindingJSON)
	if err != nil {
		return ErrStoreCorrupt
	}
	if binding.Binding.BootID != r.BootID {
		return ErrStoreCorrupt
	}
	if !isLowerSHA256(r.ProjectPolicyRevision) || !isLowerSHA256(r.ProviderProfileRevision) {
		return ErrStoreCorrupt
	}
	unit, err := DeriveTransientUnitProperties(TransientUnitPropertiesBinding{
		RunID:             r.RunID,
		SandboxGeneration: r.SandboxGeneration,
	})
	if err != nil || unit.UnitName != r.UnitName || unit.Digest != r.UnitPropertyDigest {
		return ErrStoreCorrupt
	}
	if !isCanonicalBootID(r.BootID) {
		return ErrStoreCorrupt
	}
	switch r.Phase {
	case PhasePrepared, PhaseStarting, PhaseAuthorized, PhaseRunning, PhaseTerminal:
	default:
		return ErrStoreCorrupt
	}
	if err := validateRunTicketFields(r); err != nil {
		return err
	}
	if r.NeedsAttention {
		if !isValidAttentionReason(AttentionReason(r.NeedsAttentionReason)) {
			return ErrStoreCorrupt
		}
	} else if r.NeedsAttentionReason != "" {
		return ErrStoreCorrupt
	}
	switch r.CleanupState {
	case CleanupNone, CleanupPending, CleanupComplete:
	default:
		return ErrStoreCorrupt
	}
	if r.Phase != PhaseTerminal {
		if r.TerminalOutcome != "" || r.EmptyCGroupEvidenceDigest != "" || r.CleanupState != CleanupNone {
			return ErrStoreCorrupt
		}
		return nil
	}
	if !isValidOutcome(r.TerminalOutcome) || !isLowerSHA256(r.EmptyCGroupEvidenceDigest) {
		return ErrStoreCorrupt
	}
	if r.CleanupState != CleanupPending && r.CleanupState != CleanupComplete {
		return ErrStoreCorrupt
	}
	return nil
}

// validateRunTicketFields enforces the launch-ticket field parity every stored
// run must satisfy. Prepared and starting rows carry no ticket: authorization
// has not happened. Authorized and running rows carry both fields: a ticket
// was durably persisted before any workload release. Terminal rows permit
// either, since a run may fail during starting before authorization or cross
// authorized before exiting. A one-sided row is always corrupt. When a ticket
// is present it must verify as exact canonical LaunchTicketV1 cross-bound to
// the row's protected identities.
func validateRunTicketFields(r *Run) error {
	digestSet := r.LaunchTicketDigest != ""
	jsonSet := r.LaunchTicketJSON != ""
	if digestSet != jsonSet {
		return ErrStoreCorrupt
	}
	switch r.Phase {
	case PhasePrepared, PhaseStarting:
		if digestSet {
			return ErrStoreCorrupt
		}
	case PhaseAuthorized, PhaseRunning:
		if !digestSet {
			return ErrStoreCorrupt
		}
	case PhaseTerminal:
	}
	if !digestSet {
		return nil
	}
	if err := verifyLaunchTicketForRun(r, r.LaunchTicketDigest, r.LaunchTicketJSON); err != nil {
		return ErrStoreCorrupt
	}
	return nil
}

// verifyLaunchTicketForRun binds the ticket's six authority-bearing identities
// (run, sandbox, launch request, PTY, unit name, unit property) to the
// protected run row and takes the ticket's opaque sandbox-plan digest and
// launcher-invocation identity from the ticket itself, then verifies the exact
// canonical LaunchTicketV1 bytes and digest. The row is ground truth; a ticket
// canonicalized against any other binding fails.
func verifyLaunchTicketForRun(r *Run, digest, canonicalJSON string) error {
	var ticket LaunchTicketV1
	decoder := json.NewDecoder(strings.NewReader(canonicalJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&ticket); err != nil {
		return ErrInvalidArgument
	}
	binding := LaunchTicketBinding{
		RunID:                r.RunID,
		SandboxGeneration:    r.SandboxGeneration,
		LaunchRequestDigest:  r.LaunchRequestDigest,
		PTYBindingDigest:     r.PTYBindingDigest,
		SandboxPlanDigest:    ticket.SandboxPlanDigest,
		UnitName:             r.UnitName,
		UnitPropertyDigest:   r.UnitPropertyDigest,
		LauncherInvocationID: ticket.LauncherInvocationID,
	}
	if _, err := VerifyLaunchTicket(digest, canonicalJSON, binding); err != nil {
		return ErrInvalidArgument
	}
	return nil
}

func openStoreFile(ctx context.Context, cfg Config, abs string) (*Store, error) {
	q := url.Values{}
	q.Set("_journal_mode", "WAL")
	q.Set("_synchronous", "FULL")
	q.Set("_busy_timeout", fmt.Sprintf("%d", cfg.BusyTimeout.Milliseconds()))
	q.Set("_foreign_keys", "ON")
	q.Set("_txlock", "immediate")
	uri := url.URL{Scheme: "file", Path: abs, RawQuery: q.Encode()}

	pool, err := sql.Open("sqlite3", uri.String())
	if err != nil {
		return nil, ErrStoreUnavailable
	}
	pool.SetMaxOpenConns(8)
	pool.SetConnMaxIdleTime(5 * time.Minute)
	return &Store{db: pool, path: abs, busyTimeout: cfg.BusyTimeout}, nil
}

func (s *Store) secureFiles() error {
	for _, p := range []string{s.path, s.path + "-wal", s.path + "-shm"} {
		info, err := os.Lstat(p)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return ErrStoreUnavailable
		}
		// Never chmod through a symlink planted at a store or sidecar path;
		// a symlink is refused outright so its target is left untouched.
		if info.Mode()&os.ModeSymlink != 0 {
			return ErrStoreCorrupt
		}
		if info.Mode().Perm() != storeFileMode {
			if err := os.Chmod(p, storeFileMode); err != nil {
				return ErrStoreUnavailable
			}
		}
	}
	return nil
}

func (s *Store) verifyPragmas(ctx context.Context) error {
	type check struct{ pragma, want string }
	checks := []check{
		{"journal_mode", "wal"},
		{"foreign_keys", "1"},
		{"synchronous", "2"}, // FULL
		{"busy_timeout", fmt.Sprintf("%d", s.busyTimeout.Milliseconds())},
	}
	for _, c := range checks {
		var got string
		if err := s.db.QueryRowContext(ctx, "PRAGMA "+c.pragma).Scan(&got); err != nil {
			return mapStoreError(err)
		}
		if !strings.EqualFold(got, c.want) {
			return ErrStoreCorrupt
		}
	}
	return nil
}

// Close releases the store's database pool.
func (s *Store) Close() error {
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}

// refuseSidecarSymlinks fails closed on a symlink planted at a WAL sidecar
// path before any statement can create or write that sidecar through it.
func refuseSidecarSymlinks(abs string) error {
	for _, p := range []string{abs + "-wal", abs + "-shm"} {
		info, err := os.Lstat(p)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return ErrStoreUnavailable
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return ErrStoreCorrupt
		}
	}
	return nil
}

// removeStoreFiles unlinks the store file and its WAL sidecars. os.Remove
// never follows a symlink, so an attacker-controlled link is unlinked without
// touching its target.
func removeStoreFiles(abs string) {
	for _, p := range []string{abs, abs + "-wal", abs + "-shm"} {
		_ = os.Remove(p)
	}
}

func resolveStorePath(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", ErrInvalidArgument
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", ErrInvalidArgument
	}
	return abs, nil
}

// mapStoreError collapses a SQLite error into a stable sentinel without
// embedding SQLite text, paths, or caller-controlled values. Caller context
// cancellation and deadlines survive as themselves so a supervisor that
// cancels on shutdown can distinguish its own cancellation from an authority
// fault.
func mapStoreError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	var sqErr sqlite3.Error
	if errors.As(err, &sqErr) {
		switch sqErr.Code {
		case sqlite3.ErrNotADB, sqlite3.ErrCorrupt:
			return ErrStoreCorrupt
		}
	}
	return ErrStoreUnavailable
}

// isUniqueConstraint reports whether err is a UNIQUE or PRIMARY KEY
// constraint violation specifically. Matching the extended code keeps CHECK,
// NOT NULL, and type failures from being misread as "this key already exists".
func isUniqueConstraint(err error) bool {
	var sqErr sqlite3.Error
	if !errors.As(err, &sqErr) {
		return false
	}
	return sqErr.ExtendedCode == sqlite3.ErrConstraintUnique ||
		sqErr.ExtendedCode == sqlite3.ErrConstraintPrimaryKey
}

func formatStoreTime(t time.Time) string { return t.UTC().Format(storeTimeFormat) }
