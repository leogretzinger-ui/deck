import assert from "node:assert/strict";
import test from "node:test";

import { activateDialog } from "./dialog-focus.ts";

interface FakeFocusable {
  element: HTMLElement;
  focused: () => number;
}

function focusable(documentState: { activeElement: unknown }): FakeFocusable {
  let count = 0;
  const element = {
    hidden: false,
    getClientRects: () => [{ width: 1 }],
    focus: () => {
      count++;
      documentState.activeElement = element;
    },
  } as unknown as HTMLElement;
  return { element, focused: () => count };
}

test("activateDialog focuses, traps both Tab directions, dismisses, and disposes", () => {
  const documentState = { activeElement: null as unknown };
  const first = focusable(documentState);
  const last = focusable(documentState);
  let keydown: ((event: KeyboardEvent) => void) | null = null;
  let removed: ((event: KeyboardEvent) => void) | null = null;
  let dismissals = 0;
  const dialog = {
    querySelectorAll: () => [first.element, last.element],
    addEventListener: (_type: string, handler: (event: KeyboardEvent) => void) => { keydown = handler; },
    removeEventListener: (_type: string, handler: (event: KeyboardEvent) => void) => { removed = handler; },
  } as unknown as HTMLElement;
  const originalDocument = Object.getOwnPropertyDescriptor(globalThis, "document");
  Object.defineProperty(globalThis, "document", { configurable: true, value: documentState });
  try {
    const dispose = activateDialog(dialog, () => { dismissals++; });
    assert.equal(first.focused(), 1, "the first visible control receives initial focus");

    let prevented = 0;
    documentState.activeElement = last.element;
    keydown!({ key: "Tab", shiftKey: false, preventDefault: () => { prevented++; } } as KeyboardEvent);
    assert.equal(first.focused(), 2, "Tab wraps from the last control to the first");

    documentState.activeElement = first.element;
    keydown!({ key: "Tab", shiftKey: true, preventDefault: () => { prevented++; } } as KeyboardEvent);
    assert.equal(last.focused(), 1, "Shift+Tab wraps from the first control to the last");

    keydown!({ key: "Escape", shiftKey: false, preventDefault: () => { prevented++; } } as KeyboardEvent);
    assert.equal(dismissals, 1);
    assert.equal(prevented, 3);

    dispose();
    assert.equal(removed, keydown, "the disposer removes the listener it installed");
  } finally {
    if (originalDocument === undefined) delete (globalThis as { document?: unknown }).document;
    else Object.defineProperty(globalThis, "document", originalDocument);
  }
});

test("activateDialog uses a visible requested initial control", () => {
  const documentState = { activeElement: null as unknown };
  const first = focusable(documentState);
  const preferred = focusable(documentState);
  const dialog = {
    querySelectorAll: () => [first.element, preferred.element],
    querySelector: (selector: string) => selector === "[data-return]" ? preferred.element : null,
    addEventListener: () => {},
    removeEventListener: () => {},
  } as unknown as HTMLElement;
  const originalDocument = Object.getOwnPropertyDescriptor(globalThis, "document");
  Object.defineProperty(globalThis, "document", { configurable: true, value: documentState });
  try {
    activateDialog(dialog, () => {}, "[data-return]");
    assert.equal(first.focused(), 0, "the default control is not focused first");
    assert.equal(preferred.focused(), 1, "the requested control receives initial focus");
  } finally {
    if (originalDocument === undefined) delete (globalThis as { document?: unknown }).document;
    else Object.defineProperty(globalThis, "document", originalDocument);
  }
});
