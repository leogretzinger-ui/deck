// Package server implements the HTTP API, authenticated WebSocket terminal
// proxy, controller leases, and static asset serving.
//
// At most one browser holds the controller lease and terminal attachment; a
// second connection sees a takeover screen until it deliberately takes control,
// which fences and closes the previous attachment. Leases are
// heartbeat-renewed at least every five seconds and expire no later than
// fifteen seconds after heartbeat loss; deliberate takeover atomically
// increments a fencing token that the server validates on every raw-input
// frame and release request.
//
// A browser disconnect destroys only that attachment, and a slow attachment
// is disconnected rather than allowed to backpressure the CLI.
// Terminal-emulator replies are restricted to a fixed allowlist, pass
// through the serialized writer, and can never originate from a takeover
// screen — provider output must not be able to synthesize arbitrary
// terminal input.
package server
