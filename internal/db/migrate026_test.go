package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestMigrate026UpgradesReleasedCaptureStateAndEnforcesGrantShape(t *testing.T) {
	database, _ := openMigratedThrough(t, 25)
	projectID := seedProject(t, database, "p026")
	sessionID := seedSession(t, database, "s026", projectID)
	terminalID := seedSession(t, database, "s026-terminal", projectID)
	if _, err := database.Exec(`UPDATE sessions
		SET provider = 'codex', native_session_id = NULL,
		    identity_capture_lost_at = NULL WHERE id = ?`, sessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`UPDATE sessions
		SET provider = 'codex', status = 'interrupted', native_session_id = NULL,
		    identity_capture_lost_at = NULL WHERE id = ?`, terminalID); err != nil {
		t.Fatal(err)
	}

	migration := migrationByVersion(t, 26)
	if err := applyOne(context.Background(), database, migration); err != nil {
		t.Fatalf("apply migration 026: %v", err)
	}
	var lostAt *string
	if err := database.QueryRow(`SELECT identity_capture_lost_at FROM sessions WHERE id = ?`,
		sessionID).Scan(&lostAt); err != nil || lostAt == nil {
		t.Fatalf("released pending capture upgrade = %v err=%v", lostAt, err)
	}
	var losses int
	if err := database.QueryRow(`SELECT count(*) FROM audit_events
		WHERE session_id = ? AND event_type = 'session.identity_capture_lost'`,
		sessionID).Scan(&losses); err != nil || losses != 1 {
		t.Fatalf("upgrade capture-loss audits = %d err=%v, want 1", losses, err)
	}
	if err := database.QueryRow(`SELECT identity_capture_lost_at FROM sessions WHERE id = ?`,
		terminalID).Scan(&lostAt); err != nil || lostAt != nil {
		t.Fatalf("terminal diagnostic capture loss = %v err=%v, want nil", lostAt, err)
	}
	if err := database.QueryRow(`SELECT count(*) FROM audit_events
		WHERE session_id = ? AND event_type = 'session.identity_capture_lost'`,
		terminalID).Scan(&losses); err != nil || losses != 0 {
		t.Fatalf("terminal diagnostic capture-loss audits = %d err=%v, want 0", losses, err)
	}

	now := "2026-08-14T00:00:00.000Z"
	insertGrant := `INSERT INTO provider_identity_capture_grants (
		token_sha256, session_id, provider, generation, identity_marker,
		canonical_work_dir, expected_source, expected_native_id_sha256,
		expires_at, state, accepted_event_sha256, closed_at, created_at, updated_at
	) VALUES (?, ?, 'codex', ?, ?, '/workspace/p026', ?, ?, ?, ?, ?, ?, ?, ?)`
	if _, err := database.Exec(insertGrant, hex64(1), sessionID, 1, "marker-s026",
		"startup", nil,
		now, "pending", nil, nil, now, now); err != nil {
		t.Fatalf("insert valid pending grant: %v", err)
	}
	assertRejected(t, execErr(database, insertGrant, "A"+hex64(2)[1:], sessionID, 2, "m2",
		"startup", nil, now, "pending", nil, nil, now, now), "check")
	assertRejected(t, execErr(database, insertGrant, hex64(3), sessionID, 3, "m3",
		"resume", nil, now, "pending", nil, nil, now, now), "check")
	assertRejected(t, execErr(database, insertGrant, hex64(4), sessionID, 4, "m4",
		"startup", nil, now, "consumed", nil, now, now, now), "check")

	if _, err := database.Exec(`INSERT INTO session_successor_slots (
		session_id, source_generation, source_controller_revision,
		candidate_generation, candidate_identity_marker, created_at
	) VALUES (?, 1, 0, 1, 'candidate', ?)`, sessionID, now); err == nil {
		t.Fatal("successor slot accepted a non-advancing generation")
	}
}

func openMigratedThrough(t *testing.T, maxVersion int) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "control.db")
	database := open(t, path, 5000)
	migrateThrough(t, database, maxVersion)
	return database, path
}

func migrationByVersion(t *testing.T, version int) Migration {
	t.Helper()
	migrations, err := LoadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations {
		if migration.Version == version {
			return migration
		}
	}
	t.Fatalf("migration %03d is not embedded", version)
	return Migration{}
}

func execErr(database *sql.DB, query string, args ...any) error {
	_, err := database.Exec(query, args...)
	return err
}
