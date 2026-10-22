package apppreview

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"sync"

	"github.com/noisefloor-oss/deck/internal/db"
)

var (
	ErrPreviewProjectNotFound = errors.New("preview project not found")
	ErrPreviewStatic          = errors.New("preview is managed by the installation owner")
	ErrPreviewUnconfigured    = errors.New("self-service previews are not configured")
	ErrPreviewPortsExhausted  = errors.New("no preview ports are available")
)

type RegistryConfig struct {
	LoopbackPort   int
	BaseDomain     string
	HostnamePrefix string
	PortMin        int
	PortMax        int
	StaticTargets  []Target
}

type Registration struct {
	ProjectID string `json:"project_id"`
	Origin    string `json:"origin"`
	Upstream  string `json:"upstream"`
	Source    string `json:"source"`
	Port      int    `json:"port"`
	Enabled   bool   `json:"enabled"`
}

// Registry serializes durable allocations with gateway changes. Port ownership
// is a reservation among Deck registrations, not isolation from host processes.
type Registry struct {
	mu     sync.Mutex
	db     *db.DB
	g      *Gateway
	cfg    RegistryConfig
	static map[string]Registration
}

func NewRegistry(d *db.DB, g *Gateway, cfg RegistryConfig) (*Registry, error) {
	if d == nil || g == nil {
		return nil, errors.New("apppreview: registry needs a database and gateway")
	}
	if cfg.BaseDomain == "" && !g.loopback {
		if cfg.PortMin != 0 || cfg.PortMax != 0 {
			return nil, ErrPreviewUnconfigured
		}
	} else if cfg.PortMin < 1024 || cfg.PortMax > 65535 || cfg.PortMax < cfg.PortMin || cfg.PortMax-cfg.PortMin+1 > 1024 {
		return nil, errors.New("apppreview: invalid allocation port range")
	}
	if g.loopback && (cfg.BaseDomain != "" || cfg.HostnamePrefix != "" || len(cfg.StaticTargets) != 0 || cfg.LoopbackPort < 1 || cfg.LoopbackPort > 65535 || cfg.PortMax-cfg.PortMin+1 > 254 || (cfg.LoopbackPort >= cfg.PortMin && cfg.LoopbackPort <= cfg.PortMax)) {
		return nil, errors.New("apppreview: invalid loopback allocation configuration")
	}
	r := &Registry{db: d, g: g, cfg: cfg, static: make(map[string]Registration)}
	static, err := buildTargets(Config{Targets: cfg.StaticTargets, DeckOrigins: g.deckOrigins, Loopback: g.loopback})
	if err != nil {
		return nil, err
	}
	for _, t := range static {
		_, port, _ := net.SplitHostPort(t.upstream)
		n, _ := strconv.Atoi(port)
		if cfg.BaseDomain != "" && n >= cfg.PortMin && n <= cfg.PortMax {
			return nil, errors.New("apppreview: allocation pool overlaps a static target")
		}
		r.static[t.projectID] = Registration{ProjectID: t.projectID, Origin: t.originStr, Upstream: t.upstream, Source: "static", Port: n, Enabled: true}
	}
	return r, nil
}

func (r *Registry) managedOrigin(projectID string, port int) string {
	if r.g.loopback {
		return fmt.Sprintf("http://127.0.0.%d:%d", 2+port-r.cfg.PortMin, r.cfg.LoopbackPort)
	}
	hash := sha256.Sum256([]byte(projectID))
	prefix := r.cfg.HostnamePrefix
	if prefix == "" {
		prefix = "p-"
	}
	return fmt.Sprintf("https://%s%x.%s", prefix, hash[:10], r.cfg.BaseDomain)
}

func managedRegistration(projectID, origin string, port int, enabled bool) Registration {
	return Registration{ProjectID: projectID, Origin: origin, Upstream: net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), Port: port, Enabled: enabled, Source: "managed"}
}

func (r *Registry) validateStored(reg Registration) error {
	if (r.cfg.BaseDomain == "" && !r.g.loopback) || reg.Origin != r.managedOrigin(reg.ProjectID, reg.Port) || reg.Port < r.cfg.PortMin || reg.Port > r.cfg.PortMax {
		return fmt.Errorf("apppreview: persisted allocation for project %q conflicts with the configured preview origin or port range", reg.ProjectID)
	}
	origin, _ := url.Parse(reg.Origin)
	for _, s := range r.static {
		staticOrigin, _ := url.Parse(s.Origin)
		if s.ProjectID == reg.ProjectID || s.Port == reg.Port || staticOrigin.Hostname() == origin.Hostname() {
			return fmt.Errorf("apppreview: persisted allocation for project %q conflicts with a static registration", reg.ProjectID)
		}
	}
	return nil
}

// Restore validates every retained reservation before making any route live.
// A domain or pool change must not silently move an existing project's app.
func (r *Registry) Restore(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rows, err := r.db.Reader().QueryContext(ctx, `SELECT v.project_id, v.origin, v.port, v.enabled, p.removed_at IS NULL
		FROM project_preview_registrations v JOIN projects p ON p.id = v.project_id`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	var active []Registration
	for rows.Next() {
		var id, origin string
		var port int
		var enabled, present bool
		if err := rows.Scan(&id, &origin, &port, &enabled, &present); err != nil {
			_ = rows.Close()
			return err
		}
		reg := managedRegistration(id, origin, port, enabled)
		if err := r.validateStored(reg); err != nil {
			_ = rows.Close()
			return err
		}
		if enabled && present {
			active = append(active, reg)
		}
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	for id := range r.static {
		var removed bool
		err := r.db.Reader().QueryRowContext(ctx, `SELECT removed_at IS NOT NULL FROM projects WHERE id = ?`, id).Scan(&removed)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if removed {
			r.g.Unregister(id)
		}
	}
	var restored []string
	for _, reg := range active {
		if err := r.g.Register(Target{ProjectID: reg.ProjectID, Origin: reg.Origin, Upstream: reg.Upstream}); err != nil {
			for _, id := range restored {
				r.g.Unregister(id)
			}
			return err
		}
		restored = append(restored, reg.ProjectID)
	}
	return nil
}

type rowQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func activePreviewProject(ctx context.Context, q rowQuerier, id string) error {
	var present int
	err := q.QueryRowContext(ctx, `SELECT 1 FROM projects WHERE id = ? AND removed_at IS NULL`, id).Scan(&present)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrPreviewProjectNotFound
	}
	return err
}

func readRegistration(ctx context.Context, q rowQuerier, id string) (*Registration, error) {
	var origin string
	var port int
	var enabled bool
	err := q.QueryRowContext(ctx, `SELECT origin, port, enabled FROM project_preview_registrations WHERE project_id = ?`, id).Scan(&origin, &port, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	reg := managedRegistration(id, origin, port, enabled)
	return &reg, nil
}

func (r *Registry) Get(ctx context.Context, id string) (*Registration, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := activePreviewProject(ctx, r.db.Reader(), id); err != nil {
		return nil, err
	}
	if reg, ok := r.static[id]; ok {
		reg.Enabled = r.g.HasProject(id)
		return &reg, nil
	}
	reg, err := readRegistration(ctx, r.db.Reader(), id)
	if reg != nil {
		reg.Enabled = reg.Enabled && r.g.HasProject(id)
	}
	return reg, err
}

func (r *Registry) Enable(ctx context.Context, id string) (*Registration, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var reg *Registration
	newlyRegistered := false
	err := r.db.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if err := activePreviewProject(ctx, tx, id); err != nil {
			return err
		}
		if _, ok := r.static[id]; ok {
			return ErrPreviewStatic
		}
		if r.cfg.BaseDomain == "" && !r.g.loopback {
			return ErrPreviewUnconfigured
		}
		var err error
		reg, err = readRegistration(ctx, tx, id)
		if err != nil {
			return err
		}
		changed := reg == nil || !reg.Enabled
		if reg == nil {
			port, err := r.allocatePort(ctx, tx)
			if err != nil {
				return err
			}
			value := managedRegistration(id, r.managedOrigin(id, port), port, true)
			reg = &value
			if err := r.validateStored(*reg); err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO project_preview_registrations (project_id, origin, port, enabled) VALUES (?, ?, ?, 1)`, id, reg.Origin, reg.Port)
			if err != nil {
				return err
			}
		} else {
			if err := r.validateStored(*reg); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE project_preview_registrations SET enabled = 1 WHERE project_id = ?`, id); err != nil {
				return err
			}
			reg.Enabled = true
		}
		if changed {
			if err := previewAudit(ctx, tx, id, "project.preview_enabled"); err != nil {
				return err
			}
		}
		existing := r.g.HasProject(id)
		if err := r.g.Register(Target{ProjectID: id, Origin: reg.Origin, Upstream: reg.Upstream}); err != nil {
			return err
		}
		newlyRegistered = !existing
		return nil
	})
	if err != nil {
		if newlyRegistered {
			r.g.Unregister(id)
		}
		return nil, err
	}
	return reg, nil
}

func (r *Registry) allocatePort(ctx context.Context, tx *sql.Tx) (int, error) {
	rows, err := tx.QueryContext(ctx, `SELECT port FROM project_preview_registrations`)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()
	used := make(map[int]bool)
	for rows.Next() {
		var port int
		if err := rows.Scan(&port); err != nil {
			_ = rows.Close()
			return 0, err
		}
		used[port] = true
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return 0, err
	}
	for port := r.cfg.PortMin; port <= r.cfg.PortMax; port++ {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if used[port] {
			continue
		}
		listener, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err != nil {
			continue
		}
		_ = listener.Close()
		return port, nil
	}
	return 0, ErrPreviewPortsExhausted
}

func (r *Registry) Disable(ctx context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.static[id]; ok {
		return ErrPreviewStatic
	}
	// Revocation must succeed even if the control database has become unwritable.
	r.g.Unregister(id)
	return r.db.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if err := activePreviewProject(ctx, tx, id); err != nil {
			return err
		}
		if r.cfg.BaseDomain == "" && !r.g.loopback {
			return ErrPreviewUnconfigured
		}
		return disableRegistration(ctx, tx, id)
	})
}

// ProjectRemoved follows the project's committed soft removal. Keeping its
// allocation prevents a surviving dev server from becoming a sibling preview.
func (r *Registry) ProjectRemoved(ctx context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.g.Unregister(id)
	return r.db.WithTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		return disableRegistration(ctx, tx, id)
	})
}

func disableRegistration(ctx context.Context, tx *sql.Tx, id string) error {
	res, err := tx.ExecContext(ctx, `UPDATE project_preview_registrations SET enabled = 0 WHERE project_id = ? AND enabled = 1`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return err
	}
	return previewAudit(ctx, tx, id, "project.preview_disabled")
}

func previewAudit(ctx context.Context, tx *sql.Tx, id, event string) error {
	details, err := json.Marshal(map[string]string{"project_id": id})
	if err != nil {
		return err
	}
	return db.AppendAudit(ctx, tx, db.AuditEvent{Type: event, Details: string(details)})
}
