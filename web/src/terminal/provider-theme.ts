// Per-provider presentation palettes for terminals whose CLI letterboxes
// itself: it paints a card of its own colour and leaves the rest of the
// pane unpainted, so the terminal background shows around the card and
// reads as a frame of ours. For those providers the terminal surface
// beneath them is pinned to the provider's measured palette — the same
// category of provider presentation metadata as the zero-padding provider
// list in app.css, and never a rewrite of provider pixels: painted cells
// are transported untouched, and only the canvas the provider left blank
// takes the pinned colour.
//
// The whole palette is replaced, not just the background, and for a
// measured reason: MiMo prints its status line on the unpainted ground, so
// a background-only override under a light user theme would strand
// that text dark-on-dark, and light-tuned ANSI colours would land on a
// dark page. The provider's UI is dark whatever theme the user chose;
// its terminal surface follows the provider, everything else keeps the
// user's theme.
//
// MiMo 0.1.13, measured 2026-08-23 in a private tmux namespace past the
// auth wall (no prompt ever submitted): the card paints rgb(30,30,30) and
// the ground text carries the greys below; the accent and warning colours
// are MiMo's own brand orange and starfield gold. Cool slots (cyan,
// magenta) appear nowhere in MiMo's UI; they are tuned muted-and-legible
// for the rare content that prints them on this ground. Repin every value
// here if a measured MiMo release changes what it paints.
import type { ITheme } from "@xterm/xterm";

import type { TerminalChromePalette } from "./palette.ts";

interface ProviderPresentation {
  theme: ITheme;
  chrome: TerminalChromePalette;
}

const MIMO_PRESENTATION: ProviderPresentation = {
  // Measured: card ground #1E1E1E, primary text #EEEEEE, secondary
  // #A0A0A0, placeholder dim #808080, brand accent #FB8147, starfield
  // gold #E8DAB2.
  theme: {
    background: "#1E1E1E",
    foreground: "#EEEEEE",
    brightWhite: "#EEEEEE",
    white: "#A0A0A0",
    brightBlack: "#808080",
    cursor: "#FB8147",
    yellow: "#E8DAB2",
    brightYellow: "#E8DAB2",
    cyan: "#8FB8BF",
    brightCyan: "#8FB8BF",
    magenta: "#C99AC4",
    brightMagenta: "#C99AC4",
    // Every remaining ANSI slot is set too. An unset slot does not fall back
    // to the user's palette — xterm substitutes its own generic default,
    // which on this ground is the dark-on-dark this pin exists to prevent
    // (its red and blue clear only about 2.8:1 on #1E1E1E). MiMo's own UI
    // uses none of these; they carry rare ANSI content, so they are tuned to
    // the same muted register as the cyan and magenta above rather than to
    // MiMo brand colours it does not have.
    red: "#E38A78",
    brightRed: "#F0A092",
    green: "#9CC49A",
    brightGreen: "#B2D6B0",
    blue: "#93A9D8",
    brightBlue: "#AEC0E6",
    // ANSI black is a background slot, not a foreground one: it is the
    // darkest cell colour a provider can ask for, so it steps just off the
    // ground rather than clearing a foreground floor.
    black: "#2A2A2A",
    selectionBackground: "#343434",
  },
  // Hairlines step off the measured ground in neutral grey — a tinted rule
  // would read as a seam beside MiMo's neutral card. inputBg is the
  // selection wash, one step lighter than the ground.
  chrome: {
    bg: "#1E1E1E",
    fg: "#EEEEEE",
    dim: "#A0A0A0",
    faint: "#808080",
    rule: "#2A2A2A",
    accent: "#FB8147",
    panelBorder: "#3A3A3A",
    inputBg: "#343434",
    warn: "#E8DAB2",
    magenta: "#C99AC4",
    cyan: "#8FB8BF",
  },
};

// The one registry of letterboxing providers. A provider joins only with
// a measurement of what it paints and where its unpainted ground falls.
const PROVIDER_PRESENTATIONS: Readonly<Record<string, ProviderPresentation>> = {
  mimo: MIMO_PRESENTATION,
};

// terminalThemeFor resolves the emulator palette for one session: the
// provider's presentation palette when it has one, the user's theme
// otherwise.
export function terminalThemeFor(provider: string | undefined, base: ITheme): ITheme {
  const presentation = provider === undefined ? undefined : PROVIDER_PRESENTATIONS[provider];
  return presentation === undefined ? base : presentation.theme;
}

// terminalChromeFor resolves the --t-* chrome roles the pane paints with,
// under the same rule as terminalThemeFor.
export function terminalChromeFor(
  provider: string | undefined,
  base: TerminalChromePalette,
): TerminalChromePalette {
  const presentation = provider === undefined ? undefined : PROVIDER_PRESENTATIONS[provider];
  return presentation === undefined ? base : presentation.chrome;
}
