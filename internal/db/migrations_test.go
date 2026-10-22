package db

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// NOTE: this package deliberately does NOT blank-import the sqlite3 driver.
// It is registered by db.go, in production code. Importing it here as well
// would mask the case where that production import is missing — which is
// exactly the bug this comment exists to prevent from returning: tests would
// pass while any real caller of Open failed with `unknown driver "sqlite3"`.

// dsn builds a connection string with the durability pragmas. They are DSN
// parameters rather than one-off statements because database/sql pools
// connections and a PRAGMA run on one connection does not apply to the others.
func dsn(path string, busyTimeoutMS int) string {
	return fmt.Sprintf(
		"file:%s?_journal_mode=WAL&_synchronous=FULL&_busy_timeout=%d&_foreign_keys=ON&_recursive_triggers=ON",
		path, busyTimeoutMS)
}

// openMigrated returns a migrated database on a fresh file.
func openMigrated(t *testing.T) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "control.db")
	db := open(t, path, 5000)
	if _, err := Migrate(context.Background(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db, path
}

func open(t *testing.T, path string, busyTimeoutMS int) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", dsn(path, busyTimeoutMS))
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Ping(); err != nil {
		t.Fatalf("ping %s: %v", path, err)
	}
	return db
}

// assertRejected fails unless err is a rejection whose message contains want.
// Asserting only err != nil would let a typo in the test's own SQL masquerade
// as the constraint firing, which would make every negative test below
// meaningless.
func assertRejected(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("statement was accepted; want rejection containing %q", want)
	}
	if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(want)) {
		t.Fatalf("rejected for the wrong reason: got %v, want a failure containing %q",
			err, want)
	}
}

// seedProject inserts a project and returns its id.
func seedProject(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	now := FormatTime(time.Now())
	_, err := db.Exec(
		`INSERT INTO projects (id, name, canonical_path, created_at, last_used_at)
		 VALUES (?, ?, ?, ?, ?)`,
		id, "proj-"+id, "/workspace/"+id, now, now)
	if err != nil {
		t.Fatalf("seed project: %v", err)
	}
	return id
}

// seedSession inserts a running session and returns its id.
func seedSession(t *testing.T, db *sql.DB, id, projectID string) string {
	t.Helper()
	now := FormatTime(time.Now())
	_, err := db.Exec(
		`INSERT INTO sessions (id, project_id, provider, tmux_socket_path,
		     tmux_session_name, tmux_session_id, tmux_pane_id, identity_marker,
		     pane_pid, process_group_id, process_start_ticks, expected_executable,
		     status, created_at, updated_at)
		 VALUES (?, ?, 'claude', '/run/ac.sock', ?, '$1', '%1', ?, 4242, 4242,
		         99887766, '/usr/local/bin/claude', 'running', ?, ?)`,
		id, projectID, "noisedeck-"+id, "marker-"+id, now, now)
	if err != nil {
		t.Fatalf("seed session: %v", err)
	}
	return id
}

// seedMessage inserts a composer-era message row. It exists only to seed
// 004-era databases for the migration-005 test; the composer table no longer
// survives a full migrate.
func seedMessage(t *testing.T, db *sql.DB, id, sessionID string, seq int, status string) {
	t.Helper()
	now := FormatTime(time.Now())
	_, err := db.Exec(
		`INSERT INTO composer_messages
		     (id, session_id, sequence, body, body_sha256, status, created_at, updated_at)
		 VALUES (?, ?, ?, 'hello', 'abc123', ?, ?, ?)`,
		id, sessionID, seq, status, now, now)
	if err != nil {
		t.Fatalf("seed message (status %q): %v", status, err)
	}
}

func TestMigrateAppliesAndIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.db")
	db := open(t, path, 5000)

	ran, err := Migrate(context.Background(), db)
	if err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	if len(ran) == 0 {
		t.Fatal("first migrate applied nothing")
	}

	// Every table the control database is the authority for must exist. The
	// composer_messages table is NOT among them: migration 005 retires it.
	for _, table := range []string{
		"projects", "sessions", "controller_leases",
		"taskmaster_runs", "audit_events",
		"schema_migrations",
	} {
		var name string
		err := db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table,
		).Scan(&name)
		if err != nil {
			t.Errorf("table %q missing after migrate: %v", table, err)
		}
	}

	again, err := Migrate(context.Background(), db)
	if err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if len(again) != 0 {
		t.Errorf("second migrate applied %v, want nothing", again)
	}
}

// TestMigrate005RetiresComposer proves migration 005 removes every composer
// surface without losing audit history. It seeds a genuine 004-era database
// (with a message, a dirty session, and a message-linked audit row), applies
// only 005, and checks the drop.
func TestMigrate005RetiresComposer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.db")
	db := open(t, path, 5000)
	ctx := context.Background()

	// Bring the database to version 004, the last composer-era schema.
	migrations, err := LoadMigrations()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, err := db.ExecContext(ctx, createSchemaMigrations); err != nil {
		t.Fatalf("create schema_migrations: %v", err)
	}
	for _, m := range migrations {
		if m.Version > 4 {
			break
		}
		if err := applyOne(ctx, db, m); err != nil {
			t.Fatalf("apply migration %d: %v", m.Version, err)
		}
	}

	// A real 004-era database holds all three surfaces about to be removed.
	p := seedProject(t, db, "p1")
	s := seedSession(t, db, "s1", p)
	now := FormatTime(time.Now())
	seedMessage(t, db, "m1", s, 1, "held")
	if _, err := db.Exec(
		`UPDATE sessions SET input_dirty = 1, input_dirty_since = ?, input_paste_open = 1
		 WHERE id = ?`, now, s); err != nil {
		t.Fatalf("mark dirty session: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO audit_events (session_id, event_type, message_id, details, created_at)
		 VALUES (?, 'composer.submitted', 'm1', 'pre-retirement audit', ?)`, s, now); err != nil {
		t.Fatalf("seed message-linked audit row: %v", err)
	}

	// Apply the retirement migrations (005 and later).
	ran, err := Migrate(ctx, db)
	if err != nil {
		t.Fatalf("migrate to 005+: %v", err)
	}
	if len(ran) == 0 || ran[0] != 5 {
		t.Fatalf("migrate applied %v, want 005 first", ran)
	}

	// The composer table is gone; so are the durable dirty columns; the
	// audit link column is gone; and the FK ordering held (no error above).
	var n int
	if err := db.QueryRow(
		`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='composer_messages'`,
	).Scan(&n); err != nil {
		t.Fatalf("query composer_messages: %v", err)
	}
	if n != 0 {
		t.Error("composer_messages table survived migration 005")
	}
	if err := db.QueryRow(
		`SELECT count(*) FROM pragma_table_info('sessions') WHERE name IN
		     ('input_dirty', 'input_dirty_since', 'input_paste_open')`,
	).Scan(&n); err != nil {
		t.Fatalf("query sessions columns: %v", err)
	}
	if n != 0 {
		t.Errorf("durable input-line columns survived migration 005 (%d found)", n)
	}
	if err := db.QueryRow(
		`SELECT count(*) FROM pragma_table_info('audit_events') WHERE name='message_id'`,
	).Scan(&n); err != nil {
		t.Fatalf("query audit columns: %v", err)
	}
	if n != 0 {
		t.Error("audit_events.message_id survived migration 005")
	}

	// The retirement is content-free: the audit history that
	// mattered survives, but no composer body is readable anywhere.
	var auditRows int
	if err := db.QueryRow(`SELECT count(*) FROM audit_events`).Scan(&auditRows); err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if auditRows != 1 {
		t.Errorf("audit rows = %d after migration 005, want 1 (history preserved)", auditRows)
	}
	var bodies int
	if err := db.QueryRow(
		`SELECT count(*) FROM sqlite_master WHERE type='table' AND
		     (name='composer_messages' OR name LIKE 'composer_messages%')`,
	).Scan(&bodies); err != nil {
		t.Fatalf("query body storage: %v", err)
	}
	if bodies != 0 {
		t.Error("a composer body storage object survived migration 005")
	}

	// And the migration is idempotent: a second run applies nothing.
	again, err := Migrate(ctx, db)
	if err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if len(again) != 0 {
		t.Errorf("second migrate applied %v, want nothing", again)
	}
}

func TestMigrateRejectsEditedMigration(t *testing.T) {
	db, _ := openMigrated(t)

	// Simulate a migration file being edited after it was applied.
	if _, err := db.Exec(
		`UPDATE schema_migrations SET checksum = 'deadbeef' WHERE version = 1`,
	); err != nil {
		t.Fatalf("tamper: %v", err)
	}

	_, err := Migrate(context.Background(), db)
	if err == nil {
		t.Fatal("migrate accepted an edited migration; want failure")
	}
	if !strings.Contains(err.Error(), "modified after it was applied") {
		t.Errorf("unhelpful error for edited migration: %v", err)
	}
}

func TestMigrateRejectsUnknownAppliedVersion(t *testing.T) {
	db, _ := openMigrated(t)

	// A newer build migrated this database; this binary does not know v999.
	if _, err := db.Exec(
		`INSERT INTO schema_migrations (version, name, checksum, applied_at)
		 VALUES (999, 'from_the_future', 'x', ?)`, FormatTime(time.Now()),
	); err != nil {
		t.Fatalf("insert future migration: %v", err)
	}

	_, err := Migrate(context.Background(), db)
	if err == nil {
		t.Fatal("migrate accepted an unknown applied version; want failure")
	}
	if !strings.Contains(err.Error(), "newer build") {
		t.Errorf("unhelpful error for unknown version: %v", err)
	}
}

func TestLoadMigrationsAreOrderedAndChecksummed(t *testing.T) {
	migrations, err := LoadMigrations()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(migrations) == 0 {
		t.Fatal("no migrations embedded")
	}
	for i, m := range migrations {
		if i > 0 && m.Version <= migrations[i-1].Version {
			t.Errorf("migrations out of order at %d: %d after %d",
				i, m.Version, migrations[i-1].Version)
		}
		if len(m.Checksum) != 64 {
			t.Errorf("migration %d: checksum %q is not a sha256 hex digest",
				m.Version, m.Checksum)
		}
		if strings.TrimSpace(m.SQL) == "" {
			t.Errorf("migration %d is empty", m.Version)
		}
	}
}

func TestForeignKeysEnforced(t *testing.T) {
	db, _ := openMigrated(t)
	now := FormatTime(time.Now())

	// The row is otherwise valid — full identity tuple — so the only thing
	// wrong with it is the dangling project reference. Omitting the tuple would
	// trip a CHECK first and the test would pass without ever exercising the
	// foreign key.
	_, err := db.Exec(
		`INSERT INTO sessions (id, project_id, provider, tmux_socket_path,
		     tmux_session_name, tmux_session_id, tmux_pane_id, identity_marker,
		     pane_pid, process_group_id, process_start_ticks, expected_executable,
		     status, created_at, updated_at)
		 VALUES ('s1', 'no-such-project', 'claude', '/run/ac.sock', 'noisedeck-s1',
		         '$1', '%1', 'marker', 4242, 4242, 99887766, '/usr/local/bin/claude',
		         'running', ?, ?)`, now, now)
	assertRejected(t, err, "foreign key")
}

// Removing a project must not silently destroy its session history.
func TestProjectDeleteRestrictedWhileSessionsExist(t *testing.T) {
	db, _ := openMigrated(t)
	p := seedProject(t, db, "p1")
	seedSession(t, db, "s1", p)

	_, err := db.Exec(`DELETE FROM projects WHERE id = ?`, p)
	if err == nil {
		t.Fatal("deleted a project that still has sessions; want RESTRICT")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "foreign key") {
		t.Errorf("expected a foreign key error, got: %v", err)
	}
}

func TestCheckConstraints(t *testing.T) {
	db, _ := openMigrated(t)
	p := seedProject(t, db, "p1")
	s := seedSession(t, db, "s1", p)
	now := FormatTime(time.Now())

	cases := []struct {
		name string
		want string
		exec func() error
	}{
		{"unknown provider", "chk_sessions_provider", func() error {
			_, err := db.Exec(
				`INSERT INTO sessions (id, project_id, provider, tmux_socket_path,
				     tmux_session_name, tmux_session_id, tmux_pane_id, identity_marker,
				     pane_pid, process_group_id, process_start_ticks, expected_executable,
				     status, created_at, updated_at)
				 VALUES ('s-bad', ?, 'gpt5', '/run/ac.sock', 'noisedeck-bad', '$1', '%1', 'm', 1, 1, 1,
				         '/x', 'running', ?, ?)`,
				p, now, now)
			return err
		}},
		{"unknown session status", "chk_sessions_status", func() error {
			_, err := db.Exec(
				`INSERT INTO sessions (id, project_id, provider, tmux_socket_path,
				     tmux_session_name, tmux_session_id, tmux_pane_id, identity_marker,
				     pane_pid, process_group_id, process_start_ticks, expected_executable,
				     status, created_at, updated_at)
				 VALUES ('s-bad2', ?, 'claude', '/run/ac.sock', 'noisedeck-bad2', '$1', '%1', 'm', 1, 1, 1,
				         '/x', 'idle', ?, ?)`,
				p, now, now)
			return err
		}},
		{"exit evidence while running", "chk_sessions_no_exit_while_live", func() error {
			_, err := db.Exec(
				`UPDATE sessions SET exited_at = ? WHERE id = ?`, now, s)
			return err
		}},
		{"taskmaster run exited without finish time", "chk_taskmaster_finished", func() error {
			_, err := db.Exec(
				`INSERT INTO taskmaster_runs (id, project_id, argv, status, started_at)
				 VALUES ('r-bad', ?, '["task-master"]', 'exited', ?)`, p, now)
			return err
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertRejected(t, tc.exec(), tc.want)
		})
	}
}

// One controller attachment per session.
func TestOneLeasePerSession(t *testing.T) {
	db, _ := openMigrated(t)
	p := seedProject(t, db, "p1")
	s := seedSession(t, db, "s1", p)
	now := FormatTime(time.Now())

	insert := func(controller string) error {
		_, err := db.Exec(
			`INSERT INTO controller_leases
			     (session_id, controller_id, fencing_token, last_heartbeat, expires_at)
			 VALUES (?, ?, 1, ?, ?)`, s, controller, now, now)
		return err
	}
	if err := insert("browser-a"); err != nil {
		t.Fatalf("first lease: %v", err)
	}
	assertRejected(t, insert("browser-b"), "unique constraint failed")
}

// Taskmaster generation is serialized per project.
func TestOneRunningTaskmasterRunPerProject(t *testing.T) {
	db, _ := openMigrated(t)
	p1 := seedProject(t, db, "p1")
	p2 := seedProject(t, db, "p2")
	now := FormatTime(time.Now())

	start := func(id, project string) error {
		_, err := db.Exec(
			`INSERT INTO taskmaster_runs (id, project_id, argv, status, started_at)
			 VALUES (?, ?, '["task-master","parse-prd"]', 'running', ?)`, id, project, now)
		return err
	}

	if err := start("r1", p1); err != nil {
		t.Fatalf("first run: %v", err)
	}
	assertRejected(t, start("r2", p1), "unique constraint failed")
	// A different project may run concurrently.
	if err := start("r3", p2); err != nil {
		t.Errorf("run for a different project rejected: %v", err)
	}
	// Once the first finishes, the project may run again.
	if _, err := db.Exec(
		`UPDATE taskmaster_runs SET status='exited', exit_code=0, finished_at=?
		 WHERE id='r1'`, now); err != nil {
		t.Fatalf("finish run: %v", err)
	}
	if err := start("r4", p1); err != nil {
		t.Errorf("run rejected after the previous one finished: %v", err)
	}
}

// STRICT tables reject values of the wrong storage class, so a bug cannot
// quietly write "12" into an integer column and survive a later comparison.
func TestStrictTablesRejectWrongTypes(t *testing.T) {
	db, _ := openMigrated(t)
	p := seedProject(t, db, "p1")
	s := seedSession(t, db, "s1", p)

	_, err := db.Exec(`UPDATE sessions SET pane_pid = 'not-a-pid' WHERE id = ?`, s)
	assertRejected(t, err, "cannot store TEXT value in INTEGER column")
}

// WAL lets a reader proceed while a writer holds the write lock, and the busy
// timeout lets a second writer wait rather than fail immediately.
func TestWALAllowsConcurrentReadDuringWrite(t *testing.T) {
	db, path := openMigrated(t)
	seedProject(t, db, "p1")

	var mode string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatalf("journal_mode: %v", err)
	}
	if !strings.EqualFold(mode, "wal") {
		t.Fatalf("journal_mode = %q, want wal", mode)
	}

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	now := FormatTime(time.Now())
	if _, err := tx.Exec(
		`INSERT INTO projects (id, name, canonical_path, created_at, last_used_at)
		 VALUES ('p2','p2','/workspace/p2',?,?)`, now, now); err != nil {
		t.Fatalf("write in tx: %v", err)
	}

	// A separate connection reads committed data while that write is open.
	reader := open(t, path, 5000)
	var n int
	if err := reader.QueryRow(`SELECT count(*) FROM projects`).Scan(&n); err != nil {
		t.Fatalf("concurrent read during open write: %v", err)
	}
	if n != 1 {
		t.Errorf("reader saw %d projects, want 1 (uncommitted write must not be visible)", n)
	}
}

func TestBusyTimeoutLetsASecondWriterWait(t *testing.T) {
	db, path := openMigrated(t)
	seedProject(t, db, "p1")

	// Hold the write lock.
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	now := FormatTime(time.Now())
	if _, err := tx.Exec(
		`INSERT INTO projects (id, name, canonical_path, created_at, last_used_at)
		 VALUES ('p-hold','hold','/workspace/hold',?,?)`, now, now); err != nil {
		t.Fatalf("acquire write lock: %v", err)
	}

	patient := open(t, path, 5000)
	var wg sync.WaitGroup
	var writeErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, writeErr = patient.Exec(
			`INSERT INTO projects (id, name, canonical_path, created_at, last_used_at)
			 VALUES ('p-wait','wait','/workspace/wait',?,?)`, now, now)
	}()

	// Release the lock well inside the patient writer's 5s timeout.
	time.Sleep(200 * time.Millisecond)
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	wg.Wait()

	if writeErr != nil {
		t.Errorf("second writer failed despite a 5s busy timeout: %v", writeErr)
	}
}

func TestZeroBusyTimeoutFailsFastWhenLocked(t *testing.T) {
	db, path := openMigrated(t)

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	now := FormatTime(time.Now())
	if _, err := tx.Exec(
		`INSERT INTO projects (id, name, canonical_path, created_at, last_used_at)
		 VALUES ('p-hold','hold','/workspace/hold',?,?)`, now, now); err != nil {
		t.Fatalf("acquire write lock: %v", err)
	}

	// With no busy timeout the contending writer must fail rather than block,
	// which is what proves the timeout in the previous test did the waiting.
	impatient := open(t, path, 0)
	_, err = impatient.Exec(
		`INSERT INTO projects (id, name, canonical_path, created_at, last_used_at)
		 VALUES ('p-now','now','/workspace/now',?,?)`, now, now)
	if err == nil {
		t.Fatal("write succeeded with no busy timeout while the lock was held")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "locked") &&
		!strings.Contains(strings.ToLower(err.Error()), "busy") {
		t.Errorf("expected a locked/busy error, got: %v", err)
	}
}

// Exit evidence is constrained in both directions, so neither
// "exited with no evidence" nor "running with an exit code" is representable.
func TestExitEvidenceConstraints(t *testing.T) {
	db, _ := openMigrated(t)
	p := seedProject(t, db, "p1")
	s := seedSession(t, db, "s1", p)
	now := FormatTime(time.Now())

	t.Run("exited without a timestamp", func(t *testing.T) {
		_, err := db.Exec(`UPDATE sessions SET status='exited' WHERE id=?`, s)
		assertRejected(t, err, "chk_sessions_exit_cause")
	})

	t.Run("exit code while running", func(t *testing.T) {
		_, err := db.Exec(`UPDATE sessions SET exit_code=0 WHERE id=?`, s)
		assertRejected(t, err, "chk_sessions_exit_fields_need_exit")
	})

	t.Run("both code and signal", func(t *testing.T) {
		_, err := db.Exec(
			`UPDATE sessions SET status='exited', exited_at=?, exit_code=0, exit_signal='SIGTERM'
			 WHERE id=?`, now, s)
		assertRejected(t, err, "chk_sessions_exit_code_xor_signal")
	})

	t.Run("a properly evidenced exit is accepted", func(t *testing.T) {
		if _, err := db.Exec(
			`UPDATE sessions SET status='exited', exited_at=?, exit_code=0 WHERE id=?`,
			now, s); err != nil {
			t.Errorf("evidenced exit rejected: %v", err)
		}
	})
}

// Deleting a session must not be able to erase audit history from its subject.
func TestSessionDeleteCannotEraseAudit(t *testing.T) {
	db, _ := openMigrated(t)
	p := seedProject(t, db, "p1")
	s := seedSession(t, db, "s1", p)
	if _, err := db.Exec(
		`INSERT INTO audit_events (session_id, event_type, created_at)
		 VALUES (?, 'session.launched', ?)`, s, FormatTime(time.Now())); err != nil {
		t.Fatalf("audit insert: %v", err)
	}

	_, err := db.Exec(`DELETE FROM sessions WHERE id=?`, s)
	assertRejected(t, err, "foreign key")
}

// A migration that fails partway must leave neither its schema changes nor its
// ledger row behind, or the next run would skip a migration that never applied.
func TestFailedMigrationRollsBackSchemaAndLedger(t *testing.T) {
	db, _ := openMigrated(t)

	bad := Migration{
		Version:  42,
		Name:     "deliberately_broken",
		SQL:      `CREATE TABLE should_not_survive (x INTEGER); THIS IS NOT SQL;`,
		Checksum: strings.Repeat("a", 64),
	}
	if err := applyOne(context.Background(), db, bad); err == nil {
		t.Fatal("broken migration reported success")
	}

	var tables int
	if err := db.QueryRow(
		`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='should_not_survive'`,
	).Scan(&tables); err != nil {
		t.Fatalf("check table: %v", err)
	}
	if tables != 0 {
		t.Error("a failed migration left its table behind")
	}

	var ledger int
	if err := db.QueryRow(
		`SELECT count(*) FROM schema_migrations WHERE version=42`).Scan(&ledger); err != nil {
		t.Fatalf("check ledger: %v", err)
	}
	if ledger != 0 {
		t.Error("a failed migration was recorded as applied")
	}
}

// A name, PID, or command line alone is insufficient identity, so a
// session cannot be recorded as running until the whole tuple is known.
func TestRunningRequiresFullIdentityTuple(t *testing.T) {
	db, _ := openMigrated(t)
	p := seedProject(t, db, "p1")
	now := FormatTime(time.Now())

	// `starting` legitimately has no pane yet.
	if _, err := db.Exec(
		`INSERT INTO sessions (id, project_id, provider, tmux_socket_path,
		     tmux_session_name, status, created_at, updated_at)
		 VALUES ('s-start', ?, 'claude', '/run/ac.sock', 'noisedeck-start', 'starting', ?, ?)`,
		p, now, now); err != nil {
		t.Fatalf("starting session rejected: %v", err)
	}

	// Promoting it to running without the tuple must fail.
	_, err := db.Exec(`UPDATE sessions SET status='running' WHERE id='s-start'`)
	assertRejected(t, err, "chk_sessions_running_identity")

	// Each field individually is load-bearing: dropping any one of them must
	// prevent the promotion, otherwise the constraint is weaker than the
	// tuple it claims to enforce.
	fields := []string{
		"tmux_session_id", "tmux_pane_id", "identity_marker", "pane_pid",
		"process_group_id", "process_start_ticks", "expected_executable",
	}
	for _, missing := range fields {
		t.Run("missing_"+missing, func(t *testing.T) {
			id := "s-" + missing
			if _, err := db.Exec(
				`INSERT INTO sessions (id, project_id, provider, tmux_socket_path,
				     tmux_session_name, tmux_session_id, tmux_pane_id, identity_marker,
				     pane_pid, process_group_id, process_start_ticks, expected_executable,
				     status, created_at, updated_at)
				 VALUES (?, ?, 'claude', '/run/ac.sock', ?, '$1', '%1', 'm', 1, 1, 1, '/x',
				         'starting', ?, ?)`,
				id, p, "noisedeck-"+id, now, now); err != nil {
				t.Fatalf("seed: %v", err)
			}
			_, err := db.Exec(
				fmt.Sprintf(`UPDATE sessions SET %s=NULL, status='running' WHERE id=?`, missing),
				id)
			assertRejected(t, err, "chk_sessions_running_identity")
		})
	}
}

// A project can be removed from Deck without deleting its files, and
// without erasing the history of what happened in it.
func TestProjectRemovalIsSoftAndFreesThePath(t *testing.T) {
	db, _ := openMigrated(t)
	p := seedProject(t, db, "p1")
	seedSession(t, db, "s1", p)
	now := FormatTime(time.Now())

	// A hard delete is refused while history exists — that is what makes soft
	// removal the only available path, rather than merely the preferred one.
	_, err := db.Exec(`DELETE FROM projects WHERE id=?`, p)
	assertRejected(t, err, "foreign key")

	// Soft removal succeeds.
	if _, err := db.Exec(`UPDATE projects SET removed_at=? WHERE id=?`, now, p); err != nil {
		t.Fatalf("soft removal rejected: %v", err)
	}

	// The session history survives.
	var sessions int
	if err := db.QueryRow(
		`SELECT count(*) FROM sessions WHERE project_id=?`, p).Scan(&sessions); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if sessions != 1 {
		t.Errorf("session history lost on removal: got %d, want 1", sessions)
	}

	// The same directory can be registered again afterwards.
	if _, err := db.Exec(
		`INSERT INTO projects (id, name, canonical_path, created_at, last_used_at)
		 VALUES ('p1-again', 'proj-again', '/workspace/p1', ?, ?)`, now, now); err != nil {
		t.Errorf("re-registering a removed project's path was refused: %v", err)
	}

	// But two ACTIVE projects still cannot share a path.
	_, err = db.Exec(
		`INSERT INTO projects (id, name, canonical_path, created_at, last_used_at)
		 VALUES ('p1-dup', 'dup', '/workspace/p1', ?, ?)`, now, now)
	assertRejected(t, err, "unique constraint failed")
}

func TestMigrateRejectsRenamedMigration(t *testing.T) {
	db, _ := openMigrated(t)

	if _, err := db.Exec(
		`UPDATE schema_migrations SET name = 'renamed_behind_our_back' WHERE version = 1`,
	); err != nil {
		t.Fatalf("rename: %v", err)
	}

	_, err := Migrate(context.Background(), db)
	if err == nil {
		t.Fatal("migrate accepted a renamed migration; want failure")
	}
	if !strings.Contains(err.Error(), "renamed") {
		t.Errorf("unhelpful error for renamed migration: %v", err)
	}
}

// The complement of TestFailedMigrationRollsBackSchemaAndLedger: there the SQL
// failed before the ledger insert, so only one direction was exercised. Here
// the SQL succeeds and the LEDGER insert fails, which is the case that would
// leave a schema change applied but unrecorded — and therefore re-applied on
// the next run.
func TestLedgerFailureRollsBackAppliedSchema(t *testing.T) {
	db, _ := openMigrated(t)

	// Occupy the version so the ledger insert hits a primary key conflict.
	if _, err := db.Exec(
		`INSERT INTO schema_migrations (version, name, checksum, applied_at)
		 VALUES (43, 'squatter', 'x', ?)`, FormatTime(time.Now())); err != nil {
		t.Fatalf("seed ledger row: %v", err)
	}

	valid := Migration{
		Version:  43,
		Name:     "valid_sql_doomed_ledger",
		SQL:      `CREATE TABLE should_not_survive (x INTEGER)`,
		Checksum: strings.Repeat("b", 64),
	}
	if err := applyOne(context.Background(), db, valid); err == nil {
		t.Fatal("migration reported success despite a failed ledger insert")
	}

	var tables int
	if err := db.QueryRow(
		`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='should_not_survive'`,
	).Scan(&tables); err != nil {
		t.Fatalf("check table: %v", err)
	}
	if tables != 0 {
		t.Error("schema change survived a failed ledger insert: it would be applied " +
			"again on the next run, since nothing recorded that it ran")
	}
}

// Audit rows are totally ordered by their autoincrement id, which
// is what lets a reader reconstruct transition order.
func TestAuditEventsAreTotallyOrdered(t *testing.T) {
	db, _ := openMigrated(t)
	p := seedProject(t, db, "p1")
	s := seedSession(t, db, "s1", p)
	now := FormatTime(time.Now())

	for i := 0; i < 5; i++ {
		if _, err := db.Exec(
			`INSERT INTO audit_events (session_id, event_type, details, created_at)
			 VALUES (?, ?, NULL, ?)`, s, fmt.Sprintf("evt-%d", i), now); err != nil {
			t.Fatalf("append audit event: %v", err)
		}
	}

	rows, err := db.Query(`SELECT id FROM audit_events ORDER BY id`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var prev int64
	var count int
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if id <= prev {
			t.Errorf("audit id %d did not increase past %d", id, prev)
		}
		prev = id
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate: %v", err)
	}
	if count != 5 {
		t.Errorf("got %d audit events, want 5", count)
	}
}

// 008 rebuilt the sessions table to widen the provider CHECK. The
// rebuild must preserve what the table guarantees: shell rows fit, the
// foreign keys that survived the drop/rename still bind, and the indexes
// came back.
func TestMigrate008ShellProviderAndIntactReferences(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.db")
	db := open(t, path, 5000)
	defer func() { _ = db.Close() }()
	ctx := context.Background()

	if _, err := Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	p := seedProject(t, db, "p1")

	// A shell session row fits the widened CHECK — with NULL native identity,
	// which is the identity-none contract.
	now := FormatTime(time.Now())
	if _, err := db.Exec(
		`INSERT INTO sessions (id, project_id, provider, tmux_socket_path,
		     tmux_session_name, status, created_at, updated_at)
		 VALUES ('s-shell', ?, 'shell', '/run/ac.sock', 'noisedeck-s-shell', 'starting', ?, ?)`,
		p, now, now); err != nil {
		t.Fatalf("shell provider row rejected: %v", err)
	}
	// A made-up provider still fails the CHECK.
	if _, err := db.Exec(
		`INSERT INTO sessions (id, project_id, provider, tmux_socket_path,
		     tmux_session_name, status, created_at, updated_at)
		 VALUES ('s-bogus', ?, 'zsh-plus', '/run/ac.sock', 'noisedeck-s-bogus', 'starting', ?, ?)`,
		p, now, now); err == nil {
		t.Fatal("unknown provider accepted; the CHECK must still bind")
	}

	// The rebuilt table still anchors the FKs: a lease on a real session is
	// fine, a lease on a phantom is not — foreign_keys survived the rebuild.
	if _, err := db.Exec(
		`INSERT INTO controller_leases (session_id, controller_id, last_heartbeat, expires_at)
		 VALUES ('s-shell', 'c1', ?, ?)`, now, now); err != nil {
		t.Fatalf("lease on a real session rejected: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO controller_leases (session_id, controller_id, last_heartbeat, expires_at)
		 VALUES ('no-such-session', 'c1', ?, ?)`, now, now); err == nil {
		t.Fatal("lease on a phantom session accepted; foreign_keys is off after the rebuild")
	}

	for _, idx := range []string{"idx_sessions_project", "idx_sessions_status"} {
		var n int
		if err := db.QueryRow(
			`SELECT count(*) FROM sqlite_master WHERE type='index' AND name=?`, idx,
		).Scan(&n); err != nil || n != 1 {
			t.Fatalf("index %s missing after the rebuild (n=%d, err=%v)", idx, n, err)
		}
	}
}

// The fk-off marker on migration 008 is load-bearing: with rows present,
// dropping the sessions table violates the leases/audit FKs unless the
// runner toggles foreign_keys around the transaction. Deleting the marker
// must turn this test red — an empty-table migrate would not.
func TestMigration008RequiresForeignKeysOffWithRowsPresent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.db")
	db := open(t, path, 5000)
	ctx := context.Background()

	migrations, err := LoadMigrations()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, err := db.ExecContext(ctx, createSchemaMigrations); err != nil {
		t.Fatalf("create schema_migrations: %v", err)
	}
	for _, m := range migrations {
		if m.Version > 7 {
			break
		}
		if err := applyOne(ctx, db, m); err != nil {
			t.Fatalf("apply migration %d: %v", m.Version, err)
		}
	}

	// A live pre-008 database: a session with a lease and an audit row, so
	// the rebuild's DROP TABLE has referencing children.
	p := seedProject(t, db, "p1")
	s := seedSession(t, db, "s1", p)
	now := FormatTime(time.Now())
	if _, err := db.Exec(
		`INSERT INTO controller_leases (session_id, controller_id, last_heartbeat, expires_at)
		 VALUES (?, 'c1', ?, ?)`, s, now, now); err != nil {
		t.Fatalf("seed lease: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO audit_events (session_id, event_type, details, created_at)
		 VALUES (?, 'session.created', 'pre-rebuild', ?)`, s, now); err != nil {
		t.Fatalf("seed audit: %v", err)
	}

	for _, m := range migrations {
		if m.Version != 8 {
			continue
		}
		if err := applyOne(ctx, db, m); err != nil {
			t.Fatalf("apply migration 8 with rows present: %v — the fk-off path is broken", err)
		}
	}

	// Rows survived the rebuild and the FK posture is back on.
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM sessions WHERE id = ?`, s).Scan(&n); err != nil || n != 1 {
		t.Fatalf("session lost in rebuild (n=%d, err=%v)", n, err)
	}
	var fk int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign_keys = %d after migration 8, want 1 (restored)", fk)
	}
}

// 009 widened the provider CHECK again for opencode. The rebuild
// pattern is proven by the 008 tests; this pins only what 009 changes.
func TestMigrate009OpencodeProvider(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.db")
	db := open(t, path, 5000)
	defer func() { _ = db.Close() }()
	ctx := context.Background()

	if _, err := Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	p := seedProject(t, db, "p1")
	now := FormatTime(time.Now())
	if _, err := db.Exec(
		`INSERT INTO sessions (id, project_id, provider, tmux_socket_path,
		     tmux_session_name, status, created_at, updated_at)
		 VALUES ('s-oc', ?, 'opencode', '/run/ac.sock', 'noisedeck-s-oc', 'starting', ?, ?)`,
		p, now, now); err != nil {
		t.Fatalf("opencode provider row rejected: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO sessions (id, project_id, provider, tmux_socket_path,
		     tmux_session_name, status, created_at, updated_at)
		 VALUES ('s-bogus', ?, 'unknown', '/run/ac.sock', 'noisedeck-s-bogus', 'starting', ?, ?)`,
		p, now, now); err == nil {
		t.Fatal("unknown provider accepted; the CHECK must still bind")
	}
}

// 014 widens the provider CHECK for Pi while retaining every session field
// added after the previous table rebuild.
func TestMigrate014PiProvider(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.db")
	db := open(t, path, 5000)
	ctx := context.Background()
	migrations, err := LoadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, createSchemaMigrations); err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations {
		if migration.Version >= 14 {
			break
		}
		if err := applyOne(ctx, db, migration); err != nil {
			t.Fatalf("apply migration %d: %v", migration.Version, err)
		}
	}
	sessionObjects := func() string {
		rows, err := db.Query(`SELECT type, name, sql FROM sqlite_master
			WHERE tbl_name = 'sessions' AND type IN ('index', 'trigger')
			ORDER BY type, name`)
		if err != nil {
			t.Fatalf("snapshot session objects: %v", err)
		}
		defer func() { _ = rows.Close() }()
		var out strings.Builder
		for rows.Next() {
			var kind, name string
			var ddl sql.NullString
			if err := rows.Scan(&kind, &name, &ddl); err != nil {
				t.Fatalf("scan session object: %v", err)
			}
			fmt.Fprintf(&out, "%s\t%s\t%s\n", kind, name, ddl.String)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	foreignKeys := func() string {
		rows, err := db.Query(`PRAGMA foreign_key_list(sessions)`)
		if err != nil {
			t.Fatalf("snapshot session foreign keys: %v", err)
		}
		defer func() { _ = rows.Close() }()
		var out strings.Builder
		for rows.Next() {
			var id, seq int
			var table, from, to, onUpdate, onDelete, match string
			if err := rows.Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
				t.Fatalf("scan session foreign key: %v", err)
			}
			fmt.Fprintf(&out, "%d\t%d\t%s\t%s\t%s\t%s\t%s\t%s\n",
				id, seq, table, from, to, onUpdate, onDelete, match)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	objectsBefore, foreignKeysBefore := sessionObjects(), foreignKeys()
	p := seedProject(t, db, "p14")
	now := FormatTime(time.Now())
	if _, err := db.Exec(`INSERT INTO sessions (
		id, project_id, provider, tmux_socket_path, tmux_session_name, status,
		created_at, updated_at, resume_attempts, resume_next_at,
		resume_series_started_at, stop_requested_at, last_output_at,
		identity_capture_lost_at
	) VALUES ('s-before-14', ?, 'claude', '/run/ac.sock', 'noisedeck-before',
		'interrupted', ?, ?, 2, ?, ?, ?, ?, ?)`,
		p, now, now, now, now, now, now, now); err != nil {
		t.Fatalf("seed 013 session: %v", err)
	}
	for _, migration := range migrations {
		if migration.Version == 14 {
			if err := applyOne(ctx, db, migration); err != nil {
				t.Fatalf("apply migration 14: %v", err)
			}
		}
	}
	if got := sessionObjects(); got != objectsBefore {
		t.Fatalf("session indexes/triggers changed:\nbefore:\n%s\nafter:\n%s", objectsBefore, got)
	}
	if got := foreignKeys(); got != foreignKeysBefore {
		t.Fatalf("session foreign keys changed:\nbefore:\n%s\nafter:\n%s", foreignKeysBefore, got)
	}
	var foreignKeysEnabled int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeysEnabled); err != nil || foreignKeysEnabled != 1 {
		t.Fatalf("foreign_keys = %d after migration 14, want 1 (err=%v)", foreignKeysEnabled, err)
	}

	var attempts int
	var next, series, stop, output, capture string
	if err := db.QueryRow(`SELECT resume_attempts, resume_next_at,
		resume_series_started_at, stop_requested_at, last_output_at,
		identity_capture_lost_at FROM sessions WHERE id = 's-before-14'`).Scan(
		&attempts, &next, &series, &stop, &output, &capture); err != nil {
		t.Fatalf("read preserved session: %v", err)
	}
	if attempts != 2 || next != now || series != now || stop != now || output != now || capture != now {
		t.Fatalf("post-009 fields changed: attempts=%d next=%q series=%q stop=%q output=%q capture=%q",
			attempts, next, series, stop, output, capture)
	}
	if _, err := db.Exec(`INSERT INTO sessions (
		id, project_id, provider, tmux_socket_path, tmux_session_name, status,
		created_at, updated_at
	) VALUES ('s-pi', ?, 'pi', '/run/ac.sock', 'noisedeck-pi', 'starting', ?, ?)`,
		p, now, now); err != nil {
		t.Fatalf("pi provider row rejected: %v", err)
	}
	_, err = db.Exec(`INSERT INTO sessions (
		id, project_id, provider, tmux_socket_path, tmux_session_name, status,
		created_at, updated_at
	) VALUES ('s-bogus-14', ?, 'unknown', '/run/ac.sock', 'noisedeck-bogus', 'starting', ?, ?)`,
		p, now, now)
	assertRejected(t, err, "chk_sessions_provider")
}

func TestMigrate015AddsResumeProbationWithoutResettingSeries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.db")
	database := open(t, path, 5000)
	ctx := context.Background()
	migrations, err := LoadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, createSchemaMigrations); err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations {
		if migration.Version >= 15 {
			break
		}
		if err := applyOne(ctx, database, migration); err != nil {
			t.Fatalf("apply migration %d: %v", migration.Version, err)
		}
	}
	projectID := seedProject(t, database, "p15")
	now := FormatTime(time.Now())
	if _, err := database.Exec(`INSERT INTO sessions (
		id, project_id, provider, tmux_socket_path, tmux_session_name, status,
		created_at, updated_at, resume_attempts, resume_next_at,
		resume_series_started_at
	) VALUES ('s-before-15', ?, 'claude', '/run/ac.sock', 'noisedeck-before',
		'interrupted', ?, ?, 4, ?, ?)`, projectID, now, now, now, now); err != nil {
		t.Fatalf("seed 014 session: %v", err)
	}
	for _, migration := range migrations {
		if migration.Version == 15 {
			if err := applyOne(ctx, database, migration); err != nil {
				t.Fatalf("apply migration 15: %v", err)
			}
		}
	}
	var attempts int
	var next, series string
	var probation sql.NullString
	if err := database.QueryRow(`SELECT resume_attempts, resume_next_at,
		resume_series_started_at, resume_probation_until
		FROM sessions WHERE id = 's-before-15'`).Scan(&attempts, &next, &series, &probation); err != nil {
		t.Fatalf("read migrated session: %v", err)
	}
	if attempts != 4 || next != now || series != now || probation.Valid {
		t.Fatalf("migration changed retry state: attempts=%d next=%q series=%q probation=%v",
			attempts, next, series, probation)
	}
	deadline := FormatTime(time.Now().Add(30 * time.Second))
	if _, err := database.Exec(`UPDATE sessions SET resume_probation_until = ? WHERE id = 's-before-15'`, deadline); err != nil {
		t.Fatalf("write probation deadline: %v", err)
	}
	if err := database.QueryRow(`SELECT resume_probation_until FROM sessions WHERE id = 's-before-15'`).Scan(&probation); err != nil {
		t.Fatalf("read probation deadline: %v", err)
	}
	if !probation.Valid || probation.String != deadline {
		t.Fatalf("probation = %v, want %q", probation, deadline)
	}
}

// 016 widens the provider CHECK for omp while retaining every session
// field, including 015's resume_probation_until. Like 014 it rebuilds the
// table (SQLite cannot alter a CHECK in place), so the test also pins that
// indexes, triggers, and the foreign key survive the rebuild unchanged.
func TestMigrate016OmpProvider(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.db")
	db := open(t, path, 5000)
	ctx := context.Background()
	migrations, err := LoadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, createSchemaMigrations); err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations {
		if migration.Version >= 16 {
			break
		}
		if err := applyOne(ctx, db, migration); err != nil {
			t.Fatalf("apply migration %d: %v", migration.Version, err)
		}
	}
	sessionObjects := func() string {
		rows, err := db.Query(`SELECT type, name, sql FROM sqlite_master
			WHERE tbl_name = 'sessions' AND type IN ('index', 'trigger')
			ORDER BY type, name`)
		if err != nil {
			t.Fatalf("snapshot session objects: %v", err)
		}
		defer func() { _ = rows.Close() }()
		var out strings.Builder
		for rows.Next() {
			var kind, name string
			var ddl sql.NullString
			if err := rows.Scan(&kind, &name, &ddl); err != nil {
				t.Fatalf("scan session object: %v", err)
			}
			fmt.Fprintf(&out, "%s\t%s\t%s\n", kind, name, ddl.String)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	foreignKeys := func() string {
		rows, err := db.Query(`PRAGMA foreign_key_list(sessions)`)
		if err != nil {
			t.Fatalf("snapshot session foreign keys: %v", err)
		}
		defer func() { _ = rows.Close() }()
		var out strings.Builder
		for rows.Next() {
			var id, seq int
			var table, from, to, onUpdate, onDelete, match string
			if err := rows.Scan(&id, &seq, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
				t.Fatalf("scan session foreign key: %v", err)
			}
			fmt.Fprintf(&out, "%d\t%d\t%s\t%s\t%s\t%s\t%s\t%s\n",
				id, seq, table, from, to, onUpdate, onDelete, match)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	objectsBefore, foreignKeysBefore := sessionObjects(), foreignKeys()
	p := seedProject(t, db, "p16")
	now := FormatTime(time.Now())
	deadline := FormatTime(time.Now().Add(30 * time.Second))
	if _, err := db.Exec(`INSERT INTO sessions (
		id, project_id, provider, tmux_socket_path, tmux_session_name, status,
		created_at, updated_at, resume_attempts, resume_next_at,
		resume_series_started_at, resume_probation_until
	) VALUES ('s-before-16', ?, 'claude', '/run/ac.sock', 'noisedeck-before',
		'interrupted', ?, ?, 3, ?, ?, ?)`,
		p, now, now, now, now, deadline); err != nil {
		t.Fatalf("seed 015 session: %v", err)
	}
	for _, migration := range migrations {
		if migration.Version == 16 {
			if err := applyOne(ctx, db, migration); err != nil {
				t.Fatalf("apply migration 16: %v", err)
			}
		}
	}
	if got := sessionObjects(); got != objectsBefore {
		t.Fatalf("session indexes/triggers changed:\nbefore:\n%s\nafter:\n%s", objectsBefore, got)
	}
	if got := foreignKeys(); got != foreignKeysBefore {
		t.Fatalf("session foreign keys changed:\nbefore:\n%s\nafter:\n%s", foreignKeysBefore, got)
	}
	var foreignKeysEnabled int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeysEnabled); err != nil || foreignKeysEnabled != 1 {
		t.Fatalf("foreign_keys = %d after migration 16, want 1 (err=%v)", foreignKeysEnabled, err)
	}

	var attempts int
	var next, series, probation string
	if err := db.QueryRow(`SELECT resume_attempts, resume_next_at,
		resume_series_started_at, resume_probation_until
		FROM sessions WHERE id = 's-before-16'`).Scan(
		&attempts, &next, &series, &probation); err != nil {
		t.Fatalf("read preserved session: %v", err)
	}
	if attempts != 3 || next != now || series != now || probation != deadline {
		t.Fatalf("post-015 fields changed: attempts=%d next=%q series=%q probation=%q",
			attempts, next, series, probation)
	}
	if _, err := db.Exec(`INSERT INTO sessions (
		id, project_id, provider, tmux_socket_path, tmux_session_name, status,
		created_at, updated_at
	) VALUES ('s-omp', ?, 'omp', '/run/ac.sock', 'noisedeck-omp', 'starting', ?, ?)`,
		p, now, now); err != nil {
		t.Fatalf("omp provider row rejected: %v", err)
	}
	_, err = db.Exec(`INSERT INTO sessions (
		id, project_id, provider, tmux_socket_path, tmux_session_name, status,
		created_at, updated_at
	) VALUES ('s-bogus-16', ?, 'unknown', '/run/ac.sock', 'noisedeck-bogus', 'starting', ?, ?)`,
		p, now, now)
	assertRejected(t, err, "chk_sessions_provider")
}

func TestMigrate022ClineProvider(t *testing.T) {
	db, _ := openMigrated(t)
	projectID := seedProject(t, db, "cline-project")
	now := FormatTime(time.Now())
	_, err := db.Exec(`INSERT INTO sessions
		(id, project_id, provider, tmux_socket_path, tmux_session_name,
		 status, created_at, updated_at)
		VALUES ('cline-session', ?, 'cline', '/tmp/tmux.sock', 'cline-pane',
		        'starting', ?, ?)`, projectID, now, now)
	if err != nil {
		t.Fatalf("insert Cline session after migration 022: %v", err)
	}
}

func TestMigrate023KiloProvider(t *testing.T) {
	db, _ := openMigrated(t)
	projectID := seedProject(t, db, "kilo-project")
	now := FormatTime(time.Now())
	_, err := db.Exec(`INSERT INTO sessions
		(id, project_id, provider, tmux_socket_path, tmux_session_name,
		 status, created_at, updated_at)
		VALUES ('kilo-session', ?, 'kilo', '/tmp/tmux.sock', 'kilo-pane',
		        'starting', ?, ?)`, projectID, now, now)
	if err != nil {
		t.Fatalf("insert Kilo session after migration 023: %v", err)
	}
	_, err = db.Exec(`INSERT INTO sessions
		(id, project_id, provider, tmux_socket_path, tmux_session_name,
		 status, created_at, updated_at)
		VALUES ('bogus-session-23', ?, 'unknown', '/tmp/tmux.sock', 'bogus-pane',
		        'starting', ?, ?)`, projectID, now, now)
	assertRejected(t, err, "chk_sessions_provider")
}

// 017 adds the boot epoch as a plain nullable column: rows that
// predate it carry no epoch claim (NULL, never a backfilled boot id), a
// recorded epoch round-trips, and the running-identity CHECK keeps accepting
// a complete pre-017 tuple with no epoch. Legacy rows remain representable.
func TestMigrate017BootEpoch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.db")
	db := open(t, path, 5000)
	ctx := context.Background()

	migrations, err := LoadMigrations()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, err := db.ExecContext(ctx, createSchemaMigrations); err != nil {
		t.Fatalf("create schema_migrations: %v", err)
	}
	for _, migration := range migrations {
		if migration.Version >= 17 {
			break
		}
		if err := applyOne(ctx, db, migration); err != nil {
			t.Fatalf("apply migration %d: %v", migration.Version, err)
		}
	}

	// A live pre-017 row: running with the full pre-epoch identity tuple.
	p := seedProject(t, db, "p17")
	s := seedSession(t, db, "s-before-17", p)

	for _, migration := range migrations {
		if migration.Version == 17 {
			if err := applyOne(ctx, db, migration); err != nil {
				t.Fatalf("apply migration 17: %v", err)
			}
		}
	}

	var columns int
	if err := db.QueryRow(
		`SELECT count(*) FROM pragma_table_info('sessions') WHERE name = 'boot_epoch'`,
	).Scan(&columns); err != nil {
		t.Fatalf("query sessions columns: %v", err)
	}
	if columns != 1 {
		t.Fatalf("boot_epoch column count = %d, want 1", columns)
	}

	// The pre-existing row was not backfilled: no epoch claim it never made.
	var epoch *string
	if err := db.QueryRow(`SELECT boot_epoch FROM sessions WHERE id = ?`, s).Scan(&epoch); err != nil {
		t.Fatalf("read pre-017 row: %v", err)
	}
	if epoch != nil {
		t.Fatalf("pre-017 row carries boot epoch %q; NULL is the only honest value", *epoch)
	}

	// A recorded epoch round-trips.
	const recorded = "5f0f9b3a-0000-4000-8000-000000000000"
	if _, err := db.Exec(`UPDATE sessions SET boot_epoch = ? WHERE id = ?`, recorded, s); err != nil {
		t.Fatalf("record boot epoch: %v", err)
	}
	if err := db.QueryRow(`SELECT boot_epoch FROM sessions WHERE id = ?`, s).Scan(&epoch); err != nil {
		t.Fatalf("re-read boot epoch: %v", err)
	}
	if epoch == nil || *epoch != recorded {
		t.Fatalf("boot_epoch = %v, want %q", epoch, recorded)
	}

	// The epoch is not part of the running-identity CHECK: a complete
	// pre-017 tuple remains representable with NULL so migration does not
	// rewrite history. Runtime identity verification parks that row.
	now := FormatTime(time.Now())
	if _, err := db.Exec(
		`INSERT INTO sessions (id, project_id, provider, tmux_socket_path,
		     tmux_session_name, tmux_session_id, tmux_pane_id, identity_marker,
		     pane_pid, process_group_id, process_start_ticks, expected_executable,
		     status, created_at, updated_at)
		 VALUES ('s-noepoch', ?, 'claude', '/run/ac.sock', 'noisedeck-noepoch',
		         '$1', '%1', 'm', 1, 1, 1, '/x', 'running', ?, ?)`,
		p, now, now); err != nil {
		t.Fatalf("running row without a boot epoch rejected: %v", err)
	}
}

func TestMigrate021AllowsCommandCodeAndPreservesCurrentColumns(t *testing.T) {
	db, _ := openMigrated(t)
	p := seedProject(t, db, "p21")
	now := FormatTime(time.Now())
	_, err := db.Exec(`INSERT INTO sessions (
		id, project_id, provider, tmux_socket_path, tmux_session_name,
		status, created_at, updated_at, boot_epoch, color
	) VALUES ('s-command-code', ?, 'command-code', '/run/ac.sock',
		'noisedeck-command-code', 'starting', ?, ?, 'boot-1', 'violet')`, p, now, now)
	if err != nil {
		t.Fatalf("command-code provider row rejected: %v", err)
	}
	var epoch, color string
	if err := db.QueryRow(`SELECT boot_epoch, color FROM sessions WHERE id = 's-command-code'`).Scan(&epoch, &color); err != nil {
		t.Fatalf("read migrated columns: %v", err)
	}
	if epoch != "boot-1" || color != "violet" {
		t.Fatalf("current columns = %q/%q", epoch, color)
	}
	_, err = db.Exec(`INSERT INTO sessions (
		id, project_id, provider, tmux_socket_path, tmux_session_name,
		status, created_at, updated_at
	) VALUES ('s-unknown-21', ?, 'unknown', '/run/ac.sock', 'noisedeck-unknown', 'starting', ?, ?)`, p, now, now)
	assertRejected(t, err, "chk_sessions_provider")
}

func TestMigrate027AllowsDevinAndPreservesControllerFence(t *testing.T) {
	database, _ := openMigrated(t)
	projectID := seedProject(t, database, "devin-project")
	now := FormatTime(time.Now())
	_, err := database.Exec(`INSERT INTO sessions (
		id, project_id, provider, tmux_socket_path, tmux_session_name,
		status, created_at, updated_at, controller_revision
	) VALUES ('devin-session', ?, 'devin', '/run/ac.sock',
		'noisedeck-devin', 'starting', ?, ?, 7)`, projectID, now, now)
	if err != nil {
		t.Fatalf("devin provider row rejected: %v", err)
	}
	var revision int
	if err := database.QueryRow(`SELECT controller_revision FROM sessions
		WHERE id = 'devin-session'`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if revision != 7 {
		t.Fatalf("controller revision = %d, want 7", revision)
	}
	if _, err := database.Exec(`UPDATE sessions SET status = 'interrupted'
		WHERE id = 'devin-session'`); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT controller_revision FROM sessions
		WHERE id = 'devin-session'`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if revision != 8 {
		t.Fatalf("controller trigger revision = %d, want 8", revision)
	}
	_, err = database.Exec(`INSERT INTO sessions (
		id, project_id, provider, tmux_socket_path, tmux_session_name,
		status, created_at, updated_at
	) VALUES ('unknown-session-27', ?, 'unknown', '/run/ac.sock',
		'noisedeck-unknown', 'starting', ?, ?)`, projectID, now, now)
	assertRejected(t, err, "chk_sessions_provider")
}
