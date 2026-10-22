import assert from "node:assert/strict";
import test from "node:test";
import { isAppleClient, isWindowsClient } from "./platform.ts";

test("client platform honors hints and falls back to the user agent", () => {
  for (const platform of ["Windows", "Linux", "Chrome OS", "Android"]) {
    assert.equal(isAppleClient({ userAgentData: { platform }, userAgent: "Macintosh" }), false);
  }
  for (const platform of ["macOS", "iPhone", "iPad", "iPod"]) {
    assert.equal(isAppleClient({ userAgentData: { platform }, userAgent: "" }), true);
    assert.equal(isAppleClient({ userAgentData: { platform: "" }, userAgent: platform }), true);
    assert.equal(isAppleClient({ userAgent: platform }), true);
  }
  assert.equal(isAppleClient({ userAgent: "Windows NT 10.0" }), false);
  assert.equal(isAppleClient({ userAgent: "" }), false);
});


test("Windows clipboard behavior is not enabled on other clients", () => {
  assert.equal(isWindowsClient({ userAgentData: { platform: "Windows" }, userAgent: "" }), true);
  assert.equal(isWindowsClient({ userAgentData: { platform: "" }, userAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64)" }), true);
  for (const platform of ["Linux", "Android", "macOS", "Chrome OS", "iPhone", "iPad"]) {
    assert.equal(isWindowsClient({ userAgentData: { platform }, userAgent: "Windows NT 10.0" }), false);
    assert.equal(isWindowsClient({ userAgent: platform }), false);
  }
  assert.equal(isWindowsClient({ userAgent: "" }), false);
});
