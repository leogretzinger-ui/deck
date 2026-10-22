// Tests for the shared value formatters.
import assert from "node:assert/strict";
import test from "node:test";

import { formatFileSize, formatSince } from "./format.ts";

test("formatFileSize renders a byte count compactly", () => {
  assert.equal(formatFileSize(0), "0 B");
  assert.equal(formatFileSize(512), "0.5 KB");
  assert.equal(formatFileSize(999), "1.0 KB");
  assert.equal(formatFileSize(1000), "1.0 KB");
  assert.equal(formatFileSize(1536), "1.5 KB");
  assert.equal(formatFileSize(1000000), "1.0 MB");
  assert.equal(formatFileSize(999999), "1000.0 KB");
  assert.equal(formatFileSize(1572864), "1.6 MB");
});

test("formatSince renders the design's compact units and rejects bad input", () => {
  const now = Date.parse("2026-08-02T12:00:00.000Z");
  assert.equal(formatSince("2026-08-02T11:59:18.000Z", now), "< 1m");
  assert.equal(formatSince("2026-08-02T11:56:00.000Z", now), "4m");
  assert.equal(formatSince("2026-08-02T10:00:00.000Z", now), "2h");
  assert.equal(formatSince("2026-07-28T12:00:00.000Z", now), "5d");
  // A timestamp from the future clamps rather than counting up.
  assert.equal(formatSince("2026-08-02T12:00:30.000Z", now), "< 1m");
  assert.equal(formatSince("t", now), null);
  assert.equal(formatSince("", now), null);
  assert.equal(formatSince(undefined, now), null);
});
