import { copyBytes, decodeBase64url, encodeBase64url, equalBytes } from "./encoding.ts";

export type MethodKind = "passkey" | "password" | "recovery";

export interface EnvelopeContext {
  readonly instanceId: string;
  readonly methodId: string;
  readonly revision: number;
  readonly kind: MethodKind;
}

export interface MethodEnvelope extends EnvelopeContext {
  readonly version: 1;
  readonly salt: string;
  readonly nonce: string;
  readonly ciphertext: string;
  readonly proofPublicKey: string;
  readonly iterations?: number;
}

export type UnlockCredential =
  | { readonly kind: "password"; readonly password: string }
  | { readonly kind: "passkey"; readonly prfResult: Uint8Array }
  | { readonly kind: "recovery"; readonly recoverySecret: Uint8Array };

export interface OpenedMethod {
  signProof(challenge: Uint8Array): Promise<Uint8Array<ArrayBuffer>>;
  // The caller must discard this copy after the explicit controller handoff.
  copyUnlockSecret(): Uint8Array<ArrayBuffer>;
  dispose(): void;
}

export interface ExpectedMethod extends EnvelopeContext {
  readonly proofPublicKey: string;
}

const encoder = new TextEncoder();
const payloadLimit = 4096;
const iterations = 600_000;
const identifier = /^[A-Za-z0-9_-]{1,128}$/;

function checkedContext(value: EnvelopeContext): EnvelopeContext {
  if (!value || typeof value.instanceId !== "string" || typeof value.methodId !== "string" ||
      !identifier.test(value.instanceId) || !identifier.test(value.methodId) ||
      !Number.isInteger(value.revision) || value.revision < 1 || value.revision > 0xffff_ffff ||
      !["passkey", "password", "recovery"].includes(value.kind)) throw new Error("Invalid envelope context.");
  return { instanceId: value.instanceId, methodId: value.methodId, revision: value.revision, kind: value.kind };
}

export function envelopeAdditionalData(context: EnvelopeContext): Uint8Array<ArrayBuffer> {
  const checked = checkedContext(context);
  return encoder.encode(JSON.stringify([
    "deck.envelope", 1, checked.instanceId, checked.methodId, checked.revision, checked.kind,
  ]));
}

function passwordBytes(password: string): Uint8Array<ArrayBuffer> {
  if (typeof password !== "string") throw new Error("Invalid encryption password.");
  for (const character of password) {
    const point = character.codePointAt(0)!;
    if (point >= 0xd800 && point <= 0xdfff) throw new Error("Invalid encryption password encoding.");
  }
  const bytes = encoder.encode(password.normalize("NFC"));
  if (bytes.length < 1 || bytes.length > 1024) {
    bytes.fill(0);
    throw new Error("Encryption password must contain at most 1024 UTF-8 bytes.");
  }
  return bytes;
}

export function validateNewEncryptionPassword(password: string): void {
  const bytes = passwordBytes(password);
  bytes.fill(0);
  if (Array.from(password.normalize("NFC")).length < 15) throw new Error("Use at least 15 characters for the encryption password.");
}

function credentialBytes(credential: UnlockCredential, kind: MethodKind): Uint8Array<ArrayBuffer> {
  if (!credential || credential.kind !== kind) throw new Error("Incorrect unlock method type.");
  switch (credential.kind) {
    case "password": return passwordBytes(credential.password);
    case "passkey": return copyBytes(credential.prfResult, 32);
    case "recovery": return copyBytes(credential.recoverySecret, 32);
  }
}

async function wrappingKey(kind: MethodKind, input: Uint8Array<ArrayBuffer>, salt: Uint8Array<ArrayBuffer>,
  aad: Uint8Array<ArrayBuffer>): Promise<CryptoKey> {
  const algorithm = kind === "password" ? "PBKDF2" : "HKDF";
  const material = await crypto.subtle.importKey("raw", input, algorithm, false, ["deriveKey"]);
  const parameters = kind === "password"
    ? { name: "PBKDF2", hash: "SHA-256", salt, iterations }
    : { name: "HKDF", hash: "SHA-256", salt, info: aad };
  return crypto.subtle.deriveKey(parameters, material, { name: "AES-GCM", length: 256 }, false, ["encrypt", "decrypt"]);
}

export function generateWorkspaceSecret(): Uint8Array<ArrayBuffer> {
  return crypto.getRandomValues(new Uint8Array(32));
}

export function generateMethodSalt(): Uint8Array<ArrayBuffer> {
  return crypto.getRandomValues(new Uint8Array(32));
}

export async function workspaceCommitment(secret: Uint8Array): Promise<string> {
  const bytes = copyBytes(secret, 32);
  const prefix = encoder.encode("deck.volume.v1\0");
  const input = new Uint8Array(prefix.length + bytes.length);
  input.set(prefix);
  input.set(bytes, prefix.length);
  try { return encodeBase64url(new Uint8Array(await crypto.subtle.digest("SHA-256", input))); }
  finally { bytes.fill(0); input.fill(0); }
}

export async function createMethodEnvelope(context: EnvelopeContext, workspaceSecret: Uint8Array,
  credential: UnlockCredential, methodSalt: Uint8Array): Promise<MethodEnvelope> {
  const checked = checkedContext(context);
  const aad = envelopeAdditionalData(checked);
  const salt = copyBytes(methodSalt, 32);
  if (credential.kind === "password") validateNewEncryptionPassword(credential.password);
  const secret = copyBytes(workspaceSecret, 32);
  let input: Uint8Array<ArrayBuffer> | undefined;
  let privateBytes: Uint8Array<ArrayBuffer> | undefined;
  let plaintext: Uint8Array<ArrayBuffer> | undefined;
  try {
    input = credentialBytes(credential, checked.kind);
    const key = await wrappingKey(checked.kind, input, salt, aad);
    const proof = await crypto.subtle.generateKey({ name: "ECDSA", namedCurve: "P-256" }, true, ["sign", "verify"]);
    privateBytes = new Uint8Array(await crypto.subtle.exportKey("pkcs8", proof.privateKey));
    plaintext = encoder.encode(JSON.stringify([1, encodeBase64url(secret), encodeBase64url(privateBytes)]));
    if (plaintext.length > payloadLimit - 16) throw new Error("Invalid envelope payload length.");
    const nonce = crypto.getRandomValues(new Uint8Array(12));
    const ciphertext = await crypto.subtle.encrypt({ name: "AES-GCM", iv: nonce, additionalData: aad, tagLength: 128 }, key, plaintext);
    return Object.freeze({
      version: 1, ...checked, salt: encodeBase64url(salt), nonce: encodeBase64url(nonce),
      ciphertext: encodeBase64url(new Uint8Array(ciphertext)),
      proofPublicKey: encodeBase64url(new Uint8Array(await crypto.subtle.exportKey("spki", proof.publicKey))),
      ...(checked.kind === "password" ? { iterations } : {}),
    });
  } finally {
    input?.fill(0);
    secret.fill(0);
    privateBytes?.fill(0);
    plaintext?.fill(0);
  }
}

function readEnvelope(value: unknown): MethodEnvelope {
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error("Invalid method envelope.");
  const row = value as Record<string, unknown>;
  const fields = ["version", "instanceId", "methodId", "revision", "kind", "salt", "nonce", "ciphertext", "proofPublicKey"];
  if (row.kind === "password") fields.push("iterations");
  if (Object.keys(row).length !== fields.length || fields.some(field => !Object.hasOwn(row, field)) ||
      row.version !== 1 || (row.kind === "password" && row.iterations !== iterations)) {
    throw new Error("Unsupported method envelope format.");
  }
  const context = checkedContext(row as unknown as EnvelopeContext);
  for (const field of ["salt", "nonce", "ciphertext", "proofPublicKey"]) {
    if (typeof row[field] !== "string") throw new Error("Invalid method envelope.");
  }
  return { version: 1, ...context, salt: row.salt as string, nonce: row.nonce as string,
    ciphertext: row.ciphertext as string, proofPublicKey: row.proofPublicKey as string,
    ...(context.kind === "password" ? { iterations } : {}) };
}

class MethodKeys implements OpenedMethod {
  #secret: Uint8Array<ArrayBuffer>;
  #proof: CryptoKey | null;

  constructor(secret: Uint8Array<ArrayBuffer>, proof: CryptoKey) {
    this.#secret = secret;
    this.#proof = proof;
  }

  async signProof(challenge: Uint8Array): Promise<Uint8Array<ArrayBuffer>> {
    if (!this.#proof) throw new Error("Unlock method has been disposed.");
    const message = copyBytes(challenge, 32);
    return new Uint8Array(await crypto.subtle.sign({ name: "ECDSA", hash: "SHA-256" }, this.#proof, message));
  }

  copyUnlockSecret(): Uint8Array<ArrayBuffer> {
    if (!this.#proof) throw new Error("Unlock method has been disposed.");
    return new Uint8Array(this.#secret);
  }

  dispose(): void {
    this.#secret.fill(0);
    this.#proof = null;
  }

  toJSON(): never { throw new Error("Decrypted unlock methods cannot be serialized."); }
}

export async function openMethodEnvelope(value: unknown, credential: UnlockCredential,
  expectedContext: ExpectedMethod, expectedCommitment: string): Promise<OpenedMethod> {
  const envelope = readEnvelope(value);
  const aad = envelopeAdditionalData(envelope);
  if (!equalBytes(aad, envelopeAdditionalData(expectedContext))) throw new Error("Envelope belongs to a different method.");
  const commitment = decodeBase64url(expectedCommitment, 32, 32);
  const salt = decodeBase64url(envelope.salt, 32, 32);
  const nonce = decodeBase64url(envelope.nonce, 12, 12);
  const ciphertext = decodeBase64url(envelope.ciphertext, payloadLimit);
  if (ciphertext.length <= 16) throw new Error("Invalid envelope payload length.");
  const publicBytes = decodeBase64url(envelope.proofPublicKey, 1024);
  if (!equalBytes(publicBytes, decodeBase64url(expectedContext.proofPublicKey, 1024))) throw new Error("Unexpected method proof key.");
  const input = credentialBytes(credential, envelope.kind);
  let plaintext: Uint8Array<ArrayBuffer> | undefined;
  let secret: Uint8Array<ArrayBuffer> | undefined;
  let privateBytes: Uint8Array<ArrayBuffer> | undefined;
  try {
    const key = await wrappingKey(envelope.kind, input, salt, aad);
    plaintext = new Uint8Array(await crypto.subtle.decrypt({ name: "AES-GCM", iv: nonce, additionalData: aad, tagLength: 128 }, key, ciphertext));
    const payload: unknown = JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(plaintext));
    if (!Array.isArray(payload) || payload.length !== 3 || payload[0] !== 1) throw new Error("Invalid envelope payload.");
    secret = decodeBase64url(payload[1], 32, 32);
    privateBytes = decodeBase64url(payload[2], 2048);
    if (!equalBytes(decodeBase64url(await workspaceCommitment(secret), 32, 32), commitment)) {
      throw new Error("Envelope belongs to a different workspace key.");
    }
    const proof = await crypto.subtle.importKey("pkcs8", privateBytes, { name: "ECDSA", namedCurve: "P-256" }, false, ["sign"]);
    const publicKey = await crypto.subtle.importKey("spki", publicBytes, { name: "ECDSA", namedCurve: "P-256" }, false, ["verify"]);
    // A swapped public verifier would otherwise make a tested method unusable at the gateway.
    const check = crypto.getRandomValues(new Uint8Array(32));
    const signature = await crypto.subtle.sign({ name: "ECDSA", hash: "SHA-256" }, proof, check);
    if (!await crypto.subtle.verify({ name: "ECDSA", hash: "SHA-256" }, publicKey, signature, check)) throw new Error("Mismatched proof key.");
    const opened = new MethodKeys(secret, proof);
    secret = undefined;
    return opened;
  } catch {
    throw new Error("The unlock method could not open this envelope.");
  } finally {
    input.fill(0);
    plaintext?.fill(0);
    secret?.fill(0);
    privateBytes?.fill(0);
  }
}
