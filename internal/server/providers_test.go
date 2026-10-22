package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/noisefloor-oss/deck/internal/provider"
)

// The picker asks this endpoint what to draw, so what it answers is the whole
// contract: the harnesses boot registered, and nothing else. The descriptors
// are nil on purpose — the handler must read the registry's keys and never
// touch a descriptor, because a probe here would run on every sidebar refresh.
func registry(names ...provider.Name) map[provider.Name]provider.Descriptor {
	out := make(map[provider.Name]provider.Descriptor, len(names))
	for _, name := range names {
		out[name] = nil
	}
	return out
}

func servedProviders(t *testing.T, in map[provider.Name]provider.Descriptor) []string {
	t.Helper()
	rec := httptest.NewRecorder()
	ProvidersHandler(in).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/providers", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Providers []string `json:"providers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %s: %v", rec.Body, err)
	}
	return body.Providers
}

// A slim image is a console with two harnesses and a terminal. The endpoint
// must say exactly that, because the picker draws its answer verbatim: an
// extra name is a chip that fails when clicked.
func TestProvidersHandlerServesTheRegistryAndNothingElse(t *testing.T) {
	got := servedProviders(t, registry(provider.Claude, provider.Codex, provider.Shell))
	want := []string{"claude", "codex", "shell"}
	if len(got) != len(want) {
		t.Fatalf("slim image served %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("slim image served %v, want %v", got, want)
		}
	}
}

// The response order is the order the picker draws in, so it cannot be Go's
// map iteration order. Registering the same three harnesses in a different
// order must produce the same list.
func TestProvidersHandlerSortsSoTheOrderIsNotMapOrder(t *testing.T) {
	for range 20 {
		got := servedProviders(t, registry(provider.Shell, provider.Codex, provider.Claude))
		if len(got) != 3 || got[0] != "claude" || got[1] != "codex" || got[2] != "shell" {
			t.Fatalf("served %v, want claude, codex, shell in that order", got)
		}
	}
}

// An image with no harnesses is an empty picker, not a broken one: the client
// parses an array, and Go encodes a nil slice as null.
func TestProvidersHandlerServesAnEmptyArrayNotNull(t *testing.T) {
	rec := httptest.NewRecorder()
	ProvidersHandler(nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/providers", nil))
	if body := rec.Body.String(); body != "{\"providers\":[]}\n" {
		t.Fatalf("empty registry served %q, want an empty array", body)
	}
}

// The answer changes with the image the binary runs in, and a browser that
// cached one image's list would draw the other's harnesses.
func TestProvidersHandlerIsNeverCached(t *testing.T) {
	rec := httptest.NewRecorder()
	ProvidersHandler(registry(provider.Claude)).
		ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/providers", nil))
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
}
