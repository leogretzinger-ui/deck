// Focus routing for terminal attachments. These run without a browser by
// feeding fake elements into the guard.
import assert from "node:assert/strict";
import test from "node:test";
import {
  focusObstacle,
  TerminalFocusRequest,
  terminalFocusAllowed,
} from "./terminal-focus.ts";

function fakeElement(
  tag: string,
  { region, parent }: { region?: string; parent?: Element } = {},
): Element {
  const el: Element = {
    tagName: tag,
    parent,
    closest(selector: string) {
      if (selector !== "[data-region]") return null;
      if (region !== undefined) return el;
      return parent?.closest(selector) ?? null;
    },
    getAttribute(name: string) {
      if (name === "data-region") return region ?? null;
      return null;
    },
  } as unknown as Element;
  return el;
}

const body = fakeElement("body");

test("terminalFocusAllowed permits focus when nothing has focus", () => {
  assert.equal(terminalFocusAllowed(null, body), true);
});

test("terminalFocusAllowed permits focus when focus is on the body", () => {
  assert.equal(terminalFocusAllowed(body, body), true);
});

test("terminalFocusAllowed refuses focus inside a text field", () => {
  assert.equal(terminalFocusAllowed(fakeElement("input"), body), false);
  assert.equal(terminalFocusAllowed(fakeElement("select"), body), false);
});

test("terminalFocusAllowed refuses focus inside an open dialog", () => {
  const dialog = fakeElement("div", { region: "modals" });
  const button = fakeElement("button", { parent: dialog });
  assert.equal(terminalFocusAllowed(button, body), false);
});

test("terminalFocusAllowed refuses focus inside an open popover or menu", () => {
  const popover = fakeElement("div", { region: "popovers" });
  const item = fakeElement("button", { parent: popover });
  assert.equal(terminalFocusAllowed(item, body), false);
});

test("terminalFocusAllowed permits focus on ordinary chrome", () => {
  const sidebar = fakeElement("aside", { region: "sidebar" });
  const row = fakeElement("button", { parent: sidebar });
  assert.equal(terminalFocusAllowed(row, body), true);
});

test("focusObstacle distinguishes composing from an overlay", () => {
  const input = fakeElement("input");
  const dialog = fakeElement("div", { region: "modals" });
  const dialogInput = fakeElement("input", { parent: dialog });
  assert.equal(focusObstacle(input, body), "composing");
  // A form field inside a dialog composes first: eating a keystroke is the
  // worse failure, so the composing classification must win.
  assert.equal(focusObstacle(dialogInput, body), "composing");
  assert.equal(focusObstacle(fakeElement("button", { parent: dialog }), body), "overlay");
  assert.equal(focusObstacle(fakeElement("button"), body), "none");
});

test("TerminalFocusRequest consumes a selection and allows focus when safe", () => {
  const request = new TerminalFocusRequest();
  request.request();
  assert.equal(request.consume(fakeElement("button"), body, false), "granted");
});

test("TerminalFocusRequest refuses focus when no selection was made", () => {
  const request = new TerminalFocusRequest();
  assert.equal(request.consume(fakeElement("button"), body, false), "refused");
});

test("TerminalFocusRequest refuses focus on mobile", () => {
  const request = new TerminalFocusRequest();
  request.request();
  assert.equal(request.consume(fakeElement("button"), body, true), "refused");
  // The phone's soft-key contract is not a timing problem: a later retry
  // must not grant what the first consume refused.
  assert.equal(request.consume(fakeElement("button"), body, false), "refused");
});

test("TerminalFocusRequest refuses for good while a text field holds focus", () => {
  const request = new TerminalFocusRequest();
  request.request();
  assert.equal(request.consume(fakeElement("input"), body, false), "refused");
  assert.equal(request.consume(fakeElement("button"), body, false), "refused");
});

test("TerminalFocusRequest survives a dialog refusal for a later retry", () => {
  const request = new TerminalFocusRequest();
  request.request();
  // The launch picker's own closing frames: the dialog holds focus now
  // and releases it before the retries run out.
  const dialog = fakeElement("div", { region: "modals" });
  assert.equal(request.consume(fakeElement("button", { parent: dialog }), body, false), "retry");
  assert.equal(request.consume(fakeElement("button"), body, false), "granted");
});

test("TerminalFocusRequest cancel retires a retry-pending request for good", () => {
  const request = new TerminalFocusRequest();
  request.request();
  const dialog = fakeElement("div", { region: "modals" });
  assert.equal(request.consume(fakeElement("button", { parent: dialog }), body, false), "retry");
  // The retry loop exhausted its attempts while the overlay was still
  // open and cancelled the request: a later attach with focus on plain
  // chrome must not steal focus with no fresh gesture behind it.
  request.cancel();
  assert.equal(request.consume(fakeElement("button"), body, false), "refused");
});
