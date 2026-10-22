package audit_test

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

	"github.com/noisefloor-oss/deck/internal/audit"
	"github.com/noisefloor-oss/deck/internal/db"
	"github.com/noisefloor-oss/deck/internal/project"
	"github.com/noisefloor-oss/deck/internal/session"
)

func openDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(context.Background(), db.Config{
		Path: filepath.Join(t.TempDir(), "control.db"),
	})
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// newSessionID registers a real project and session — audit_events'
// session_id is FK-protected (ON DELETE RESTRICT), so a session-bound
// event needs the real chain. That is the schema working as designed.
func newSessionID(t *testing.T, d *db.DB) string {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	p, _, err := project.NewRepository(d).Register(ctx, "demo", root, []string{root}, project.GitIdentity{Name: "Test Author", Email: "test@example.invalid"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	native := "native-1"
	s := &session.Session{
		ID:              "s-test-1",
		ProjectID:       p.ID,
		Provider:        "claude",
		TmuxSocketPath:  filepath.Join(t.TempDir(), "ac.sock"),
		TmuxSessionName: "noisedeck-test",
		Generation:      1,
		NativeSessionID: &native,
	}
	if err := session.NewRepository(d).Create(ctx, s); err != nil {
		t.Fatalf("create session: %v", err)
	}
	return s.ID
}

func appendN(t *testing.T, d *db.DB, r *audit.Repository, n int, sessionID *string) {
	t.Helper()
	for i := 0; i < n; i++ {
		err := d.WithTx(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
			return r.Append(ctx, tx, sessionID, audit.SessionCreated, "test event")
		})
		if err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
}

func TestAppendAndListRoundTrip(t *testing.T) {
	d := openDB(t)
	r := audit.NewRepository(d)
	sid := newSessionID(t, d)

	before, err := r.List(context.Background(), audit.Query{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	base := len(before) // register + create write their own events

	appendN(t, d, r, 3, &sid)
	all, err := r.List(context.Background(), audit.Query{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != base+3 {
		t.Fatalf("events = %d, want %d", len(all), base+3)
	}
	// Ordered by id — the total order.
	for i := 1; i < len(all); i++ {
		if all[i].ID <= all[i-1].ID {
			t.Fatalf("order broken at %d: %v", i, all)
		}
	}
	last := all[len(all)-1]
	if last.Type != string(audit.SessionCreated) || *last.SessionID != sid {
		t.Fatalf("event = %+v", last)
	}

	// Filters: session, type, and their combination.
	filtered, err := r.List(context.Background(), audit.Query{SessionID: sid})
	if err != nil || len(filtered) != 4 { // 3 appended + create's own
		t.Fatalf("session filter: %d %v", len(filtered), err)
	}
	none, err := r.List(context.Background(), audit.Query{SessionID: "other"})
	if err != nil || len(none) != 0 {
		t.Fatalf("wrong session: %d %v", len(none), err)
	}
	byType, err := r.List(context.Background(), audit.Query{Type: string(audit.SessionExited)})
	if err != nil || len(byType) != 0 {
		t.Fatalf("type filter: %d %v", len(byType), err)
	}
}

func TestListLimitReturnsLatestPage(t *testing.T) {
	d := openDB(t)
	r := audit.NewRepository(d)
	appendN(t, d, r, 5, nil)
	page, err := r.List(context.Background(), audit.Query{Limit: 2})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(page) != 2 {
		t.Fatalf("page = %d, want 2", len(page))
	}
	// The latest two, presented oldest-first within the page.
	if page[0].ID != 4 || page[1].ID != 5 {
		t.Fatalf("page = %+v", page)
	}
}

func TestHandlerEvents(t *testing.T) {
	d := openDB(t)
	r := audit.NewRepository(d)
	sid := newSessionID(t, d)
	appendN(t, d, r, 2, &sid)

	h := audit.NewHandler(r, zerolog.Nop())
	mux := http.NewServeMux()
	h.Mount(mux.Handle, func(next http.Handler) http.Handler { return next })

	req := httptest.NewRequest(http.MethodGet, "/api/audit/events?session_id=s-test-1", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body)
	}
	var events []audit.Event
	if err := json.NewDecoder(rec.Body).Decode(&events); err != nil || len(events) != 3 {
		t.Fatalf("body: %v %v", events, err)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/audit/events?limit=banana", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "bad_request") {
		t.Fatalf("bad limit: status = %d body %s", rec.Code, rec.Body)
	}
}
