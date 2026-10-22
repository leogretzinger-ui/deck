import assert from "node:assert/strict";
import test from "node:test";
import type { GitHubAttempt, GitHubConnectClient, GitHubCurrent, GitHubStatus } from "./client.ts";
import { connectAction, connectedAccount, connectedLogin, connectScreen, GitHubConnectModel, signedIn, type GitHubConnectState } from "./model.ts";

const running: GitHubAttempt = { id: "attempt-a", status: "running", generation: 1, started_at: "2026-09-23T00:00:00Z" };
const withCode: GitHubAttempt = { ...running, code: "2DFA-5170" };
const stored: GitHubStatus = { outcome: "authenticated", login: "octo", source: "stored", source_writeable: true, active: true, state: "success" };
function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: Error) => void;
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}
async function until(check: () => boolean, label = "condition never held"): Promise<void> {
  for (let i = 0; i < 200 && !check(); i++) await new Promise(resolve => setTimeout(resolve, 2));
  assert.ok(check(), label);
}
function fixture(pollMs = 1, retryMs = 1) {
  const calls: string[] = [];
  const signals: AbortSignal[] = [];
  let status: GitHubStatus = { outcome: "no-account" };
  let attempt: GitHubAttempt | null = null;
  const client: GitHubConnectClient = {
    status: async signal => { signals.push(signal); calls.push("status"); return status; },
    current: async () => { calls.push("current"); return { csrf: "csrf-a", attempt }; },
    start: async (csrf, signal) => { assert.equal(csrf, "csrf-a"); signals.push(signal); calls.push("start"); attempt = running; return running; },
    cancel: async (id, csrf, signal) => { assert.equal(csrf, "csrf-a"); signals.push(signal); calls.push(`cancel:${id}`); },
  };
  let changes = 0;
  const model = new GitHubConnectModel(client, () => changes++, { pollMs, retryMs });
  return { model, client, calls, signals, changes: () => changes, status: (value: GitHubStatus) => { status = value; }, attempt: (value: GitHubAttempt | null) => { attempt = value; } };
}
// holdStatus parks a status read, after letting `skip` reads through, so a
// test can look at the screen while a reconciling read is still out.
function holdStatus(f: ReturnType<typeof fixture>, skip = 0) {
  const read = f.client.status;
  const held = deferred<void>();
  let waiting = false;
  f.client.status = async signal => {
    if (skip-- > 0) return read(signal);
    f.client.status = read;
    const value = await read(signal);
    waiting = true;
    await held.promise;
    return value;
  };
  return { waiting: () => waiting, release: () => held.resolve() };
}
// The model is a small state machine over (phase, attempt status):
//   idle -> checking (open, Try again)
//   checking -> starting (nothing connected, on open or Try again)
//            | polling (attempt running, or unavailable for now) | settled
//   starting -> polling (start returned a running attempt)
//            | checking (response lost or refused, or the attempt it returned is already over)
//   polling -> polling (running, or unavailable for now)
//           | checking (a poll failed or saw the attempt end) -> polling
//           | settled (connected, or attempt over after a fresh status read)
//   polling -> cancelling (Cancel) -> checking, whatever the cancel returned
//   checking -> failed (reads kept failing for 30 seconds) -> checking (Try again)
// Settled is connected, ended (ready to connect again), or a notice or failure.
// Every uncertain outcome goes back through a fresh read, and the screen
// shows Checking until a read lands; failed reads are retried with backoff
// for 30 seconds before the screen offers Try again.
const base: GitHubConnectState = { phase: "ready", status: null, attempt: null, failure: null, followed: false };

test("a status read names the connected account, none, or nothing it can vouch for", () => {
  assert.equal(connectedLogin(stored), "octo");
  assert.equal(connectedLogin({ outcome: "authenticated" }), "", "a connected account without a login is still connected");
  assert.equal(connectedLogin({ outcome: "no-account" }), null);
  assert.equal(connectedLogin({ outcome: "authentication-required" }), null);
  assert.equal(connectedLogin({ outcome: "auth-issue", state: "error" }), null);
  assert.equal(connectedLogin({ outcome: "auth-issue", state: "timeout" }), undefined, "a timed-out check answers nothing");
  assert.equal(connectedLogin({ outcome: "something-new" }), undefined);
});

test("opening with no account starts sign-in once and shows the code when gh prints it", async () => {
  const f = fixture();
  const held = holdStatus(f);
  const opening = f.model.open();
  await until(held.waiting);
  assert.deepEqual(connectScreen(f.model.state), { kind: "checking" });
  held.release();
  await opening;
  assert.deepEqual(f.calls, ["status", "current", "start"]);
  assert.deepEqual(connectScreen(f.model.state), { kind: "starting", running: true });
  f.attempt(withCode);
  await until(() => connectScreen(f.model.state).kind === "code");
  assert.deepEqual(connectScreen(f.model.state), { kind: "code", code: "2DFA-5170" });
  assert.equal(f.calls.filter(call => call === "start").length, 1);
  f.model.dispose();
});

test("the screen switches to connected by itself and stops polling", async () => {
  const f = fixture();
  await f.model.open();
  f.attempt(withCode);
  f.status(stored);
  await until(() => connectScreen(f.model.state).kind === "connected");
  assert.deepEqual(connectScreen(f.model.state), { kind: "connected", login: "octo" });
  assert.equal(signedIn(f.model.state), "octo", "the sign-in this screen started has connected");
  const reads = f.calls.length;
  await new Promise(resolve => setTimeout(resolve, 20));
  assert.equal(f.calls.length, reads, "a known end state stops the polling");
});

test("a sign-in that ends without approval offers Try again, which starts a new one", async () => {
  const f = fixture();
  await f.model.open();
  f.attempt({ ...withCode, status: "exited" });
  await until(() => connectScreen(f.model.state).kind === "ended");
  await f.model.retry();
  assert.equal(f.calls.filter(call => call === "start").length, 2);
  f.model.dispose();
});

test("a status read paired with the attempt's exit is read again before the screen ends", async () => {
  const f = fixture();
  await f.model.open();
  // status() runs first and returns before current() reports the exit.
  f.client.current = async () => { f.status(stored); return { csrf: "csrf-a", attempt: { ...withCode, status: "exited" } }; };
  await until(() => f.model.state.attempt?.status === "exited");
  assert.deepEqual(connectScreen(f.model.state), { kind: "connected", login: "octo" });
  f.model.dispose();
});

test("reopening after a sign-in ended reads status again before showing it", async () => {
  const f = fixture();
  // status() runs first and returns before current() reports the exit.
  f.client.current = async () => {
    f.calls.push("current");
    f.status(stored);
    return { csrf: "csrf-a", attempt: { ...withCode, status: "exited" } };
  };
  await f.model.open();
  assert.deepEqual(f.calls, ["status", "current", "status"]);
  assert.deepEqual(connectScreen(f.model.state), { kind: "connected", login: "octo" });
});

test("a failed poll aborts its paired request, so failures leave nothing in flight", async () => {
  const f = fixture();
  f.attempt(withCode);
  await f.model.open();
  const pending: AbortSignal[] = [];
  f.client.status = async () => { throw new Error("unavailable"); };
  f.client.current = signal => { pending.push(signal); return new Promise<GitHubCurrent>(() => {}); };
  await until(() => pending.length >= 3);
  assert.ok(pending.slice(0, -1).every(signal => signal.aborted), "an earlier poll's request is still in flight");
  f.model.dispose();
  assert.ok(pending.every(signal => signal.aborted));
});

test("a failed poll shows Checking, without Cancel, until a poll reads again", async () => {
  const f = fixture();
  f.attempt(withCode);
  await f.model.open();
  const read = f.client.status;
  f.client.status = async () => { throw new Error("unavailable"); };
  await until(() => connectScreen(f.model.state).kind === "checking");
  assert.equal(f.model.busy, true, "Cancel is not offered while the sign-in is uncertain");
  const reads = f.calls.length;
  await until(() => f.calls.length > reads + 2);
  assert.deepEqual(connectScreen(f.model.state), { kind: "checking" }, "failures keep showing Checking");
  f.client.status = read;
  await until(() => connectScreen(f.model.state).kind === "code");
  assert.equal(f.model.busy, false);
  f.model.dispose();
});

test("a poll that sees the attempt end shows Checking while the follow-up read is out", async () => {
  const f = fixture();
  f.attempt(withCode);
  await f.model.open();
  let held: ReturnType<typeof holdStatus> | undefined;
  f.client.current = async () => {
    f.status(stored);
    held ??= holdStatus(f);
    return { csrf: "csrf-a", attempt: { ...withCode, status: "exited" } };
  };
  await until(() => held?.waiting() === true);
  assert.deepEqual(connectScreen(f.model.state), { kind: "checking" }, "the code is not shown after the attempt ended");
  assert.equal(f.model.busy, true);
  held!.release();
  await until(() => connectScreen(f.model.state).kind === "connected");
  f.model.dispose();
});

test("an attempt that is over shows Checking at once, without waiting for its paired status read", async () => {
  for (const over of ["exited", "unavailable"] as const) {
    const f = fixture();
    f.attempt(withCode);
    await f.model.open();
    const read = f.client.status;
    const paired = deferred<GitHubStatus>();
    const fresh = deferred<void>();
    let reads = 0;
    f.attempt({ ...withCode, status: over });
    f.client.status = async signal => {
      if (++reads === 1) return paired.promise;
      await fresh.promise;
      return read(signal);
    };
    await until(() => reads === 2);
    assert.deepEqual(connectScreen(f.model.state), { kind: "checking" }, `${over}: the code is gone before the paired status returns`);
    assert.equal(f.model.busy, true, `${over}: Cancel is gone before the paired status returns`);
    fresh.resolve();
    if (over === "exited") await until(() => connectScreen(f.model.state).kind === "ended");
    else await until(() => f.model.state.phase === "ready" && f.model.state.attempt?.status === "unavailable");
    assert.deepEqual(connectScreen(f.model.state), over === "exited" ? { kind: "ended" } : { kind: "checking" });
    f.model.dispose();
  }
});

const accountError: GitHubStatus = { outcome: "auth-issue", login: "octo", source: "stored", source_writeable: true, active: true, state: "error" };
const DEFINITE: Record<string, GitHubStatus> = {
  "connected": stored,
  "no account": { outcome: "no-account" },
  "missing CLI": { outcome: "missing-cli", platform: "linux" },
  "expired or revoked credential": accountError,
};
const INDEFINITE: Record<string, () => GitHubStatus> = {
  "timeout": () => ({ outcome: "timeout" }),
  "account timeout": () => ({ ...accountError, state: "timeout" }),
  "run error": () => ({ outcome: "run-error" }),
  "malformed": () => ({ outcome: "malformed" }),
  "oversized": () => ({ outcome: "oversized" }),
  "unknown outcome": () => ({ outcome: "future-outcome" }),
  "HTTP error": () => { throw new Error("GitHub connection request was not confirmed"); },
  "network error": () => { throw new TypeError("Failed to fetch"); },
};

// Each path drives the model to the point where the class under test starts
// answering status reads. `after` is what is left once a definite read
// lands: a running attempt, one that is over, an unavailable one, or a
// read on open, which starts sign-in when nothing is connected.
interface Path {
  name: string;
  attempt: GitHubAttempt | null;
  start?: "ok" | "lost" | "exited";
  cancel?: "ok" | "lost";
  after: "running" | "over" | "unavailable" | "opens";
  drive(model: GitHubConnectModel, inject: () => void, attempt: (value: GitHubAttempt) => void): Promise<void>;
}
const exited: GitHubAttempt = { ...withCode, status: "exited" };
const PATHS: Path[] = [
  { name: "first open", attempt: null, after: "opens", drive: async (model, inject) => { inject(); void model.open(); } },
  { name: "reopen with a running attempt", attempt: withCode, after: "running", drive: async (model, inject) => { inject(); void model.open(); } },
  { name: "reopen with an ended attempt", attempt: exited, after: "opens", drive: async (model, inject) => { inject(); void model.open(); } },
  { name: "reopen with an unavailable attempt", attempt: { ...withCode, status: "unavailable" }, after: "unavailable", drive: async (model, inject) => { inject(); void model.open(); } },
  { name: "reopen with an attempt Deck lost track of", attempt: { ...withCode, status: "unavailable", final: true }, after: "opens", drive: async (model, inject) => { inject(); void model.open(); } },
  { name: "start succeeds", attempt: null, start: "ok", after: "running", drive: async model => { void model.open(); } },
  { name: "start response lost", attempt: null, start: "lost", after: "running", drive: async model => { void model.open(); } },
  { name: "start returns an exited attempt", attempt: null, start: "exited", after: "over", drive: async model => { void model.open(); } },
  { name: "cancel succeeds", attempt: withCode, cancel: "ok", after: "over", drive: async model => { await model.open(); void model.cancel(); } },
  { name: "cancel response lost", attempt: withCode, cancel: "lost", after: "over", drive: async model => { await model.open(); void model.cancel(); } },
  { name: "poll tick", attempt: withCode, after: "running", drive: async (model, inject) => { await model.open(); inject(); } },
  { name: "attempt observed ended", attempt: withCode, after: "over", drive: async (model, inject, attempt) => { await model.open(); inject(); attempt(exited); } },
];

function expectedScreen(path: Path, answer: string): ReturnType<typeof connectScreen> {
  if (answer === "connected") return { kind: "connected", login: "octo" };
  if (path.after === "running") return { kind: "code", code: "2DFA-5170" };
  if (path.after === "unavailable") return { kind: "checking" };
  if (answer === "missing CLI") return { kind: "missing-cli", platform: "linux" };
  return path.after === "opens" ? { kind: "code", code: "2DFA-5170" } : { kind: "ended" };
}

async function runPath(path: Path, indefinite: string | null, answer: string): Promise<void> {
  const label = `${path.name}, ${indefinite ?? "no failure"}, then ${answer}`;
  let attempt = path.attempt;
  let respond: () => GitHubStatus = () => ({ outcome: "no-account" });
  let failures = 0;
  let answered = 0;
  let starts = 0;
  let cancels = 0;
  const answering = () => { answered++; return DEFINITE[answer]!; };
  const inject = () => {
    if (indefinite === null) { respond = answering; return; }
    const fail = INDEFINITE[indefinite]!;
    respond = () => { failures++; return fail(); };
  };
  const client: GitHubConnectClient = {
    status: async () => respond(),
    current: async () => ({ csrf: "csrf-a", attempt }),
    start: async () => {
      starts++;
      if (path.start === undefined) { attempt = withCode; return withCode; }
      inject();
      if (path.start === "exited") { attempt = exited; return exited; }
      attempt = withCode;
      if (path.start === "lost") throw new Error("connection lost");
      return withCode;
    },
    cancel: async () => {
      cancels++;
      attempt = { ...withCode, status: "cancelled" };
      inject();
      if (path.cancel === "lost") throw new Error("lost response");
    },
  };
  const model = new GitHubConnectModel(client, () => {}, { pollMs: 1, retryMs: 1 });
  try {
    await path.drive(model, inject, value => { attempt = value; });
    if (indefinite !== null) {
      await until(() => failures >= 3, `${label}: reads keep being retried`);
      assert.deepEqual(connectScreen(model.state), { kind: "checking" }, `${label}: failed reads show Checking`);
      assert.equal(model.busy, true, `${label}: nothing can be pressed while reads fail`);
      assert.equal(model.state.failure, null, label);
      respond = answering;
    }
    const expected = expectedScreen(path, answer);
    await until(() => answered > 0 && model.state.phase === "ready" && model.state.status === DEFINITE[answer], `${label}: the injected answer is read and kept`);
    await until(() => JSON.stringify(connectScreen(model.state)) === JSON.stringify(expected), `${label}: settles on ${expected.kind}`);
    assert.deepEqual(connectScreen(model.state), expected, label);
    const launched = path.start !== undefined || (path.after === "opens" && answer !== "connected" && answer !== "missing CLI");
    assert.equal(starts, launched ? 1 : 0, `${label}: starts`);
    assert.equal(cancels, path.cancel === undefined ? 0 : 1, `${label}: cancels`);
  } finally {
    model.dispose();
  }
}

test("every path settles only on a definite read and keeps Checking through every indefinite one", async t => {
  for (const path of PATHS) {
    await t.test(path.name, async () => {
      for (const answer of Object.keys(DEFINITE)) {
        await runPath(path, null, answer);
        for (const indefinite of Object.keys(INDEFINITE)) await runPath(path, indefinite, answer);
      }
    });
  }
});

test("an indefinite status shows Checking and backs off without waiting for a slow attempt response", async () => {
  for (const indefinite of Object.keys(INDEFINITE)) {
    const f = fixture();
    f.attempt(withCode);
    await f.model.open();
    const read = f.client.status;
    const current = f.client.current;
    const slow = deferred<GitHubCurrent>();
    const pending: AbortSignal[] = [];
    let reads = 0;
    f.client.current = signal => { pending.push(signal); return slow.promise; };
    f.client.status = async () => { reads++; return INDEFINITE[indefinite]!(); };
    await until(() => reads >= 2, `${indefinite}: the read is retried while the attempt response is out`);
    assert.deepEqual(connectScreen(f.model.state), { kind: "checking" }, `${indefinite}: the code is gone`);
    assert.equal(f.model.busy, true, `${indefinite}: Cancel is gone`);
    assert.ok(pending[0]!.aborted, `${indefinite}: the slow attempt request is abandoned`);
    f.client.status = read;
    f.client.current = current;
    await until(() => connectScreen(f.model.state).kind === "code", `${indefinite}: a landed read shows the code again`);
    slow.resolve({ csrf: "csrf-a", attempt: withCode });
    f.model.dispose();
  }
});

test("reads failing for 30 seconds back off, then stop and offer Try again", async t => {
  t.mock.timers.enable({ apis: ["setTimeout", "Date"] });
  const flush = () => new Promise(resolve => setImmediate(resolve));
  const f = fixture(1500, 1000);
  f.attempt(withCode);
  await f.model.open();
  const read = f.client.status;
  const failedAt: number[] = [];
  f.client.status = async () => { failedAt.push(Date.now()); throw new Error("unavailable"); };
  f.client.cancel = async () => { throw new Error("lost response"); };
  void f.model.cancel();
  for (let second = 0; second < 29; second++) {
    await flush();
    t.mock.timers.tick(1000);
  }
  await flush();
  assert.deepEqual(connectScreen(f.model.state), { kind: "checking" });
  for (let second = 29; second < 36; second++) {
    await flush();
    t.mock.timers.tick(1000);
  }
  await flush();
  assert.deepEqual(failedAt, [0, 1000, 3000, 7000, 15000, 25000, 35000]);
  assert.deepEqual(connectScreen(f.model.state), { kind: "failed", message: "Deck couldn't read your GitHub connection." });
  assert.equal(f.model.busy, false, "Try again can be pressed");
  t.mock.timers.tick(60000);
  await flush();
  assert.equal(failedAt.length, 7, "reads stop once the screen offers Try again");
  f.client.status = read;
  await f.model.retry();
  assert.deepEqual(connectScreen(f.model.state), { kind: "code", code: "2DFA-5170" }, "Try again reads and finds the sign-in");
  f.model.dispose();
});

test("a poll answered after its timeout shows Checking and keeps polling", async t => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const flush = () => new Promise(resolve => setImmediate(resolve));
  const f = fixture();
  f.attempt(withCode);
  await f.model.open();
  const current = f.client.current;
  const late = deferred<GitHubCurrent>();
  f.client.current = () => late.promise;
  t.mock.timers.tick(1);
  await flush();
  t.mock.timers.tick(15000);
  late.resolve({ csrf: "csrf-a", attempt: withCode });
  await flush();
  assert.deepEqual(connectScreen(f.model.state), { kind: "checking" });
  f.client.current = current;
  t.mock.timers.tick(1);
  await flush();
  assert.deepEqual(connectScreen(f.model.state), { kind: "code", code: "2DFA-5170" });
  f.model.dispose();
});

test("reopening finds the running sign-in instead of starting another; closing never cancels", async () => {
  const f = fixture();
  f.attempt(withCode);
  await f.model.open();
  assert.deepEqual(f.calls, ["status", "current"]);
  assert.deepEqual(connectScreen(f.model.state), { kind: "code", code: "2DFA-5170" });
  f.model.dispose();
  assert.equal(f.calls.some(call => call.startsWith("cancel")), false);
});

test("a connected account is shown and starts nothing", async () => {
  const f = fixture();
  f.status(stored);
  await f.model.open();
  assert.deepEqual(f.calls, ["status", "current"], "a connected account never starts sign-in");
  assert.deepEqual(connectScreen(f.model.state), { kind: "connected", login: "octo" });
  assert.equal(signedIn(f.model.state), null, "an account connected before opening is shown, not closed on");
  await f.model.retry();
  assert.equal(f.calls.includes("start"), false);
  assert.equal(signedIn(f.model.state), null);
});

test("a sign-in already running when the screen opens is followed to its end", async () => {
  const f = fixture();
  f.attempt(withCode);
  await f.model.open();
  assert.equal(f.calls.includes("start"), false);
  assert.equal(signedIn(f.model.state), null);
  f.attempt({ ...withCode, status: "exited" });
  f.status(stored);
  await until(() => signedIn(f.model.state) !== null);
  assert.equal(signedIn(f.model.state), "octo");
  f.model.dispose();
  assert.equal(signedIn(f.model.state), null, "a closed screen reports nothing");
});

test("a connected account with another sign-in running is shown, not closed on", async () => {
  const f = fixture();
  f.status(stored);
  f.attempt(withCode);
  await f.model.open();
  assert.deepEqual(connectScreen(f.model.state), { kind: "connected", login: "octo" });
  assert.equal(signedIn(f.model.state), null);
  f.model.dispose();
});

test("credentials gh does not store are never offered a sign-in", async () => {
  for (const status of [
    { ...stored, source: "environment", source_writeable: false },
    { ...stored, source: "unknown", source_writeable: false },
    { outcome: "auth-issue", source: "environment", state: "error", active: true, login: "octo" },
    { outcome: "auth-issue", source: "unknown", state: "error", active: true, login: "octo" },
  ]) {
    const f = fixture();
    f.status(status);
    await f.model.open();
    assert.deepEqual(f.calls, ["status", "current"]);
    const screen = connectScreen(f.model.state);
    if (status.outcome === "authenticated") assert.deepEqual(screen, { kind: "connected", login: "octo" });
    else assert.equal(screen.kind, "notice");
  }
});

test("a missing GitHub CLI says so and starts nothing", async () => {
  const f = fixture();
  f.status({ outcome: "missing-cli", platform: "darwin" });
  await f.model.open();
  assert.deepEqual(f.calls, ["status", "current"]);
  assert.deepEqual(connectScreen(f.model.state), { kind: "missing-cli", platform: "darwin" });
});

test("connect eligibility preserves environment and unknown credential precedence", () => {
  for (const source of ["environment", "unknown", "future-source"]) {
    for (const outcome of ["no-account", "auth-issue", "authenticated"]) {
      assert.equal(connectAction({ source, outcome, state: "error", active: true, source_writeable: true, login: "person" }), null);
    }
  }
  const failed: GitHubStatus = { outcome: "auth-issue", source: "stored", state: "error", active: true, source_writeable: true, login: "person" };
  assert.equal(connectAction(failed), "reconnect");
  for (const override of [{ active: false }, { source_writeable: false }, { login: "" }, { state: "timeout" }, { outcome: "authenticated" }]) {
    assert.equal(connectAction({ ...failed, ...override }), null);
  }
  assert.equal(connectAction({ outcome: "no-account" }), "connect");
});

test("each state derives exactly one screen", () => {
  const exited = { ...running, status: "exited" as const };
  const unavailable = { ...running, status: "unavailable" as const };
  const cases: Array<[Partial<GitHubConnectState>, ReturnType<typeof connectScreen>]> = [
    [{ phase: "idle" }, { kind: "checking" }],
    [{ phase: "checking", status: stored }, { kind: "checking" }],
    [{ phase: "checking", status: { outcome: "no-account" }, attempt: exited }, { kind: "checking" }],
    [{ phase: "cancelling", status: { outcome: "no-account" }, attempt: withCode }, { kind: "checking" }],
    [{ phase: "starting" }, { kind: "starting", running: false }],
    [{ status: { outcome: "no-account" }, attempt: running }, { kind: "starting", running: true }],
    [{ status: { outcome: "no-account" }, attempt: withCode }, { kind: "code", code: "2DFA-5170" }],
    [{ status: { outcome: "no-account" }, attempt: unavailable }, { kind: "checking" }],
    [{ status: { outcome: "no-account" }, attempt: { ...unavailable, final: true } }, { kind: "failed", message: "Deck lost track of this GitHub sign-in." }],
    [{ status: accountError, attempt: { ...unavailable, final: true } }, { kind: "failed", message: "Deck lost track of this GitHub sign-in." }],
    [{ status: { outcome: "missing-cli", platform: "linux" }, attempt: { ...unavailable, final: true } }, { kind: "missing-cli", platform: "linux" }],
    [{ status: stored, attempt: { ...unavailable, final: true } }, { kind: "connected", login: "octo" }],
    [{ phase: "idle", status: stored, failure: "read" }, { kind: "failed", message: "Deck couldn't read your GitHub connection." }],
    [{ status: stored, attempt: exited }, { kind: "connected", login: "octo" }],
    [{ status: stored }, { kind: "connected", login: "octo" }],
    [{ status: { outcome: "no-account" }, attempt: exited }, { kind: "ended" }],
    [{ status: { outcome: "no-account" }, attempt: { ...running, status: "cancelled" } }, { kind: "ended" }],
    [{ status: { outcome: "missing-cli", platform: "linux" } }, { kind: "missing-cli", platform: "linux" }],
    [{ status: { outcome: "no-account" }, failure: "start" }, { kind: "failed", message: "Deck couldn't start GitHub sign-in." }],
    [{ status: { outcome: "malformed" } }, { kind: "failed", message: "Deck couldn't read your GitHub connection." }],
    [{ status: { ...stored, outcome: "auth-issue", state: "timeout" } }, { kind: "failed", message: "Deck couldn't read your GitHub connection." }],
  ];
  for (const [state, screen] of cases) assert.deepEqual(connectScreen({ ...base, ...state }), screen, JSON.stringify(state));
  assert.equal(connectScreen({ ...base, status: { outcome: "authentication-required" } }).kind, "notice");
  assert.equal(signedIn({ ...base, status: stored, followed: true }), "octo");
  assert.equal(signedIn({ ...base, status: { ...stored, login: undefined }, followed: true }), "");
  assert.equal(signedIn({ ...base, status: stored }), null);
  assert.equal(signedIn({ ...base, phase: "checking", status: stored, followed: true }), null);
  assert.equal(signedIn({ ...base, status: { outcome: "no-account" }, followed: true }), null);
  assert.equal(connectedAccount({ ...base, status: stored }), "octo", "an account connected before the screen opened is reported on close");
  assert.equal(connectedAccount({ ...base, status: { ...stored, login: undefined } }), "");
  assert.equal(connectedAccount({ ...base, phase: "checking", status: stored }), null);
  assert.equal(connectedAccount({ ...base, phase: "idle", status: stored, failure: "read" }), null);
  assert.equal(connectedAccount({ ...base, status: { outcome: "no-account" } }), null);
});

test("double Connect launches once", async () => {
  const f = fixture();
  const pending = deferred<GitHubAttempt>();
  let starts = 0;
  f.client.start = () => { starts++; return pending.promise; };
  const opening = f.model.open();
  await until(() => starts === 1);
  await f.model.retry();
  assert.equal(starts, 1);
  pending.resolve(running);
  await opening;
  f.model.dispose();
});

test("a start whose outcome is unknown is never replayed; a read reconciles it", async () => {
  const f = fixture();
  let starts = 0;
  f.client.start = async () => { starts++; throw new Error("connection lost"); };
  await f.model.open();
  assert.equal(starts, 1);
  assert.deepEqual(connectScreen(f.model.state), { kind: "failed", message: "Deck couldn't start GitHub sign-in." });

  const g = fixture();
  g.client.start = async () => { g.attempt(withCode); held = holdStatus(g); throw new Error("connection lost"); };
  let held: ReturnType<typeof holdStatus> | undefined;
  const opening = g.model.open();
  await until(() => held?.waiting() === true);
  assert.deepEqual(connectScreen(g.model.state), { kind: "checking" }, "nothing is shown until the read returns");
  held!.release();
  await opening;
  assert.deepEqual(connectScreen(g.model.state), { kind: "code", code: "2DFA-5170" }, "the launched attempt is found by reading");
  g.model.dispose();
});

test("a start whose response is lost but whose sign-in finished shows the connection", async () => {
  const f = fixture();
  f.client.start = async () => { f.attempt({ ...withCode, status: "exited" }); f.status(stored); throw new Error("connection lost"); };
  await f.model.open();
  assert.equal(f.model.state.failure, null);
  assert.deepEqual(connectScreen(f.model.state), { kind: "connected", login: "octo" });
});

test("a start that returns an attempt already over is read again before the screen ends", async () => {
  const f = fixture();
  const exited: GitHubAttempt = { ...withCode, status: "exited" };
  let held: ReturnType<typeof holdStatus> | undefined;
  f.client.start = async () => { f.calls.push("start"); f.attempt(exited); f.status(stored); held = holdStatus(f, 1); return exited; };
  const opening = f.model.open();
  await until(() => held?.waiting() === true);
  assert.deepEqual(connectScreen(f.model.state), { kind: "checking" }, "the ended attempt is not shown before the read");
  held!.release();
  await opening;
  assert.deepEqual(f.calls, ["status", "current", "start", "status", "current", "status"]);
  assert.deepEqual(connectScreen(f.model.state), { kind: "connected", login: "octo" });

  const g = fixture();
  g.client.start = async () => { g.attempt(exited); return exited; };
  await g.model.open();
  assert.deepEqual(connectScreen(g.model.state), { kind: "ended" }, "an attempt that ended unapproved is not a failed start");
});

test("an unavailable attempt shows Checking and keeps being read until sign-in shows up", async () => {
  const f = fixture();
  await f.model.open();
  f.attempt({ ...withCode, status: "unavailable" });
  await until(() => f.model.state.attempt?.status === "unavailable");
  assert.deepEqual(connectScreen(f.model.state), { kind: "checking" });
  const reads = f.calls.length;
  await until(() => f.calls.length > reads + 2);
  f.attempt(withCode);
  await until(() => connectScreen(f.model.state).kind === "code");
  f.attempt({ ...withCode, status: "exited" });
  f.status(stored);
  await until(() => connectScreen(f.model.state).kind === "connected");
  assert.equal(f.calls.filter(call => call === "start").length, 1, "reading never restarts sign-in");

  const g = fixture();
  g.attempt({ ...withCode, status: "unavailable" });
  await g.model.open();
  assert.deepEqual(g.calls, ["status", "current", "status"], "reopening on an unavailable attempt starts nothing");
  assert.deepEqual(connectScreen(g.model.state), { kind: "checking" });
  g.attempt({ ...withCode, status: "exited" });
  await until(() => connectScreen(g.model.state).kind === "ended");
});

test("an attempt Deck lost track of shows a failure with Try again, which starts a new sign-in", async () => {
  const f = fixture();
  await f.model.open();
  f.attempt({ ...withCode, status: "unavailable", final: true });
  await until(() => connectScreen(f.model.state).kind === "failed");
  assert.deepEqual(connectScreen(f.model.state), { kind: "failed", message: "Deck lost track of this GitHub sign-in." });
  assert.equal(f.model.busy, false);
  const reads = f.calls.length;
  await new Promise(resolve => setTimeout(resolve, 20));
  assert.equal(f.calls.length, reads, "an attempt Deck gave up on is not polled");
  await f.model.retry();
  assert.equal(f.calls.filter(call => call === "start").length, 2);
  assert.deepEqual(connectScreen(f.model.state), { kind: "starting", running: true });
  f.model.dispose();

  const g = fixture();
  g.client.start = async () => { g.calls.push("start"); g.attempt({ ...withCode, status: "unavailable", final: true }); throw new Error("launch could not be confirmed"); };
  await g.model.open();
  assert.deepEqual(connectScreen(g.model.state), { kind: "failed", message: "Deck couldn't start GitHub sign-in." }, "a lost launch is a failed start");
  assert.equal(g.calls.filter(call => call === "start").length, 1, "a lost launch is not replayed");
});

test("cancel sends once, then shows one fresh read: ready to connect again", async () => {
  const f = fixture(60000);
  f.attempt(withCode);
  await f.model.open();
  const pending = deferred<void>();
  const ids: string[] = [];
  f.client.cancel = id => { ids.push(id); return pending.promise; };
  const cancel = f.model.cancel();
  await f.model.cancel();
  assert.deepEqual(ids, [running.id], "a second Cancel is not sent");
  assert.deepEqual(connectScreen(f.model.state), { kind: "checking" });
  f.attempt({ ...withCode, status: "cancelled" });
  const held = holdStatus(f, 1);
  pending.resolve();
  await until(held.waiting);
  assert.deepEqual(connectScreen(f.model.state), { kind: "checking" }, "the cancelled attempt is not shown before the read");
  held.release();
  await cancel;
  assert.deepEqual(connectScreen(f.model.state), { kind: "ended" });
  assert.equal(f.calls.includes("start"), false, "cancel never starts a new sign-in");
});

test("a lost cancel response is read again: a sign-in still running is followed, never cancelled twice", async () => {
  const f = fixture();
  f.attempt(withCode);
  await f.model.open();
  let cancels = 0;
  f.client.cancel = async () => { cancels++; throw new Error("lost response"); };
  await f.model.cancel();
  assert.deepEqual(connectScreen(f.model.state), { kind: "code", code: "2DFA-5170" });
  f.attempt({ ...withCode, status: "exited" });
  f.status(stored);
  await until(() => connectScreen(f.model.state).kind === "connected");
  assert.equal(cancels, 1);

  const g = fixture();
  g.attempt(withCode);
  await g.model.open();
  g.client.cancel = async () => { g.attempt({ ...withCode, status: "cancelled" }); throw new Error("lost response"); };
  await g.model.cancel();
  assert.equal(g.model.state.failure, null);
  assert.deepEqual(connectScreen(g.model.state), { kind: "ended" }, "a cancel that landed shows ready to connect again");
});

test("cancel racing a successful sign-in shows the connected account", async () => {
  for (const lost of [false, true]) {
    const f = fixture(60000);
    f.attempt(withCode);
    await f.model.open();
    let cancels = 0;
    f.client.cancel = async () => {
      cancels++;
      f.attempt({ ...withCode, status: "exited" });
      f.status(stored);
      if (lost) throw new Error("lost response");
    };
    await f.model.cancel();
    assert.deepEqual(connectScreen(f.model.state), { kind: "connected", login: "octo" }, `lost=${lost}`);
    assert.equal(cancels, 1);
    const reads = f.calls.length;
    await new Promise(resolve => setTimeout(resolve, 10));
    assert.equal(f.calls.length, reads, "a connected account stops the polling");
  }
});

test("close aborts pending reads and ignores stale callbacks", async () => {
  const f = fixture();
  const pending = deferred<GitHubCurrent>();
  f.client.current = () => pending.promise;
  const opening = f.model.open();
  f.model.dispose();
  const changes = f.changes();
  pending.resolve({ csrf: "late", attempt: running });
  await opening;
  assert.equal(f.signals[0]!.aborted, true);
  assert.equal(f.model.state.phase, "closed");
  assert.equal(f.model.state.attempt, null);
  assert.equal(f.changes(), changes);
});

test("close during start revokes callbacks without issuing cancellation or replay", async () => {
  const f = fixture();
  const pending = deferred<GitHubAttempt>();
  f.client.start = (_csrf, signal) => { f.signals.push(signal); return pending.promise; };
  const opening = f.model.open();
  await until(() => f.signals.length === 2);
  f.model.dispose();
  const changes = f.changes();
  pending.resolve(running);
  await opening;
  assert.equal(f.model.state.phase, "closed");
  assert.equal(f.signals.at(-1)!.aborted, true);
  assert.equal(f.changes(), changes);
  assert.equal(f.calls.some(call => call.startsWith("cancel")), false);
});

test("a failed current read cannot authorize a start from a successful status read", async () => {
  const f = fixture();
  let failures = 0;
  f.client.current = async () => { failures++; throw new Error("unavailable"); };
  void f.model.open();
  await until(() => failures >= 3);
  assert.equal(f.calls.includes("start"), false);
  assert.equal(f.signals[0]!.aborted, true, "a failed paired read revokes the other request too");
  assert.deepEqual(connectScreen(f.model.state), { kind: "checking" });
  f.model.dispose();
});

test("late responses after timeout cannot confirm a read", async t => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const f = fixture();
  const pendingRead = deferred<GitHubCurrent>();
  f.client.current = () => pendingRead.promise;
  void f.model.open();
  t.mock.timers.tick(15000);
  pendingRead.resolve({ csrf: "csrf-a", attempt: null });
  await new Promise(resolve => setImmediate(resolve));
  assert.deepEqual(connectScreen(f.model.state), { kind: "checking" });
  assert.equal(f.calls.includes("start"), false);
  f.model.dispose();
});
