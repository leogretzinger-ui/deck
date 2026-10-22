import { test } from "node:test";
import assert from "node:assert/strict";
import { mobileComposeMarkup } from "./mobile-compose-markup.ts";

function countOccurrences(haystack: string, needle: string): number {
  if (needle === "") return 0;
  return haystack.split(needle).length - 1;
}

test("the markup is pure: two calls return the same string", () => {
  assert.equal(mobileComposeMarkup(), mobileComposeMarkup());
});

test("there is exactly one form carrying the compose data hook", () => {
  const markup = mobileComposeMarkup();
  assert.equal(countOccurrences(markup, "<form"), 1);
  assert.ok(/<form\b[^>]*\bdata-mobile-compose-form\b/.test(markup));
});

test("there is exactly one textarea with the compose input hook and rows=1", () => {
  const markup = mobileComposeMarkup();
  assert.equal(countOccurrences(markup, "<textarea"), 1);
  assert.ok(/<textarea\b[^>]*\bdata-mobile-compose-input\b/.test(markup));
  assert.ok(/<textarea\b[^>]*\brows="1"/.test(markup));
});

test("the textarea accessible name is 'Terminal input' and it carries no placeholder", () => {
  const markup = mobileComposeMarkup();
  assert.ok(/<textarea\b[^>]*\baria-label="Terminal input"/.test(markup));
  assert.ok(/<textarea\b[^>]*\bplaceholder=""/.test(markup));
});

test("the compose row renders a prompt prefix before the field", () => {
  const markup = mobileComposeMarkup();
  const prompt = /<span data-mobile-compose-prompt[^>]*aria-hidden="true"[^>]*>›<\/span>/;
  assert.ok(prompt.test(markup), "the prompt prefix is missing");
  const promptIndex = markup.search(prompt);
  const textareaIndex = markup.search(/<textarea\b/);
  assert.ok(promptIndex !== -1 && textareaIndex !== -1 && promptIndex < textareaIndex, "the prompt must appear before the field");
});

test("the textarea carries the required fit classes for the label wrapper", () => {
  const markup = mobileComposeMarkup();
  const textareaClass = /<textarea\b[^>]*\bclass="([^"]*)"/.exec(markup);
  assert.notEqual(textareaClass, null, "the textarea has a class attribute");
  const classes = textareaClass![1]!.split(/\s+/);
  // Height is the stylesheet's: auto height centers the single line — and
  // the caret — on the prompt's line inside the 44px label; h-full pinned
  // the caret to the box's top-left.
  const required = [
    "w-full",
    "bg-transparent",
    "font-mono",
    "resize-none",
    "outline-none",
  ];
  for (const cls of required) {
    assert.ok(classes.includes(cls), `textarea is missing required class ${cls}`);
  }
  assert.ok(!classes.includes("h-full"),
    "h-full pins the caret to the box's top-left; height is the stylesheet's");
});

test("there is exactly one submit button with the send hook and 'Send' label", () => {
  const markup = mobileComposeMarkup();
  assert.equal(countOccurrences(markup, "<button"), 1);
  assert.ok(/<button\b[^>]*\btype="submit"/.test(markup));
  assert.ok(/<button\b[^>]*\bdata-mobile-compose-send\b/.test(markup));
  assert.ok(/<button\b[^>]*\baria-label="Send"/.test(markup));
});

test("the send button is a 44px accent target", () => {
  const markup = mobileComposeMarkup();
  const buttonClass = /<button\b[^>]*\bclass="([^"]*)"/.exec(markup);
  assert.notEqual(buttonClass, null, "the send button has a class attribute");
  const classes = buttonClass![1]!.split(/\s+/);
  assert.ok(classes.includes("h-11"), "send button needs h-11 for a 44px height");
  assert.ok(classes.includes("w-11"), "send button needs w-11 for a 44px width");
  assert.ok(classes.includes("bg-accent"), "send button needs an accent fill");
});

test("the markup exposes no keyboard or Escape surface", () => {
  const markup = mobileComposeMarkup();
  for (const token of ["keydown", "keyup", "keypress", "Escape"]) {
    assert.equal(countOccurrences(markup, token), 0, `markup must not contain ${token}`);
  }
});
