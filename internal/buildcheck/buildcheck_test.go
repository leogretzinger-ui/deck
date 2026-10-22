package buildcheck_test

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	_ "github.com/mattn/go-sqlite3"
	"github.com/rs/zerolog"
)

// TestSQLiteDriverWorks proves the cgo SQLite driver compiles and runs, and
// that the settings the control database relies on actually take effect — not
// merely that the statements are accepted.
//
// The database is file-backed under t.TempDir(): an in-memory database silently
// reports journal_mode "memory" and can never enter WAL, so it cannot establish
// this. The settings are supplied as DSN parameters rather than one-off
// statements because database/sql pools connections; a PRAGMA executed on one
// connection would not apply to the others. This is the pattern internal/db
// must use.
func TestSQLiteDriverWorks(t *testing.T) {
	dsn := "file:" + filepath.Join(t.TempDir(), "control.db") +
		"?_journal_mode=WAL&_synchronous=FULL&_busy_timeout=5000&_foreign_keys=ON"

	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := db.Ping(); err != nil {
		t.Fatalf("ping: %v", err)
	}

	// Read back the effective values. synchronous=FULL is 2 in SQLite's
	// encoding; foreign_keys is a boolean; busy_timeout is milliseconds.
	checks := []struct {
		name  string
		query string
		want  string
	}{
		{"journal_mode", "PRAGMA journal_mode", "wal"},
		{"synchronous", "PRAGMA synchronous", "2"},
		{"busy_timeout", "PRAGMA busy_timeout", "5000"},
		{"foreign_keys", "PRAGMA foreign_keys", "1"},
	}
	for _, c := range checks {
		var got string
		if err := db.QueryRow(c.query).Scan(&got); err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if !strings.EqualFold(got, c.want) {
			t.Errorf("%s = %q, want %q", c.name, got, c.want)
		}
	}

	// A trivial round trip, so the driver is exercised beyond pragmas.
	if _, err := db.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT NOT NULL)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO t (v) VALUES (?)`, "x"); err != nil {
		t.Fatalf("insert: %v", err)
	}
	var v string
	if err := db.QueryRow(`SELECT v FROM t WHERE id = 1`).Scan(&v); err != nil {
		t.Fatalf("select: %v", err)
	}
	if v != "x" {
		t.Errorf("round trip = %q, want %q", v, "x")
	}
}

// TestZerologEmitsStructuredJSON proves the logger produces the structured,
// machine-readable records lifecycle logging needs: one JSON object per event,
// correlated by application session id.
func TestZerologEmitsStructuredJSON(t *testing.T) {
	var buf bytes.Buffer
	logger := zerolog.New(&buf)
	logger.Info().Str("session_id", "s-1").Msg("reconciled")

	var record map[string]any
	if err := json.Unmarshal(buf.Bytes(), &record); err != nil {
		t.Fatalf("log line is not valid JSON: %v (line=%q)", err, buf.String())
	}
	if got := record["session_id"]; got != "s-1" {
		t.Errorf("session_id = %v, want s-1", got)
	}
	if got := record["message"]; got != "reconciled" {
		t.Errorf("message = %v, want reconciled", got)
	}
}

// TestWebSocketUpgraderRejectsForeignOrigin proves the WebSocket dependency is
// usable and that CheckOrigin is the hook where a WebSocket's Origin is
// strictly validated. This tests our own policy function, not library behavior.
func TestWebSocketUpgraderRejectsForeignOrigin(t *testing.T) {
	const allowed = "http://127.0.0.1:8837"

	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return r.Header.Get("Origin") == allowed
		},
	}

	cases := []struct {
		origin string
		want   bool
	}{
		{allowed, true},
		{"http://evil.example", false},
		{"", false},
	}
	for _, tc := range cases {
		req := httptestRequest(t, tc.origin)
		if got := upgrader.CheckOrigin(req); got != tc.want {
			t.Errorf("CheckOrigin(origin=%q) = %v, want %v", tc.origin, got, tc.want)
		}
	}
}

func httptestRequest(t *testing.T, origin string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:8837/ws", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	return req
}
