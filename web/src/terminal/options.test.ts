import { test } from "node:test";
import assert from "node:assert/strict";
import { composeTerminalOptions, deviceLetterSpacing, monoTrackingPx, monoWeight, watchDevicePixelRatio, type MediaQueryLike, type RatioWindow } from "./options.ts";

test("composed terminal options include the macOS Option+click selector and per-mount values", () => {
  const fontFamily = "monospace";
  const fontSize = 14;
  const theme = { background: "#000000", foreground: "#ffffff" };

  const options = composeTerminalOptions(fontFamily, fontSize, theme);

  assert.equal(options.macOptionClickForcesSelection, true);
  assert.equal(options.fontFamily, fontFamily);
  assert.equal(options.fontSize, fontSize);
  assert.equal(options.theme, theme);
});

test("letter spacing defaults to none and is carried when given", () => {
  const theme = { background: "#000000", foreground: "#ffffff" };
  assert.equal(composeTerminalOptions("monospace", 14, theme).letterSpacing, 0);
  assert.equal(composeTerminalOptions("monospace", 14, theme, 1.078).letterSpacing, 1.078);
});

// The stylesheet states mono tracking in em; the terminal needs pixels at
// its own font size, and a unitless or malformed value must not become NaN.
test("monoTrackingPx converts the stylesheet's tracking at the terminal's size", () => {
  assert.equal(monoTrackingPx("0.077em", 14), 1.078);
  assert.equal(monoTrackingPx("0em", 14), 0);
  assert.equal(monoTrackingPx("-0.01em", 10), -0.1);
  assert.equal(monoTrackingPx("2px", 14), 2);
  assert.equal(monoTrackingPx("", 14), 0);
  assert.equal(monoTrackingPx("normal", 14), 0);
  assert.equal(monoTrackingPx("0.5", 14), 0);
});

test("deviceLetterSpacing scales CSS tracking to xterm's device pixels", () => {
  assert.equal(deviceLetterSpacing(1, 2), 2);
  assert.equal(deviceLetterSpacing(1, 1), 1);
  assert.equal(deviceLetterSpacing(1.078, 2), 2);
  assert.equal(deviceLetterSpacing(0, 2), 0);
  assert.equal(deviceLetterSpacing(1, Number.NaN), 1);
  assert.equal(deviceLetterSpacing(1, 0), 1);
});

// The watcher arms a query for the current ratio, hears a change through the
// query or through a resize, reports only when the ratio actually moved,
// re-arms for the new ratio (a query matches one ratio only), and leaves no
// listener behind once disposed.
test("watchDevicePixelRatio reports real ratio changes from either signal and disposes cleanly", () => {
  let ratio = 1;
  const queries: Array<{ query: string; listeners: Set<() => void> }> = [];
  const resize = new Set<() => void>();
  const win: RatioWindow = {
    matchMedia: (query: string): MediaQueryLike => {
      const entry = { query, listeners: new Set<() => void>() };
      queries.push(entry);
      return { addEventListener: (_t, l) => entry.listeners.add(l), removeEventListener: (_t, l) => entry.listeners.delete(l) };
    },
    get devicePixelRatio() { return ratio; },
    addEventListener: (_t, l) => resize.add(l),
    removeEventListener: (_t, l) => resize.delete(l),
  };
  const seen: number[] = [];
  const watch = watchDevicePixelRatio((r) => seen.push(r), win);
  assert.deepEqual(queries.map((q) => q.query), ["(resolution: 1dppx)"]);
  for (const l of [...resize]) l();
  assert.deepEqual(seen, [], "a resize at the same ratio is not a change");
  ratio = 2;
  for (const l of [...resize]) l();
  assert.deepEqual(seen, [2], "a resize that carries a new ratio reports it");
  assert.deepEqual(queries.map((q) => q.query), ["(resolution: 1dppx)", "(resolution: 2dppx)"]);
  assert.equal(queries[0]!.listeners.size, 0, "the old query's listener is removed");
  for (const l of [...queries[1]!.listeners]) l();
  assert.deepEqual(seen, [2], "the query firing at the known ratio reports nothing");
  ratio = 1.5;
  for (const l of [...queries[1]!.listeners]) l();
  assert.deepEqual(seen, [2, 1.5], "the query firing with a new ratio reports it");
  watch.dispose();
  assert.equal(queries[2]!.listeners.size, 0, "disposal removes the live query listener");
  assert.equal(resize.size, 0, "disposal removes the resize listener");
  ratio = 3;
  for (const l of [...resize]) l();
  assert.deepEqual(seen, [2, 1.5], "a disposed watcher reports nothing");
});

test("the mono weight reaches xterm, defaulting to regular", () => {
  const theme = { background: "#000000", foreground: "#ffffff" };
  assert.equal(composeTerminalOptions("monospace", 14, theme).fontWeight, 400);
  assert.equal(composeTerminalOptions("monospace", 14, theme, 0, 600).fontWeight, 600);
  assert.equal(monoWeight("600"), 600);
  assert.equal(monoWeight(" 400 "), 400);
  assert.equal(monoWeight(""), 400);
  assert.equal(monoWeight("bold"), 400);
  assert.equal(monoWeight("950"), 400);
});
