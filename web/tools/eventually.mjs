// Asserting on state the browser has not reached yet.
//
// A probe drives the UI and then asserts on state the app updates through a
// request the probe never awaited: a dialog detaches when its own request
// resolves, and the follow-up the app fires next lands some milliseconds
// later. Asserting straight after the UI settles reads that state mid-flight,
// so the probe fails on timing rather than on behaviour — and a probe that
// fails at random teaches everyone to re-run it, which is how a real break
// gets waved through.
//
// eventually polls until the condition holds, so a probe waits for the state
// it means to assert. It fails with the last observed value, because
// "expected 2" without saying what was actually there names nothing a reader
// can act on.

const DEFAULT_TIMEOUT_MS = 5000;
const POLL_MS = 25;

// How long an "exactly once" claim watches for a late duplicate.
//
// Reaching the value proves only half the claim; a duplicate arriving after
// the read must fail too, and nothing can prove no request will ever follow.
// The window is the assertion's reach, not a safety margin: a duplicate
// outside it is invisible.
//
// What the reach is, stated plainly rather than implied.
//
// Four counters are asserted this way, and each runs one chain from a DOM
// handler through the sidebar model to the API client, awaited end to end:
//
//   project rename    inline-Enter handler -> SidebarModel.renameProject
//                     -> HttpApiClient.renameProject
//   session rename    Enter and blur handlers -> SidebarModel.rename
//                     -> HttpApiClient.renameSession
//   git-identity      save and resolve paths, converging on one mutation
//   provider launch   provider click, plus the awaited post-identity retry
//
// None of those schedules a request for later. The session-rename blur branch
// does set 300ms and 0ms timers, but both call repaint() and cannot issue a
// request. HttpApiClient calls fetch directly, with no retry, queue,
// interceptor, reconnect handler or service worker to replay one.
//
// The app does defer work for as long as 30s elsewhere — output activity, the
// tasks panel, composer status, version and session metadata — but none of
// those modules calls these mutation methods, so their delays do not bound
// this watch and a window sized to them would buy nothing.
//
// A second therefore covers the paths that exist, and a probe that asserts
// again at the end of its run extends that to a second past its last step.
// What it does NOT cover is a duplicate scheduled further out by a bug that
// introduces deferred scheduling where there is none today. That case is
// invisible here, and this comment is the only thing standing between that gap
// and a reader who assumes otherwise.
//
// Page quiescence cannot substitute: a page with no requests in flight is idle
// exactly while a timer waits to fire, so an idle wait returns at once and
// sees nothing.
const OBSERVE_MS = 1000;

// eventually resolves once read() satisfies predicate, and throws with the
// last value seen if the deadline passes first.
export async function eventually(read, predicate, message, timeoutMs = DEFAULT_TIMEOUT_MS) {
  const deadline = Date.now() + timeoutMs;
  let last = await read();
  while (!predicate(last)) {
    if (Date.now() >= deadline) {
      throw new Error(`${message} (waited ${timeoutMs}ms, last value ${JSON.stringify(last)})`);
    }
    await new Promise((resolve) => setTimeout(resolve, POLL_MS));
    last = await read();
  }
  return last;
}

// eventuallyEqual is the common case: wait for a counter to reach a value.
export async function eventuallyEqual(read, expected, message, timeoutMs = DEFAULT_TIMEOUT_MS) {
  return eventually(read, (v) => v === expected, message, timeoutMs);
}

// exactlyOnce takes every count a probe claims is final, waits for each to
// arrive, then holds all of them under observation together — one window for
// the set rather than one per counter, since they are all watching the same
// stretch of time.
//
// counts is an array of { read, expected, message }.
export async function exactlyOnce(counts, observeMs = OBSERVE_MS) {
  for (const { read, expected, message } of counts) {
    await eventuallyEqual(read, expected, message);
  }
  const until = Date.now() + observeMs;
  while (Date.now() < until) {
    await new Promise((resolve) => setTimeout(resolve, POLL_MS));
    for (const { read, expected, message } of counts) {
      const now = await read();
      if (now !== expected) {
        throw new Error(`${message} (reached ${JSON.stringify(expected)}, then moved to ${JSON.stringify(now)} within ${observeMs}ms)`);
      }
    }
  }
}

// settled waits for read() to hold one value for quietMs. Quiet is not proof
// that the app is done — only that nothing moved for that long — so it
// follows a wait for the specific report expected wherever one exists, and
// stands alone only where the app reports nothing and the measurement itself
// is the only signal there is.
export async function settled(read, label, quietMs = 150, timeoutMs = DEFAULT_TIMEOUT_MS) {
  const deadline = Date.now() + timeoutMs;
  let value = await read();
  let since = Date.now();
  while (Date.now() - since < quietMs) {
    if (Date.now() > deadline) {
      throw new Error(`timed out waiting for ${label} to settle (last value ${JSON.stringify(value)})`);
    }
    await new Promise((resolve) => setTimeout(resolve, POLL_MS));
    const next = await read();
    if (next !== value) {
      value = next;
      since = Date.now();
    }
  }
  return value;
}
