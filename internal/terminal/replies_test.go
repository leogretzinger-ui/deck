package terminal

import (
	"bytes"
	"encoding/base64"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/noisefloor-oss/deck/internal/tmux"
)

// waitConnectionClosed blocks until the attachment socket closes — the
// transport's way of telling a fenced controller it no longer holds the
// session.
func waitConnectionClosed(t *testing.T, c *websocket.Conn) {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	for {
		if _, _, err := c.ReadMessage(); err != nil {
			return
		}
	}
}

// The allowlist admits exactly the replies a real emulator emits, whole
// frames only.
func TestFilterReplyAllows(t *testing.T) {
	for name, data := range map[string][]byte{
		"primary DA":        {0x1b, '[', '?', '1', ';', '2', 'c'},
		"secondary DA":      {0x1b, '[', '>', '0', ';', '9', '5', ';', '0', 'c'},
		"cursor position":   {0x1b, '[', '1', '2', ';', '4', '0', 'R'},
		"status OK":         {0x1b, '[', '0', 'n'},
		"focus in":          {0x1b, '[', 'I'},
		"focus out":         {0x1b, '[', 'O'},
		"sgr mouse press":   {0x1b, '[', '<', '0', ';', '4', ';', '5', 'M'},
		"sgr mouse release": {0x1b, '[', '<', '0', ';', '4', ';', '5', 'm'},
		"osc color BEL": append(append([]byte{0x1b, ']'},
			[]byte("11;rgb:1e1e/1e1e/2e2e")...), 0x07),
		"osc color ST": append(append([]byte{0x1b, ']'},
			[]byte("10;rgb:ffff/ffff/ffff")...), 0x1b, '\\'),
		"osc palette reply": append(append([]byte{0x1b, ']'},
			[]byte("4;0;rgb:ffff/ffff/ffff")...), 0x07),
		"concatenated": {0x1b, '[', '?', '1', ';', '2', 'c',
			0x1b, '[', '1', '2', ';', '4', '0', 'R'},
	} {
		if !filterReply(data) {
			t.Errorf("%s: genuine reply rejected", name)
		}
	}
}

// Everything that is not a complete allowlisted reply is rejected whole:
// the channel must never carry user-shaped bytes.
func TestFilterReplyRejects(t *testing.T) {
	for name, data := range map[string][]byte{
		"empty":              {},
		"plain text":         []byte("hello"),
		"command with CR":    []byte("not-a-reply\r"),
		"ctrl-c":             {0x03},
		"paste open":         {0x1b, '[', '2', '0', '0', '~'},
		"paste close":        {0x1b, '[', '2', '0', '1', '~'},
		"partial CSI":        {0x1b, '[', '?', '1'},
		"bare ESC":           {0x1b},
		"arrow key":          {0x1b, '[', 'A'},
		"lookalike space c":  {0x1b, '[', ' ', 'c'},
		"lookalike params I": {0x1b, '[', '1', '2', '3', 'I'},
		"lookalike OSC":      {0x1b, ']', '1', '2', '3', 0x07},
		// A numeric selector over arbitrary
		// printable text is not a reply, whatever follows it.
		"osc arbitrary selector": append(append([]byte{0x1b, ']'},
			[]byte("1337;touch /workspace/.p25osc;#")...), 0x07),
		"osc color selector non-color payload": append(append([]byte{0x1b, ']'},
			[]byte("10;touch x")...), 0x07),
		"osc palette without index": append(append([]byte{0x1b, ']'},
			[]byte("4;rgb:ffff/ffff/ffff")...), 0x07),
		"osc empty channels": append(append([]byte{0x1b, ']'},
			[]byte("11;rgb:///")...), 0x07),
		"nonzero status":              {0x1b, '[', '1', 'n'},
		"reply plus trailing garbage": {0x1b, '[', '?', '1', ';', '2', 'c', 'X'},
		"garbage plus reply":          {'X', 0x1b, '[', '?', '1', ';', '2', 'c'},
	} {
		if filterReply(data) {
			t.Errorf("%s: forwarded though not a reply", name)
		}
	}

	// The cap: genuine replies repeated past maxReplyBytes are a payload,
	// not a reply.
	oversized := bytes.Repeat([]byte{0x1b, '[', '?', '1', ';', '2', 'c'}, 40)
	if len(oversized) <= maxReplyBytes {
		t.Fatal("test bug: oversized fixture fits the cap")
	}
	if filterReply(oversized) {
		t.Error("oversized frame forwarded")
	}
}

// captureWrites replaces the bounded-write seam with one that records
// every frame's bytes before performing the real write.
func captureWrites(e *env) func() [][]byte {
	var mu sync.Mutex
	var writes [][]byte
	e.proxy.writeWithin = func(att *tmux.Attachment, data []byte, d time.Duration) (int, error) {
		cp := append([]byte(nil), data...)
		mu.Lock()
		writes = append(writes, cp)
		mu.Unlock()
		return att.WriteWithin(data, d)
	}
	return func() [][]byte {
		mu.Lock()
		defer mu.Unlock()
		return append([][]byte(nil), writes...)
	}
}

// A genuine reply from the controller flows through the same serialized,
// bounded writer as raw input.
func TestReplyFlowsThroughSerializedWriter(t *testing.T) {
	e := newEnv(t)
	writes := captureWrites(e)

	c, _ := e.dial(t, "browser-a", false)
	defer c.Close()
	token := awaitAttached(t, c)

	reply := []byte{0x1b, '[', '?', '1', ';', '2', 'c'}
	send(t, c, ClientFrame{Type: TypeReply, Token: token,
		Generation: e.session.Generation,
		Data:       base64.StdEncoding.EncodeToString(reply)})

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, w := range writes() {
			if bytes.Equal(w, reply) {
				goto flowed
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("allowlisted reply never reached the serialized writer")
flowed:

	// The pipeline stays live: ordinary typing right after still flows
	// through the same writer.
	send(t, c, e.inputFrame(token, base64.StdEncoding.EncodeToString([]byte("z"))))
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, w := range writes() {
			if bytes.Equal(w, []byte("z")) {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("typing after a reply never reached the serialized writer")
}

// A "reply" carrying user-shaped bytes is dropped before the gate:
// it never reaches the writer and costs the attachment nothing.
func TestNonAllowlistReplyBlocked(t *testing.T) {
	e := newEnv(t)
	writes := captureWrites(e)

	c, _ := e.dial(t, "browser-a", false)
	defer c.Close()
	token := awaitAttached(t, c)

	payload := []byte("not-a-reply\r")
	send(t, c, ClientFrame{Type: TypeReply, Token: token,
		Generation: e.session.Generation,
		Data:       base64.StdEncoding.EncodeToString(payload)})

	// The rejection is answered so a browser counting one status per write
	// frame stays in step; it is not a terminal outcome.
	awaitStatus(t, c, WriteDropped)

	// Prove the pipeline stayed alive and ordered: a later legitimate
	// keystroke flows. If the rejected frame had been forwarded, its bytes
	// would appear in the captured writes ahead of it.
	send(t, c, e.inputFrame(token, base64.StdEncoding.EncodeToString([]byte("z"))))
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, w := range writes() {
			if bytes.Equal(w, []byte("z")) {
				goto live
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("pipeline died after a rejected reply")
live:
	awaitStatus(t, c, WriteSent)
	for _, w := range writes() {
		if bytes.Contains(w, payload) {
			t.Fatalf("rejected reply bytes reached the writer: %q", w)
		}
	}
}

// Replies respect fencing exactly like input: a stale token fences the
// attachment rather than writing.
func TestReplyRespectsFencing(t *testing.T) {
	e := newEnv(t)
	c, _ := e.dial(t, "browser-a", false)
	defer c.Close()
	token := awaitAttached(t, c)

	reply := []byte{0x1b, '[', '?', '1', ';', '2', 'c'}
	send(t, c, ClientFrame{Type: TypeReply, Token: token + 1,
		Generation: e.session.Generation,
		Data:       base64.StdEncoding.EncodeToString(reply)})
	waitConnectionClosed(t, c)
}
