package server

import (
	"net/http"
	"strings"
	"testing"
	"testing/fstest"
)

func benchAssets() fstest.MapFS {
	assets := fstest.MapFS{}
	for name, file := range staticAssets().(fstest.MapFS) {
		assets[name] = file
	}
	assets["bench.html"] = &fstest.MapFile{Data: []byte(`<!doctype html><title>Noisefloor Deck — font substitution bench</title>`)}
	return assets
}

// The public static surface never serves the bench page, even when the build
// carries it: with the experiment off, /bench.html must 404 exactly like any
// unknown path — a gated surface must not be discoverable by its refusal.
func TestStaticHandlerRefusesTheBenchPage(t *testing.T) {
	rec := requestStatic(t, StaticHandler(benchAssets(), "dev"), "/bench.html", "text/html")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("public /bench.html = %d, want 404", rec.Code)
	}
	unknown := requestStatic(t, StaticHandler(benchAssets(), "dev"), "/no-such-page.html", "text/html")
	if rec.Code != unknown.Code || rec.Body.String() != unknown.Body.String() {
		t.Fatalf("bench refusal (%d %q) differs from an unknown path (%d %q)",
			rec.Code, rec.Body.String(), unknown.Code, unknown.Body.String())
	}
}

// The dedicated bench route serves the page with the bench-only CSP: Google
// Fonts' stylesheet and font hosts are admitted THERE and nowhere else — the
// app's own pages keep the strict static policy.
func TestFontBenchHandlerServesWithItsOwnCSP(t *testing.T) {
	rec := requestStatic(t, FontBenchHandler(benchAssets()), "/bench.html", "text/html")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "font substitution bench") {
		t.Fatalf("bench route = %d %q", rec.Code, rec.Body.String())
	}
	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "style-src 'self' 'unsafe-inline' https://fonts.googleapis.com") ||
		!strings.Contains(csp, "font-src 'self' https://fonts.gstatic.com") {
		t.Fatalf("bench CSP does not admit the candidate hosts: %q", csp)
	}
	appRec := requestStatic(t, StaticHandler(benchAssets(), "dev"), "/", "text/html")
	appCSP := appRec.Header().Get("Content-Security-Policy")
	if strings.Contains(appCSP, "fonts.googleapis.com") || strings.Contains(appCSP, "fonts.gstatic.com") {
		t.Fatalf("the app's own CSP was widened: %q", appCSP)
	}
}

func TestFontBenchHandlerReportsAnUnbuiltFrontend(t *testing.T) {
	rec := requestStatic(t, FontBenchHandler(fstest.MapFS{}), "/bench.html", "text/html")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("bench without assets = %d, want 503", rec.Code)
	}
}
