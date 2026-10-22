// The theme palettes' contrast floor. No ANSI slot may resolve to its
// own background — SGR-37/97 must never equal the ground colour.
import assert from "node:assert/strict";
import test from "node:test";
import { terminalTheme } from "./theme.ts";

function luminance(hex: string): number {
  const c = [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16) / 255);
  const lin = c.map((v) => (v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4));
  return 0.2126 * lin[0]! + 0.7152 * lin[1]! + 0.0722 * lin[2]!;
}

function contrast(a: string, b: string): number {
  const x = luminance(a);
  const y = luminance(b);
  return (Math.max(x, y) + 0.05) / (Math.min(x, y) + 0.05);
}

for (const mode of ["dark", "light"] as const) {
  test(`${mode}: every painted slot clears a 1.5:1 floor on its background`, () => {
    const theme = terminalTheme(mode);
    const bg = theme.background!;
    for (const [slot, value] of Object.entries(theme)) {
      if (slot === "background" || value === undefined || typeof value !== "string") continue;
      assert.ok(
        contrast(value, bg) >= 1.5,
        `${mode}.${slot} ${value} on ${bg} = ${contrast(value, bg).toFixed(2)}:1`,
      );
    }
  });
}
