package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// TimeFormat is the storage format for every timestamp column: ISO-8601 in UTC
// with millisecond precision. It sorts lexicographically, which is what lets
// ordering queries work on TEXT columns.
const TimeFormat = "2006-01-02T15:04:05.000Z"

// FormatTime renders t for storage. The caller is responsible for passing a
// time it actually means; this converts to UTC rather than assuming it.
func FormatTime(t time.Time) string { return t.UTC().Format(TimeFormat) }

// Migration is one versioned schema change, loaded from the embedded FS.
type Migration struct {
	Version  int
	Name     string
	SQL      string
	Checksum string // sha256 of SQL, hex-encoded
}

var migrationFilePattern = regexp.MustCompile(`^(\d{3})_([a-z0-9_]+)\.sql$`)

// LoadMigrations reads and validates every embedded migration, ordered by
// version. It rejects unparseable filenames and duplicate versions rather than
// silently skipping them: a migration the runner cannot see is a migration
// that never runs, which is the worst possible failure here.
func LoadMigrations() ([]Migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read embedded migrations: %w", err)
	}

	migrations := make([]Migration, 0, len(entries))
	seen := make(map[int]string, len(entries))

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := migrationFilePattern.FindStringSubmatch(e.Name())
		if m == nil {
			return nil, fmt.Errorf(
				"migration %q does not match NNN_lower_snake_case.sql", e.Name())
		}
		version, err := strconv.Atoi(m[1])
		if err != nil { // unreachable given the \d{3} pattern, but cheap to keep honest
			return nil, fmt.Errorf("migration %q: bad version: %w", e.Name(), err)
		}
		if prev, dup := seen[version]; dup {
			return nil, fmt.Errorf(
				"duplicate migration version %d: %q and %q", version, prev, e.Name())
		}
		seen[version] = e.Name()

		body, err := fs.ReadFile(migrationFS, "migrations/"+e.Name())
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", e.Name(), err)
		}
		sum := sha256.Sum256(body)

		migrations = append(migrations, Migration{
			Version:  version,
			Name:     m[2],
			SQL:      string(body),
			Checksum: hex.EncodeToString(sum[:]),
		})
	}

	sort.Slice(migrations, func(i, j int) bool {
		return migrations[i].Version < migrations[j].Version
	})
	return migrations, nil
}

// appliedMigration is one row of schema_migrations.
type appliedMigration struct {
	Version  int
	Name     string
	Checksum string
}

const createSchemaMigrations = `
CREATE TABLE IF NOT EXISTS schema_migrations (
  version    INTEGER PRIMARY KEY,
  name       TEXT NOT NULL,
  checksum   TEXT NOT NULL,
  applied_at TEXT NOT NULL
) STRICT`

// Migrate brings the database up to the latest embedded schema version and
// returns the versions it applied, in order. Applying nothing is success.
//
// Migrations are forward-only. There is deliberately no Down: the control
// database holds durable input the operator has been promised will not vanish,
// and an automated down-migration is a fast path to destroying exactly that.
// The supported way back is restore-from-backup, which a human decides to
// run.
//
// Each migration runs in its own transaction together with its
// schema_migrations row, so a failure leaves the database at the last fully
// applied version rather than half-way through one.
//
// Migrate does not set connection pragmas. Journal mode, synchronous, busy
// timeout, and foreign_keys are properties of the connection and belong to
// whoever opens it.
func Migrate(ctx context.Context, sqlDB *sql.DB) ([]int, error) {
	migrations, err := LoadMigrations()
	if err != nil {
		return nil, err
	}

	if _, err := sqlDB.ExecContext(ctx, createSchemaMigrations); err != nil {
		return nil, fmt.Errorf("create schema_migrations: %w", err)
	}

	applied, err := loadApplied(ctx, sqlDB)
	if err != nil {
		return nil, err
	}

	if err := verifyApplied(migrations, applied); err != nil {
		return nil, err
	}

	var ran []int
	for _, m := range migrations {
		if _, done := applied[m.Version]; done {
			continue
		}
		if err := applyOne(ctx, sqlDB, m); err != nil {
			return ran, err
		}
		ran = append(ran, m.Version)
	}
	return ran, nil
}

func loadApplied(ctx context.Context, sqlDB *sql.DB) (map[int]appliedMigration, error) {
	rows, err := sqlDB.QueryContext(ctx,
		`SELECT version, name, checksum FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	defer func() { _ = rows.Close() }()

	applied := make(map[int]appliedMigration)
	for rows.Next() {
		var a appliedMigration
		if err := rows.Scan(&a.Version, &a.Name, &a.Checksum); err != nil {
			return nil, fmt.Errorf("scan schema_migrations: %w", err)
		}
		applied[a.Version] = a
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate schema_migrations: %w", err)
	}
	return applied, nil
}

// verifyApplied fails closed on any disagreement between what this binary
// carries and what the database says was already run. Both directions matter:
// an edited migration means the live schema is not what the source claims, and
// an unknown applied version means this binary is older than the database.
// Either way, continuing would build on an assumption we cannot support.
func verifyApplied(migrations []Migration, applied map[int]appliedMigration) error {
	embedded := make(map[int]Migration, len(migrations))
	for _, m := range migrations {
		embedded[m.Version] = m
	}

	versions := make([]int, 0, len(applied))
	for v := range applied {
		versions = append(versions, v)
	}
	sort.Ints(versions)

	for _, v := range versions {
		a := applied[v]
		m, known := embedded[v]
		if !known {
			return fmt.Errorf(
				"database has migration %d (%q) applied, but this binary does not "+
					"contain it: the database was migrated by a newer build",
				v, a.Name)
		}
		if a.Checksum != m.Checksum {
			return fmt.Errorf(
				"migration %d (%q) was modified after it was applied "+
					"(recorded checksum %s, embedded %s): the live schema does not "+
					"match the source; add a new migration instead of editing an "+
					"applied one",
				v, m.Name, short(a.Checksum), short(m.Checksum))
		}
		// A rename with identical SQL is harmless to the schema but means the
		// ledger no longer identifies the file that produced it, which is the
		// first thing anyone reads when diagnosing a migration problem.
		if a.Name != m.Name {
			return fmt.Errorf(
				"migration %d was applied as %q but this binary calls it %q: "+
					"an applied migration was renamed; restore the original "+
					"filename or add a new migration",
				v, a.Name, m.Name)
		}
	}
	return nil
}

// VerifySchemaCurrent reports whether the database's applied migrations
// exactly match the migrations embedded in THIS binary. It is stricter than
// the check Migrate performs before applying: not only must every applied row
// be known and unmodified (verifyApplied), but every embedded migration must
// already be present in the ledger. A backup reader uses this to refuse a
// source it cannot safely copy — a database behind this binary was last
// opened by an older version — and a foreign or empty file fails here, since
// a missing schema_migrations table surfaces as loadApplied's error.
func VerifySchemaCurrent(ctx context.Context, sqlDB *sql.DB) error {
	migrations, err := LoadMigrations()
	if err != nil {
		return err
	}
	applied, err := loadApplied(ctx, sqlDB)
	if err != nil {
		return fmt.Errorf("db: schema compatibility check: %w", err)
	}
	if err := verifyApplied(migrations, applied); err != nil {
		return fmt.Errorf("db: schema compatibility check: %w", err)
	}
	for _, m := range migrations {
		if _, ok := applied[m.Version]; !ok {
			return fmt.Errorf(
				"db: schema compatibility check: the database does not match "+
					"this binary's schema (migration %d %q is not applied) and "+
					"must be opened by the matching version", m.Version, m.Name)
		}
	}
	return nil
}

// fkOffMarker opts a migration into applyWithForeignKeysOff. It must be the
// first non-blank line of the file.
const fkOffMarker = "-- foreign_keys: off"

func applyOne(ctx context.Context, sqlDB *sql.DB, m Migration) error {
	// A migration whose first line is the fkOffMarker rebuilds a table that
	// other tables reference (SQLite cannot alter a CHECK), which requires
	// foreign_keys=OFF around the transaction. The pragma cannot change
	// inside a transaction, so that migration runs on a dedicated conn with
	// the pragma toggled outside it — and always restored before the conn
	// returns to the pool. defer_foreign_keys is NOT a substitute: it only
	// defers constraints declared DEFERRABLE, and ours are immediate.
	if strings.HasPrefix(strings.TrimSpace(m.SQL), fkOffMarker) {
		return applyWithForeignKeysOff(ctx, sqlDB, m)
	}
	tx, err := sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %d: %w", m.Version, err)
	}
	defer func() { _ = tx.Rollback() }() // no-op once committed

	if _, err := tx.ExecContext(ctx, m.SQL); err != nil {
		return fmt.Errorf("apply migration %d (%q): %w", m.Version, m.Name, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, name, checksum, applied_at)
		 VALUES (?, ?, ?, ?)`,
		m.Version, m.Name, m.Checksum, FormatTime(time.Now()),
	); err != nil {
		return fmt.Errorf("record migration %d: %w", m.Version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %d: %w", m.Version, err)
	}
	return nil
}

func applyWithForeignKeysOff(ctx context.Context, sqlDB *sql.DB, m Migration) error {
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return fmt.Errorf("migration %d: checkout conn: %w", m.Version, err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
		return fmt.Errorf("migration %d: disable foreign keys: %w", m.Version, err)
	}
	// Restore before anything returns the conn to the pool, whatever happens
	// below. The pragma is connection-scoped, so this leaves no trace.
	defer func() {
		_, _ = conn.ExecContext(context.Background(), "PRAGMA foreign_keys=ON")
	}()

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %d: %w", m.Version, err)
	}
	defer func() { _ = tx.Rollback() }() // no-op once committed

	if _, err := tx.ExecContext(ctx, m.SQL); err != nil {
		return fmt.Errorf("apply migration %d (%q): %w", m.Version, m.Name, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, name, checksum, applied_at)
		 VALUES (?, ?, ?, ?)`,
		m.Version, m.Name, m.Checksum, FormatTime(time.Now()),
	); err != nil {
		return fmt.Errorf("record migration %d: %w", m.Version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %d: %w", m.Version, err)
	}
	return nil
}

func short(checksum string) string {
	if len(checksum) <= 12 {
		return checksum
	}
	return checksum[:12]
}
