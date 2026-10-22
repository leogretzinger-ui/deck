import assert from "node:assert/strict";
import test from "node:test";
import { MethodManagement } from "./management-controller.ts";
import type { ManagementView } from "./management.ts";
import type { SecurityAction, SecurityClient, SecurityMethod, SecurityState } from "../../security/client.ts";
import { createMethodEnvelope, generateWorkspaceSecret, workspaceCommitment, type MethodEnvelope, type MethodKind, type OpenedMethod } from "../../security/envelope.ts";
import { decodeBase64url, encodeBase64url } from "../../security/encoding.ts";
import { formatRecoveryKey, generateRecoverySecret } from "../../security/recovery.ts";

const password = "public original password fixture";
const newPassword = "public replacement password fixture";

async function fixture(recovered = false, sourceKind: "password" | "recovery" = "password") {
  const secret = generateWorkspaceSecret();
  const commitment = await workspaceCommitment(secret);
  const state: SecurityState = { instanceId: "test-workspace", phase: "ready", commitment, storage: "open", runtime: "running", locked: false, browserGranted: true,
    ownerBound: true, workspaceAuthorized: true, onboarding: { policy: "required", protection: "encrypted", started: true, setupComplete: true } };
  const methods: SecurityMethod[] = [];
  const actions: SecurityAction[] = [];
  const challenges = new Map<string, { action: SecurityAction; bytes: Uint8Array<ArrayBuffer> }>();
  const complete = new Set<string>();
  const envelopeReads: string[] = [];
  let sequence = 0, lost = 0, closed = 0, loseResponse = false, stripGrant = false;
  let blockPrepare: (() => Promise<void>) | undefined;
  let blockStore: ((signal?: AbortSignal) => Promise<void>) | undefined;
  let blockOperate: ((signal?: AbortSignal) => Promise<void>) | undefined;
  let blockObserve: ((signal?: AbortSignal) => Promise<void>) | undefined;
  async function prepare(kind: MethodKind, label: string) {
    await blockPrepare?.();
    const method: SecurityMethod = { id: `method-${++sequence}`, revision: 1, kind, label, status: "pending", salt: encodeBase64url(crypto.getRandomValues(new Uint8Array(32))) };
    methods.push(method);
    return { method: structuredClone(method) };
  }
  const original = (await prepare(sourceKind, "Original method")).method;
  const recoverySecret = generateRecoverySecret();
  const recoveryKey = await formatRecoveryKey(recoverySecret);
  original.envelope = await createMethodEnvelope({ instanceId: state.instanceId, methodId: original.id, revision: 1, kind: sourceKind }, secret,
    sourceKind === "password" ? { kind: "password", password } : { kind: "recovery", recoverySecret }, decodeBase64url(original.salt, 32));
  recoverySecret.fill(0);
  Object.assign(methods[0]!, original, { status: "active" });
  const api = {
    async state() { return structuredClone(state); },
    async methods() { return structuredClone(methods); },
    prepare,
    async label(method: SecurityMethod, label: string) { methods.find(item => item.id === method.id)!.label = label; },
    async store(method: SecurityMethod, envelope: MethodEnvelope, suppliedCommitment: string, _registration?: unknown, signal?: AbortSignal) {
      await blockStore?.(signal);
      signal?.throwIfAborted();
      assert.equal(suppliedCommitment, commitment);
      Object.assign(methods.find(item => item.id === method.id)!, { envelope: structuredClone(envelope) });
    },
    async envelope(method: SecurityMethod) { envelopeReads.push(method.id); return structuredClone(methods.find(item => item.id === method.id)!.envelope); },
    async challenge(action: SecurityAction) {
      const id = `operation-${++sequence}`;
      const bytes = crypto.getRandomValues(new Uint8Array(32));
      challenges.set(id, { action: structuredClone(action), bytes });
      return { id, operationId: id, challenge: encodeBase64url(bytes), expiresAt: new Date(Date.now() + 300000).toISOString() };
    },
    async operate(body: { challengeId: string; signature: string }, signal?: AbortSignal) {
      assert.deepEqual(Object.keys(body).sort(), ["challengeId", "signature"], "management never sends a raw volume key or password");
      const challenge = challenges.get(body.challengeId)!;
      assert.ok(challenge, "operation challenge is single-use");
      challenges.delete(body.challengeId);
      const method = methods.find(item => item.id === challenge.action.method.id)!;
      const publicKey = await crypto.subtle.importKey("spki", decodeBase64url(method.envelope!.proofPublicKey, 256), { name: "ECDSA", namedCurve: "P-256" }, false, ["verify"]);
      assert.equal(await crypto.subtle.verify({ name: "ECDSA", hash: "SHA-256" }, publicKey, decodeBase64url(body.signature, 64, 64), challenge.bytes), true);
      const action = challenge.action;
      actions.push(action);
      if (action.operation === "verify-method") method.verifiedAt = new Date().toISOString();
      else if (action.operation === "add-method" || action.operation === "replace-method") {
        const candidate = methods.find(item => item.id === action.target!.id)!;
        assert.equal(method.status, "active");
        assert.equal(candidate.status, "pending");
        assert.ok(candidate.verifiedAt, "the candidate must be tested before activation");
        candidate.status = "active";
        if (action.replacement) methods.find(item => item.id === action.replacement!.id)!.status = "removed";
        if (stripGrant) state.browserGranted = false;
      } else if (action.operation === "remove-method") {
        methods.find(item => item.id === action.target!.id)!.status = "removed";
      }
      complete.add(body.challengeId);
      await blockOperate?.(signal);
      if (loseResponse) { loseResponse = false; throw new TypeError("response lost"); }
      return { id: body.challengeId, status: "accepted" };
    },
    async observe(id: string, signal?: AbortSignal) {
      await blockObserve?.(signal);
      return { id, status: complete.has(id) ? "complete" : "unknown", storage: "open", runtime: "running", locked: false };
    },
  } as unknown as SecurityClient;
  const views: ManagementView[] = [];
  const manager = new MethodManagement({ api, state, ...(recovered ? { recoveryMethod: { id: "used-recovery", revision: 1 } } : {}), async onClose() { closed++; }, authorizationLost() { lost++; } }, view => views.push(view), "test-workspace.example");
  await manager.start();
  return { manager, methods, actions, views, original, envelopeReads, secret, recoveryKey,
    view: () => views.at(-1)!, loseNextResponse: () => { loseResponse = true; }, revokeOnMutation: () => { stripGrant = true; },
    blockStore: (fn: (signal?: AbortSignal) => Promise<void>) => { blockStore = fn; },
    blockOperate: (fn: (signal?: AbortSignal) => Promise<void>) => { blockOperate = fn; },
    blockObserve: (fn: (signal?: AbortSignal) => Promise<void>) => { blockObserve = fn; },
    blockPrepare: (fn: () => Promise<void>) => { blockPrepare = fn; }, lost: () => lost, closed: () => closed };
}

function responseGate(abortable = false) {
  let entered!: () => void, release!: () => void;
  let signal: AbortSignal | undefined;
  const ready = new Promise<void>(resolve => { entered = resolve; });
  const response = new Promise<void>(resolve => { release = resolve; });
  return { ready, release: () => release(), signal: () => signal,
    wait(current?: AbortSignal): Promise<void> {
      signal = current; entered();
      if (!abortable) return response;
      return new Promise<void>((resolve, reject) => {
        const abort = () => reject(current?.reason);
        current?.addEventListener("abort", abort, { once: true });
        void response.then(() => { current?.removeEventListener("abort", abort); resolve(); });
      });
    },
  };
}

async function savePassword(f: Awaited<ReturnType<typeof fixture>>) {
  await f.manager.run({ kind: "replace", id: f.original.id });
  await f.manager.run({ kind: "password", current: password, password: newPassword });
  assert.equal(f.view().screen.kind, "check");
  assert.equal(f.methods[0]!.status, "active");
  assert.equal(f.methods[1]!.status, "pending");
}

async function authorizeReplacement(f: Awaited<ReturnType<typeof fixture>>) {
  await f.manager.run({ kind: "activate" });
  await f.manager.run({ kind: "use-method", id: f.original.id });
  await f.manager.run({ kind: "credential", value: password });
}

test("password replacement rereads the saved candidate and keeps the old method until fresh authorization", async () => {
  const f = await fixture();
  try {
    await savePassword(f);
    await f.manager.run({ kind: "activate" });
    assert.equal(f.methods[0]!.status, "active");
    await f.manager.run({ kind: "candidate", value: "wrong candidate fixture password" });
    assert.equal(f.view().screen.kind, "check");
    assert.ok(f.view().error);
    assert.equal(f.methods[0]!.status, "active");
    await f.manager.run({ kind: "candidate", value: newPassword });
    assert.equal(f.view().screen.kind, "ready");
    assert.ok(f.envelopeReads.filter(id => id === f.methods[1]!.id).length >= 3);
    await authorizeReplacement(f);
    assert.equal(f.methods[0]!.status, "removed");
    assert.equal(f.methods[1]!.status, "active");
    assert.deepEqual(f.actions.map(action => [action.operation, action.method.id]), [
      ["verify-method", f.original.id], ["verify-method", f.methods[1]!.id], ["replace-method", f.original.id],
    ]);
    assert.equal(f.view().screen.kind, "list");
  } finally { f.manager.dispose(); f.secret.fill(0); }
});

test("lost replacement response observes its ID and never resubmits the proof", async () => {
  const f = await fixture();
  try {
    await savePassword(f);
    await f.manager.run({ kind: "candidate", value: newPassword });
    f.loseNextResponse();
    await authorizeReplacement(f);
    assert.equal(f.view().screen.kind, "unknown");
    const count = f.actions.length;
    await f.manager.run({ kind: "cancel" });
    assert.equal(f.view().screen.kind, "unknown");
    await f.manager.run({ kind: "observe" });
    assert.equal(f.actions.length, count);
    assert.equal(f.view().screen.kind, "list");
    assert.equal(f.methods[1]!.status, "active");
  } finally { f.manager.dispose(); f.secret.fill(0); }
});

test("replacement revoking this browser yields to the coordinator without rendering a stale method list", async () => {
  const f = await fixture();
  try {
    await savePassword(f);
    await f.manager.run({ kind: "candidate", value: newPassword });
    f.revokeOnMutation();
    await authorizeReplacement(f);
    assert.equal(f.lost(), 1);
    const count = f.views.length;
    await f.manager.run({ kind: "add-passkey" });
    assert.equal(f.views.length, count);
  } finally { f.manager.dispose(); f.secret.fill(0); }
});

test("last everyday method removal sends no request and active test uses its own fresh proof", async () => {
  const f = await fixture();
  try {
    await f.manager.run({ kind: "remove", id: f.original.id });
    assert.equal(f.view().screen.kind, "last-method");
    assert.equal(f.actions.length, 0);
    await f.manager.run({ kind: "cancel" });
    await f.manager.run({ kind: "test", id: f.original.id });
    await f.manager.run({ kind: "credential", value: password });
    assert.equal(f.actions[0]!.operation, "verify-method");
    assert.equal(f.methods[0]!.status, "active");
    assert.ok(f.methods[0]!.verifiedAt);
  } finally { f.manager.dispose(); f.secret.fill(0); }
});

test("cancelling while preparation waits cannot save, enable or repaint a candidate", async () => {
  const f = await fixture();
  try {
    let release!: () => void;
    let started!: () => void;
    const ready = new Promise<void>(resolve => { started = resolve; });
    f.blockPrepare(() => { started(); return new Promise<void>(resolve => { release = resolve; }); });
    await f.manager.run({ kind: "replace", id: f.original.id });
    const operation = f.manager.run({ kind: "password", current: password, password: newPassword });
    await ready;
    // Retain the actual browser buffers to check erasure while HTTP is stalled.
    const held = f.manager as unknown as { opened: OpenedMethod; volumeCopy: Uint8Array };
    const opened = held.opened, volume = held.volumeCopy;
    assert.ok(volume.some(byte => byte !== 0));
    await f.manager.run({ kind: "cancel" });
    assert.ok(volume.every(byte => byte === 0));
    assert.throws(() => opened.copyUnlockSecret(), /disposed/);
    release(); await operation;
    assert.equal(f.view().screen.kind, "list");
    assert.equal(f.methods[0]!.status, "active");
    assert.equal(f.methods[1]!.envelope, undefined);
  } finally { f.manager.dispose(); f.secret.fill(0); }
});

test("recovery replacement generates a new browser key and cannot remove the old key before verification", async () => {
  const f = await fixture();
  try {
    await f.manager.run({ kind: "add-recovery" });
    await f.manager.run({ kind: "use-method", id: f.original.id });
    await f.manager.run({ kind: "credential", value: password });
    const screen = f.view().screen;
    assert.equal(screen.kind, "recovery-save");
    if (screen.kind !== "recovery-save") throw new Error("missing key");
    const key = screen.key;
    await f.manager.run({ kind: "saved-key" });
    await f.manager.run({ kind: "candidate", value: key });
    await f.manager.run({ kind: "name", value: "Recovery key" });
    await f.manager.run({ kind: "use-method", id: f.original.id });
    await f.manager.run({ kind: "credential", value: password });
    const old = f.methods.find(method => method.kind === "recovery")!;
    assert.equal(old.status, "active");
    await f.manager.run({ kind: "replace", id: old.id });
    await f.manager.run({ kind: "use-method", id: f.original.id });
    await f.manager.run({ kind: "credential", value: password });
    const replacement = f.view().screen;
    if (replacement.kind !== "recovery-save") throw new Error("missing replacement key");
    assert.notEqual(replacement.key, key);
    assert.equal(old.status, "active");
    await f.manager.run({ kind: "saved-key" });
    await f.manager.run({ kind: "candidate", value: key });
    assert.equal(old.status, "active");
    assert.equal(f.view().screen.kind, "check");
    await f.manager.run({ kind: "candidate", value: replacement.key });
    await authorizeReplacement(f);
    assert.equal(old.status, "removed");
    assert.equal(f.methods.filter(method => method.kind === "recovery" && method.status === "active").length, 1);
  } finally { f.manager.dispose(); f.secret.fill(0); }
});

test("recovery follow-up uses current metadata and only explicit Open returns to the workspace", async () => {
  const f = await fixture(true);
  try {
    assert.equal(f.view().screen.kind, "recovery-checklist");
    assert.equal(f.view().methods.filter(method => method.status === "active").length, 1);
    await f.manager.run({ kind: "review-methods" });
    assert.equal(f.view().screen.kind, "list");
    await f.manager.run({ kind: "close" });
    assert.equal(f.closed(), 0);
    assert.equal(f.view().screen.kind, "recovery-checklist");
    await f.manager.run({ kind: "open-workspace" });
    assert.equal(f.closed(), 1);
  } finally { f.manager.dispose(); f.secret.fill(0); }
});

test("adding then removing a second everyday method requires distinct fresh authorizations", async () => {
  const f = await fixture();
  try {
    await f.manager.run({ kind: "add-password" });
    await f.manager.run({ kind: "password", password: newPassword });
    await f.manager.run({ kind: "use-method", id: f.original.id });
    await f.manager.run({ kind: "credential", value: password });
    await f.manager.run({ kind: "candidate", value: newPassword });
    await f.manager.run({ kind: "name", value: "Second password" });
    await f.manager.run({ kind: "use-method", id: f.original.id });
    await f.manager.run({ kind: "credential", value: password });
    const added = f.methods.find(method => method.id !== f.original.id)!;
    assert.equal(added.status, "active");
    assert.equal(f.methods[0]!.status, "active");
    await f.manager.run({ kind: "remove", id: added.id });
    await f.manager.run({ kind: "confirm-remove" });
    assert.equal(added.status, "active");
    await f.manager.run({ kind: "use-method", id: f.original.id });
    await f.manager.run({ kind: "credential", value: password });
    assert.equal(added.status, "removed");
    assert.equal(f.methods[0]!.status, "active");
    assert.equal(f.actions.at(-1)!.operation, "remove-method");
    assert.deepEqual(f.actions.at(-1)!.target, { id: added.id, revision: 1 });
  } finally { f.manager.dispose(); f.secret.fill(0); }
});

test("cancelling a pending candidate upload aborts its HTTP request", async () => {
  const f = await fixture();
  try {
    let entered!: () => void;
    const ready = new Promise<void>(resolve => { entered = resolve; });
    let uploadSignal: AbortSignal | undefined;
    f.blockStore(signal => {
      uploadSignal = signal; entered();
      return new Promise<void>((_resolve, reject) => signal?.addEventListener("abort", () => reject(signal.reason), { once: true }));
    });
    await f.manager.run({ kind: "replace", id: f.original.id });
    const operation = f.manager.run({ kind: "password", current: password, password: newPassword });
    await ready;
    assert.ok(uploadSignal, "the candidate upload receives the controller cancellation signal");
    await f.manager.run({ kind: "cancel" });
    await operation;
    assert.equal(uploadSignal.aborted, true);
    assert.equal(f.view().screen.kind, "list");
    assert.equal(f.methods[0]!.status, "active");
    assert.equal(f.methods[1]!.envelope, undefined);
  } finally { f.manager.dispose(); f.secret.fill(0); }
});

for (const stage of ["operate", "observe"] as const) {
  test(`cancelling a source proof waiting on ${stage} never prepares a candidate after the response arrives`, async () => {
    const f = await fixture();
    const gate = responseGate();
    try {
      if (stage === "operate") f.blockOperate(gate.wait);
      else f.blockObserve(gate.wait);
      await f.manager.run({ kind: "replace", id: f.original.id });
      const operation = f.manager.run({ kind: "password", current: password, password: newPassword });
      await gate.ready;
      const held = f.manager as unknown as { opened: OpenedMethod; pending: { id: string } };
      const opened = held.opened, operationID = held.pending.id;
      await f.manager.run({ kind: "cancel" });
      assert.equal(gate.signal()?.aborted, true, "the pending operation wait must receive the cancelled ceremony signal");
      assert.throws(() => opened.copyUnlockSecret(), /disposed/);
      assert.equal(f.view().screen.kind, "unknown");
      assert.equal(f.view().busy, false);
      gate.release(); await operation;
      assert.equal(f.methods.length, 1, "late proof completion cannot prepare or store a candidate");
      assert.equal(f.view().screen.kind, "unknown");
      assert.equal(held.pending.id, operationID, "the public receipt ID survives cancellation");
      const proofCount = f.actions.length;
      await f.manager.run({ kind: "observe" });
      assert.equal(f.view().screen.kind, "list");
      assert.equal(f.methods.length, 1);
      assert.equal(f.methods[0]!.status, "active");
      assert.equal(f.actions.length, proofCount, "observation never resubmits a proof");
    } finally { gate.release(); f.manager.dispose(); f.secret.fill(0); }
  });
}

test("cancelled candidate verification observes completion without reviving the abandoned flow", async () => {
  const f = await fixture();
  const gate = responseGate();
  try {
    await savePassword(f);
    f.blockOperate(gate.wait);
    const operation = f.manager.run({ kind: "candidate", value: newPassword });
    await gate.ready;
    await f.manager.run({ kind: "cancel" });
    assert.equal(gate.signal()?.aborted, true);
    gate.release(); await operation;
    assert.equal(f.view().screen.kind, "unknown");
    const proofCount = f.actions.length;
    await f.manager.run({ kind: "observe" });
    assert.equal(f.view().screen.kind, "list", "a cleared flow must not strand the screen or become ready for activation");
    assert.equal(f.actions.length, proofCount);
    assert.equal(f.methods[0]!.status, "active");
    assert.equal(f.methods[1]!.status, "pending");
    assert.ok(f.methods[1]!.verifiedAt);
    await f.manager.run({ kind: "activate" });
    assert.equal(f.view().screen.kind, "list");
  } finally { gate.release(); f.manager.dispose(); f.secret.fill(0); }
});

test("disposal aborts a recovery source observation and immediately clears its parsed credential", async () => {
  const f = await fixture(false, "recovery");
  const gate = responseGate(true);
  try {
    f.blockObserve(gate.wait);
    await f.manager.run({ kind: "add-password" });
    await f.manager.run({ kind: "password", password: newPassword });
    await f.manager.run({ kind: "use-method", id: f.original.id });
    const operation = f.manager.run({ kind: "credential", value: f.recoveryKey });
    await gate.ready;
    const held = f.manager as unknown as { opened: OpenedMethod; credentialInUse: { kind: "recovery"; recoverySecret: Uint8Array } };
    const opened = held.opened, credential = held.credentialInUse;
    assert.ok(credential.recoverySecret.some(byte => byte !== 0));
    f.manager.dispose();
    assert.equal(gate.signal()?.aborted, true);
    assert.throws(() => opened.copyUnlockSecret(), /disposed/);
    assert.ok(credential.recoverySecret.every(byte => byte === 0), "disposal erases the parsed key before any response arrives");
    await operation;
    const viewCount = f.views.length;
    gate.release();
    assert.equal(f.methods.length, 1);
    assert.equal(f.views.length, viewCount);
  } finally { gate.release(); f.manager.dispose(); f.secret.fill(0); }
});

test("cancelling recovery-key formatting clears the candidate and cannot restore a key afterward", async t => {
  const f = await fixture();
  const gate = responseGate();
  const generated: Uint8Array[] = [];
  const random = crypto.getRandomValues.bind(crypto);
  const digest = crypto.subtle.digest.bind(crypto.subtle);
  const prefix = new TextEncoder().encode("deck.recovery.DK1\0");
  t.mock.method(crypto, "getRandomValues", (array: Uint8Array<ArrayBuffer>) => {
    const filled = random(array); generated.push(filled); return filled;
  });
  t.mock.method(crypto.subtle, "digest", async (algorithm: AlgorithmIdentifier, data: BufferSource) => {
    const bytes = data instanceof ArrayBuffer ? new Uint8Array(data) : new Uint8Array(data.buffer, data.byteOffset, data.byteLength);
    if (prefix.every((byte, index) => bytes[index] === byte)) await gate.wait();
    return digest(algorithm, data);
  });
  try {
    await f.manager.run({ kind: "add-recovery" });
    await f.manager.run({ kind: "use-method", id: f.original.id });
    const operation = f.manager.run({ kind: "credential", value: password });
    await gate.ready;
    const candidateSecret = generated.at(-1)!;
    assert.ok(candidateSecret.some(byte => byte !== 0));
    await f.manager.run({ kind: "cancel" });
    assert.ok(candidateSecret.every(byte => byte === 0), "the generated candidate is erased during the pending crypto wait");
    gate.release(); await operation;
    assert.equal((f.manager as unknown as { key?: string }).key, undefined);
    assert.equal(f.view().screen.kind, "list");
    assert.equal(f.methods[0]!.status, "active");
    assert.equal(f.methods[1]!.envelope, undefined);
  } finally { gate.release(); f.manager.dispose(); f.secret.fill(0); }
});
