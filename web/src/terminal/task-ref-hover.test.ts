import assert from "node:assert/strict";
import test from "node:test";
import type { ILink, ILinkProvider, Terminal } from "@xterm/xterm";

import { TaskRefHover, type HoverTimer } from "./task-ref-hover.ts";
import type { TmTask } from "../api/tasks.ts";

interface FakeCell {
  getChars(): string;
  getWidth(): number;
}

class FakeLine {
  private cells: Array<{ chars: string; width: number }> = [];
  private text: string;
  readonly isWrapped: boolean;

  constructor(text: string, isWrapped = false) {
    this.text = text;
    this.isWrapped = isWrapped;
    for (const char of text) {
      const width = char === "界" || char === "🎯" ? 2 : 1;
      this.cells.push({ chars: char, width });
      if (width === 2) this.cells.push({ chars: "", width: 0 });
    }
  }

  get length(): number { return this.cells.length; }
  getCell(x: number): FakeCell | undefined {
    const cell = this.cells[x];
    if (cell === undefined) return undefined;
    return {
      getChars: () => cell.chars,
      getWidth: () => cell.width,
    };
  }
  translateToString(): string { return this.text; }
}

class FakeTerminal {
  provider: ILinkProvider | null = null;
  scrollListeners: Array<() => void> = [];
  resizeListeners: Array<() => void> = [];
  lines = new Map<number, string>();
  wrappedLines = new Set<number>();
  lineRequests: number[] = [];
  registered = false;
  linkProviderDisposed = false;

  registerLinkProvider(provider: ILinkProvider): { dispose(): void } {
    this.provider = provider;
    this.registered = true;
    return { dispose: () => { this.linkProviderDisposed = true; } };
  }
  onScroll(fn: () => void): { dispose(): void } {
    this.scrollListeners.push(fn);
    return { dispose: () => { this.scrollListeners = this.scrollListeners.filter((l) => l !== fn); } };
  }
  onResize(fn: () => void): { dispose(): void } {
    this.resizeListeners.push(fn);
    return { dispose: () => { this.resizeListeners = this.resizeListeners.filter((l) => l !== fn); } };
  }
  emitScroll(): void { for (const l of this.scrollListeners) l(); }
  emitResize(): void { for (const l of this.resizeListeners) l(); }

  readonly buffer = {
    active: {
      getLine: (y: number): FakeLine | undefined => {
        this.lineRequests.push(y);
        const text = this.lines.get(y);
        return text === undefined ? undefined : new FakeLine(text, this.wrappedLines.has(y));
      },
    },
  };

  asTerminal(): Terminal {
    return this as unknown as Terminal;
  }
}

interface FakePopover {
  style: { left: string; top: string };
  innerHTML: string;
  className: string;
  offsetWidth: number;
  offsetHeight: number;
  attributes: Record<string, string>;
  removed: boolean;
  setAttribute(name: string, value: string): void;
  addEventListener(name: string, listener: (event: unknown) => void): void;
  querySelectorAll(): HTMLElement[];
  remove(): void;
}

class FakeContainer {
  children: FakePopover[] = [];
  rect = { left: 0, top: 0, width: 800, height: 400 };
  appendChild(el: FakePopover): void { this.children.push(el); }
  getBoundingClientRect(): { left: number; top: number; width: number; height: number } {
    return this.rect;
  }
  asContainer(): HTMLElement {
    return this as unknown as HTMLElement;
  }
  top(): FakePopover | null {
    return this.children.length > 0 ? this.children[this.children.length - 1]! : null;
  }
}

function fakeTimer(): HoverTimer & { fire(): boolean; pending(): number } {
  let next = 1;
  const jobs = new Map<number, () => void>();
  return {
    set(fn) { const id = next++; jobs.set(id, fn); return id; },
    clear(id) { jobs.delete(id); },
    pending: () => jobs.size,
    fire() {
      const first = jobs.entries().next();
      if (first.done) return false;
      const [id, fn] = first.value;
      jobs.delete(id);
      fn();
      return true;
    },
  };
}

const sampleTags: [string, TmTask[]][] = [
  ["master", [
    { id: "126", title: "hover card task", status: "in-progress", priority: "high" },
    { id: "200", title: "another task", status: "pending" },
  ]],
];

function makeHover(opts: {
  term: FakeTerminal;
  container?: FakeContainer;
  tags?: [string, TmTask[]][];
  timer?: ReturnType<typeof fakeTimer>;
}): { hover: TaskRefHover; term: FakeTerminal; container: FakeContainer; timer: ReturnType<typeof fakeTimer>; created: FakePopover[] } {
  const container = opts.container ?? new FakeContainer();
  const timer = opts.timer ?? fakeTimer();
  const created: FakePopover[] = [];
  const tags = opts.tags ?? sampleTags;
  const hover = new TaskRefHover(opts.term.asTerminal(), container.asContainer(), {
    getTags: () => tags,
    timer,
    createPopover: () => {
      const el: FakePopover = {
        style: { left: "", top: "" },
        innerHTML: "",
        className: "",
        offsetWidth: 240,
        offsetHeight: 80,
        attributes: {},
        removed: false,
        setAttribute(name, value) { this.attributes[name] = value; },
        addEventListener() {},
        querySelectorAll() { return []; },
        remove() { this.removed = true; },
      };
      created.push(el);
      return el as unknown as HTMLElement;
    },
  });
  return { hover, term: opts.term, container, timer, created };
}

function linksFor(term: FakeTerminal, lineNumber: number): ILink[] | undefined {
  assert.ok(term.provider !== null, "link provider was not registered");
  let out: ILink[] | undefined;
  term.provider.provideLinks(lineNumber, (l) => { out = l; });
  return out;
}

test("registers exactly one link provider on the terminal", () => {
  const term = new FakeTerminal();
  const { hover } = makeHover({ term });
  assert.ok(term.registered);
  hover.dispose();
});

test("provideLinks returns a link for a resolvable reference on the line", () => {
  const term = new FakeTerminal();
  term.lines.set(2, "see Task 126 for the plan");
  const { hover } = makeHover({ term });
  const links = linksFor(term, 3);
  assert.ok(links !== undefined && links.length === 1, "exactly one link");
  const link = links![0]!;
  assert.equal(link.range.start.x, 5, "start column is 1-based ('see ' is 4 chars)");
  assert.equal(link.range.start.y, 3);
  assert.equal(link.range.end.x, 12, "end column is the inclusive final cell");
  assert.equal(link.range.end.y, 3);
  assert.equal(term.lineRequests[0], 2, "xterm's 1-based provider y maps to its 0-based buffer");
  assert.equal(link.decorations?.pointerCursor, false);
  assert.equal(link.decorations?.underline, true);
  hover.dispose();
});

test("link ranges use terminal cells when wide glyphs precede a reference", () => {
  const term = new FakeTerminal();
  term.lines.set(0, "界 Task 126");
  const { hover } = makeHover({ term });
  const link = linksFor(term, 1)?.[0];
  assert.ok(link !== undefined);
  assert.equal(link.range.start.x, 4);
  assert.equal(link.range.end.x, 11);
  hover.dispose();
});

test("a reference split by an xterm wrap maps to a multi-row range", () => {
  const term = new FakeTerminal();
  term.lines.set(0, "Task ");
  term.lines.set(1, "126");
  term.wrappedLines.add(1);
  const { hover } = makeHover({ term });
  for (const providerLine of [1, 2]) {
    const link = linksFor(term, providerLine)?.[0];
    assert.ok(link !== undefined);
    assert.deepEqual(link.range, {
      start: { x: 1, y: 1 },
      end: { x: 3, y: 2 },
    });
  }
  hover.dispose();
});

test("wide cells map correctly when a reference continues on a wrapped row", () => {
  const term = new FakeTerminal();
  term.lines.set(0, "🎯Task ");
  term.lines.set(1, "126");
  term.wrappedLines.add(1);
  const { hover } = makeHover({ term });
  const link = linksFor(term, 2)?.[0];
  assert.ok(link !== undefined);
  assert.deepEqual(link.range, {
    start: { x: 3, y: 1 },
    end: { x: 3, y: 2 },
  });
  hover.dispose();
});

test("hard-newline neighbors never form a cross-line reference", () => {
  const term = new FakeTerminal();
  term.lines.set(0, "Task ");
  term.lines.set(1, "126");
  const { hover } = makeHover({ term });
  assert.equal(linksFor(term, 1), undefined);
  assert.equal(linksFor(term, 2), undefined);
  hover.dispose();
});

test("the task-dash form linkifies too", () => {
  const term = new FakeTerminal();
  term.lines.set(0, "blocked by task-200 yes");
  const { hover } = makeHover({ term });
  const links = linksFor(term, 1);
  assert.ok(links !== undefined && links.length === 1);
  hover.dispose();
});

test("provideLinks returns undefined when no reference is present", () => {
  const term = new FakeTerminal();
  term.lines.set(0, "just prose, nothing here");
  const { hover } = makeHover({ term });
  assert.equal(linksFor(term, 1), undefined);
  hover.dispose();
});

test("an unknown id fails closed: no link is produced", () => {
  const term = new FakeTerminal();
  term.lines.set(0, "see Task 9999 nowhere");
  const { hover } = makeHover({ term });
  assert.equal(linksFor(term, 1), undefined);
  hover.dispose();
});

test("multiple resolvable references on a line each become a link", () => {
  const term = new FakeTerminal();
  term.lines.set(0, "Task 126 then task-200");
  const { hover } = makeHover({ term });
  const links = linksFor(term, 1);
  assert.equal(links?.length, 2);
  hover.dispose();
});

test("a line past the buffer end yields undefined, not a throw", () => {
  const term = new FakeTerminal();
  const { hover } = makeHover({ term });
  assert.equal(linksFor(term, 99), undefined);
  hover.dispose();
});

test("a bare '#126' is not linkified in the terminal", () => {
  const term = new FakeTerminal();
  term.lines.set(0, "fixed in #126");
  const { hover } = makeHover({ term });
  assert.equal(linksFor(term, 1), undefined);
  hover.dispose();
});

function hoverEvent(x: number, y: number): MouseEvent {
  return { clientX: x, clientY: y } as unknown as MouseEvent;
}

function hoverLink(link: ILink, x: number, y: number): void {
  link.hover?.(hoverEvent(x, y), link.text);
}
function leaveLink(link: ILink): void {
  link.leave?.(hoverEvent(0, 0), link.text);
}

test("hover is debounced: no popover until the timer fires", () => {
  const term = new FakeTerminal();
  term.lines.set(0, "Task 126");
  const { hover, timer, container } = makeHover({ term });
  const links = linksFor(term, 1)!;
  hoverLink(links[0]!, 100, 50);
  assert.equal(container.children.length, 0, "nothing shown before the debounce");
  assert.equal(timer.pending(), 1, "a show is scheduled");
  timer.fire();
  assert.equal(container.children.length, 1, "the popover is shown after the debounce");
  assert.equal(container.top()!.attributes["data-task-ref-popover"], "");
  assert.equal(container.top()!.attributes.role, "region");
  assert.equal(container.top()!.attributes["aria-label"], "Task 126 preview");
  hover.dispose();
});

test("the popover renders the resolved task's card content", () => {
  const term = new FakeTerminal();
  term.lines.set(0, "Task 126");
  const { hover, timer, container } = makeHover({ term });
  const link = linksFor(term, 1)![0]!;
  hoverLink(link, 100, 50);
  timer.fire();
  const html = container.top()!.innerHTML;
  assert.ok(html.includes("hover card task"), "the task title is rendered");
  assert.ok(html.includes("126"), "the task id is rendered");
  hover.dispose();
});

test("leave replaces a pending show with a bounded dismiss delay", () => {
  const term = new FakeTerminal();
  term.lines.set(0, "Task 126");
  const { hover, timer, container } = makeHover({ term });
  const link = linksFor(term, 1)![0]!;
  hoverLink(link, 100, 50);
  assert.equal(timer.pending(), 1);
  leaveLink(link);
  assert.equal(timer.pending(), 1, "the debounce became a dismiss delay");
  timer.fire();
  assert.equal(container.children.length, 0, "nothing is shown");
  hover.dispose();
});

test("leave removes an already-visible popover after the transfer delay", () => {
  const term = new FakeTerminal();
  term.lines.set(0, "Task 126");
  const { hover, timer, container } = makeHover({ term });
  const link = linksFor(term, 1)![0]!;
  hoverLink(link, 100, 50);
  timer.fire();
  const visible = container.top()!;
  leaveLink(link);
  assert.equal(visible.removed, false, "the preview stays available for pointer transfer");
  timer.fire();
  assert.equal(visible.removed, true, "the popover element was removed");
  hover.dispose();
});

test("activate is a no-op: clicking a reference does nothing", () => {
  const term = new FakeTerminal();
  term.lines.set(0, "Task 126");
  const { hover } = makeHover({ term });
  const link = linksFor(term, 1)![0]!;
  assert.equal(typeof link.activate, "function");
  assert.doesNotThrow(() => link.activate(hoverEvent(0, 0), "task-126"));
  hover.dispose();
});

test("terminal scroll dismisses a visible popover and cancels the debounce", () => {
  const term = new FakeTerminal();
  term.lines.set(0, "Task 126");
  const { hover, timer, container } = makeHover({ term });
  const link = linksFor(term, 1)![0]!;
  hoverLink(link, 100, 50);
  timer.fire();
  const visible = container.top()!;
  hoverLink(link, 110, 60);
  term.emitScroll();
  assert.equal(visible.removed, true, "the visible popover was removed");
  assert.equal(timer.pending(), 0, "the pending show was canceled");
  hover.dispose();
});

test("terminal resize/reflow dismisses a visible popover", () => {
  const term = new FakeTerminal();
  term.lines.set(0, "Task 126");
  const { hover, timer, container } = makeHover({ term });
  const link = linksFor(term, 1)![0]!;
  hoverLink(link, 100, 50);
  timer.fire();
  const visible = container.top()!;
  term.emitResize();
  assert.equal(visible.removed, true, "reflow dismisses the popover");
  hover.dispose();
});

test("new output (workspace dismiss) cancels a pending show and removes the popover", () => {
  const term = new FakeTerminal();
  term.lines.set(0, "Task 126");
  const { hover, timer, container } = makeHover({ term });
  const link = linksFor(term, 1)![0]!;
  hoverLink(link, 100, 50);
  timer.fire();
  const visible = container.top()!;
  hoverLink(link, 120, 70);
  hover.dismiss();
  assert.equal(visible.removed, true);
  assert.equal(timer.pending(), 0);
  hover.dispose();
});

test("dispose removes the popover, cancels the debounce, and drops the provider registration", () => {
  const term = new FakeTerminal();
  term.lines.set(0, "Task 126");
  const { hover, timer, container } = makeHover({ term });
  const link = linksFor(term, 1)![0]!;
  hoverLink(link, 100, 50);
  timer.fire();
  const visible = container.top()!;
  hover.dispose();
  assert.equal(visible.removed, true, "popover removed on dispose");
  assert.equal(term.linkProviderDisposed, true, "link provider disposed");
  assert.equal(timer.pending(), 0, "no pending work");
});

test("after dispose, provideLinks fails closed", () => {
  const term = new FakeTerminal();
  term.lines.set(0, "Task 126");
  const { hover } = makeHover({ term });
  hover.dispose();
  let out: ILink[] | undefined = ["sentinel" as unknown as ILink];
  term.provider!.provideLinks(1, (l) => { out = l; });
  assert.equal(out, undefined, "a disposed controller resolves no links");
});

test("a delayed debounce from the previous project cannot show its card", () => {
  const term = new FakeTerminal();
  term.lines.set(0, "Task 126");
  const { hover, timer, container } = makeHover({ term });
  const link = linksFor(term, 1)![0]!;
  hoverLink(link, 100, 50);
  assert.equal(timer.pending(), 1);
  hover.dispose();
  timer.fire();
  assert.equal(container.children.length, 0, "no popover from a disposed controller");
});

test("show re-resolves against the current cache and fails closed if the id left it", () => {
  const term = new FakeTerminal();
  term.lines.set(0, "Task 126");
  let tags: [string, TmTask[]][] = [["master", [{ id: "126", title: "was here", status: "pending" }]]];
  const container = new FakeContainer();
  const timer = fakeTimer();
  const created: FakePopover[] = [];
  const hover = new TaskRefHover(term.asTerminal(), container.asContainer(), {
    getTags: () => tags,
    timer,
    createPopover: () => {
      const el: FakePopover = {
        style: { left: "", top: "" }, innerHTML: "", className: "", offsetWidth: 240, offsetHeight: 80,
        attributes: {}, removed: false,
        setAttribute(name, value) { this.attributes[name] = value; },
        addEventListener() {},
        querySelectorAll() { return []; },
        remove() { this.removed = true; },
      };
      created.push(el);
      return el as unknown as HTMLElement;
    },
  });
  const link = linksFor(term, 1)![0]!;
  hoverLink(link, 100, 50);
  tags = [["master", [{ id: "200", title: "different", status: "pending" }]]];
  timer.fire();
  assert.equal(container.children.length, 0, "no card for an id that left the cache");
  hover.dispose();
});

test("a cache refresh that adds an id makes it resolvable on the next call", () => {
  const term = new FakeTerminal();
  term.lines.set(0, "Task 300");
  let tags: [string, TmTask[]][] = [["master", []]];
  const container = new FakeContainer();
  const timer = fakeTimer();
  const hover = new TaskRefHover(term.asTerminal(), container.asContainer(), {
    getTags: () => tags,
    timer,
    createPopover: () => {
      const el: FakePopover = {
        style: { left: "", top: "" }, innerHTML: "", className: "", offsetWidth: 240, offsetHeight: 80,
        attributes: {}, removed: false,
        setAttribute(name, value) { this.attributes[name] = value; },
        addEventListener() {},
        querySelectorAll() { return []; },
        remove() { this.removed = true; },
      };
      return el as unknown as HTMLElement;
    },
  });
  assert.equal(linksFor(term, 1), undefined, "unknown before refresh");
  tags = [["master", [{ id: "300", title: "newly cached", status: "pending" }]]];
  const links = linksFor(term, 1);
  assert.ok(links !== undefined && links.length === 1, "resolvable after refresh");
  hover.dispose();
});

test("a dependency-free popover remains a labelled preview region", () => {
  const term = new FakeTerminal();
  term.lines.set(0, "Task 126");
  const { hover, timer, container } = makeHover({ term });
  const link = linksFor(term, 1)![0]!;
  hoverLink(link, 100, 50);
  timer.fire();
  const popover = container.top()!;
  assert.ok(!popover.className.includes("pointer-events-none"), "dependency references can receive input");
  assert.equal(popover.attributes.role, "region");
  assert.ok(!popover.innerHTML.includes("tabindex"), "no focusable element inside the popover");
  hover.dispose();
});

test("the controller adds no keyboard listener to the terminal or container", () => {
  const term = new FakeTerminal();
  const { hover } = makeHover({ term });
  assert.equal(term.scrollListeners.length, 1);
  assert.equal(term.resizeListeners.length, 1);
  hover.dispose();
  assert.equal(term.scrollListeners.length, 0, "scroll subscription cleaned up");
  assert.equal(term.resizeListeners.length, 0, "resize subscription cleaned up");
});
