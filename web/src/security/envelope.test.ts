import { test } from "node:test";
import assert from "node:assert/strict";
import { createHash, createPublicKey, verify } from "node:crypto";
import { decodeBase64url, encodeBase64url } from "./encoding.ts";
import {
  createMethodEnvelope, envelopeAdditionalData, generateMethodSalt, generateWorkspaceSecret,
  openMethodEnvelope, validateNewEncryptionPassword, workspaceCommitment,
} from "./envelope.ts";
import type { EnvelopeContext, MethodEnvelope, UnlockCredential } from "./envelope.ts";

// Public deterministic inputs shared with the protocol's independent vectors.
const secret = Uint8Array.from({ length: 32 }, (_, index) => index);
const salt = Uint8Array.from({ length: 32 }, (_, index) => index + 32);
const password = "  cafe\u0301 test password  ";
const passwordKeyHex = "d9448484791b29c6dca33763b14149a5d087da9bc19e5a5b4cc48735fa585664";
const baseContext = { instanceId: "instance_fixture", methodId: "password_fixture", revision: 1 };
const passwordContext: EnvelopeContext = { ...baseContext, kind: "password" };
const passwordCredential: UnlockCredential = { kind: "password", password };
const commitment = await workspaceCommitment(secret);

async function fixtureKey(): Promise<CryptoKey> {
  return crypto.subtle.importKey("raw", new Uint8Array(Buffer.from(passwordKeyHex, "hex")), "AES-GCM", false, ["encrypt", "decrypt"]);
}

async function maliciousPayload(envelope: MethodEnvelope, plaintext: Uint8Array): Promise<MethodEnvelope> {
  const ciphertext = await crypto.subtle.encrypt({ name: "AES-GCM", iv: decodeBase64url(envelope.nonce, 12),
    additionalData: envelopeAdditionalData(envelope), tagLength: 128 }, await fixtureKey(), new Uint8Array(plaintext));
  return { ...envelope, ciphertext: encodeBase64url(new Uint8Array(ciphertext)) };
}

test("AAD and workspace commitment match their independent byte definitions", async () => {
  assert.equal(new TextDecoder().decode(envelopeAdditionalData(passwordContext)),
    '["deck.envelope",1,"instance_fixture","password_fixture",1,"password"]');
  assert.equal(commitment, createHash("sha256").update("deck.volume.v1\0").update(secret).digest("base64url"));
  for (const context of [{ ...passwordContext, instanceId: "has a space" }, { ...passwordContext, methodId: "é" },
    { ...passwordContext, revision: 0 }, { ...passwordContext, revision: 1.5 }, { ...passwordContext, revision: 2 ** 32 },
    { ...passwordContext, kind: "unknown" }]) {
    assert.throws(() => envelopeAdditionalData(context as EnvelopeContext));
  }
});

test("password envelope uses the published PBKDF2 vector and exact plaintext array", async () => {
  const envelope = await createMethodEnvelope(passwordContext, secret, passwordCredential, salt);
  const plaintext = await crypto.subtle.decrypt({ name: "AES-GCM", iv: decodeBase64url(envelope.nonce, 12),
    additionalData: envelopeAdditionalData(passwordContext), tagLength: 128 }, await fixtureKey(), decodeBase64url(envelope.ciphertext, 4096));
  const payload = JSON.parse(new TextDecoder().decode(plaintext)) as unknown[];
  assert.equal(payload.length, 3);
  assert.equal(payload[0], 1);
  assert.equal(payload[1], encodeBase64url(secret));
  assert.ok(decodeBase64url(payload[2], 2048).length > 32);
  assert.equal(envelope.iterations, 600_000);
  assert.equal(JSON.stringify(envelope).includes(encodeBase64url(secret)), false);
});

test("each method opens the persisted envelope, signs a raw challenge and disposes secrets", async () => {
  const credentials: UnlockCredential[] = [passwordCredential, { kind: "passkey", prfResult: salt },
    { kind: "recovery", recoverySecret: salt }];
  for (const credential of credentials) {
    const context = { ...baseContext, kind: credential.kind };
    const envelope = await createMethodEnvelope(context, secret, credential, salt);
    const persisted: unknown = JSON.parse(JSON.stringify(envelope));
    const opened = await openMethodEnvelope(persisted, credential, envelope, commitment);
    const challenge = new Uint8Array(32).fill(42);
    const signature = await opened.signProof(challenge);
    assert.equal(signature.length, 64);
    const publicKey = createPublicKey({ key: Buffer.from(envelope.proofPublicKey, "base64url"), format: "der", type: "spki" });
    assert.equal(verify("sha256", challenge, { key: publicKey, dsaEncoding: "ieee-p1363" }, signature), true);
    assert.equal(verify("sha256", Buffer.from(encodeBase64url(challenge)), { key: publicKey, dsaEncoding: "ieee-p1363" }, signature), false);
    assert.deepEqual(opened.copyUnlockSecret(), secret);
    const copy = opened.copyUnlockSecret();
    copy.fill(0);
    assert.deepEqual(opened.copyUnlockSecret(), secret);
    assert.deepEqual(Object.keys(opened), []);
    assert.throws(() => JSON.stringify(opened), /cannot be serialized/);
    await assert.rejects(opened.signProof(new Uint8Array(31)));
    opened.dispose();
    opened.dispose();
    assert.throws(() => opened.copyUnlockSecret(), /disposed/);
    await assert.rejects(opened.signProof(challenge), /disposed/);
  }
});

test("PRF and recovery use independently derived HKDF keys bound to their AAD", async () => {
  for (const credential of [{ kind: "passkey", prfResult: salt }, { kind: "recovery", recoverySecret: salt }] as const) {
    const envelope = await createMethodEnvelope({ ...baseContext, kind: credential.kind }, secret, credential, salt);
    const material = await crypto.subtle.importKey("raw", salt, "HKDF", false, ["deriveKey"]);
    const key = await crypto.subtle.deriveKey({ name: "HKDF", hash: "SHA-256", salt,
      info: envelopeAdditionalData(envelope) }, material, { name: "AES-GCM", length: 256 }, false, ["decrypt"]);
    const plaintext = await crypto.subtle.decrypt({ name: "AES-GCM", iv: decodeBase64url(envelope.nonce, 12),
      additionalData: envelopeAdditionalData(envelope), tagLength: 128 }, key, decodeBase64url(envelope.ciphertext, 4096));
    assert.equal(JSON.parse(new TextDecoder().decode(plaintext))[1], encodeBase64url(secret));
    assert.equal("iterations" in envelope, false);
  }
});

test("password normalization preserves whitespace and case without accepting malformed Unicode", async () => {
  const envelope = await createMethodEnvelope(passwordContext, secret, passwordCredential, salt);
  (await openMethodEnvelope(envelope, { kind: "password", password: password.normalize("NFC") }, envelope, commitment)).dispose();
  for (const wrong of [password.trim(), password.toUpperCase(), "a completely different password"]) {
    await assert.rejects(openMethodEnvelope(envelope, { kind: "password", password: wrong }, envelope, commitment));
  }
  for (const invalid of ["", "short", "a".repeat(1025), "\ud800" + "a".repeat(20), "\udfff" + "a".repeat(20), "🗝".repeat(14)]) {
    assert.throws(() => validateNewEncryptionPassword(invalid));
  }
  for (const valid of ["🗝".repeat(15), password, "a".repeat(1024)]) validateNewEncryptionPassword(valid);
});

test("modified authenticated context, ciphertext, nonce, salt or commitment cannot open", async () => {
  const envelope = await createMethodEnvelope(passwordContext, secret, passwordCredential, salt);
  const ciphertext = decodeBase64url(envelope.ciphertext, 4096);
  ciphertext[0] = ciphertext[0]! ^ 1;
  const altered: MethodEnvelope[] = [
    { ...envelope, instanceId: "other_instance" }, { ...envelope, methodId: "other_method" },
    { ...envelope, revision: 2 }, { ...envelope, ciphertext: encodeBase64url(ciphertext) },
    { ...envelope, nonce: encodeBase64url(new Uint8Array(12)) },
    { ...envelope, salt: encodeBase64url(new Uint8Array(32)) },
  ];
  for (const candidate of altered) await assert.rejects(openMethodEnvelope(candidate, passwordCredential, candidate, commitment));
  await assert.rejects(openMethodEnvelope(envelope, passwordCredential, { ...envelope, methodId: "other" }, commitment));
  await assert.rejects(openMethodEnvelope(envelope, passwordCredential, envelope, await workspaceCommitment(salt)));
});

test("format and KDF bounds are rejected before derivation", async () => {
  const envelope = await createMethodEnvelope(passwordContext, secret, passwordCredential, salt);
  const deriveKey = crypto.subtle.deriveKey;
  let calls = 0;
  crypto.subtle.deriveKey = (async () => { calls++; throw new Error("KDF should not run"); }) as typeof deriveKey;
  try {
    for (const bad of [
      ...[0, 599999, 600001, 2 ** 32, "600000", null].map(iterations => ({ ...envelope, iterations })),
      { ...envelope, version: 2 }, { ...envelope, algorithm: "AES-CBC" },
      { ...envelope, salt: "AA" }, { ...envelope, nonce: "AA" },
      { ...envelope, ciphertext: encodeBase64url(new Uint8Array(4097)) },
      { ...envelope, proofPublicKey: encodeBase64url(new Uint8Array(1025)) },
    ]) await assert.rejects(openMethodEnvelope(bad, passwordCredential, envelope, commitment));
    assert.equal(calls, 0);
  } finally { crypto.subtle.deriveKey = deriveKey; }
});

test("malformed encrypted payloads and swapped proof keys fail even with a valid GCM tag", async () => {
  const envelope = await createMethodEnvelope(passwordContext, secret, passwordCredential, salt);
  for (const payload of ["{}", "[1]", "[1,1,1]", '[2,"AA","AA"]', '[1,"AA","AA",1]',
    JSON.stringify([1, encodeBase64url(secret), encodeBase64url(new Uint8Array(2049))]),
    JSON.stringify([1, encodeBase64url(secret), "AA"])]) {
    await assert.rejects(openMethodEnvelope(await maliciousPayload(envelope, new TextEncoder().encode(payload)), passwordCredential, envelope, commitment));
  }
  await assert.rejects(openMethodEnvelope(await maliciousPayload(envelope, new Uint8Array([0xff])), passwordCredential, envelope, commitment));
  const other = await createMethodEnvelope(passwordContext, secret, passwordCredential, salt);
  assert.notEqual(other.proofPublicKey, envelope.proofPublicKey);
  assert.notEqual(other.nonce, envelope.nonce);
  const swapped = { ...envelope, proofPublicKey: other.proofPublicKey };
  await assert.rejects(openMethodEnvelope(swapped, passwordCredential, envelope, commitment), /Unexpected method proof key/);
  await assert.rejects(openMethodEnvelope(swapped, passwordCredential, swapped, commitment));
});

test("wrong credential kinds and invalid key lengths fail", async () => {
  await assert.rejects(createMethodEnvelope(passwordContext, secret, { kind: "recovery", recoverySecret: salt }, salt));
  for (const size of [0, 31, 33]) {
    await assert.rejects(createMethodEnvelope({ ...baseContext, kind: "passkey" }, secret,
      { kind: "passkey", prfResult: new Uint8Array(size) }, salt));
    await assert.rejects(workspaceCommitment(new Uint8Array(size)));
  }
  assert.equal(generateWorkspaceSecret().length, 32);
  assert.equal(generateMethodSalt().length, 32);
  assert.notDeepEqual(generateWorkspaceSecret(), generateWorkspaceSecret());
});
