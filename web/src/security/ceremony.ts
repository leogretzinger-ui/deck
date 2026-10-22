import { decodeBase64url, encodeBase64url } from "./encoding.ts";
import { createMethodEnvelope, openMethodEnvelope, type OpenedMethod, type UnlockCredential } from "./envelope.ts";
import { passkeyPRFResult, serializePublicCredential, type PublicCredentialJSON } from "./webauthn.ts";
import { SecurityClient, type PreparedMethod, type SecurityAction, type SecurityChallenge, type SecurityMethod } from "./client.ts";

export class IncompatiblePasskey extends Error {
  constructor() { super("This passkey cannot unlock the workspace in this browser."); }
}
export class MethodUnlockError extends Error {
  constructor() { super("The unlock method could not open its saved envelope."); }
}

function publicOptions(value: Record<string, unknown>): Record<string, unknown> {
  const result = structuredClone(value);
  result.challenge = decodeBase64url(result.challenge as string, 1024);
  for (const key of ["allowCredentials", "excludeCredentials"]) {
    if (Array.isArray(result[key])) result[key] = result[key].map((entry: Record<string, unknown>) => ({ ...entry, id: decodeBase64url(entry.id as string, 1024) }));
  }
  if (result.user && typeof result.user === "object") {
    const user = result.user as Record<string, unknown>;
    user.id = decodeBase64url(user.id as string, 64);
  }
  const extensions = result.extensions as { prf?: { eval?: { first: string; second?: string } } } | undefined;
  if (extensions?.prf?.eval) {
    const values = extensions.prf.eval;
    result.extensions = { ...extensions, prf: { eval: { first: decodeBase64url(values.first, 32, 32),
      ...(values.second ? { second: decodeBase64url(values.second, 32, 32) } : {}) } } };
  }
  return result;
}

function requireCredential(value: Credential | null): PublicKeyCredential {
  if (!value || value.type !== "public-key") throw new IncompatiblePasskey();
  return value as PublicKeyCredential;
}

export async function registerPasskey(prepared: PreparedMethod, signal: AbortSignal): Promise<{
  credential: UnlockCredential & { kind: "passkey" }; registration: PublicCredentialJSON;
}> {
  if (!navigator.credentials || !prepared.registration) throw new IncompatiblePasskey();
  const options = publicOptions(prepared.registration.publicKey) as unknown as PublicKeyCredentialCreationOptions;
  const salt = decodeBase64url(prepared.method.salt, 32, 32);
  options.extensions = { ...options.extensions, prf: { eval: { first: salt } } } as AuthenticationExtensionsClientInputs;
  const created = requireCredential(await navigator.credentials.create({ publicKey: options, signal }));
  signal.throwIfAborted();
  const registration = serializePublicCredential(created);
  if (!registration.clientExtensionResults.prf?.enabled) throw new IncompatiblePasskey();
  let prfResult: Uint8Array<ArrayBuffer>;
  try { prfResult = passkeyPRFResult(created); }
  catch {
    // Some authenticators enable PRF during creation but evaluate it only on get.
    // This local read supplies wrapping material; the server challenge below
    // separately verifies the persisted envelope and the public assertion.
    const read = requireCredential(await navigator.credentials.get({ signal, publicKey: {
      challenge: crypto.getRandomValues(new Uint8Array(32)), rpId: options.rp.id,
      allowCredentials: [{ type: "public-key", id: created.rawId }], userVerification: "required",
      extensions: { prf: { eval: { first: salt } } } as AuthenticationExtensionsClientInputs,
    } }));
    signal.throwIfAborted();
    try { prfResult = passkeyPRFResult(read); } catch { throw new IncompatiblePasskey(); }
  }
  return { credential: { kind: "passkey", prfResult }, registration };
}

export async function readPasskey(challenge: SecurityChallenge, signal: AbortSignal): Promise<{
  credential: UnlockCredential & { kind: "passkey" }; assertion: PublicCredentialJSON;
}> {
  if (!navigator.credentials || !challenge.assertion) throw new IncompatiblePasskey();
  const assertion = requireCredential(await navigator.credentials.get({ signal,
    publicKey: publicOptions(challenge.assertion.publicKey) as unknown as PublicKeyCredentialRequestOptions }));
  signal.throwIfAborted();
  let prfResult: Uint8Array<ArrayBuffer>;
  try { prfResult = passkeyPRFResult(assertion); } catch { throw new IncompatiblePasskey(); }
  return { credential: { kind: "passkey", prfResult }, assertion: serializePublicCredential(assertion) };
}

export function clearCredential(credential: UnlockCredential | undefined): void {
  if (credential?.kind === "passkey") credential.prfResult.fill(0);
  if (credential?.kind === "recovery") credential.recoverySecret.fill(0);
}

export async function saveCandidate(api: SecurityClient, instanceId: string, prepared: PreparedMethod,
  secret: Uint8Array, commitment: string, credential: UnlockCredential, registration?: PublicCredentialJSON, signal?: AbortSignal): Promise<SecurityMethod> {
  signal?.throwIfAborted();
  const method = prepared.method;
  const envelope = await createMethodEnvelope({ instanceId, methodId: method.id, revision: method.revision, kind: method.kind },
    secret, credential, decodeBase64url(method.salt, 32, 32));
  signal?.throwIfAborted();
  await api.store(method, envelope, commitment, registration, signal);
  signal?.throwIfAborted();
  // The upload buffer is not evidence that the stored method is usable.
  // The next proof always fetches the saved bytes through the API.
  const persisted = await api.envelope(method, signal);
  signal?.throwIfAborted();
  if (!persisted || typeof persisted !== "object" || Array.isArray(persisted) ||
      Object.keys(persisted).length !== Object.keys(envelope).length ||
      Object.entries(envelope).some(([key, value]) => (persisted as Record<string, unknown>)[key] !== value)) {
    throw new Error("The saved unlock method does not match the method that was uploaded.");
  }
  return { ...method, envelope: persisted as typeof envelope };
}

export interface PreparedProof {
  challenge: SecurityChallenge;
  opened: OpenedMethod;
  signature: string;
  assertion?: PublicCredentialJSON;
}

export async function prepareProof(api: SecurityClient, instanceId: string, commitment: string,
  method: SecurityMethod, action: SecurityAction, credential: UnlockCredential | undefined, signal: AbortSignal): Promise<PreparedProof> {
  signal.throwIfAborted();
  const challenge = await api.challenge(action, signal);
  signal.throwIfAborted();
  let passkey: Awaited<ReturnType<typeof readPasskey>> | undefined;
  let opened: OpenedMethod | undefined;
  const clear = (): void => { clearCredential(passkey?.credential); opened?.dispose(); };
  signal.addEventListener("abort", clear, { once: true });
  try {
    if (method.kind === "passkey") passkey = await readPasskey(challenge, signal);
    signal.throwIfAborted();
    const input = passkey?.credential ?? credential;
    if (!input) throw new Error("Enter an unlock method to continue.");
    const envelope = await api.envelope(method, signal);
    signal.throwIfAborted();
    if (!method.envelope) throw new Error("The unlock method has no saved envelope.");
    try {
      opened = await openMethodEnvelope(envelope, input, { instanceId, methodId: method.id,
        revision: method.revision, kind: method.kind, proofPublicKey: method.envelope.proofPublicKey }, commitment);
    } catch { signal.throwIfAborted(); throw new MethodUnlockError(); }
    signal.throwIfAborted();
    const signature = encodeBase64url(await opened.signProof(decodeBase64url(challenge.challenge, 32, 32)));
    signal.throwIfAborted();
    return { challenge, opened, signature, ...(passkey ? { assertion: passkey.assertion } : {}) };
  } catch (error) { opened?.dispose(); throw error; }
  finally { signal.removeEventListener("abort", clear); clearCredential(passkey?.credential); }
}
