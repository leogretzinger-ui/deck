package terminal

import "encoding/json"

// Frame types. The protocol is deliberately tiny: terminal bytes in both
// directions, a resize, a scroll (a tmux copy-mode view command), a
// heartbeat, a per-write outcome report, and errors. Anything richer would
// mean this layer understood the terminal, which it must not (doc.go).
const (
	// Client -> server.
	TypeInput  = "input"
	TypeResize = "resize"
	// TypeReply is an emulator-generated terminal reply, sent when the
	// browser's emulator answers a provider's device/status query WITHOUT
	// the user typing anything. Replies are restricted to a fixed
	// allowlist (replies.go) and may only come from the fenced controller —
	// provider output must never synthesize arbitrary input.
	TypeReply     = "reply"
	TypeHeartbeat = "heartbeat"
	// TypeScroll asks tmux to move the pane's copy-mode view through its own
	// scrollback. It carries a direction and a line count, never
	// terminal bytes, and the server translates it into tmux view commands —
	// tmux owns the pane and its history, so this stays inside the authority
	// invariant rather than growing a browser-side scrollback.
	TypeScroll = "scroll"

	// Server -> client.
	TypeOutput   = "output"
	TypeAttached = "attached"
	// TypeScrollStatus reports tmux's post-command pane view state after a
	// scroll command succeeds. The browser uses this acknowledgment instead
	// of assuming that a requested state transition occurred.
	TypeScrollStatus = "scroll_status"
	// TypeWriteStatus reports the outcome of one input/reply write: sent,
	// failed, uncertain, or interrupted. It carries a write-outcome code,
	// never a claim of delivery.
	TypeWriteStatus = "status"
	TypeError       = "error"
)

// Error codes the browser acts on. `lease_held` is the takeover screen's
// trigger; `fenced` means this controller was superseded and must stop;
// `slow_consumer` means the attachment was dropped to keep the CLI
// unblocked.
const (
	CodeLeaseHeld = "lease_held"
	CodeFenced    = "fenced"
	// CodeLeaseExpired means this controller's own lease timed out and
	// NOBODY took the session. It is a separate code from fenced because
	// the browser acts on it differently — reclaiming its own idle
	// session is safe, taking one from a live controller is not — and
	// because telling a user another browser has control when none
	// does is simply false.
	CodeLeaseExpired = "lease_expired"
	CodeSlowConsumer = "slow_consumer"
	CodeAttachFailed = "attach_failed"
	CodeBadFrame     = "bad_frame"
	CodeNotRunning   = "not_running"
)

// Write outcome codes. These describe a single write's status, never
// delivery: a write acknowledged by tmux is `sent` (bytes handed to tmux
// for the live pane), a provable pre-write rejection is `failed`, a live
// write that cannot prove zero bytes crossed the tmux boundary is
// `uncertain`, and a lost browser/service connection is reported as
// `interrupted`. A reply the allowlist rejects is answered `dropped`: nothing
// was written, the attachment stays open, and the status exists so a browser
// counting one status per forwarded write stays in step.
const (
	WriteSent        = "sent"
	WriteFailed      = "failed"
	WriteUncertain   = "uncertain"
	WriteInterrupted = "interrupted"
	WriteDropped     = "dropped"
)

// ClientFrame is one message from the browser.
type ClientFrame struct {
	Type string `json:"type"`
	// Token is the controller's fencing token. Required on every input and
	// resize: the server validates it before writing.
	Token int64 `json:"token"`
	// Generation is the session generation the browser believes it is
	// attached to, learned from the attached frame. Required on every input
	// and reply: the server rejects a stale generation before writing.
	Generation int `json:"generation"`
	// Data is raw terminal input, base64-encoded so arbitrary bytes survive
	// JSON transport intact — control characters, escape sequences, and
	// invalid UTF-8 alike.
	Data string `json:"data,omitempty"`
	Cols uint16 `json:"cols,omitempty"`
	Rows uint16 `json:"rows,omitempty"`
	// Direction and Lines describe a scroll frame: "up" or "down" moves a
	// clamped number of history lines; "live" exits the pane view. An unknown
	// direction is a bad frame.
	Direction string `json:"direction,omitempty"`
	Lines     int    `json:"lines,omitempty"`
}

// Scroll directions a scroll frame may carry.
const (
	ScrollUp   = "up"
	ScrollDown = "down"
	ScrollLive = "live"
)

// ServerFrame is one message to the browser.
type ServerFrame struct {
	Type string `json:"type"`
	// Data is raw terminal output, base64-encoded (see ClientFrame.Data).
	Data string `json:"data,omitempty"`
	// Token is the controller's current fencing token, sent on attach so
	// the client can stamp its frames.
	Token int64 `json:"token,omitempty"`
	// Generation is the session generation the attachment was created for,
	// sent on attach so the client can stamp its input frames.
	Generation int `json:"generation,omitempty"`
	// Code and Message describe an error or a write outcome. Messages are
	// user-facing and carry no provider output or internal detail.
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
	// Active is tmux's observed post-command pane_in_mode state. It appears
	// only on TypeScrollStatus frames.
	Active *bool `json:"active,omitempty"`
}

func encodeFrame(f ServerFrame) ([]byte, error) { return json.Marshal(f) }
