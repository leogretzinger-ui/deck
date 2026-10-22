import { test } from "node:test";
import assert from "node:assert/strict";
import { SecurityClient, SecurityError, mayOpenWorkspace, parseSecurityState, parseOnboardingState } from "./client.ts";

const ready = { instanceId: "test-instance", phase: "ready", commitment: "public", storage: "open", runtime: "running", locked: false, browserGranted: true,
  ownerBound: true, workspaceAuthorized: true, onboarding: { policy: "required", protection: "encrypted", started: true, setupComplete: true } } as const;
test("private admission needs observed running storage and a current browser grant", () => {
  assert.equal(mayOpenWorkspace(ready), true);
  for (const change of [{ browserGranted: false }, { workspaceAuthorized: false }, { storage: "unknown" }, { runtime: "starting" }]) {
    assert.equal(mayOpenWorkspace(parseSecurityState({ ...ready, ...change })), false);
  }
  for (const change of [{ locked: undefined }, { locked: "true" }, { browserGranted: "true" }, { phase: "unexpected" }, { runtime: "ready" }, { instanceId: "" }]) {
    assert.throws(() => parseSecurityState({ ...ready, ...change }));
  }
});

test("plain admission requires explicit completed protection and current observed access", () => {
  const plain = { ...ready, commitment: undefined, browserGranted: false, storage: "unencrypted", plainOperationId: "a".repeat(43),
    onboarding: { policy: "optional", protection: "none", started: true, setupComplete: true } };
  assert.equal(mayOpenWorkspace(parseSecurityState(plain)), true);
  for (const change of [{ workspaceAuthorized: false }, { runtime: "stopped" }, { storage: "unknown" }]) {
    assert.equal(mayOpenWorkspace(parseSecurityState({ ...plain, ...change })), false);
  }
  for (const change of [{ browserGranted: true }, { locked: true }, { storage: "open" }, { storage: "closed" }, { plainOperationId: "invented" }, { ownerBound: false },
    { onboarding: { ...plain.onboarding, policy: "required" } }, { onboarding: { ...plain.onboarding, setupComplete: false } }]) {
    assert.throws(() => parseSecurityState({ ...plain, ...change }));
  }
  assert.throws(() => parseSecurityState({ ...ready, storage: "unencrypted" }));
});

test("onboarding policy and ownership progress must be supplied by the server", () => {
  const fresh = { policy: "optional", protection: "undecided", started: false, setupComplete: false };
  assert.deepEqual(parseOnboardingState(fresh), fresh);
  for (const change of [{ started: undefined }, { policy: undefined }, { setupComplete: true }, { protection: "none" },
    { started: true, setupComplete: true }, { started: true, policy: "unavailable", protection: "encrypted" }]) {
    assert.throws(() => parseOnboardingState({ ...fresh, ...change }));
  }
});

test("mutations reflect CSRF and cannot follow a redirect or a missing gateway marker", async () => {
  const calls: Array<{ input: RequestInfo | URL; init?: RequestInit }> = [];
  const request: typeof fetch = async (input, init) => {
    calls.push({ input, init });
    return new Response(null, { status: 204, headers: { "X-Deck-Workspace-Security": "v1" } });
  };
  const api = new SecurityClient(request, () => "__Host-noisedeck_csrf=public-test-csrf");
  await api.redeem();
  assert.equal(new Headers(calls[0]!.init!.headers).get("X-CSRF-Token"), "public-test-csrf");
  assert.equal(calls[0]!.init!.credentials, "same-origin");
  assert.equal(calls[0]!.init!.redirect, "error");
  assert.equal(calls[0]!.init!.body, "{}");
  await assert.rejects(new SecurityClient(request, () => "").redeem(), SecurityError);
  assert.equal(calls.length, 1, "no request is sent without the session-bound CSRF token");
  const unmarked = new SecurityClient(async () => new Response(JSON.stringify(ready)), () => "");
  await assert.rejects(unmarked.state(), /did not identify/);
});

test("pre-sign-in invitation handoff is distinct from redemption", async () => {
  let call: { input: RequestInfo | URL; init?: RequestInit } | undefined;
  const api = new SecurityClient(async (input, init) => { call = { input, init }; return new Response(null, { status: 204, headers: { "X-Deck-Workspace-Security": "v1" } }); }, () => "");
  await api.handoff("public-invitation-fixture");
  assert.equal(call?.input, "/api/security/v1/invitation/handoff");
  assert.equal(new Headers(call?.init?.headers).has("X-CSRF-Token"), false);
  assert.equal(call?.init?.method, "POST");
});


test("operation observations require the backend's boolean lock qualification", async () => {
  const observed = { id: "public-operation", status: "unconfirmed", storage: "closed", runtime: "stopped", locked: true };
  const api = (value: unknown) => new SecurityClient(async () => new Response(JSON.stringify(value), {
    headers: { "X-Deck-Workspace-Security": "v1" },
  }), () => "");
  assert.deepEqual(await api(observed).observe(observed.id), observed);
  assert.equal((await api({ ...observed, locked: false }).observe(observed.id)).locked, false);
  for (const locked of [undefined, "true", 1, null]) {
    await assert.rejects(api({ ...observed, locked }).observe(observed.id), /operation result is unavailable/);
  }
});

test("401 notifies the owner before waiting for an error body", async () => {
  let close!: () => void;
  const body = new ReadableStream({ start(controller) { close = () => controller.close(); } });
  const api = new SecurityClient(async () => new Response(body, {
    status: 401, headers: { "X-Deck-Workspace-Security": "v1" },
  }));
  let notify!: () => void;
  const expired = new Promise<void>(resolve => { notify = resolve; });
  api.onAuthorizationLost = () => notify();
  const request = api.state();
  await expired;
  close();
  await assert.rejects(request, error => error instanceof SecurityError && error.status === 401);
});

for (const method of ["operate", "observe", "startPlain", "observePlain", "selectProtection"] as const) {
  test(`${method} aborts its pending HTTP wait with the ceremony signal`, async () => {
    const controller = new AbortController();
    let supplied: AbortSignal | null | undefined;
    const api = new SecurityClient(async (_input, init) => {
      supplied = init?.signal;
      return new Promise<Response>((_resolve, reject) => supplied?.addEventListener("abort", () => reject(supplied?.reason), { once: true }));
    }, () => "noisedeck_csrf=public-fixture");
    const request = method === "operate" ? api.operate({ challengeId: "public-operation", signature: "public-proof" }, controller.signal)
      : method === "observe" ? api.observe("public-operation", controller.signal)
        : method === "startPlain" ? api.startPlain(undefined, controller.signal)
          : method === "observePlain" ? api.observePlain("public-operation", controller.signal)
            : api.selectProtection("none", controller.signal);
    assert.equal(supplied, controller.signal);
    controller.abort();
    await assert.rejects(request, error => error instanceof DOMException && error.name === "AbortError");
  });
}
