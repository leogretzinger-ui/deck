import { copyBytes, equalBytes } from "./encoding.ts";

const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ";
const checksumPrefix = new TextEncoder().encode("deck.recovery.DK1\0");

function base32(bytes: Uint8Array): string {
  let accumulator = 0;
  let bits = 0;
  let result = "";
  for (const byte of bytes) {
    accumulator = (accumulator << 8) | byte;
    bits += 8;
    while (bits >= 5) {
      bits -= 5;
      result += alphabet[(accumulator >>> bits) & 31];
    }
    accumulator &= (1 << bits) - 1;
  }
  if (bits) result += alphabet[(accumulator << (5 - bits)) & 31];
  return result;
}

async function checksum(secret: Uint8Array): Promise<string> {
  const input = new Uint8Array(checksumPrefix.length + secret.length);
  input.set(checksumPrefix);
  input.set(secret, checksumPrefix.length);
  try { return base32(new Uint8Array(await crypto.subtle.digest("SHA-256", input))).slice(0, 4); }
  finally { input.fill(0); }
}

export function generateRecoverySecret(): Uint8Array<ArrayBuffer> {
  return crypto.getRandomValues(new Uint8Array(32));
}

export async function formatRecoveryKey(secret: Uint8Array): Promise<string> {
  const bytes = copyBytes(secret, 32);
  try {
    const body = base32(bytes);
    return `DK1-${body.match(/.{4}/g)!.join("-")}-${await checksum(bytes)}`;
  } finally { bytes.fill(0); }
}

export async function parseRecoveryKey(value: string): Promise<Uint8Array<ArrayBuffer>> {
  if (typeof value !== "string" || value.length > 512 || /[^\x00-\x7f]/.test(value)) throw new Error("Invalid recovery key.");
  const compact = value.replace(/[\t\n\v\f\r -]/g, "").toUpperCase();
  if (!compact.startsWith("DK1")) throw new Error("Unsupported recovery key version.");
  const suffix = compact.slice(3).replaceAll("O", "0").replace(/[IL]/g, "1");
  if (suffix.length !== 56 || !/^[0-9A-HJKMNP-TV-Z]+$/.test(suffix)) throw new Error("Invalid recovery key.");
  const data = suffix.slice(0, 52);
  const secret = new Uint8Array(32);
  let accumulator = 0;
  let bits = 0;
  let offset = 0;
  for (const character of data) {
    accumulator = (accumulator << 5) | alphabet.indexOf(character);
    bits += 5;
    if (bits >= 8) {
      bits -= 8;
      secret[offset++] = (accumulator >>> bits) & 255;
    }
    accumulator &= (1 << bits) - 1;
  }
  try {
    // Nonzero unused bits would give several written keys the same secret.
    if (base32(secret) !== data || !equalBytes(new TextEncoder().encode(await checksum(secret)),
      new TextEncoder().encode(suffix.slice(52)))) throw new Error("Recovery key check failed.");
    return secret;
  } catch (error) {
    secret.fill(0);
    throw error;
  }
}
