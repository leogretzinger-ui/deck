import { test } from "node:test";
import assert from "node:assert/strict";
import { TERMINAL_THEMES, type TerminalThemeName } from "./themes.ts";
import {
  applyTerminalChromeVars,
  terminalChromePalette,
  type TerminalChromePalette,
} from "./palette.ts";

// The terminal element is the only place the --t-* chrome roles may be
// read, and the projection writes exactly the eleven-role set. The test
// drives the real writer against a captured style bag rather than a DOM,
// so it pins the name/value mapping and the closed set together.
interface StyleCapture {
  style: { setProperty(name: string, value: string): void };
  written: Map<string, string>;
}

function capture(): StyleCapture {
  const written = new Map<string, string>();
  return {
    style: {
      setProperty(name: string, value: string): void {
        written.set(name, value);
      },
    },
    written,
  };
}

const ROLE_VARS: readonly [keyof TerminalChromePalette, string][] = [
  ["bg", "--t-bg"],
  ["fg", "--t-fg"],
  ["dim", "--t-dim"],
  ["faint", "--t-faint"],
  ["rule", "--t-rule"],
  ["accent", "--t-accent"],
  ["panelBorder", "--t-panel-border"],
  ["inputBg", "--t-input-bg"],
  ["warn", "--t-warn"],
  ["magenta", "--t-magenta"],
  ["cyan", "--t-cyan"],
];

function assertRoleSet(target: StyleCapture): void {
  assert.deepEqual(
    [...target.written.keys()].sort(),
    ROLE_VARS.map(([, name]) => name).sort(),
  );
}

test("the chrome writer writes exactly the eleven roles with their values", () => {
  const palette: TerminalChromePalette = {
    bg: "#000000",
    fg: "#111111",
    dim: "#222222",
    faint: "#333333",
    rule: "#444444",
    accent: "#555555",
    panelBorder: "#666666",
    inputBg: "#777777",
    warn: "#888888",
    magenta: "#999999",
    cyan: "#AAAAAA",
  };
  const target = capture();
  applyTerminalChromeVars(target as unknown as HTMLElement, palette);
  assertRoleSet(target);
  for (const [key, name] of ROLE_VARS) {
    assert.equal(target.written.get(name), palette[key]);
  }
});

test("every curated theme projects through the same eleven-role scoping", () => {
  for (const [name, theme] of Object.entries(TERMINAL_THEMES)) {
    const target = capture();
    applyTerminalChromeVars(
      target as unknown as HTMLElement,
      terminalChromePalette(name as TerminalThemeName, theme),
    );
    assertRoleSet(target);
  }
});
