import { test } from "node:test";
import assert from "node:assert/strict";
import { encodeBase64url } from "./encoding.ts";
import { passkeyPRFResult, serializePublicCredential } from "./webauthn.ts";

function credential(registration = false): PublicKeyCredential {
  const id = new Uint8Array([1, 2, 3]);
  const prfSecret = new Uint8Array(32).fill(42);
  const response = registration
    ? { clientDataJSON: new Uint8Array([4, 5]).buffer, attestationObject: new Uint8Array([6, 7]).buffer,
      getTransports: () => ["internal", "hybrid"] }
    : { clientDataJSON: new Uint8Array([4, 5]).buffer, authenticatorData: new Uint8Array([6, 7]).buffer,
      signature: new Uint8Array([8, 9]).buffer, userHandle: null };
  return {
    id: encodeBase64url(id), rawId: id.buffer, type: "public-key", authenticatorAttachment: "platform", response,
    getClientExtensionResults: () => ({ prf: { enabled: true, results: { first: prfSecret.buffer, second: prfSecret.buffer } },
      credProps: { rk: true }, unknownExtension: { secret: "must not be copied" } }),
    toJSON: () => { throw new Error("toJSON would expose PRF secrets"); },
    unexpectedSecret: prfSecret,
  } as unknown as PublicKeyCredential;
}

test("registration serialization allowlists public fields and excludes all PRF outputs", () => {
  const serialized = serializePublicCredential(credential(true));
  assert.deepEqual(serialized, {
    id: "AQID", rawId: "AQID", type: "public-key", authenticatorAttachment: "platform",
    response: { clientDataJSON: "BAU", attestationObject: "Bgc", transports: ["internal", "hybrid"] },
    clientExtensionResults: { prf: { enabled: true }, credProps: { rk: true } },
  });
  assert.equal(JSON.stringify(serialized).includes(encodeBase64url(new Uint8Array(32).fill(42))), false);
  assert.equal(JSON.stringify(serialized).includes("results"), false);
});

test("assertion serialization keeps signature and user handle without calling toJSON", () => {
  const assertion = credential();
  assert.deepEqual(serializePublicCredential(assertion).response,
    { clientDataJSON: "BAU", authenticatorData: "Bgc", signature: "CAk", userHandle: null });
  const withHandle = { ...assertion, response: { ...assertion.response, userHandle: new Uint8Array([10, 11]).buffer } };
  assert.equal(serializePublicCredential(withHandle as PublicKeyCredential).response.userHandle, "Cgs");
});

test("serialization never copies toJSON extension secrets", () => {
  const fake = credential(true);
  Object.defineProperty(fake, "toJSON", { value: () => ({ clientExtensionResults: {
    prf: { results: { first: "dangerous copied secret" } },
  } }) });
  assert.equal(JSON.stringify(serializePublicCredential(fake)).includes("dangerous"), false);
  assert.equal(JSON.stringify(serializePublicCredential(fake)).includes("unexpectedSecret"), false);
});

test("PRF result extraction requires an actual 32-byte result and returns an owned copy", () => {
  const fake = credential();
  const first = passkeyPRFResult(fake);
  assert.equal(first.length, 32);
  assert.equal(first[0], 42);
  first.fill(0);
  assert.equal(passkeyPRFResult(fake)[0], 42);
  for (const outputs of [{}, { prf: { enabled: true } }, { prf: { results: {} } },
    { prf: { results: { first: new Uint8Array(31) } } }, { prf: { results: { first: new Uint8Array(33) } } },
    { prf: { results: { first: "not bytes" } } }]) {
    assert.throws(() => passkeyPRFResult({ ...fake, getClientExtensionResults: () => outputs } as PublicKeyCredential));
  }
  const sliced = new Uint8Array(40).fill(12).subarray(4, 36);
  assert.deepEqual(passkeyPRFResult({ ...fake, getClientExtensionResults: () => ({ prf: { results: { first: sliced } } }) } as PublicKeyCredential),
    new Uint8Array(32).fill(12));
});

test("invalid credential identifiers and oversized public fields fail before submission", () => {
  const fake = credential();
  for (const altered of [
    { ...fake, id: "AQID=" }, { ...fake, type: "password" },
    { ...fake, rawId: new Uint8Array(1025).buffer },
    { ...fake, authenticatorAttachment: "unexpected" },
    { ...fake, response: { ...fake.response, clientDataJSON: new Uint8Array(16385).buffer } },
    { ...fake, response: { ...fake.response, signature: new Uint8Array(4097).buffer } },
    { ...fake, response: { ...fake.response, userHandle: new Uint8Array(65).buffer } },
  ]) assert.throws(() => serializePublicCredential(altered as PublicKeyCredential));
});
