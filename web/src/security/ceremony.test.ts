import { test } from "node:test";
import assert from "node:assert/strict";
import { saveCandidate, prepareProof } from "./ceremony.ts";
import { generateWorkspaceSecret, workspaceCommitment } from "./envelope.ts";
import { encodeBase64url } from "./encoding.ts";
import type { PreparedMethod, SecurityClient } from "./client.ts";

test("saved-method proof opens read-back ciphertext and signs the fresh server challenge", async () => {
  const secret = generateWorkspaceSecret();
  const commitment = await workspaceCommitment(secret);
  const challenge = new Uint8Array(32).fill(4);
  let saved: unknown;
  let reads = 0;
  const api = {
    async store(_method: unknown, envelope: unknown) { saved = structuredClone(envelope); },
    async envelope() { reads++; return structuredClone(saved); },
    async challenge() { return { id: encodeBase64url(challenge), operationId: encodeBase64url(challenge), challenge: encodeBase64url(challenge), expiresAt: "2030-01-01T00:00:00Z" }; },
  } as unknown as SecurityClient;
  const prepared: PreparedMethod = { method: { id: "test-method", revision: 1, kind: "password", label: "Password", status: "pending", salt: encodeBase64url(new Uint8Array(32).fill(3)) } };
  const credential = { kind: "password", password: "public fixture password" } as const;
  const method = await saveCandidate(api, "test-instance", prepared, secret, commitment, credential);
  secret.fill(0);
  const proof = await prepareProof(api, "test-instance", commitment, method, { operation: "verify-method", method: { id: method.id, revision: method.revision } }, credential, new AbortController().signal);
  assert.equal(reads, 2, "verification must re-read persisted bytes after the upload check");
  assert.equal(proof.signature.length, 86);
  proof.opened.dispose();
  await assert.rejects(prepareProof(api, "test-instance", commitment, method, { operation: "verify-method", method }, { kind: "password", password: "wrong fixture password" }, new AbortController().signal));
  saved = { ...(saved as object), ciphertext: "swapped" };
  await assert.rejects(prepareProof(api, "test-instance", commitment, method, { operation: "verify-method", method }, credential, new AbortController().signal));
});


test("cancellation during envelope fetch aborts without returning an opened method", async () => {
  const abort = new AbortController();
  let receivedSignal: AbortSignal | undefined;
  let fetching!: () => void;
  const entered = new Promise<void>(resolve => { fetching = resolve; });
  const api = {
    async challenge() { return { id: "public-id", operationId: "public-id", challenge: encodeBase64url(new Uint8Array(32)), expiresAt: "2030-01-01T00:00:00Z" }; },
    async envelope(_method: unknown, signal: AbortSignal) {
      receivedSignal = signal; fetching();
      return new Promise((_resolve, reject) => signal.addEventListener("abort", () => reject(signal.reason), { once: true }));
    },
  } as unknown as SecurityClient;
  const method = { id: "test-method", revision: 1, kind: "password", label: "Password", status: "active", salt: "" } as const;
  const pending = prepareProof(api, "test-instance", "commitment", method, { operation: "unlock", method }, { kind: "password", password: "public fixture password" }, abort.signal);
  await entered;
  abort.abort();
  await assert.rejects(pending, { name: "AbortError" });
  assert.equal(receivedSignal, abort.signal);
});


test("cancelled candidate wrapping cannot submit a late envelope", async () => {
  const abort = new AbortController();
  const secret = generateWorkspaceSecret();
  const commitment = await workspaceCommitment(secret);
  let stores = 0;
  const api = { async store() { stores++; } } as unknown as SecurityClient;
  const prepared: PreparedMethod = { method: { id: "cancelled-method", revision: 1, kind: "password", label: "Password", status: "pending", salt: encodeBase64url(new Uint8Array(32).fill(3)) } };
  const pending = saveCandidate(api, "test-instance", prepared, secret, commitment, { kind: "password", password: "public fixture password" }, undefined, abort.signal);
  abort.abort();
  await assert.rejects(pending, { name: "AbortError" });
  assert.equal(stores, 0);
  secret.fill(0);
});
