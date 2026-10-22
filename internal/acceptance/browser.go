package acceptance

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// Frame types of the terminal WS protocol (internal/terminal/protocol.go,
// mirrored by web/src/api/frames.ts).
const (
	frameInput      = "input"
	frameResize     = "resize"
	frameHeartbeat  = "heartbeat"
	frameOutput     = "output"
	frameAttached   = "attached"
	frameStatus     = "status"
	frameError      = "error"
	frameReply      = "reply"
	errLeaseHeld    = "lease_held"
	errFenced       = "fenced"
	errSlowConsumer = "slow_consumer"
)

// Write outcome codes of the terminal WS protocol.
const (
	outcomeSent        = "sent"
	outcomeFailed      = "failed"
	outcomeUncertain   = "uncertain"
	outcomeInterrupted = "interrupted"
)

type clientFrame struct {
	Type       string `json:"type"`
	Token      int64  `json:"token"`
	Generation int    `json:"generation"`
	Data       string `json:"data,omitempty"`
	Cols       int    `json:"cols,omitempty"`
	Rows       int    `json:"rows,omitempty"`
}

type serverFrame struct {
	Type       string `json:"type"`
	Data       string `json:"data,omitempty"`
	Token      int64  `json:"token,omitempty"`
	Generation int    `json:"generation,omitempty"`
	Code       string `json:"code,omitempty"`
	Message    string `json:"message,omitempty"`
}

// browser is a real WebSocket client standing in for the user's
// browser: it attaches, heartbeats, reads output, and sends input —
// exactly the attach/takeover/fencing flows of the real frontend.
type browser struct {
	t      *testing.T
	conn   *websocket.Conn
	token  int64
	gen    int
	write  sync.Mutex
	mu     sync.Mutex
	output strings.Builder
	err    *serverFrame
	status *serverFrame
	closed chan struct{}
}

// attach opens the terminal WS for a session and waits for the attached
// frame carrying this controller's fencing token.
func (e *env) attach(sessionID string) *browser {
	return e.attachAs(sessionID, "acceptance-browser", false)
}

// attachTakeover opens the terminal WS as a DIFFERENT controller with
// takeover=1, fencing the current controller. It is the acceptance
// shape of the displaced-controller scenario the failure matrix needs.
func (e *env) attachTakeover(sessionID string) *browser {
	return e.attachAs(sessionID, "takeover-browser", true)
}

func (e *env) attachAs(sessionID, controllerID string, takeover bool) *browser {
	e.t.Helper()
	url := "ws" + strings.TrimPrefix(e.base, "http") + "/api/sessions/" + sessionID +
		"/terminal?controller=" + controllerID + "&cols=80&rows=24"
	if takeover {
		url += "&takeover=1"
	}
	conn, resp, err := websocket.DefaultDialer.Dial(url, http.Header{
		"Origin":            []string{e.base},
		"X-Acceptance-User": []string{"acceptance"},
	})
	if err != nil {
		if resp != nil {
			_ = resp.Body.Close()
			e.t.Fatalf("ws dial %s: %v (status %d)", sessionID, err, resp.StatusCode)
		}
		e.t.Fatalf("ws dial %s: %v", sessionID, err)
	}
	b := &browser{t: e.t, conn: conn, closed: make(chan struct{})}
	e.t.Cleanup(func() { _ = b.conn.Close() })
	go b.readLoop()
	f := b.awaitFrame(frameAttached, 10*time.Second)
	b.token = f.Token
	if b.token <= 0 {
		e.t.Fatalf("attached frame carried token %d", b.token)
	}
	// The attached frame also names the session generation the browser must
	// stamp its input frames with.
	b.gen = f.Generation
	// A heartbeat carries the fencing token, so it cannot start before the
	// attached frame establishes that token.
	go b.heartbeatLoop()
	return b
}

func (b *browser) readLoop() {
	defer close(b.closed)
	for {
		_, raw, err := b.conn.ReadMessage()
		if err != nil {
			return
		}
		var f serverFrame
		if err := json.Unmarshal(raw, &f); err != nil {
			continue
		}
		b.mu.Lock()
		switch f.Type {
		case frameOutput:
			data, derr := base64.StdEncoding.DecodeString(f.Data)
			if derr == nil {
				b.output.Write(data)
			}
		case frameError:
			fc := f
			b.err = &fc
		case frameStatus:
			fc := f
			b.status = &fc
		}
		b.mu.Unlock()
		if f.Type == frameAttached {
			fc := f
			attachedMu.Lock()
			attachedWait[b] = &fc
			attachedMu.Unlock()
		}
	}
}

// awaitFrame waits for a frame of the given type.
func (b *browser) awaitFrame(want string, d time.Duration) serverFrame {
	b.t.Helper()
	deadline := time.Now().Add(d)
	// The read loop stores errors and statuses directly and retains the one
	// attached frame in attachedWait until attach picks it up.
	for time.Now().Before(deadline) {
		b.mu.Lock()
		if want == frameError && b.err != nil {
			f := *b.err
			b.mu.Unlock()
			return f
		}
		if want == frameStatus && b.status != nil {
			f := *b.status
			b.status = nil
			b.mu.Unlock()
			return f
		}
		b.mu.Unlock()
		if want == frameAttached {
			if f := b.takeAttached(); f != nil {
				return *f
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	b.t.Fatalf("no %s frame within %s", want, d)
	return serverFrame{}
}

// awaitStatus waits for a write-outcome frame with exactly the given code,
// ignoring other statuses (sent acks for earlier keystrokes are common).
func (b *browser) awaitStatus(want string, d time.Duration) serverFrame {
	b.t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		b.mu.Lock()
		if b.status != nil && b.status.Code == want {
			f := *b.status
			b.status = nil
			b.mu.Unlock()
			return f
		}
		b.mu.Unlock()
		time.Sleep(25 * time.Millisecond)
	}
	b.t.Fatalf("no %s write outcome within %s", want, d)
	return serverFrame{}
}

func (b *browser) closeAndWait(d time.Duration) {
	b.t.Helper()
	_ = b.conn.Close()
	select {
	case <-b.closed:
	case <-time.After(d):
		b.t.Fatalf("browser WebSocket did not close within %s", d)
	}
}

// attachedOnce holds the attached frame until attach() picks it up.
var (
	attachedMu   sync.Mutex
	attachedWait = map[*browser]*serverFrame{}
)

func (b *browser) takeAttached() *serverFrame {
	attachedMu.Lock()
	defer attachedMu.Unlock()
	f := attachedWait[b]
	delete(attachedWait, b)
	return f
}

// send transmits one client frame with the fencing token and the session
// generation: the server must not accept input that was written for a
// different terminal lifetime.
func (b *browser) send(f clientFrame) {
	b.t.Helper()
	f.Token = b.token
	f.Generation = b.gen
	raw, err := json.Marshal(f)
	if err != nil {
		b.t.Fatalf("marshal frame: %v", err)
	}
	if err := b.writeMessage(raw); err != nil {
		b.t.Fatalf("write frame: %v", err)
	}
}

// Gorilla permits one concurrent writer. Tests can send input while the
// heartbeat goroutine fires, so every client frame shares this lock.
func (b *browser) writeMessage(raw []byte) error {
	b.write.Lock()
	defer b.write.Unlock()
	return b.conn.WriteMessage(websocket.TextMessage, raw)
}

// input sends literal bytes to the terminal.
func (b *browser) input(data []byte) {
	b.send(clientFrame{Type: frameInput, Data: base64.StdEncoding.EncodeToString(data)})
}

// tryInput sends literal bytes WITHOUT failing the test when the socket is
// already closed. The displaced-browser scenario sends into a torn-down
// attachment on purpose: the bytes must be refused, and a refused frame or a
// dead socket is the expected outcome, not a test failure.
func (b *browser) tryInput(data []byte) bool {
	raw, err := json.Marshal(clientFrame{
		Type:       frameInput,
		Token:      b.token,
		Generation: b.gen,
		Data:       base64.StdEncoding.EncodeToString(data),
	})
	if err != nil {
		return false
	}
	return b.writeMessage(raw) == nil
}

// resize reports the browser terminal dimensions through the real protocol.
func (b *browser) resize(cols, rows int) {
	b.send(clientFrame{Type: frameResize, Cols: cols, Rows: rows})
}

// heartbeatLoop keeps the lease alive the way the real frontend does
// (the server's TTL is 15s; it beats at 5s) until the connection dies.
func (b *browser) heartbeatLoop() {
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-b.closed:
			return
		case <-tick.C:
			if err := b.writeMessage(mustJSON(clientFrame{
				Type:       frameHeartbeat,
				Token:      b.token,
				Generation: b.gen,
			})); err != nil {
				return
			}
		}
	}
}

func mustJSON(v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return raw
}

// outputContains polls the accumulated output for a marker.
func (b *browser) outputContains(marker string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		b.mu.Lock()
		s := b.output.String()
		b.mu.Unlock()
		if strings.Contains(s, marker) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

func (b *browser) outputSnapshot() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.output.String()
}
