import type { SessionDetail } from "../api/sessions.ts";
import { escapeHtml } from "../lib/escape.ts";
import {
  providerDisplayName,
  providerTileClass,
  sessionTileColorStyle,
} from "./session-identity.ts";

// The selected session's identity as the titlebar draws it: a colour tile
// and the session's name. The tile's background resolves through
// sessionTileColorStyle — the sidebar tile's own rule — with the project's
// derived tint passed in beside the session colour, so one session wears
// one colour wherever the app draws it. Only the colour resolution is
// shared: this tile is 22px on --radius-control-sm against the sidebar's
// 16px --radius-mark.
export function terminalIdentityHtml(
  detail: Pick<SessionDetail, "provider" | "display_name" | "color">,
  autoColor: string | null | undefined,
): string {
  return `<div data-terminal-identity class="flex min-w-0 flex-1 items-center gap-2.5 leading-tight">
        <span data-terminal-provider class="h-[22px] w-[22px] flex-none rounded-control-sm ${providerTileClass(detail.provider)}" ${sessionTileColorStyle(detail.color, autoColor)}></span>
        <span data-titlebar-title class="min-w-0 truncate text-body font-medium text-text-primary">${escapeHtml(detail.display_name || providerDisplayName(detail.provider))}</span>
      </div>`;
}

// The no-session titlebar identity: with nothing
// selected the header falls back to the PROJECT — the tile and name of
// the project in context, in the session identity's exact geometry, so
// the two states read as one family. The tile wears the project's
// effective tint and the neutral sunken surface when it wears none.
export function projectIdentityHtml(
  project: { name: string },
  tint: string | null | undefined,
): string {
  const background = tint ? ` style="background:${escapeHtml(tint)}"` : "";
  return `<div data-terminal-identity class="flex min-w-0 flex-1 items-center gap-2.5 leading-tight">
        <span data-terminal-provider class="h-[22px] w-[22px] flex-none rounded-control-sm bg-surface-sunken"${background}></span>
        <span data-titlebar-title class="min-w-0 truncate text-body font-medium text-text-primary">${escapeHtml(project.name)}</span>
      </div>`;
}
