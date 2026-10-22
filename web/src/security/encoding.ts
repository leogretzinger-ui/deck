export function encodeBase64url(bytes: Uint8Array): string {
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary).replaceAll("+", "-").replaceAll("/", "_").replace(/=+$/, "");
}

export function decodeBase64url(value: unknown, maxBytes: number, exactBytes?: number): Uint8Array<ArrayBuffer> {
  if (typeof value !== "string" || value.length > Math.ceil(maxBytes * 4 / 3) ||
      !/^[A-Za-z0-9_-]+$/.test(value)) throw new Error("Invalid base64url encoding.");
  let binary: string;
  try { binary = atob(value.replaceAll("-", "+").replaceAll("_", "/")); }
  catch { throw new Error("Invalid base64url encoding."); }
  const bytes = Uint8Array.from(binary, character => character.charCodeAt(0));
  if (bytes.length > maxBytes || (exactBytes !== undefined && bytes.length !== exactBytes) ||
      encodeBase64url(bytes) !== value) throw new Error("Invalid base64url encoding.");
  return bytes;
}

export function copyBytes(value: Uint8Array, length: number): Uint8Array<ArrayBuffer> {
  if (!(value instanceof Uint8Array) || value.length !== length) throw new Error("Invalid key material length.");
  return new Uint8Array(value);
}

export function equalBytes(left: Uint8Array, right: Uint8Array): boolean {
  if (left.length !== right.length) return false;
  let difference = 0;
  for (let index = 0; index < left.length; index++) difference |= left[index]! ^ right[index]!;
  return difference === 0;
}
