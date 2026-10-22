package supervisor

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func rawDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("raw sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

// setCheckOff runs a corrupting UPDATE with CHECK constraints disabled on a
// dedicated connection, so a stored row can violate an invariant the schema
// normally refuses to represent.
func setCheckOff(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("checkout conn: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(context.Background(), `PRAGMA ignore_check_constraints = ON`); err != nil {
		t.Fatalf("ignore_check_constraints: %v", err)
	}
	if _, err := conn.ExecContext(context.Background(), query, args...); err != nil {
		t.Fatalf("corrupting exec %q: %v", query, err)
	}
}

func TestStoreOpenMissingFailsClosed(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "absent", "runs.db")
	if _, err := OpenStore(ctx, Config{Path: path}); !errors.Is(err, ErrStoreNotFound) {
		t.Fatalf("error = %v, want ErrStoreNotFound", err)
	}

	// a genuinely absent store is created only through the explicit API
	store, err := InitializeStore(ctx, Config{Path: path})
	if err != nil {
		t.Fatalf("InitializeStore: %v", err)
	}
	_ = store.Close()
	reopened, err := OpenStore(ctx, Config{Path: path})
	if err != nil {
		t.Fatalf("OpenStore after initialize: %v", err)
	}
	_ = reopened.Close()
}

func TestStoreInitializeCreatesParentDirectory(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "deep", "nested", "runs.db")
	store, err := InitializeStore(ctx, Config{Path: path})
	if err != nil {
		t.Fatalf("InitializeStore: %v", err)
	}
	_ = store.Close()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("store file was not created: %v", err)
	}
}

func TestStoreInitializeRefusesExistingPath(t *testing.T) {
	ctx := context.Background()
	store, path := newTestStore(t)
	_ = store.Close()
	if _, err := InitializeStore(ctx, Config{Path: path}); !errors.Is(err, ErrStoreExists) {
		t.Fatalf("error = %v, want ErrStoreExists", err)
	}

	// any existing file, valid or not, is refused untouched
	junk := filepath.Join(t.TempDir(), "junk")
	if err := os.WriteFile(junk, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := InitializeStore(ctx, Config{Path: junk}); !errors.Is(err, ErrStoreExists) {
		t.Fatalf("error = %v, want ErrStoreExists", err)
	}
}

func TestStoreOpenRejectsCorruptDatabaseFile(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "garbage.db")
	if err := os.WriteFile(path, []byte("this is not a sqlite database at all, not even close"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := OpenStore(ctx, Config{Path: path}); !errors.Is(err, ErrStoreCorrupt) {
		t.Fatalf("error = %v, want ErrStoreCorrupt", err)
	}
}

func TestStoreOpenRejectsEmptyStoreFile(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "empty.db")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := OpenStore(ctx, Config{Path: path}); !errors.Is(err, ErrStoreCorrupt) {
		t.Fatalf("error = %v, want ErrStoreCorrupt", err)
	}
}

func TestStoreOpenRejectsNonRegularPath(t *testing.T) {
	ctx := context.Background()
	if _, err := OpenStore(ctx, Config{Path: t.TempDir()}); !errors.Is(err, ErrStoreCorrupt) {
		t.Fatalf("error = %v, want ErrStoreCorrupt", err)
	}
}

func TestStoreRejectsEmptyPath(t *testing.T) {
	ctx := context.Background()
	if _, err := OpenStore(ctx, Config{}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("OpenStore error = %v, want ErrInvalidArgument", err)
	}
	if _, err := InitializeStore(ctx, Config{}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("InitializeStore error = %v, want ErrInvalidArgument", err)
	}
}

func TestStoreOpenRejectsUnsupportedSchemaVersion(t *testing.T) {
	ctx := context.Background()
	store, path := newTestStore(t)
	_ = store.Close()

	db := rawDB(t, path)
	mustExec(t, db, `UPDATE meta SET schema_version = 5`)

	if _, err := OpenStore(ctx, Config{Path: path}); !errors.Is(err, ErrStoreVersion) {
		t.Fatalf("error = %v, want ErrStoreVersion", err)
	}
}

func TestStoreOpenRefusesV1WithoutNormalizing(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "runs-v1.db")
	db := rawDB(t, path)
	v1RunsTableSQL := strings.Replace(createRunsTableSQL,
		"  sandbox_generation              TEXT NOT NULL,\n", "", 1)
	if v1RunsTableSQL == createRunsTableSQL {
		t.Fatal("sandbox generation column marker was not found")
	}
	mustExec(t, db, createMetaTableSQL)
	mustExec(t, db, `INSERT INTO meta (schema_version) VALUES (1)`)
	mustExec(t, db, v1RunsTableSQL)

	if _, err := OpenStore(ctx, Config{Path: path}); !errors.Is(err, ErrStoreVersion) {
		t.Fatalf("error = %v, want ErrStoreVersion", err)
	}
	var version, sandboxColumns int
	if err := db.QueryRow(`SELECT schema_version FROM meta`).Scan(&version); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('runs') WHERE name = 'sandbox_generation'`,
	).Scan(&sandboxColumns); err != nil {
		t.Fatalf("read runs columns: %v", err)
	}
	if version != 1 || sandboxColumns != 0 {
		t.Fatalf("v1 store was normalized: version=%d sandbox_columns=%d", version, sandboxColumns)
	}
}

// TestStoreOpenRefusesV2WithoutNormalizing proves a schema-v2 store (which
// carried the untyped v1 unit-property vector) is refused as ErrStoreVersion
// rather than normalized or misread as a corrupt v3 row. The version gate
// fires before any row validation, so even a v2 row whose digest is invalid
// under v3 surfaces as a version refusal, never corruption.
func TestStoreOpenRefusesV2WithoutNormalizing(t *testing.T) {
	ctx := context.Background()
	store, path := newTestStore(t)
	run, err := store.Reserve(ctx, reserveInput(t, nil))
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	_ = store.Close()

	// A real v2 store held the v1-vector digest; plant one so a broken open
	// that skipped the version gate would classify this row as corrupt.
	db := rawDB(t, path)
	mustExec(t, db, `UPDATE meta SET schema_version = 2`)
	mustExec(t, db, `UPDATE runs SET unit_property_digest = ? WHERE run_id = ?`,
		"2d8f97fabc47bb0510e858f9e02322b8f61ba81c53b04e6b840c634f2e9ebbc4", run.RunID)

	if _, err := OpenStore(ctx, Config{Path: path}); !errors.Is(err, ErrStoreVersion) {
		t.Fatalf("error = %v, want ErrStoreVersion", err)
	}
	var version int
	if err := db.QueryRow(`SELECT schema_version FROM meta`).Scan(&version); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if version != 2 {
		t.Fatalf("v2 store was normalized: version=%d", version)
	}
}

// TestStoreOpenRefusesV3WithoutNormalizing proves a schema-v3 store (which
// lacked the launch-ticket columns and the authorized phase edge) is refused
// as ErrStoreVersion rather than normalized or misread as a corrupt v4 row.
// The table is genuinely v3-shaped, so a broken open that skipped the version
// gate would hit a DDL mismatch (ErrStoreCorrupt) or a missing-column select
// before it could ever validate a row; the version refusal proves the gate
// fires first.
func TestStoreOpenRefusesV3WithoutNormalizing(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "runs-v3.db")

	// Derive the v3 runs DDL: v4 minus the launch-ticket columns, the three
	// ticket-parity constraints, and the 'authorized' phase enum entry.
	const ticketColumns = "  launch_ticket_digest            TEXT,\n  launch_ticket_json              TEXT,\n"
	v3Runs := strings.Replace(createRunsTableSQL, ticketColumns, "", 1)
	if v3Runs == createRunsTableSQL {
		t.Fatal("v3 ticket column marker was not found")
	}
	const ticketConstraints = `  ),
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
  )`
	v3Runs = strings.Replace(v3Runs, ticketConstraints, "  )", 1)
	if strings.Contains(v3Runs, "launch_ticket") {
		t.Fatal("v3 ticket constraint marker was not removed")
	}
	v3Runs = strings.Replace(v3Runs,
		"phase IN ('prepared','starting','authorized','running','terminal')",
		"phase IN ('prepared','starting','running','terminal')", 1)
	if strings.Contains(v3Runs, "authorized") {
		t.Fatal("v3 phase enum marker was not reverted")
	}

	db := rawDB(t, path)
	mustExec(t, db, createMetaTableSQL)
	mustExec(t, db, `INSERT INTO meta (schema_version) VALUES (3)`)
	mustExec(t, db, v3Runs)

	if _, err := OpenStore(ctx, Config{Path: path}); !errors.Is(err, ErrStoreVersion) {
		t.Fatalf("error = %v, want ErrStoreVersion", err)
	}
	var version int
	if err := db.QueryRow(`SELECT schema_version FROM meta`).Scan(&version); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if version != 3 {
		t.Fatalf("v3 store was normalized: version=%d", version)
	}
	var ticketColumnCount int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('runs') WHERE name = 'launch_ticket_digest'`,
	).Scan(&ticketColumnCount); err != nil {
		t.Fatalf("read runs columns: %v", err)
	}
	if ticketColumnCount != 0 {
		t.Fatalf("v3 store was migrated: launch_ticket_digest column count=%d", ticketColumnCount)
	}
}

func TestStoreOpenRejectsCorruptSchema(t *testing.T) {
	ctx := context.Background()
	store, path := newTestStore(t)
	_ = store.Close()

	db := rawDB(t, path)
	mustExec(t, db, `ALTER TABLE runs RENAME TO runs_backup`)
	mustExec(t, db, `CREATE TABLE runs (run_id TEXT PRIMARY KEY) STRICT`)

	if _, err := OpenStore(ctx, Config{Path: path}); !errors.Is(err, ErrStoreCorrupt) {
		t.Fatalf("error = %v, want ErrStoreCorrupt", err)
	}
}

// TestStoreOpenRejectsConstraintDrift covers a table recreated with the same
// column names but without the constraints that carry the authority invariant.
// Column-name checks alone would miss these; the open compares the full DDL.
// The drift variants are derived from createRunsTableSQL, so a change to the
// production DDL cannot silently turn this test into a vacuous pass.
func TestStoreOpenRejectsConstraintDrift(t *testing.T) {
	ctx := context.Background()

	dropUnitNameUnique := strings.Replace(createRunsTableSQL, "  UNIQUE (unit_name),\n", "", 1)
	dropSessionKeyUnique := strings.Replace(createRunsTableSQL,
		"  UNIQUE (application_session_id, application_session_generation),\n", "", 1)
	dropChecks := strings.Replace(createRunsTableSQL,
		"  CONSTRAINT chk_runs_phase CHECK (phase IN ('prepared','starting','authorized','running','terminal')),\n", "", 1)
	if dropUnitNameUnique == createRunsTableSQL || dropSessionKeyUnique == createRunsTableSQL || dropChecks == createRunsTableSQL {
		t.Fatalf("constraint markers were not found in createRunsTableSQL; the oracle is vacuous")
	}

	cases := []struct {
		name string
		ddl  string
	}{
		{"missing unit name uniqueness", dropUnitNameUnique},
		{"missing session key uniqueness", dropSessionKeyUnique},
		{"missing phase check constraint", dropChecks},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, path := newTestStore(t)
			_ = store.Close()

			db := rawDB(t, path)
			mustExec(t, db, `ALTER TABLE runs RENAME TO runs_backup`)
			mustExec(t, db, tc.ddl)

			if _, err := OpenStore(ctx, Config{Path: path}); !errors.Is(err, ErrStoreCorrupt) {
				t.Fatalf("error = %v, want ErrStoreCorrupt", err)
			}
		})
	}

	// control: the unmodified derived DDL must still open, proving the oracle
	// does not reject every table it is fed
	t.Run("control unmodified ddl opens", func(t *testing.T) {
		store, path := newTestStore(t)
		_ = store.Close()

		db := rawDB(t, path)
		mustExec(t, db, `ALTER TABLE runs RENAME TO runs_backup`)
		mustExec(t, db, createRunsTableSQL)

		reopened, err := OpenStore(ctx, Config{Path: path})
		if err != nil {
			t.Fatalf("unmodified DDL rejected: %v", err)
		}
		_ = reopened.Close()
	})
}

func TestStoreOpenRejectsCorruptRows(t *testing.T) {
	ctx := context.Background()
	evidence := strings.Repeat("c", 64)
	cases := []struct {
		name    string
		corrupt func(t *testing.T, db *sql.DB, runID string)
	}{
		{"invalid phase enum", func(t *testing.T, db *sql.DB, runID string) {
			setCheckOff(t, db, `UPDATE runs SET phase = 'bogus' WHERE run_id = ?`, runID)
		}},
		{"request json does not match digest", func(t *testing.T, db *sql.DB, runID string) {
			mustExec(t, db, `UPDATE runs SET launch_request_json = '{}' WHERE run_id = ?`, runID)
		}},
		{"request digest malformed", func(t *testing.T, db *sql.DB, runID string) {
			mustExec(t, db, `UPDATE runs SET launch_request_digest = 'zz' WHERE run_id = ?`, runID)
		}},
		{"session id not a uuid", func(t *testing.T, db *sql.DB, runID string) {
			mustExec(t, db, `UPDATE runs SET application_session_id = 'not-a-uuid' WHERE run_id = ?`, runID)
		}},
		{"zero generation", func(t *testing.T, db *sql.DB, runID string) {
			setCheckOff(t, db, `UPDATE runs SET application_session_generation = 0 WHERE run_id = ?`, runID)
		}},
		{"boot id malformed", func(t *testing.T, db *sql.DB, runID string) {
			mustExec(t, db, `UPDATE runs SET boot_id = 'bogus' WHERE run_id = ?`, runID)
		}},
		{"sandbox generation malformed", func(t *testing.T, db *sql.DB, runID string) {
			mustExec(t, db, `UPDATE runs SET sandbox_generation = 'bogus' WHERE run_id = ?`, runID)
		}},
		{"sandbox generation changed", func(t *testing.T, db *sql.DB, runID string) {
			mustExec(t, db,
				`UPDATE runs SET sandbox_generation = '723e4567-e89b-42d3-a456-426614174000' WHERE run_id = ?`,
				runID)
		}},
		{"unit property digest malformed", func(t *testing.T, db *sql.DB, runID string) {
			mustExec(t, db, `UPDATE runs SET unit_property_digest = 'bogus' WHERE run_id = ?`, runID)
		}},
		{"unit property digest changed", func(t *testing.T, db *sql.DB, runID string) {
			mustExec(t, db, `UPDATE runs SET unit_property_digest = ? WHERE run_id = ?`, strings.Repeat("a", 64), runID)
		}},
		{"unit name malformed", func(t *testing.T, db *sql.DB, runID string) {
			mustExec(t, db, `UPDATE runs SET unit_name = '/etc/passwd' WHERE run_id = ?`, runID)
		}},
		{"unit name changed", func(t *testing.T, db *sql.DB, runID string) {
			mustExec(t, db, `UPDATE runs SET unit_name = ? WHERE run_id = ?`,
				"noisedeck-confined-"+strings.Repeat("a", 64)+".service", runID)
		}},
		{"pty binding json does not match digest", func(t *testing.T, db *sql.DB, runID string) {
			mustExec(t, db, `UPDATE runs SET pty_binding_json = '{}' WHERE run_id = ?`, runID)
		}},
		{"cleanup complete without positive evidence", func(t *testing.T, db *sql.DB, runID string) {
			setCheckOff(t, db, `UPDATE runs SET cleanup_state = 'complete' WHERE run_id = ?`, runID)
		}},
		{"terminal without positive evidence", func(t *testing.T, db *sql.DB, runID string) {
			setCheckOff(t, db,
				`UPDATE runs SET phase = 'terminal', terminal_outcome = 'success', cleanup_state = 'pending'
				 WHERE run_id = ?`, runID)
		}},
		{"terminal outcome invalid", func(t *testing.T, db *sql.DB, runID string) {
			setCheckOff(t, db,
				`UPDATE runs SET phase = 'terminal', terminal_outcome = 'weird',
				        empty_cgroup_evidence_digest = ?, cleanup_state = 'pending'
				 WHERE run_id = ?`, evidence, runID)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, path := newTestStore(t)
			run, err := store.Reserve(ctx, reserveInput(t, nil))
			if err != nil {
				t.Fatalf("Reserve: %v", err)
			}
			_ = store.Close()

			db := rawDB(t, path)
			tc.corrupt(t, db, run.RunID)

			if _, err := OpenStore(ctx, Config{Path: path}); !errors.Is(err, ErrStoreCorrupt) {
				t.Fatalf("error = %v, want ErrStoreCorrupt", err)
			}
		})
	}
}

// TestStoreOpenRejectsCorruptTicket covers reopen-time validation of the
// durable launch ticket on an authorized row. Each case advances a run to
// authorized, then corrupts the stored ticket so the open must fail closed as
// ErrStoreCorrupt rather than accepting an unverified authority record.
func TestStoreOpenRejectsCorruptTicket(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name    string
		corrupt func(t *testing.T, db *sql.DB, run *Run)
	}{
		{"ticket json does not match digest", func(t *testing.T, db *sql.DB, run *Run) {
			mustExec(t, db, `UPDATE runs SET launch_ticket_json = '{}' WHERE run_id = ?`, run.RunID)
		}},
		{"ticket digest malformed", func(t *testing.T, db *sql.DB, run *Run) {
			mustExec(t, db, `UPDATE runs SET launch_ticket_digest = 'zz' WHERE run_id = ?`, run.RunID)
		}},
		{"ticket digest changed", func(t *testing.T, db *sql.DB, run *Run) {
			mustExec(t, db, `UPDATE runs SET launch_ticket_digest = ? WHERE run_id = ?`,
				strings.Repeat("9", 64), run.RunID)
		}},
		{"one-sided ticket json only", func(t *testing.T, db *sql.DB, run *Run) {
			setCheckOff(t, db, `UPDATE runs SET launch_ticket_digest = NULL WHERE run_id = ?`, run.RunID)
		}},
		{"ticket present during starting", func(t *testing.T, db *sql.DB, run *Run) {
			setCheckOff(t, db, `UPDATE runs SET phase = 'starting' WHERE run_id = ?`, run.RunID)
		}},
		{"authorized row missing ticket", func(t *testing.T, db *sql.DB, run *Run) {
			setCheckOff(t, db,
				`UPDATE runs SET launch_ticket_digest = NULL, launch_ticket_json = NULL WHERE run_id = ?`,
				run.RunID)
		}},
		{"ticket binds different run", func(t *testing.T, db *sql.DB, run *Run) {
			foreign := canonicalTicketMutated(t, run, func(t *LaunchTicketV1) {
				t.LaunchRequestDigest = strings.Repeat("9", 64)
			})
			mustExec(t, db, `UPDATE runs SET launch_ticket_digest = ?, launch_ticket_json = ? WHERE run_id = ?`,
				foreign.Digest, foreign.CanonicalJSON, run.RunID)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, path := newTestStore(t)
			run, err := store.Reserve(ctx, reserveInput(t, nil))
			if err != nil {
				t.Fatalf("Reserve: %v", err)
			}
			run = startAndAuthorize(t, ctx, store, run)
			_ = store.Close()

			db := rawDB(t, path)
			tc.corrupt(t, db, run)

			if _, err := OpenStore(ctx, Config{Path: path}); !errors.Is(err, ErrStoreCorrupt) {
				t.Fatalf("error = %v, want ErrStoreCorrupt", err)
			}
		})
	}
}

func TestStoreOpenRejectsMissingRunsTable(t *testing.T) {
	ctx := context.Background()
	store, path := newTestStore(t)
	_ = store.Close()

	db := rawDB(t, path)
	mustExec(t, db, `DROP TABLE runs`)

	if _, err := OpenStore(ctx, Config{Path: path}); !errors.Is(err, ErrStoreCorrupt) {
		t.Fatalf("error = %v, want ErrStoreCorrupt", err)
	}
}

func TestStoreOpenRejectsMissingMetaTable(t *testing.T) {
	ctx := context.Background()
	store, path := newTestStore(t)
	_ = store.Close()

	db := rawDB(t, path)
	mustExec(t, db, `DROP TABLE meta`)

	if _, err := OpenStore(ctx, Config{Path: path}); !errors.Is(err, ErrStoreCorrupt) {
		t.Fatalf("error = %v, want ErrStoreCorrupt", err)
	}
}

func TestStoreOpenRejectsDuplicateMetaRows(t *testing.T) {
	ctx := context.Background()
	store, path := newTestStore(t)
	_ = store.Close()

	db := rawDB(t, path)
	mustExec(t, db, `INSERT INTO meta (schema_version) VALUES (?)`, storeSchemaVersion)

	if _, err := OpenStore(ctx, Config{Path: path}); !errors.Is(err, ErrStoreCorrupt) {
		t.Fatalf("error = %v, want ErrStoreCorrupt", err)
	}
}

func TestStoreWriteContentionFailsClosed(t *testing.T) {
	store, path := newTestStore(t)
	ctx := context.Background()
	run, err := store.Reserve(ctx, reserveInput(t, nil))
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}

	// A second handle with a tiny busy timeout so contention surfaces as a
	// bounded failure instead of a hang. It must be opened before the raw
	// connection takes the write lock, because opening verifies the store.
	slowStore, err := OpenStore(ctx, Config{Path: path, BusyTimeout: 50 * time.Millisecond})
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer func() { _ = slowStore.Close() }()

	raw := rawDB(t, path)
	conn, err := raw.Conn(ctx)
	if err != nil {
		t.Fatalf("raw Conn: %v", err)
	}
	if _, err := conn.ExecContext(ctx, `BEGIN EXCLUSIVE`); err != nil {
		t.Fatalf("BEGIN EXCLUSIVE: %v", err)
	}

	_, err = slowStore.TransitionToStarting(ctx, run.RunID)
	if !errors.Is(err, ErrStoreUnavailable) {
		_, _ = conn.ExecContext(ctx, `ROLLBACK`)
		t.Fatalf("error = %v, want ErrStoreUnavailable under contention", err)
	}
	if _, err := conn.ExecContext(ctx, `ROLLBACK`); err != nil {
		t.Fatalf("ROLLBACK: %v", err)
	}
	_ = conn.Close()

	// the contended transition left no partial phase advance
	got, err := slowStore.Get(ctx, run.ApplicationSessionID, run.ApplicationSessionGeneration)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Phase != PhasePrepared {
		t.Fatalf("phase = %s, want prepared after contended write", got.Phase)
	}
}

func TestStoreOpenRejectsSymlinkStorePath(t *testing.T) {
	ctx := context.Background()
	realStore, realPath := newTestStore(t)
	_ = realStore.Close()

	linkPath := filepath.Join(t.TempDir(), "runs-link.db")
	if err := os.Symlink(realPath, linkPath); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	if _, err := OpenStore(ctx, Config{Path: linkPath}); !errors.Is(err, ErrStoreCorrupt) {
		t.Fatalf("error = %v, want ErrStoreCorrupt", err)
	}
	// the target must be untouched, including its mode
	info, err := os.Stat(realPath)
	if err != nil {
		t.Fatalf("Stat target: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("symlinked open changed target mode to %v", info.Mode().Perm())
	}
}

func TestStoreSecureFilesRejectsSymlinkSidecar(t *testing.T) {
	ctx := context.Background()
	store, path := newTestStore(t)
	if _, err := store.Reserve(ctx, reserveInput(t, nil)); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	_ = store.Close()

	// Plant a symlink where the WAL sidecar belongs, pointing at an arbitrary
	// loose file that must never be chmodded through the link.
	target := filepath.Join(t.TempDir(), "unrelated")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	before, err := os.Stat(target)
	if err != nil {
		t.Fatalf("Stat target: %v", err)
	}
	if err := os.Symlink(target, path+"-wal"); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	if err := store.secureFiles(); !errors.Is(err, ErrStoreCorrupt) {
		t.Fatalf("secureFiles = %v, want ErrStoreCorrupt", err)
	}
	after, err := os.Stat(target)
	if err != nil {
		t.Fatalf("Stat target: %v", err)
	}
	if after.Mode().Perm() != before.Mode().Perm() {
		t.Fatalf("secureFiles changed target mode through the symlink: %v -> %v",
			before.Mode().Perm(), after.Mode().Perm())
	}
}

// TestStoreOpenRejectsSidecarSymlinkBeforeWrite proves the sidecar symlink
// refusal happens before the first statement, which is what would create and
// write the WAL/SHM files through the link.
func TestStoreOpenRejectsSidecarSymlinkBeforeWrite(t *testing.T) {
	ctx := context.Background()
	store, path := newTestStore(t)
	if _, err := store.Reserve(ctx, reserveInput(t, nil)); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	_ = store.Close()

	target := filepath.Join(t.TempDir(), "wal-target")
	const sentinel = "sentinel-bytes"
	if err := os.WriteFile(target, []byte(sentinel), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.Symlink(target, path+"-wal"); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	if _, err := OpenStore(ctx, Config{Path: path}); !errors.Is(err, ErrStoreCorrupt) {
		t.Fatalf("error = %v, want ErrStoreCorrupt", err)
	}
	body, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile target: %v", err)
	}
	if string(body) != sentinel {
		t.Fatalf("sidecar symlink was written through: target now %q", body)
	}
}

func TestStoreMapsContextCancellationNotToUnavailable(t *testing.T) {
	store, _ := newTestStore(t)
	in := reserveInput(t, nil)

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Reserve(canceledCtx, in); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Reserve = %v, want context.Canceled", err)
	}

	expiredCtx, cancel := context.WithTimeout(context.Background(), -time.Second)
	defer cancel()
	if _, err := store.Reserve(expiredCtx, in); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired Reserve = %v, want context.DeadlineExceeded", err)
	}
}

func TestStoreBusyTimeoutRoundsUpSubMillisecond(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "runs.db")
	store, err := InitializeStore(ctx, Config{Path: path, BusyTimeout: 500 * time.Microsecond})
	if err != nil {
		t.Fatalf("InitializeStore: %v", err)
	}
	_ = store.Close()

	reopened, err := OpenStore(ctx, Config{Path: path, BusyTimeout: 500 * time.Microsecond})
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer func() { _ = reopened.Close() }()

	db := rawDB(t, path)
	var busy int
	if err := db.QueryRow(`PRAGMA busy_timeout`).Scan(&busy); err != nil {
		t.Fatalf("busy_timeout: %v", err)
	}
	if busy < 1 {
		t.Fatalf("busy_timeout = %d, want at least 1ms (not truncated to 0)", busy)
	}
}

func TestStoreRemoveStoreFilesCleansSidecarsWithoutFollowingLinks(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(dir, "runs.db")
	if err := os.WriteFile(abs, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.WriteFile(abs+"-shm", []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	// an attacker-controlled sidecar link must be unlinked, never followed
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("sentinel"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.Symlink(target, abs+"-wal"); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	removeStoreFiles(abs)

	for _, p := range []string{abs, abs + "-wal", abs + "-shm"} {
		if _, err := os.Lstat(p); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s still exists after removeStoreFiles: %v", p, err)
		}
	}
	body, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile target: %v", err)
	}
	if string(body) != "sentinel" {
		t.Fatalf("symlink target was touched: %q", body)
	}
}
