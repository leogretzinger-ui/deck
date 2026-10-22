package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// sessionColumnInfo snapshots the shape PRAGMA table_info reports: column
// order, type, NOT NULL, and defaults. The 031 rebuild recreates the table,
// so any column the new CREATE TABLE drops or retypes shows up here as a
// diff against the pre-migration snapshot.
func sessionColumnInfo(t *testing.T, db *sql.DB) [][4]any {
	t.Helper()
	rows, err := db.Query(`PRAGMA table_info(sessions)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var cols [][4]any
	for rows.Next() {
		var cid, pk int
		var name, colType string
		var notNull, dflt any
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		cols = append(cols, [4]any{name, colType, notNull, dflt})
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return cols
}

func TestMigrate031AdmitsQwenAndRebuildsSessionsIntact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.db")
	db := open(t, path, 5000)
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	migrations, err := LoadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, createSchemaMigrations); err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations {
		if migration.Version >= 31 {
			break
		}
		if err := applyOne(ctx, db, migration); err != nil {
			t.Fatalf("apply migration %d: %v", migration.Version, err)
		}
	}
	p := seedProject(t, db, "p1")
	const started = "2026-08-20T02:00:00.000Z"
	if _, err := db.Exec(`INSERT INTO sessions (
		id, project_id, provider, tmux_socket_path, tmux_session_name,
		tmux_session_id, tmux_pane_id, identity_marker, pane_pid,
		process_group_id, process_start_ticks, expected_executable,
		native_session_id, status, created_at, updated_at, display_name,
		color, boot_epoch, generation_started_at, generation_resumed,
		automatic_recoveries, automatic_recovery_series_at
	) VALUES ('existing', ?, 'codex', '/run/tmux.sock', 'noisedeck-existing',
		'$1', '%2', 'marker-existing', 4242, 4242, 129809, '/usr/local/bin/node',
		NULL, 'starting', ?, ?, 'kept-name', '#C54D6E', 'boot-1', ?, 1, 3, ?)`,
		p, started, started, started, started); err != nil {
		t.Fatal(err)
	}
	before := sessionColumnInfo(t, db)

	for _, migration := range migrations {
		if migration.Version == 31 {
			if err := applyOne(ctx, db, migration); err != nil {
				t.Fatalf("apply migration 31: %v", err)
			}
		}
	}

	// The rebuild recreates every column with its original shape — type,
	// NOT NULL, default — under the same name. Positional order is
	// presentation: 030 appended its columns with ALTER TABLE while this
	// rebuild normalizes them into place, and nothing reads sessions
	// positionally.
	after := sessionColumnInfo(t, db)
	if len(after) != len(before) {
		t.Fatalf("column count %d after rebuild, want %d", len(after), len(before))
	}
	beforeByName := map[string][3]any{}
	for _, col := range before {
		beforeByName[col[0].(string)] = [3]any{col[1], col[2], col[3]}
	}
	for _, col := range after {
		name := col[0].(string)
		want, ok := beforeByName[name]
		if !ok {
			t.Fatalf("column %q appeared in the rebuild that 030 did not have", name)
		}
		got := [3]any{col[1], col[2], col[3]}
		if got != want {
			t.Fatalf("column %q changed across the rebuild: %v -> %v", name, want, got)
		}
		delete(beforeByName, name)
	}
	if len(beforeByName) > 0 {
		t.Fatalf("columns lost in the rebuild: %v", beforeByName)
	}

	// Both session indexes must come back; the rebuild drops them with the
	// table and nothing else recreates them.
	for _, idx := range []string{"idx_sessions_project", "idx_sessions_status"} {
		var n int
		if err := db.QueryRow(
			`SELECT count(*) FROM sqlite_master WHERE type='index' AND name=?`, idx,
		).Scan(&n); err != nil || n != 1 {
			t.Fatalf("index %s missing after the rebuild (n=%d, err=%v)", idx, n, err)
		}
	}

	// Representative row: the identity tuple, presentation fields, and the
	// generation-refusal evidence all survive the rebuild byte-for-byte.
	var gotMarker, gotDisplay, gotColor, gotBoot string
	var gotPane, gotPGID, gotTicks int
	var gotStarted string
	var gotResumed, gotRecoveries int
	var gotSeries string
	if err := db.QueryRow(`SELECT identity_marker, pane_pid, process_group_id,
		process_start_ticks, display_name, color, boot_epoch,
		generation_started_at, generation_resumed,
		automatic_recoveries, automatic_recovery_series_at
		FROM sessions WHERE id='existing'`).Scan(&gotMarker, &gotPane, &gotPGID, &gotTicks,
		&gotDisplay, &gotColor, &gotBoot, &gotStarted, &gotResumed, &gotRecoveries,
		&gotSeries); err != nil {
		t.Fatal(err)
	}
	if gotMarker != "marker-existing" || gotPane != 4242 || gotPGID != 4242 || gotTicks != 129809 ||
		gotDisplay != "kept-name" || gotColor != "#C54D6E" || gotBoot != "boot-1" ||
		gotStarted != started || gotResumed != 1 || gotRecoveries != 3 || gotSeries != started {
		t.Fatalf("existing row did not survive the rebuild: %q/%d/%d/%d/%q/%q/%q/%q/%d/%d/%q",
			gotMarker, gotPane, gotPGID, gotTicks, gotDisplay, gotColor, gotBoot,
			gotStarted, gotResumed, gotRecoveries, gotSeries)
	}

	// The controller-revision trigger must come back and fire: a tracked
	// update bumps the revision, so a rebuild that silently dropped it
	// leaves every later controller transition invisible.
	if _, err := db.Exec(`UPDATE sessions SET native_session_id='native-after'
		WHERE id='existing'`); err != nil {
		t.Fatalf("tracked update after the rebuild: %v", err)
	}
	var revision int
	if err := db.QueryRow(`SELECT controller_revision FROM sessions WHERE id='existing'`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if revision != 1 {
		t.Fatalf("controller_revision = %d after a tracked update, want 1", revision)
	}

	// The project foreign key must come back with ON DELETE RESTRICT: the
	// pragma reports it and deleting the referenced project is refused.
	var onDelete string
	if err := db.QueryRow(`SELECT on_delete FROM pragma_foreign_key_list('sessions')
		WHERE "table"='projects'`).Scan(&onDelete); err != nil {
		t.Fatalf("sessions no longer references projects: %v", err)
	}
	if onDelete != "RESTRICT" {
		t.Fatalf("sessions->projects on_delete = %s, want RESTRICT", onDelete)
	}
	if _, err := db.Exec(`DELETE FROM projects WHERE id=?`, p); err == nil {
		t.Fatal("deleting a project with live sessions was accepted after the rebuild")
	}

	// The CHECK domains come back with the table: the widened provider list
	// admits qwen and still refuses an unknown provider, and the
	// generation-refusal domain still refuses values outside {0,1}.
	if _, err := db.Exec(`INSERT INTO sessions (
		id, project_id, provider, tmux_socket_path, tmux_session_name,
		status, created_at, updated_at
	) VALUES ('qwen', ?, 'qwen', '/run/tmux.sock', 'noisedeck-qwen',
		'starting', ?, ?)`, p, started, started); err != nil {
		t.Fatalf("qwen provider row rejected: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO sessions (
		id, project_id, provider, tmux_socket_path, tmux_session_name,
		status, created_at, updated_at
	) VALUES ('bogus', ?, 'qwen-preview', '/run/tmux.sock', 'noisedeck-bogus',
		'starting', ?, ?)`, p, started, started); err == nil {
		t.Fatal("unknown provider accepted")
	}
	if _, err := db.Exec(`UPDATE sessions SET generation_resumed=2 WHERE id='qwen'`); err == nil {
		t.Fatal("generation_resumed accepted a value outside {0,1}")
	}
}
