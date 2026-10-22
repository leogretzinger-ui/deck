import { test } from "node:test";
import assert from "node:assert/strict";
import {
  DEFAULT_TERMINAL_FONT_SIZE,
  MAX_TERMINAL_FONT_SIZE,
  MIN_TERMINAL_FONT_SIZE,
  setTerminalFontSize,
  terminalFontSize,
} from "./font-size.ts";

// Minimal localStorage stub: the helper's only browser dependency. The
// original property DESCRIPTOR is captured and restored (or the stub
// deleted when none existed) — a host that defines localStorage as an
// accessor gets its accessor back, not a data property over it.
function withStorage(run: (store: Map<string, string>) => void): void {
  const store = new Map<string, string>();
  const original = Object.getOwnPropertyDescriptor(globalThis, "localStorage");
  Object.defineProperty(globalThis, "localStorage", {
    value: {
      getItem: (k: string) => store.get(k) ?? null,
      setItem: (k: string, v: string) => void store.set(k, v),
      removeItem: (k: string) => void store.delete(k),
    },
    configurable: true,
  });
  try {
    run(store);
  } finally {
    if (original === undefined) {
      delete (globalThis as { localStorage?: unknown }).localStorage;
    } else {
      Object.defineProperty(globalThis, "localStorage", original);
    }
  }
}

test("terminal font size defaults to the chrome-matching size", () => {
  withStorage(() => {
    assert.equal(terminalFontSize(), DEFAULT_TERMINAL_FONT_SIZE);
  });
});

test("terminal font size persists, rejects garbage, and clamps the range", () => {
  withStorage((store) => {
    assert.equal(setTerminalFontSize(16), 16);
    assert.equal(terminalFontSize(), 16);
    store.set("noisedeck-terminal-font-size", "banana");
    assert.equal(terminalFontSize(), DEFAULT_TERMINAL_FONT_SIZE);
    store.set("noisedeck-terminal-font-size", "12.5");
    assert.equal(terminalFontSize(), DEFAULT_TERMINAL_FONT_SIZE);
    assert.equal(setTerminalFontSize(999), MAX_TERMINAL_FONT_SIZE);
    assert.equal(setTerminalFontSize(1), MIN_TERMINAL_FONT_SIZE);
    assert.equal(terminalFontSize(), MIN_TERMINAL_FONT_SIZE);
  });
});
