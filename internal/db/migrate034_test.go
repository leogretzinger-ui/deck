package db

import (
	"context"
	"path/filepath"
	"testing"
)

func TestMigrate034AdmitsCursorAndKeepsQoder(t *testing.T) {
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
		if migration.Version >= 34 {
			break
		}
		if err := applyOne(ctx, db, migration); err != nil {
			t.Fatalf("apply migration %d: %v", migration.Version, err)
		}
	}
	p := seedProject(t, db, "p1")
	const now = "2026-08-22T02:00:00.000Z"
	if _, err := db.Exec(`INSERT INTO sessions (
		id, project_id, provider, tmux_socket_path, tmux_session_name,
		status, created_at, updated_at, generation_started_at,
		generation_resumed, automatic_recoveries, automatic_recovery_series_at
	) VALUES ('qoder', ?, 'qoder', '/run/tmux.sock', 'noisedeck-qoder',
		'starting', ?, ?, ?, 1, 4, ?)`, p, now, now, now, now); err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations {
		if migration.Version == 34 {
			if err := applyOne(ctx, db, migration); err != nil {
				t.Fatalf("apply migration 34: %v", err)
			}
		}
	}
	var resumed, recoveries int
	var started, series string
	if err := db.QueryRow(`SELECT generation_started_at, generation_resumed,
		automatic_recoveries, automatic_recovery_series_at FROM sessions WHERE id='qoder'`).Scan(&started, &resumed, &recoveries, &series); err != nil {
		t.Fatal(err)
	}
	if started != now || resumed != 1 || recoveries != 4 || series != now {
		t.Fatalf("retained evidence = %q/%d/%d/%q", started, resumed, recoveries, series)
	}
	if _, err := db.Exec(`INSERT INTO sessions (
		id, project_id, provider, tmux_socket_path, tmux_session_name,
		status, created_at, updated_at
	) VALUES ('cursor', ?, 'cursor', '/run/tmux.sock', 'noisedeck-cursor',
		'starting', ?, ?)`, p, now, now); err != nil {
		t.Fatalf("cursor provider row rejected: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO sessions (
		id, project_id, provider, tmux_socket_path, tmux_session_name,
		status, created_at, updated_at
	) VALUES ('bogus', ?, 'cursor-preview', '/run/tmux.sock', 'noisedeck-bogus',
		'starting', ?, ?)`, p, now, now); err == nil {
		t.Fatal("unknown provider accepted")
	}
}
