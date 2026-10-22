package session

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/noisefloor-oss/deck/internal/db"
	"github.com/noisefloor-oss/deck/internal/project"
	"github.com/noisefloor-oss/deck/internal/provider"
)

// newUsageEnv writes a launch history by hand: the aggregate reads recorded
// rows, so the rows are the fixture. Two projects, one provider used in both,
// an ended and an interrupted session that are still launches, and a removed
// one that is not.
func newUsageEnv(t *testing.T) *Repository {
	t.Helper()
	d, err := db.Open(context.Background(), db.Config{Path: filepath.Join(t.TempDir(), "control.db")})
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	at := func(minutes int) string {
		return db.FormatTime(time.Date(2026, 9, 1, 12, minutes, 0, 0, time.UTC))
	}
	err = d.WithTx(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		for _, id := range []string{"p1", "p2"} {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO projects (id, name, canonical_path, created_at, last_used_at)
				VALUES (?, ?, ?, ?, ?)`, id, id, t.TempDir(), at(0), at(0)); err != nil {
				return err
			}
		}
		rows := []struct {
			id, projectID, prov, status, created string
			removed                              any
		}{
			{"s1", "p1", "claude", "starting", at(1), nil},
			{"s2", "p1", "claude", "exited", at(2), nil},
			{"s3", "p1", "claude", "interrupted", at(3), nil},
			{"s4", "p1", "codex", "exited", at(4), nil},
			{"s5", "p2", "claude", "starting", at(5), nil},
			// Removed rows survive for the audit trail and are not launches
			// the user still has.
			{"s6", "p1", "codex", "exited", at(6), at(7)},
			// An image variant that shipped a harness this one does not.
			{"s7", "p1", "opencode", "exited", at(8), nil},
		}
		for _, row := range rows {
			// An exited row owes the schema its exit evidence.
			var exitedAt, exitCode any
			if row.status == "exited" {
				exitedAt, exitCode = row.created, 0
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO sessions (id, project_id, provider, tmux_socket_path,
					tmux_session_name, status, exited_at, exit_code,
					created_at, updated_at, removed_at)
				VALUES (?, ?, ?, '/run/s', 'noisedeck-'||?, ?, ?, ?, ?, ?, ?)`,
				row.id, row.projectID, row.prov, row.id, row.status,
				exitedAt, exitCode,
				row.created, row.created, row.removed); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed usage fixture: %v", err)
	}
	return NewRepository(d)
}

// One fixture serves every case: opening a migrated database costs seconds,
// and this package's budget is already spent on real tmux.
func TestHarnessUsage(t *testing.T) {
	repo := newUsageEnv(t)
	mux := usageHandler(t, repo)
	t.Run("counts every launch that was not removed", func(t *testing.T) {
		usageCountsEveryLaunch(t, repo)
	})
	t.Run("scopes to one project", func(t *testing.T) { usageScopesToOneProject(t, repo) })
	t.Run("reports no history as empty", func(t *testing.T) { usageEmptyForUnknownProject(t, repo) })
	t.Run("serves both scopes, launchable harnesses only", func(t *testing.T) {
		usageServesBothScopes(t, mux)
	})
	t.Run("without a project the project scope is empty", func(t *testing.T) {
		usageEmptyProjectScope(t, mux)
	})
	t.Run("carries aggregate usage and nothing else", func(t *testing.T) {
		usageCarriesAggregateOnly(t, mux)
	})
}

func usageCountsEveryLaunch(t *testing.T, repo *Repository) {
	usage, err := repo.UsageByProvider(context.Background(), "")
	if err != nil {
		t.Fatalf("global usage: %v", err)
	}
	want := map[string]int{"claude": 4, "codex": 1, "opencode": 1}
	if len(usage) != len(want) {
		t.Fatalf("global usage = %+v, want %d providers", usage, len(want))
	}
	for _, one := range usage {
		if one.Launches != want[one.Provider] {
			t.Errorf("%s launches = %d, want %d", one.Provider, one.Launches, want[one.Provider])
		}
	}
	// claude's newest surviving launch is s5 at 12:05; the removed codex row
	// at 12:06 must not become codex's last use either.
	for _, one := range usage {
		switch one.Provider {
		case "claude":
			if got := one.LastUsedAt.UTC().Minute(); got != 5 {
				t.Errorf("claude last used at minute %d, want 5", got)
			}
		case "codex":
			if got := one.LastUsedAt.UTC().Minute(); got != 4 {
				t.Errorf("codex last used at minute %d, want 4 (the removed row is not a launch)", got)
			}
		}
	}
}

func usageScopesToOneProject(t *testing.T, repo *Repository) {
	usage, err := repo.UsageByProvider(context.Background(), "p2")
	if err != nil {
		t.Fatalf("project usage: %v", err)
	}
	if len(usage) != 1 || usage[0].Provider != "claude" || usage[0].Launches != 1 {
		t.Fatalf("p2 usage = %+v, want one claude launch", usage)
	}
}

func usageEmptyForUnknownProject(t *testing.T, repo *Repository) {
	usage, err := repo.UsageByProvider(context.Background(), "no-such-project")
	if err != nil {
		t.Fatalf("unknown project usage: %v", err)
	}
	if len(usage) != 0 {
		t.Fatalf("unknown project usage = %+v, want empty", usage)
	}
}

// usageHandler wires the endpoint over an image that ships claude and codex
// and not opencode, so the registry filter has something to drop.
func usageHandler(t *testing.T, repo *Repository) http.Handler {
	t.Helper()
	registry := map[provider.Name]provider.Descriptor{
		provider.Claude: nil,
		provider.Codex:  nil,
	}
	h := NewHandler(NewManager(repo, nil, registry, "noisedeck"),
		repo, project.NewRepository(repo.db), zerolog.Nop())
	mux := http.NewServeMux()
	h.Mount(mux.Handle, func(next http.Handler) http.Handler { return next })
	return mux
}

type usageResponse struct {
	Global  []HarnessUsage `json:"global"`
	Project []HarnessUsage `json:"project"`
}

func getUsage(t *testing.T, mux http.Handler, path string) (usageResponse, []byte) {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200: %s", path, rec.Code, rec.Body.String())
	}
	var body usageResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode usage: %v", err)
	}
	return body, rec.Body.Bytes()
}

func usageServesBothScopes(t *testing.T, mux http.Handler) {
	body, raw := getUsage(t, mux, "/api/harness-usage?project=p1")
	for _, one := range body.Global {
		if one.Provider == "opencode" {
			t.Fatalf("a harness this image cannot launch reached the response: %s", raw)
		}
	}
	if len(body.Global) != 2 {
		t.Fatalf("global = %+v, want claude and codex", body.Global)
	}
	if len(body.Project) != 2 {
		t.Fatalf("project scope = %+v, want claude and codex", body.Project)
	}
	if body.Project[0].Provider != "claude" || body.Project[0].Launches != 3 {
		t.Errorf("p1 claude = %+v, want 3 launches", body.Project[0])
	}
	if body.Global[0].Launches != 4 {
		t.Errorf("global claude = %+v, want 4 launches", body.Global[0])
	}
}

func usageEmptyProjectScope(t *testing.T, mux http.Handler) {
	body, raw := getUsage(t, mux, "/api/harness-usage")
	if body.Project == nil || len(body.Project) != 0 {
		t.Fatalf("project scope = %+v, want an empty list", body.Project)
	}
	if !bytes.Contains(raw, []byte(`"project":[]`)) {
		t.Fatalf("empty scope must serialize as [], not null: %s", raw)
	}
}

func usageCarriesAggregateOnly(t *testing.T, mux http.Handler) {
	_, raw := getUsage(t, mux, "/api/harness-usage?project=p1")
	var decoded map[string][]map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode usage: %v", err)
	}
	for scope, entries := range decoded {
		for _, entry := range entries {
			for key := range entry {
				switch key {
				case "provider", "launches", "last_used_at":
				default:
					t.Errorf("%s entry carries %q; the endpoint reports aggregate usage only", scope, key)
				}
			}
		}
	}
}
