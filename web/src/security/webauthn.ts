import { encodeBase64url } from "./encoding.ts";

function responseBytes(value: unknown, maxBytes: number): Uint8Array<ArrayBuffer> {
  if (!(value instanceof ArrayBuffer) && !ArrayBuffer.isView(value)) throw new Error("Invalid passkey response.");
  if (value.byteLength === 0 || value.byteLength > maxBytes) throw new Error("Invalid passkey response length.");
  if (value instanceof ArrayBuffer) return new Uint8Array(value.slice(0));
  return new Uint8Array(new Uint8Array(value.buffer, value.byteOffset, value.byteLength));
}

type ExtensionResults = {
  prf?: { enabled?: boolean; results?: { first?: unknown } };
  credProps?: { rk?: boolean };
};

export function passkeyPRFResult(credential: PublicKeyCredential): Uint8Array<ArrayBuffer> {
  const outputs = credential.getClientExtensionResults() as ExtensionResults;
  const value = outputs.prf?.results?.first;
  if (value === undefined) throw new Error("This passkey cannot unlock the workspace here.");
  const bytes = responseBytes(value, 32);
  if (bytes.length !== 32) throw new Error("Invalid passkey encryption result.");
  return bytes;
}

export interface PublicCredentialJSON {
  id: string;
  rawId: string;
  type: "public-key";
  authenticatorAttachment?: string;
  response: {
    clientDataJSON: string;
    attestationObject?: string;
    transports?: string[];
    authenticatorData?: string;
    signature?: string;
    userHandle?: string | null;
  };
  clientExtensionResults: { prf?: { enabled: boolean }; credProps?: { rk: boolean } };
}

export function serializePublicCredential(credential: PublicKeyCredential): PublicCredentialJSON {
  if (credential.type !== "public-key") throw new Error("Invalid passkey credential type.");
  const rawId = responseBytes(credential.rawId, 1024);
  const id = encodeBase64url(rawId);
  if (credential.id !== id) throw new Error("Invalid passkey credential identifier.");
  const response = credential.response;
  const publicResponse: PublicCredentialJSON["response"] = {
    clientDataJSON: encodeBase64url(responseBytes(response.clientDataJSON, 16_384)),
  };
  if ("attestationObject" in response) {
    const registration = response as AuthenticatorAttestationResponse;
    publicResponse.attestationObject = encodeBase64url(responseBytes(registration.attestationObject, 65_536));
    if (typeof registration.getTransports === "function") {
      const transports = registration.getTransports();
      if (!Array.isArray(transports) || transports.length > 16 ||
          transports.some(transport => typeof transport !== "string" || transport.length > 32)) {
        throw new Error("Invalid passkey transports.");
      }
      publicResponse.transports = [...transports];
    }
  } else {
    const assertion = response as AuthenticatorAssertionResponse;
    publicResponse.authenticatorData = encodeBase64url(responseBytes(assertion.authenticatorData, 4096));
    publicResponse.signature = encodeBase64url(responseBytes(assertion.signature, 4096));
    publicResponse.userHandle = assertion.userHandle === null ? null
      : encodeBase64url(responseBytes(assertion.userHandle, 64));
  }
  // toJSON includes PRF results; copying arbitrary extensions can disclose the wrapping key.
  const extensions = credential.getClientExtensionResults() as ExtensionResults;
  const clientExtensionResults: PublicCredentialJSON["clientExtensionResults"] = {};
  if (typeof extensions.prf?.enabled === "boolean") clientExtensionResults.prf = { enabled: extensions.prf.enabled };
  if (typeof extensions.credProps?.rk === "boolean") clientExtensionResults.credProps = { rk: extensions.credProps.rk };
  const result: PublicCredentialJSON = { id, rawId: id, type: "public-key", response: publicResponse, clientExtensionResults };
  if (credential.authenticatorAttachment !== null && credential.authenticatorAttachment !== undefined) {
    if (!["platform", "cross-platform"].includes(credential.authenticatorAttachment)) throw new Error("Invalid passkey attachment.");
    result.authenticatorAttachment = credential.authenticatorAttachment;
  }
  return result;
}
