package apppreview

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// pinnedTransport dials one fixed listener whatever URL the request names,
// so a test can send a configured preview Host through a real server.
func pinnedTransport(addr string) *http.Transport {
	return &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", addr)
		},
	}
}

// wsUpstream is a dev server with a WebSocket echo endpoint.
type wsUpstream struct {
	server *httptest.Server

	mu        sync.Mutex
	handshake http.Header
	host      string
}

func newWsUpstream(t *testing.T) *wsUpstream {
	t.Helper()
	u := &wsUpstream{}
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	u.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ws" {
			w.WriteHeader(http.StatusOK)
			io.WriteString(w, "alive")
			return
		}
		u.mu.Lock()
		u.handshake = r.Header.Clone()
		u.host = r.Host
		u.mu.Unlock()
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			kind, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if err := conn.WriteMessage(kind, msg); err != nil {
				return
			}
		}
	}))
	t.Cleanup(u.server.Close)
	return u
}

func (u *wsUpstream) addr() string {
	return strings.TrimPrefix(u.server.URL, "http://")
}

// alive reports whether the dev server still serves after gateway-side
// teardown: closing preview sessions must never kill it.
func (u *wsUpstream) alive(t *testing.T) {
	t.Helper()
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Get(u.server.URL + "/health")
	if err != nil {
		t.Fatalf("dev server died: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "alive" {
		t.Fatalf("dev server unhealthy: %d %q", resp.StatusCode, body)
	}
}

// wsSetup wires a real gateway listener in front of a wsUpstream, redeems
// a session cookie through that listener, and exposes both sides.
type wsSetup struct {
	g      *Gateway
	gwAddr string
	clock  *fakeClock
	cookie *http.Cookie
	up     *wsUpstream
}

func newWsSetup(t *testing.T) *wsSetup {
	t.Helper()
	up := newWsUpstream(t)
	g, clock := newTestGateway(t, testDeckOrigin, testTargets(up.addr(), "127.0.0.1:18082")...)
	gw := httptest.NewServer(g)
	t.Cleanup(gw.Close)
	s := &wsSetup{g: g, gwAddr: strings.TrimPrefix(gw.URL, "http://"), clock: clock, up: up}

	entry, err := g.Issue(testProjectA, "operator")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	form := url.Values{"ticket": {entry.Ticket}}
	req, err := http.NewRequest(http.MethodPost, "http://"+testHostA+entryPath, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", testDeckOrigin)
	client := &http.Client{
		Transport:     pinnedTransport(s.gwAddr),
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("entry through the listener: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("entry through the listener: got %d want 303", resp.StatusCode)
	}
	cookies := resp.Cookies()
	if len(cookies) != 1 {
		t.Fatalf("entry set %d cookies, want 1", len(cookies))
	}
	s.cookie = cookies[0]
	return s
}

// dial opens a WebSocket through the gateway listener with the given Origin
// and optional preview cookie.
func (s *wsSetup) dial(t *testing.T, origin string, withCookie bool) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	d := &websocket.Dialer{
		NetDial: func(_, _ string) (net.Conn, error) {
			return net.DialTimeout("tcp", s.gwAddr, 5*time.Second)
		},
		Proxy:            func(*http.Request) (*url.URL, error) { return nil, nil },
		HandshakeTimeout: 5 * time.Second,
	}
	header := http.Header{}
	if origin != "" {
		header.Set("Origin", origin)
	}
	if withCookie {
		header.Set("Cookie", s.cookie.Name+"="+s.cookie.Value)
	}
	return d.Dial("ws://"+testHostA+"/ws", header)
}

// wsClosedWithin reports whether an established echo connection has died
// within d; a probe round-trip proves it is still serving.
func wsClosedWithin(t *testing.T, c *websocket.Conn, d time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(d)
	if err := c.WriteControl(websocket.PingMessage, nil, deadline); err != nil {
		return true
	}
	c.SetReadDeadline(deadline)
	if err := c.WriteMessage(websocket.TextMessage, []byte("probe")); err != nil {
		return true
	}
	if _, _, err := c.ReadMessage(); err != nil {
		return !errors.Is(err, os.ErrDeadlineExceeded)
	}
	return false
}

func waitForClosed(t *testing.T, c *websocket.Conn, total time.Duration) {
	t.Helper()
	deadline := time.Now().Add(total)
	for time.Now().Before(deadline) {
		if wsClosedWithin(t, c, time.Second) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("websocket stayed open past the deadline")
}

func TestWebSocketProxiesWithExactOrigin(t *testing.T) {
	s := newWsSetup(t)
	conn, resp, err := s.dial(t, "https://"+testHostA, true)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("dial with exact origin: %v (resp %v)", err, resp)
	}
	defer conn.Close()
	if err := conn.WriteMessage(websocket.TextMessage, []byte("hmr")); err != nil {
		t.Fatalf("write: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(msg) != "hmr" {
		t.Fatalf("echo: got %q want hmr", msg)
	}
}

func TestWebSocketRefusesForeignAndMissingOrigin(t *testing.T) {
	s := newWsSetup(t)
	for name, origin := range map[string]string{
		"foreign": "https://evil.example.com",
		"sibling": "https://" + testHostB,
		"deck":    testDeckOrigin,
		"missing": "",
		"null":    "null",
	} {
		_, resp, err := s.dial(t, origin, true)
		if resp != nil {
			_ = resp.Body.Close()
		}
		if err == nil {
			t.Fatalf("origin %s: upgrade succeeded", name)
		}
		if resp == nil || resp.StatusCode != http.StatusForbidden {
			t.Fatalf("origin %s: got resp %v want 403", name, resp)
		}
	}

	// Anonymous handshakes never reach the origin check.
	_, resp, err := s.dial(t, "https://"+testHostA, false)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err == nil {
		t.Fatal("anonymous websocket upgraded")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("anonymous websocket: got resp %v want 403", resp)
	}
}

func TestRevokeClosesEstablishedWebSocket(t *testing.T) {
	s := newWsSetup(t)
	conn, resp, err := s.dial(t, "https://"+testHostA, true)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	s.g.Revoke("operator")
	waitForClosed(t, conn, 5*time.Second)
	s.up.alive(t)
}

func TestExpiryClosesEstablishedWebSocket(t *testing.T) {
	s := newWsSetup(t)
	conn, resp, err := s.dial(t, "https://"+testHostA, true)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	s.clock.Advance(sessionTTL + time.Minute)
	waitForClosed(t, conn, 5*time.Second)
}

func TestGatewayCloseClosesWebSocketAndSparesUpstream(t *testing.T) {
	s := newWsSetup(t)
	conn, resp, err := s.dial(t, "https://"+testHostA, true)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if err := s.g.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	waitForClosed(t, conn, 5*time.Second)
	s.up.alive(t)

	req, _ := http.NewRequest(http.MethodGet, "http://"+testHostA+"/", nil)
	req.AddCookie(s.cookie)
	client := &http.Client{Transport: pinnedTransport(s.gwAddr)}
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("request after Close: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("request after Close: got %d want 503", resp.StatusCode)
	}
}

func TestUpstreamHandshakeSeesOnlyAppCookies(t *testing.T) {
	s := newWsSetup(t)
	header := http.Header{}
	header.Set("Origin", "https://"+testHostA)
	header.Set("Cookie", s.cookie.Name+"="+s.cookie.Value+"; __Host-noisedeck_session=deck; app_ws=1")
	d := &websocket.Dialer{
		NetDial: func(_, _ string) (net.Conn, error) {
			return net.DialTimeout("tcp", s.gwAddr, 5*time.Second)
		},
		Proxy:            func(*http.Request) (*url.URL, error) { return nil, nil },
		HandshakeTimeout: 5 * time.Second,
	}
	conn, resp, err := d.Dial("ws://"+testHostA+"/ws", header)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	conn.Close()

	s.up.mu.Lock()
	defer s.up.mu.Unlock()
	if got := s.up.handshake.Get("Cookie"); got != "app_ws=1" {
		t.Fatalf("upstream handshake Cookie: got %q want app_ws=1", got)
	}
	if s.up.host != testHostA {
		t.Fatalf("upstream handshake Host: got %q want %q", s.up.host, testHostA)
	}
}
