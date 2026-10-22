// Reclaiming the terminal when the user comes back to the window.
//
// A backgrounded tab loses its terminal two ways, and the user caused
// neither. It stops draining its WebSocket, so the proxy drops it to keep
// the CLI unblocked (slow_consumer); and its heartbeat is a page timer
// the browser throttles, so the lease times out (lease_expired). An
// active window wants its terminal back without being asked.
//
// It does NOT apply to a held or fenced lease. Those mean a different,
// LIVE controller has the session, and two windows that both
// reclaimed on focus would fence each other indefinitely.
//
// The distinction is the server's to make, not this module's. `fenced` and
// `lease_expired` are separate frames precisely because the browser cannot
// tell them apart: both arrive as a lost lease. What separates them is the
// ORDER of the lease validation checks — controller identity, then
// token, then expiry — so a lease another controller holds short-circuits
// before expiry is ever evaluated. Reverse that order and a lease someone
// else took and then let go stale would report as expiry, and this module
// would reclaim into a real displacement.

// ProblemKind is why the attachment is not connected.
export type ProblemKind =
  | "takeover"
  | "fenced"
  | "leaseExpired"
  | "dropped"
  | "error"
  | "closed";

// reclaimsOnFocus reports whether this kind may be reconnected without
// asking. Only the kinds where nothing else holds the session.
export function reclaimsOnFocus(kind: ProblemKind | null): boolean {
  switch (kind) {
    case "dropped":
    case "closed":
    case "error":
    // A lease this controller lost to the TTL was taken by nobody: the
    // heartbeat is a page timer, and a hidden tab throttles it. Reclaiming
    // an idle session of your own displaces no one.
    case "leaseExpired":
      return true;
    default:
      // takeover and fenced name another controller; null means the
      // attachment is mid-flight and has not failed at all.
      return false;
  }
}

// MAX_AUTO_RECLAIMS bounds consecutive automatic attempts. A server that
// keeps refusing must surface the button rather than spin: the user
// seeing a stuck terminal is recoverable, a reconnect loop against a live
// CLI is not.
export const MAX_AUTO_RECLAIMS = 3;

// MIN_STABLE_MS is how long a connection must last to earn the budget
// back.
//
// Resetting on ANY successful attach does not bound anything: a
// slow-consumer drop can only happen AFTER an attach succeeded, so every
// drop refunds the attempt that caused it and the loop runs forever. Only
// a connection that survived is evidence the reconnect worked.
export const MIN_STABLE_MS = 30_000;

// ReclaimDecision is what the shell should do about a disconnected
// terminal, given everything it knows.
export interface ReclaimDecision {
  reconnect: boolean;
  // attempts is the counter to carry forward.
  attempts: number;
}

export interface ReclaimInput {
  kind: ProblemKind | null;
  // visible is document.visibilityState === "visible". A hidden tab never
  // reclaims: it would take the session from a window the user is
  // actually looking at, and it would immediately be dropped again for the
  // same reason it was dropped the first time.
  visible: boolean;
  // sessionRunning gates on the session being alive. A dead session's
  // overlay is recovery evidence, not a connection problem.
  sessionRunning: boolean;
  // connected short-circuits everything.
  connected: boolean;
  // connectedMs is how long the connection that just ended lasted, or
  // null when there was none. A connection that did not survive
  // MIN_STABLE_MS earns nothing back: it is a symptom of the loop, not
  // evidence against it.
  connectedMs: number | null;
  attempts: number;
}

// decideReclaim is the whole policy, pure so it can be tested without a
// browser.
export function decideReclaim(input: ReclaimInput): ReclaimDecision {
  if (input.connected) {
    // A connection that has lasted gives the budget back; one that has
    // not is left to be judged when it ends.
    const stable = input.connectedMs !== null && input.connectedMs >= MIN_STABLE_MS;
    return { reconnect: false, attempts: stable ? 0 : input.attempts };
  }
  const earned = input.connectedMs !== null && input.connectedMs >= MIN_STABLE_MS;
  const attempts = earned ? 0 : input.attempts;
  if (!input.visible || !input.sessionRunning || !reclaimsOnFocus(input.kind)) {
    return { reconnect: false, attempts };
  }
  if (attempts >= MAX_AUTO_RECLAIMS) {
    return { reconnect: false, attempts };
  }
  return { reconnect: true, attempts: attempts + 1 };
}
