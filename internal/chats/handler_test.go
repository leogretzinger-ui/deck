package chats

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/noisefloor-oss/deck/internal/session"
)

type fakeSessions map[string]*session.Session

func (f fakeSessions) Get(_ context.Context, id string) (*session.Session, error) {
	s, ok := f[id]
	if !ok {
		return nil, session.ErrNotFound
	}
	return s, nil
}

// fakeA2A writes a script that records its argv and prints the ledger file
// named by A2A_TEST_LEDGER, so the handler is exercised through a real
// child process with a real timeout and environment.
func fakeA2A(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	ledger := filepath.Join(dir, "ledger.json")
	script := filepath.Join(dir, "a2a")
	body := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$0.calls\"\nprintf '%s\\n' \"$A2A_HANDLE|$A2A_NO_COURIER\" >> \"$0.env\"\ncat \"" + ledger + "\"\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return script, ledger
}

func writeLedger(t *testing.T, path, agent, chats string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(`{"agent":"`+agent+`","chats":`+chats+`}`), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mount(h *Handler) *http.ServeMux {
	mux := http.NewServeMux()
	h.Mount(mux.Handle, func(next http.Handler) http.Handler { return next })
	return mux
}

func TestHandleDerivesTheAgentHandleTheWayA2ADoes(t *testing.T) {
	handle, ok := Handle(&session.Session{ID: "61C37A0C-2158-4134-b99e-0914dc1471ff", Provider: "claude"})
	if !ok || handle != "claude-61c3" {
		t.Fatalf("handle = %q, %v", handle, ok)
	}
	if _, ok := Handle(&session.Session{ID: "8e70aaaa", Provider: "shell"}); ok {
		t.Fatal("a shell session has no chat handle")
	}
}

func TestLedgerRunsA2AForTheSessionAgentReadOnly(t *testing.T) {
	script, ledger := fakeA2A(t)
	writeLedger(t, ledger, "codex-8e70", `[{"id":"chat-1","state":"open","members":["codex-8e70","claude-61c3"],"rows":[]}]`)
	sessions := fakeSessions{"s1": {ID: "8e70bbbb-0000", Provider: "codex"}}
	t.Setenv("A2A_HANDLE", "operator-override")
	h := NewHandler(sessions, script, zerolog.Nop())
	rec := httptest.NewRecorder()
	mount(h).ServeHTTP(rec, httptest.NewRequest("GET", "/api/sessions/s1/chats", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body)
	}
	var got Ledger
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Agent != "codex-8e70" || !strings.Contains(string(got.Chats), `"chat-1"`) {
		t.Fatalf("ledger = %+v", got)
	}
	calls, _ := os.ReadFile(script + ".calls")
	if strings.TrimSpace(string(calls)) != "ledger --for-session 8e70bbbb-0000 --harness codex --compact" {
		t.Fatalf("a2a was invoked with %q; the console only ever reads, by session", calls)
	}
	env, _ := os.ReadFile(script + ".env")
	if strings.TrimSpace(string(env)) != "|1" {
		t.Fatalf("child env = %q; the user's A2A_HANDLE must not leak and no courier may start", env)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("ledger responses are not cacheable")
	}
}

func TestLedgerFollowsTheHandleTheAgentChose(t *testing.T) {
	script, ledger := fakeA2A(t)
	// a2a resolved the session to the name the agent chose with `a2a name`.
	writeLedger(t, ledger, "reviewer-933", `[{"id":"chat-9","state":"open","members":["reviewer-933","codex-8e70"],"rows":[]}]`)
	sessions := fakeSessions{"s1": {ID: "61c37a0c-2158", Provider: "claude"}}
	h := NewHandler(sessions, script, zerolog.Nop())
	rec := httptest.NewRecorder()
	mount(h).ServeHTTP(rec, httptest.NewRequest("GET", "/api/sessions/s1/chats", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body)
	}
	var got Ledger
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Agent != "reviewer-933" {
		t.Fatalf("ledger = %s (%v): the renamed agent's chats are served under its chosen handle", rec.Body, err)
	}
	if h, _ := Handle(sessions["s1"]); h != "claude-61c3" {
		t.Fatalf("default handle = %q", h)
	}
}

func TestIndexIsReEncodedFromAnAllowlistSoNoContentCrosses(t *testing.T) {
	script, ledger := fakeA2A(t)
	if err := os.WriteFile(ledger, []byte(`{"chats":[{"id":"chat-1","state":"open","members":["a","b"],"left":[],"last":"2026-09-03T00:00:00Z","last_row":"evt_1","rows":1,"body":"the secret plan","text":"leak"}],"transcript":"everything they said"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(fakeSessions{}, script, zerolog.Nop())
	rec := httptest.NewRecorder()
	mount(h).ServeHTTP(rec, httptest.NewRequest("GET", "/api/chats", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"chat-1"`) {
		t.Fatalf("index: %d %s", rec.Code, rec.Body)
	}
	for _, leaked := range []string{"secret", "leak", "transcript", "everything"} {
		if strings.Contains(rec.Body.String(), leaked) {
			t.Fatalf("index served a field outside the allowlist: %s", rec.Body)
		}
	}
	var got Index
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || len(got.Chats) != 1 || got.Chats[0].Rows != 1 || *got.Chats[0].LastRow != "evt_1" {
		t.Fatalf("index shape = %s (%v)", rec.Body, err)
	}
	calls, _ := os.ReadFile(script + ".calls")
	if strings.TrimSpace(string(calls)) != "ledger --index --compact" {
		t.Fatalf("a2a was invoked with %q", calls)
	}
	if err := os.WriteFile(ledger, []byte(`{"agent":"x","chats":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	mount(h).ServeHTTP(rec, httptest.NewRequest("GET", "/api/chats", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("an empty index is still an index: %d", rec.Code)
	}
	if err := os.WriteFile(ledger, []byte(`[]`), 0o600); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	mount(h).ServeHTTP(rec, httptest.NewRequest("GET", "/api/chats", nil))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("a non-index shape is refused: %d", rec.Code)
	}
}

func TestIndexServesTheSessionHandleMap(t *testing.T) {
	script, ledger := fakeA2A(t)
	if err := os.WriteFile(ledger, []byte(`{"chats":[],"sessions":{"61c37a0c-2158-4134-b99e-0914dc1471ff":"reviewer-933"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(fakeSessions{}, script, zerolog.Nop())
	rec := httptest.NewRecorder()
	mount(h).ServeHTTP(rec, httptest.NewRequest("GET", "/api/chats", nil))
	var got Index
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Sessions["61c37a0c-2158-4134-b99e-0914dc1471ff"] != "reviewer-933" {
		t.Fatalf("sessions map = %s (%v)", rec.Body, err)
	}
	if err := os.WriteFile(ledger, []byte(`{"chats":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	mount(h).ServeHTTP(rec, httptest.NewRequest("GET", "/api/chats", nil))
	if !strings.Contains(rec.Body.String(), `"sessions":{}`) {
		t.Fatalf("an a2a without the map serves an empty one, so the browser falls back: %s", rec.Body)
	}
	if err := os.WriteFile(ledger, []byte(`{"chats":[],"sessions":{"s1":""}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	mount(h).ServeHTTP(rec, httptest.NewRequest("GET", "/api/chats", nil))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("a map entry without a handle is refused: %d", rec.Code)
	}
}

func TestLedgerReadTimesOutAndRefusesOverflow(t *testing.T) {
	dir := t.TempDir()
	slow := filepath.Join(dir, "a2a")
	if err := os.WriteFile(slow, []byte("#!/bin/sh\nsleep 5\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	sessions := fakeSessions{"s1": {ID: "8e70bbbb", Provider: "codex"}}
	previous := ledgerReadTimeout
	ledgerReadTimeout = 200 * time.Millisecond
	defer func() { ledgerReadTimeout = previous }()
	h := NewHandler(sessions, slow, zerolog.Nop())
	started := time.Now()
	rec := httptest.NewRecorder()
	mount(h).ServeHTTP(rec, httptest.NewRequest("GET", "/api/sessions/s1/chats", nil))
	if rec.Code != http.StatusBadGateway || time.Since(started) > 3*time.Second {
		t.Fatalf("slow child: %d after %s", rec.Code, time.Since(started))
	}
	loud := filepath.Join(dir, "loud")
	if err := os.WriteFile(loud, []byte("#!/bin/sh\nprintf '{\"agent\":\"codex-8e70\",\"chats\":[{\"id\":\"'; head -c 5000 /dev/zero | tr '\\0' 'x'; printf '\"}]}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	limit := ledgerOutputLimit
	ledgerOutputLimit = 1024
	defer func() { ledgerOutputLimit = limit }()
	rec = httptest.NewRecorder()
	NewHandler(sessions, loud, zerolog.Nop()).Ledger(rec, withPath(httptest.NewRequest("GET", "/api/sessions/s1/chats", nil), "s1"))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("overflowing child: %d %s", rec.Code, rec.Body.String()[:40])
	}
}

func chatStatus(t *testing.T, h *Handler) (bool, *httptest.ResponseRecorder) {
	t.Helper()
	rec := httptest.NewRecorder()
	mount(h).ServeHTTP(rec, httptest.NewRequest("GET", "/api/chats/status", nil))
	var got struct {
		Available bool `json:"available"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("status: %d %s (%v)", rec.Code, rec.Body, err)
	}
	return got.Available, rec
}

func TestStatusIsAvailableWhenTheServiceAnswers(t *testing.T) {
	script, ledger := fakeA2A(t)
	if err := os.WriteFile(ledger, []byte(`{"chats":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	available, rec := chatStatus(t, NewHandler(fakeSessions{}, script, zerolog.Nop()))
	if !available {
		t.Fatalf("status = %s", rec.Body)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("status responses are not cacheable")
	}
}

func TestStatusDoesNotReadTheHistory(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "a2a")
	body := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$0.calls\"\n[ \"$*\" = \"ledger --probe\" ] || sleep 5\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	available, rec := chatStatus(t, NewHandler(fakeSessions{}, script, zerolog.Nop()))
	if !available {
		t.Fatalf("a service whose history is slow to read is still available: %s", rec.Body)
	}
	if calls, _ := os.ReadFile(script + ".calls"); strings.TrimSpace(string(calls)) != "ledger --probe" {
		t.Fatalf("status ran %q", calls)
	}
}

func TestStatusIsUnavailableWithoutTheClientOrItsService(t *testing.T) {
	dir := t.TempDir()
	refused := filepath.Join(dir, "a2a")
	if err := os.WriteFile(refused, []byte("#!/bin/sh\necho 'logstream unreachable' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, executable := range map[string]string{
		"missing client":  filepath.Join(dir, "absent"),
		"refused service": refused,
	} {
		if available, rec := chatStatus(t, NewHandler(fakeSessions{}, executable, zerolog.Nop())); available {
			t.Fatalf("%s: status = %s", name, rec.Body)
		}
	}
}

func TestStatusGivesUpOnASlowServiceAndCachesTheAnswer(t *testing.T) {
	dir := t.TempDir()
	slow := filepath.Join(dir, "a2a")
	if err := os.WriteFile(slow, []byte("#!/bin/sh\nprintf x >> \"$0.calls\"\nsleep 5\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	previous := statusTimeout
	statusTimeout = 200 * time.Millisecond
	defer func() { statusTimeout = previous }()
	h := NewHandler(fakeSessions{}, slow, zerolog.Nop())
	started := time.Now()
	if available, _ := chatStatus(t, h); available || time.Since(started) > 3*time.Second {
		t.Fatalf("slow child: available=%v after %s", available, time.Since(started))
	}
	chatStatus(t, h)
	if calls, _ := os.ReadFile(slow + ".calls"); string(calls) != "x" {
		t.Fatalf("probe ran %d times; the answer is cached", len(calls))
	}
}

func withPath(r *http.Request, id string) *http.Request {
	r.SetPathValue("id", id)
	return r
}

func TestLedgerRefusesAShapeItDoesNotRecognise(t *testing.T) {
	script, ledger := fakeA2A(t)
	sessions := fakeSessions{"s1": {ID: "8e70bbbb", Provider: "codex"}}
	h := NewHandler(sessions, script, zerolog.Nop())
	for _, body := range []string{`{"agent":"","chats":[]}`, `{"agent":"codex-8e70","chats":{}}`, `not json`} {
		if err := os.WriteFile(ledger, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		mount(h).ServeHTTP(rec, httptest.NewRequest("GET", "/api/sessions/s1/chats", nil))
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("body %q: status = %d", body, rec.Code)
		}
	}
}

func TestLedgerNotFoundAndNoHandle(t *testing.T) {
	script, _ := fakeA2A(t)
	sessions := fakeSessions{"sh": {ID: "aaaa-1", Provider: "shell"}}
	h := NewHandler(sessions, script, zerolog.Nop())
	rec := httptest.NewRecorder()
	mount(h).ServeHTTP(rec, httptest.NewRequest("GET", "/api/sessions/missing/chats", nil))
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "not_found") {
		t.Fatalf("missing session: %d %s", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	mount(h).ServeHTTP(rec, httptest.NewRequest("GET", "/api/sessions/sh/chats", nil))
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "no_agent_handle") {
		t.Fatalf("shell session: %d %s", rec.Code, rec.Body)
	}
}

func TestEventsEmitOnlyWhenTheLedgerChanges(t *testing.T) {
	script, ledger := fakeA2A(t)
	writeLedger(t, ledger, "codex-8e70", `[]`)
	sessions := fakeSessions{"s1": {ID: "8e70bbbb", Provider: "codex"}}
	beat := eventsHeartbeat
	eventsHeartbeat = 700 * time.Millisecond
	defer func() { eventsHeartbeat = beat }()
	h := NewHandler(sessions, script, zerolog.Nop())
	srv := httptest.NewServer(mount(h))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/sessions/s1/chats/events", nil)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type = %q", ct)
	}
	reader := bufio.NewReader(resp.Body)
	lines := make(chan string, 64)
	go func() {
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				close(lines)
				return
			}
			lines <- strings.TrimRight(line, "\n")
		}
	}()
	expect := func(want string) {
		t.Helper()
		for {
			select {
			case line, ok := <-lines:
				if !ok {
					t.Fatalf("stream ended before %q", want)
				}
				if strings.Contains(line, want) {
					return
				}
			case <-ctx.Done():
				t.Fatalf("timed out waiting for %q", want)
			}
		}
	}
	expect(": connected")
	expect(`{"sequence":1}`) // the first read is always news relative to nothing
	writeLedger(t, ledger, "codex-8e70", `[{"id":"chat-2","state":"invited","members":["codex-8e70","kimi-2b77"],"rows":[]}]`)
	expect(`{"sequence":2}`)
	// An unchanged ledger produces no further event; the heartbeat comment
	// keeps the stream alive.
	expect(": heartbeat")
	select {
	case line := <-lines:
		if strings.HasPrefix(line, "event:") {
			t.Fatalf("unexpected event on an unchanged ledger: %q", line)
		}
	case <-time.After(2500 * time.Millisecond):
	}
}
