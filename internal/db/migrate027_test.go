package db

import (
	"context"
	"testing"
)

func TestMigrate027PreservesDurableCaptureReferences(t *testing.T) {
	database, _ := openMigratedThrough(t, 26)
	projectID := seedProject(t, database, "p027-upgrade")
	sessionID := seedSession(t, database, "s027-upgrade", projectID)
	now := "2026-08-14T00:00:00.000Z"
	tokenHash := hex64(27)
	if _, err := database.Exec(`INSERT INTO provider_identity_capture_grants (
		token_sha256, session_id, provider, generation, identity_marker,
		canonical_work_dir, expected_source, expected_native_id_sha256,
		expires_at, state, created_at, updated_at
	) VALUES (?, ?, 'codex', 2, 'candidate-027', '/workspace/p027-upgrade',
		'startup', NULL, ?, 'pending', ?, ?)`, tokenHash, sessionID, now, now, now); err != nil {
		t.Fatalf("seed durable capture grant: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO session_successor_slots (
		session_id, source_generation, source_controller_revision,
		candidate_generation, candidate_identity_marker, capture_token_sha256,
		created_at
	) VALUES (?, 1, 0, 2, 'candidate-027', ?, ?)`, sessionID, tokenHash, now); err != nil {
		t.Fatalf("seed successor slot: %v", err)
	}

	if err := applyOne(context.Background(), database, migrationByVersion(t, 27)); err != nil {
		t.Fatalf("apply migration 027: %v", err)
	}
	var grantSession, slotToken string
	if err := database.QueryRow(`SELECT session_id FROM provider_identity_capture_grants
		WHERE token_sha256 = ?`, tokenHash).Scan(&grantSession); err != nil {
		t.Fatalf("read preserved grant: %v", err)
	}
	if err := database.QueryRow(`SELECT capture_token_sha256 FROM session_successor_slots
		WHERE session_id = ?`, sessionID).Scan(&slotToken); err != nil {
		t.Fatalf("read preserved slot: %v", err)
	}
	if grantSession != sessionID || slotToken != tokenHash {
		t.Fatalf("durable capture references changed: grant_session=%t slot_token=%t",
			grantSession == sessionID, slotToken == tokenHash)
	}
	rows, err := database.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("migration 027 left a broken foreign-key reference")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("check migration 027 foreign-key references: %v", err)
	}
	if _, err := database.Exec(`DELETE FROM sessions WHERE id = ?`, sessionID); err == nil {
		t.Fatal("durable capture references no longer restrict session deletion")
	}
}
