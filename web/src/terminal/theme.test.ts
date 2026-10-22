import { test } from "node:test";
import assert from "node:assert/strict";
import {
  customThemeSlots,
  normalizeHex,
  resolveTerminalTheme,
  setCustomThemeSlots,
  setTerminalThemeName,
  terminalThemeName,
} from "./theme.ts";
import {
  DEFAULT_CUSTOM_SLOTS,
  DEFAULT_TERMINAL_THEME,
  TERMINAL_THEMES,
} from "./themes.ts";

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

test("terminal theme defaults to the preserved default palette", () => {
  withStorage(() => {
    assert.equal(terminalThemeName(), DEFAULT_TERMINAL_THEME);
  });
});

test("terminal theme persists round-trip and unknown values fall back", () => {
  withStorage((store) => {
    assert.equal(setTerminalThemeName("nord"), "nord");
    assert.equal(terminalThemeName(), "nord");
    store.set("noisedeck-terminal-theme", "hotdog-stand");
    assert.equal(terminalThemeName(), DEFAULT_TERMINAL_THEME);
    // Object.prototype names must not resolve through the record.
    store.set("noisedeck-terminal-theme", "toString");
    assert.equal(terminalThemeName(), DEFAULT_TERMINAL_THEME);
    assert.equal(setTerminalThemeName("not-a-theme"), DEFAULT_TERMINAL_THEME);
    assert.equal(terminalThemeName(), DEFAULT_TERMINAL_THEME);
  });
});

test("legacy stored names from the pre-design set fall back to the default", () => {
  withStorage((store) => {
    for (const legacy of ["default", "gruvbox-dark", "catppuccin-mocha", "everforest-dark"]) {
      store.set("noisedeck-terminal-theme", legacy);
      assert.equal(terminalThemeName(), DEFAULT_TERMINAL_THEME);
    }
  });
});

test("normalizeHex accepts #rgb/#rrggbb and rejects everything else", () => {
  assert.equal(normalizeHex("#a1b2c3"), "#A1B2C3");
  assert.equal(normalizeHex(" #A1B2C3 "), "#A1B2C3");
  assert.equal(normalizeHex("#abc"), "#AABBCC");
  assert.equal(normalizeHex("a1b2c3"), null);
  assert.equal(normalizeHex("#a1b2c"), null);
  assert.equal(normalizeHex("#gggggg"), null);
  assert.equal(normalizeHex(""), null);
});

test("custom slots default, persist per slot, and reject invalid colors", () => {
  withStorage((store) => {
    assert.deepEqual(customThemeSlots(), DEFAULT_CUSTOM_SLOTS);
    const applied = setCustomThemeSlots({ ...DEFAULT_CUSTOM_SLOTS, accent: "#ff8800" });
    assert.equal(applied.accent, "#FF8800");
    assert.equal(customThemeSlots().accent, "#FF8800");
    // Slots the record leaves invalid keep their stored value.
    assert.equal(customThemeSlots().background, DEFAULT_CUSTOM_SLOTS.background);
    assert.equal(
      setCustomThemeSlots({ ...customThemeSlots(), accent: "orange" }).accent,
      "#FF8800",
    );
    // A corrupt record falls back per slot.
    store.set("noisedeck-terminal-theme-custom", `{"accent":"#123456","text":"nope"}`);
    assert.equal(customThemeSlots().accent, "#123456");
    assert.equal(customThemeSlots().text, DEFAULT_CUSTOM_SLOTS.text);
    store.set("noisedeck-terminal-theme-custom", "not-json");
    assert.deepEqual(customThemeSlots(), DEFAULT_CUSTOM_SLOTS);
  });
});

test("resolveTerminalTheme returns curated palettes and composes custom", () => {
  withStorage(() => {
    assert.deepEqual(resolveTerminalTheme("nord"), TERMINAL_THEMES.nord);
    assert.deepEqual(resolveTerminalTheme("custom"), TERMINAL_THEMES.custom);
    setCustomThemeSlots({ ...DEFAULT_CUSTOM_SLOTS, background: "#111111" });
    assert.equal(resolveTerminalTheme("custom").background, "#111111");
  });
});
