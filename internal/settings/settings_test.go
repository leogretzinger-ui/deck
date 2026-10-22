package settings

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rs/zerolog"

	"github.com/noisefloor-oss/deck/internal/db"
)

func newTestRepo(t *testing.T) (*Repository, *db.DB) {
	t.Helper()
	d, err := db.Open(context.Background(), db.Config{
		Path: filepath.Join(t.TempDir(), "control.db"),
	})
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return NewRepository(d), d
}

// A fresh database has no settings row: every setting reads as off. For
// permission-skipping that is the whole safety property — a console that
// defaulted to "on" would skip prompts nobody asked to skip.
func TestDefaultsAreOff(t *testing.T) {
	repo, _ := newTestRepo(t)
	s, err := repo.Get(context.Background())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if s.SkipPermissions {
		t.Fatal("skip_permissions defaults to true; it must default to false")
	}
	if s.SkipCloseConfirm {
		t.Fatal("skip_close_confirm defaults to true; the confirmation must be on by default")
	}
	if s.SkipRemoveProjectConfirm {
		t.Fatal("skip_remove_project_confirm defaults to true; the confirmation must be on by default")
	}
	skip, err := repo.SkipPermissions(context.Background())
	if err != nil || skip {
		t.Fatalf("SkipPermissions() = %v, %v; want false, nil", skip, err)
	}
}

// Put replaces the whole document, so a client that does not know a
// setting omits it and writes the zero value. Every setting is named for
// what it SKIPS so that zero value is the careful one — otherwise a stale
// client silently drops a safety prompt.
func TestOmittedFieldsWriteTheCarefulValue(t *testing.T) {
	repo, d := newTestRepo(t)
	ctx := context.Background()
	if err := repo.Set(ctx, Settings{SkipPermissions: true, SkipCloseConfirm: true, SkipRemoveProjectConfirm: true}); err != nil {
		t.Fatalf("set: %v", err)
	}

	// A body from a client that predates skip_close_confirm.
	var stale Settings
	if err := json.Unmarshal([]byte(`{"skip_permissions":true}`), &stale); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if err := repo.Set(ctx, stale); err != nil {
		t.Fatalf("set stale: %v", err)
	}
	s, err := repo.Get(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if s.SkipCloseConfirm {
		t.Fatal("an omitted skip_close_confirm left the confirmation off; it must fall back to on")
	}
	if s.SkipRemoveProjectConfirm {
		t.Fatal("an omitted skip_remove_project_confirm left the confirmation off; it must fall back to on")
	}
	if !s.SkipPermissions {
		t.Fatal("the field the client did send was lost")
	}
	// Read the row directly. Get() returning false would satisfy the check
	// above even if it had stopped reading this key at all, which would
	// make the test pass for the wrong reason.
	var stored string
	if err := d.Reader().QueryRow(
		`SELECT value FROM settings WHERE key = ?`, KeySkipCloseConfirm).Scan(&stored); err != nil {
		t.Fatalf("read stored value: %v", err)
	}
	if stored != "false" {
		t.Fatalf("stored %s = %q, want %q", KeySkipCloseConfirm, stored, "false")
	}
}

// Each key audits on its own: a write that changes one must not record
// the other, and a write that changes both must record both.
func TestEachSettingAuditsIndependently(t *testing.T) {
	repo, d := newTestRepo(t)
	ctx := context.Background()
	count := func() int {
		var n int
		if err := d.Reader().QueryRow(
			`SELECT COUNT(*) FROM audit_events WHERE event_type = ?`, AuditChanged).Scan(&n); err != nil {
			t.Fatalf("count audit: %v", err)
		}
		return n
	}

	if err := repo.Set(ctx, Settings{SkipCloseConfirm: true}); err != nil {
		t.Fatalf("set: %v", err)
	}
	if n := count(); n != 1 {
		t.Fatalf("audit events = %d, want 1 (only skip_close_confirm changed)", n)
	}
	var details string
	if err := d.Reader().QueryRow(
		`SELECT details FROM audit_events WHERE event_type = ? ORDER BY id DESC LIMIT 1`,
		AuditChanged).Scan(&details); err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if !strings.Contains(details, "skip_close_confirm=true") || !strings.Contains(details, "was false") {
		t.Fatalf("audit details = %q, want the before and after", details)
	}

	// Both flip in one write: two records, not one.
	if err := repo.Set(ctx, Settings{SkipPermissions: true, SkipCloseConfirm: false}); err != nil {
		t.Fatalf("set both: %v", err)
	}
	if n := count(); n != 3 {
		t.Fatalf("audit events = %d, want 3 (both of this write's changes recorded)", n)
	}
}

// An unreadable stored value fails closed for this key too.
func TestUnknownStoredCloseConfirmReadsAsOff(t *testing.T) {
	repo, d := newTestRepo(t)
	ctx := context.Background()
	for _, stored := range []string{"TRUE", "1", "yes", "", "on"} {
		if err := d.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `
				INSERT INTO settings (key, value, updated_at) VALUES (?, ?, '2026-01-01T00:00:00.000Z')
				ON CONFLICT(key) DO UPDATE SET value = excluded.value`, KeySkipCloseConfirm, stored)
			return err
		}); err != nil {
			t.Fatalf("plant %q: %v", stored, err)
		}
		s, err := repo.Get(ctx)
		if err != nil {
			t.Fatalf("read %q: %v", stored, err)
		}
		if s.SkipCloseConfirm {
			t.Fatalf("stored %q read as ON; only the exact %q spelling may enable it", stored, "true")
		}
	}
}

func TestRemoveProjectConfirmationSettingRoundTrips(t *testing.T) {
	repo, d := newTestRepo(t)
	ctx := context.Background()
	if err := repo.Set(ctx, Settings{SkipRemoveProjectConfirm: true}); err != nil {
		t.Fatalf("set: %v", err)
	}
	s, err := repo.Get(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !s.SkipRemoveProjectConfirm || s.SkipCloseConfirm || s.SkipPermissions {
		t.Fatalf("settings = %+v, want only project-removal confirmation skipped", s)
	}
	var details string
	if err := d.Reader().QueryRow(
		`SELECT details FROM audit_events WHERE event_type = ? ORDER BY id DESC LIMIT 1`,
		AuditChanged).Scan(&details); err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if !strings.Contains(details, "skip_remove_project_confirm=true") || !strings.Contains(details, "was false") {
		t.Fatalf("audit details = %q, want the before and after", details)
	}
}

// Writes round-trip, are idempotent, and audit only real changes.
func TestSetRoundTripAndAudit(t *testing.T) {
	repo, d := newTestRepo(t)
	ctx := context.Background()

	if err := repo.Set(ctx, Settings{SkipPermissions: true}); err != nil {
		t.Fatalf("set: %v", err)
	}
	s, err := repo.Get(ctx)
	if err != nil || !s.SkipPermissions {
		t.Fatalf("after set: %+v, %v", s, err)
	}
	// Re-writing the same value is a no-op for the audit trail: an audit
	// full of unchanged writes hides the changes that matter.
	if err := repo.Set(ctx, Settings{SkipPermissions: true}); err != nil {
		t.Fatalf("idempotent set: %v", err)
	}
	var n int
	if err := d.Reader().QueryRow(
		`SELECT COUNT(*) FROM audit_events WHERE event_type = ?`, AuditChanged).Scan(&n); err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if n != 1 {
		t.Fatalf("audit events = %d, want exactly 1 (the real change)", n)
	}

	// Turning it back off audits again, with the before/after on record.
	if err := repo.Set(ctx, Settings{SkipPermissions: false}); err != nil {
		t.Fatalf("unset: %v", err)
	}
	var details string
	if err := d.Reader().QueryRow(
		`SELECT details FROM audit_events WHERE event_type = ? ORDER BY id DESC LIMIT 1`,
		AuditChanged).Scan(&details); err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if !strings.Contains(details, "skip_permissions=false") || !strings.Contains(details, "was true") {
		t.Fatalf("audit details = %q, want the before and after", details)
	}
}

// An unreadable stored value fails CLOSED. A setting that removes safety
// prompts must never be enabled by a value nobody wrote deliberately.
func TestUnknownStoredValueReadsAsOff(t *testing.T) {
	repo, d := newTestRepo(t)
	ctx := context.Background()
	for _, stored := range []string{"TRUE", "1", "yes", "", "on"} {
		if err := d.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `
				INSERT INTO settings (key, value, updated_at) VALUES (?, ?, '2026-01-01T00:00:00.000Z')
				ON CONFLICT(key) DO UPDATE SET value = excluded.value`, KeySkipPermissions, stored)
			return err
		}); err != nil {
			t.Fatalf("plant %q: %v", stored, err)
		}
		skip, err := repo.SkipPermissions(ctx)
		if err != nil {
			t.Fatalf("read %q: %v", stored, err)
		}
		if skip {
			t.Fatalf("stored %q read as ON; only the exact %q spelling may enable it", stored, "true")
		}
	}
}

// The endpoints round-trip the document and reject a malformed body.
func TestHandlerRoundTrip(t *testing.T) {
	repo, _ := newTestRepo(t)
	h := NewHandler(repo, zerolog.Nop())
	mux := http.NewServeMux()
	h.Mount(mux.Handle, func(next http.Handler) http.Handler { return next })

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/settings", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d (%s)", rec.Code, rec.Body)
	}
	var got Settings
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil || got.SkipPermissions {
		t.Fatalf("GET body = %+v, %v; want defaults", got, err)
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/settings",
		strings.NewReader(`{"skip_permissions":true}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d (%s)", rec.Code, rec.Body)
	}
	stored, err := repo.Get(context.Background())
	if err != nil || !stored.SkipPermissions {
		t.Fatalf("after PUT: %+v, %v", stored, err)
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/settings",
		strings.NewReader(`not json`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed PUT status = %d, want 400", rec.Code)
	}
	// The bad request must not have changed anything.
	stored, err = repo.Get(context.Background())
	if err != nil || !stored.SkipPermissions {
		t.Fatalf("malformed PUT mutated state: %+v, %v", stored, err)
	}
}
