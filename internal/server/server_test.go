package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

func TestHarnessLoginSubmissionNeverEntersRequestLog(t *testing.T) {
	var logs bytes.Buffer
	s := New(Config{AllowedOrigins: []string{"https://deck.example"}}, zerolog.New(&logs), nil)
	s.Handle("POST /api/harness-login", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusConflict)
	}))
	request := httptest.NewRequest("POST", "/api/harness-login?ignored=query-canary", strings.NewReader(`{"callback":"http://localhost:1455/auth/callback?code=callback-canary&state=state-canary"}`))
	request.Header.Set("Origin", "https://deck.example")
	request.Header.Set(RequestIDHeader, "header-canary")
	response := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusConflict || !strings.Contains(logs.String(), "/api/harness-login") {
		t.Fatal("request did not traverse the logging middleware")
	}
	if strings.Contains(logs.String(), "canary") {
		t.Fatal("callback, query or caller header entered application log")
	}
}

func newTestServer(t *testing.T, checks []Check) *Server {
	t.Helper()
	return New(Config{
		Bind:           "127.0.0.1:0",
		AllowedOrigins: []string{"http://127.0.0.1:8837"},
	}, zerolog.Nop(), checks)
}

// do runs a request through the full middleware chain, exactly as a real
// request would travel.
func do(t *testing.T, s *Server, method, path string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(rec, req)
	return rec
}

// Liveness is process liveness only: true even when readiness is
// false and every deep check would fail.
func TestLivezAlwaysOK(t *testing.T) {
	s := newTestServer(t, []Check{{Name: "always-broken", Probe: func(context.Context) error {
		return errors.New("down")
	}}})
	if rec := do(t, s, http.MethodGet, "/livez", nil); rec.Code != http.StatusOK {
		t.Fatalf("livez = %d, want 200", rec.Code)
	}
}

func TestReadyzGatesOnFlagAndChecks(t *testing.T) {
	probeErr := error(nil)
	s := newTestServer(t, []Check{{Name: "db-writable", Probe: func(context.Context) error {
		return probeErr
	}}})

	// Before the startup sequence flips readiness: 503.
	if rec := do(t, s, http.MethodGet, "/readyz", nil); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz before ready = %d, want 503", rec.Code)
	}

	s.SetReady(true)
	if rec := do(t, s, http.MethodGet, "/readyz", nil); rec.Code != http.StatusOK {
		t.Fatalf("readyz when ready = %d, want 200", rec.Code)
	}

	// A failing deep check un-readies with the probe NAME only — no error
	// detail in the response.
	probeErr = errors.New("disk on fire: /var/secret/path")
	rec := do(t, s, http.MethodGet, "/readyz", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz with failing check = %d, want 503", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "db-writable") {
		t.Fatalf("failure must name the probe: %q", body)
	}
	if strings.Contains(body, "secret") {
		t.Fatalf("probe error detail leaked into the response: %q", body)
	}
}

func TestReadyzRunsChecksConcurrently(t *testing.T) {
	started := make(chan string, 2)
	release := make(chan struct{})
	probe := func(name string) Check {
		return Check{Name: name, Probe: func(context.Context) error {
			started <- name
			<-release
			return nil
		}}
	}
	s := newTestServer(t, []Check{probe("first"), probe("second")})
	s.SetReady(true)

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- do(t, s, http.MethodGet, "/readyz", nil) }()
	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("readiness checks did not start concurrently")
		}
	}
	close(release)
	if rec := <-done; rec.Code != http.StatusOK {
		t.Fatalf("readyz = %d, want 200", rec.Code)
	}
}

func TestReadyzReportsFirstConfiguredFailure(t *testing.T) {
	firstRelease := make(chan struct{})
	s := newTestServer(t, []Check{
		{Name: "first", Probe: func(context.Context) error {
			<-firstRelease
			return errors.New("first failed")
		}},
		{Name: "second", Probe: func(context.Context) error {
			close(firstRelease)
			return errors.New("second failed first")
		}},
	})
	s.SetReady(true)
	rec := do(t, s, http.MethodGet, "/readyz", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz = %d, want 503", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "first") || strings.Contains(body, "second") {
		t.Fatalf("readyz reported failures out of configured order: %q", body)
	}
}

func TestReadyzProbePanicUsesServerRecovery(t *testing.T) {
	s := newTestServer(t, []Check{{Name: "panics", Probe: func(context.Context) error {
		panic("probe panic")
	}}})
	s.SetReady(true)
	if rec := do(t, s, http.MethodGet, "/readyz", nil); rec.Code != http.StatusInternalServerError {
		t.Fatalf("panicking readyz = %d, want 500", rec.Code)
	}
	if rec := do(t, s, http.MethodGet, "/livez", nil); rec.Code != http.StatusOK {
		t.Fatalf("server unusable after recovered readiness panic: %d", rec.Code)
	}
}

func TestPanicRecoveredWithoutKillingServer(t *testing.T) {
	s := newTestServer(t, nil)
	s.Handle("GET /boom", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("kaboom")
	}))
	if rec := do(t, s, http.MethodGet, "/boom", nil); rec.Code != http.StatusInternalServerError {
		t.Fatalf("panic route = %d, want 500", rec.Code)
	}
	if rec := do(t, s, http.MethodGet, "/livez", nil); rec.Code != http.StatusOK {
		t.Fatalf("server unusable after recovered panic: %d", rec.Code)
	}
}

func TestRequestIDAssignedAndInboundIgnored(t *testing.T) {
	s := newTestServer(t, nil)
	var seen string
	s.Handle("GET /id", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = RequestID(r.Context())
	}))
	rec := do(t, s, http.MethodGet, "/id", map[string]string{RequestIDHeader: "attacker-chosen"})
	got := rec.Header().Get(RequestIDHeader)
	if got == "" || got == "attacker-chosen" {
		t.Fatalf("request id = %q; must be assigned server-side", got)
	}
	if seen != got {
		t.Fatalf("context id %q != header id %q", seen, got)
	}
}

func TestOriginGuard(t *testing.T) {
	s := newTestServer(t, nil)
	s.Handle("POST /api/thing", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	cases := []struct {
		name   string
		method string
		origin string
		want   int
	}{
		{"post allowed origin", http.MethodPost, "http://127.0.0.1:8837", http.StatusNoContent},
		{"post foreign origin", http.MethodPost, "https://evil.example", http.StatusForbidden},
		// The rejection itself must carry a request id — the guard sits
		// inside the id/log wrappers, not outside them.
		{"post no origin (non-browser)", http.MethodPost, "", http.StatusNoContent},
		{"get foreign origin (safe method)", http.MethodGet, "https://evil.example", http.StatusOK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hdr := map[string]string{}
			if c.origin != "" {
				hdr["Origin"] = c.origin
			}
			path := "/api/thing"
			if c.method == http.MethodGet {
				path = "/livez"
			}
			rec := do(t, s, c.method, path, hdr)
			if rec.Code != c.want {
				t.Fatalf("%s = %d, want %d", c.name, rec.Code, c.want)
			}
			if rec.Header().Get(RequestIDHeader) == "" {
				t.Fatalf("%s: response carries no request id — guard escaped the id middleware", c.name)
			}
		})
	}
}

// http.ResponseController must reach the real writer through the logging
// wrapper — the WebSocket layer hijacks through this chain.
func TestResponseControllerWorksThroughChain(t *testing.T) {
	s := newTestServer(t, nil)
	var flushErr error
	s.Handle("GET /flush", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		flushErr = http.NewResponseController(w).Flush()
	}))
	if rec := do(t, s, http.MethodGet, "/flush", nil); rec.Code != http.StatusOK {
		t.Fatalf("flush route = %d", rec.Code)
	}
	if flushErr != nil {
		t.Fatalf("Flush through the middleware chain failed: %v", flushErr)
	}
}

// The log must record the status the client received: the first FINAL
// status — not a later superfluous call, and never an informational 1xx.
func TestStatusWriterRecordsFirstFinalStatus(t *testing.T) {
	sw := &statusWriter{ResponseWriter: httptest.NewRecorder(), status: http.StatusOK}
	sw.WriteHeader(http.StatusEarlyHints) // 103: forwarded, not recorded
	sw.WriteHeader(http.StatusNoContent)
	sw.WriteHeader(http.StatusInternalServerError)
	if sw.status != http.StatusNoContent {
		t.Fatalf("recorded status = %d, want first final status %d", sw.status, http.StatusNoContent)
	}
}

// A body write commits an implicit 200 on the wire; a WriteHeader after it
// is ignored by net/http and must not change what the log reports.
func TestStatusWriterBodyWriteCommits200(t *testing.T) {
	sw := &statusWriter{ResponseWriter: httptest.NewRecorder(), status: http.StatusOK}
	_, _ = sw.Write([]byte("body"))
	sw.WriteHeader(http.StatusInternalServerError)
	if sw.status != http.StatusOK {
		t.Fatalf("recorded status = %d, want the implicit 200 the wire carried", sw.status)
	}
}

// The pinned gorilla/websocket upgrader asserts http.Hijacker DIRECTLY on
// the writer it receives; the chain must satisfy that assertion over a real
// socket, not just the ResponseController path.
// lockedBuf makes log capture race-safe: request goroutines write log lines
// and a hijacked handler can outlive Shutdown (hijacked conns are not
// tracked by it), so reads and writes genuinely overlap.
type lockedBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func TestDirectFlusherAssertionThroughChain(t *testing.T) {
	logBuf := &lockedBuf{}
	s := New(Config{
		Bind:           "127.0.0.1:0",
		AllowedOrigins: []string{"http://127.0.0.1:8837"},
	}, zerolog.New(logBuf), nil)
	// The handler stays open until the client has read the flushed event,
	// so the body only reaches the client if Flush really pushes it: a
	// Flush that does nothing leaves the bytes buffered until the handler
	// returns, and the client's read deadline expires first.
	clientRead := make(chan struct{})
	s.Handle("GET /stream", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		f.Flush()
		_, _ = w.Write([]byte("event: ping\ndata: 1\n\n"))
		f.Flush()
		select {
		case <-clientRead:
		case <-r.Context().Done():
		}
		// Too late to change the wire status; the log must keep the 200
		// the first Flush committed.
		w.WriteHeader(http.StatusInternalServerError)
	}))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for s.BoundAddr() == "" {
		if time.Now().After(deadline) {
			t.Fatal("never bound")
		}
		time.Sleep(10 * time.Millisecond)
	}

	resp, err := (&http.Client{Timeout: 5 * time.Second}).Get(fmt.Sprintf("http://%s/stream", s.BoundAddr()))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: the chain must expose Flusher to stream handlers", resp.StatusCode)
	}
	type read struct {
		body string
		err  error
	}
	got := make(chan read, 1)
	go func(body io.Reader) {
		buf := make([]byte, 64)
		n, err := body.Read(buf)
		got <- read{string(buf[:n]), err}
	}(resp.Body)
	select {
	case r := <-got:
		if r.err != nil || r.body != "event: ping\ndata: 1\n\n" {
			t.Fatalf("first read = %q, %v", r.body, r.err)
		}
	case <-time.After(3 * time.Second):
		close(clientRead)
		t.Fatal("the flushed event never reached the client while the handler was still open")
	}
	close(clientRead)
	cancel()
	<-done
	if !strings.Contains(logBuf.String(), `"status":200`) {
		t.Fatalf("log did not record the streamed 200: %s", logBuf.String())
	}
}

func TestDrainEndsOpenStreams(t *testing.T) {
	s := New(Config{
		Bind:           "127.0.0.1:0",
		AllowedOrigins: []string{"http://127.0.0.1:8837"},
	}, zerolog.New(&lockedBuf{}), nil)
	streaming := make(chan struct{})
	s.Handle("GET /stream", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		close(streaming)
		<-r.Context().Done()
	}))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for s.BoundAddr() == "" {
		if time.Now().After(deadline) {
			t.Fatal("never bound")
		}
		time.Sleep(10 * time.Millisecond)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Get(fmt.Sprintf("http://%s/stream", s.BoundAddr()))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	<-streaming

	start := time.Now()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run = %v: an open stream must not fail the drain", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the drain waited on an open stream instead of ending it")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("drain took %s with one open stream", elapsed)
	}
}

func TestDrainLetsInFlightRequestsFinish(t *testing.T) {
	s := New(Config{
		Bind:           "127.0.0.1:0",
		AllowedOrigins: []string{"http://127.0.0.1:8837"},
	}, zerolog.New(&lockedBuf{}), nil)
	entered := make(chan struct{})
	release := make(chan struct{})
	ctxErr := make(chan error, 1)
	s.Handle("POST /work", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		ctxErr <- r.Context().Err()
		w.WriteHeader(http.StatusCreated)
	}))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for s.BoundAddr() == "" {
		if time.Now().After(deadline) {
			t.Fatal("never bound")
		}
		time.Sleep(10 * time.Millisecond)
	}
	status := make(chan int, 1)
	go func() {
		resp, err := (&http.Client{Timeout: 10 * time.Second}).Post(fmt.Sprintf("http://%s/work", s.BoundAddr()), "text/plain", nil)
		if err != nil {
			status <- 0
			return
		}
		resp.Body.Close()
		status <- resp.StatusCode
	}()
	<-entered
	cancel()
	// Give the drain time to start before the handler finishes.
	time.Sleep(200 * time.Millisecond)
	close(release)
	if err := <-ctxErr; err != nil {
		t.Fatalf("an admitted request's context was cancelled by the drain: %v", err)
	}
	if got := <-status; got != http.StatusCreated {
		t.Fatalf("status = %d, want 201", got)
	}
	if err := <-done; err != nil {
		t.Fatalf("Run = %v", err)
	}
}

func TestFlushCommitsTheLoggedStatus(t *testing.T) {
	logBuf := &lockedBuf{}
	s := New(Config{
		Bind:           "127.0.0.1:0",
		AllowedOrigins: []string{"http://127.0.0.1:8837"},
	}, zerolog.New(logBuf), nil)
	s.Handle("GET /flush-only", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.(http.Flusher).Flush()
		// Too late to change the wire status; the log must keep the 200
		// the Flush committed.
		w.WriteHeader(http.StatusInternalServerError)
	}))
	rec := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/flush-only", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("wire status = %d", rec.Code)
	}
	if !strings.Contains(logBuf.String(), `"status":200`) {
		t.Fatalf("log did not keep the flushed 200: %s", logBuf.String())
	}
}

func TestDirectHijackAssertionThroughChain(t *testing.T) {
	logBuf := &lockedBuf{}
	s := New(Config{
		Bind:           "127.0.0.1:0",
		AllowedOrigins: []string{"http://127.0.0.1:8837"},
	}, zerolog.New(logBuf), nil)
	s.Handle("GET /hijack", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		h, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "no hijacker", http.StatusInternalServerError)
			return
		}
		conn, buf, err := h.Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 8\r\n\r\nhijacked")
		_ = buf.Flush()
	}))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for s.BoundAddr() == "" {
		if time.Now().After(deadline) {
			t.Fatal("never bound")
		}
		time.Sleep(10 * time.Millisecond)
	}

	resp, err := http.Get(fmt.Sprintf("http://%s/hijack", s.BoundAddr()))
	if err != nil {
		t.Fatalf("hijack request: %v", err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(b) != "hijacked" {
		t.Fatalf("hijacked response = %d %q, want 200 \"hijacked\"", resp.StatusCode, b)
	}
	cancel()
	<-done

	// The log must report the protocol switch, not a default 200: writes
	// after a hijack bypass WriteHeader entirely. The handler goroutine can
	// outlive Shutdown, so poll rather than assert instantly.
	logDeadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(logBuf.String(), `"status":101`) {
		if time.Now().After(logDeadline) {
			t.Fatalf("hijacked request not logged as 101:\n%s", logBuf.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Run over a real socket: serve, hit /livez, cancel, and confirm a clean
// graceful exit.
func TestRunServesAndShutsDownGracefully(t *testing.T) {
	s := newTestServer(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	// The port is kernel-chosen (:0); wait for the bound address, then hit
	// the real socket.
	var resp *http.Response
	deadline := time.Now().Add(5 * time.Second)
	for {
		var err error
		if addr := s.BoundAddr(); addr != "" {
			resp, err = http.Get(fmt.Sprintf("http://%s/livez", addr))
			if err == nil {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("server never answered: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(b), "ok") {
		t.Fatalf("livez over socket = %d %q", resp.StatusCode, b)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v on graceful shutdown", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not exit after cancellation")
	}
}
