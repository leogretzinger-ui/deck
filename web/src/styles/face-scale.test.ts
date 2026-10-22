import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { test } from "node:test";

const css = readFileSync(new URL("./app.css", import.meta.url), "utf8");

// The scale the app paints with, and the ramp the licensed cut was tuned to.
// A literal changing here is a design decision; a wrapper disappearing is the
// face layer silently ceasing to exist, which is what these assertions catch.
const SIZES: Array<[string, string]> = [
  ["--text-body", "15px"],
  ["--text-body-sm", "var(--type-body-sm-size)"],
  ["--text-caption", "12px"],
  ["--text-micro", "11px"],
  ["--text-title-3", "17px"],
  ["--text-title-2", "22px"],
];
const LEADINGS: Array<[string, string]> = [
  ["--text-body--line-height", "1.55"],
  ["--text-body-sm--line-height", "var(--type-body-sm-lh)"],
  ["--text-caption--line-height", "1.4"],
  ["--text-micro--line-height", "1.3"],
  ["--text-title-3--line-height", "1.34"],
  ["--text-title-2--line-height", "1.28"],
];

function escapeRegExp(text: string): string {
  return text.replace(/[\\^$.*+?()[\]{}|\/-]/g, "\\$&");
}

function declaration(token: string): string {
  const m = new RegExp(`${escapeRegExp(token)}\\s*:\\s*([^;]+);`).exec(css);
  assert.ok(m, `${token} must be declared`);
  return m[1]!.trim();
}

test("every painted size reads through the face size trim", () => {
  for (const [token, literal] of SIZES) {
    assert.equal(
      declaration(token),
      `calc(${literal} * var(--face-ui-size, 1))`,
      `${token} must multiply its ramp by the face trim`,
    );
  }
});

test("every painted leading reads through the face leading trim", () => {
  for (const [token, literal] of LEADINGS) {
    assert.equal(
      declaration(token),
      `calc(${literal} * var(--face-ui-leading, 1))`,
      `${token} must multiply its ramp by the face trim`,
    );
  }
});

// The identity is supplied by the var() fallback and nowhere else. A trim
// defined at :root would make the default face a special case, and a missing
// fallback would collapse the declaration to an invalid value.
test("the trims are never declared, so the default resolves to the bare ramp", () => {
  for (const trim of ["--face-ui-size", "--face-ui-leading"]) {
    assert.equal(
      new RegExp(`${trim}\\s*:`).test(css), false,
      `${trim} must not be declared; the identity is the var() fallback`,
    );
  }
});

// What the arithmetic actually produces, rather than only its shape.
function resolve(expr: string, trim: number | null): number {
  const m = /^calc\(([\d.]+)(px)? \* var\(--face-ui-(?:size|leading), 1\)\)$/.exec(expr);
  assert.ok(m, `unrecognised expression: ${expr}`);
  return Number(m[1]) * (trim ?? 1);
}

test("the default computes the literal, and a trim moves the whole scale", () => {
  assert.equal(resolve(declaration("--text-caption"), null), 12);
  assert.equal(resolve(declaration("--text-body"), null), 15);
  // A measured face at 0.9 moves every size by the same factor.
  assert.equal(resolve(declaration("--text-caption"), 0.9), 10.8);
  assert.equal(resolve(declaration("--text-body"), 0.9), 13.5);
  assert.equal(resolve(declaration("--text-body--line-height"), 1.04), 1.612);
});

// The wrapping is worthless if Tailwind does not carry it into the utilities
// the markup actually uses, so this compiles the real stylesheet and reads the
// generated rules back.
test("the compiled utilities consume the wrapped tokens", async () => {
  const require = createRequire(new URL("../../package.json", import.meta.url));
  // The package's CJS entry exposes the API under `default`; take whichever
  // shape this resolution yields so a packaging change fails loudly here
  // rather than silently skipping the assertions below.
  const mod = await import(require.resolve("tailwindcss"));
  const tw = ("compile" in mod ? mod : mod.default) as { compile: typeof import("tailwindcss").compile };
  assert.equal(typeof tw.compile, "function", "the Tailwind compile API must be reachable");
  const indexPath = require.resolve("tailwindcss/index.css");
  const compiler = await tw.compile(css, {
    base: new URL("..", import.meta.url).pathname,
    loadStylesheet: async () => ({
      path: indexPath,
      base: indexPath.replace(/\/index\.css$/, ""),
      content: readFileSync(indexPath, "utf8"),
    }),
  });
  const out = compiler.build(["text-caption", "text-body-sm", "text-micro"]);

  assert.match(out, /--text-caption:\s*calc\(12px \* var\(--face-ui-size, 1\)\)/,
    "the compiled theme must keep the trim expression");
  assert.match(out, /\.text-caption\s*\{[^}]*font-size:\s*var\(--text-caption\)/,
    "the utility must read the wrapped token, not an inlined literal");
  // A face scope must survive compilation, or selecting a face changes nothing.
  assert.match(out, /\[data-face="inter"\]/,
    "face scopes must reach the compiled sheet");
});
