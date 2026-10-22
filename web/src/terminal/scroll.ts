// Wheel and key routing for terminal scrollback.
//
// The attached tmux client runs in xterm's alternate screen, so xterm's own
// scrollback is empty while a session is attached — and full-screen TUIs
// would otherwise receive wheel events as arrow keys (codex cycles its input
// history). The buffer that CAN scroll is tmux's pane history, and tmux owns
// it (authority invariant): scrolling is therefore expressed as a scroll
// frame the server translates into tmux copy-mode view commands.
//
// The decisions here consult only the browser emulator's OWN state (which
// buffer xterm is in, whether the application requested mouse reports) —
// never provider output, which this product must not interpret.

export type ScrollDirection = "up" | "down";

export interface ViewportScrollCommand {
  direction: ScrollDirection;
  lines: number;
}

export type ScrollCommand =
  | ViewportScrollCommand
  | { direction: "live" };

export interface WheelState {
  // bufferType is xterm's active buffer. In the normal buffer xterm's own
  // viewport scrolls natively and nothing here interferes.
  bufferType: "normal" | "alternate";
  // mouseTracking is true when the application requested mouse reports; the
  // wheel then belongs to the application, not to scrollback.
  mouseTracking: boolean;
  // pageLines is the terminal's row count, the size of one page.
  pageLines: number;
}

export type WheelDecision =
  | { kind: "native" }
  | { kind: "consume" }
  | { kind: "scroll"; command: ViewportScrollCommand };

// One wheel notch is ~100 pixels in most browsers; 16 px/line lands near
// xterm's own cell-height conversion (~6 lines per notch).
const pixelsPerLine = 16;
// deltaMode values from the DOM WheelEvent spec.
const domDeltaLine = 1;
const domDeltaPage = 2;
// Each scroll command becomes one tmux invocation server-side, so trackpad
// event storms are coalesced: sub-line deltas accumulate, and commands are
// emitted at most once per interval with the accumulated line count. This
// coalescing is a courtesy for smoothness — the SERVER enforces its own
// rate limit on scroll frames and drops the excess (proxy.go).
const maxLinesPerCommand = 120;
const minCommandIntervalMs = 25;

const touchThresholdPx = 12;
const touchAxisRatio = 1.25;
const touchLongPressMs = 500;

export type TouchDecision =
  | { kind: "native" }
  | { kind: "consume" }
  | { kind: "scroll"; command: ViewportScrollCommand };

// tmux's bounded down command has no response telling the browser that the
// pane reached the bottom. An upward mobile drag therefore returns the
// alternate-screen pane directly to its live view; normal-buffer touch still
// moves incrementally through xterm's observable viewport.
export class AlternateTouchGesturePlanner {
  private liveSent = false;

  plan(command: ViewportScrollCommand): ScrollCommand | null {
    if (this.liveSent) return null;
    if (command.direction === "down") {
      this.liveSent = true;
      return { direction: "live" };
    }
    return command;
  }

  reset(): void {
    this.liveSent = false;
  }
}

// TouchScrollPlanner claims only a deliberate, predominantly vertical drag.
// Taps, long presses, horizontal page gestures, multi-touch, and gestures
// that overlap an xterm selection remain browser/xterm events. Once claimed,
// move events are converted into the same bounded line commands as wheel
// input, which also suppresses the synthetic click a browser would otherwise
// emit at the end of a touch drag.
export class TouchScrollPlanner {
  private startX = 0;
  private startY = 0;
  private lastY = 0;
  private startedAt = 0;
  private active = false;
  private claimed = false;
  private carry = 0;
  private lastEmit = Number.NEGATIVE_INFINITY;

  start(point: { clientX: number; clientY: number; timeStamp: number }): void {
    this.reset();
    this.startX = point.clientX;
    this.startY = point.clientY;
    this.lastY = point.clientY;
    this.startedAt = point.timeStamp;
    this.active = true;
  }

  move(
    point: { clientX: number; clientY: number; timeStamp: number },
    state: { selectionActive: boolean },
  ): TouchDecision {
    if (!this.active) return { kind: "native" };
    if (state.selectionActive || (!this.claimed && point.timeStamp - this.startedAt >= touchLongPressMs)) {
      this.reset();
      return { kind: "native" };
    }

    if (!this.claimed) {
      const x = Math.abs(point.clientX - this.startX);
      const y = Math.abs(point.clientY - this.startY);
      if (Math.max(x, y) < touchThresholdPx) return { kind: "native" };
      if (y < x * touchAxisRatio) {
        this.reset();
        return { kind: "native" };
      }
      this.claimed = true;
    }

    const deltaY = point.clientY - this.lastY;
    this.lastY = point.clientY;
    this.carry += deltaY / pixelsPerLine;
    this.carry = Math.max(-maxLinesPerCommand, Math.min(maxLinesPerCommand, this.carry));
    const whole = Math.floor(Math.abs(this.carry));
    if (whole < 1 || point.timeStamp - this.lastEmit < minCommandIntervalMs) {
      return { kind: "consume" };
    }
    // Touch follows native content motion: dragging the content downward
    // reveals older lines, while dragging it upward advances toward live.
    const direction: ScrollDirection = this.carry > 0 ? "up" : "down";
    this.carry -= Math.sign(this.carry) * whole;
    this.lastEmit = point.timeStamp;
    return { kind: "scroll", command: { direction, lines: whole } };
  }

  end(): boolean {
    const claimed = this.claimed;
    this.reset();
    return claimed;
  }

  reset(): void {
    this.active = false;
    this.claimed = false;
    this.carry = 0;
    this.lastEmit = Number.NEGATIVE_INFINITY;
  }
}

// WheelScrollPlanner turns a stream of wheel events into scroll commands.
// It is deliberately DOM-free: the caller passes the event's numbers and the
// emulator state, so the whole decision is unit-testable.
export class WheelScrollPlanner {
  private carry = 0;
  private lastEmit = Number.NEGATIVE_INFINITY;

  plan(
    event: { deltaY: number; deltaMode: number; timeStamp: number },
    state: WheelState,
  ): WheelDecision {
    if (state.mouseTracking || state.bufferType === "normal") {
      // Native handling is correct here; stale accumulation must not leak
      // into the next tmux-routed scroll.
      this.carry = 0;
      return { kind: "native" };
    }
    // Alternate screen without mouse reports: xterm would translate the
    // wheel into arrow keys, which is exactly the defect (codex cycling its
    // input history). The event is always consumed; a command is emitted
    // once at least one whole line has accumulated.
    this.carry += wheelLines(event.deltaY, event.deltaMode, state.pageLines);
    // The accumulator is bounded to what one command may carry. Without the
    // bound, one oversized delta (an OS page-scroll on a tall terminal, a
    // momentum fling) leaves a residue larger than any later event, and the
    // planner keeps emitting the ORIGINAL direction no matter which way the
    // user wheels next. Excess intent is dropped, not owed.
    this.carry = Math.max(-maxLinesPerCommand, Math.min(maxLinesPerCommand, this.carry));
    const whole = Math.floor(Math.abs(this.carry));
    if (whole < 1 || event.timeStamp - this.lastEmit < minCommandIntervalMs) {
      return { kind: "consume" };
    }
    const direction: ScrollDirection = this.carry > 0 ? "down" : "up";
    this.carry -= Math.sign(this.carry) * whole;
    this.lastEmit = event.timeStamp;
    return { kind: "scroll", command: { direction, lines: whole } };
  }
}

function wheelLines(deltaY: number, deltaMode: number, pageLines: number): number {
  switch (deltaMode) {
    case domDeltaLine:
      return deltaY;
    case domDeltaPage:
      return deltaY * pageLines;
    default:
      return deltaY / pixelsPerLine;
  }
}

// planScrollKey is the keyboard path, the equivalent of tmux prefix-[:
// Shift+PageUp enters copy-mode one page back, Shift+PageDown pages toward
// the bottom (leaving copy-mode there). Returns null when xterm should
// handle the key itself — in the normal buffer Shift+PageUp/PageDown are
// xterm's own native viewport scroll, and every other key is none of this
// module's business.
//
// While the view is open, navigation is the wheel and these Shift+Page
// keys. Any OTHER typed key deliberately exits the view and lands in the
// TUI — the server cancels copy-mode before writing user input, so
// typing is never silently swallowed by tmux (native terminals snap to the
// bottom on keystroke for the same reason).
//
// Unlike the wheel path, this one does NOT yield to mouseTracking: a key is
// not a mouse report, so claiming it takes nothing the application asked
// for — and for a mouse-aware TUI whose wheel events the application keeps,
// Shift+PageUp is the only scroll gesture left. The asymmetry is deliberate.
export function planScrollKey(
  event: {
    type: string;
    key: string;
    shiftKey: boolean;
    ctrlKey: boolean;
    altKey: boolean;
    metaKey: boolean;
  },
  state: { bufferType: "normal" | "alternate"; pageLines: number },
): ViewportScrollCommand | null {
  if (event.type !== "keydown") return null;
  if (!event.shiftKey || event.ctrlKey || event.altKey || event.metaKey) {
    return null;
  }
  if (state.bufferType === "normal") return null;
  const lines = Math.max(1, state.pageLines);
  if (event.key === "PageUp") return { direction: "up", lines };
  if (event.key === "PageDown") return { direction: "down", lines };
  return null;
}

// A full-screen application that requested mouse reports keeps its
// transcript in its own buffer: the tmux pane history is empty, so a
// copy-mode scroll has nowhere to go. The drag must become the same
// wheel reports the desktop's wheel produces, delivered to the
// application through the ordinary input stream.
export type AlternateTouchRoute = "copy-mode" | "mouse-reports";

export function alternateTouchRoute(mouseTracking: boolean): AlternateTouchRoute {
  return mouseTracking ? "mouse-reports" : "copy-mode";
}

// One wheel report moves a TUI a few lines (the application decides how
// many), so reports are emitted at a fraction of the planner's line rate;
// the divisor is calibrated by feel against Claude Code's transcript.
const linesPerWheelReport = 3;

// wheelReports encodes SGR-1006 wheel events (button 64 up, 65 down) at a
// 1-based cell position, one report per element: the outbound classifier
// recognizes a single well-formed report as an emulator reply, so each
// must travel alone. Only clamped integers reach the sequence.
export function wheelReports(
  command: ViewportScrollCommand,
  cell: { column: number; row: number },
  size: { columns: number; rows: number },
): string[] {
  const count = Math.max(1, Math.round(command.lines / linesPerWheelReport));
  const button = command.direction === "up" ? 64 : 65;
  const column = Math.min(Math.max(1, Math.floor(cell.column)), Math.max(1, size.columns));
  const row = Math.min(Math.max(1, Math.floor(cell.row)), Math.max(1, size.rows));
  return Array.from({ length: count }, () => `\x1b[<${button};${column};${row}M`);
}
