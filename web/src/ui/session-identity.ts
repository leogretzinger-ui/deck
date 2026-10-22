// How a session or provider wears its identity: the tile colour rule
// and the provider's display vocabulary. Colour data lives in
// lib/session-colors; these helpers turn it into markup attributes.
import { escapeHtml } from "../lib/escape.ts";
import { SESSION_COLOR_VARS } from "../lib/session-colors.ts";

// sessionTileColorStyle decides the session tile's inline background. An
// user-assigned session colour tag wins outright; otherwise an active
// auto-color project tint inherits at render time. When neither is set the
// provider hue class on the tile is left visible.
export function sessionTileColorStyle(
  sessionColor: string | null | undefined,
  autoColor: string | null | undefined,
): string {
  if (sessionColor && sessionColor in SESSION_COLOR_VARS) {
    return `style="background:${SESSION_COLOR_VARS[sessionColor]}"`;
  }
  if (autoColor) {
    return `style="background:${escapeHtml(autoColor)}"`;
  }
  return "";
}

// providerDisplayName maps a provider key to its user-visible label.
// Every provider renders as its lowercase id; only the shell utility
// provider is shown as "terminal". No provider is title-cased.
export function providerDisplayName(provider: string): string {
  if (provider === "shell") return "terminal";
  // The executable is command-code; the session is just "command". A
  // session name sits in a 190px row next to a colour tile, and the
  // "-code" half repeats what the tile already says.
  if (provider === "command-code") return "command";
  return provider;
}

// A mark answers "which one", never "what state": every harness has its own
// hue reserved for that provider, and none of them borrows a status token. Tiles
// carry colour and never letters, so the hue is the whole identity. The hex
// values live in app.css only.
const PROVIDER_MARK: Record<string, string> = {
  claude: "bg-provider-claude",
  codex: "bg-provider-codex",
  cursor: "bg-provider-cursor",
  "command-code": "bg-provider-command-code",
  cline: "bg-provider-cline",
  devin: "bg-provider-devin",
  grok: "bg-provider-grok",
  kimi: "bg-provider-kimi",
  kilo: "bg-provider-kilo",
  mimo: "bg-provider-mimo",
  omp: "bg-provider-omp",
  opencode: "bg-provider-opencode",
  pi: "bg-provider-pi",
  qoder: "bg-provider-qoder",
  qwen: "bg-provider-qwen",
  shell: "bg-provider-shell",
};

// A provider the palette has no hue for still renders on a neutral that
// reads in both themes: the tertiary text grey, never a surface, which in
// dark mode is black on black.
export const UNMARKED_PROVIDER_TILE = "bg-text-tertiary";

export function providerTileClass(provider: string): string {
  return PROVIDER_MARK[provider] ?? UNMARKED_PROVIDER_TILE;
}

// MARKED_PROVIDERS names the harnesses the palette reserves a hue for. It is
// not an inventory: which harnesses a running image actually has is the
// server's answer, and a provider absent from this table still renders — on
// the neutral above, visible in either theme. What the table does claim is that no two entries collide,
// which is a property of the palette and is asserted against this list.
export const MARKED_PROVIDERS: readonly string[] = Object.keys(PROVIDER_MARK);

export function sessionColorStyle(color: string | null | undefined): string {
  if (!color || !(color in SESSION_COLOR_VARS)) return "";
  return `style="background:${SESSION_COLOR_VARS[color]}"`;
}
