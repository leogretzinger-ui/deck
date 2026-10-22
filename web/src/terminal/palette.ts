// Chrome-role resolution for the active terminal theme. The
// emulator keeps its ANSI slots in themes.ts; these are the non-ANSI
// --t-* roles the reference pane paints with. They resolve on the terminal
// element only, so nothing outside the emulator reads them.
import type { ITheme } from "@xterm/xterm";
import {
  type CustomThemeSlots,
  type TerminalThemeName,
} from "./themes.ts";

export interface TerminalChromePalette {
  bg: string;
  fg: string;
  dim: string;
  faint: string;
  rule: string;
  accent: string;
  panelBorder: string;
  inputBg: string;
  warn: string;
  magenta: string;
  cyan: string;
}

// customThemeChromeSlots is the chrome-role mapping for the editable
// custom theme. Unlike the ANSI mapping in themes.ts it includes the two
// reference hairlines and selection wash, so a custom theme change repaints
// the whole pane rather than leaving the frame on the default.
export function customThemeChromeSlots(slots: CustomThemeSlots): TerminalChromePalette {
  return {
    bg: slots.background,
    fg: slots.text,
    dim: slots.dimText,
    accent: slots.accent,
    warn: slots.warning,
    cyan: slots.cyan,
    faint: "#5E6878",
    rule: "#2A313B",
    panelBorder: "#3A414D",
    inputBg: "#353A45",
    magenta: "#E255A1",
  };
}

// The two chrome hairlines the reference defines per theme and xterm's
// ITheme does not model. Values are the reference DC's [data-term-theme]
// sets verbatim.
const RULE: Record<TerminalThemeName, string> = {
  noisefloor: "#2A313B",
  gruvbox: "#3C3836",
  tokyonight: "#292E42",
  nord: "#3B4252",
  everforest: "#E6E2CC",
  onelight: "#E5E5E6",
  custom: "#2A313B",
};

const PANEL_BORDER: Record<TerminalThemeName, string> = {
  noisefloor: "#3A414D",
  gruvbox: "#504945",
  tokyonight: "#3B4261",
  nord: "#4C566A",
  everforest: "#BDC3AF",
  onelight: "#D0D0D1",
  custom: "#3A414D",
};

const TERMINAL_CHROME_VAR_NAMES: readonly [keyof TerminalChromePalette, string][] = [
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

function slot(theme: ITheme, key: keyof ITheme, fallback: string): string {
  const value = theme[key];
  return typeof value === "string" ? value : fallback;
}

export function terminalChromePalette(
  name: TerminalThemeName,
  theme: ITheme,
): TerminalChromePalette {
  return {
    bg: slot(theme, "background", "#0A1522"),
    fg: slot(theme, "foreground", "#E4E1D8"),
    dim: slot(theme, "white", "#8A93A3"),
    faint: slot(theme, "brightBlack", "#5E6878"),
    rule: RULE[name],
    accent: slot(theme, "cursor", "#E0764E"),
    panelBorder: PANEL_BORDER[name],
    inputBg: slot(theme, "selectionBackground", "#353A45"),
    warn: slot(theme, "yellow", "#D7D74A"),
    magenta: slot(theme, "magenta", "#E255A1"),
    cyan: slot(theme, "cyan", "#56C8D8"),
  };
}

// applyTerminalChromeVars writes the resolved --t-* locals onto the
// terminal element, which is the only place those locals may be read.
export function applyTerminalChromeVars(
  element: HTMLElement,
  palette: TerminalChromePalette,
): void {
  for (const [key, name] of TERMINAL_CHROME_VAR_NAMES) {
    element.style.setProperty(name, palette[key]);
  }
}
