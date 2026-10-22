// Package terminal is the browser's terminal attachment: a WebSocket that
// transports bytes between one browser and one tmux client, and nothing
// else.
//
// # What this layer is not
//
// It does not parse, interpret, or rewrite terminal output. It does not
// know what a provider is doing, whether a turn is active, or what a prompt
// looks like — inferring any of that is forbidden. tmux performs the
// emulation and the browser's xterm.js renders it; this code moves bytes
// and enforces who may send them.
//
// # The three rules it does enforce
//
//  1. WHO. Every frame from the browser carries the controller's fencing
//     token, validated against the lease before a single byte reaches the
//     terminal. A superseded controller's writes are refused even if its
//     socket is still open, and the connection is closed. Output flows to
//     any attached viewer; INPUT requires the lease.
//  2. ONE. At most one browser holds the attachment. A second connection
//     without a deliberate takeover is told the lease is held and closed —
//     it shows a takeover screen; it does not silently share control.
//  3. NEVER BACKPRESSURE THE CLI. A slow browser is disconnected, not
//     allowed to stall the provider. Output is queued to a bounded buffer;
//     a client that cannot keep up overflows it and is dropped. The tmux
//     client keeps draining regardless, so the provider never blocks on a
//     browser.
//
// # Lifetime
//
// A disconnect destroys only that attachment: the tmux client dies, the
// session and its provider process do not. Closing the browser tab, a
// network failure, or a takeover all take the same path.
package terminal
