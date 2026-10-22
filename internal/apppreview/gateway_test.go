package apppreview

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testDeckOrigin = "https://deck.example.com"
	testHostA      = "app-a.preview.example.com"
	testHostB      = "app-b.preview.example.com"
	testProjectA   = "project-a"
	testProjectB   = "project-b"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Unix(1700000000, 0)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// capturedRequest is what a fake dev server saw.
type capturedRequest struct {
	Method string
	Target string
	Host   string
	Header http.Header
	Body   string
}

type captureUpstream struct {
	mu       sync.Mutex
	prefix   string
	requests []capturedRequest
	respond  func(w http.ResponseWriter, r *http.Request)
	server   *httptest.Server
}

func newCaptureUpstream(t *testing.T, prefix string, respond func(http.ResponseWriter, *http.Request)) *captureUpstream {
	t.Helper()
	u := &captureUpstream{prefix: prefix, respond: respond}
	u.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		u.mu.Lock()
		u.requests = append(u.requests, capturedRequest{
			Method: r.Method,
			Target: r.URL.RequestURI(),
			Host:   r.Host,
			Header: r.Header.Clone(),
			Body:   string(body),
		})
		u.mu.Unlock()
		if u.respond != nil {
			u.respond(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, u.prefix+r.URL.RequestURI())
	}))
	t.Cleanup(u.server.Close)
	return u
}

func (u *captureUpstream) addr() string {
	return strings.TrimPrefix(u.server.URL, "http://")
}

func (u *captureUpstream) count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.requests)
}

func (u *captureUpstream) last() capturedRequest {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.requests[len(u.requests)-1]
}

func newTestGateway(t *testing.T, deckOrigin string, targets ...Target) (*Gateway, *fakeClock) {
	t.Helper()
	g, err := New(Config{DeckOrigins: []string{deckOrigin}, Targets: targets})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	clock := newFakeClock()
	g.setClock(clock.Now)
	t.Cleanup(func() { g.Close() })
	return g, clock
}

func testTargets(upstreamA, upstreamB string) []Target {
	return []Target{
		{ProjectID: testProjectA, Origin: "https://" + testHostA, Upstream: upstreamA},
		{ProjectID: testProjectB, Origin: "https://" + testHostB, Upstream: upstreamB},
	}
}

func newSetup(t *testing.T) (*Gateway, *fakeClock, *captureUpstream, *captureUpstream) {
	t.Helper()
	a := newCaptureUpstream(t, "a:", nil)
	b := newCaptureUpstream(t, "b:", nil)
	g, clock := newTestGateway(t, testDeckOrigin, testTargets(a.addr(), b.addr())...)
	return g, clock, a, b
}

func previewRequest(method, host, target string, body io.Reader) *http.Request {
	req := httptest.NewRequest(method, target, body)
	req.Host = host
	return req
}

// enterPreview redeems a fresh ticket for projectID on host and returns the
// session cookie.
func enterPreview(t *testing.T, g *Gateway, projectID, host string) *http.Cookie {
	t.Helper()
	entry, err := g.Issue(projectID, "operator")
	if err != nil {
		t.Fatalf("Issue(%q): %v", projectID, err)
	}
	return postTicket(t, g, host, entry.Ticket)
}

func postTicket(t *testing.T, g *Gateway, host, ticket string) *http.Cookie {
	t.Helper()
	form := "ticket=" + ticket
	req := previewRequest(http.MethodPost, host, entryPath, strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", testDeckOrigin)
	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("entry POST: got %d want %d", rec.Code, http.StatusSeeOther)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("entry POST: got %d cookies, want 1", len(cookies))
	}
	return cookies[0]
}

func authed(t *testing.T, g *Gateway, cookie *http.Cookie, host, method, target string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()
	req := previewRequest(method, host, target, body)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	// Browsers always attach same-origin evidence — the app's Origin on
	// POSTs, Sec-Fetch-* everywhere — so the helper sends the app Origin.
	// Requests built by hand below exercise the cases without it.
	req.Header.Set("Origin", "https://"+host)
	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, req)
	return rec
}

func TestConfigRejectsInvalidRegistrations(t *testing.T) {
	cases := []struct {
		name    string
		deck    string
		targets []Target
		wantErr string
	}{
		{
			name:    "empty deck origin",
			deck:    "",
			targets: testTargets("127.0.0.1:18081", "127.0.0.1:18082"),
			wantErr: "deck origin",
		},
		{
			name:    "deck origin with path",
			deck:    "https://deck.example.com/deck",
			targets: testTargets("127.0.0.1:18081", "127.0.0.1:18082"),
			wantErr: "must not carry a path",
		},
		{
			name:    "upstream hostname",
			deck:    testDeckOrigin,
			targets: []Target{{ProjectID: "p", Origin: "https://a.example.com", Upstream: "localhost:5173"}},
			wantErr: "IP literal",
		},
		{
			name:    "upstream url form",
			deck:    testDeckOrigin,
			targets: []Target{{ProjectID: "p", Origin: "https://a.example.com", Upstream: "http://127.0.0.1:5173"}},
			wantErr: "bare ip:port",
		},
		{
			name:    "upstream not loopback",
			deck:    testDeckOrigin,
			targets: []Target{{ProjectID: "p", Origin: "https://a.example.com", Upstream: "10.0.0.5:5173"}},
			wantErr: "loopback",
		},
		{
			name:    "upstream without port",
			deck:    testDeckOrigin,
			targets: []Target{{ProjectID: "p", Origin: "https://a.example.com", Upstream: "127.0.0.1"}},
			wantErr: "host:port",
		},
		{
			name:    "upstream port zero",
			deck:    testDeckOrigin,
			targets: []Target{{ProjectID: "p", Origin: "https://a.example.com", Upstream: "127.0.0.1:0"}},
			wantErr: "invalid port",
		},
		{
			name:    "upstream with path",
			deck:    testDeckOrigin,
			targets: []Target{{ProjectID: "p", Origin: "https://a.example.com", Upstream: "127.0.0.1:5173/app"}},
			wantErr: "invalid port",
		},
		{
			name:    "origin plain http",
			deck:    testDeckOrigin,
			targets: []Target{{ProjectID: "p", Origin: "http://a.example.com", Upstream: "127.0.0.1:5173"}},
			wantErr: "must use https",
		},
		{
			name:    "origin with path",
			deck:    testDeckOrigin,
			targets: []Target{{ProjectID: "p", Origin: "https://a.example.com/app", Upstream: "127.0.0.1:5173"}},
			wantErr: "must not carry a path",
		},
		{
			name:    "origin with userinfo",
			deck:    testDeckOrigin,
			targets: []Target{{ProjectID: "p", Origin: "https://user@a.example.com", Upstream: "127.0.0.1:5173"}},
			wantErr: "userinfo",
		},
		{
			name:    "origin without scheme",
			deck:    testDeckOrigin,
			targets: []Target{{ProjectID: "p", Origin: "a.example.com", Upstream: "127.0.0.1:5173"}},
			wantErr: "must use https",
		},
		{
			name:    "loopback origin",
			deck:    testDeckOrigin,
			targets: []Target{{ProjectID: "p", Origin: "https://127.0.0.1:8443", Upstream: "127.0.0.1:5173"}},
			wantErr: "loopback",
		},
		{
			name:    "localhost origin",
			deck:    testDeckOrigin,
			targets: []Target{{ProjectID: "p", Origin: "https://app.localhost", Upstream: "127.0.0.1:5173"}},
			wantErr: "localhost name",
		},
		{
			name:    "empty project id",
			deck:    testDeckOrigin,
			targets: []Target{{Origin: "https://a.example.com", Upstream: "127.0.0.1:5173"}},
			wantErr: "empty project id",
		},
		{
			name: "duplicate project",
			deck: testDeckOrigin,
			targets: []Target{
				{ProjectID: "p", Origin: "https://a.example.com", Upstream: "127.0.0.1:5173"},
				{ProjectID: "p", Origin: "https://b.example.com", Upstream: "127.0.0.1:5174"},
			},
			wantErr: "duplicate registration for project",
		},
		{
			name: "duplicate origin",
			deck: testDeckOrigin,
			targets: []Target{
				{ProjectID: "p", Origin: "https://a.example.com", Upstream: "127.0.0.1:5173"},
				{ProjectID: "q", Origin: "https://a.example.com", Upstream: "127.0.0.1:5174"},
			},
			wantErr: "duplicate preview origin",
		},
		{
			name: "duplicate origin via default port",
			deck: testDeckOrigin,
			targets: []Target{
				{ProjectID: "p", Origin: "https://a.example.com", Upstream: "127.0.0.1:5173"},
				{ProjectID: "q", Origin: "https://a.example.com:443", Upstream: "127.0.0.1:5174"},
			},
			wantErr: "duplicate preview origin",
		},
		{
			name: "duplicate upstream",
			deck: testDeckOrigin,
			targets: []Target{
				{ProjectID: "p", Origin: "https://a.example.com", Upstream: "127.0.0.1:5173"},
				{ProjectID: "q", Origin: "https://b.example.com", Upstream: "127.0.0.1:5173"},
			},
			wantErr: "duplicate upstream endpoint",
		},
		{
			name:    "preview origin is deck origin",
			deck:    testDeckOrigin,
			targets: []Target{{ProjectID: "p", Origin: testDeckOrigin, Upstream: "127.0.0.1:5173"}},
			wantErr: "Deck's own origin",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(Config{DeckOrigins: []string{tc.deck}, Targets: tc.targets})
			if err == nil {
				t.Fatal("New accepted an invalid config")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not mention %q", err, tc.wantErr)
			}
		})
	}
}

func TestConfigAcceptsValidRegistrations(t *testing.T) {
	g, err := New(Config{
		DeckOrigins: []string{"HTTPS://Deck.Example.COM:443"}, // normalized
		Targets: []Target{
			{ProjectID: "p", Origin: "https://a.example.com:9943", Upstream: "127.0.0.1:5173"},
			{ProjectID: "q", Origin: "https://b.example.com", Upstream: "[::1]:5174"},
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer g.Close()
	if !g.HasProject("p") || !g.HasProject("q") {
		t.Fatal("registered projects missing from HasProject")
	}
	if g.HasProject("nope") {
		t.Fatal("HasProject claims an unregistered project")
	}
	entry, err := g.Issue("q", "operator")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if want := "https://b.example.com" + entryPath; entry.URL != want {
		t.Fatalf("entry URL: got %q want %q", entry.URL, want)
	}
	// Host matching accepts the explicit default port and upper case.
	for _, host := range []string{"b.example.com", "b.example.com:443", "B.Example.COM"} {
		entry, err := g.Issue("q", "operator")
		if err != nil {
			t.Fatalf("Issue: %v", err)
		}
		rec := httptest.NewRecorder()
		req := previewRequest(http.MethodPost, host, entryPath, strings.NewReader("ticket="+entry.Ticket))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", "https://deck.example.com")
		g.ServeHTTP(rec, req)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("host %q: entry got %d want 303", host, rec.Code)
		}
	}
	// A non-default port stays part of the identity.
	rec := httptest.NewRecorder()
	req := previewRequest(http.MethodPost, "a.example.com", entryPath, strings.NewReader("ticket=x"))
	req.Header.Set("Origin", "https://deck.example.com")
	g.ServeHTTP(rec, req)
	if rec.Code != http.StatusMisdirectedRequest {
		t.Fatalf("host without registered port: got %d want 421", rec.Code)
	}
}

func TestEntryJSONContract(t *testing.T) {
	b, err := json.Marshal(Entry{URL: "https://u", Ticket: "t"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got, want := string(b), `{"url":"https://u","ticket":"t"}`; got != want {
		t.Fatalf("Entry JSON: got %s want %s", got, want)
	}
}

func TestIssueRejectsUnknownProjectAndEmptyPrincipal(t *testing.T) {
	g, clock := newTestGateway(t, testDeckOrigin, testTargets("127.0.0.1:18081", "127.0.0.1:18082")...)
	_ = clock
	if _, err := g.Issue("nope", "operator"); err == nil {
		t.Fatal("Issue accepted an unregistered project")
	}
	if _, err := g.Issue(testProjectA, ""); err == nil {
		t.Fatal("Issue accepted an empty principal")
	}
	if err := g.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := g.Issue(testProjectA, "operator"); err == nil {
		t.Fatal("Issue worked on a closed gateway")
	}
}

func TestEnterSetsSecureHostOnlyCookie(t *testing.T) {
	g, _, _, _ := newSetup(t)
	cookie := enterPreview(t, g, testProjectA, testHostA)
	if cookie.Name != cookieName {
		t.Fatalf("cookie name: got %q want %q", cookie.Name, cookieName)
	}
	if !cookie.Secure || !cookie.HttpOnly || cookie.Path != "/" || cookie.Domain != "" {
		t.Fatalf("cookie attributes: %+v", cookie)
	}
	if cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("SameSite: got %v want Lax", cookie.SameSite)
	}
	if cookie.MaxAge != int(sessionTTL/time.Second) {
		t.Fatalf("MaxAge: got %d want %d", cookie.MaxAge, int(sessionTTL/time.Second))
	}
}

func TestEnterResponseIsUncachedAndBodiless(t *testing.T) {
	g, _, _, _ := newSetup(t)
	entry, err := g.Issue(testProjectA, "operator")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	req := previewRequest(http.MethodPost, testHostA, entryPath, strings.NewReader("ticket="+entry.Ticket))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", testDeckOrigin)
	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("got %d want 303", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/" {
		t.Fatalf("Location: got %q want /", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control: got %q want no-store", got)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("entry response carries a body: %q", rec.Body.String())
	}
}

func TestEnterRequiresDeckOriginAndPost(t *testing.T) {
	g, _, _, _ := newSetup(t)
	entry, err := g.Issue(testProjectA, "operator")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	get := previewRequest(http.MethodGet, testHostA, entryPath, nil)
	recGet := httptest.NewRecorder()
	g.ServeHTTP(recGet, get)
	if recGet.Code != http.StatusMethodNotAllowed || recGet.Header().Get("Allow") != http.MethodPost {
		t.Fatalf("GET entry: got %d Allow %q", recGet.Code, recGet.Header().Get("Allow"))
	}

	for name, origin := range map[string]string{
		"missing": "",
		"foreign": "https://evil.example.com",
		"sibling": "https://" + testHostB,
		"null":    "null",
	} {
		req := previewRequest(http.MethodPost, testHostA, entryPath, strings.NewReader("ticket="+entry.Ticket))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		g.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("origin %s: got %d want 403", name, rec.Code)
		}
	}

	// The refused attempts must not have consumed the ticket: only a
	// successful redemption is single-use.
	if cookie := postTicket(t, g, testHostA, entry.Ticket); cookie.Name != cookieName {
		t.Fatalf("ticket refused spent: got cookie %q", cookie.Name)
	}
}

func TestTicketIsSingleUse(t *testing.T) {
	g, _, _, _ := newSetup(t)
	entry, err := g.Issue(testProjectA, "operator")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	postTicket(t, g, testHostA, entry.Ticket)

	req := previewRequest(http.MethodPost, testHostA, entryPath, strings.NewReader("ticket="+entry.Ticket))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", testDeckOrigin)
	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("replayed ticket: got %d want 403", rec.Code)
	}
}

func TestTicketExpiry(t *testing.T) {
	g, clock, _, _ := newSetup(t)

	fresh, err := g.Issue(testProjectA, "operator")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	clock.Advance(ticketTTL - time.Second)
	req := previewRequest(http.MethodPost, testHostA, entryPath, strings.NewReader("ticket="+fresh.Ticket))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", testDeckOrigin)
	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("ticket one second before expiry: got %d want 303", rec.Code)
	}

	stale, err := g.Issue(testProjectA, "operator")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	clock.Advance(ticketTTL)
	req2 := previewRequest(http.MethodPost, testHostA, entryPath, strings.NewReader("ticket="+stale.Ticket))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req2.Header.Set("Origin", testDeckOrigin)
	rec2 := httptest.NewRecorder()
	g.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusForbidden {
		t.Fatalf("ticket at expiry: got %d want 403", rec2.Code)
	}
}

func TestTicketBoundToItsPreviewHost(t *testing.T) {
	g, _, _, _ := newSetup(t)
	entry, err := g.Issue(testProjectA, "operator")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	req := previewRequest(http.MethodPost, testHostB, entryPath, strings.NewReader("ticket="+entry.Ticket))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", testDeckOrigin)
	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-host ticket: got %d want 403", rec.Code)
	}
}

func TestAnonymousRequestsAreDenied(t *testing.T) {
	g, _, a, b := newSetup(t)
	for _, tc := range []struct{ method, target string }{
		{http.MethodGet, "/"},
		{http.MethodGet, "/assets/app.css"},
		{http.MethodGet, "/api/data"},
		{http.MethodPost, "/api/data"},
		{http.MethodHead, "/"},
		{http.MethodOptions, "/api/data"},
	} {
		rec := authed(t, g, nil, testHostA, tc.method, tc.target, nil)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("anonymous %s %s: got %d want 403", tc.method, tc.target, rec.Code)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("anonymous %s %s: Cache-Control %q", tc.method, tc.target, got)
		}
		if rec.Body.Len() != 0 {
			t.Fatalf("anonymous %s %s carries a body", tc.method, tc.target)
		}
	}
	// A made-up cookie value is not authority either.
	req := previewRequest(http.MethodGet, testHostA, "/", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: "forged"})
	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("forged cookie: got %d want 403", rec.Code)
	}
	if a.count()+b.count() != 0 {
		t.Fatal("an anonymous request reached an upstream")
	}
}

func TestUnknownHostHasNoRoute(t *testing.T) {
	g, _, _, _ := newSetup(t)
	for _, host := range []string{"evil.example.com", testHostA + ".evil.com", "", "localhost"} {
		rec := authed(t, g, nil, host, http.MethodGet, "/", nil)
		if rec.Code != http.StatusMisdirectedRequest {
			t.Fatalf("host %q: got %d want 421", host, rec.Code)
		}
	}
}

func TestProxyFormRequestsAreRejected(t *testing.T) {
	g, _, _, _ := newSetup(t)

	connect := previewRequest(http.MethodConnect, testHostA, "/", nil)
	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, connect)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("CONNECT: got %d want 400", rec.Code)
	}

	absolute := httptest.NewRequest(http.MethodGet, "http://"+testHostA+"/", nil)
	rec2 := httptest.NewRecorder()
	g.ServeHTTP(rec2, absolute)
	if rec2.Code != http.StatusBadRequest {
		t.Fatalf("absolute-form: got %d want 400", rec2.Code)
	}

	asterisk := httptest.NewRequest(http.MethodOptions, "*", nil)
	asterisk.Host = testHostA
	asterisk.RequestURI = "*"
	rec3 := httptest.NewRecorder()
	g.ServeHTTP(rec3, asterisk)
	if rec3.Code != http.StatusBadRequest {
		t.Fatalf("asterisk-form: got %d want 400", rec3.Code)
	}
}

func TestSessionCookieOnlyAuthorizesItsPreviewHost(t *testing.T) {
	g, _, a, b := newSetup(t)
	cookie := enterPreview(t, g, testProjectA, testHostA)
	rec := authed(t, g, cookie, testHostB, http.MethodGet, "/", nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-host session: got %d want 403", rec.Code)
	}
	if a.count() != 0 || b.count() != 0 {
		t.Fatal("a cross-host session reached an upstream")
	}
}

func TestBrowserCrossOriginRules(t *testing.T) {
	g, _, a, _ := newSetup(t)
	cookie := enterPreview(t, g, testProjectA, testHostA)

	cases := []struct {
		name     string
		method   string
		headers  map[string]string
		wantCode int
	}{
		{
			name:     "no same-origin evidence at all",
			method:   http.MethodGet,
			wantCode: http.StatusForbidden,
		},
		{
			name:     "same-origin fetch metadata",
			method:   http.MethodGet,
			headers:  map[string]string{"Sec-Fetch-Site": "same-origin"},
			wantCode: http.StatusOK,
		},
		{
			name:     "same-origin POST carries the app origin",
			method:   http.MethodPost,
			headers:  map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "https://" + testHostA},
			wantCode: http.StatusOK,
		},
		{
			name:     "explicit foreign origin",
			method:   http.MethodGet,
			headers:  map[string]string{"Origin": "https://evil.example.com"},
			wantCode: http.StatusForbidden,
		},
		{
			name:     "origin null",
			method:   http.MethodGet,
			headers:  map[string]string{"Origin": "null"},
			wantCode: http.StatusForbidden,
		},
		{
			name:     "cross-site XHR",
			method:   http.MethodGet,
			headers:  map[string]string{"Sec-Fetch-Site": "cross-site", "Sec-Fetch-Mode": "cors"},
			wantCode: http.StatusForbidden,
		},
		{
			name:     "sibling same-site XHR",
			method:   http.MethodGet,
			headers:  map[string]string{"Sec-Fetch-Site": "same-site", "Sec-Fetch-Mode": "cors"},
			wantCode: http.StatusForbidden,
		},
		{
			name:     "cross-site subresource",
			method:   http.MethodGet,
			headers:  map[string]string{"Sec-Fetch-Site": "cross-site", "Sec-Fetch-Mode": "no-cors", "Sec-Fetch-Dest": "image"},
			wantCode: http.StatusForbidden,
		},
		{
			name:   "top-level GET OAuth navigation",
			method: http.MethodGet,
			headers: map[string]string{
				"Sec-Fetch-Site": "cross-site", "Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "document",
			},
			wantCode: http.StatusOK,
		},
		{
			name:   "cross-site POST navigation is not OAuth",
			method: http.MethodPost,
			headers: map[string]string{
				"Sec-Fetch-Site": "cross-site", "Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "document",
			},
			wantCode: http.StatusForbidden,
		},
		{
			name:     "typed URL navigation",
			method:   http.MethodGet,
			headers:  map[string]string{"Sec-Fetch-Site": "none", "Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "document"},
			wantCode: http.StatusOK,
		},
		{
			name: "no-site POST navigation", method: http.MethodPost,
			headers:  map[string]string{"Sec-Fetch-Site": "none", "Sec-Fetch-Mode": "navigate", "Sec-Fetch-Dest": "document"},
			wantCode: http.StatusForbidden,
		},
		{
			name:     "no-site non-navigation",
			method:   http.MethodGet,
			headers:  map[string]string{"Sec-Fetch-Site": "none", "Sec-Fetch-Mode": "cors"},
			wantCode: http.StatusForbidden,
		},
		{
			name:     "same-origin referer",
			method:   http.MethodGet,
			headers:  map[string]string{"Referer": "https://" + testHostA + "/page"},
			wantCode: http.StatusOK,
		},
		{
			name:     "foreign referer",
			method:   http.MethodGet,
			headers:  map[string]string{"Referer": "https://evil.example.com/"},
			wantCode: http.StatusForbidden,
		},
		{
			name:     "unparseable referer fails closed",
			method:   http.MethodGet,
			headers:  map[string]string{"Referer": "%%% not a url"},
			wantCode: http.StatusForbidden,
		},
		{
			name:     "service worker registration",
			method:   http.MethodGet,
			headers:  map[string]string{"Sec-Fetch-Site": "same-origin", "Service-Worker": "script"},
			wantCode: http.StatusForbidden,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := a.count()
			req := previewRequest(tc.method, testHostA, "/api/thing", nil)
			req.AddCookie(cookie)
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			g.ServeHTTP(rec, req)
			if rec.Code != tc.wantCode {
				t.Fatalf("got %d want %d", rec.Code, tc.wantCode)
			}
			if tc.wantCode == http.StatusForbidden && a.count() != before {
				t.Fatal("a refused request reached the upstream")
			}
		})
	}
}

func TestSessionExpiresAbsolutely(t *testing.T) {
	g, clock, _, _ := newSetup(t)
	cookie := enterPreview(t, g, testProjectA, testHostA)

	clock.Advance(sessionTTL - time.Minute)
	if rec := authed(t, g, cookie, testHostA, http.MethodGet, "/", nil); rec.Code != http.StatusOK {
		t.Fatalf("one minute before expiry: got %d want 200", rec.Code)
	}

	clock.Advance(time.Minute)
	if rec := authed(t, g, cookie, testHostA, http.MethodGet, "/", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("past expiry: got %d want 403", rec.Code)
	}
}

func TestSessionsPerPrincipalAreBounded(t *testing.T) {
	old := maxSessionsPerPrincipal
	maxSessionsPerPrincipal = 2
	t.Cleanup(func() { maxSessionsPerPrincipal = old })

	g, _, _, _ := newSetup(t)
	first := enterPreview(t, g, testProjectA, testHostA)
	enterPreview(t, g, testProjectA, testHostA)
	third := enterPreview(t, g, testProjectA, testHostA)

	if rec := authed(t, g, first, testHostA, http.MethodGet, "/", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("oldest session survived eviction: got %d want 403", rec.Code)
	}
	if rec := authed(t, g, third, testHostA, http.MethodGet, "/", nil); rec.Code != http.StatusOK {
		t.Fatalf("newest session refused: got %d want 200", rec.Code)
	}
}

func TestOutstandingTicketsAreBounded(t *testing.T) {
	old := maxOutstandingTickets
	maxOutstandingTickets = 2
	t.Cleanup(func() { maxOutstandingTickets = old })

	g, _, _, _ := newSetup(t)
	first, err := g.Issue(testProjectA, "operator")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	g.Issue(testProjectA, "operator")
	third, err := g.Issue(testProjectA, "operator")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	req := previewRequest(http.MethodPost, testHostA, entryPath, strings.NewReader("ticket="+first.Ticket))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", testDeckOrigin)
	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("evicted ticket still redeemable: got %d want 403", rec.Code)
	}
	if cookie := postTicket(t, g, testHostA, third.Ticket); cookie.Name != cookieName {
		t.Fatalf("newest ticket refused: got cookie %q", cookie.Name)
	}
}

func TestRevokeEndsSessionsAndTickets(t *testing.T) {
	g, _, _, _ := newSetup(t)
	cookie := enterPreview(t, g, testProjectA, testHostA)
	entry, err := g.Issue(testProjectA, "operator")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	g.Revoke("operator")

	if rec := authed(t, g, cookie, testHostA, http.MethodGet, "/", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("session survived revoke: got %d want 403", rec.Code)
	}
	req := previewRequest(http.MethodPost, testHostA, entryPath, strings.NewReader("ticket="+entry.Ticket))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", testDeckOrigin)
	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("ticket survived revoke: got %d want 403", rec.Code)
	}

	// Reopening through Deck still works after revocation.
	if rec := authed(t, g, enterPreview(t, g, testProjectA, testHostA), testHostA, http.MethodGet, "/", nil); rec.Code != http.StatusOK {
		t.Fatalf("renewed session refused: got %d want 200", rec.Code)
	}
}

func TestClosedGatewayRefusesEverything(t *testing.T) {
	g, _, _, _ := newSetup(t)
	cookie := enterPreview(t, g, testProjectA, testHostA)
	if err := g.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := g.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	rec := authed(t, g, cookie, testHostA, http.MethodGet, "/", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("after Close: got %d want 503", rec.Code)
	}
}
