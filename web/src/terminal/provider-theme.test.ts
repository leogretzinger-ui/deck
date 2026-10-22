// Presentation palettes are measured facts about a provider's rendering,
// so the tests hold both halves: the palette keeps its contrast floors on
// the measured ground, and the resolution never reaches past its one
// provider.
import test from "node:test";
import assert from "node:assert/strict";

import type { ITheme } from "@xterm/xterm";

import {
  terminalChromeFor,
  terminalThemeFor,
} from "./provider-theme.ts";
import { TERMINAL_THEMES } from "./themes.ts";

function relativeLuminance(hex: string): number {
  const channels = [0, 2, 4].map((offset) => Number.parseInt(hex.slice(1 + offset, 3 + offset), 16) / 255);
  const [red, green, blue] = channels.map((channel) =>
    channel <= 0.03928 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4,
  );
  return 0.2126 * red! + 0.7152 * green! + 0.0722 * blue!;
}

function contrastRatio(first: string, second: string): number {
  const firstLuminance = relativeLuminance(first);
  const secondLuminance = relativeLuminance(second);
  return (
    (Math.max(firstLuminance, secondLuminance) + 0.05) /
    (Math.min(firstLuminance, secondLuminance) + 0.05)
  );
}

test("mimo resolves to the measured presentation palette", () => {
  const theme = terminalThemeFor("mimo", TERMINAL_THEMES.noisefloor);
  // The ground is MiMo 0.1.13's painted card colour, measured 2026-08-23.
  // If it changes, the measurement changed and the palette must be
  // re-measured, not patched.
  assert.equal(theme.background, "#1E1E1E");
  const chrome = terminalChromeFor("mimo", {
    bg: "#0A1522", fg: "#E4E1D8", dim: "#8A93A3", faint: "#5E6878",
    rule: "#2A313B", accent: "#E0764E", panelBorder: "#3A414D",
    inputBg: "#353A45", warn: "#D7D74A", magenta: "#E255A1", cyan: "#56C8D8",
  });
  assert.equal(chrome.bg, "#1E1E1E");
  assert.equal(chrome.accent, "#FB8147");
});

test("presentation palettes clear the dark-theme contrast floors", () => {
  const theme = terminalThemeFor("mimo", TERMINAL_THEMES.noisefloor);
  assert.ok(contrastRatio(theme.foreground as string, theme.background as string) >= 7,
    "foreground");
  // Every foreground slot, enumerated from the standard ANSI set rather than
  // from the ones that happen to be set: a slot left undefined is exactly the
  // failure this pin exists to prevent, and a test that iterates what is
  // present can never see one missing.
  const FOREGROUND_SLOTS = [
    "white", "brightWhite", "red", "brightRed", "green", "brightGreen",
    "yellow", "brightYellow", "blue", "brightBlue", "magenta", "brightMagenta",
    "cyan", "brightCyan",
  ] as const;
  for (const slot of FOREGROUND_SLOTS) {
    const value = theme[slot];
    assert.equal(typeof value, "string", `${slot} is unset, so xterm substitutes its own default`);
    assert.ok(contrastRatio(value as string, theme.background as string) >= 4.5, slot);
  }
  // Black is a background slot: it steps off the ground rather than clearing
  // a foreground floor, so it is asserted as set and distinct, not legible.
  assert.equal(typeof theme.black, "string", "black is unset");
  assert.notEqual(theme.black, theme.background, "black is indistinguishable from the ground");
  assert.ok(contrastRatio(theme.brightBlack as string, theme.background as string) >= 3,
    "brightBlack");
  assert.ok(contrastRatio(theme.cursor as string, theme.background as string) >= 3,
    "cursor");
  assert.ok(
    contrastRatio(theme.foreground as string, theme.selectionBackground as string) >= 4.5,
    "foreground over the selection wash",
  );
});

test("every other provider keeps the user's palette untouched", () => {
  const bases: ITheme[] = [TERMINAL_THEMES.noisefloor, TERMINAL_THEMES.onelight];
  for (const provider of [undefined, "", "claude", "codex", "grok", "opencode"] as const) {
    for (const base of bases) {
      assert.equal(terminalThemeFor(provider, base), base,
        `${provider ?? "undefined"} must not reach a presentation palette`);
    }
  }
});
