// User-tunable terminal font size: persisted in
// localStorage, applied at mount and on change. xterm's own default
// (15px) shouts over the chrome's 13px body text, so the default here
// matches the chrome; the user can step it within a sane range.
const KEY = "noisedeck-terminal-font-size";

export const DEFAULT_TERMINAL_FONT_SIZE = 13;
export const MIN_TERMINAL_FONT_SIZE = 10;
export const MAX_TERMINAL_FONT_SIZE = 20;

export function terminalFontSize(): number {
  const raw = Number(localStorage.getItem(KEY));
  if (Number.isInteger(raw) && raw >= MIN_TERMINAL_FONT_SIZE && raw <= MAX_TERMINAL_FONT_SIZE) {
    return raw;
  }
  return DEFAULT_TERMINAL_FONT_SIZE;
}

// setTerminalFontSize clamps and persists, returning the applied value so
// the UI can label what actually happened.
export function setTerminalFontSize(size: number): number {
  const clamped = Math.min(
    MAX_TERMINAL_FONT_SIZE,
    Math.max(MIN_TERMINAL_FONT_SIZE, Math.round(size)),
  );
  localStorage.setItem(KEY, String(clamped));
  return clamped;
}
