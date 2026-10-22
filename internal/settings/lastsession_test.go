package settings

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"
)

// A fresh database remembers nothing, and a cleared record reads the same
// as one never written: the console then falls back to its own browser.
func TestLastSessionAbsentUntilWritten(t *testing.T) {
	repo, _ := newTestRepo(t)
	ctx := context.Background()
	if _, found, err := repo.GetLastSession(ctx, ""); err != nil || found {
		t.Fatalf("fresh read: found=%v err=%v; want absent", found, err)
	}
	if err := repo.SetLastSession(ctx, "", "sess-1"); err != nil {
		t.Fatalf("set: %v", err)
	}
	id, found, err := repo.GetLastSession(ctx, "")
	if err != nil || !found || id != "sess-1" {
		t.Fatalf("read after set: id=%q found=%v err=%v", id, found, err)
	}
	if err := repo.SetLastSession(ctx, "", ""); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if _, found, err := repo.GetLastSession(ctx, ""); err != nil || found {
		t.Fatalf("read after clear: found=%v err=%v; want absent", found, err)
	}
}

// One user's place is theirs: another principal reads nothing, and the
// unauthenticated installation-wide record is a scope of its own.
func TestLastSessionScopedByPrincipal(t *testing.T) {
	repo, _ := newTestRepo(t)
	ctx := context.Background()
	if err := repo.SetLastSession(ctx, "alice", "sess-a"); err != nil {
		t.Fatalf("set alice: %v", err)
	}
	if _, found, _ := repo.GetLastSession(ctx, "bob"); found {
		t.Fatalf("bob read alice's session")
	}
	if _, found, _ := repo.GetLastSession(ctx, ""); found {
		t.Fatalf("the installation scope read a user's session")
	}
	id, found, _ := repo.GetLastSession(ctx, "alice")
	if !found || id != "sess-a" {
		t.Fatalf("alice read %q found=%v", id, found)
	}
}

// Ids are opaque, but a document is not an id.
func TestLastSessionRejectsOversizedID(t *testing.T) {
	repo, _ := newTestRepo(t)
	err := repo.SetLastSession(context.Background(), "", strings.Repeat("x", maxSessionIDLength+1))
	if err == nil {
		t.Fatalf("oversized id accepted")
	}
}

// The handler: 404 until written, the stored document after, 400 on a body
// that is not a last-session object, and an empty id forgets.
func TestLastSessionHandler(t *testing.T) {
	repo, _ := newTestRepo(t)
	h := NewHandler(repo, zerolog.Nop())

	rec := httptest.NewRecorder()
	h.GetLastSession(rec, httptest.NewRequest(http.MethodGet, "/api/settings/last-session", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("fresh GET = %d; want 404", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.PutLastSession(rec, httptest.NewRequest(http.MethodPut, "/api/settings/last-session",
		strings.NewReader(`{"session_id":"sess-7"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d body=%s; want 200", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.GetLastSession(rec, httptest.NewRequest(http.MethodGet, "/api/settings/last-session", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET after PUT = %d; want 200", rec.Code)
	}
	var doc lastSessionDocument
	if err := json.NewDecoder(rec.Body).Decode(&doc); err != nil || doc.SessionID != "sess-7" {
		t.Fatalf("GET body = %+v err=%v; want sess-7", doc, err)
	}

	rec = httptest.NewRecorder()
	h.PutLastSession(rec, httptest.NewRequest(http.MethodPut, "/api/settings/last-session",
		strings.NewReader(`[1,2]`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT of a non-object = %d; want 400", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.PutLastSession(rec, httptest.NewRequest(http.MethodPut, "/api/settings/last-session",
		strings.NewReader(`{"session_id":""}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("clearing PUT = %d; want 200", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.GetLastSession(rec, httptest.NewRequest(http.MethodGet, "/api/settings/last-session", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET after clear = %d; want 404", rec.Code)
	}
}
