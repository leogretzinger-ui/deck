import assert from "node:assert/strict";
import test from "node:test";
import {
  HarnessLoginCoordinator,
  type HarnessLoginCoordinatorPorts,
  type SuppressedLoginAttempt,
} from "./harness-login-coordinator.ts";
import type { HarnessLoginListing, HarnessLoginAttempt } from "./harness-login.ts";

function attempt(id: string, flow = "codex-browser"): HarnessLoginAttempt {
  return { id, flow, expires: new Date(60000).toISOString() };
}

// The coordinator is driven through pollNow() so no test depends on wall
// time; the ports record every decision the unit under test makes.
function fixture(listing: HarnessLoginListing = { pending: [] }, initialStored: SuppressedLoginAttempt[] = []) {
  const state = {
    session: "session-a" as string | null,
    attached: true,
    visible: true,
    occupied: false,
    now: 1000,
    fetches: 0,
    signals: [] as AbortSignal[],
    listings: new Map<string, HarnessLoginListing | Promise<HarnessLoginListing>>([["session-a", listing]]),
  };
  const raises: { session: string; listing: HarnessLoginListing }[] = [];
  let stored = initialStored;
  let raiseSucceeds = true;
  const ports = (): HarnessLoginCoordinatorPorts => ({
    listing: (session, signal) => {
      state.fetches += 1;
      state.signals.push(signal);
      const answer = state.listings.get(session);
      if (answer === undefined) return Promise.reject(new Error(`unexpected session ${session}`));
      return Promise.resolve(answer);
    },
    raise: (session, raised) => {
      raises.push({ session, listing: raised });
      return raiseSucceeds;
    },
    occupied: () => state.occupied,
    session: () => state.session,
    attached: () => state.attached,
    visible: () => state.visible,
    now: () => state.now,
  });
  const store = {
    getItem: () => JSON.stringify(stored),
    setItem: (_key: string, value: string) => { stored = JSON.parse(value) as SuppressedLoginAttempt[]; },
    removeItem: () => { stored = []; },
  };
  const primary = new HarnessLoginCoordinator(ports(), store);
  return {
    state,
    raises,
    stored: () => stored,
    failRaises: () => { raiseSucceeds = false; },
    coordinator: primary,
    reload: () => new HarnessLoginCoordinator(ports(), { ...store, getItem: () => JSON.stringify(stored) }),
  };
}

test("raises the shared dialog once for a single supported attempt", async () => {
  const listing: HarnessLoginListing = { pending: [attempt("a")], csrf: "opaque" };
  const { coordinator, raises } = fixture(listing);
  await coordinator.pollNow();
  await coordinator.pollNow();
  assert.equal(raises.length, 1);
  assert.equal(raises[0]!.session, "session-a");
  assert.equal(raises[0]!.listing, listing, "the dialog receives the listing that was actually fetched");
});

test("listing timeout and controller invalidation abort the actual request", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const { coordinator, state, raises } = fixture();
  let finish!: (listing: HarnessLoginListing) => void;
  state.listings.set("session-a", new Promise(resolve => { finish = resolve; }));
  const pending = coordinator.pollNow();
  assert.equal(state.signals[0]!.aborted, false);
  t.mock.timers.tick(8000);
  assert.equal(state.signals[0]!.aborted, true);
  coordinator.invalidate();
  finish({ pending: [attempt("old")] });
  await pending;
  assert.equal(raises.length, 0);

  state.listings.set("session-a", new Promise(resolve => { finish = resolve; }));
  const second = coordinator.pollNow();
  coordinator.invalidate();
  assert.equal(state.signals[1]!.aborted, true);
  finish({ pending: [attempt("new")] });
  await second;
  assert.equal(raises.length, 0);
});

test("an empty, unsupported, or unavailable listing never raises", async () => {
  for (const listing of [
    { pending: [] },
    { pending: [attempt("u", "kimi-device")] },
    { pending: [attempt("x")], unavailable: true },
  ] as HarnessLoginListing[]) {
    const { coordinator, raises, state } = fixture(listing);
    await coordinator.pollNow();
    assert.equal(raises.length, 0);
    assert.deepEqual(coordinator.pendingFor("session-a"), listing.unavailable === true
      ? []
      : listing.pending.filter((one) => one.flow === "codex-browser"));
    assert.equal(state.fetches, 1);
    coordinator.dispose();
  }
});

test("a stale answer from another session or a disposed coordinator never raises", async () => {
  const { coordinator, raises, state } = fixture({ pending: [] });
  // The pinned lib predates Promise.withResolvers; a deferred answer is
  // held through a typed resolver instead.
  let resolveStale!: (listing: HarnessLoginListing) => void;
  state.listings.set("session-a", new Promise((resolve) => { resolveStale = resolve; }));
  const stale = coordinator.pollNow();
  state.session = "session-b";
  resolveStale({ pending: [attempt("a")] });
  await stale;
  assert.equal(raises.length, 0, "an answer that outlived its session is dropped");

  state.session = "session-a";
  state.listings.set("session-a", { pending: [] });
  await coordinator.pollNow();
  let resolveLate!: (listing: HarnessLoginListing) => void;
  state.listings.set("session-a", new Promise((resolve) => { resolveLate = resolve; }));
  const late = coordinator.pollNow();
  coordinator.dispose();
  resolveLate({ pending: [attempt("b")] });
  await late;
  assert.equal(raises.length, 0, "an answer that outlived the coordinator is dropped");
});

test("no listing is fetched while detached, hidden, or sessionless", async () => {
  const { coordinator, state } = fixture({ pending: [attempt("a")] });
  state.attached = false;
  await coordinator.pollNow();
  state.attached = true;
  state.visible = false;
  await coordinator.pollNow();
  state.visible = true;
  state.session = null;
  await coordinator.pollNow();
  assert.equal(state.fetches, 0);
});

test("an occupied screen defers the raise to the next poll, without suppressing", async () => {
  const { coordinator, raises, state } = fixture({ pending: [attempt("a")] });
  state.occupied = true;
  await coordinator.pollNow();
  assert.equal(raises.length, 0);
  state.occupied = false;
  await coordinator.pollNow();
  assert.equal(raises.length, 1, "the attempt deferred by an open dialog still raises once clear");
});

test("a dismissal suppresses the attempt across polls and a reload", async () => {
  const { coordinator, raises, reload } = fixture({ pending: [attempt("a")] });
  await coordinator.pollNow();
  coordinator.closed("session-a", ["a"], "dismissed");
  await coordinator.pollNow();
  assert.equal(raises.length, 1, "a dismissed attempt does not nag the same coordinator");

  const reloaded = reload();
  await reloaded.pollNow();
  assert.equal(raises.length, 1, "the suppression persists through storage and a fresh coordinator");
});

test("a context-lost teardown is not an answer, but a raised attempt still raises only once", async () => {
  const neverRaised = fixture({ pending: [attempt("a")] });
  neverRaised.coordinator.closed("session-a", ["a"], "context-lost");
  await neverRaised.coordinator.pollNow();
  assert.equal(neverRaised.raises.length, 1, "an attempt whose dialog was only torn down may still be raised");

  const raised = fixture({ pending: [attempt("b")] });
  await raised.coordinator.pollNow();
  raised.coordinator.closed("session-a", ["b"], "context-lost");
  await raised.coordinator.pollNow();
  assert.equal(raised.raises.length, 1, "raise-once holds even when the raise ended context-lost");
});

test("a failed mount records nothing and retries on the next poll", async () => {
  const { coordinator, raises, stored, failRaises } = fixture({ pending: [attempt("a")] });
  failRaises();
  await coordinator.pollNow();
  assert.equal(raises.length, 1);
  assert.deepEqual(stored(), [], "no suppression for a dialog that never opened");
  await coordinator.pollNow();
  assert.equal(raises.length, 2, "the next poll retries an attempt whose raise never mounted");
});

test("a multi-attempt listing raises one picker and dismisses every listed attempt", async () => {
  const { coordinator, raises, stored } = fixture({ pending: [attempt("a"), attempt("b", "pi-openai")] });
  await coordinator.pollNow();
  assert.equal(raises.length, 1);
  assert.equal(raises[0]!.listing.pending.length, 2, "a genuine choice keeps every attempt in the picker");
  coordinator.closed("session-a", ["a", "b"], "dismissed");
  assert.deepEqual(
    stored().map((entry) => entry.id).sort(),
    ["a", "b"],
    "picking nothing still answers every attempt the dialog presented",
  );
});

test("the suppression store stays bounded and drops expired entries", async () => {
  const expired: SuppressedLoginAttempt = { id: "gone", session: "session-a", expires: 500 };
  const { coordinator, stored } = fixture({ pending: [attempt("a")] }, [expired]);
  await coordinator.pollNow();
  coordinator.closed("session-a", ["a"], "dismissed");
  const ids = stored().map((entry) => entry.id);
  assert.equal(ids.includes("gone"), false, "entries older than their expiry are pruned on write");
  assert.equal(ids.includes("a"), true);

  const many = fixture({ pending: [] });
  await many.coordinator.pollNow();
  many.coordinator.closed("session-a", Array.from({ length: 1030 }, (_, i) => `attempt-${i}`), "dismissed");
  assert.equal(many.stored().length, 1024, "the store keeps at most the newest 1024 entries");
});

test("pendingFor answers only for the session whose listing it holds", async () => {
  const { coordinator, state } = fixture({ pending: [attempt("a"), attempt("u", "kimi-device")] });
  await coordinator.pollNow();
  assert.deepEqual(coordinator.pendingFor("session-a").map((one) => one.id), ["a"]);
  assert.deepEqual(coordinator.pendingFor("session-b"), []);
  state.session = null;
  assert.deepEqual(coordinator.pendingFor("session-a").map((one) => one.id), ["a"], "a later selection does not erase the listing the chrome still shows");
});

test("a response received after hiding or controller loss cannot open a dialog", async () => {
  for (const key of ["visible", "attached"] as const) {
    const { coordinator, raises, state } = fixture();
    let resolve!: (listing: HarnessLoginListing) => void;
    state.listings.set("session-a", new Promise(done => { resolve = done; }));
    const pending = coordinator.pollNow();
    state[key] = false;
    resolve({ pending: [attempt("late")] });
    await pending;
    assert.equal(raises.length, 0);
  }
});

test("a new controller can poll immediately without accepting its predecessor's late answer", async () => {
  const { coordinator, raises, state } = fixture();
  let resolve!: (listing: HarnessLoginListing) => void;
  state.listings.set("session-a", new Promise(done => { resolve = done; }));
  const old = coordinator.pollNow();
  coordinator.invalidate();
  state.listings.set("session-a", { pending: [attempt("current")] });
  await coordinator.pollNow();
  resolve({ pending: [attempt("stale")] });
  await old;
  assert.deepEqual(raises.map(item => item.listing.pending[0]!.id), ["current"]);
  assert.deepEqual(coordinator.pendingFor("session-a").map(item => item.id), ["current"]);
});

test("a native registration arriving during an older listing queues one immediate refresh", async () => {
  const { coordinator, raises, state } = fixture();
  let resolve!: (listing: HarnessLoginListing) => void;
  state.listings.set("session-a", new Promise(done => { resolve = done; }));
  const old = coordinator.pollNow();
  await coordinator.pollNow();
  await coordinator.pollNow();
  state.listings.set("session-a", { pending: [attempt("registered")] });
  resolve({ pending: [] });
  await old;
  await Promise.resolve();
  assert.equal(state.fetches, 2);
  assert.deepEqual(raises.map(item => item.listing.pending[0]!.id), ["registered"]);
});

test("expired attempts disappear from the contextual action without claiming success", async () => {
  const { coordinator, raises, state } = fixture({ pending: [attempt("a")] });
  await coordinator.pollNow();
  coordinator.closed("session-a", ["a"], "dismissed");
  state.now = 60001;
  assert.deepEqual(coordinator.pendingFor("session-a"), []);
  await coordinator.pollNow();
  assert.equal(raises.length, 1);
});
