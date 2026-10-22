import { test } from "node:test";
import assert from "node:assert/strict";
import {
  bytesToBase64,
  base64ToBytes,
  encodeFrame,
  decodeFrame,
  FRAME,
} from "./frames.ts";

test("base64 round-trips arbitrary bytes", () => {
  const data = new Uint8Array(256);
  for (let i = 0; i < 256; i++) {
    data[i] = i;
  }
  assert.deepEqual(base64ToBytes(bytesToBase64(data)), data);
});

test("base64 round-trips large payloads in chunks", () => {
  const data = new Uint8Array(70000);
  for (let i = 0; i < data.length; i++) {
    data[i] = i % 256;
  }
  assert.deepEqual(base64ToBytes(bytesToBase64(data)), data);
});

test("base64 round-trips escape sequences", () => {
  const data = new TextEncoder().encode("\x1b[?1;2c\x1b[200~pasted\x1b[201~");
  assert.deepEqual(base64ToBytes(bytesToBase64(data)), data);
});

test("encodeFrame serializes and decodeFrame accepts server frames", () => {
  const raw = encodeFrame({ type: FRAME.input, token: 42, data: "bHM=" });
  assert.deepEqual(JSON.parse(raw), { type: "input", token: 42, data: "bHM=" });

  assert.deepEqual(decodeFrame(`{"type":"attached","token":7}`), {
    type: "attached",
    token: 7,
  });
  assert.deepEqual(decodeFrame(`{"type":"output","data":"aGVsbG8="}`), {
    type: "output",
    data: "aGVsbG8=",
  });
  assert.deepEqual(decodeFrame(`{"type":"scroll_status","active":false}`), {
    type: "scroll_status",
    active: false,
  });
  assert.deepEqual(decodeFrame(`{"type":"error","code":"fenced"}`), {
    type: "error",
    code: "fenced",
  });
});

test("decodeFrame refuses malformed frames", () => {
  assert.throws(() => decodeFrame("not json"), /not JSON/);
  assert.throws(() => decodeFrame("[1,2]"), /not an object/);
  assert.throws(() => decodeFrame(`{"type":"input"}`), /unknown type/);
  assert.throws(() => decodeFrame(`{"type":"mystery"}`), /unknown type/);
  assert.throws(() => decodeFrame(`{}`), /unknown type/);
});
