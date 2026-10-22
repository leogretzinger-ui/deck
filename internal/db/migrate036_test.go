package db

import (
	"context"
	"path/filepath"
	"testing"
)

// Migration 036 adds the conversation store record: the ledger file the
// provider's hook reported and its derived store root. Rows that predate the
// migration read NULL for both — a claim-free state that gates nothing — and
// the columns round-trip values after it.
func TestMigrate036RecordsTheConversationStore(t *testing.T) {
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
		if migration.Version >= 36 {
			break
		}
		if err := applyOne(ctx, db, migration); err != nil {
			t.Fatalf("apply migration %d: %v", migration.Version, err)
		}
	}
	p := seedProject(t, db, "p1")
	const now = "2026-08-27T02:00:00.000Z"
	if _, err := db.Exec(`INSERT INTO sessions (
		id, project_id, provider, tmux_socket_path, tmux_session_name,
		status, created_at, updated_at
	) VALUES ('legacy', ?, 'claude', '/run/tmux.sock', 'noisedeck-legacy',
		'starting', ?, ?)`, p, now, now); err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations {
		if migration.Version == 36 {
			if err := applyOne(ctx, db, migration); err != nil {
				t.Fatalf("apply migration 36: %v", err)
			}
		}
	}
	var transcript, root *string
	if err := db.QueryRow(`SELECT native_transcript_path, native_store_root
		FROM sessions WHERE id='legacy'`).Scan(&transcript, &root); err != nil {
		t.Fatal(err)
	}
	if transcript != nil || root != nil {
		t.Fatalf("pre-migration row claims a store: transcript=%v root=%v", transcript, root)
	}
	const ledger = "/home/op/.claude/projects/-workspace-app/8b2f7c1d.jsonl"
	if _, err := db.Exec(`UPDATE sessions
		SET native_transcript_path = ?, native_store_root = '/home/op/.claude'
		WHERE id='legacy'`, ledger); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT native_transcript_path, native_store_root
		FROM sessions WHERE id='legacy'`).Scan(&transcript, &root); err != nil {
		t.Fatal(err)
	}
	if transcript == nil || *transcript != ledger || root == nil || *root != "/home/op/.claude" {
		t.Fatalf("store record did not round-trip: transcript=%v root=%v", transcript, root)
	}
}
