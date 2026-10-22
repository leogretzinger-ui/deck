// The reclaim policy. It decides whether a disconnected terminal may
// reconnect itself, so the safety property under test is which kinds it
// refuses — taking a session from another live controller must stay the
// user's act.
import assert from "node:assert/strict";
import test from "node:test";

import {
  decideReclaim,
  MAX_AUTO_RECLAIMS,
  MIN_STABLE_MS,
  reclaimsOnFocus,
  type ProblemKind,
} from "./reclaim.ts";

function input(over: Partial<Parameters<typeof decideReclaim>[0]> = {}) {
  return {
    kind: "dropped" as ProblemKind | null,
    visible: true,
    sessionRunning: true,
    connected: false,
    connectedMs: null,
    attempts: 0,
    ...over,
  };
}

// The whole point: a drop the user did not cause reconnects itself.
test("a window in front reclaims a dropped terminal", () => {
  assert.deepEqual(decideReclaim(input()), { reconnect: true, attempts: 1 });
  assert.equal(decideReclaim(input({ kind: "closed" })).reconnect, true);
  assert.equal(decideReclaim(input({ kind: "error" })).reconnect, true);
});

// The safety property. A held or fenced lease means a DIFFERENT live
// controller: the lease manager renews for the same controller and
// reclaims an expired one, so a refusal is always someone else. Two
// windows that both reclaimed on focus would fence each other forever.
test("a session another controller holds is never taken automatically", () => {
  assert.equal(decideReclaim(input({ kind: "takeover" })).reconnect, false);
  assert.equal(decideReclaim(input({ kind: "fenced" })).reconnect, false);
  assert.equal(reclaimsOnFocus("takeover"), false);
  assert.equal(reclaimsOnFocus("fenced"), false);
});

// A hidden tab reclaiming would take the session from the window the
// user is actually looking at, and would be dropped again for the same
// reason it was dropped the first time.
test("a hidden window never reclaims", () => {
  assert.equal(decideReclaim(input({ visible: false })).reconnect, false);
});

// A dead session's overlay is recovery evidence, not a connection problem.
test("a session that is not running is left alone", () => {
  assert.equal(decideReclaim(input({ sessionRunning: false })).reconnect, false);
});

// Nothing to do while connected, and the budget comes back only once the
// connection has proved it lasted.
test("a connected terminal reclaims nothing, and only a lasting one refunds", () => {
  // Both halves matter. Asserting only `reconnect === false` leaves the
  // budget unpinned, which is round 1's unbounded loop through a narrower
  // door: a focus event landing while briefly connected mid-flap would
  // refund the attempt that caused the flap.
  const flapping = decideReclaim(input({ connected: true, connectedMs: 500, attempts: 2 }));
  assert.equal(flapping.reconnect, false);
  assert.equal(flapping.attempts, 2, "a connection that has not lasted refunds nothing");

  const lasting = decideReclaim(input({ connected: true, connectedMs: MIN_STABLE_MS, attempts: 2 }));
  assert.equal(lasting.reconnect, false);
  assert.equal(lasting.attempts, 0, "one that has lasted gives the budget back");
});

// The bound only bounds if a short-lived connection earns nothing. A
// slow-consumer drop can ONLY follow a successful attach, so refunding on
// any attach refunds the very attempt that caused the drop and the loop
// runs forever.
test("a connection that did not last earns no budget back", () => {
  const flapped = decideReclaim(input({ connectedMs: 1_000, attempts: 3 }));
  assert.equal(flapped.reconnect, false, "the third attempt is still spent");
  assert.equal(flapped.attempts, 3);

  const stable = decideReclaim(input({ connectedMs: MIN_STABLE_MS, attempts: 3 }));
  assert.equal(stable.reconnect, true, "a connection that lasted earns the budget back");
  assert.equal(stable.attempts, 1, "and the count starts again");
});

// A flapping server converges on the button rather than alternating
// reset and retry forever.
test("a flapping connection converges on the manual control", () => {
  let attempts = 0;
  for (let i = 0; i < 10; i++) {
    const d = decideReclaim(input({ connectedMs: 500, attempts }));
    attempts = d.attempts;
    if (i >= MAX_AUTO_RECLAIMS) {
      assert.equal(d.reconnect, false, `attempt ${i} should be refused`);
    }
  }
  assert.equal(attempts, MAX_AUTO_RECLAIMS, "the count stops at the bound");
});

// A lease this controller lost to the TTL was taken by nobody: the
// heartbeat is a page timer and a hidden tab throttles it. Reclaiming an
// idle session of your own displaces no one, so it must not be confused
// with a genuine takeover.
test("a lease that merely expired is reclaimed, unlike a fenced one", () => {
  assert.equal(reclaimsOnFocus("leaseExpired"), true);
  assert.equal(reclaimsOnFocus("fenced"), false);
  assert.equal(decideReclaim(input({ kind: "leaseExpired" })).reconnect, true);
});

// A server that keeps refusing must surface the button rather than spin: a
// reconnect loop against a live CLI is the worse failure.
test("consecutive attempts are bounded, and the count carries", () => {
  let state = input();
  for (let i = 1; i <= MAX_AUTO_RECLAIMS; i++) {
    const d = decideReclaim(state);
    assert.equal(d.reconnect, true, `attempt ${i} should be allowed`);
    assert.equal(d.attempts, i);
    state = { ...state, attempts: d.attempts };
  }
  const exhausted = decideReclaim(state);
  assert.equal(exhausted.reconnect, false, "the budget stops the loop");
  assert.equal(exhausted.attempts, MAX_AUTO_RECLAIMS, "and does not keep climbing");
});

// A refusal must not consume budget: a hidden tab that is focused ten
// times should still have all of its attempts when it finally reclaims.
test("a refused decision spends nothing", () => {
  for (const over of [{ visible: false }, { sessionRunning: false }, { kind: "takeover" as ProblemKind }]) {
    assert.equal(decideReclaim(input({ ...over, attempts: 1 })).attempts, 1);
  }
});

// null is mid-flight, not a failure — reconnecting then would race the
// attachment already in progress.
test("an attachment that has not failed is not reconnected", () => {
  assert.equal(reclaimsOnFocus(null), false);
  assert.equal(decideReclaim(input({ kind: null })).reconnect, false);
});
