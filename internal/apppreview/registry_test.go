package apppreview

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/noisefloor-oss/deck/internal/db"
)

func registryDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(context.Background(), db.Config{Path: filepath.Join(t.TempDir(), "control.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func registrySQL(t *testing.T, d *db.DB, query string, args ...any) {
	t.Helper()
	if err := d.WithTx(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, query, args...)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func registryProject(t *testing.T, d *db.DB, id string) {
	t.Helper()
	registrySQL(t, d, `INSERT INTO projects (id, name, canonical_path, created_at, last_used_at) VALUES (?, ?, ?, '2026-01-01', '2026-01-01')`, id, id, "/sample/"+id)
}

func registryPool(t *testing.T, size int) (int, int) {
	t.Helper()
	for attempts := 0; attempts < 100; attempts++ {
		l, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := l.Addr().(*net.TCPAddr).Port
		_ = l.Close()
		if port+size-1 > 65535 {
			continue
		}
		var listeners []net.Listener
		for i := 0; i < size; i++ {
			l, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port+i)))
			if err != nil {
				break
			}
			listeners = append(listeners, l)
		}
		for _, l := range listeners {
			_ = l.Close()
		}
		if len(listeners) == size {
			return port, port + size - 1
		}
	}
	t.Fatal("could not find a temporary port pool")
	return 0, 0
}

func registryFixture(t *testing.T, size int) (*Registry, *db.DB, *Gateway) {
	t.Helper()
	d := registryDB(t)
	g, _ := newTestGateway(t, testDeckOrigin)
	min, max := registryPool(t, size)
	r, err := NewRegistry(d, g, RegistryConfig{BaseDomain: "preview.example.net", PortMin: min, PortMax: max})
	if err != nil {
		t.Fatal(err)
	}
	return r, d, g
}

func TestRegistryAllocationSurvivesDisableAndRestart(t *testing.T) {
	r, d, g := registryFixture(t, 2)
	ctx := context.Background()
	registryProject(t, d, "a")
	registryProject(t, d, "b")
	if reg, err := r.Get(ctx, "a"); err != nil || reg != nil {
		t.Fatalf("unregistered = %+v, %v", reg, err)
	}
	a, err := r.Enable(ctx, "a")
	if err != nil || !g.HasProject("a") || !a.Enabled {
		t.Fatalf("enable = %+v, %v", a, err)
	}
	if err := r.Disable(ctx, "a"); err != nil || g.HasProject("a") {
		t.Fatalf("disable: %v", err)
	}
	got, err := r.Get(ctx, "a")
	if err != nil || got.Enabled || got.Origin != a.Origin || got.Port != a.Port {
		t.Fatalf("disabled allocation = %+v, %v", got, err)
	}
	b, err := r.Enable(ctx, "b")
	if err != nil || b.Port == a.Port || b.Origin == a.Origin {
		t.Fatalf("second allocation = %+v, %v", b, err)
	}
	a2, err := r.Enable(ctx, "a")
	if err != nil || *a2 != *a {
		t.Fatalf("reenable moved allocation: %+v -> %+v, %v", a, a2, err)
	}
	if _, err := r.Enable(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	g.Close()
	g2, _ := newTestGateway(t, testDeckOrigin)
	r2, err := NewRegistry(d, g2, r.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := r2.Restore(ctx); err != nil || !g2.HasProject("a") || !g2.HasProject("b") {
		t.Fatalf("restore: %v", err)
	}
	got, err = r2.Get(ctx, "a")
	if err != nil || *got != *a {
		t.Fatalf("restart moved allocation: %+v, %v", got, err)
	}
}

func TestRegistryInstallationHostnamePrefix(t *testing.T) {
	r, d, g := registryFixture(t, 2)
	ctx := context.Background()
	registryProject(t, d, "same-project")
	defaultOrigin := r.managedOrigin("same-project", 0)
	if !strings.HasPrefix(defaultOrigin, "https://p-") {
		t.Fatalf("default hostname changed: %s", defaultOrigin)
	}
	r.cfg.HostnamePrefix = "main-p-"
	reg, err := r.Enable(ctx, "same-project")
	if err != nil {
		t.Fatal(err)
	}
	if reg.Origin == defaultOrigin || reg.Origin != strings.Replace(defaultOrigin, "https://p-", "https://main-p-", 1) {
		t.Fatalf("prefix changed more than the label namespace: %s", reg.Origin)
	}
	if err := r.Disable(ctx, reg.ProjectID); err != nil {
		t.Fatal(err)
	}
	reg2, err := r.Enable(ctx, reg.ProjectID)
	if err != nil || *reg2 != *reg {
		t.Fatalf("reenable moved prefixed allocation: %+v, %v", reg2, err)
	}
	g.Close()
	g2, _ := newTestGateway(t, testDeckOrigin)
	r2, err := NewRegistry(d, g2, r.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := r2.Restore(ctx); err != nil || !g2.HasProject(reg.ProjectID) {
		t.Fatalf("restore prefixed allocation: %v", err)
	}
	if err := r2.Disable(ctx, reg.ProjectID); err != nil {
		t.Fatal(err)
	}
	r2.cfg.HostnamePrefix = "alt-p-"
	if err := r2.Restore(ctx); err == nil {
		t.Fatal("changed prefix accepted a retained disabled allocation")
	}
	if _, err := r2.Enable(ctx, reg.ProjectID); err == nil {
		t.Fatal("changed prefix moved a retained allocation")
	}
	if g2.HasProject(reg.ProjectID) {
		t.Fatal("failed restore activated a route")
	}
}

func TestRegistryConcurrentEnablesReserveDistinctPorts(t *testing.T) {
	r, d, _ := registryFixture(t, 32)
	const count = 12
	for i := 0; i < count; i++ {
		registryProject(t, d, fmt.Sprint(i))
	}
	var wg sync.WaitGroup
	results := make(chan *Registration, count*2)
	for i := 0; i < count*2; i++ {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			reg, err := r.Enable(context.Background(), id)
			if err != nil {
				t.Errorf("enable %s: %v", id, err)
				return
			}
			results <- reg
		}(fmt.Sprint(i % count))
	}
	wg.Wait()
	close(results)
	ports := map[int]string{}
	projects := map[string]int{}
	for reg := range results {
		if id, ok := ports[reg.Port]; ok && id != reg.ProjectID {
			t.Fatalf("port %d shared by %s and %s", reg.Port, id, reg.ProjectID)
		}
		if port, ok := projects[reg.ProjectID]; ok && port != reg.Port {
			t.Fatalf("project %s received ports %d and %d", reg.ProjectID, port, reg.Port)
		}
		ports[reg.Port], projects[reg.ProjectID] = reg.ProjectID, reg.Port
	}
	if len(projects) != count {
		t.Fatalf("allocated %d projects, want %d", len(projects), count)
	}
}

func TestRegistrySkipsOccupiedPortsAndRetainsRemovedAllocation(t *testing.T) {
	r, d, g := registryFixture(t, 2)
	ctx := context.Background()
	occupied, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(r.cfg.PortMin)))
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	registryProject(t, d, "a")
	registryProject(t, d, "b")
	a, err := r.Enable(ctx, "a")
	if err != nil || a.Port != r.cfg.PortMax {
		t.Fatalf("occupied port allocated: %+v, %v", a, err)
	}
	registrySQL(t, d, `UPDATE projects SET removed_at = '2026-01-02' WHERE id = 'a'`)
	if err := r.ProjectRemoved(ctx, "a"); err != nil || g.HasProject("a") {
		t.Fatalf("remove: %v", err)
	}
	if _, err := r.Enable(ctx, "a"); !errors.Is(err, ErrPreviewProjectNotFound) {
		t.Fatalf("removed project enabled: %v", err)
	}
	if _, err := r.Enable(ctx, "b"); !errors.Is(err, ErrPreviewPortsExhausted) {
		t.Fatalf("removed project's port reused: %v", err)
	}
	// Even a removal whose callback never ran must not restore an enabled route.
	registrySQL(t, d, `UPDATE project_preview_registrations SET enabled = 1 WHERE project_id = 'a'`)
	g2, _ := newTestGateway(t, testDeckOrigin)
	r2, _ := NewRegistry(d, g2, r.cfg)
	if err := r2.Restore(ctx); err != nil || g2.HasProject("a") {
		t.Fatalf("restored removed project: %v", err)
	}
}

func TestRegistryConfigurationDriftRefusesRestore(t *testing.T) {
	r, d, _ := registryFixture(t, 3)
	registryProject(t, d, "a")
	a, err := r.Enable(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Disable(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*RegistryConfig){
		"domain": func(c *RegistryConfig) { c.BaseDomain = "other.example.org" },
		"range":  func(c *RegistryConfig) { c.PortMin = a.Port + 1 },
		"static project": func(c *RegistryConfig) {
			c.StaticTargets = []Target{{ProjectID: "a", Origin: "https://a.example.org", Upstream: "127.0.0.1:1024"}}
		},
		"static hostname": func(c *RegistryConfig) {
			c.StaticTargets = []Target{{ProjectID: "b", Origin: a.Origin + ":8443", Upstream: "127.0.0.1:1024"}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := r.cfg
			change(&cfg)
			g, _ := newTestGateway(t, testDeckOrigin)
			next, err := NewRegistry(d, g, cfg)
			if err == nil {
				err = next.Restore(context.Background())
			}
			if err == nil || g.HasProject("a") {
				t.Fatalf("configuration drift accepted: %v", err)
			}
		})
	}
}

func TestRegistryStaticTargetsAreReadOnly(t *testing.T) {
	d := registryDB(t)
	registryProject(t, d, "a")
	target := Target{ProjectID: "a", Origin: "https://a.example.net", Upstream: "127.0.0.1:5173"}
	g, _ := newTestGateway(t, testDeckOrigin, target)
	r, err := NewRegistry(d, g, RegistryConfig{StaticTargets: []Target{target}})
	if err != nil {
		t.Fatal(err)
	}
	reg, err := r.Get(context.Background(), "a")
	if err != nil || reg.Source != "static" || !reg.Enabled {
		t.Fatalf("static GET = %+v, %v", reg, err)
	}
	if _, err := r.Enable(context.Background(), "a"); !errors.Is(err, ErrPreviewStatic) {
		t.Fatalf("static enable: %v", err)
	}
	if err := r.Disable(context.Background(), "a"); !errors.Is(err, ErrPreviewStatic) || !g.HasProject("a") {
		t.Fatalf("static disable: %v", err)
	}
	registryProject(t, d, "b")
	if _, err := r.Enable(context.Background(), "b"); !errors.Is(err, ErrPreviewUnconfigured) {
		t.Fatalf("unconfigured enable: %v", err)
	}
	if err := r.Disable(context.Background(), "b"); !errors.Is(err, ErrPreviewUnconfigured) {
		t.Fatalf("unconfigured disable: %v", err)
	}
	registrySQL(t, d, `UPDATE projects SET removed_at = '2026-01-02' WHERE id = 'a'`)
	if err := r.Restore(context.Background()); err != nil || g.HasProject("a") {
		t.Fatalf("restored removed static project: %v", err)
	}
}

func TestRegistryGatewayFailureDoesNotReservePort(t *testing.T) {
	r, d, g := registryFixture(t, 1)
	registryProject(t, d, "a")
	g.Close()
	if _, err := r.Enable(context.Background(), "a"); err == nil {
		t.Fatal("closed gateway accepted enable")
	}
	reg, err := r.Get(context.Background(), "a")
	if err != nil || reg != nil {
		t.Fatalf("gateway failure retained reservation: %+v, %v", reg, err)
	}
}

func TestRegistryDisableRevokesEvenWhenWriteFails(t *testing.T) {
	r, d, g := registryFixture(t, 1)
	registryProject(t, d, "a")
	if _, err := r.Enable(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	registrySQL(t, d, `CREATE TRIGGER fail_preview_write BEFORE UPDATE ON project_preview_registrations BEGIN SELECT RAISE(ABORT, 'test write failure'); END`)
	if err := r.Disable(context.Background(), "a"); err == nil || g.HasProject("a") {
		t.Fatalf("write failure did not revoke: %v", err)
	}
	reg, err := r.Get(context.Background(), "a")
	if err != nil || reg.Enabled {
		t.Fatalf("GET reported live route after failed disable: %+v, %v", reg, err)
	}
}

func TestRegistryCommitFailureRevokesNewRoute(t *testing.T) {
	r, d, g := registryFixture(t, 1)
	registryProject(t, d, "a")
	registrySQL(t, d, `CREATE TABLE fail_commit (project_id TEXT REFERENCES projects(id) DEFERRABLE INITIALLY DEFERRED)`)
	registrySQL(t, d, `CREATE TRIGGER fail_preview_commit AFTER INSERT ON project_preview_registrations BEGIN INSERT INTO fail_commit VALUES ('missing'); END`)
	if _, err := r.Enable(context.Background(), "a"); err == nil || g.HasProject("a") {
		t.Fatalf("commit failure left route active: %v", err)
	}
	reg, err := r.Get(context.Background(), "a")
	if err != nil || reg != nil {
		t.Fatalf("failed allocation persisted: %+v, %v", reg, err)
	}
}

func TestRegistryIdempotentEnableCommitFailurePreservesSession(t *testing.T) {
	r, d, g := registryFixture(t, 1)
	registryProject(t, d, "a")
	reg, err := r.Enable(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	origin, err := url.Parse(reg.Origin)
	if err != nil {
		t.Fatal(err)
	}
	cookie := enterPreview(t, g, "a", origin.Host)
	registrySQL(t, d, `CREATE TABLE fail_commit (project_id TEXT REFERENCES projects(id) DEFERRABLE INITIALLY DEFERRED)`)
	registrySQL(t, d, `CREATE TRIGGER fail_preview_commit AFTER UPDATE ON project_preview_registrations BEGIN INSERT INTO fail_commit VALUES ('missing'); END`)
	if _, err := r.Enable(context.Background(), "a"); err == nil || !g.HasProject("a") {
		t.Fatalf("idempotent commit failure revoked route: %v", err)
	}
	// There is no dev server; a gateway error proves this cookie still passed
	// authorization instead of being revoked by the failed no-op operation.
	if response := authed(t, g, cookie, origin.Host, http.MethodGet, "/", nil); response.Code != http.StatusBadGateway {
		t.Fatalf("existing session was revoked: status %d", response.Code)
	}
}
