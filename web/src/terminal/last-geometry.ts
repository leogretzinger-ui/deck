// The last fitted terminal geometry on this screen. Creation sends it as a
// size hint so a new session's window is born at the width this user is
// actually viewing — tmux's 80x24 default lays every headless line out for
// a screen nobody has. A hint, not a truth: the attach that
// follows re-fits exactly, and the server ignores out-of-range values.
let last: { cols: number; rows: number } | null = null;

export function recordTerminalGeometry(cols: number, rows: number): void {
  if (Number.isInteger(cols) && Number.isInteger(rows) && cols > 0 && rows > 0) {
    last = { cols, rows };
  }
}

export function lastTerminalGeometry(): { cols: number; rows: number } | null {
  return last;
}
