package server

import (
	"encoding/json"
	"net/http"

	"github.com/noisefloor-oss/deck/internal/provider"
)

// ProvidersHandler publishes the harnesses this image can launch, so the
// session picker offers those and no others. It is an inventory, not a
// probe: it reads the registry boot already resolved and touches nothing
// else, which is what lets the browser ask on every sidebar refresh.
//
// The names are frozen at construction because the registry is: boot builds
// it once, and a handler that re-derived it per request would only be able
// to disagree with the launcher.
func ProvidersHandler(registry map[provider.Name]provider.Descriptor) http.Handler {
	// The response is an object rather than a bare array so a later field —
	// a display label, a version — can be added without a second endpoint or
	// a breaking shape change for clients already parsing this one.
	body := struct {
		Providers []string `json:"providers"`
	}{Providers: provider.AvailableNames(registry)}
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	})
}
