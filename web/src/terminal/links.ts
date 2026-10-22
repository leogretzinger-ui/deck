// URLs in terminal output, reconstructed across the rows a TUI hard-wraps
// them over. Ink-rendered harnesses (Claude Code among them) wrap their own
// output at the PTY width with real line breaks and pad each row with spaces,
// so a sign-in URL longer than the window arrives as three or four separate
// rows that xterm does not know belong together: the stock link addon stops
// at the first row's end and opens a truncated URL, and a selection across
// the rows copies newlines into the middle of it. This provider joins such
// rows before matching, and the same rule fixes the copied text.
//
// Presentational only: nothing here feeds identity, input, or any
// authoritative state; activation opens a browsing context and that is all.
import type { IBufferLine, ILink, ILinkProvider, Terminal } from "@xterm/xterm";

const MAX_ROWS = 64;
const MAX_CHARS = 8192;

export interface Row {
  // The row's characters, one per cell for ordinary text; a wide character
  // contributes one character for its two cells and a combining sequence
  // several code units for one cell. Blank cells are spaces.
  text: string;
  // xterm's own soft wrap: the row continues the previous one verbatim.
  wrapped: boolean;
  // For every UTF-16 index of text, the first cell it occupies and the cell
  // after its last (a wide character spans two). String offsets are not
  // columns once the row holds a wide or combining character, which is why
  // the mapping is carried rather than assumed.
  cellStart: number[];
  cellEnd: number[];
  // Cells of the row: its width, and the cell after the last non-blank one.
  cols: number;
  inkEnd: number;
}

// rowFromText: a row of ordinary single-cell characters, for callers and
// tests that have only the string.
export function rowFromText(text: string, wrapped = false, cols = text.length): Row {
  return rowFromChars([...text].map((ch) => ({ ch, width: 1 })), wrapped, cols);
}

// rowFromChars: a row from its cells, each a character (possibly several
// code units) and the cells it occupies.
export function rowFromChars(chars: Array<{ ch: string; width: number }>, wrapped = false, cols?: number): Row {
  let text = "";
  const cellStart: number[] = [];
  const cellEnd: number[] = [];
  let x = 0;
  let inkEnd = 0;
  for (const { ch, width } of chars) {
    const w = Math.max(1, width);
    const chars16 = ch === "" ? " " : ch;
    for (let i = 0; i < chars16.length; i++) { cellStart.push(x); cellEnd.push(x + w); }
    text += chars16;
    if (chars16.trim() !== "") inkEnd = x + w;
    x += w;
  }
  return { text, wrapped, cellStart, cellEnd, cols: cols ?? x, inkEnd };
}

// rowFromLine reads a buffer row cell by cell. The trailing half of a wide
// character is a zero-width cell and is skipped; an empty cell is a space.
export function rowFromLine(line: IBufferLine, cols: number): Row {
  const chars: Array<{ ch: string; width: number }> = [];
  for (let x = 0; x < line.length; x++) {
    const cell = line.getCell(x);
    if (cell === undefined) break;
    const width = cell.getWidth();
    if (width === 0) continue;
    chars.push({ ch: cell.getChars(), width });
  }
  return rowFromChars(chars, line.isWrapped, cols);
}

// A row the application hard-wrapped fills the width (Ink breaks at the last
// column and pads the remainder), so a row whose ink reaches within a cell of
// the right edge and whose successor begins without a space is read as a
// continuation. This is a heuristic with a known ambiguity: a URL that ends
// exactly at the edge, followed by ordinary unindented prose, gains the
// prose's first word (matching stops at its first space), which is the same
// broken link the stock provider produced for every wrapped URL; a URL with
// a space in it is not a URL, so nothing longer is ever joined.
export function continuesInto(row: Row, next: Row | undefined): boolean {
  if (next === undefined) return false;
  if (next.wrapped) return true;
  if (row.inkEnd < row.cols - 1) return false;
  return next.text.length > 0 && !/^\s/.test(next.text);
}

// The URL grammar: a scheme in either case (the addon this replaces took
// HTTPS:// as readily as https://), then anything but whitespace and the
// quoting characters that terminals put around links, with trailing
// punctuation and an unbalanced closing bracket given back to the
// sentence. What survives must parse as a URL; that is the only length
// rule, so http://a is a link as it was before.
const URL_RE = /https?:\/\/[^\s"'<>`]+/gi;

function isUrl(candidate: string): boolean {
  try {
    const u = new URL(candidate);
    return u.protocol === "http:" || u.protocol === "https:";
  } catch {
    return false;
  }
}

export function trimUrlEnd(raw: string): string {
  let url = raw.replace(/[.,;:!?'"]+$/, "");
  for (const [open, close] of [["(", ")"], ["[", "]"], ["{", "}"]] as const) {
    while (url.endsWith(close) && (url.split(close).length - 1) > (url.split(open).length - 1)) url = url.slice(0, -1);
  }
  return url;
}

interface Segment { row: number; start: number; end: number; source: Row }

// joinRows builds the logical text of rows[first..] following the wrap
// rules, remembering which row each stretch came from so a match maps back
// to cells. Hard-wrapped rows contribute their ink only; soft wraps
// contribute the row verbatim, because their spaces are real.
function joinRows(rows: Row[], first: number): { text: string; segments: Segment[] } {
  let text = "";
  const segments: Segment[] = [];
  for (let i = first; i < rows.length && segments.length < MAX_ROWS; i++) {
    const row = rows[i]!;
    const next = rows[i + 1];
    const continues = continuesInto(row, next);
    const part = continues && !(next?.wrapped) ? row.text.trimEnd() : row.text;
    if (text.length + part.length > MAX_CHARS) break;
    segments.push({ row: i, start: text.length, end: text.length + part.length, source: row });
    text += part;
    if (!continues) break;
  }
  return { text, segments };
}

function toCell(segments: Segment[], index: number, side: "start" | "end"): { row: number; col: number } | null {
  for (const s of segments) {
    if (side === "start" && index >= s.start && index < s.end) return { row: s.row, col: s.source.cellStart[index - s.start]! };
    if (side === "end" && index > s.start && index <= s.end) return { row: s.row, col: s.source.cellEnd[index - s.start - 1]! };
  }
  return null;
}

export interface UrlMatch {
  text: string;
  // Zero-based row indexes into the rows array and zero-based cell columns,
  // end exclusive.
  startRow: number;
  startCol: number;
  endRow: number;
  endCol: number;
}

// findUrls returns every URL whose logical line begins at a row that is not
// itself a continuation, over all the rows given.
export function findUrls(rows: Row[]): UrlMatch[] {
  const out: UrlMatch[] = [];
  let i = 0;
  while (i < rows.length) {
    const { text, segments } = joinRows(rows, i);
    if (segments.length === 0) { i += 1; continue; }
    for (const m of text.matchAll(URL_RE)) {
      const url = trimUrlEnd(m[0]);
      if (!isUrl(url)) continue;
      const start = toCell(segments, m.index!, "start");
      const end = toCell(segments, m.index! + url.length, "end");
      if (start === null || end === null) continue;
      out.push({ text: url, startRow: start.row, startCol: start.col, endRow: end.row, endCol: end.col });
    }
    i = segments[segments.length - 1]!.row + 1;
  }
  return out;
}

// joinWrappedSelection: the copied form of a selection that is exactly one
// URL the application broke over rows, and nothing else: every row must
// continue into the next by the hard-wrap rule, and the joined text must be
// the URL entire — no trailing punctuation, quote or suffix that the repair
// would have to drop, since the native copy is suppressed in its favour and
// selected bytes must never be lost. Returns null for anything else. The
// selection text carries no cell widths, so the seam rule counts code units
// here: a row holding wide characters reads as shorter than the width and
// is not joined, which fails toward the native copy.
export function joinWrappedSelection(selection: string, cols: number): string | null {
  const lines = selection.split(/\r?\n/);
  if (lines.length < 2) return null;
  const rows: Row[] = lines.map((text) => rowFromText(text, false, cols));
  for (let i = 0; i < rows.length - 1; i++) {
    if (!continuesInto(rows[i]!, rows[i + 1])) return null;
  }
  const joined = rows.map((r) => r.text.trimEnd()).join("").trim();
  if (/\s/.test(joined)) return null;
  const m = URL_RE.exec(joined);
  URL_RE.lastIndex = 0;
  if (m === null || m.index !== 0 || m[0].length !== joined.length) return null;
  if (trimUrlEnd(m[0]) !== m[0] || !isUrl(m[0])) return null;
  return m[0];
}

// The provider. xterm asks per buffer row (1-based); the links returned are
// those intersecting that row, with ranges in xterm's 1-based inclusive cells.
export class WrappedUrlLinkProvider implements ILinkProvider {
  private readonly terminal: Terminal;
  private readonly open: (url: string) => void;

  constructor(terminal: Terminal, open: (url: string) => void) {
    this.terminal = terminal;
    this.open = open;
  }

  provideLinks(bufferLineNumber: number, callback: (links: ILink[] | undefined) => void): void {
    const buffer = this.terminal.buffer.active;
    const cols = this.terminal.cols;
    const target = bufferLineNumber - 1;
    const row = (i: number): Row | undefined => {
      const line = buffer.getLine(i);
      return line === undefined ? undefined : rowFromLine(line, cols);
    };
    // Walk up to the row this one continues from, then collect forward
    // while rows keep continuing.
    let first = target;
    let cur = row(first);
    if (cur === undefined) { callback(undefined); return; }
    for (let n = 0; n < MAX_ROWS && first > 0; n++) {
      const prev = row(first - 1);
      if (prev === undefined || !continuesInto(prev, cur)) break;
      first -= 1;
      cur = prev;
    }
    const rows: Row[] = [];
    for (let i = first; i < first + MAX_ROWS * 2; i++) {
      const r = row(i);
      if (r === undefined) break;
      rows.push(r);
      if (i >= target) {
        const next = row(i + 1);
        if (next === undefined || !continuesInto(r, next)) break;
      }
    }
    const links: ILink[] = [];
    for (const u of findUrls(rows)) {
      const startRow = first + u.startRow, endRow = first + u.endRow;
      if (target < startRow || target > endRow) continue;
      links.push({
        range: { start: { x: u.startCol + 1, y: startRow + 1 }, end: { x: u.endCol, y: endRow + 1 } },
        text: u.text,
        decorations: { pointerCursor: true, underline: true },
        activate: (_event, text) => this.open(text),
      });
    }
    callback(links.length > 0 ? links : undefined);
  }
}
