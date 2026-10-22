import assert from "node:assert/strict";
import test from "node:test";
import { passwordChangeHtml, setupFailure } from "./password-form.ts";

test("changing your password requires the current password and confirms the new one", () => {
  const html = passwordChangeHtml();
  assert.match(html, /aria-label="Change password"/);
  assert.equal((html.match(/role="dialog"/g) ?? []).length, 1);
  assert.match(html, /name="current_password"/);
  assert.match(html, /autocomplete="current-password"/);
  assert.match(html, /name="confirm_password"/);
  assert.equal((html.match(/type="password"/g) ?? []).length, 3);
  for (const button of html.matchAll(/<button\b[^>]*>/g)) assert.match(button[0], /data-hint=/);
});

test("new password fields show the length rule before you submit", () => {
  const html = passwordChangeHtml();
  const fields = [...html.matchAll(/<input\b[^>]*>/g)].map((m) => m[0]);
  for (const field of fields) {
    const current = field.includes('name="current_password"');
    assert.equal(field.includes('minlength="8"'), !current, field);
  }
  assert.match(html, /data-hint="Enter your new password\. Use at least 8 characters\."/);
  assert.match(html, /<p id="password-rule"[^>]*>Use at least 8 characters\.<\/p>/);
  assert.match(html, /name="password"[^>]*aria-describedby="password-rule"/);
});

test("a refused short password reads as the length rule", () => {
  assert.equal(setupFailure(400, null), "Use at least 8 characters for your password.");
});
