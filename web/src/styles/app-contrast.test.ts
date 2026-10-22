// Light mode's text roles must clear AA on the opaque grounds they paint on.
// The terminal palettes carry the same obligation in themes.test.ts; these
// roles are CSS custom properties rather than TypeScript, so the stylesheet
// is what this reads.
//
// Token pairs only. A wash composited over a ground, and anything a rendered
// state produces, needs a browser and is not claimed here.
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const css = readFileSync(new URL("./app.css", import.meta.url), "utf8");

// The light ramps and their role aliases live in the @theme blocks, and
// [data-theme="dark"] { overrides them below, so only what precedes that rule
// is light. The selector also appears in a @custom-variant earlier in the
// file, which is why the boundary is the rule's opening brace and not the
// bare string.
function lightBlock(): string {
  const start = css.indexOf("@theme {");
  assert.notEqual(start, -1, "app.css no longer opens its light ramps with @theme");
  const end = css.indexOf('\n[data-theme="dark"] {', start);
  assert.notEqual(end, -1, "app.css no longer defines a dark override block");
  return css.slice(start, end);
}

// Resolving a role to a literal, refusing every shape that would make the
// answer ambiguous: a token declared twice (the first would silently win), a
// token declared nowhere, and an alias chain that never reaches a colour.
function token(name: string, seen: readonly string[] = []): string {
  assert.ok(!seen.includes(name), `--${name} closes an alias cycle: ${[...seen, name].join(" -> ")}`);
  const block = lightBlock();
  const declared = [...block.matchAll(new RegExp(`--${name}:\\s*([^;]+);`, "g"))];
  assert.equal(declared.length, 1,
    `--${name} is declared ${declared.length} times in the light ramps; one of them would silently win`);
  const value = declared[0]![1]!.trim();
  const indirect = /^var\(--([a-z0-9-]+)\)$/.exec(value);
  return indirect ? token(indirect[1]!, [...seen, name]) : value;
}

// Six hex digits and nothing else. An eight-digit literal slices the same
// three channels and scores a fully transparent ink as opaque, which is the
// failure that matters: the pair is certified against a colour nobody can
// see. Other notations already fail the comparison, since a NaN ratio is
// never above the floor; asserting the format gives them a diagnostic that
// names the cause instead of a contrast number they never had.
function luminance(hex: string): number {
  assert.match(hex, /^#[0-9a-f]{6}$/i, `${hex} is not an opaque six-digit hex; this test cannot judge it`);
  const channels = [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16) / 255);
  const linear = channels.map((v) => (v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4));
  return 0.2126 * linear[0]! + 0.7152 * linear[1]! + 0.0722 * linear[2]!;
}

function contrast(a: string, b: string): number {
  const x = luminance(a);
  const y = luminance(b);
  return (Math.max(x, y) + 0.05) / (Math.min(x, y) + 0.05);
}

// AA for small text. Every one of these roles is used at body size or below —
// tertiary carries the smallest and the most of it.
const FLOOR = 4.5;

test("light mode's text roles clear AA on every opaque ground that carries text", () => {
  // Every opaque ground the app paints text on, checked against all three
  // roles rather than the pairs that happen to exist today: a role is moved
  // onto a ground far more often than a ground is introduced, and a list of
  // observed pairs would pass a regression the day someone reuses one. The
  // semantic aliases are here beside the ramps they currently equal, so
  // repointing one is caught instead of passing on its old value.
  //
  // Translucent tokens are deliberately absent — a wash over a ground has to
  // be composited, which needs a browser.
  const surfaces = [
    "color-surface-card", "color-paper", "color-surface-muted", "color-surface-sunken",
    "color-surface", "color-surface-overlay", "color-wash-selected",
    "color-sidebar-surface", "color-field", "color-field-hover",
    "color-accent-tint", "color-warning-tint", "color-error-tint",
    // The font bench paints primary ink on the success tint and takes these
    // same tokens, so it is one of the app's opaque grounds too.
    "color-success-tint",
  ];
  for (const role of ["color-text-primary", "color-text-secondary", "color-text-tertiary"]) {
    const ink = token(role);
    for (const surface of surfaces) {
      const ground = token(surface);
      const ratio = contrast(ink, ground);
      assert.ok(
        ratio >= FLOOR,
        `light ${role} ${ink} on ${surface} ${ground} = ${ratio.toFixed(2)}:1, floor ${FLOOR}`,
      );
    }
  }
});
