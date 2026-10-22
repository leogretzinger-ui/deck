package boot

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// The dev preview is the most permissive surface Deck mounts: it serves
// project source and executes it with the user's live authority. Two
// gates are therefore load-bearing at the real mux, not just in a unit test —
// the experiment flag and the browser authentication.
func TestDevPreviewIsGatedByTheFlagAndTheAuthentication(t *testing.T) {
	vite := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "dev server saw "+r.URL.Path)
	}))
	defer vite.Close()
	upstream, err := url.Parse(vite.URL)
	if err != nil {
		t.Fatal(err)
	}

	// Off: the path must 404 like any unknown one. A gated surface that
	// announced itself with a 401 would be a discovery oracle.
	off := testConfig(t)
	off.Auth.Mode = "trusted_proxy"
	off.Auth.TrustedProxy.Addresses = []string{"127.0.0.1"}
	offBase := serveServer(t, upOrFail(t, off))
	if code, _ := authenticatedGet(t, offBase+"/__dev/"); code != http.StatusNotFound {
		t.Fatalf("dev preview with the experiment off = %d, want 404", code)
	}

	on := testConfig(t)
	on.Auth.Mode = "trusted_proxy"
	on.Auth.TrustedProxy.Addresses = []string{"127.0.0.1"}
	on.Experiments.DevPreview = true
	on.Experiments.DevPreviewUpstream = upstream.Host
	onBase := serveServer(t, upOrFail(t, on))

	if code, _ := get(t, onBase+"/__dev/"); code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated dev preview = %d, want 401", code)
	}
	code, body := authenticatedGet(t, onBase+"/__dev/assets/main.js")
	if code != http.StatusOK {
		t.Fatalf("authenticated dev preview = %d (%s)", code, body)
	}
	// The prefix reaches the dev server intact: Vite is configured with a
	// matching base and must own the whole subtree.
	if !strings.Contains(body, "dev server saw /__dev/assets/main.js") {
		t.Fatalf("dev server received %q", body)
	}

	// Enabling the preview must not widen the app's own pages.
	appResp, err := http.Get(onBase + "/") //nolint:noctx // test helper style matches the file
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = appResp.Body.Close() }()
	if csp := appResp.Header.Get("Content-Security-Policy"); strings.Contains(csp, "unsafe-eval") {
		t.Fatalf("the app's CSP was widened by the dev preview: %q", csp)
	}
}
