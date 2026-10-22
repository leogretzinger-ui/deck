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
	"time"

	// Registers the "sqlite3" driver that openPool asks database/sql for.
	// This import belongs in production code, not in a test file: without it
	// here, every caller outside this package's own tests fails at runtime with
	// `sql: unknown driver "sqlite3"`.
	_ "github.com/mattn/go-sqlite3"
)

// DefaultBusyTimeout bounds how long a statement waits on a lock before
// failing. The wait must be bounded: an unbounded one would turn a stuck
// writer into a hung service with no diagnostic.
const DefaultBusyTimeout = 5 * time.Second

// DefaultMaxReadConns caps the reader pool. Reads are cheap and concurrent
// under WAL; this exists to bound file descriptors, not to serialize.
const DefaultMaxReadConns = 4

// dbFileMode is owner-only. The control database carries fencing tokens and
// session/audit records, so it is credential-sensitive.
const dbFileMode os.FileMode = 0o600

// Config parameterizes Open. The zero value is not usable; Path is required.
type Config struct {
	Path         string
	BusyTimeout  time.Duration
	MaxReadConns int
}

func (c Config) withDefaults() Config {
	if c.BusyTimeout <= 0 {
		c.BusyTimeout = DefaultBusyTimeout
	}
	if c.MaxReadConns <= 0 {
		c.MaxReadConns = DefaultMaxReadConns
	}
	return c
}

// DB is the control database handle.
//
// It holds two pools over the same file. SQLite in WAL mode supports many
// concurrent readers alongside exactly one writer, and database/sql cannot tell
// the two apart — so a single pool must either be capped at one connection,
// serializing reads behind writes, or left uncapped, letting several
// connections contend for the write lock and rely on the busy timeout to sort
// it out.
//
// Neither is acceptable here. The reconciler reads every fifteen seconds while
// the terminal writer and lease manager must write promptly; coupling them
// means a slow read can delay a write, or a write can fail on a lock it should
// never have contended for. Splitting the pools removes
// in-process write contention entirely: exactly one connection can write, so
// writes queue in Go rather than racing in SQLite.
type DB struct {
	read  *sql.DB
	write *sql.DB
	path  string
	cfg   Config
}

// Open prepares the control database at cfg.Path, creating it and its parent
// directory if absent, and applying the migrations.
//
// The durability settings — WAL, synchronous=FULL, the busy timeout, and
// foreign keys — are passed as DSN parameters and then READ BACK. A misspelled
// DSN parameter is silently ignored by SQLite, so setting them without
// verifying would produce a database that looks durable and is not — the
// failure would surface as lost control state after a crash, long after it
// could be diagnosed.
func Open(ctx context.Context, cfg Config) (*DB, error) {
	cfg = cfg.withDefaults()
	if strings.TrimSpace(cfg.Path) == "" {
		return nil, errors.New("db: Config.Path is required")
	}

	abs, err := filepath.Abs(cfg.Path)
	if err != nil {
		return nil, fmt.Errorf("db: resolve %q: %w", cfg.Path, err)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return nil, fmt.Errorf("db: create directory for %q: %w", abs, err)
	}

	write, err := openPool(abs, cfg, true, false)
	if err != nil {
		return nil, err
	}
	read, err := openPool(abs, cfg, false, false)
	if err != nil {
		_ = write.Close()
		return nil, err
	}

	d := &DB{read: read, write: write, path: abs, cfg: cfg}

	// Force the file into existence before checking its mode, so the
	// permission check is not a no-op on first run.
	if err := write.PingContext(ctx); err != nil {
		_ = d.Close()
		return nil, fmt.Errorf("db: open %q: %w", abs, err)
	}
	if err := secureFile(abs); err != nil {
		_ = d.Close()
		return nil, err
	}
	if err := verifyPragmas(ctx, write, cfg, "write", true); err != nil {
		_ = d.Close()
		return nil, err
	}
	if err := verifyPragmas(ctx, read, cfg, "read", true); err != nil {
		_ = d.Close()
		return nil, err
	}
	if _, err := Migrate(ctx, write); err != nil {
		_ = d.Close()
		return nil, fmt.Errorf("db: migrate %q: %w", abs, err)
	}

	return d, nil
}

func openPool(path string, cfg Config, writer bool, readOnly bool) (*sql.DB, error) {
	q := url.Values{}
	q.Set("_synchronous", "FULL")
	q.Set("_busy_timeout", fmt.Sprintf("%d", cfg.BusyTimeout.Milliseconds()))
	q.Set("_foreign_keys", "ON")
	// The project-policy-authority tables (migration 020) protect immutability
	// with triggers, and a trigger fired by INSERT OR REPLACE conflict
	// resolution is only guaranteed to run in full when recursive triggers are
	// on. Default OFF would leave that chain short-circuited, so every pool
	// opts in and verifyPragmas reads it back; the no-delete and immutability
	// triggers depend on it firing deterministically.
	q.Set("_recursive_triggers", "ON")
	// A read-only pool must not request WAL: switching journal mode is a
	// write, and a backup artifact produced by VACUUM INTO is in delete
	// journal mode, so asking a mode=ro connection to set WAL fails on
	// exactly the files OpenBackupSource exists to read. A live source is
	// already WAL (the backend's writer established it), so reading it needs
	// no journal-mode request either.
	if !readOnly {
		q.Set("_journal_mode", "WAL")
	}
	// The backend's writer pool is the only read-write pool. Only it takes
	// the write lock at BEGIN rather than on first write, because a deferred
	// transaction that upgrades can fail at commit, after the work is done
	// and after audit rows have been built — the most expensive possible
	// moment to discover contention. Every other pool (the backend's reader
	// and both backup-source pools) opens mode=ro.
	if !writer || readOnly {
		q.Set("mode", "ro")
	} else {
		q.Set("_txlock", "immediate")
	}

	// Build the URI through net/url rather than concatenating. `?` and `#` are
	// legal in Unix filenames but are delimiters in a SQLite URI, so a raw
	// concatenation truncates the path at the first one — SQLite then opens a
	// DIFFERENT file, silently, while every error message still names the path
	// that was asked for. A wrong-database-opened-successfully failure is far
	// worse than a refusal.
	uri := url.URL{Scheme: "file", Path: path, RawQuery: q.Encode()}

	pool, err := sql.Open("sqlite3", uri.String())
	if err != nil {
		return nil, fmt.Errorf("db: open pool for %q: %w", path, err)
	}
	if writer {
		// Exactly one writer, in-process. This is the mechanism that makes
		// writes queue instead of contend.
		pool.SetMaxOpenConns(1)
	} else {
		pool.SetMaxOpenConns(cfg.MaxReadConns)
	}
	pool.SetConnMaxIdleTime(5 * time.Minute)
	return pool, nil
}

// secureFile enforces owner-only permissions on the database and its WAL
// sidecars. The sidecars matter: recently committed rows live in
// the -wal file, so a world-readable WAL leaks exactly what the database
// protects.
func secureFile(path string) error {
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		info, err := os.Stat(p)
		if errors.Is(err, os.ErrNotExist) {
			continue // sidecars exist only while the database is open
		}
		if err != nil {
			return fmt.Errorf("db: stat %q: %w", p, err)
		}
		if info.Mode().Perm() != dbFileMode {
			if err := os.Chmod(p, dbFileMode); err != nil {
				return fmt.Errorf("db: restrict permissions on %q: %w", p, err)
			}
		}
	}
	return nil
}

// verifyPragmas reads back the settings the DSN requested. Read-only pools
// cannot change journal mode, so they are checked for the mode the writer
// established rather than asked to set it. A backup source is exempt from the
// WAL check entirely: journal mode is the source file's property, and a
// VACUUM INTO artifact is legitimately not WAL.
func verifyPragmas(ctx context.Context, pool *sql.DB, cfg Config, role string, requireWAL bool) error {
	type check struct {
		pragma string
		want   string
	}
	var checks []check
	if requireWAL {
		checks = append(checks, check{"journal_mode", "wal"})
	}
	checks = append(checks,
		check{"foreign_keys", "1"},
		check{"recursive_triggers", "1"},
		check{"busy_timeout", fmt.Sprintf("%d", cfg.BusyTimeout.Milliseconds())},
	)
	// synchronous is a per-connection setting that a read-only connection
	// reports as its own; only the writer's value affects durability.
	if role == "write" {
		checks = append(checks, check{"synchronous", "2"}) // 2 == FULL
	}

	for _, c := range checks {
		var got string
		if err := pool.QueryRowContext(ctx, "PRAGMA "+c.pragma).Scan(&got); err != nil {
			return fmt.Errorf("db: read %s pragma %s: %w", role, c.pragma, err)
		}
		if !strings.EqualFold(got, c.want) {
			return fmt.Errorf(
				"db: %s pool has %s=%s, want %s: the connection string did not take "+
					"effect, so this database does not have the durability the "+
					"control state requires",
				role, c.pragma, got, c.want)
		}
	}
	return nil
}

// Path reports the absolute database path.
func (d *DB) Path() string { return d.path }

// Reader returns the read pool. Callers must not write through it; it is
// opened read-only, so attempts fail rather than silently contending.
func (d *DB) Reader() *sql.DB { return d.read }

// Close releases both pools.
func (d *DB) Close() error {
	var errs []error
	if d.read != nil {
		if err := d.read.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close read pool: %w", err))
		}
	}
	if d.write != nil {
		if err := d.write.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close write pool: %w", err))
		}
	}
	return errors.Join(errs...)
}

// WithTx runs fn inside a single write transaction, committing if it returns
// nil and rolling back otherwise.
//
// Every state transition must append its audit event inside the same
// transaction as the change, which is why this is the only write path: an
// audit row committed separately from the change it describes can be lost
// independently of it, and a transition with no audit event is invisible to
// the very reconstruction the audit log exists to support.
func (d *DB) WithTx(ctx context.Context, fn func(context.Context, *sql.Tx) error) error {
	tx, err := d.write.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("db: begin transaction: %w", err)
	}

	// Safety net for a panicking callback. It is a no-op after a commit or an
	// explicit rollback (both leave the transaction done), but without it a
	// panic that some outer layer recovers would strand the write pool's ONLY
	// connection on an open transaction — after which every subsequent write
	// blocks until its context expires, with nothing to point at the cause.
	defer func() { _ = tx.Rollback() }()

	if err := fn(ctx, tx); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			// Report both: the rollback failure explains why state may not
			// match what the returned error implies.
			return errors.Join(err, fmt.Errorf("db: rollback: %w", rbErr))
		}
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("db: commit: %w", err)
	}
	return nil
}

// VerifyWritable proves the database is actually writable right now, which is
// what readiness depends on. A successful Ping does not establish
// this: a read-only filesystem, a full disk, or a revoked permission all leave
// the connection healthy and every write failing.
//
// The probe is an UPDATE of a single migration-created row, committed. It must
// be a real mutation every time — a `CREATE TABLE IF NOT EXISTS` would succeed
// as a no-op after the first call and report a healthy database while writes
// were failing, which is the exact failure this function exists to catch. The
// row is pinned to one by a CHECK, so repeated probes cannot grow the file.
func (d *DB) VerifyWritable(ctx context.Context) error {
	return d.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE writability_probe SET checked_at = ? WHERE id = 1`,
			FormatTime(time.Now()))
		if err != nil {
			return fmt.Errorf("db: writability probe: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("db: writability probe: rows affected: %w", err)
		}
		if n != 1 {
			return fmt.Errorf(
				"db: writability probe updated %d rows, want 1: the probe row is "+
					"missing, so writability was not actually tested", n)
		}
		return nil
	})
}

// AuditEvent is one ordered entry in the audit log.
//
// Details carries identifiers and state transitions only. It must never
// contain prompt bodies, terminal contents, environment secrets, or
// authentication material.
type AuditEvent struct {
	SessionID *string
	Type      string
	Details   string
}

// AppendAudit writes an audit event on an existing transaction.
//
// Taking a *sql.Tx rather than the DB makes the common mistake harder: an
// audit event cannot be written through a bare connection, so it cannot end up
// committed independently of the change it describes. It does NOT by itself
// guarantee that the event shares the change's transaction — a caller can
// still open a separate transaction for the audit event, or omit it entirely.
// Closing that gap is the job of the repository transition methods, each of
// which must perform its change and append its event within one WithTx.
func AppendAudit(ctx context.Context, tx *sql.Tx, e AuditEvent) error {
	if strings.TrimSpace(e.Type) == "" {
		return errors.New("db: audit event type is required")
	}
	var details any
	if e.Details != "" {
		details = e.Details
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO audit_events (session_id, event_type, details, created_at)
		 VALUES (?, ?, ?, ?)`,
		e.SessionID, e.Type, details, FormatTime(time.Now()),
	); err != nil {
		return fmt.Errorf("db: append audit event %q: %w", e.Type, err)
	}
	return nil
}
