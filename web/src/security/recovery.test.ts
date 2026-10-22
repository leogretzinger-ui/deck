import { test } from "node:test";
import assert from "node:assert/strict";
import { formatRecoveryKey, generateRecoverySecret, parseRecoveryKey } from "./recovery.ts";
import { decodeBase64url, encodeBase64url } from "./encoding.ts";

// Public protocol fixture, not an operational recovery key.
const secret = Uint8Array.from({ length: 32 }, (_, index) => index);
const display = "DK1-000G-40R4-0M30-E209-185G-R38E-1W81-24GK-2GAH-C5RR-34D1-P70X-3RFG-9206";

test("recovery encoding matches the independently calculated protocol fixture", async () => {
  assert.equal(await formatRecoveryKey(secret), display);
  assert.deepEqual(await parseRecoveryKey(display), secret);
});

test("recovery parsing accepts ASCII formatting and Crockford transcription aliases", async () => {
  for (const text of [display.toLowerCase(), display.replaceAll("-", ""),
    display.replaceAll("-", " \n\t"), display.replaceAll("0", "O"),
    display.replaceAll("1", "I").replace("DKI", "DK1"),
    display.replaceAll("1", "l").replace("DKl", "DK1")]) {
    assert.deepEqual(await parseRecoveryKey(text), secret);
  }
});

test("bad recovery checks, versions, alphabet, lengths and padding bits fail", async () => {
  for (const text of [display.replace("DK1", "DK2"), display.slice(1), display.slice(0, -1),
    display + "0", display.replace("9206", "9207"), display.replace("3RFG", "3RFH"),
    display.replace("000G", "000U"), display.replace("000G", "000="),
    display.replace("000G", "000Ｇ"), display.replace("-", "\u00a0"), " ".repeat(513) + display]) {
    await assert.rejects(parseRecoveryKey(text));
  }
  await assert.rejects(formatRecoveryKey(new Uint8Array(31)));
});

test("fresh recovery secrets preserve all 32 bytes across formatting", async () => {
  const first = generateRecoverySecret();
  const second = generateRecoverySecret();
  assert.equal(first.length, 32);
  assert.notDeepEqual(first, second);
  for (const bytes of [first, second, new Uint8Array(32), new Uint8Array(32).fill(255)]) {
    assert.deepEqual(await parseRecoveryKey(await formatRecoveryKey(bytes)), bytes);
  }
});

test("base64url rejects ambiguous, padded and oversized representations", () => {
  assert.deepEqual(decodeBase64url(encodeBase64url(secret), 32, 32), secret);
  assert.deepEqual(decodeBase64url("_w", 1, 1), new Uint8Array([255]));
  for (const text of ["", "_x", "/w", "_w==", "_w\n", "====", "A", "A".repeat(44)]) {
    assert.throws(() => decodeBase64url(text, 1, 1));
  }
  assert.throws(() => decodeBase64url("AA", 32, 32));
});
