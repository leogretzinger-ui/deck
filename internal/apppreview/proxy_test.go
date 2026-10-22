package apppreview

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestProxiesAssetsAPIAndSpaRoutes(t *testing.T) {
	g, _, a, b := newSetup(t)
	cookie := enterPreview(t, g, testProjectA, testHostA)

	cases := []struct {
		method string
		target string
		body   string
	}{
		{http.MethodGet, "/", ""},
		{http.MethodGet, "/assets/app.css?v=2", ""},
		{http.MethodGet, "/app/dashboard?tab=editor", ""},
		{http.MethodPost, "/api/data", `{"x":1}`},
		{http.MethodPut, "/api/data/7", "put-body"},
		{http.MethodDelete, "/api/data/7", ""},
	}
	for _, tc := range cases {
		var body io.Reader
		if tc.body != "" {
			body = strings.NewReader(tc.body)
		}
		rec := authed(t, g, cookie, testHostA, tc.method, tc.target, body)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s %s: got %d want 200", tc.method, tc.target, rec.Code)
		}
		if got := rec.Body.String(); got != "a:"+tc.target {
			t.Fatalf("%s %s: body %q want %q", tc.method, tc.target, got, "a:"+tc.target)
		}
		last := a.last()
		if last.Method != tc.method || last.Target != tc.target {
			t.Fatalf("upstream saw %s %s", last.Method, last.Target)
		}
		if last.Body != tc.body {
			t.Fatalf("%s %s: upstream body %q want %q", tc.method, tc.target, last.Body, tc.body)
		}
	}

	// The second host routes to its own upstream with the same rules.
	rec := authed(t, g, cookie, testHostB, http.MethodGet, "/", nil)
	if rec.Code == http.StatusOK || rec.Code != http.StatusForbidden {
		t.Fatalf("session cookie leaked across hosts: got %d", rec.Code)
	}
	cookieB := enterPreview(t, g, testProjectB, testHostB)
	recB := authed(t, g, cookieB, testHostB, http.MethodGet, "/deep/route", nil)
	if recB.Code != http.StatusOK || recB.Body.String() != "b:/deep/route" {
		t.Fatalf("host B: got %d %q", recB.Code, recB.Body.String())
	}
	if b.last().Host != testHostB {
		t.Fatalf("upstream B saw Host %q want %q", b.last().Host, testHostB)
	}
}

func TestConstructsForwardingMetadataFromConfiguration(t *testing.T) {
	g, _, a, _ := newSetup(t)
	cookie := enterPreview(t, g, testProjectA, testHostA)

	req := previewRequest(http.MethodGet, testHostA, "/page", nil)
	req.AddCookie(cookie)
	req.Header.Set("Origin", "https://"+testHostA)
	// Forged topology headers must not survive: Host matching and
	// forwarding metadata come from configuration, never from the client.
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	req.Header.Set("X-Forwarded-Host", "evil.example.com")
	req.Header.Set("X-Forwarded-Proto", "http")
	req.Header.Set("Forwarded", "for=203.0.113.9;host=evil.example.com")
	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d want 200", rec.Code)
	}

	last := a.last()
	if last.Host != testHostA {
		t.Fatalf("upstream Host: got %q want %q", last.Host, testHostA)
	}
	if got := last.Header.Get("X-Forwarded-Proto"); got != "https" {
		t.Fatalf("X-Forwarded-Proto: got %q want https", got)
	}
	if got := last.Header.Get("X-Forwarded-Host"); got != testHostA {
		t.Fatalf("X-Forwarded-Host: got %q want %q", got, testHostA)
	}
	if got := last.Header.Get("X-Forwarded-For"); got != "" {
		t.Fatalf("X-Forwarded-For leaked: %q", got)
	}
	if got := last.Header.Get("Forwarded"); got != "" {
		t.Fatalf("Forwarded leaked: %q", got)
	}
}

func TestStripsGatewayAndDeckCookiesAndIdentityHeaders(t *testing.T) {
	g, _, a, _ := newSetup(t)
	cookie := enterPreview(t, g, testProjectA, testHostA)

	req := previewRequest(http.MethodPost, testHostA, "/api/echo", strings.NewReader("body"))
	req.Header.Set("Origin", "https://"+testHostA)
	req.AddCookie(cookie)
	req.AddCookie(&http.Cookie{Name: "__Host-noisedeck_session", Value: "deck-session"})
	req.AddCookie(&http.Cookie{Name: "noisedeck_csrf", Value: "deck-csrf"})
	req.AddCookie(&http.Cookie{Name: "__Host-noisedeck_oidc_txn", Value: "deck-txn"})
	req.AddCookie(&http.Cookie{Name: "app_session", Value: "keep-me"})
	req.AddCookie(&http.Cookie{Name: "app_prefs", Value: "a=b"})
	// The app's own authorization header stays: app auth is the app's.
	req.Header.Set("Authorization", "Bearer app-token")
	for _, h := range []string{
		"X-Real-Ip", "X-Forwarded-User", "X-Auth-Request-User",
		"X-Remote-User", "X-Original-Url", "Proxy-Authorization",
		"X-Auth-Request-Groups", "X-Auth-Request-Preferred-Username", "X-Forwarded-Prefix", "X-Forwarded-Uri",
	} {
		req.Header.Set(h, "edge-secret")
	}
	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d want 200", rec.Code)
	}

	last := a.last()
	if got, want := last.Header.Get("Cookie"), "app_session=keep-me; app_prefs=a=b"; got != want {
		t.Fatalf("upstream Cookie: got %q want %q", got, want)
	}
	if got := last.Header.Get("Authorization"); got != "Bearer app-token" {
		t.Fatalf("app Authorization: got %q", got)
	}
	for _, h := range []string{
		"X-Real-Ip", "X-Forwarded-User", "X-Auth-Request-User",
		"X-Remote-User", "X-Original-Url", "Proxy-Authorization",
		"X-Auth-Request-Groups", "X-Auth-Request-Preferred-Username", "X-Forwarded-Prefix", "X-Forwarded-Uri",
		"X-Forwarded-For",
	} {
		if got := last.Header.Get(h); got != "" {
			t.Fatalf("identity header %s reached the upstream: %q", h, got)
		}
	}
}

func TestEdgeCredentialsCannotRideConnectionNamedHeaders(t *testing.T) {
	respond := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Connection", "X-Edge-Auth")
		w.Header().Set("X-Edge-Auth", "edge-secret")
		w.WriteHeader(http.StatusOK)
	}
	a := newCaptureUpstream(t, "a:", respond)
	g, _ := newTestGateway(t, testDeckOrigin, testTargets(a.addr(), "127.0.0.1:18082")...)
	cookie := enterPreview(t, g, testProjectA, testHostA)

	// Request direction: a header named in Connection is hop-by-hop and
	// must not reach the upstream.
	req := previewRequest(http.MethodGet, testHostA, "/", nil)
	req.AddCookie(cookie)
	req.Header.Set("Origin", "https://"+testHostA)
	req.Header.Set("Connection", "X-Client-Cred")
	req.Header.Set("X-Client-Cred", "client-secret")
	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d want 200", rec.Code)
	}
	if got := a.last().Header.Get("X-Client-Cred"); got != "" {
		t.Fatalf("Connection-named request header reached upstream: %q", got)
	}
	// Response direction: the same trick must not smuggle a header to the
	// browser past the gateway.
	if got := rec.Header().Get("X-Edge-Auth"); got != "" {
		t.Fatalf("Connection-named response header reached the browser: %q", got)
	}
}

func TestSanitizesUpstreamSetCookiesAndCORS(t *testing.T) {
	respond := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Add("Set-Cookie", "app_sess=1; Path=/; HttpOnly; Secure; SameSite=Lax")
		w.Header().Add("Set-Cookie", "app_stats=2; Path=/; Domain=preview.example.com; Secure")
		w.Header().Add("Set-Cookie", "evil=3; Domain=evil.example.com")
		w.Header().Add("Set-Cookie", cookieName+"=forged; Path=/; Secure")
		w.Header().Add("Set-Cookie", "noisedeck_session=forged")
		w.Header().Add("Set-Cookie", "bad name=4")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Set("Cache-Control", "public, max-age=600")
		w.WriteHeader(http.StatusOK)
	}
	a := newCaptureUpstream(t, "a:", respond)
	g, _ := newTestGateway(t, testDeckOrigin, testTargets(a.addr(), "127.0.0.1:18082")...)
	cookie := enterPreview(t, g, testProjectA, testHostA)

	rec := authed(t, g, cookie, testHostA, http.MethodGet, "/", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d want 200", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control: got %q want no-store", got)
	}
	for _, h := range []string{"Access-Control-Allow-Origin", "Access-Control-Allow-Credentials"} {
		if got := rec.Header().Get(h); got != "" {
			t.Fatalf("CORS header %s reached the browser: %q", h, got)
		}
	}

	var gotApp, gotStats bool
	for _, c := range rec.Result().Cookies() {
		switch c.Name {
		case "app_sess":
			gotApp = true
			if c.Domain != "" {
				t.Fatalf("app cookie gained a Domain: %q", c.Domain)
			}
		case "app_stats":
			gotStats = true
			if c.Domain != "" {
				t.Fatalf("parent-domain attribute survived: %q", c.Domain)
			}
		case "evil", cookieName, "noisedeck_session", "bad name":
			t.Fatalf("forbidden cookie %q reached the browser", c.Name)
		}
	}
	if !gotApp || !gotStats {
		t.Fatalf("legitimate app cookies dropped: app=%v stats=%v", gotApp, gotStats)
	}
}

func TestUpstreamRedirectIsPassedThroughNotFollowed(t *testing.T) {
	respond := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/moved" {
			http.Redirect(w, r, "/landing", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "landing:"+r.URL.Path)
	}
	a := newCaptureUpstream(t, "a:", respond)
	g, _ := newTestGateway(t, testDeckOrigin, testTargets(a.addr(), "127.0.0.1:18082")...)
	cookie := enterPreview(t, g, testProjectA, testHostA)

	rec := authed(t, g, cookie, testHostA, http.MethodGet, "/moved", nil)
	if rec.Code != http.StatusFound {
		t.Fatalf("got %d want 302", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/landing" {
		t.Fatalf("Location: got %q want /landing", got)
	}
	if a.count() != 1 {
		t.Fatalf("gateway followed the redirect: %d upstream requests", a.count())
	}
}

func TestStreamingResponsePassesThrough(t *testing.T) {
	respond := func(w http.ResponseWriter, _ *http.Request) {
		for i := 1; i <= 3; i++ {
			io.WriteString(w, "chunk")
			w.(http.Flusher).Flush()
			time.Sleep(10 * time.Millisecond)
		}
	}
	a := newCaptureUpstream(t, "a:", respond)
	g, _ := newTestGateway(t, testDeckOrigin, testTargets(a.addr(), "127.0.0.1:18082")...)
	cookie := enterPreview(t, g, testProjectA, testHostA)

	rec := authed(t, g, cookie, testHostA, http.MethodGet, "/stream", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "chunkchunkchunk" {
		t.Fatalf("streamed body: got %q", got)
	}
}

func TestReservedEntryPathIsTheOnlyInterceptedOne(t *testing.T) {
	g, _, a, _ := newSetup(t)
	cookie := enterPreview(t, g, testProjectA, testHostA)
	rec := authed(t, g, cookie, testHostA, http.MethodGet, "/__deck_preview/app-router", nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "a:/__deck_preview/app-router" {
		t.Fatalf("reserved subtree proxied: got %d %q", rec.Code, rec.Body.String())
	}
	if a.last().Target != "/__deck_preview/app-router" {
		t.Fatalf("upstream saw %q", a.last().Target)
	}
}

func TestStoppedDevServerIsUnavailable(t *testing.T) {
	up := newCaptureUpstream(t, "a:", nil)
	up.server.Close() // the dev server is stopped by hand, independently

	g, _ := newTestGateway(t, testDeckOrigin, testTargets(up.addr(), "127.0.0.1:18082")...)
	cookie := enterPreview(t, g, testProjectA, testHostA)
	rec := authed(t, g, cookie, testHostA, http.MethodGet, "/", nil)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("stopped upstream: got %d want 502", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control: got %q want no-store", got)
	}
	if !strings.Contains(rec.Body.String(), "Start the project's dev server") {
		t.Fatalf("unavailable response lacks recovery guidance: %q", rec.Body.String())
	}
}
