// This link provider is presentational only: matches never feed identity,
// status, input, recovery, or any other authoritative state.
import type { IBufferLine, ILink, ILinkProvider, Terminal } from "@xterm/xterm";
import type { TmTask } from "../api/tasks.ts";
import {
  collectTaskIDs,
  findTaskRefs,
  resolveTask,
  type TaskRefMatch,
} from "../ui/task-ref.ts";
import {
  TASK_POPOVER_CLASS,
  taskCardPopoverHtml,
} from "../ui/task-card.ts";

const HOVER_DELAY_MS = 350;
const MAX_LOGICAL_LINE_CHARS = 4096;
const MAX_LOGICAL_LINE_ROWS = 128;

interface LogicalLineSegment {
  line: IBufferLine;
  lineIndex: number;
  start: number;
  end: number;
}

interface LogicalLine {
  text: string;
  segments: LogicalLineSegment[];
}

export interface HoverTimer {
  set(callback: () => void, delayMS: number): number;
  clear(id: number): void;
}

const browserTimer: HoverTimer = {
  set: (callback, delayMS) => window.setTimeout(callback, delayMS),
  clear: (id) => window.clearTimeout(id),
};

export interface TaskRefHoverOptions {
  // This reads the selected project's existing cache; link callbacks never fetch.
  getTags: () => [string, TmTask[]][];
  timer?: HoverTimer;
  createPopover?: () => HTMLElement;
}

export class TaskRefHover {
  private disposed = false;
  private hoverTimer: number | null = null;
  private popover: HTMLElement | null = null;
  private linkRegistration: { dispose(): void };
  private onScrollSub: { dispose(): void };
  private onResizeSub: { dispose(): void };
  private knownSet: Set<string> = new Set();
  private knownTags: [string, TmTask[]][] | null = null;
  private terminal: Terminal;
  private container: HTMLElement;
  private timer: HoverTimer;
  private createPopover: () => HTMLElement;
  private getTags: () => [string, TmTask[]][];

  constructor(
    terminal: Terminal,
    container: HTMLElement,
    opts: TaskRefHoverOptions,
  ) {
    this.terminal = terminal;
    this.container = container;
    this.timer = opts.timer ?? browserTimer;
    this.createPopover = opts.createPopover ?? (() => document.createElement("div"));
    this.getTags = opts.getTags;
    const provider: ILinkProvider = { provideLinks: (line, cb) => this.provideLinks(line, cb) };
    this.linkRegistration = terminal.registerLinkProvider(provider);
    this.onScrollSub = terminal.onScroll(() => this.dismiss());
    this.onResizeSub = terminal.onResize(() => this.dismiss());
  }

  dismiss(): void {
    if (this.disposed) return;
    this.cancelHover();
    this.removePopover();
  }

  dispose(): void {
    if (this.disposed) return;
    this.disposed = true;
    this.cancelHover();
    this.removePopover();
    this.linkRegistration.dispose();
    this.onScrollSub.dispose();
    this.onResizeSub.dispose();
  }

  private tags(): [string, TmTask[]][] {
    const current = this.getTags();
    if (current !== this.knownTags) {
      this.knownTags = current;
      this.knownSet = collectTaskIDs(current);
    }
    return current;
  }

  private provideLinks(
    bufferLineNumber: number,
    callback: (links: ILink[] | undefined) => void,
  ): void {
    if (this.disposed) {
      callback(undefined);
      return;
    }
    const logical = this.logicalLine(bufferLineNumber);
    if (logical === null) {
      callback(undefined);
      return;
    }
    const refs = findTaskRefs(logical.text);
    if (refs.length === 0) {
      callback(undefined);
      return;
    }
    this.tags();
    const links: ILink[] = [];
    for (const ref of refs) {
      if (!this.knownSet.has(ref.id)) continue;
      const link = this.makeLink(logical, ref);
      if (link !== null) links.push(link);
    }
    callback(links.length > 0 ? links : undefined);
  }

  private logicalLine(bufferLineNumber: number): LogicalLine | null {
    const buffer = this.terminal.buffer.active;
    let firstIndex = bufferLineNumber - 1;
    let first = buffer.getLine(firstIndex);
    if (first === undefined) return null;
    let rows = 1;
    while (first.isWrapped) {
      if (firstIndex === 0 || rows >= MAX_LOGICAL_LINE_ROWS) return null;
      firstIndex -= 1;
      first = buffer.getLine(firstIndex);
      if (first === undefined) return null;
      rows += 1;
    }

    const segments: LogicalLineSegment[] = [];
    let text = "";
    for (let lineIndex = firstIndex; segments.length < MAX_LOGICAL_LINE_ROWS; lineIndex += 1) {
      const line = buffer.getLine(lineIndex);
      if (line === undefined) break;
      const row = line.translateToString(false);
      if (text.length + row.length > MAX_LOGICAL_LINE_CHARS) return null;
      const start = text.length;
      text += row;
      segments.push({ line, lineIndex, start, end: text.length });
      const next = buffer.getLine(lineIndex + 1);
      if (next === undefined || !next.isWrapped) break;
    }
    if (segments.length === MAX_LOGICAL_LINE_ROWS) {
      const next = buffer.getLine(segments[segments.length - 1]!.lineIndex + 1);
      if (next?.isWrapped) return null;
    }
    return { text, segments };
  }

  private makeLink(logical: LogicalLine, ref: TaskRefMatch): ILink | null {
    const id = ref.id;
    const start = stringIndexToPosition(logical.segments, ref.start, "start");
    const end = stringIndexToPosition(logical.segments, ref.end, "end");
    if (start === null || end === null) return null;
    return {
      range: { start, end },
      text: `task-${id}`,
      decorations: { pointerCursor: false, underline: true },
      hover: (event) => this.scheduleShow(event, id),
      leave: () => this.scheduleDismiss(),
      activate: () => {},
      dispose: () => {},
    };
  }

  private scheduleShow(event: MouseEvent, id: string): void {
    if (this.disposed) return;
    this.cancelHover();
    const anchor = { clientX: event.clientX, clientY: event.clientY };
    this.hoverTimer = this.timer.set(() => {
      this.hoverTimer = null;
      this.show(anchor, id);
    }, HOVER_DELAY_MS);
  }

  private scheduleDismiss(): void {
    this.cancelHover();
    this.hoverTimer = this.timer.set(() => {
      this.hoverTimer = null;
      this.removePopover();
    }, 100);
  }

  private show(anchor: { clientX: number; clientY: number }, id: string): void {
    if (this.disposed) return;
    // The cache may refresh during the debounce, so stale ids fail closed here too.
    const task = resolveTask(id, this.tags());
    if (task === null) {
      this.removePopover();
      return;
    }
    this.removePopover();
    const popover = this.createPopover();
    popover.setAttribute("data-task-ref-popover", "");
    popover.setAttribute("role", "region");
    popover.setAttribute("aria-label", `Task ${id} preview`);
    popover.setAttribute("tabindex", "-1");
    popover.className = TASK_POPOVER_CLASS;
    popover.innerHTML = taskCardPopoverHtml(task, this.knownSet);
    this.container.appendChild(popover);
    this.popover = popover;
    popover.addEventListener("mouseenter", () => this.cancelHover());
    popover.addEventListener("mouseleave", () => this.scheduleDismiss());
    popover.addEventListener("focusin", () => this.cancelHover());
    popover.addEventListener("focusout", () => this.scheduleDismiss());
    for (const dependency of popover.querySelectorAll<HTMLElement>("[data-task-ref]")) {
      const dependencyID = dependency.dataset.taskRef ?? "";
      dependency.addEventListener("mouseenter", (event) => {
        const mouse = event as MouseEvent;
        this.scheduleShow(mouse, dependencyID);
      });
      dependency.addEventListener("focus", () => {
        const rect = dependency.getBoundingClientRect();
        this.cancelHover();
        this.show({ clientX: rect.left, clientY: rect.bottom }, dependencyID);
        this.popover?.focus({ preventScroll: true });
      });
      dependency.addEventListener("mouseleave", () => this.scheduleDismiss());
      dependency.addEventListener("blur", () => this.scheduleDismiss());
    }
    this.position(popover, anchor);
  }

  private position(popover: HTMLElement, anchor: { clientX: number; clientY: number }): void {
    const rect = this.container.getBoundingClientRect();
    const x = anchor.clientX - rect.left;
    const y = anchor.clientY - rect.top;
    const pw = popover.offsetWidth;
    const ph = popover.offsetHeight;
    let left = x + 12;
    if (left + pw > rect.width) left = Math.max(8, x - pw - 12);
    let top = y + 16;
    if (top + ph > rect.height) top = Math.max(8, y - ph - 8);
    popover.style.left = `${Math.round(left)}px`;
    popover.style.top = `${Math.round(top)}px`;
  }

  private cancelHover(): void {
    if (this.hoverTimer !== null) {
      this.timer.clear(this.hoverTimer);
      this.hoverTimer = null;
    }
  }

  private removePopover(): void {
    if (this.popover !== null) {
      this.popover.remove();
      this.popover = null;
    }
  }
}

function stringIndexToPosition(
  segments: LogicalLineSegment[],
  stringIndex: number,
  edge: "start" | "end",
): { x: number; y: number } | null {
  for (let i = 0; i < segments.length; i += 1) {
    const segment = segments[i]!;
    if (stringIndex < segment.start || stringIndex > segment.end) continue;
    if (edge === "start" && stringIndex === segment.end && i + 1 < segments.length) continue;
    const column = stringIndexToCell(segment.line, stringIndex - segment.start);
    if (column === null) return null;
    return {
      x: edge === "start" ? column + 1 : column,
      y: segment.lineIndex + 1,
    };
  }
  return null;
}

function stringIndexToCell(line: IBufferLine, stringIndex: number): number | null {
  if (stringIndex === 0) return 0;
  let remaining = stringIndex;
  for (let column = 0; column < line.length; column += 1) {
    const cell = line.getCell(column);
    if (cell === undefined || cell.getWidth() === 0) continue;
    remaining -= cell.getChars().length || 1;
    if (remaining === 0) return column + cell.getWidth();
    if (remaining < 0) return null;
  }
  return null;
}
