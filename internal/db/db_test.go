package db

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()
	d, err := Open(context.Background(), Config{
		Path: filepath.Join(t.TempDir(), "control.db"),
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func TestOpenAppliesAndVerifiesPragmas(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	// The writer carries the durability settings.
	writeChecks := map[string]string{
		"journal_mode":       "wal",
		"synchronous":        "2",
		"foreign_keys":       "1",
		"recursive_triggers": "1",
		"busy_timeout":       "5000",
	}
	for pragma, want := range writeChecks {
		var got string
		if err := d.write.QueryRowContext(ctx, "PRAGMA "+pragma).Scan(&got); err != nil {
			t.Fatalf("read %s: %v", pragma, err)
		}
		if !strings.EqualFold(got, want) {
			t.Errorf("write pool %s = %q, want %q", pragma, got, want)
		}
	}

	// The reader sees the same journal mode, enforces foreign keys, and has
	// recursive triggers on so the policy-authority trigger chain fires fully.
	for pragma, want := range map[string]string{
		"journal_mode":       "wal",
		"foreign_keys":       "1",
		"recursive_triggers": "1",
	} {
		var got string
		if err := d.read.QueryRowContext(ctx, "PRAGMA "+pragma).Scan(&got); err != nil {
			t.Fatalf("read %s on reader: %v", pragma, err)
		}
		if !strings.EqualFold(got, want) {
			t.Errorf("read pool %s = %q, want %q", pragma, got, want)
		}
	}
}

func TestOpenRunsMigrations(t *testing.T) {
	d := openTestDB(t)

	var n int
	if err := d.Reader().QueryRow(
		`SELECT count(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatalf("query schema_migrations: %v", err)
	}
	if n == 0 {
		t.Error("Open did not apply any migrations")
	}
}

func TestOpenCreatesParentDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "deeper", "control.db")
	d, err := Open(context.Background(), Config{Path: path})
	if err != nil {
		t.Fatalf("open into a missing directory: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	if _, err := os.Stat(path); err != nil {
		t.Errorf("database file not created: %v", err)
	}
}

func TestOpenRejectsEmptyPath(t *testing.T) {
	if _, err := Open(context.Background(), Config{Path: "   "}); err == nil {
		t.Fatal("Open accepted a blank path")
	}
}

// The database holds control state and audit records, so it and its WAL
// sidecars must be owner-only. The -wal file matters as much as the main
// file: recently committed rows live there.
func TestOpenEnforcesOwnerOnlyPermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "control.db")

	// Pre-create the file world-readable so the check has something to fix.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("pre-create: %v", err)
	}
	_ = f.Close()

	d, err := Open(context.Background(), Config{Path: path})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	// Write something so the WAL sidecar exists.
	if err := d.WithTx(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO projects (id, name, canonical_path, created_at, last_used_at)
			 VALUES ('p','p','/workspace/p',?,?)`, FormatTime(time.Now()), FormatTime(time.Now()))
		return err
	}); err != nil {
		t.Fatalf("seed write: %v", err)
	}

	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		info, err := os.Stat(p)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		if perm := info.Mode().Perm(); perm != dbFileMode {
			t.Errorf("%s has mode %04o, want %04o", filepath.Base(p), perm, dbFileMode)
		}
	}
}

func TestWithTxCommitsOnSuccess(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	now := FormatTime(time.Now())

	if err := d.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO projects (id, name, canonical_path, created_at, last_used_at)
			 VALUES ('p1','one','/workspace/one',?,?)`, now, now)
		return err
	}); err != nil {
		t.Fatalf("WithTx: %v", err)
	}

	var n int
	if err := d.Reader().QueryRow(
		`SELECT count(*) FROM projects WHERE id='p1'`).Scan(&n); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if n != 1 {
		t.Errorf("committed row not visible: got %d, want 1", n)
	}
}

func TestWithTxRollsBackOnError(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	now := FormatTime(time.Now())
	sentinel := errors.New("deliberate failure")

	err := d.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO projects (id, name, canonical_path, created_at, last_used_at)
			 VALUES ('p1','one','/workspace/one',?,?)`, now, now); err != nil {
			return err
		}
		// The write above succeeded; the transaction still must not commit.
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("WithTx returned %v, want the callback's error", err)
	}

	var n int
	if err := d.Reader().QueryRow(`SELECT count(*) FROM projects`).Scan(&n); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if n != 0 {
		t.Errorf("rolled-back row is visible: got %d rows, want 0", n)
	}
}

// A failing statement inside the transaction must not leave earlier statements
// committed either.
func TestWithTxRollsBackPartialWork(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	now := FormatTime(time.Now())

	err := d.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO projects (id, name, canonical_path, created_at, last_used_at)
			 VALUES ('good','g','/workspace/g',?,?)`, now, now); err != nil {
			return err
		}
		// Violates the provider CHECK.
		_, err := tx.ExecContext(ctx,
			`INSERT INTO sessions (id, project_id, provider, tmux_socket_path,
			     tmux_session_name, status, created_at, updated_at)
			 VALUES ('s','good','not-a-provider','/run/s','noisedeck-s','starting',?,?)`, now, now)
		return err
	})
	if err == nil {
		t.Fatal("WithTx succeeded despite a constraint violation")
	}

	var n int
	if err := d.Reader().QueryRow(`SELECT count(*) FROM projects`).Scan(&n); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if n != 0 {
		t.Errorf("earlier statement survived the failed transaction: %d rows", n)
	}
}

func TestReaderCannotWrite(t *testing.T) {
	d := openTestDB(t)
	now := FormatTime(time.Now())

	_, err := d.Reader().Exec(
		`INSERT INTO projects (id, name, canonical_path, created_at, last_used_at)
		 VALUES ('x','x','/workspace/x',?,?)`, now, now)
	if err == nil {
		t.Fatal("the read pool accepted a write; it must be read-only")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "readonly") {
		t.Errorf("expected a readonly error, got: %v", err)
	}
}

// The point of splitting the pools: a read in flight must not delay a write,
// and concurrent writes must queue rather than contend for the SQLite lock.
func TestConcurrentWritesSerializeWithoutBusyErrors(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	const writers = 8
	var wg sync.WaitGroup
	errs := make([]error, writers)

	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			now := FormatTime(time.Now())
			errs[i] = d.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
				id := "p" + string(rune('a'+i))
				_, err := tx.ExecContext(ctx,
					`INSERT INTO projects (id, name, canonical_path, created_at, last_used_at)
					 VALUES (?,?,?,?,?)`, id, id, "/workspace/"+id, now, now)
				return err
			})
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("writer %d failed: %v", i, err)
		}
	}

	var n int
	if err := d.Reader().QueryRow(`SELECT count(*) FROM projects`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != writers {
		t.Errorf("got %d projects, want %d", n, writers)
	}
}

func TestReadsProceedDuringAWrite(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	now := FormatTime(time.Now())

	if err := d.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO projects (id, name, canonical_path, created_at, last_used_at)
			 VALUES ('seed','seed','/workspace/seed',?,?)`, now, now)
		return err
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Hold a write transaction open, then read from the other pool.
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- d.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO projects (id, name, canonical_path, created_at, last_used_at)
				 VALUES ('held','held','/workspace/held',?,?)`, now, now); err != nil {
				return err
			}
			close(started)
			<-release
			return nil
		})
	}()

	<-started
	readDone := make(chan error, 1)
	go func() {
		var n int
		readDone <- d.Reader().QueryRow(`SELECT count(*) FROM projects`).Scan(&n)
	}()

	select {
	case err := <-readDone:
		if err != nil {
			t.Errorf("read during an open write failed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Error("read blocked behind an open write transaction; WAL should permit it")
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("held write: %v", err)
	}
}

func TestVerifyWritable(t *testing.T) {
	d := openTestDB(t)
	if err := d.VerifyWritable(context.Background()); err != nil {
		t.Errorf("VerifyWritable on a healthy database: %v", err)
	}
}

// The probe must perform a real, committed mutation on EVERY call. A probe
// that is a no-op after the first invocation would report a healthy database
// while writes were failing — the precise failure it exists to detect.
func TestVerifyWritableMutatesEveryTime(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	read := func() string {
		t.Helper()
		var v string
		if err := d.Reader().QueryRow(
			`SELECT checked_at FROM writability_probe WHERE id = 1`).Scan(&v); err != nil {
			t.Fatalf("read probe: %v", err)
		}
		return v
	}

	seed := read()
	var prev = seed
	for i := 0; i < 3; i++ {
		time.Sleep(2 * time.Millisecond) // the format has millisecond precision
		if err := d.VerifyWritable(ctx); err != nil {
			t.Fatalf("probe %d: %v", i, err)
		}
		got := read()
		if got == prev {
			t.Fatalf("probe %d did not change checked_at (%q): it performed no write",
				i, got)
		}
		prev = got
	}
	if prev == seed {
		t.Error("checked_at never moved from its seeded value")
	}
}

// The probe row is pinned to exactly one, so repeated probing cannot grow the
// database over uptime.
func TestWritabilityProbeCannotGrow(t *testing.T) {
	d := openTestDB(t)

	err := d.WithTx(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO writability_probe (id, checked_at) VALUES (2, ?)`,
			FormatTime(time.Now()))
		return err
	})
	if err == nil {
		t.Fatal("a second probe row was accepted; the table can grow")
	}
	if !strings.Contains(err.Error(), "chk_probe_single_row") {
		t.Errorf("rejected for the wrong reason: %v", err)
	}
}

// With a single-connection write pool, a panicking callback that some outer
// layer recovers must not strand that connection on an open transaction —
// otherwise every later write blocks until its context expires.
func TestWithTxSurvivesAPanickingCallback(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Error("expected the panic to propagate to the caller")
			}
		}()
		_ = d.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
			now := FormatTime(time.Now())
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO projects (id, name, canonical_path, created_at, last_used_at)
				 VALUES ('doomed','d','/workspace/doomed',?,?)`, now, now); err != nil {
				t.Errorf("setup write: %v", err)
			}
			panic("callback exploded")
		})
	}()

	// The write pool must still be usable. Bound it so a stranded connection
	// fails the test instead of hanging it.
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	now := FormatTime(time.Now())
	if err := d.WithTx(bounded, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO projects (id, name, canonical_path, created_at, last_used_at)
			 VALUES ('after','a','/workspace/after',?,?)`, now, now)
		return err
	}); err != nil {
		t.Fatalf("write after a panicking transaction: %v "+
			"(the sole writer connection was likely stranded)", err)
	}

	// And the panicking transaction must have rolled back.
	var n int
	if err := d.Reader().QueryRow(
		`SELECT count(*) FROM projects WHERE id='doomed'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("the panicking transaction committed %d rows; want 0", n)
	}
}

func TestVerifyWritableFailsWhenClosed(t *testing.T) {
	d := openTestDB(t)
	if err := d.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := d.VerifyWritable(context.Background()); err == nil {
		t.Error("VerifyWritable succeeded on a closed database")
	}
}

func TestAppendAuditRequiresType(t *testing.T) {
	d := openTestDB(t)
	err := d.WithTx(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		return AppendAudit(ctx, tx, AuditEvent{Type: "  "})
	})
	if err == nil {
		t.Fatal("AppendAudit accepted a blank event type")
	}
}

// The audit event and the change it describes share a transaction, so a
// rolled-back change cannot leave its audit row behind claiming it happened.
func TestAuditEventRollsBackWithItsChange(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	now := FormatTime(time.Now())
	sentinel := errors.New("abort after audit")

	err := d.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO projects (id, name, canonical_path, created_at, last_used_at)
			 VALUES ('p1','one','/workspace/one',?,?)`, now, now); err != nil {
			return err
		}
		if err := AppendAudit(ctx, tx, AuditEvent{Type: "project.registered"}); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("WithTx returned %v, want the sentinel", err)
	}

	var audits, projects int
	if err := d.Reader().QueryRow(`SELECT count(*) FROM audit_events`).Scan(&audits); err != nil {
		t.Fatalf("count audits: %v", err)
	}
	if err := d.Reader().QueryRow(`SELECT count(*) FROM projects`).Scan(&projects); err != nil {
		t.Fatalf("count projects: %v", err)
	}
	if audits != 0 || projects != 0 {
		t.Errorf("rollback left %d audit events and %d projects; want 0 and 0",
			audits, projects)
	}
}

func TestAppendAuditCommitsWithItsChange(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	now := FormatTime(time.Now())

	if err := d.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO projects (id, name, canonical_path, created_at, last_used_at)
			 VALUES ('p1','one','/workspace/one',?,?)`, now, now); err != nil {
			return err
		}
		return AppendAudit(ctx, tx, AuditEvent{
			Type:    "project.registered",
			Details: `{"project_id":"p1"}`,
		})
	}); err != nil {
		t.Fatalf("WithTx: %v", err)
	}

	var eventType, details string
	if err := d.Reader().QueryRow(
		`SELECT event_type, details FROM audit_events ORDER BY id LIMIT 1`,
	).Scan(&eventType, &details); err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if eventType != "project.registered" {
		t.Errorf("event_type = %q", eventType)
	}
	if details != `{"project_id":"p1"}` {
		t.Errorf("details = %q", details)
	}
}

func TestOpenFailsOnUnwritableDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; permission checks do not apply")
	}
	dir := t.TempDir()
	locked := filepath.Join(dir, "locked")
	if err := os.Mkdir(locked, 0o500); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

	_, err := Open(context.Background(), Config{Path: filepath.Join(locked, "control.db")})
	if err == nil {
		t.Fatal("Open succeeded in a directory that cannot be written")
	}
}

// `?` and `#` are legal in Unix filenames but are delimiters in a SQLite URI.
// Concatenating the path into the DSN truncates it at the first one, and
// SQLite then opens a different file — succeeding, while every message still
// names the path that was requested. The database must be at the path asked
// for, or Open must fail.
func TestOpenHandlesURIDelimitersInPath(t *testing.T) {
	for _, name := range []string{
		"question?mark", "hash#mark", "amp&sand", "per%cent", "space bar",
	} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), name)
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatalf("mkdir %q: %v", dir, err)
			}
			path := filepath.Join(dir, "control.db")

			d, err := Open(context.Background(), Config{Path: path})
			if err != nil {
				t.Fatalf("open %q: %v", path, err)
			}
			t.Cleanup(func() { _ = d.Close() })

			// The file must exist exactly where it was requested.
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("database not created at the requested path %q: %v", path, err)
			}

			// And it must be a real, migrated, writable database — not an
			// empty file that happens to sit at the right path.
			if err := d.VerifyWritable(context.Background()); err != nil {
				t.Errorf("VerifyWritable: %v", err)
			}
			var n int
			if err := d.Reader().QueryRow(
				`SELECT count(*) FROM schema_migrations`).Scan(&n); err != nil {
				t.Fatalf("query migrations: %v", err)
			}
			if n == 0 {
				t.Error("no migrations applied; a different file was probably opened")
			}
		})
	}
}
