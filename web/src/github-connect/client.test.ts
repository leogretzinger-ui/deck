import assert from "node:assert/strict";
import test from "node:test";
import { HttpGitHubConnectClient } from "./client.ts";

const attempt = { id: "attempt-a", status: "running", generation: 1, started_at: "2026-09-23T00:00:00Z" };

test("typed client reads first and supplies both CSRF headers only on explicit writes", async t => {
  const calls: Array<{ path: string; init: RequestInit }> = [];
  t.mock.method(globalThis, "fetch", async (path: string, init: RequestInit) => {
    calls.push({ path, init });
    const body = path.endsWith("status") ? { outcome: "no-account", token: "must-not-project" }
      : path.endsWith("attempt") ? { csrf: "principal-csrf", attempt }
      : attempt;
    return path.endsWith("cancel") ? new Response(null, { status: 204 }) : Response.json(body);
  });
  const client = new HttpGitHubConnectClient(undefined, () => "noisedeck_csrf=cookie-csrf");
  const signal = new AbortController().signal;
  const status = await client.status(signal);
  assert.equal("token" in status, false);
  const current = await client.current(signal);
  await client.start(current.csrf, signal);
  await client.cancel(current.attempt!.id, current.csrf, signal);
  assert.deepEqual(calls.map(call => call.init.method), ["GET", "GET", "POST", "POST"]);
  for (const { init } of calls) {
    assert.equal(init.cache, "no-store");
    assert.equal(init.referrerPolicy, "no-referrer");
    assert.equal(init.redirect, "error");
  }
  const headers = new Headers(calls[2]!.init.headers);
  assert.equal(headers.get("X-GitHub-Connect-CSRF"), "principal-csrf");
  assert.equal(headers.get("X-CSRF-Token"), "cookie-csrf");
  assert.equal(calls[2]!.init.body, "{}");
  assert.deepEqual(JSON.parse(calls[3]!.init.body as string), { id: "attempt-a" });
});

test("authorization loss notifies once and raw failure bodies do not escape", async t => {
  t.mock.method(globalThis, "fetch", async () => new Response("private diagnostic", { status: 401 }));
  let lost = 0;
  const client = new HttpGitHubConnectClient(() => lost++, () => "");
  await assert.rejects(client.status(new AbortController().signal), { message: "GitHub connection request was not confirmed" });
  assert.equal(lost, 1);
});

test("an aborted request cannot invoke a late authorization callback", async t => {
  const controller = new AbortController();
  t.mock.method(globalThis, "fetch", async () => { controller.abort(); return new Response(null, { status: 401 }); });
  let lost = 0;
  const client = new HttpGitHubConnectClient(() => lost++);
  await assert.rejects(client.status(controller.signal));
  assert.equal(lost, 0);
});

test("mutations without the principal CSRF read are refused without a request", async t => {
  let requests = 0;
  t.mock.method(globalThis, "fetch", async () => { requests++; return Response.json(attempt); });
  const client = new HttpGitHubConnectClient(undefined, () => "");
  await assert.rejects(client.start("", new AbortController().signal));
  assert.equal(requests, 0);
});

test("the attempt carries a well-formed device code only", async t => {
  let code: unknown = "2DFA-5170";
  t.mock.method(globalThis, "fetch", async (path: string) => {
    if (path.endsWith("status")) return Response.json({ outcome: "missing-cli", platform: "darwin" });
    return Response.json(path.endsWith("attempt") ? { csrf: "principal-csrf", attempt: { ...attempt, code } } : { ...attempt, code });
  });
  const client = new HttpGitHubConnectClient(undefined, () => "");
  const signal = new AbortController().signal;
  assert.equal((await client.status(signal)).platform, "darwin");
  assert.equal((await client.current(signal)).attempt?.code, "2DFA-5170");
  for (const bad of ["<b>x</b>", "2dfa-5170", 42, "2DFA-5170\n"]) {
    code = bad;
    assert.equal((await client.current(signal)).attempt?.code, undefined, JSON.stringify(bad));
  }
});

test("an attempt is final only when the backend says so", async t => {
  let final: unknown = true;
  t.mock.method(globalThis, "fetch", async () => Response.json({ csrf: "principal-csrf", attempt: { ...attempt, status: "unavailable", final } }));
  const client = new HttpGitHubConnectClient(undefined, () => "");
  const signal = new AbortController().signal;
  assert.equal((await client.current(signal)).attempt?.final, true);
  for (const other of [false, "true", 1, undefined]) {
    final = other;
    assert.equal((await client.current(signal)).attempt?.final, undefined, JSON.stringify(other));
  }
});
