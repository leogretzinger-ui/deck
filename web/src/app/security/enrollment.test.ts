import assert from "node:assert/strict";
import test from "node:test";
import { enrollmentHtml } from "./enrollment.ts";
import { parseSecurityPreview } from "../../preview/security-states.ts";

test("workspace metadata and method labels cannot create markup", () => {
  const html = enrollmentHtml({
    workspace: { name: '<img src=x onerror="alert(1)">', address: "evil.example<svg/onload=alert(1)>", operator: "<script>bad()</script>" },
    screen: { kind: "method-ready", methodKind: "passkey", methodName: '\" autofocus onfocus=\"alert(1)' },
    error: '<script>alert("error")</script>',
  });
  assert.doesNotMatch(html, /<script>|<img src=x|value="" autofocus/i);
  assert.match(html, /&lt;script&gt;/);
  assert.match(html, /value="&quot; autofocus/);
});

test("an envelope check makes no storage-open or enrollment-complete claim", () => {
  for (const methodKind of ["passkey", "password"] as const) {
    const html = enrollmentHtml({
      workspace: { name: "workspace", address: "workspace.example", operator: "operator" },
      screen: { kind: "method-ready", methodKind, methodName: "My method" },
    });
    assert.match(html, /passed its unlock check/);
    assert.doesNotMatch(html, /unlocked workspace|opened the workspace|workspace is ready/);
  }
});

test("unknown and combined security fixture routes fail before workspace construction", () => {
  assert.throws(() => parseSecurityPreview(new URLSearchParams("security=missing")), /invalid security/);
  assert.throws(() => parseSecurityPreview(new URLSearchParams("security=1a&panel=files")), /cannot be combined/);
  assert.equal(parseSecurityPreview(new URLSearchParams("security=3m")), "3m");
  assert.equal(parseSecurityPreview(new URLSearchParams("panel=files")), null);
});
