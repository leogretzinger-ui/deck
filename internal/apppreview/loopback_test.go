package apppreview

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

var loopbackDeckOrigins = []string{"http://127.0.0.1:8837", "http://localhost:8837"}

func newLoopbackGateway(t *testing.T, targets ...Target) *Gateway {
	t.Helper()
	g, err := New(Config{Loopback: true, DeckOrigins: loopbackDeckOrigins, Targets: targets})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = g.Close() })
	return g
}

func TestLoopbackTargetValidation(t *testing.T) {
	for _, origin := range []string{"http://127.0.0.2:8840", "http://127.0.0.255:8840", "http://127.1.2.3:80"} {
		g := newLoopbackGateway(t, Target{ProjectID: "app", Origin: origin, Upstream: "127.0.0.1:15000"})
		if !g.HasProject("app") {
			t.Fatal("valid target missing")
		}
	}
	for _, origin := range []string{"https://127.0.0.2:8840", "http://127.0.0.1:8840", "http://localhost:8840", "http://[::1]:8840", "http://[::ffff:127.0.0.2]:8840", "http://127.0.0.2", "http://127.0.0.2:", "http://127.0.0.2:0", "http://127.0.0.2:65536", "http://192.0.2.1:8840", "http://app.example.net:8840", "http://127.0.0.2:8840/app"} {
		t.Run(origin, func(t *testing.T) {
			g := newLoopbackGateway(t)
			if err := g.Register(Target{ProjectID: "app", Origin: origin, Upstream: "127.0.0.1:15000"}); err == nil {
				t.Fatal("unsafe target accepted")
			}
		})
	}
	for _, deck := range []string{"http://127.0.0.2:8840", "http://127.0.0.2:8837"} {
		g, err := New(Config{Loopback: true, DeckOrigins: append([]string{deck}, loopbackDeckOrigins...), Targets: []Target{{ProjectID: "app", Origin: "http://127.0.0.2:8840", Upstream: "127.0.0.1:15000"}}})
		if err == nil {
			_ = g.Close()
			t.Fatal("Deck host accepted as preview")
		}
	}
}

func TestLoopbackHTTPEntry(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.2:0")
	if err != nil {
		t.Skipf("127.0.0.2 unavailable: %v", err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "app=value" || r.Header.Get("X-Forwarded-Proto") != "http" || r.Host != listener.Addr().String() {
			t.Errorf("upstream received incorrect cookies or forwarding metadata")
		}
		http.SetCookie(w, &http.Cookie{Name: loopbackCookieName, Value: "forged"})
		http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "forged"})
		_, _ = io.WriteString(w, "preview app")
	}))
	defer upstream.Close()
	origin := "http://" + listener.Addr().String()
	g := newLoopbackGateway(t, Target{ProjectID: "app", Origin: origin, Upstream: strings.TrimPrefix(upstream.URL, "http://")})
	server := httptest.NewUnstartedServer(g)
	_ = server.Listener.Close()
	server.Listener = listener
	server.Start()
	defer server.Close()
	transport := &http.Transport{}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	// The linter cannot see a body closed in Cleanup, so the helper drains and
	// closes each response and hands back only what the assertions read.
	type reply struct {
		status  int
		header  http.Header
		cookies []*http.Cookie
		body    string
	}
	request := func(method, path, from, body string) reply {
		t.Helper()
		req, err := http.NewRequest(method, origin+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Origin", from)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		content, _ := io.ReadAll(res.Body)
		_ = res.Body.Close()
		return reply{status: res.StatusCode, header: res.Header, cookies: res.Cookies(), body: string(content)}
	}
	if res := request("GET", "/", origin, ""); res.status != 403 {
		t.Fatalf("anonymous access: %d", res.status)
	}
	for _, deck := range loopbackDeckOrigins {
		client.Jar, _ = cookiejar.New(nil)
		entry, err := g.Issue("app", "operator")
		if err != nil {
			t.Fatal(err)
		}
		body := url.Values{"ticket": {entry.Ticket}}.Encode()
		if res := request("POST", entryPath, "http://127.0.0.3:8840", body); res.status != 403 {
			t.Fatalf("foreign entry: %d", res.status)
		}
		res := request("POST", entryPath, deck, body)
		if res.status != 303 || res.header.Get("Location") != "/" {
			t.Fatalf("entry: %d", res.status)
		}
		cookies := res.cookies
		if len(cookies) != 1 {
			t.Fatalf("cookies = %d", len(cookies))
		}
		c := cookies[0]
		if c.Name != "deck_preview" || c.Secure || !c.HttpOnly || c.Domain != "" || c.Path != "/" || c.SameSite != http.SameSiteLaxMode || c.MaxAge != 1800 {
			t.Fatal("incorrect loopback cookie attributes")
		}
		if res := request("POST", entryPath, deck, body); res.status != 403 {
			t.Fatalf("ticket replay: %d", res.status)
		}
		u, _ := url.Parse(origin)
		client.Jar.SetCookies(u, []*http.Cookie{{Name: "app", Value: "value"}, {Name: "noisedeck_session", Value: "private"}, {Name: cookieName, Value: "private"}})
		res = request("GET", "/", origin, "")
		if res.status != 200 || res.body != "preview app" || len(res.cookies) != 0 {
			t.Fatalf("authorized app: %d, %q", res.status, res.body)
		}
		if res := request("GET", "/", "http://127.0.0.3:8840", ""); res.status != 403 {
			t.Fatalf("foreign fetch: %d", res.status)
		}
	}
}

func TestLoopbackRegistryRestore(t *testing.T) {
	d := registryDB(t)
	g := newLoopbackGateway(t)
	low, high := registryPool(t, 2)
	cfg := RegistryConfig{LoopbackPort: 8840, PortMin: low, PortMax: high}
	r, err := NewRegistry(d, g, cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for i, id := range []string{"a", "b"} {
		registryProject(t, d, id)
		reg, err := r.Enable(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if reg.Origin != fmt.Sprintf("http://127.0.0.%d:8840", 2+i) || reg.Port != low+i {
			t.Fatalf("allocation: %+v", reg)
		}
		if err := r.Disable(ctx, id); err != nil {
			t.Fatal(err)
		}
		again, err := r.Enable(ctx, id)
		if err != nil || *again != *reg {
			t.Fatalf("reenable moved allocation: %+v, %v", again, err)
		}
	}
	_ = g.Close()
	restored := newLoopbackGateway(t)
	r2, err := NewRegistry(d, restored, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := r2.Restore(ctx); err != nil || !restored.HasProject("a") || !restored.HasProject("b") {
		t.Fatalf("restore: %v", err)
	}
	for _, change := range []func(*RegistryConfig){
		func(c *RegistryConfig) { c.PortMin-- },
		func(c *RegistryConfig) { c.LoopbackPort++ },
	} {
		changed := cfg
		change(&changed)
		gateway := newLoopbackGateway(t)
		registry, err := NewRegistry(d, gateway, changed)
		if err != nil {
			t.Fatal(err)
		}
		if err := registry.Restore(ctx); err == nil {
			t.Fatal("changed origin silently accepted")
		}
		if gateway.HasProject("a") || gateway.HasProject("b") {
			t.Fatal("conflicting restore installed routes")
		}
	}
	r.cfg.PortMax = r.cfg.PortMin + 253
	if got := r.managedOrigin("last", r.cfg.PortMax); got != "http://127.0.0.255:8840" {
		t.Fatalf("last slot = %s", got)
	}
}

func TestBothPreviewCookiesAreReserved(t *testing.T) {
	h := http.Header{"Cookie": {"deck_preview=private; __Host-deck_preview=private; app=value"}}
	filterForwardedCookies(h)
	if got := h.Get("Cookie"); got != "app=value" {
		t.Fatalf("forwarded cookies = %s", got)
	}
	for _, name := range []string{"deck_preview", "__Host-deck_preview"} {
		for _, host := range []string{"app.example.net", "127.0.0.2"} {
			if _, ok := sanitizeSetCookie(name+"=forged; Path=/", host); ok {
				t.Fatalf("upstream could set %s on %s", name, host)
			}
		}
	}
}
