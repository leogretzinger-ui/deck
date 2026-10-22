package server

import (
	"encoding/json"
	"net/http"
)

// BuildHandler exposes only the immutable identity of the running build.
// It carries no session or terminal state and is never cacheable.
func BuildHandler(buildID string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			BuildID string `json:"build_id"`
		}{BuildID: buildID})
	})
}
