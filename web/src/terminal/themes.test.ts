// The curated palettes' shape contract: a slot left undefined silently
// inherits an xterm default tuned for a different background, so every
// named community scheme must paint the full set. "noisefloor" and
// "custom" are exempt from the ANSI red/green/blue slots the design does
// not define — noisefloor must instead stay byte-identical to the
// workspace mapping it realizes, including the slots that
// mapping leaves to xterm.
import { test } from "node:test";
import assert from "node:assert/strict";
import type { ITheme } from "@xterm/xterm";
import { terminalTheme } from "../app/theme.ts";
import {
  CUSTOM_SLOT_LABELS,
  DEFAULT_CUSTOM_SLOTS,
  DEFAULT_TERMINAL_THEME,
  TERMINAL_THEME_LABELS,
  TERMINAL_THEME_NAMES,
  TERMINAL_THEMES,
  customTerminalTheme,
} from "./themes.ts";

const REQUIRED_KEYS: readonly (keyof ITheme)[] = [
  "background",
  "foreground",
  "cursor",
  "selectionBackground",
  "black",
  "red",
  "green",
  "yellow",
  "blue",
  "magenta",
  "cyan",
  "white",
  "brightBlack",
  "brightRed",
  "brightGreen",
  "brightYellow",
  "brightBlue",
  "brightMagenta",
  "brightCyan",
  "brightWhite",
];

// The design defines no ANSI red/green/blue (or black beyond noisefloor's
// line-200) for these two; their remaining slots stay xterm's.
const PARTIAL_THEMES = new Set(["noisefloor", "custom"]);

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

test("light themes clear their role contrast floors", () => {
  const ansi: readonly (keyof ITheme)[] = [
    "red", "brightRed", "green", "brightGreen", "yellow", "brightYellow",
    "blue", "brightBlue", "magenta", "brightMagenta", "cyan", "brightCyan",
  ];
  const lightThemes = ["everforest", "onelight"] as const;

  for (const name of lightThemes) {
    const theme = TERMINAL_THEMES[name];
    for (const slot of ["foreground", "brightWhite", "black"] as const) {
      assert.ok(contrastRatio(theme[slot] as string, theme.background as string) >= 7, `${name}.${slot}`);
    }
    for (const slot of ansi) {
      assert.ok(contrastRatio(theme[slot] as string, theme.background as string) >= 4.5, `${name}.${slot}`);
    }
    assert.ok(contrastRatio(theme.white as string, theme.background as string) >= 4.5, `${name}.white`);
    assert.ok(contrastRatio(theme.brightBlack as string, theme.background as string) >= 3, `${name}.brightBlack`);
    assert.ok(contrastRatio(theme.cursor as string, theme.background as string) >= 3, `${name}.cursor`);
    assert.ok(
      contrastRatio(theme.foreground as string, theme.selectionBackground as string) >= 4.5,
      `${name}.foreground on selectionBackground`,
    );
  }
});

test("names, labels, and the themes record cover exactly the same set", () => {
  const names = [...TERMINAL_THEME_NAMES].sort();
  assert.deepEqual(Object.keys(TERMINAL_THEMES).sort(), names);
  assert.deepEqual(Object.keys(TERMINAL_THEME_LABELS).sort(), names);
  assert.ok(TERMINAL_THEME_NAMES.includes(DEFAULT_TERMINAL_THEME));
  assert.equal(DEFAULT_TERMINAL_THEME, "noisefloor");
});

test("the design's theme list, in order, with its labels", () => {
  assert.deepEqual(
    TERMINAL_THEME_NAMES.map((name) => TERMINAL_THEME_LABELS[name]),
    ["Noisefloor", "Gruvbox", "Tokyo Night", "Nord", "Everforest Light", "One Light", "Custom"],
  );
});

test("every named community scheme paints all required slots with hex colors", () => {
  for (const name of TERMINAL_THEME_NAMES) {
    if (PARTIAL_THEMES.has(name)) continue;
    const theme = TERMINAL_THEMES[name];
    for (const key of REQUIRED_KEYS) {
      const value = theme[key];
      assert.match(
        typeof value === "string" ? value : "",
        /^#[0-9a-f]{6}$/i,
        `${name}.${String(key)}`,
      );
    }
  }
});

test('"noisefloor" preserves the workspace terminal mapping verbatim', () => {
  assert.deepEqual(TERMINAL_THEMES.noisefloor, terminalTheme("dark"));
});

test("dark DC anchors and light retuned anchors stay pinned", () => {
  // Spot-pins the two provenance rules: dark-theme anchors match the
  // imported DC verbatim, while light-theme anchors pin design-side retuned
  // values that are expected to differ from that DC. The contrast-floor test
  // above computes the correctness bounds for the light themes.
  assert.equal(TERMINAL_THEMES.gruvbox.background, "#282828");
  assert.equal(TERMINAL_THEMES.gruvbox.cursor, "#FE8019");
  assert.equal(TERMINAL_THEMES.tokyonight.background, "#1A1B26");
  assert.equal(TERMINAL_THEMES.tokyonight.foreground, "#C0CAF5");
  assert.equal(TERMINAL_THEMES.nord.background, "#2E3440");
  assert.equal(TERMINAL_THEMES.nord.cursor, "#D08770");
  assert.equal(TERMINAL_THEMES.everforest.background, "#FDF6E3");
  assert.equal(TERMINAL_THEMES.everforest.foreground, "#48565D");
  assert.equal(TERMINAL_THEMES.onelight.background, "#FAFAFA");
  assert.equal(TERMINAL_THEMES.onelight.cursor, "#9B6900");
  assert.equal(TERMINAL_THEMES.custom.background, "#10141B");
});

test("custom slots and labels cover the design's six rows", () => {
  assert.deepEqual(
    Object.keys(CUSTOM_SLOT_LABELS),
    ["background", "text", "dimText", "accent", "warning", "cyan"],
  );
  assert.deepEqual(
    Object.values(CUSTOM_SLOT_LABELS),
    ["Background", "Text", "Dim text", "Accent", "Warning", "Cyan"],
  );
});

test("customTerminalTheme maps the six slots over the custom base", () => {
  const theme = customTerminalTheme({
    background: "#101010",
    text: "#EEEEEE",
    dimText: "#888888",
    accent: "#FF8800",
    warning: "#DDDD44",
    cyan: "#44CCDD",
  });
  assert.equal(theme.background, "#101010");
  assert.equal(theme.foreground, "#EEEEEE");
  assert.equal(theme.brightWhite, "#EEEEEE");
  assert.equal(theme.white, "#888888");
  assert.equal(theme.cursor, "#FF8800");
  assert.equal(theme.yellow, "#DDDD44");
  assert.equal(theme.brightYellow, "#DDDD44");
  assert.equal(theme.cyan, "#44CCDD");
  assert.equal(theme.brightCyan, "#44CCDD");
  // Unedited roles keep the base: magenta, faint, and the selection wash.
  assert.equal(theme.magenta, TERMINAL_THEMES.custom.magenta);
  assert.equal(theme.brightBlack, TERMINAL_THEMES.custom.brightBlack);
  assert.equal(theme.selectionBackground, TERMINAL_THEMES.custom.selectionBackground);
});

test("the default custom slots are the design's custom-set values", () => {
  assert.deepEqual(customTerminalTheme(DEFAULT_CUSTOM_SLOTS), TERMINAL_THEMES.custom);
});
