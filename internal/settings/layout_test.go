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

// A fresh database has no layout row: the repository must report it absent
// so the UI can fall back to its defaults rather than treat a missing row as
// an explicit all-collapsed state.
func TestLayoutDefaultsAreEmpty(t *testing.T) {
	repo, _ := newTestRepo(t)
	l, found, err := repo.GetLayout(context.Background())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if found {
		t.Fatalf("layout reported present for a fresh database")
	}
	if len(l.Expanded) != 0 || len(l.Projects) != 0 || len(l.Sessions) != 0 {
		t.Fatalf("layout = %+v; want empty fields", l)
	}
}

// The full document round-trips, including the nested per-project session
// order. A missing field stays nil/empty on read, which the UI ignores.
func TestLayoutRoundTrip(t *testing.T) {
	repo, _ := newTestRepo(t)
	ctx := context.Background()
	want := Layout{
		Expanded: []string{"proj-a", "proj-c"},
		Projects: []string{"proj-c", "proj-a", "proj-b"},
		Sessions: map[string][]string{
			"proj-a": {"sess-2", "sess-1"},
			"proj-b": {"sess-3"},
		},
		Colors: map[string]string{"proj-b": "gray", "proj-c": "red"},
	}
	if err := repo.SetLayout(ctx, want); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, found, err := repo.GetLayout(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !found {
		t.Fatalf("layout reported absent after write")
	}
	if !layoutsEqual(got, want) {
		t.Fatalf("layout = %+v, want %+v", got, want)
	}
}

// Setting an all-empty layout creates a real saved state that must be
// distinguishable from a never-written row. The repository reports it present
// so the UI collapses every project instead of falling back to defaults.
func TestLayoutEmptyAfterSetIsPresent(t *testing.T) {
	repo, _ := newTestRepo(t)
	ctx := context.Background()
	want := Layout{Expanded: []string{}, Projects: []string{}, Sessions: map[string][]string{}}
	if err := repo.SetLayout(ctx, want); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, found, err := repo.GetLayout(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !found {
		t.Fatalf("all-empty layout reported absent after write")
	}
	if !layoutsEqual(got, want) {
		t.Fatalf("layout = %+v, want %+v", got, want)
	}
}

// Layout ids do not have to exist as projects or sessions. A stale id from
// a rename or deletion must round-trip harmlessly so the UI can drop it.
func TestLayoutSurvivesUnknownIDs(t *testing.T) {
	repo, _ := newTestRepo(t)
	ctx := context.Background()
	want := Layout{
		Expanded: []string{"deleted-project"},
		Projects: []string{"deleted-project", "still-here"},
		Sessions: map[string][]string{
			"deleted-project": {"deleted-session"},
		},
	}
	if err := repo.SetLayout(ctx, want); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, found, err := repo.GetLayout(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !found {
		t.Fatalf("layout reported absent after write")
	}
	if !layoutsEqual(got, want) {
		t.Fatalf("layout = %+v, want %+v", got, want)
	}
}

// Settings and layout are independent documents. A Settings Put must not
// touch the layout row, because the UI writes settings on every toggle and
// would otherwise erase the user's drag-built order.
func TestSettingsPutDoesNotDisturbLayout(t *testing.T) {
	repo, d := newTestRepo(t)
	ctx := context.Background()
	want := Layout{
		Expanded: []string{"proj-a"},
		Projects: []string{"proj-a", "proj-b"},
		Sessions: map[string][]string{
			"proj-a": {"sess-1"},
		},
	}
	if err := repo.SetLayout(ctx, want); err != nil {
		t.Fatalf("set layout: %v", err)
	}
	if err := repo.Set(ctx, Settings{SkipPermissions: true}); err != nil {
		t.Fatalf("set settings: %v", err)
	}
	got, found, err := repo.GetLayout(ctx)
	if err != nil {
		t.Fatalf("get layout: %v", err)
	}
	if !found {
		t.Fatalf("layout reported absent after settings write")
	}
	if !layoutsEqual(got, want) {
		t.Fatalf("layout = %+v, want %+v", got, want)
	}
	// The layout row must still exist and be the one we wrote.
	var stored string
	if err := d.Reader().QueryRow(
		`SELECT value FROM settings WHERE key = ?`, KeyLayout).Scan(&stored); err != nil {
		t.Fatalf("read stored layout: %v", err)
	}
	var storedLayout Layout
	if err := json.Unmarshal([]byte(stored), &storedLayout); err != nil {
		t.Fatalf("decode stored layout: %v", err)
	}
	if !layoutsEqual(storedLayout, want) {
		t.Fatalf("stored layout = %+v, want %+v", storedLayout, want)
	}
}

// Layout writes must not append audit events. The settings audit trail is
// for safety flags; a drag reorder would drown it.
func TestLayoutWriteDoesNotAudit(t *testing.T) {
	repo, d := newTestRepo(t)
	ctx := context.Background()
	if err := repo.SetLayout(ctx, Layout{Projects: []string{"proj-a"}}); err != nil {
		t.Fatalf("set: %v", err)
	}
	var n int
	if err := d.Reader().QueryRow(
		`SELECT COUNT(*) FROM audit_events WHERE event_type = ?`, AuditChanged).Scan(&n); err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if n != 0 {
		t.Fatalf("audit events = %d, want 0 (layout is not a security event)", n)
	}
}

// The layout endpoints round-trip the document and reject a malformed body.
func TestLayoutHandlerRoundTrip(t *testing.T) {
	repo, _ := newTestRepo(t)
	h := NewHandler(repo, zerolog.Nop())
	mux := http.NewServeMux()
	h.Mount(mux.Handle, func(next http.Handler) http.Handler { return next })

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/settings/layout", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET status = %d (%s), want 404 for a missing layout", rec.Code, rec.Body)
	}

	body := `{"expanded":["proj-a"],"projects":["proj-a","proj-b"],"sessions":{"proj-a":["sess-1"]}}`
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/settings/layout",
		strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d (%s)", rec.Code, rec.Body)
	}
	stored, found, err := repo.GetLayout(context.Background())
	if err != nil {
		t.Fatalf("after PUT: %v", err)
	}
	if !found {
		t.Fatalf("layout reported absent after PUT")
	}
	want := Layout{
		Expanded: []string{"proj-a"},
		Projects: []string{"proj-a", "proj-b"},
		Sessions: map[string][]string{"proj-a": {"sess-1"}},
	}
	if !layoutsEqual(stored, want) {
		t.Fatalf("stored layout = %+v, want %+v", stored, want)
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/settings/layout", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status after PUT = %d (%s)", rec.Code, rec.Body)
	}
	var got Layout
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil || !layoutsEqual(got, want) {
		t.Fatalf("GET body after PUT = %+v, %v; want %+v", got, err, want)
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/settings/layout",
		strings.NewReader(`not json`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed PUT status = %d, want 400", rec.Code)
	}
	stored, found, err = repo.GetLayout(context.Background())
	if err != nil || !found || !layoutsEqual(stored, want) {
		t.Fatalf("malformed PUT mutated state: %+v, found=%v, %v", stored, found, err)
	}
}

func layoutsEqual(a, b Layout) bool {
	if len(a.Expanded) != len(b.Expanded) || len(a.Projects) != len(b.Projects) || len(a.Sessions) != len(b.Sessions) {
		return false
	}
	for i := range a.Expanded {
		if a.Expanded[i] != b.Expanded[i] {
			return false
		}
	}
	for i := range a.Projects {
		if a.Projects[i] != b.Projects[i] {
			return false
		}
	}
	for k, av := range a.Sessions {
		bv, ok := b.Sessions[k]
		if !ok || len(av) != len(bv) {
			return false
		}
		for i := range av {
			if av[i] != bv[i] {
				return false
			}
		}
	}
	if len(a.Colors) != len(b.Colors) {
		return false
	}
	for k, av := range a.Colors {
		if bv, ok := b.Colors[k]; !ok || av != bv {
			return false
		}
	}
	return true
}
