// Light mode is disabled: currentTheme is pinned dark and no control
// can select the retained light palette.
import type { ITheme } from "@xterm/xterm";

export type WorkspaceTheme = "dark" | "light";

export function currentTheme(): WorkspaceTheme {
  return "dark";
}

// The xterm palette per theme (the terminal follows the chrome; the
// reference's light mode is warm cream, its dark mode near-black navy).
// The ANSI set flips too: xterm's defaults are dark-tuned, so SGR 97
// (bright white) would be invisible on cream.
export function terminalTheme(theme: WorkspaceTheme): ITheme {
  return theme === "dark"
    ? {
        // The Noisefloor palette: the design's .wsterm
        // variable set, mapped as themes.ts documents. Must stay
        // identical to TERMINAL_THEMES.noisefloor (test-guarded).
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
      }
    : {
        background: "#EEECE8",
        foreground: "#251805",
        cursor: "#251805",
        selectionBackground: "#C4BFB4",
        black: "#251805",
        brightBlack: "#918472",
        white: "#B9B4AA",
        brightWhite: "#5A4A33",
        blue: "#1E4FD8",
        brightBlue: "#1A46BE",
        red: "#C22F2F",
        brightRed: "#A32626",
        green: "#1E7A46",
        brightGreen: "#166337",
        yellow: "#B0760B",
        brightYellow: "#916006",
        cyan: "#0E8A6B",
        brightCyan: "#0B7057",
        magenta: "#8A4FB8",
        brightMagenta: "#6F3D97",
      };
}
