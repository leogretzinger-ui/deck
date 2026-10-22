package githubconnect

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"

	"github.com/noisefloor-oss/deck/internal/auth"
	"github.com/noisefloor-oss/deck/internal/terminal"
)

const testOrigin = "https://deck.example"

// handlerEnv wires the full backend surface: real manager over a temporary
// tmux namespace, real proxy, fake gh on PATH, trusted-proxy auth wrapper.
type handlerEnv struct {
	mgr *Manager
	h   *Handler
	srv *httptest.Server
}

func newHandlerEnv(t *testing.T) *handlerEnv {
	t.Helper()
	m := newTestManager(t)
	m.statusFn = noAccount
	fakeGhOnPath(t, "printf 'LOGIN-PANE-READY\\n'\nsleep 300\n")
	proxy := m.NewProxy([]string{testOrigin}, zerolog.Nop())
	h := NewHandler(m, proxy, []string{testOrigin})
	authn, err := auth.New(auth.Config{
		Mode: "trusted_proxy", ProxyAddresses: []string{"127.0.0.1"},
		IdentityHeader: "X-Identity",
	}, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	h.Mount(mux.Handle, authn.Require)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &handlerEnv{mgr: m, h: h, srv: srv}
}

func (e *handlerEnv) call(t *testing.T, method, path, principal, origin, csrf string) *httptest.ResponseRecorder {
	t.Helper()
	var body string
	if method == http.MethodPost {
		body = `{"id":"placeholder"}`
	}
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:30000"
	if principal != "" {
		req.Header.Set("X-Identity", principal)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	req.Header.Set(CSRFHeader, csrf)
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	out := httptest.NewRecorder()
	e.srv.Config.Handler.ServeHTTP(out, req)
	return out
}

func (e *handlerEnv) csrfFor(t *testing.T, principal string) string {
	t.Helper()
	out := e.call(t, http.MethodGet, "/api/github/connect/attempt", principal, "", "")
	if out.Code != http.StatusOK {
		t.Fatalf("attempt page: %d", out.Code)
	}
	var page struct {
		CSRF string `json:"csrf"`
	}
	if json.Unmarshal(out.Body.Bytes(), &page) != nil || page.CSRF == "" {
		t.Fatal("missing CSRF token")
	}
	return page.CSRF
}

func TestHandlerStatusIsReadOnlyMetadata(t *testing.T) {
	e := newHandlerEnv(t)
	e.mgr.statusFn = func(context.Context) Result {
		return Result{Outcome: OutcomeAuthenticated, Status: &Status{Account: Account{
			Login: "alice", GitProtocol: "https", State: StateSuccess, Active: true,
			Source: SourceStored, SourceWriteable: true}}}
	}
	out := e.call(t, http.MethodGet, "/api/github/connect/status", "operator-a", "", "")
	if out.Code != http.StatusOK {
		t.Fatalf("status: %d", out.Code)
	}
	var v statusView
	if json.Unmarshal(out.Body.Bytes(), &v) != nil || v.Login != "alice" || v.Source != "stored" {
		t.Fatalf("status view = %s", out.Body.String())
	}
	// Read never launches.
	if sessions, _ := e.mgr.tmux.ListSessions(context.Background()); len(sessions) != 0 {
		t.Fatal("status read launched a login")
	}
}

func TestHandlerStartCSRFAndOriginBoundary(t *testing.T) {
	e := newHandlerEnv(t)
	csrf := e.csrfFor(t, "operator-a")
	for _, c := range []struct {
		name, principal, origin, csrf string
		status                        int
	}{
		{"unauthenticated", "", testOrigin, csrf, http.StatusUnauthorized},
		{"missing-origin", "operator-a", "", csrf, http.StatusForbidden},
		{"foreign-origin", "operator-a", "https://evil.invalid", csrf, http.StatusForbidden},
		{"missing-csrf", "operator-a", testOrigin, "", http.StatusForbidden},
		{"wrong-csrf", "operator-a", testOrigin, strings.Repeat("0", 64), http.StatusForbidden},
	} {
		t.Run(c.name, func(t *testing.T) {
			out := e.call(t, http.MethodPost, "/api/github/connect/start", c.principal, c.origin, c.csrf)
			if out.Code != c.status {
				t.Fatalf("status = %d, want %d", out.Code, c.status)
			}
		})
	}
	if sessions, _ := e.mgr.tmux.ListSessions(context.Background()); len(sessions) != 0 {
		t.Fatal("rejected POST launched a login")
	}
}

func TestHandlerStartAndCurrentAttemptMetadataOnly(t *testing.T) {
	e := newHandlerEnv(t)
	csrf := e.csrfFor(t, "operator-a")
	out := e.call(t, http.MethodPost, "/api/github/connect/start", "operator-a", testOrigin, csrf)
	if out.Code != http.StatusOK {
		t.Fatalf("start: %d %s", out.Code, out.Body.String())
	}
	var meta AttemptMetadata
	if json.Unmarshal(out.Body.Bytes(), &meta) != nil || meta.ID == "" {
		t.Fatalf("start body = %s", out.Body.String())
	}

	// Current returns metadata only — no identity tuple leaks.
	out = e.call(t, http.MethodGet, "/api/github/connect/attempt", "operator-a", "", "")
	var page struct {
		Attempt *AttemptMetadata `json:"attempt"`
	}
	if json.Unmarshal(out.Body.Bytes(), &page) != nil || page.Attempt == nil || page.Attempt.ID != meta.ID {
		t.Fatalf("attempt page = %s", out.Body.String())
	}
	if page.Attempt.Status != AttemptRunning {
		t.Fatalf("attempt status = %s", page.Attempt.Status)
	}
	for _, leak := range []string{"pane", "pid", "marker", "session_id"} {
		if strings.Contains(out.Body.String(), leak) {
			t.Fatalf("attempt metadata leaked %q: %s", leak, out.Body.String())
		}
	}
	// Foreign principal sees no attempt.
	out = e.call(t, http.MethodGet, "/api/github/connect/attempt", "operator-b", "", "")
	if json.Unmarshal(out.Body.Bytes(), &page) != nil || page.Attempt != nil {
		t.Fatalf("foreign principal learned the attempt: %s", out.Body.String())
	}
}

func TestHandlerCancelFlow(t *testing.T) {
	e := newHandlerEnv(t)
	csrf := e.csrfFor(t, "operator-a")
	meta, err := e.mgr.Start(context.Background(), auth.ProxyPrincipal("operator-a"))
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/github/connect/cancel",
		strings.NewReader(`{"id":"`+meta.ID+`"}`))
	req.RemoteAddr = "127.0.0.1:30000"
	req.Header.Set("X-Identity", "operator-a")
	req.Header.Set("Origin", testOrigin)
	req.Header.Set(CSRFHeader, csrf)
	req.Header.Set("Content-Type", "application/json")
	out := httptest.NewRecorder()
	e.srv.Config.Handler.ServeHTTP(out, req)
	if out.Code != http.StatusNoContent {
		t.Fatalf("cancel: %d %s", out.Code, out.Body.String())
	}
	// Unknown id → 404, before any gate work.
	req2 := httptest.NewRequest(http.MethodPost, "/api/github/connect/cancel",
		strings.NewReader(`{"id":"nope"}`))
	req2.RemoteAddr = "127.0.0.1:30000"
	req2.Header.Set("X-Identity", "operator-a")
	req2.Header.Set("Origin", testOrigin)
	req2.Header.Set(CSRFHeader, csrf)
	req2.Header.Set("Content-Type", "application/json")
	out2 := httptest.NewRecorder()
	e.srv.Config.Handler.ServeHTTP(out2, req2)
	if out2.Code != http.StatusNotFound {
		t.Fatalf("unknown cancel: %d", out2.Code)
	}
}

// dialAttempt opens a login-pane attachment over the real proxy.
func (e *handlerEnv) dialAttempt(t *testing.T, principal, id, controller string, takeover bool) (*websocket.Conn, int) {
	t.Helper()
	u := "ws" + strings.TrimPrefix(e.srv.URL, "http") +
		"/api/github/connect/attempt/" + id + "/terminal?controller=" + controller + "&cols=80&rows=24"
	if takeover {
		u += "&takeover=1"
	}
	conn, resp, err := websocket.DefaultDialer.Dial(u, http.Header{"Origin": []string{testOrigin}, "X-Identity": []string{principal}})
	if err != nil && resp == nil {
		t.Fatalf("dial: %v", err)
	}
	status := 0
	if resp != nil {
		status = resp.StatusCode
		_ = resp.Body.Close()
	}
	return conn, status
}

// awaitAttached reads frames until the attached frame carries a token.
func awaitAttached(t *testing.T, c *websocket.Conn) (token int64, generation int) {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		var f struct {
			Type       string `json:"type"`
			Token      int64  `json:"token"`
			Generation int    `json:"generation"`
		}
		if err := c.ReadJSON(&f); err != nil {
			t.Fatalf("read frame: %v", err)
		}
		if f.Type == "attached" {
			return f.Token, f.Generation
		}
	}
}

func TestHandlerTerminalAttachOutputAndCancelRevokesAttachment(t *testing.T) {
	e := newHandlerEnv(t)
	meta, err := e.mgr.Start(context.Background(), auth.ProxyPrincipal("operator-a"))
	if err != nil {
		t.Fatal(err)
	}

	// Foreign owner is refused before the proxy.
	if _, status := e.dialAttempt(t, "operator-b", meta.ID, "browser-b", false); status != http.StatusForbidden {
		t.Fatalf("foreign attach status = %d, want 403", status)
	}
	// Unknown id is refused.
	if _, status := e.dialAttempt(t, "operator-a", "no-such-id", "browser-a", false); status != http.StatusNotFound {
		t.Fatalf("unknown attach status = %d, want 404", status)
	}

	c, status := e.dialAttempt(t, "operator-a", meta.ID, "browser-a", false)
	if status != http.StatusSwitchingProtocols {
		t.Fatalf("attach status = %d", status)
	}
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	var token int64
	var generation int
	var output strings.Builder
	attached := false
	for !attached || !strings.Contains(output.String(), "LOGIN-PANE-READY") {
		var frame terminal.ServerFrame
		if err := c.ReadJSON(&frame); err != nil {
			t.Fatalf("read initial frames: %v", err)
		}
		if frame.Type == "attached" {
			attached = true
			token, generation = frame.Token, frame.Generation
		}
		if frame.Data != "" {
			data, err := base64.StdEncoding.DecodeString(frame.Data)
			if err != nil {
				t.Fatal(err)
			}
			output.Write(data)
		}
	}

	// A stale generation frame is refused: the attachment is closed, not
	// silently applied.
	if err := c.WriteJSON(map[string]any{
		"type": "input", "token": token, "generation": generation + 1, "data": "aGk=",
	}); err != nil {
		t.Fatalf("write stale frame: %v", err)
	}
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	staleSeen := false
	for {
		var f struct {
			Type string `json:"type"`
			Code string `json:"code"`
		}
		if err := c.ReadJSON(&f); err != nil {
			break // closed — expected
		}
		if f.Type == terminal.TypeWriteStatus && f.Code == terminal.WriteFailed {
			staleSeen = true
		}
	}
	if !staleSeen {
		t.Fatal("stale generation frame did not report a failed write")
	}
	c.Close()

	// Reload: a fresh attachment joins the SAME live attempt.
	c2, status := e.dialAttempt(t, "operator-a", meta.ID, "browser-a", false)
	if status != http.StatusSwitchingProtocols {
		t.Fatalf("reload attach status = %d", status)
	}
	defer c2.Close()
	awaitAttached(t, c2)

	// Explicit cancellation revokes the output attachment: the socket
	// closes without the browser leaving first.
	done := make(chan struct{})
	go func() {
		defer close(done)
		c2.SetReadDeadline(time.Now().Add(5 * time.Second))
		for {
			if _, _, err := c2.ReadMessage(); err != nil {
				return
			}
		}
	}()
	csrf := e.csrfFor(t, "operator-a")
	req := httptest.NewRequest(http.MethodPost, "/api/github/connect/cancel", strings.NewReader(`{"id":"`+meta.ID+`"}`))
	req.RemoteAddr = "127.0.0.1:30000"
	req.Header.Set("X-Identity", "operator-a")
	req.Header.Set("Origin", testOrigin)
	req.Header.Set(CSRFHeader, csrf)
	req.Header.Set("Content-Type", "application/json")
	out := httptest.NewRecorder()
	e.srv.Config.Handler.ServeHTTP(out, req)
	if out.Code != http.StatusNoContent {
		t.Fatalf("cancel: %d", out.Code)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not revoke the output attachment")
	}
}

func TestHandlerExplicitTakeoverClosesPreviousBrowser(t *testing.T) {
	e := newHandlerEnv(t)
	meta, err := e.mgr.Start(context.Background(), auth.ProxyPrincipal("operator-a"))
	if err != nil {
		t.Fatal(err)
	}
	first, status := e.dialAttempt(t, "operator-a", meta.ID, "first-browser", false)
	if status != http.StatusSwitchingProtocols {
		t.Fatalf("first attach: %d", status)
	}
	defer first.Close()
	firstToken, _ := awaitAttached(t, first)
	refused, status := e.dialAttempt(t, "operator-a", meta.ID, "second-browser", false)
	if status != http.StatusSwitchingProtocols {
		t.Fatalf("refusal handshake: %d", status)
	}
	defer refused.Close()
	refused.SetReadDeadline(time.Now().Add(5 * time.Second))
	var refusal terminal.ServerFrame
	if err := refused.ReadJSON(&refusal); err != nil {
		t.Fatal(err)
	}
	if refusal.Code != terminal.CodeLeaseHeld {
		t.Fatalf("refusal = %+v", refusal)
	}
	second, status := e.dialAttempt(t, "operator-a", meta.ID, "second-browser", true)
	if status != http.StatusSwitchingProtocols {
		t.Fatalf("takeover attach: %d", status)
	}
	defer second.Close()
	secondToken, _ := awaitAttached(t, second)
	if secondToken <= firstToken {
		t.Fatal("takeover did not advance token")
	}
	first.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		if _, _, err := first.ReadMessage(); err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.ClosePolicyViolation) {
				break
			}
			if ne, ok := err.(interface{ Timeout() bool }); ok && ne.Timeout() {
				t.Fatal("displaced browser remained connected")
			}
			break
		}
	}
	current, ok := e.mgr.Current(context.Background(), auth.ProxyPrincipal("operator-a"))
	if !ok || current.ID != meta.ID || current.Status != AttemptRunning {
		t.Fatalf("takeover replaced native attempt: %+v", current)
	}
}
