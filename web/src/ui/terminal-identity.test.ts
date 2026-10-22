import assert from "node:assert/strict";
import test from "node:test";

import { sessionTileHtml } from "../panels/sidebar/markup.ts";
import { sessionTileColorStyle } from "./session-identity.ts";
import { terminalIdentityHtml } from "./terminal-identity.ts";

test("the terminal identity contains only provider and session identity", () => {
  for (const provider of ["claude", "cline", "command-code", "codex", "cursor", "devin", "grok", "opencode", "kimi", "mimo", "omp", "qoder", "qwen", "shell"]) {
    const html = terminalIdentityHtml({
      provider,
      display_name: `${provider} working session`,
      color: null,
    }, undefined);
    assert.match(html, /data-terminal-provider/);
    assert.match(html, new RegExp(`${provider} working session`));
    assert.ok(!html.includes("project-name"));
    assert.match(html, /data-titlebar-title class="[^"]*min-w-0[^"]*truncate/);
    // The header carries no status text: no outcome/status subtitle span.
    assert.ok(!html.includes("text-micro"), "identity renders no outcome subtitle");
  }
});

test("user labels remain escaped", () => {
  const html = terminalIdentityHtml({ provider: "claude", display_name: "<long & named>", color: "blue" }, undefined);
  assert.ok(html.includes("&lt;long &amp; named&gt;"));
  assert.ok(!html.includes("<long"));
});

test("the provider tile is a plain colour square with no glyph", () => {
  const html = terminalIdentityHtml({ provider: "shell", display_name: "shell session", color: null }, undefined);
  const tile = /data-terminal-provider class="([^"]*)"/.exec(html)?.[1] ?? "";
  assert.ok(tile.includes("h-[22px] w-[22px]"), "the header tile is 22px");
  assert.ok(tile.includes("rounded-control-sm"), "the header tile uses --radius-control-sm");
  assert.ok(!/\bgrid\b/.test(tile), "no inert grid utility on a content-free tile");
  assert.ok(!tile.includes("font-mono"), "no glyph font on the tile");
  assert.ok(!html.includes("<svg"), "no icon renders inside the tile");
});

// One session identity wears one colour wherever the app draws it: the
// titlebar chip and the sidebar tile must resolve the same background from
// the same session facts. The sidebar side is built exactly as the session
// row builds it — sessionTileHtml over sessionTileColorStyle with the
// group's derived tint — and the chip side is terminalIdentityHtml with
// that tint, so a divergence here is two surfaces disagreeing about one
// identity, not a styling choice.
test("the sidebar tile and the titlebar chip wear one colour per session", () => {
  const cases = [
    {
      label: "user tag set",
      color: "amber",
      autoColor: "var(--color-mark-crimson)",
      background: "background:var(--color-mark-gold)",
    },
    {
      label: "auto tint only",
      color: null,
      autoColor: "var(--color-mark-crimson)",
      background: "background:var(--color-mark-crimson)",
    },
    { label: "neither set", color: null, autoColor: null, background: null },
  ] as const;
  const styleOf = (openTag: string): string | null => / style="([^"]*)"/.exec(openTag)?.[1] ?? null;
  for (const { label, color, autoColor, background } of cases) {
    const chip = /<span data-terminal-provider[^>]*>/.exec(
      terminalIdentityHtml({ provider: "claude", display_name: "", color }, autoColor),
    )?.[0] ?? "";
    const tile = sessionTileHtml("claude", sessionTileColorStyle(color, autoColor));
    assert.equal(styleOf(chip), styleOf(tile), `${label}: both surfaces resolve the same colour`);
    const expected = background ?? "no override, so the provider hue shows";
    assert.equal(styleOf(chip), background, `${label}: the chip carries ${expected}`);
    assert.equal(styleOf(tile), background, `${label}: the sidebar tile carries ${expected}`);
  }
});
