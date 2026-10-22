package boot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/noisefloor-oss/deck/internal/apppreview"
	"github.com/noisefloor-oss/deck/internal/auth"
	"github.com/noisefloor-oss/deck/internal/config"
	"github.com/noisefloor-oss/deck/internal/project"
)

func (h *Handles) mountProjectPreviews(cfg *config.Config, projects *project.Repository) error {
	if cfg.ProjectPreviews.Loopback || cfg.ProjectPreviews.Bind != "" {
		pc := apppreview.Config{DeckOrigins: []string{cfg.ProjectPreviews.DeckOrigin}, Loopback: cfg.ProjectPreviews.Loopback}
		if pc.Loopback {
			pc.DeckOrigins = cfg.Server.AllowedOrigins
		}
		for _, t := range cfg.ProjectPreviews.Targets {
			pc.Targets = append(pc.Targets, apppreview.Target{ProjectID: t.ProjectID, Origin: t.Origin, Upstream: t.Upstream})
		}
		var err error
		h.Preview, err = apppreview.New(pc)
		if err != nil {
			return fmt.Errorf("project previews: %w", err)
		}
		h.PreviewRegistry, err = apppreview.NewRegistry(h.DB, h.Preview, apppreview.RegistryConfig{
			BaseDomain:     cfg.ProjectPreviews.BaseDomain,
			LoopbackPort:   cfg.ProjectPreviews.LoopbackPort,
			HostnamePrefix: cfg.ProjectPreviews.HostnamePrefix,
			PortMin:        cfg.ProjectPreviews.PortMin, PortMax: cfg.ProjectPreviews.PortMax,
			StaticTargets: pc.Targets,
		})
		if err != nil {
			return fmt.Errorf("project preview registrations: %w", err)
		}
		if err := h.PreviewRegistry.Restore(context.Background()); err != nil {
			return fmt.Errorf("restore project previews: %w", err)
		}
		socket := cfg.ProjectPreviews.MetadataSocket
		if socket == "" {
			socket, err = apppreview.DefaultMetadataSocket()
			if err != nil {
				return fmt.Errorf("project preview metadata: %w", err)
			}
		}
		h.previewMetadata, err = apppreview.StartMetadata(socket, h.PreviewRegistry)
		if err != nil {
			return fmt.Errorf("project preview metadata: %w", err)
		}
		h.Manager.SetProjectPreviewSocket(socket)
		h.previewListeners, err = listenProjectPreviews(cfg.ProjectPreviews)
		if err != nil {
			return err
		}
		preview := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// A deployment may use a custom identity header. It is never app data.
			r.Header.Del(cfg.Auth.TrustedProxy.IdentityHeader)
			h.Preview.ServeHTTP(w, r)
		})
		h.previewHTTP = &http.Server{Handler: preview, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
		h.Auth.OnLogout(h.Preview.Revoke)
		h.Projects.OnRemoved(func(ctx context.Context, id string) {
			if err := h.PreviewRegistry.ProjectRemoved(ctx, id); err != nil {
				h.log.Error().Err(err).Msg("could not persist removal of project preview access")
			}
		})
		h.log.Warn().Msg("project app previews enabled in the unconfined workspace; dev-server processes are not isolated")
	}
	h.Server.Handle("POST /api/projects/{id}/preview", h.Auth.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		fail := func(status int, code, message string) {
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": message}})
		}
		id := r.PathValue("id")
		if h.Preview == nil || !h.Preview.HasProject(id) {
			fail(http.StatusNotFound, "preview_unavailable", "Preview is not configured for this project.")
			return
		}
		if _, err := projects.Get(r.Context(), id); err != nil {
			if errors.Is(err, project.ErrNotFound) {
				fail(http.StatusNotFound, "not_found", "Project not found.")
			} else {
				fail(http.StatusInternalServerError, "internal", "Could not read the project.")
			}
			return
		}
		entry, err := h.Preview.Issue(id, auth.Principal(r.Context()))
		if err != nil {
			fail(http.StatusServiceUnavailable, "preview_unavailable", "Preview could not be opened. Try again.")
			return
		}
		_ = json.NewEncoder(w).Encode(entry)
	})))
	h.mountProjectPreviewSettings(cfg, projects)
	return nil
}

func listenProjectPreviews(cfg config.ProjectPreviews) ([]net.Listener, error) {
	addresses := []string{cfg.Bind}
	if cfg.Loopback && cfg.Bind == "" {
		addresses = nil
		for i := 0; i <= cfg.PortMax-cfg.PortMin; i++ {
			addresses = append(addresses, fmt.Sprintf("127.0.0.%d:%d", 2+i, cfg.LoopbackPort))
		}
	}
	var listeners []net.Listener
	for _, address := range addresses {
		listener, err := net.Listen("tcp", address)
		if err != nil {
			for _, opened := range listeners {
				_ = opened.Close()
			}
			return nil, fmt.Errorf("project preview listener %s: %w", address, err)
		}
		listeners = append(listeners, listener)
	}
	return listeners, nil
}
