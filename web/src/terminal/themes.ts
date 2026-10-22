// User-selectable terminal palettes. The four dark themes
// retain the design's [data-term-theme="…"] helmet-CSS variable values
// verbatim (values only — the markup is not reproduced). Everforest Light and
// One Light use the design side's retuned sets instead; they are expected to
// differ from the imported design, and their correctness is
// held by the measured contrast floors in themes.test.ts. Licensing is fine:
// the community schemes are MIT/Apache and palettes aren't copyrightable.
//
// Each theme defines eleven slots; they map onto xterm's ITheme as:
//   --t-bg       -> background
//   --t-fg       -> foreground, brightWhite
//   --t-dim      -> white
//   --t-faint    -> brightBlack
//   --t-accent   -> cursor (each theme's signature hue)
//   --t-input-bg -> selectionBackground
//   --t-warn     -> yellow, brightYellow
//   --t-magenta  -> magenta, brightMagenta
//   --t-cyan     -> cyan, brightCyan
// (--t-rule and --t-panel-border are chrome hairlines, not ANSI slots.)
// The design defines no ANSI red/green/blue/black: for the dark named community
// schemes those come from the scheme's own published palette so SGR text
// stays legible on the theme's background. The light themes use their
// retuned ANSI slots, which themes.test.ts holds to their floors;
// Noisefloor and Custom leave undefined slots to xterm's defaults, except
// Noisefloor's black (#26374D, the workspace line-200 — pure default black
// would vanish on #0A1522).
import type { ITheme } from "@xterm/xterm";

export type TerminalThemeName =
  | "noisefloor"
  | "gruvbox"
  | "tokyonight"
  | "nord"
  | "everforest"
  | "onelight"
  | "custom";

export const DEFAULT_TERMINAL_THEME: TerminalThemeName = "noisefloor";

export const TERMINAL_THEME_NAMES: readonly TerminalThemeName[] = [
  "noisefloor",
  "gruvbox",
  "tokyonight",
  "nord",
  "everforest",
  "onelight",
  "custom",
];

export const TERMINAL_THEME_LABELS: Record<TerminalThemeName, string> = {
  noisefloor: "Noisefloor",
  gruvbox: "Gruvbox",
  tokyonight: "Tokyo Night",
  nord: "Nord",
  everforest: "Everforest Light",
  onelight: "One Light",
  custom: "Custom",
};

export const TERMINAL_THEMES: Record<TerminalThemeName, ITheme> = {
  // noisefloor — the design's default .wsterm set; must stay identical to
  // app/theme.ts terminalTheme("dark") (test-guarded).
  noisefloor: {
    background: "#0A1522",
    foreground: "#E4E1D8",
    cursor: "#E0764E",
    selectionBackground: "#353A45",
    black: "#26374D",
    brightBlack: "#5E6878",
    white: "#8A93A3",
    brightWhite: "#E4E1D8",
    yellow: "#D7D74A",
    brightYellow: "#D7D74A",
    magenta: "#E255A1",
    brightMagenta: "#E255A1",
    cyan: "#56C8D8",
    brightCyan: "#56C8D8",
  },
  // gruvbox — design set + morhetz/gruvbox (MIT) ANSI red/green/blue/black.
  gruvbox: {
    background: "#282828",
    foreground: "#EBDBB2",
    cursor: "#FE8019",
    selectionBackground: "#3C3836",
    black: "#282828",
    red: "#CC241D",
    green: "#98971A",
    yellow: "#FABD2F",
    blue: "#458588",
    magenta: "#D3869B",
    cyan: "#8EC07C",
    white: "#A89984",
    brightBlack: "#7C6F64",
    brightRed: "#FB4934",
    brightGreen: "#B8BB26",
    brightYellow: "#FABD2F",
    brightBlue: "#83A598",
    brightMagenta: "#D3869B",
    brightCyan: "#8EC07C",
    brightWhite: "#EBDBB2",
  },
  // tokyonight — design set + folke/tokyonight.nvim (Apache-2.0) ANSI slots.
  tokyonight: {
    background: "#1A1B26",
    foreground: "#C0CAF5",
    cursor: "#FF9E64",
    selectionBackground: "#24283B",
    black: "#15161E",
    red: "#F7768E",
    green: "#9ECE6A",
    yellow: "#E0AF68",
    blue: "#7AA2F7",
    magenta: "#BB9AF7",
    cyan: "#7DCFFF",
    white: "#787C99",
    brightBlack: "#565F89",
    brightRed: "#F7768E",
    brightGreen: "#9ECE6A",
    brightYellow: "#E0AF68",
    brightBlue: "#7AA2F7",
    brightMagenta: "#BB9AF7",
    brightCyan: "#7DCFFF",
    brightWhite: "#C0CAF5",
  },
  // nord — design set + arcticicestudio/nord (MIT) ANSI slots.
  nord: {
    background: "#2E3440",
    foreground: "#D8DEE9",
    cursor: "#D08770",
    selectionBackground: "#3B4252",
    black: "#3B4252",
    red: "#BF616A",
    green: "#A3BE8C",
    yellow: "#EBCB8B",
    blue: "#81A1C1",
    magenta: "#B48EAD",
    cyan: "#88C0D0",
    white: "#9AA5B8",
    brightBlack: "#616E88",
    brightRed: "#BF616A",
    brightGreen: "#A3BE8C",
    brightYellow: "#EBCB8B",
    brightBlue: "#81A1C1",
    brightMagenta: "#B48EAD",
    brightCyan: "#88C0D0",
    brightWhite: "#D8DEE9",
  },
  // everforest — the design side's retuned Everforest Light set; it is
  // intentionally different from the imported design and is held by
  // the contrast floors in themes.test.ts.
  everforest: {
    background: "#FDF6E3",
    foreground: "#48565D",
    cursor: "#DD6900",
    selectionBackground: "#EFEBD4",
    black: "#48565D",
    red: "#D43034",
    green: "#697800",
    yellow: "#946900",
    blue: "#1478A8",
    magenta: "#B74496",
    cyan: "#00815A",
    white: "#667465",
    brightBlack: "#879182",
    brightRed: "#D43034",
    brightGreen: "#697800",
    brightYellow: "#946900",
    brightBlue: "#1478A8",
    brightMagenta: "#B74496",
    brightCyan: "#00815A",
    brightWhite: "#48565D",
  },
  // onelight — the design side's retuned One Light set; it is intentionally
  // different from the imported design and is held by the contrast
  // floors in themes.test.ts.
  onelight: {
    background: "#FAFAFA",
    foreground: "#383A42",
    cursor: "#9B6900",
    selectionBackground: "#F0F0F1",
    black: "#383A42",
    red: "#CD4136",
    green: "#318332",
    yellow: "#986801",
    blue: "#356CE5",
    magenta: "#A626A4",
    cyan: "#007AAF",
    white: "#696C77",
    brightBlack: "#909096",
    brightRed: "#CD4136",
    brightGreen: "#318332",
    brightYellow: "#986801",
    brightBlue: "#356CE5",
    brightMagenta: "#A626A4",
    brightCyan: "#007AAF",
    brightWhite: "#383A42",
  },
  // custom — the design's custom-set starting values. The user's per-slot
  // overrides (terminal/theme.ts) are applied over this base at resolve
  // time; this entry is what "Custom" renders before any slot is edited.
  custom: {
    background: "#10141B",
    foreground: "#E4E1D8",
    cursor: "#E0764E",
    selectionBackground: "#353A45",
    brightBlack: "#5E6878",
    white: "#8A93A3",
    brightWhite: "#E4E1D8",
    yellow: "#D7D74A",
    brightYellow: "#D7D74A",
    magenta: "#E255A1",
    brightMagenta: "#E255A1",
    cyan: "#56C8D8",
    brightCyan: "#56C8D8",
  },
};

// The user-editable custom slots: the design's six rows.
export interface CustomThemeSlots {
  background: string;
  text: string;
  dimText: string;
  accent: string;
  warning: string;
  cyan: string;
}

export const CUSTOM_SLOT_LABELS: Record<keyof CustomThemeSlots, string> = {
  background: "Background",
  text: "Text",
  dimText: "Dim text",
  accent: "Accent",
  warning: "Warning",
  cyan: "Cyan",
};

// The design's custom-set values, reduced to the six editable slots.
export const DEFAULT_CUSTOM_SLOTS: CustomThemeSlots = {
  background: "#10141B",
  text: "#E4E1D8",
  dimText: "#8A93A3",
  accent: "#E0764E",
  warning: "#D7D74A",
  cyan: "#56C8D8",
};

// customTerminalTheme builds the Custom ITheme from the user's slots,
// over the design's custom base. Six slots cover the mapped roles: background,
// text (foreground/brightWhite), dim text (white), accent (cursor),
// warning (yellows), cyan (cyans); magenta, brightBlack (the design's
// faint), and the selection wash keep the base values.
export function customTerminalTheme(slots: CustomThemeSlots): ITheme {
  return {
    ...TERMINAL_THEMES.custom,
    background: slots.background,
    foreground: slots.text,
    brightWhite: slots.text,
    white: slots.dimText,
    cursor: slots.accent,
    yellow: slots.warning,
    brightYellow: slots.warning,
    cyan: slots.cyan,
    brightCyan: slots.cyan,
  };
}
