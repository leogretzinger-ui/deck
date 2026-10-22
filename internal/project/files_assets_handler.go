package project

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// assetsEventsInterval is longer than the files feed's: each tick walks all
// five locations recursively (bounded, but up to MaxAssetEntries entries
// each) where the files feed reads one directory.
const assetsEventsInterval = 5 * time.Second

// maxAssetDeleteBodyBytes leaves room for MaxAssetDeleteFiles entries with
// long paths and their versions.
const maxAssetDeleteBodyBytes = 4 << 20

// ListAssets serves the scan of a project's agent locations.
func (h *Handler) ListAssets(w http.ResponseWriter, r *http.Request) {
	root, ok := h.projectRoot(w, r)
	if !ok {
		return
	}
	listing, err := ScanAssets(root)
	if err != nil {
		h.fileError(w, err)
		return
	}
	h.json(w, http.StatusOK, listing)
}

// AssetsEvents keeps the asset listing live, in the files feed's style:
// `assets-changed` whenever the bounded scan's paths, sizes, or mtimes
// differ from the last tick.
func (h *Handler) AssetsEvents(w http.ResponseWriter, r *http.Request) {
	root, ok := h.projectRoot(w, r)
	if !ok {
		return
	}
	if _, err := ScanAssets(root); err != nil {
		h.fileError(w, err)
		return
	}
	h.streamChanges(w, r, "assets-changed", assetsEventsInterval, func() (string, error) {
		return assetsFingerprint(root)
	})
}

// DeleteAssets removes the requested agent files and answers with one
// outcome per file. The request is refused whole only when it is malformed;
// otherwise every file gets an answer, deleted or skipped with the reason.
func (h *Handler) DeleteAssets(w http.ResponseWriter, r *http.Request) {
	root, ok := h.projectRoot(w, r)
	if !ok {
		return
	}
	var req struct {
		Files []AssetDeleteRequest `json:"files"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAssetDeleteBodyBytes))
	if err := dec.Decode(&req); err != nil {
		var maxBytes *http.MaxBytesError
		if errors.As(err, &maxBytes) {
			h.err(w, http.StatusRequestEntityTooLarge, "too_large", fmt.Sprintf("request exceeds %d bytes", maxBytes.Limit))
			return
		}
		h.err(w, http.StatusBadRequest, "bad_request", "body must be JSON naming files")
		return
	}
	if len(req.Files) == 0 {
		h.err(w, http.StatusBadRequest, "bad_request", "files is required")
		return
	}
	if len(req.Files) > MaxAssetDeleteFiles {
		h.err(w, http.StatusBadRequest, "bad_request", fmt.Sprintf("at most %d files per request", MaxAssetDeleteFiles))
		return
	}
	for _, f := range req.Files {
		if strings.TrimSpace(f.Path) == "" || strings.TrimSpace(f.Version) == "" || len(f.Version) > 128 {
			h.err(w, http.StatusBadRequest, "bad_request", "each file needs a path and the version the listing reported")
			return
		}
	}
	results, err := DeleteAssets(root, req.Files)
	if err != nil {
		h.fileError(w, err)
		return
	}
	project := r.PathValue("id")
	for _, res := range results {
		ev := h.log.Info().Str("project", project).Str("path", res.Path).Str("outcome", res.Outcome)
		if res.Reason != "" {
			ev = ev.Str("reason", res.Reason)
		}
		if res.err != nil {
			ev = ev.Err(res.err)
		}
		ev.Msg("asset delete")
	}
	h.json(w, http.StatusOK, map[string]any{"results": results})
}
