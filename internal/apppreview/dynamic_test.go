package apppreview

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRegisterAddsRoutesWithoutRestartAndPreservesUnchangedSessions(t *testing.T) {
	a := newCaptureUpstream(t, "a:", nil)
	g, _ := newTestGateway(t, testDeckOrigin)
	registration := Target{ProjectID: testProjectA, Origin: "https://" + testHostA, Upstream: a.addr()}
	if err := g.Register(registration); err != nil {
		t.Fatal(err)
	}
	cookie := enterPreview(t, g, testProjectA, testHostA)
	entry, err := g.Issue(testProjectA, "operator")
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Register(registration); err != nil {
		t.Fatal(err)
	}
	if !g.HasProject(testProjectA) {
		t.Fatal("registered project is missing")
	}
	if res := authed(t, g, cookie, testHostA, http.MethodGet, "/asset.js", nil); res.Code != http.StatusOK || res.Body.String() != "a:/asset.js" {
		t.Fatalf("unchanged registration lost its session: %d %q", res.Code, res.Body.String())
	}
	postTicket(t, g, testHostA, entry.Ticket)
}

func TestRegisterRejectsConflictsWithoutChangingExistingRoute(t *testing.T) {
	g, _, a, _ := newSetup(t)
	cookie := enterPreview(t, g, testProjectA, testHostA)
	for name, registration := range map[string]Target{
		"project":         {ProjectID: testProjectA, Origin: "https://new.example.net", Upstream: "127.0.0.1:19001"},
		"origin":          {ProjectID: "new", Origin: "https://" + testHostA, Upstream: "127.0.0.1:19001"},
		"host other port": {ProjectID: "new", Origin: "https://" + testHostA + ":8443", Upstream: "127.0.0.1:19001"},
		"upstream":        {ProjectID: "new", Origin: "https://new.example.net", Upstream: a.addr()},
		"external target": {ProjectID: "new", Origin: "https://new.example.net", Upstream: "192.0.2.1:19001"},
		"deck origin":     {ProjectID: "new", Origin: testDeckOrigin, Upstream: "127.0.0.1:19001"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := g.Register(registration); err == nil {
				t.Fatal("conflicting or invalid registration succeeded")
			}
		})
	}
	if res := authed(t, g, cookie, testHostA, http.MethodGet, "/", nil); res.Code != http.StatusOK || res.Body.String() != "a:/" {
		t.Fatalf("rejected registration changed the route: %d %q", res.Code, res.Body.String())
	}
	if g.HasProject("new") {
		t.Fatal("rejected registration was installed")
	}
}

func TestStaticTargetsCannotShareHostnameAcrossPorts(t *testing.T) {
	_, err := New(Config{DeckOrigins: []string{testDeckOrigin}, Targets: []Target{
		{ProjectID: testProjectA, Origin: "https://" + testHostA, Upstream: "127.0.0.1:19001"},
		{ProjectID: testProjectB, Origin: "https://" + testHostA + ":8443", Upstream: "127.0.0.1:19002"},
	}})
	if err == nil {
		t.Fatal("two projects sharing a cookie hostname were accepted")
	}
}

func TestUnregisterRevokesAllProjectAuthorityAndSparesSibling(t *testing.T) {
	g, _, a, b := newSetup(t)
	cookie := enterPreview(t, g, testProjectA, testHostA)
	otherEntry, err := g.Issue(testProjectA, "other-principal")
	if err != nil {
		t.Fatal(err)
	}
	otherCookie := postTicket(t, g, testHostA, otherEntry.Ticket)
	entry, err := g.Issue(testProjectA, "operator")
	if err != nil {
		t.Fatal(err)
	}
	siblingCookie := enterPreview(t, g, testProjectB, testHostB)
	g.Unregister(testProjectA)
	g.Unregister(testProjectA)
	if g.HasProject(testProjectA) {
		t.Fatal("unregistered project still has a route")
	}
	if _, err := g.Issue(testProjectA, "operator"); err == nil {
		t.Fatal("unregistered project issued a ticket")
	}
	if res := authed(t, g, cookie, testHostA, http.MethodGet, "/", nil); res.Code != http.StatusMisdirectedRequest {
		t.Fatalf("removed host returned %d, want 421", res.Code)
	}
	if err := g.Register(testTargets(a.addr(), b.addr())[0]); err != nil {
		t.Fatal(err)
	}
	for _, oldCookie := range []*http.Cookie{cookie, otherCookie} {
		if res := authed(t, g, oldCookie, testHostA, http.MethodGet, "/", nil); res.Code != http.StatusForbidden {
			t.Fatalf("old session survived re-registration: %d", res.Code)
		}
	}
	req := previewRequest(http.MethodPost, testHostA, entryPath, strings.NewReader("ticket="+entry.Ticket))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", testDeckOrigin)
	res := httptest.NewRecorder()
	g.ServeHTTP(res, req)
	if res.Code != http.StatusForbidden {
		t.Fatalf("old ticket survived re-registration: %d", res.Code)
	}
	if res := authed(t, g, siblingCookie, testHostB, http.MethodGet, "/", nil); res.Code != http.StatusOK || res.Body.String() != "b:/" {
		t.Fatalf("unregister disturbed sibling: %d %q", res.Code, res.Body.String())
	}
	if a.count() != 0 {
		t.Fatal("revoked requests reached the upstream")
	}
}

func TestStaleRouteCannotUseNewRegistrationAuthority(t *testing.T) {
	g, _, _, _ := newSetup(t)
	g.mu.Lock()
	oldTarget := g.byProject[testProjectA]
	g.mu.Unlock()
	g.Unregister(testProjectA)
	newUpstream := newCaptureUpstream(t, "new:", nil)
	if err := g.Register(Target{ProjectID: testProjectA, Origin: "https://" + testHostA, Upstream: newUpstream.addr()}); err != nil {
		t.Fatal(err)
	}
	// A request may already have resolved its target before disable/re-enable.
	// It must not acquire authority for that old target from a new credential.
	entry, err := g.Issue(testProjectA, "operator")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := g.consumeTicket(entry.Ticket, oldTarget); ok {
		t.Fatal("stale target redeemed a new ticket")
	}
	cookie := enterPreview(t, g, testProjectA, testHostA)
	req := previewRequest(http.MethodGet, testHostA, "/", nil)
	req.AddCookie(cookie)
	if s := g.authorize(req, oldTarget); s != nil {
		t.Fatal("stale target accepted a new session")
	}
	if res := authed(t, g, cookie, testHostA, http.MethodGet, "/", nil); res.Code != http.StatusOK || res.Body.String() != "new:/" {
		t.Fatalf("fresh registration failed: %d %q", res.Code, res.Body.String())
	}
}

func TestUnregisterClosesWebSocketBeforeReturning(t *testing.T) {
	s := newWsSetup(t)
	conn, resp, err := s.dial(t, "https://"+testHostA, true)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	s.g.Unregister(testProjectA)
	if !wsClosedWithin(t, conn, time.Second) {
		t.Fatal("unregister returned with a usable websocket")
	}
	s.up.alive(t)
}

func TestUnregisterClosesStreamingResponse(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "stream started\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer up.Close()
	g, _ := newTestGateway(t, testDeckOrigin, Target{ProjectID: testProjectA, Origin: "https://" + testHostA, Upstream: strings.TrimPrefix(up.URL, "http://")})
	cookie := enterPreview(t, g, testProjectA, testHostA)
	server := httptest.NewServer(g)
	defer server.Close()
	defer g.Close()
	req, err := http.NewRequest(http.MethodGet, server.URL+"/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = testHostA
	req.Header.Set("Origin", "https://"+testHostA)
	req.AddCookie(cookie)
	client := &http.Client{Timeout: 5 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	buf := make([]byte, len("stream started\n"))
	if _, err := io.ReadFull(res.Body, buf); err != nil {
		t.Fatal(err)
	}
	g.Unregister(testProjectA)
	started := time.Now()
	if _, err := res.Body.Read(make([]byte, 1)); err == nil {
		t.Fatal("stream still readable after unregister")
	}
	if time.Since(started) >= time.Second {
		t.Fatal("unregister did not close the streaming response")
	}
}

type delayedCloseConn struct {
	net.Conn
	started chan struct{}
	release chan struct{}
}

func (c *delayedCloseConn) Close() error {
	close(c.started)
	<-c.release
	return c.Conn.Close()
}

func TestUnregisterWaitsForEarlierSessionRevocation(t *testing.T) {
	g, _, _, _ := newSetup(t)
	cookie := enterPreview(t, g, testProjectA, testHostA)
	g.mu.Lock()
	s := g.sessions[tokenHash(cookie.Value)]
	g.mu.Unlock()
	local, peer := net.Pipe()
	defer peer.Close()
	conn := &delayedCloseConn{Conn: local, started: make(chan struct{}), release: make(chan struct{})}
	s.track(conn)
	var release sync.Once
	defer release.Do(func() { close(conn.release) })
	revoked := make(chan struct{})
	go func() {
		g.Revoke("operator")
		close(revoked)
	}()
	<-conn.started
	unregistered := make(chan struct{})
	go func() {
		g.Unregister(testProjectA)
		close(unregistered)
	}()
	select {
	case <-unregistered:
		t.Fatal("unregister returned before a removed session's connection closed")
	case <-time.After(50 * time.Millisecond):
	}
	release.Do(func() { close(conn.release) })
	select {
	case <-unregistered:
	case <-time.After(time.Second):
		t.Fatal("unregister did not finish after connection closure")
	}
	<-revoked
}

func TestDynamicRoutesConcurrentWithAuthorization(t *testing.T) {
	g, _, a, b := newSetup(t)
	registration := testTargets(a.addr(), b.addr())[0]
	start := make(chan struct{})
	var wg sync.WaitGroup
	for worker := 0; worker < 5; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < 100; i++ {
				g.HasProject(testProjectA)
				entry, err := g.Issue(testProjectA, "operator")
				if err != nil {
					continue
				}
				req := previewRequest(http.MethodPost, testHostA, entryPath, strings.NewReader("ticket="+entry.Ticket))
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				req.Header.Set("Origin", testDeckOrigin)
				res := httptest.NewRecorder()
				g.ServeHTTP(res, req)
				for _, cookie := range res.Result().Cookies() {
					request := previewRequest(http.MethodGet, testHostA, "/", nil)
					request.AddCookie(cookie)
					request.Header.Set("Origin", "https://"+testHostA)
					g.ServeHTTP(httptest.NewRecorder(), request)
				}
			}
		}()
	}
	close(start)
	for i := 0; i < 100; i++ {
		g.Unregister(testProjectA)
		if err := g.Register(registration); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
	if !g.HasProject(testProjectB) {
		t.Fatal("concurrent mutations removed the sibling route")
	}
}

func TestClosedGatewayCannotRegisterOrRedeem(t *testing.T) {
	g, _, a, b := newSetup(t)
	entry, err := g.Issue(testProjectA, "operator")
	if err != nil {
		t.Fatal(err)
	}
	g.mu.Lock()
	target := g.byProject[testProjectA]
	g.mu.Unlock()
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
	if err := g.Register(testTargets(a.addr(), b.addr())[0]); err == nil {
		t.Fatal("closed gateway accepted registration")
	}
	if _, ok := g.consumeTicket(entry.Ticket, target); ok {
		t.Fatal("closed gateway redeemed a ticket")
	}
	if g.HasProject(testProjectA) {
		t.Fatal("closed gateway reported an available project")
	}
	g.Unregister(testProjectA)
}
