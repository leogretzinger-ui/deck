// User-tunable terminal color theme: the selected
// palette name is persisted in localStorage and resolved against the
// curated record, applied at mount and on change. An unknown stored value
// falls back to the default rather than rendering with a partial palette.
// The Custom theme's per-slot colors persist the same way (one JSON
// record) and are validated per slot — a malformed slot falls back to the
// design's starting value, never to a missing color.
import {
  DEFAULT_CUSTOM_SLOTS,
  DEFAULT_TERMINAL_THEME,
  TERMINAL_THEMES,
  customTerminalTheme,
  type CustomThemeSlots,
  type TerminalThemeName,
} from "./themes.ts";
import type { ITheme } from "@xterm/xterm";

const KEY = "noisedeck-terminal-theme";
const CUSTOM_KEY = "noisedeck-terminal-theme-custom";

function isTerminalThemeName(value: string): value is TerminalThemeName {
  return Object.prototype.hasOwnProperty.call(TERMINAL_THEMES, value);
}

export function terminalThemeName(): TerminalThemeName {
  const raw = localStorage.getItem(KEY);
  if (raw !== null && isTerminalThemeName(raw)) return raw;
  return DEFAULT_TERMINAL_THEME;
}

// setTerminalThemeName validates, persists, and returns the applied name so
// the UI can index exactly what took effect.
export function setTerminalThemeName(name: string): TerminalThemeName {
  const resolved = isTerminalThemeName(name) ? name : DEFAULT_TERMINAL_THEME;
  localStorage.setItem(KEY, resolved);
  return resolved;
}

const HEX = /^#[0-9a-f]{6}$/i;

// normalizeHex accepts #rgb and #rrggbb (case-insensitive) and returns
// uppercase #RRGGBB, or null for anything else.
export function normalizeHex(value: string): string | null {
  const raw = value.trim();
  if (HEX.test(raw)) return raw.toUpperCase();
  const short = /^#([0-9a-f])([0-9a-f])([0-9a-f])$/i.exec(raw);
  if (short !== null) {
    return `#${short[1]}${short[1]}${short[2]}${short[2]}${short[3]}${short[3]}`.toUpperCase();
  }
  return null;
}

// customThemeSlots reads the persisted custom palette; each slot falls
// back to the design's starting value independently.
export function customThemeSlots(): CustomThemeSlots {
  const slots = { ...DEFAULT_CUSTOM_SLOTS };
  const raw = localStorage.getItem(CUSTOM_KEY);
  if (raw === null) return slots;
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return slots;
  }
  if (typeof parsed !== "object" || parsed === null) return slots;
  for (const key of Object.keys(slots) as (keyof CustomThemeSlots)[]) {
    const value = (parsed as Record<string, unknown>)[key];
    const hex = typeof value === "string" ? normalizeHex(value) : null;
    if (hex !== null) slots[key] = hex;
  }
  return slots;
}

// setCustomThemeSlots validates each slot independently and persists the
// record in one write — the theme dialog's Apply. An invalid color leaves
// that slot at its stored value; the returned record is exactly what took
// effect.
export function setCustomThemeSlots(slots: CustomThemeSlots): CustomThemeSlots {
  const next = customThemeSlots();
  for (const key of Object.keys(next) as (keyof CustomThemeSlots)[]) {
    const hex = normalizeHex(slots[key]);
    if (hex !== null) next[key] = hex;
  }
  localStorage.setItem(CUSTOM_KEY, JSON.stringify(next));
  return next;
}

// resolveTerminalTheme is what the terminal mounts: the curated palette
// for a named theme, or the user's slots composed over the custom
// base.
export function resolveTerminalTheme(name: TerminalThemeName): ITheme {
  if (name === "custom") return customTerminalTheme(customThemeSlots());
  return TERMINAL_THEMES[name];
}
