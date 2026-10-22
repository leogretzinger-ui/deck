import { test } from "node:test";
import assert from "node:assert/strict";
import { isSafeUrl } from "./url.ts";

test("http and https links open", () => {
  assert.equal(isSafeUrl("http://example.com"), true);
  assert.equal(isSafeUrl("https://example.com/path?q=1#frag"), true);
  assert.equal(isSafeUrl("https://user:pass@example.com:8443/x"), true);
});

test("every other scheme is refused", () => {
  assert.equal(isSafeUrl("javascript:alert(1)"), false);
  assert.equal(isSafeUrl("data:text/html,<script>alert(1)</script>"), false);
  assert.equal(isSafeUrl("file:///etc/passwd"), false);
  assert.equal(isSafeUrl("vbscript:msgbox(1)"), false);
  assert.equal(isSafeUrl("ftp://example.com"), false);
});

test("unparseable strings are refused", () => {
  assert.equal(isSafeUrl(""), false);
  assert.equal(isSafeUrl("not a url"), false);
  assert.equal(isSafeUrl("https//example.com"), false);
});

test("scheme tricks fail closed", () => {
  // URL parsers lowercase the scheme; anything else unusual is refused
  // on parse or scheme mismatch.
  assert.equal(isSafeUrl("JAVAscript:alert(1)"), false);
  assert.equal(isSafeUrl("jav\tascript:alert(1)"), false);
});
