package server

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func staticAssets() fs.FS {
	return fstest.MapFS{
		"index.html":                {Data: []byte(`<!doctype html><meta name="noisedeck-build-id" content="__NOISEDECK_BUILD_ID__"><title>Deck</title>`)},
		"assets/main-a1b2c3.js":     {Data: []byte("console.log('live')")},
		"assets/main-a1b2c3.css":    {Data: []byte("body{color:black}")},
		"fonts/operator-mono.woff2": {Data: []byte("font")},
	}
}

func TestStaticIndexEmbedsEscapedBuildIdentity(t *testing.T) {
	rec := requestStatic(t, StaticHandler(staticAssets(), `release-1"><script>`), "/", "text/html")
	if body := rec.Body.String(); !strings.Contains(body, `content="release-1&#34;&gt;&lt;script&gt;"`) || strings.Contains(body, `<script>`) {
		t.Fatalf("index build identity was not safely embedded: %s", body)
	}
}

func TestBuildHandlerIsContentFreeAndUncacheable(t *testing.T) {
	rec := httptest.NewRecorder()
	BuildHandler("build-42").ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/build", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "{\"build_id\":\"build-42\"}\n" {
		t.Fatalf("build response = %d %q", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("build response cache policy = %q", rec.Header().Get("Cache-Control"))
	}
}

func requestStatic(t *testing.T, h http.Handler, target, accept string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestStaticIndexHeaders(t *testing.T) {
	rec := requestStatic(t, StaticHandler(staticAssets(), "dev"), "/", "text/html")
	if rec.Code != http.StatusOK {
		t.Fatalf("index status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("index Cache-Control = %q", got)
	}
	for _, header := range []string{"Content-Security-Policy", "X-Content-Type-Options", "X-Frame-Options", "Referrer-Policy"} {
		if rec.Header().Get(header) == "" {
			t.Fatalf("index missing %s", header)
		}
	}
}

func TestStaticHashedAssetIsImmutable(t *testing.T) {
	rec := requestStatic(t, StaticHandler(staticAssets(), "dev"), "/assets/main-a1b2c3.js", "*/*")
	if rec.Code != http.StatusOK {
		t.Fatalf("asset status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Fatalf("asset Cache-Control = %q", got)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/javascript; charset=utf-8" {
		t.Fatalf("asset Content-Type = %q", got)
	}
}

func TestStaticSPAFallbackIsNavigationOnly(t *testing.T) {
	h := StaticHandler(staticAssets(), "dev")
	if rec := requestStatic(t, h, "/sessions/one", "text/html"); rec.Code != http.StatusOK {
		t.Fatalf("SPA navigation = %d, want 200", rec.Code)
	}
	for _, target := range []string{
		"/missing.js",
		"/assets/missing",
		"/fonts/missing.woff2",
		"/api/not-real",
		"/auth/not-real",
		"/livez/not-real",
		"/readyz/not-real",
		"/UNBUILT",
		"/.gitignore",
	} {
		if rec := requestStatic(t, h, target, "text/html"); rec.Code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", target, rec.Code)
		}
	}
	if rec := requestStatic(t, h, "/sessions/one", "application/json"); rec.Code != http.StatusNotFound {
		t.Fatalf("non-navigation fallback = %d, want 404", rec.Code)
	}
}

func TestStaticUnbuiltBinaryFailsVisibly(t *testing.T) {
	h := StaticHandler(fstest.MapFS{"UNBUILT": {Data: []byte("marker")}}, "dev")
	rec := requestStatic(t, h, "/", "text/html")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unbuilt frontend = %d, want 503", rec.Code)
	}
	if rec.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("unbuilt response lost static security headers")
	}
}
