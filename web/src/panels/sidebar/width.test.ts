import assert from "node:assert/strict";
import test from "node:test";
import { parseSidebarWidth, sidebarWidthBounds } from "./width.ts";

test("sidebar preference rejects corrupt, nonfinite, and out-of-range storage", () => {
  for (const value of [null, "", " ", "NaN", "Infinity", "oops", "0", "219", "481", "-272"]) {
    assert.equal(parseSidebarWidth(value), 272, String(value));
  }
  assert.equal(parseSidebarWidth("352"), 352);
  assert.equal(parseSidebarWidth("350.7"), 351);
});
test("desktop bounds reserve workspace space and cap excessive sidebar widths", () => {
  assert.deepEqual(sidebarWidthBounds(1400), { min: 220, max: 480 });
  assert.deepEqual(sidebarWidthBounds(800), { min: 220, max: 320 });
  assert.deepEqual(sidebarWidthBounds(768), { min: 220, max: 288 });
  assert.deepEqual(sidebarWidthBounds(390), { min: 220, max: 220 });
});
