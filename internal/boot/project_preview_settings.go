package boot

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/noisefloor-oss/deck/internal/apppreview"
	"github.com/noisefloor-oss/deck/internal/config"
	"github.com/noisefloor-oss/deck/internal/project"
)

type projectPreviewSettings struct {
	Configured  bool   `json:"configured"`
	SelfService bool   `json:"self_service"`
	Enabled     bool   `json:"enabled"`
	Source      string `json:"source,omitempty"`
	Origin      string `json:"origin,omitempty"`
	Port        int    `json:"port,omitempty"`
	Upstream    string `json:"upstream,omitempty"`
}

func (h *Handles) mountProjectPreviewSettings(cfg *config.Config, projects *project.Repository) {
	settings := func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if _, err := projects.Get(r.Context(), id); err != nil {
			if errors.Is(err, project.ErrNotFound) {
				previewAPIError(w, http.StatusNotFound, "not_found", "Project not found.")
			} else {
				previewAPIError(w, http.StatusInternalServerError, "internal", "Could not read the project.")
			}
			return
		}
		if r.Method != http.MethodGet && !emptyPreviewRequest(w, r) {
			return
		}
		out := projectPreviewSettings{Configured: h.PreviewRegistry != nil, SelfService: cfg.ProjectPreviews.Loopback || cfg.ProjectPreviews.BaseDomain != ""}
		if h.PreviewRegistry == nil {
			if r.Method == http.MethodGet {
				previewJSON(w, out)
			} else {
				previewAPIError(w, http.StatusConflict, "preview_unconfigured", "The installation owner must configure project previews first.")
			}
			return
		}
		var registration *apppreview.Registration
		var err error
		switch r.Method {
		case http.MethodPut:
			registration, err = h.PreviewRegistry.Enable(r.Context(), id)
		case http.MethodDelete:
			err = h.PreviewRegistry.Disable(r.Context(), id)
			if err == nil {
				registration, err = h.PreviewRegistry.Get(r.Context(), id)
			}
		default:
			registration, err = h.PreviewRegistry.Get(r.Context(), id)
		}
		if err != nil {
			h.previewRegistrationError(w, err)
			return
		}
		if registration != nil {
			out.Enabled, out.Source = registration.Enabled, registration.Source
			out.Origin, out.Port, out.Upstream = registration.Origin, registration.Port, registration.Upstream
		}
		previewJSON(w, out)
	}
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		h.Server.Handle(method+" /api/projects/{id}/preview/settings", h.Auth.Require(http.HandlerFunc(settings)))
	}
	h.Server.Handle("POST /api/projects/{id}/preview/check", h.Auth.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !emptyPreviewRequest(w, r) {
			return
		}
		if h.PreviewRegistry == nil {
			previewAPIError(w, http.StatusConflict, "preview_unconfigured", "The installation owner must configure project previews first.")
			return
		}
		registration, err := h.PreviewRegistry.Get(r.Context(), r.PathValue("id"))
		if err != nil {
			h.previewRegistrationError(w, err)
			return
		}
		if registration == nil || !registration.Enabled {
			previewAPIError(w, http.StatusConflict, "preview_disabled", "Enable this project's preview before checking its port.")
			return
		}
		// A TCP connection observes a listener only. It neither reads project
		// content nor attests that the intended application owns the port.
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", registration.Upstream)
		if err == nil {
			_ = conn.Close()
		}
		previewJSON(w, struct {
			Reachable bool      `json:"reachable"`
			CheckedAt time.Time `json:"checked_at"`
		}{Reachable: err == nil, CheckedAt: time.Now().UTC()})
	})))
}

// The registration endpoint has no destination fields. Rejecting unknown
// fields keeps clients from mistaking ignored input for a selected upstream.
func emptyPreviewRequest(w http.ResponseWriter, r *http.Request) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	d.DisallowUnknownFields()
	var empty *struct{}
	err := d.Decode(&empty)
	if err == io.EOF || (err == nil && empty != nil && d.Decode(new(any)) == io.EOF) {
		return true
	}
	previewAPIError(w, http.StatusBadRequest, "bad_request", "This request accepts no settings. Deck assigns the preview address and port.")
	return false
}

func (h *Handles) previewRegistrationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, apppreview.ErrPreviewProjectNotFound):
		previewAPIError(w, http.StatusNotFound, "not_found", "Project not found.")
	case errors.Is(err, apppreview.ErrPreviewStatic):
		previewAPIError(w, http.StatusConflict, "preview_static", "This preview is managed by the installation's configuration.")
	case errors.Is(err, apppreview.ErrPreviewUnconfigured):
		previewAPIError(w, http.StatusConflict, "preview_unconfigured", "The installation owner must configure a preview domain and port range first.")
	case errors.Is(err, apppreview.ErrPreviewPortsExhausted):
		previewAPIError(w, http.StatusConflict, "preview_ports_exhausted", "No preview port is available. The installation owner can expand the port range.")
	default:
		h.log.Error().Err(err).Msg("project preview registration failed")
		previewAPIError(w, http.StatusInternalServerError, "internal", "Could not update the preview registration. Try again.")
	}
}

func previewJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func previewAPIError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": message}})
}
