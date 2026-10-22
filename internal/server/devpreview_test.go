package server

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// devPreviewUpstream stands in for the Vite dev server: it reports the exact
// path it was asked for, so a test can prove the prefix survived the hop.
func devPreviewUpstream(t *testing.T, h http.Handler) *url.URL {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse upstream: %v", err)
	}
	return u
}

// With the experiment off nothing mounts the prefix, and the public static
// handler must refuse the whole subtree byte-for-byte as it refuses a path
// that does not exist. The SPA-shell fallback is what makes this load-bearing
// rather than cosmetic: without the reservation an extensionless /__dev/
// navigation would receive index.html and render the COMPILED app under a
// base it was not built for — a broken page that looks like a real one.
func TestStaticHandlerRefusesTheDevPreviewPrefix(t *testing.T) {
	h := StaticHandler(staticAssets(), "dev")
	missing := requestStatic(t, h, "/no-such-page.html", "text/html")
	if missing.Code != http.StatusNotFound {
		t.Fatalf("control path = %d, want 404", missing.Code)
	}
	for _, target := range []string{"/__dev", "/__dev/", "/__dev/index.html", "/__dev/@vite/client"} {
		rec := requestStatic(t, h, target, "text/html")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("public %s = %d, want 404", target, rec.Code)
		}
		if rec.Body.String() != missing.Body.String() {
			t.Fatalf("%s refusal %q differs from a missing path %q",
				target, rec.Body.String(), missing.Body.String())
		}
	}
}

// The backend must NOT strip the prefix. Vite is configured with a matching
// base and emits its asset and HMR URLs under it; a stripped prefix would
// make it emit "/assets/..." that collide with the compiled app's own static
// surface.
func TestDevPreviewHandlerKeepsThePrefix(t *testing.T) {
	var got string
	upstream := devPreviewUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Path
		_, _ = io.WriteString(w, "ok")
	}))
	rec := requestStatic(t, DevPreviewHandler(upstream), "/__dev/assets/main.js", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("dev preview = %d, want 200", rec.Code)
	}
	if got != "/__dev/assets/main.js" {
		t.Fatalf("upstream saw %q, want the prefix intact", got)
	}
}

// The dev route's CSP is widened for a bundler in development — and only
// there. The app's own compiled pages must keep the strict policy, which is
// the entire reason staticCSP exists.
func TestDevPreviewHandlerServesWithItsOwnCSP(t *testing.T) {
	upstream := devPreviewUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Vite sends its own; the proxy must replace it, not append to it.
		w.Header().Set("Cache-Control", "no-cache")
		// A plugin's policy. A browser enforces every policy it receives and
		// the strictest wins, so this must not survive the hop — composed
		// with ours it would forbid the inline script the preview needs.
		w.Header().Set("Content-Security-Policy", "default-src 'none'")
		w.Header().Set("Content-Security-Policy-Report-Only", "default-src 'none'")
		_, _ = io.WriteString(w, "<!doctype html>")
	}))
	rec := requestStatic(t, DevPreviewHandler(upstream), "/__dev/", "text/html")
	if got := rec.Header().Values("Content-Security-Policy"); len(got) != 1 {
		t.Fatalf("Content-Security-Policy = %v, want exactly one policy — a browser enforces all of them", got)
	}
	if rpt := rec.Header().Values("Content-Security-Policy-Report-Only"); len(rpt) != 0 {
		t.Fatalf("upstream report-only policy survived: %v", rpt)
	}
	csp := rec.Header().Get("Content-Security-Policy")
	// The route's own policy legitimately contains 'none' directives, so
	// match the upstream one by its distinctive default-src.
	if strings.Contains(csp, "default-src 'none'") {
		t.Fatalf("the upstream policy leaked into the route's own: %q", csp)
	}
	if !strings.Contains(csp, "script-src 'self' 'unsafe-inline' 'unsafe-eval'") {
		t.Fatalf("dev CSP does not admit a dev bundler: %q", csp)
	}
	if !strings.Contains(csp, "connect-src 'self' ws: wss:") {
		t.Fatalf("dev CSP does not admit the HMR socket: %q", csp)
	}
	// Exactly one value. ReverseProxy adds the upstream's headers to ours,
	// and Vite sends its own Cache-Control, so a naive pre-set emits both.
	if cc := rec.Header().Values("Cache-Control"); len(cc) != 1 || cc[0] != "no-store" {
		t.Fatalf("dev preview Cache-Control = %v, want exactly [no-store]", cc)
	}

	appCSP := requestStatic(t, StaticHandler(staticAssets(), "dev"), "/", "text/html").
		Header().Get("Content-Security-Policy")
	if strings.Contains(appCSP, "unsafe-eval") || strings.Contains(appCSP, "script-src 'self' 'unsafe-inline'") {
		t.Fatalf("the app's own CSP was widened: %q", appCSP)
	}
}

// HMR is a WebSocket upgrade. httputil.ReverseProxy switches to a raw byte
// pipe on a 101, and this asserts that end to end — without it the page
// would load and then silently never reload.
func TestDevPreviewHandlerCarriesTheHMRUpgrade(t *testing.T) {
	upstream := devPreviewUpstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			t.Errorf("upstream saw Upgrade %q, want websocket", r.Header.Get("Upgrade"))
			return
		}
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack upstream: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = io.WriteString(conn, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		_, _ = io.WriteString(conn, "hmr-update")
	}))

	front := httptest.NewServer(DevPreviewHandler(upstream))
	defer front.Close()

	conn, err := net.Dial("tcp", strings.TrimPrefix(front.URL, "http://"))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	_, _ = io.WriteString(conn, "GET /__dev/@vite-hmr HTTP/1.1\r\nHost: deck.test\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n")

	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("upgrade = %d, want 101", resp.StatusCode)
	}
	payload := make([]byte, len("hmr-update"))
	if _, err := io.ReadFull(br, payload); err != nil {
		t.Fatalf("read pushed bytes: %v", err)
	}
	if string(payload) != "hmr-update" {
		t.Fatalf("pushed %q through the tunnel, want hmr-update", payload)
	}
}

// The dev server is started and stopped by hand, so its absence is the
// ordinary state. Say what to run rather than leaking a dial error.
func TestDevPreviewHandlerReportsAMissingDevServer(t *testing.T) {
	// A port nothing listens on: bind one, learn its address, release it.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()

	upstream, err := url.Parse("http://" + addr)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rec := requestStatic(t, DevPreviewHandler(upstream), "/__dev/", "text/html")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("absent dev server = %d, want 502", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "pnpm dev") {
		t.Fatalf("502 does not say how to start it: %q", rec.Body.String())
	}
}

// A dev server may answer with 103 Early Hints before its real response.
// net/http forwards the informational response and then CLEARS the header
// map, so anything installed on the ResponseWriter before proxying is gone by
// the time the 200 is written — the preview would then run with no policy and
// none of the other security headers. This needs real servers on both sides:
// a ResponseRecorder does not reproduce the clearing.
func TestDevPreviewHandlerKeepsItsHeadersAfterEarlyHints(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Link", "</__dev/src/main.ts>; rel=modulepreload")
		w.WriteHeader(http.StatusEarlyHints)
		w.Header().Set("Content-Security-Policy", "default-src 'none'")
		_, _ = io.WriteString(w, "<!doctype html>")
	}))
	defer upstream.Close()
	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}

	front := httptest.NewServer(DevPreviewHandler(target))
	defer front.Close()

	resp, err := http.Get(front.URL + "/__dev/") //nolint:noctx // matches the file's test style
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	csp := resp.Header.Values("Content-Security-Policy")
	if len(csp) != 1 {
		t.Fatalf("Content-Security-Policy = %v after early hints, want exactly one policy", csp)
	}
	if !strings.Contains(csp[0], "'unsafe-eval'") || strings.Contains(csp[0], "default-src 'none'") {
		t.Fatalf("policy after early hints is not the route's own: %q", csp[0])
	}
	for header, want := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
		"Cache-Control":          "no-store",
	} {
		if got := resp.Header.Get(header); got != want {
			t.Fatalf("%s = %q after early hints, want %q", header, got, want)
		}
	}
}
