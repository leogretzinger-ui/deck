package tasks

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/noisefloor-oss/deck/internal/db"
	"github.com/noisefloor-oss/deck/internal/project"
)

// An uninitialized store has no task state and no feed: the events stream
// is the distinct 404 the panel hides on, not an mtime watch.
func TestEventsRefusesUninitializedStore(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	d, err := db.Open(ctx, db.Config{Path: filepath.Join(t.TempDir(), "control.db")})
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	repo := project.NewRepository(d)
	p, _, err := repo.Register(ctx, "demo", dir, []string{dir}, project.GitIdentity{Name: "Test Author", Email: "test@example.invalid"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	h := NewHandler(repo, writeStub(t, ""), zerolog.Nop())
	mux := http.NewServeMux()
	h.Mount(mux.Handle, func(next http.Handler) http.Handler { return next })
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	resp, err := http.Get(server.URL + "/api/projects/" + p.ID + "/taskmaster/events")
	if err != nil {
		t.Fatalf("events get: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("events status = %d, want 404", resp.StatusCode)
	}
}

func TestEventsUsesLiveFeed(t *testing.T) {
	ctx := context.Background()
	dir := liveReadProject(t)
	d, err := db.Open(ctx, db.Config{Path: filepath.Join(t.TempDir(), "control.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	repo := project.NewRepository(d)
	p, _, err := repo.Register(ctx, "demo", dir, []string{dir}, project.GitIdentity{Name: "Test Author", Email: "test@example.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	stub := writeStub(t, `
if [ "$1" = tasks ] && [ "$2" = live ] && [ "$3" = feed ] && [ "$4" = --since ] && [ "$6" = --json ]; then
  seq=1
  if [ -f "$PWD/.feed-change" ]; then seq=2; fi
  if [ "$5" -ge "$seq" ]; then
    printf '{"events":[],"oldestRetainedSeq":1,"truncated":false}\n'
  else
    printf '{"events":[{"seq":%s}],"oldestRetainedSeq":1,"truncated":false}\n' "$seq"
  fi
  exit 0
fi
exit 29
`)
	h := NewHandler(repo, stub, zerolog.Nop())
	mux := http.NewServeMux()
	h.Mount(mux.Handle, func(next http.Handler) http.Handler { return next })
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	resp, err := http.Get(server.URL + "/api/projects/" + p.ID + "/taskmaster/events")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	if err := os.WriteFile(filepath.Join(dir, ".feed-change"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	r := bufio.NewReader(resp.Body)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(line, "data:") && strings.Contains(line, `"sequence":2`) {
			return
		}
	}
	t.Fatal("no live feed event within the deadline")
}
