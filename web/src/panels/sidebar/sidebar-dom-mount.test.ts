// Geometry tests for the sidebar row-menu mount path. The full Sidebar class
// needs a real browser, but the mountRowMenu positioning decision is small:
// resolve the row, insert the menu, then measure and place it in the next
// animation frame. These tests exercise that path with a minimal fake DOM so
// the regression (a project menu anchored to the <li> wrapper and a session
// menu placed before layout settled) is caught by `node --test`.
import assert from "node:assert/strict";
import test from "node:test";

import { Sidebar } from "./sidebar-dom.ts";
import { SidebarModel } from "./sidebar.ts";
import type { ProjectWithSessions } from "./sidebar.ts";
import { applyAutoColors } from "../../lib/auto-color.ts";
import type { ApiClient } from "../../api/contracts.ts";
import type { GitIdentityProfile, Project } from "../../api/projects.ts";
import type { SessionSummary } from "../../api/sessions.ts";
import type { SidebarLayout } from "../../api/settings.ts";

interface FakeRect {
  top: number;
  left: number;
  bottom: number;
  right: number;
  width: number;
  height: number;
  x: number;
  y: number;
  toJSON(): Record<string, number>;
}

interface FakeElement {
  tag: string;
  className: string;
  dataset: Record<string, string>;
  hidden: boolean;
  isConnected: boolean;
  style: Record<string, string>;
  offsetWidth: number;
  offsetHeight: number;
  innerHTML: string;
  getAttribute(name: string): string | null;
  setAttribute(name: string, value: string): void;
  removeAttribute(name: string): void;
  getBoundingClientRect(): FakeRect;
  closest(selector: string): FakeElement | null;
  querySelector(selector: string): FakeElement | null;
  querySelectorAll(selector: string): FakeElement[];
  addEventListener(type: string, fn: (ev: unknown) => void): void;
  removeEventListener(type: string, fn: (ev: unknown) => void): void;
  matches(selector: string): boolean;
  dispatchEvent(ev: { type: string; [key: string]: unknown }): void;
  focus(): void;
  append(child: FakeElement): void;
  replaceChildren(): void;
  remove(): void;
  classList: { contains(cls: string): boolean; add(cls: string): void; remove(cls: string): void };
}

function rect(top: number, left: number, bottom: number, right: number): FakeRect {
  return {
    top,
    left,
    bottom,
    right,
    width: right - left,
    height: bottom - top,
    x: left,
    y: top,
    toJSON() {
      return { top, left, bottom, right, width: this.width, height: this.height, x: this.x, y: this.y };
    },
  };
}

function fakeElement(overrides: Partial<FakeElement> = {}): FakeElement {
  const listeners = new Map<string, ((ev: unknown) => void)[]>();
  const attrs: Record<string, string> = {};
  const self: FakeElement = {
    tag: "div",
    className: "",
    dataset: {},
    hidden: false,
    isConnected: true,
    style: { top: "", left: "" },
    offsetWidth: 0,
    offsetHeight: 0,
    innerHTML: "",
    getAttribute(name) {
      return attrs[name] ?? null;
    },
    setAttribute(name, value) {
      attrs[name] = value;
    },
    removeAttribute(name) {
      delete attrs[name];
    },
    getBoundingClientRect() {
      return rect(0, 0, 0, 0);
    },
    closest() {
      return null;
    },
    querySelector() {
      return null;
    },
    querySelectorAll() {
      return [];
    },
    addEventListener(type, fn) {
      const list = listeners.get(type) ?? [];
      list.push(fn);
      listeners.set(type, list);
    },
    removeEventListener(type, fn) {
      const list = listeners.get(type);
      if (list === undefined) return;
      const index = list.indexOf(fn);
      if (index !== -1) list.splice(index, 1);
    },
    matches() {
      return false;
    },
    dispatchEvent(ev: { type: string; [key: string]: unknown }) {
      for (const fn of listeners.get(ev.type) ?? []) fn(ev);
    },
    focus() {},
    append() {},
    replaceChildren() {},
    remove() {},
    classList: {
      contains(cls: string): boolean {
        return self.className.split(/\s+/).includes(cls);
      },
      add(cls: string): void {
        if (!self.classList.contains(cls)) {
          self.className = self.className ? `${self.className} ${cls}` : cls;
        }
      },
      remove(cls: string): void {
        self.className = self.className.split(/\s+/).filter((c) => c !== cls && c !== "").join(" ");
      },
    },
    ...overrides,
  };
  return self;
}

function makeModel(): { model: ProjectWithSessions[]; session: SessionSummary } {
  const session: SessionSummary = {
    id: "s1",
    project_id: "p1",
    provider: "claude",
    display_name: "",
    status: "running",
    generation: 1,
    created_at: "t",
    updated_at: "t",
    recovery_capability: "none",
    recovery_state: "unavailable",
    presentation_state: "running",
    color: "amber",
  };
  const group: ProjectWithSessions = {
    project: { id: "p1", name: "alpha", canonical_path: "/srv/alpha", last_used_at: "t" },
    sessions: [session],
  };
  return { model: [group], session };
}

// The fake window and document record their listeners in these maps, so
// a test can count and fire what a mounted menu registered.
interface MenuListenerEnv {
  window: Map<string, ((ev: unknown) => void)[]>;
  document: Map<string, ((ev: unknown) => void)[]>;
  fireWindow(type: string, ev?: Record<string, unknown>): void;
  fireDocument(type: string, ev?: Record<string, unknown>): void;
}

interface Harness extends MenuListenerEnv {
  sidebar: Sidebar;
  container: FakeElement;
  popovers: FakeElement;
  sessionRow: FakeElement;
  sessionMetaSpan: FakeElement;
  projectRow: FakeElement;
  projectLi: FakeElement;
  projectsAnchor: FakeElement;
  raf: (() => void)[];
  lastMenu(): FakeElement;
}

function makeHarness(
  raf: (() => void)[],
  env: MenuListenerEnv,
  onProjectListRendered?: () => void,
): Harness {
  const menuWidth = 120;

  const modalsRegion = fakeElement({ tag: "div" });
  let backdrop: FakeElement | null = null;
  const popovers = fakeElement({
    tag: "div",
    querySelector(sel) {
      return sel === "[data-popover-backdrop]" ? backdrop : null;
    },
    replaceChildren() {
      backdrop = null;
    },
  });
  Object.defineProperty(popovers, "innerHTML", {
    get() {
      return (popovers as unknown as { _html: string })._html;
    },
    set(value: string) {
      (popovers as unknown as { _html: string })._html = value;
      const menu = fakeElement({ tag: "div", className: "menu absolute", offsetWidth: menuWidth });
      backdrop = fakeElement({
        tag: "div",
        dataset: { "popover-backdrop": "" },
        querySelector(sel) {
          return sel === "[role=menu]" ? menu : null;
        },
        querySelectorAll() {
          return [];
        },
      });
    },
  });

  const workspace = fakeElement({
    tag: "div",
    dataset: { workspace: "" },
    querySelector(sel) {
      if (sel === '[data-region="popovers"]') return popovers;
      if (sel === '[data-region="modals"]') return modalsRegion;
      return null;
    },
  });

  const sessionAnchor = fakeElement({
    tag: "button",
    dataset: { sessionMenu: "s1" },
    className: "session-menu-trigger",
  });
  const sessionStatusCell = fakeElement({
    tag: "span",
    dataset: { sessionStatus: "" },
  });
  const sessionRow = fakeElement({
    tag: "li",
    dataset: { session: "s1" },
    className: "session-row",
    getBoundingClientRect() {
      return rect(60, 20, 100, 250);
    },
    querySelector(sel) {
      return sel === "[data-session-status]" ? sessionStatusCell : null;
    },
  });
  // The span setStreaming and the age tick patch in place. Its innerHTML is
  // a plain property on the fake, so a test can see every write.
  const sessionMetaSpan = fakeElement({
    tag: "span",
    dataset: { sessionMeta: "s1" },
  });

  const projectAnchor = fakeElement({
    tag: "button",
    dataset: { projectMenu: "p1" },
    className: "project-menu-trigger",
  });
  // The PROJECTS header menu's trigger, sharing the row triggers' container.
  const projectsMenuAnchor = fakeElement({
    tag: "button",
    dataset: { projectsMenu: "" },
    getBoundingClientRect() {
      return rect(10, 16, 36, 90);
    },
  });
  const projectRow = fakeElement({
    tag: "div",
    className: "group/project relative flex items-center",
    getBoundingClientRect() {
      return rect(40, 20, 80, 240);
    },
  });
  const projectLi = fakeElement({
    tag: "li",
    dataset: { project: "p1", projectRow: "" },
    className: "project-group",
    getBoundingClientRect() {
      return rect(40, 20, 200, 240);
    },
    querySelector(sel) {
      return sel === '[class~="group/project"]' ? projectRow : null;
    },
  });

  const projectList = fakeElement({ tag: "ul", dataset: { projectList: "" } });
  const container = fakeElement({
    tag: "aside",
    dataset: { sidebar: "" },
    closest(sel) {
      return sel === "[data-workspace]" ? workspace : null;
    },
    querySelector(sel) {
      if (sel === '[data-session-menu="s1"]') return sessionAnchor;
      if (sel === '[data-session="s1"]') return sessionRow;
      if (sel === '[data-session-meta="s1"]') return sessionMetaSpan;
      if (sel === '[data-project-menu="p1"]') return projectAnchor;
      if (sel === '[data-project="p1"]') return projectLi;
      if (sel === '[data-project="p1"] [class~="group/project"]') return projectRow;
      if (sel === "[data-projects-menu]") return projectsMenuAnchor;
      if (sel === "[data-project-list]") return projectList;
      return null;
    },
    querySelectorAll() {
      return [];
    },
  });

  const { model } = makeModel();
  const sidebarModel = {
    api: {},
    groups: model,
    selectedSessionID: null,
    error: null,
    gitIdentities: null,
    creatingSession: false,
    applyProjectColors() {},
      } as unknown as SidebarModel;

  const sidebar = new Sidebar(
    container as unknown as HTMLElement,
    sidebarModel,
    onProjectListRendered,
  );
  return {
    ...env,
    sidebar,
    container,
    popovers,
    sessionRow,
    sessionMetaSpan,
    projectRow,
    projectLi,
    projectsAnchor: projectsMenuAnchor,
    raf,
    lastMenu() {
      const bd = popovers.querySelector("[data-popover-backdrop]");
      assert.notEqual(bd, null, "popover backdrop was not mounted");
      const menu = bd!.querySelector("[role=menu]");
      assert.notEqual(menu, null, "menu was not mounted");
      return menu!;
    },
  };
}

function withEnv(
  run: (harness: Harness) => void,
  onProjectListRendered?: () => void,
): void {
  const originalRAF = (globalThis as unknown as { requestAnimationFrame?: (cb: () => void) => number }).requestAnimationFrame;
  const originalWindow = (globalThis as unknown as { window?: unknown }).window;
  const originalDocument = (globalThis as unknown as { document?: unknown }).document;
  const originalNode = (globalThis as unknown as { Node?: unknown }).Node;
  const originalCSS = (globalThis as unknown as { CSS?: unknown }).CSS;

  const raf: (() => void)[] = [];
  (globalThis as unknown as { requestAnimationFrame: (cb: () => void) => number }).requestAnimationFrame = (cb) => {
    raf.push(cb);
    return 0;
  };
  const winReg = listenerRegistry();
  const docReg = listenerRegistry();
  const fire = (registry: Map<string, ((ev: unknown) => void)[]>, type: string, ev: Record<string, unknown>) => {
    for (const fn of registry.get(type) ?? []) fn(ev);
  };
  const env: MenuListenerEnv = {
    window: winReg,
    document: docReg,
    fireWindow: (type, ev = {}) => fire(winReg, type, ev),
    fireDocument: (type, ev = {}) => fire(docReg, type, ev),
  };
  (globalThis as unknown as { window: unknown }).window = {
    matchMedia: () => ({ matches: false }),
    setInterval: () => 0,
    clearInterval: () => {},
    addEventListener: winReg.addEventListener,
    removeEventListener: winReg.removeEventListener,
    innerHeight: 900,
  };
  (globalThis as unknown as { document: unknown }).document = {
    createElement: () => fakeElement({ tag: "div" }),
    addEventListener: docReg.addEventListener,
    removeEventListener: docReg.removeEventListener,
  };
  (globalThis as unknown as { CSS: unknown }).CSS = { escape: (s: string) => s };

  (globalThis as unknown as { Node: unknown }).Node = class {};

  const harness = makeHarness(raf, env, onProjectListRendered);

  try {
    run(harness);
  } finally {
    (globalThis as unknown as { requestAnimationFrame?: (cb: () => void) => number }).requestAnimationFrame = originalRAF;
    (globalThis as unknown as { window?: unknown }).window = originalWindow;
    (globalThis as unknown as { document?: unknown }).document = originalDocument;
    (globalThis as unknown as { CSS?: unknown }).CSS = originalCSS;
    (globalThis as unknown as { Node?: unknown }).Node = originalNode;
  }
}

test("a session row menu is positioned after layout settles, using the row rect", () => {
  withEnv(({ sidebar, sessionRow, raf }) => {
    sidebar.openSessionMenu("s1");
    const menu = (sidebar as unknown as { popoverRegion(): FakeElement | null }).popoverRegion()?.querySelector("[data-popover-backdrop]")?.querySelector("[role=menu]");
    assert.notEqual(menu, null, "menu should be mounted");

    // Positioning must not happen in the same tick as the insert.
    assert.equal(menu!.style.top, "", "top should not be set before the animation frame");
    assert.equal(menu!.style.left, "", "left should not be set before the animation frame");
    assert.equal(raf.length, 1, "positioning should be scheduled in requestAnimationFrame");

    raf[0]!();
    const rowRect = sessionRow.getBoundingClientRect();
    assert.equal(menu!.style.top, `${rowRect.bottom - 2}px`, "menu top should hang from the row bottom minus the 2px overlap");
    assert.equal(menu!.style.left, `${Math.max(8, rowRect.right - menu!.offsetWidth)}px`, "menu should be right-aligned to the row, clamped 8px from the left edge");
  });
});

// The popover region is fixed to the viewport, so a menu hanging below the
// last row in the sidebar is clipped by the bottom edge and its actions
// cannot be reached. The flip is decided from the row rect and the menu's
// own height, and only when both are real.
function placeSessionMenu(
  harness: Harness,
  menuHeight: number,
  viewportHeight: number,
): { top: string | undefined; rowRect: FakeRect } {
  harness.sidebar.openSessionMenu("s1");
  const menu = harness.lastMenu();
  menu.offsetHeight = menuHeight;
  (globalThis as unknown as { window: { innerHeight: number } }).window.innerHeight = viewportHeight;
  harness.raf[0]!();
  return { top: menu.style.top, rowRect: harness.sessionRow.getBoundingClientRect() };
}

test("a row menu whose height exceeds the space below opens upward", () => {
  withEnv((harness) => {
    const { top, rowRect } = placeSessionMenu(harness, 40, 100);
    assert.ok(rowRect.bottom + 40 > 100, "fixture: a downward menu would run past the viewport");
    assert.equal(
      top,
      `${rowRect.top - 40 + 2}px`,
      "an upward menu should put its bottom edge on the row top, keeping the 2px overlap",
    );
  });
});

test("a row menu with room below still opens downward", () => {
  withEnv((harness) => {
    const { top, rowRect } = placeSessionMenu(harness, 40, 900);
    assert.equal(top, `${rowRect.bottom - 2}px`, "a menu that fits below should not flip");
  });
});

// When neither edge holds the menu — a phone viewport under seven rows —
// it is pinned inside the viewport at the sidebar's 8px inset and scrolls
// within itself, so its last row is still reachable. Flipping into a space
// that is also too small would buy nothing.
test("a row menu that fits neither side is pinned inside the viewport and scrolls", () => {
  withEnv((harness) => {
    const { top, rowRect } = placeSessionMenu(harness, 200, 100);
    assert.ok(rowRect.top < 200, "fixture: the space above cannot hold the menu either");
    assert.equal(top, "8px", "a menu taller than the viewport starts at the top inset");
    const menu = harness.lastMenu();
    assert.equal(menu.style.maxHeight, "84px", "and is capped to the viewport minus both insets");
    assert.equal(menu.style.overflowY, "auto", "so its rows scroll rather than clip");
  });
  withEnv((harness) => {
    // Row 60–100 in a 150px viewport: 70px fits neither below (168) nor
    // above (-8), but does fit the viewport, so it is pinned to the bottom
    // inset; the cap is set but never binds.
    const { top, rowRect } = placeSessionMenu(harness, 70, 150);
    assert.ok(rowRect.bottom - 2 + 70 > 150 && rowRect.top + 2 - 70 < 0, "fixture: neither side holds the menu");
    assert.equal(top, "72px", "a menu that fits the viewport but neither side of the row is pinned to the bottom inset");
  });
});

test("a row menu whose last pixel lands on the viewport edge does not flip", () => {
  withEnv((harness) => {
    const rowRect = harness.sessionRow.getBoundingClientRect();
    // Exactly filling the space below is a fit: the overlap is part of the
    // menu's own extent, so ignoring it would flip this one for nothing.
    const viewport = 100;
    const { top } = placeSessionMenu(harness, viewport - (rowRect.bottom - 2), viewport);
    assert.equal(top, `${rowRect.bottom - 2}px`, "a menu that ends on the viewport edge still fits below");
  });
});

test("a row menu keeps its downward placement when the viewport height is unknown", () => {
  withEnv((harness) => {
    const { top, rowRect } = placeSessionMenu(harness, 40, 0);
    assert.equal(top, `${rowRect.bottom - 2}px`, "a zero viewport measures nothing and must not move the menu");
  });
});

// A flipped menu whose row sits past the bottom edge — the list scrolled
// after the menu opened, or the window shrank — overflows anyway, and a
// flipped placement is not exempt from the clamp. Likewise a row scrolled
// above the top edge must not drag its menu off the top.
test("a flipped row menu that still overflows the bottom is pinned inside the viewport", () => {
  withEnv((harness) => {
    // Row 900..940 in a 768px viewport: downward overflows, upward
    // (862 - 300 = 562 >= 0) qualifies for the flip, yet the flipped
    // bottom lands at 902 — past the edge. The clamp pins the menu to the
    // bottom inset.
    harness.sessionRow.getBoundingClientRect = () => rect(900, 20, 940, 250);
    const { top } = placeSessionMenu(harness, 300, 768);
    assert.equal(top, "460px", "a flipped menu that overflows anyway is pinned to the bottom inset");
    const menu = harness.lastMenu();
    assert.equal(menu.style.maxHeight, "752px", "the cap is set even where it does not bind");
  });
});

test("a row menu whose row scrolled above the viewport is pinned to the top inset", () => {
  withEnv((harness) => {
    // Row -40..0: downward placement puts the menu top at -2, off the top
    // edge where nothing can reach it.
    harness.sessionRow.getBoundingClientRect = () => rect(-40, 20, 0, 250);
    const { top } = placeSessionMenu(harness, 300, 768);
    assert.equal(top, "8px", "a menu above the viewport is pulled back to the top inset");
  });
});

test("a row menu keeps its downward placement when its own height is unknown", () => {
  withEnv((harness) => {
    // The DOM-free callers report a zero height; a viewport this short would
    // otherwise satisfy every part of the flip.
    const { top, rowRect } = placeSessionMenu(harness, 0, 50);
    assert.equal(top, `${rowRect.bottom - 2}px`, "a zero-height menu overflows nothing and must not move");
  });
});

test("a project row menu hangs from the inner project row, not the expanded <li>", () => {
  withEnv(({ sidebar, projectRow, projectLi, raf }) => {
    sidebar.openProjectMenu("p1");
    const menu = (sidebar as unknown as { popoverRegion(): FakeElement | null }).popoverRegion()?.querySelector("[data-popover-backdrop]")?.querySelector("[role=menu]");
    assert.notEqual(menu, null, "menu should be mounted");

    raf[0]!();
    const liRect = projectLi.getBoundingClientRect();
    const rowRect = projectRow.getBoundingClientRect();
    assert.notEqual(rowRect.bottom, liRect.bottom, "fixture: project row and wrapper have different bottoms");
    assert.equal(menu!.style.top, `${rowRect.bottom - 2}px`, "project menu top should use the inner row bottom, not the expanded <li>");
    assert.equal(menu!.style.left, `${Math.max(8, rowRect.right - menu!.offsetWidth)}px`, "project menu left should be right-aligned to the inner row");
  });
});

// Menu swaps on the shared popover region: each mount replaces the open
// menu's markup wholesale, and the replaced menu's listeners — window
// scroll/resize re-placement, the header menu's document keydown and
// focus net — must come down with that markup. A leaked set stays live
// behind every later menu and piles up with each switch.
test("opening the projects header menu over a row menu releases the row menu's window listeners", () => {
  withEnv(({ sidebar, window, fireWindow, lastMenu }) => {
    sidebar.openSessionMenu("s1");
    const rowMenu = lastMenu();
    // The registry mutates its arrays in place, so snapshot the functions.
    const rowScroll = [...(window.get("scroll") ?? [])];
    const rowResize = [...(window.get("resize") ?? [])];
    assert.equal(rowScroll.length, 1, "fixture: the open row menu holds the only scroll listener");
    assert.equal(rowResize.length, 1, "fixture: the open row menu holds the only resize listener");

    sidebar.openProjectsMenu();
    assert.notEqual(lastMenu(), rowMenu, "the header menu replaces the row menu's markup");
    const scroll = window.get("scroll") ?? [];
    const resize = window.get("resize") ?? [];
    assert.equal(scroll.length, 1, "one scroll listener remains: the header menu's own");
    assert.equal(resize.length, 1, "one resize listener remains: the header menu's own");
    assert.ok(!scroll.includes(rowScroll[0]!), "the row menu's scroll listener is released");
    assert.ok(!resize.includes(rowResize[0]!), "the row menu's resize listener is released");
    fireWindow("resize", {});
    assert.equal(rowMenu.style.top, "", "the replaced row menu is never re-placed");
  });
});

test("opening a row menu over the projects header menu releases its document listeners", () => {
  withEnv(({ sidebar, window, document, fireDocument, projectsAnchor, lastMenu }) => {
    sidebar.openProjectsMenu();
    const headerMenu = lastMenu();
    assert.equal(projectsAnchor.getAttribute("aria-expanded"), "true", "fixture: the header trigger is pressed");
    assert.equal((document.get("keydown") ?? []).length, 1, "fixture: the open header menu owns a document keydown handler");

    sidebar.openProjectMenu("p1");
    assert.notEqual(lastMenu(), headerMenu, "the row menu replaces the header menu's markup");
    assert.equal(projectsAnchor.getAttribute("aria-expanded"), "false", "the header trigger leaves the pressed state with its menu");
    assert.equal((document.get("keydown") ?? []).length, 0, "the header menu's keydown handler is released");
    assert.equal((document.get("focusin") ?? []).length, 0, "the header menu's focus net is released");
    assert.equal((window.get("scroll") ?? []).length, 1, "one scroll listener remains: the row menu's own");
    let claimed = false;
    fireDocument("keydown", { key: "Escape", preventDefault() { claimed = true; } });
    assert.ok(!claimed, "the dismissed header menu cannot claim Escape");
  });
});

test("a row menu that replaced the header menu survives the next render", () => {
  withEnv(({ sidebar, lastMenu }) => {
    sidebar.openProjectsMenu();
    sidebar.openProjectMenu("p1");
    const rowMenu = lastMenu();
    // A repaint reconciles an open header menu by closing it. A stale
    // projectsMenuAnchor still takes that path and unmounts the row menu
    // that replaced the header menu.
    const internals = sidebar as unknown as { model: { groups: { project: { name: string } }[] } };
    internals.model.groups[0]!.project.name = "beta";
    sidebar.render();
    assert.equal(lastMenu(), rowMenu, "the row menu stays mounted through the repaint");
  });
});

test("repeated row and header switching leaves one listener set, not a pile", () => {
  withEnv(({ sidebar, window, document, lastMenu }) => {
    const menus: FakeElement[] = [];
    const open = (header: boolean, step: number): void => {
      if (header) sidebar.openProjectsMenu();
      else sidebar.openSessionMenu("s1");
      menus.push(lastMenu());
      const scroll = window.get("scroll") ?? [];
      const resize = window.get("resize") ?? [];
      const keydown = document.get("keydown") ?? [];
      const focusin = document.get("focusin") ?? [];
      assert.equal(scroll.length, 1, `step ${step}: one scroll listener — the open menu's`);
      assert.equal(resize.length, 1, `step ${step}: one resize listener — the open menu's`);
      assert.equal(keydown.length, header ? 1 : 0, `step ${step}: only the open menu's keyboard contract`);
      assert.equal(focusin.length, header ? 1 : 0, `step ${step}: only the open menu's focus net`);
    };
    open(false, 0);
    open(true, 1);
    open(false, 2);
    open(true, 3);
    open(false, 4);
    assert.equal(new Set(menus).size, menus.length, "every switch mounts a fresh menu");
  });
});

test("dispose releases the open menus' window and document listeners", () => {
  withEnv(({ sidebar, window, document, fireDocument }) => {
    sidebar.openProjectsMenu();
    sidebar.openSessionMenu("s1");
    sidebar.openProjectsMenu();
    assert.equal((window.get("resize") ?? []).length, 1, "fixture: the open header menu holds the resize listener");
    assert.equal((document.get("keydown") ?? []).length, 1, "fixture: the open header menu holds the keydown handler");

    sidebar.dispose();
    assert.equal((window.get("scroll") ?? []).length, 0, "no scroll listener survives disposal");
    assert.equal((window.get("resize") ?? []).length, 0, "no resize listener survives disposal");
    assert.equal((document.get("keydown") ?? []).length, 0, "no keydown handler survives disposal");
    assert.equal((document.get("focusin") ?? []).length, 0, "no focus net survives disposal");
    let claimed = false;
    fireDocument("keydown", { key: "Escape", preventDefault() { claimed = true; } });
    assert.ok(!claimed, "a disposed menu's Escape handler is gone");
  });
});

// The project-list observer is what a colour-mirroring surface outside the
// sidebar (the titlebar chip) re-derives from, so both row-redraw paths
// must fire it — a redraw path that skips the notification leaves every
// mirror stale.
test("every row redraw fires the project-list observer", () => {
  let fires = 0;
  withEnv(({ sidebar }) => {
    assert.equal(fires, 1, "the constructor's initial render notifies");
    // model is private; reach it only to force the next render to paint.
    const internals = sidebar as unknown as {
      model: { groups: { project: { name: string } }[] };
    };
    internals.model.groups[0]!.project.name = "beta";
    sidebar.render();
    assert.equal(fires, 2, "a full render that repaints notifies");
    // renderProjectList is private; the cast reaches the partial redraw.
    const partial = sidebar as unknown as { renderProjectList(): void };
    partial.renderProjectList();
    assert.equal(fires, 3, "a list-only redraw notifies");
    sidebar.render();
    assert.equal(fires, 4,
      "a render after a partial redraw repaints — the list-only write dropped the cached markup");
  }, () => {
    fires += 1;
  });
});

// A spinner patched in place is later invisible to a render whose bytes
// re-equal the cache: the server can stop presenting the session as
// running, which makes the streaming flip leave no trace in the rebuilt
// markup. The patch must drop the cache so that render repaints and the
// spinner cannot outlive the state that justified it.
// The phone's leading status cell rides the same output edge: the 1.5s
// streaming window is far shorter than the 10s poll, so a cell only a full
// render writes is empty for the whole window — or stuck after it.
test("setStreaming patches the leading status cell on both edges", () => {
  withEnv(({ sidebar, container }) => {
    const cell = () => container.querySelector('[data-session="s1"]')!.querySelector("[data-session-status]")!;
    assert.equal(cell().innerHTML, "", "the cell starts empty");
    sidebar.setStreaming("s1", true);
    assert.ok(cell().innerHTML.includes("output streaming"),
      "the streaming edge did not light the leading cell");
    sidebar.setStreaming("s1", false);
    assert.equal(cell().innerHTML, "", "the silence edge did not clear the leading cell");
  });
});

test("a divergent meta patch forces the next render to repaint", () => {
  withEnv(({ sidebar, container, sessionMetaSpan }) => {
    sidebar.setStreaming("s1", true);
    assert.ok(sessionMetaSpan.innerHTML.includes("output streaming"),
      "setStreaming did not patch the session's meta span");
    const internals = sidebar as unknown as {
      model: { groups: { sessions: { presentation_state: string }[] }[] };
    };
    internals.model.groups[0]!.sessions[0]!.presentation_state = "ended";
    container.innerHTML = "sentinel";
    sidebar.render();
    assert.notEqual(container.innerHTML, "sentinel",
      "a render whose bytes re-equal the cache skipped over a span patched out of band");
  });
});

interface DragRow {
  tag: string;
  dataset: Record<string, string>;
  className: string;
  clientHeight: number;
  getBoundingClientRect(): { top: number; height: number };
  // shiftTo moves the row's box after layout, standing in for the shift a
  // real browser applies when the drop gap's margin opens on the row.
  shiftTo(top: number): void;
  classList: { add(cls: string): void; remove(...cls: string[]): void; contains(cls: string): boolean };
  addEventListener(type: string, fn: (ev: unknown) => void): void;
  dispatchEvent(ev: { type: string; [key: string]: unknown }): void;
}

interface DragList {
  tag: string;
  listeners: Map<string, ((ev: unknown) => void)[]>;
  innerHTML: string;
  contains(node: unknown): boolean;
  addEventListener(type: string, fn: (ev: unknown) => void): void;
  dispatchEvent(ev: { type: string; [key: string]: unknown }): void;
  querySelector(sel: string): FakeElement | null;
  querySelectorAll(sel: string): FakeElement[];
}

interface SessionDragRail extends DragList {
  id: string;
  getAttribute(name: string): string | null;
}

function withDragEnv(run: (harness: ReturnType<typeof makeDragHarness>) => void): void {
  const originalRAF = (globalThis as unknown as { requestAnimationFrame?: (cb: () => void) => number }).requestAnimationFrame;
  const originalWindow = (globalThis as unknown as { window?: unknown }).window;
  const originalDocument = (globalThis as unknown as { document?: unknown }).document;
  const originalCSS = (globalThis as unknown as { CSS?: unknown }).CSS;

  (globalThis as unknown as { requestAnimationFrame: (cb: () => void) => number }).requestAnimationFrame = () => 0;
  (globalThis as unknown as { window: unknown }).window = {
    matchMedia: () => ({ matches: false }),
    setInterval: () => 0,
    addEventListener: () => {},
    removeEventListener: () => {},
  };
  (globalThis as unknown as { document: unknown }).document = {
    createElement: () => fakeElement({ tag: "div" }),
  };
  (globalThis as unknown as { CSS: unknown }).CSS = { escape: (s: string) => s };

  try {
    run(makeDragHarness());
  } finally {
    (globalThis as unknown as { requestAnimationFrame?: (cb: () => void) => number }).requestAnimationFrame = originalRAF;
    (globalThis as unknown as { window?: unknown }).window = originalWindow;
    (globalThis as unknown as { document?: unknown }).document = originalDocument;
    (globalThis as unknown as { CSS?: unknown }).CSS = originalCSS;
  }
}

function makeDragHarness(): {
  sidebar: Sidebar;
  rows: DragRow[];
  list: DragList;
  saved: SidebarLayout[];
  renderCount: { value: number };
} {
  const makeRow = (id: string, top: number): DragRow => {
    const classes = new Set<string>();
    const listeners = new Map<string, ((ev: unknown) => void)[]>();
    let currentTop = top;
    return {
      tag: "li",
      dataset: { project: id, projectRow: "" },
      className: "rounded-control",
      clientHeight: 40,
      getBoundingClientRect() { return { top: currentTop, height: 40 }; },
      shiftTo(next) { currentTop = next; },
      classList: {
        add(cls: string) {
          classes.add(cls);
        },
        remove(...clsList: string[]) {
          for (const c of clsList) classes.delete(c);
        },
        contains(cls: string) {
          return classes.has(cls);
        },
      },
      addEventListener(type: string, fn: (ev: unknown) => void) {
        const list = listeners.get(type) ?? [];
        list.push(fn);
        listeners.set(type, list);
      },
      dispatchEvent(ev: { type: string; [key: string]: unknown }) {
        for (const fn of listeners.get(ev.type) ?? []) fn(ev);
      },
    };
  };

  const rows: DragRow[] = [makeRow("p1", 0), makeRow("p2", 44)];
  const rowSet = new Set<unknown>(rows);
  const projectRowFor = (sel: string): DragRow | null => {
    const match = /^\[data-project="([^"]+)"\]$/.exec(sel);
    if (match === null) return null;
    return rows.find((row) => row.dataset.project === match[1]) ?? null;
  };

  const listListeners = new Map<string, ((ev: unknown) => void)[]>();
  const list: DragList = {
    tag: "ul",
    listeners: listListeners,
    innerHTML: "",
    contains(node: unknown) {
      return rowSet.has(node);
    },
    addEventListener(type: string, fn: (ev: unknown) => void) {
      const list = listListeners.get(type) ?? [];
      list.push(fn);
      listListeners.set(type, list);
    },
    dispatchEvent(ev: { type: string; [key: string]: unknown }) {
      for (const fn of listListeners.get(ev.type) ?? []) fn(ev);
    },
    querySelector(sel: string) {
      return projectRowFor(sel) as unknown as FakeElement | null;
    },
    querySelectorAll(sel: string) {
      return sel === "[data-project-row]" ? rows as unknown as FakeElement[] : [];
    },
  };

  const renderCount = { value: 0 };
  const saved: SidebarLayout[] = [];
  const sidebarModel = {
    groups: [
      { project: { id: "p1", name: "alpha", canonical_path: "/srv/alpha", last_used_at: "t" }, sessions: [] },
      { project: { id: "p2", name: "bravo", canonical_path: "/srv/bravo", last_used_at: "t" }, sessions: [] },
    ],
    selectedSessionID: null,
    error: null,
    gitIdentities: null,
    creatingSession: false,
    applyProjectColors() {},
    api: {
      getLayout: () => Promise.resolve({ expanded: [], projects: [], sessions: {} }),
      saveLayout(next: SidebarLayout) {
        saved.push(next);
        return Promise.resolve(next);
      },
    },
  } as unknown as SidebarModel;

  const container = fakeElement({
    tag: "aside",
    closest(sel: string) {
      return sel === "[data-workspace]" ? fakeElement({ tag: "div", dataset: { workspace: "" } }) : null;
    },
    querySelector(sel: string) {
      if (sel === "[data-project-list]") return list as unknown as FakeElement;
      return null;
    },
    querySelectorAll(sel: string) {
      if (sel === "[data-project-row]") return rows as unknown as FakeElement[];
      if (sel.includes("project-row-drop-gap")) return rows as unknown as FakeElement[];
      return [];
    },
  });

  const sidebar = new Sidebar(container as unknown as HTMLElement, sidebarModel);
  const originalRenderProjectList = (sidebar as unknown as { renderProjectList(): void }).renderProjectList.bind(sidebar);
  (sidebar as unknown as { renderProjectList(): void }).renderProjectList = () => {
    renderCount.value += 1;
    originalRenderProjectList();
  };

  return { sidebar, rows, list, saved, renderCount };
}

// Project rows move by menu alone: the rows carry no draggable attribute
// and the shell binds no project drag handlers, so a drag gesture over the
// list paints nothing and commits nothing.
test("a drag gesture over the project list is inert", () => {
  withDragEnv(({ rows, list, saved }) => {
    const source = rows[0]!;
    const target = rows[1]!;
    const dataTransfer = { effectAllowed: "", dropEffect: "", setData() {} };

    source.dispatchEvent({ type: "dragstart", dataTransfer });
    assert.ok(!source.classList.contains("project-row-drag"), "no opacity class is painted");

    list.dispatchEvent({ type: "dragover", clientY: 50, dataTransfer, preventDefault() {}, stopPropagation() {} });
    assert.ok(!target.classList.contains("project-row-drop-gap-before"), "no gap opens before the target");
    list.dispatchEvent({ type: "dragover", clientY: 80, dataTransfer, preventDefault() {}, stopPropagation() {} });
    assert.ok(!target.classList.contains("project-row-drop-gap-after"), "no gap opens after the target");

    list.dispatchEvent({ type: "drop", clientY: 80, dataTransfer, preventDefault() {}, stopPropagation() {} });
    assert.equal(saved.length, 0, "a drop saves nothing");
  });
});

// Session drag tests: sessions reorder within their own project rail, a
// cross-project drop is ignored, and the saved layout round-trips.

function makeSessionDragRow(id: string, top: number): DragRow {
  const classes = new Set<string>();
  const listeners = new Map<string, ((ev: unknown) => void)[]>();
  let currentTop = top;
  return {
    tag: "li",
    dataset: { session: id },
    className: "rounded-control",
    clientHeight: 30,
    getBoundingClientRect() { return { top: currentTop, height: 30 }; },
    shiftTo(next) { currentTop = next; },
    classList: {
      add(cls: string) { classes.add(cls); },
      remove(...clsList: string[]) { for (const c of clsList) classes.delete(c); },
      contains(cls: string) { return classes.has(cls); },
    },
    addEventListener(type: string, fn: (ev: unknown) => void) {
      const list = listeners.get(type) ?? [];
      list.push(fn);
      listeners.set(type, list);
    },
    dispatchEvent(ev: { type: string; [key: string]: unknown }) {
      for (const fn of listeners.get(ev.type) ?? []) fn(ev);
    },
  };
}

function makeSessionDragProjectRow(id: string, top: number): DragRow {
  const classes = new Set<string>();
  const listeners = new Map<string, ((ev: unknown) => void)[]>();
  let currentTop = top;
  return {
    tag: "li",
    dataset: { project: id, projectRow: "" },
    className: "rounded-control",
    clientHeight: 40,
    getBoundingClientRect() { return { top: currentTop, height: 40 }; },
    shiftTo(next) { currentTop = next; },
    classList: {
      add(cls: string) { classes.add(cls); },
      remove(...clsList: string[]) { for (const c of clsList) classes.delete(c); },
      contains(cls: string) { return classes.has(cls); },
    },
    addEventListener(type: string, fn: (ev: unknown) => void) {
      const list = listeners.get(type) ?? [];
      list.push(fn);
      listeners.set(type, list);
    },
    dispatchEvent(ev: { type: string; [key: string]: unknown }) {
      for (const fn of listeners.get(ev.type) ?? []) fn(ev);
    },
  };
}

interface SessionDragHarness {
  sidebar: Sidebar;
  container: FakeElement;
  projectRows: DragRow[];
  sessionRows: { p1: DragRow[]; p2: DragRow[] };
  rails: Record<"p1" | "p2", SessionDragRail>;
  list: DragList;
  saved: SidebarLayout[];
  renderCount: { value: number };
}

// Session rows lay out 30px tall with 2px gaps; the first row's top for a
// rail is derived from the saved order so the fake geometry matches the
// rendered order the layout document carries.
function makeSessionDragHarness(layout: SidebarLayout): SessionDragHarness {
  const saved: SidebarLayout[] = [];
  const renderCount = { value: 0 };

  const projectRows: DragRow[] = [makeSessionDragProjectRow("p1", 0), makeSessionDragProjectRow("p2", 84)];
  const p1Order = layout.sessions.p1 ?? ["s1", "s2", "s3"];
  const sessionRows: { p1: DragRow[]; p2: DragRow[] } = {
    p1: p1Order.map((id, index) => makeSessionDragRow(id, index * 32)),
    p2: [makeSessionDragRow("s4", 0), makeSessionDragRow("s5", 32)],
  };
  const allSessionRows = [...sessionRows.p1, ...sessionRows.p2];

  const makeRail = (pid: keyof typeof sessionRows): SessionDragRail => {
    const rowSet = new Set(sessionRows[pid]);
    const listeners = new Map<string, ((ev: unknown) => void)[]>();
    return {
      tag: "ul",
      id: `sessions-${pid}`,
      getAttribute(name: string) { return name === "id" ? `sessions-${pid}` : null; },
      listeners,
      innerHTML: "",
      contains(node: unknown) { return rowSet.has(node as DragRow); },
      addEventListener(type: string, fn: (ev: unknown) => void) {
        const list = listeners.get(type) ?? [];
        list.push(fn);
        listeners.set(type, list);
      },
      dispatchEvent(ev: { type: string; [key: string]: unknown }) {
        for (const fn of listeners.get(ev.type) ?? []) fn(ev);
      },
      querySelector(sel: string) {
        const match = /^\[data-session="([^"]+)"\]$/.exec(sel);
        if (match === null) return null;
        return (sessionRows[pid].find((row) => row.dataset.session === match[1]) ?? null) as unknown as FakeElement | null;
      },
      querySelectorAll(sel: string) {
        if (sel === "[data-session]") return sessionRows[pid] as unknown as FakeElement[];
        return [];
      },
    };
  };
  const rails: Record<"p1" | "p2", SessionDragRail> = {
    p1: makeRail("p1"),
    p2: makeRail("p2"),
  };

  const rowSet = new Set<unknown>([...projectRows, ...allSessionRows]);
  const listListeners = new Map<string, ((ev: unknown) => void)[]>();
  const list: DragList = {
    tag: "ul",
    listeners: listListeners,
    innerHTML: "",
    contains(node: unknown) { return rowSet.has(node); },
    addEventListener(type: string, fn: (ev: unknown) => void) {
      const list = listListeners.get(type) ?? [];
      list.push(fn);
      listListeners.set(type, list);
    },
    dispatchEvent(ev: { type: string; [key: string]: unknown }) {
      for (const fn of listListeners.get(ev.type) ?? []) fn(ev);
    },
    querySelector(sel: string) {
      const match = /^\[data-project="([^"]+)"\]$/.exec(sel);
      if (match === null) return null;
      return (projectRows.find((row) => row.dataset.project === match[1]) ?? null) as unknown as FakeElement | null;
    },
    querySelectorAll(sel: string) {
      return sel === "[data-project-row]" ? projectRows as unknown as FakeElement[] : [];
    },
  };

  const container = fakeElement({
    tag: "aside",
    closest(sel: string) {
      return sel === "[data-workspace]" ? fakeElement({ tag: "div", dataset: { workspace: "" } }) : null;
    },
    querySelector(sel: string) {
      if (sel === "[data-project-list]") return list as unknown as FakeElement;
      return null;
    },
    querySelectorAll(sel: string) {
      if (sel === "[data-project-row]") return projectRows as unknown as FakeElement[];
      if (sel === "[data-session]") return allSessionRows as unknown as FakeElement[];
      if (sel === ".session-rail") return [rails.p1, rails.p2] as unknown as FakeElement[];
      if (sel.includes("project-row-drop-gap")) {
        return [...projectRows, ...allSessionRows] as unknown as FakeElement[];
      }
      return [];
    },
  });

  const groups: ProjectWithSessions[] = [
    {
      project: { id: "p1", name: "alpha", canonical_path: "/srv/alpha", last_used_at: "t" },
      sessions: [
        { id: "s1", project_id: "p1", provider: "claude", display_name: "", status: "running", generation: 1, created_at: "t", updated_at: "t", recovery_capability: "none", recovery_state: "unavailable", presentation_state: "running" },
        { id: "s2", project_id: "p1", provider: "codex", display_name: "", status: "running", generation: 1, created_at: "t", updated_at: "t", recovery_capability: "none", recovery_state: "unavailable", presentation_state: "running" },
        { id: "s3", project_id: "p1", provider: "shell", display_name: "", status: "running", generation: 1, created_at: "t", updated_at: "t", recovery_capability: "none", recovery_state: "unavailable", presentation_state: "running" },
      ],
    },
    {
      project: { id: "p2", name: "beta", canonical_path: "/srv/beta", last_used_at: "t" },
      sessions: [
        { id: "s4", project_id: "p2", provider: "claude", display_name: "", status: "running", generation: 1, created_at: "t", updated_at: "t", recovery_capability: "none", recovery_state: "unavailable", presentation_state: "running" },
        { id: "s5", project_id: "p2", provider: "codex", display_name: "", status: "running", generation: 1, created_at: "t", updated_at: "t", recovery_capability: "none", recovery_state: "unavailable", presentation_state: "running" },
      ],
    },
  ];

  const sidebarModel = {
    groups,
    selectedSessionID: null,
    error: null,
    gitIdentities: null,
    creatingSession: false,
    applyProjectColors() {},
        api: {
      getLayout: () => Promise.resolve(layout),
      saveLayout: (next: SidebarLayout) => {
        saved.push(next);
        return Promise.resolve(next);
      },
    },
  } as unknown as SidebarModel;

  const sidebar = new Sidebar(container as unknown as HTMLElement, sidebarModel);
  const originalRenderProjectList = (sidebar as unknown as { renderProjectList(): void }).renderProjectList.bind(sidebar);
  (sidebar as unknown as { renderProjectList(): void }).renderProjectList = () => {
    renderCount.value += 1;
    originalRenderProjectList();
  };

  return { sidebar, container, projectRows, sessionRows, rails, list, saved, renderCount };
}

async function withSessionDragEnv(
  layout: SidebarLayout,
  run: (harness: SessionDragHarness) => void | Promise<void>,
): Promise<void> {
  const originalRAF = (globalThis as unknown as { requestAnimationFrame?: (cb: () => void) => number }).requestAnimationFrame;
  const originalWindow = (globalThis as unknown as { window?: unknown }).window;
  const originalDocument = (globalThis as unknown as { document?: unknown }).document;
  const originalCSS = (globalThis as unknown as { CSS?: unknown }).CSS;

  const raf: (() => void)[] = [];
  (globalThis as unknown as { requestAnimationFrame: (cb: () => void) => number }).requestAnimationFrame = (cb) => {
    raf.push(cb);
    return 0;
  };
  (globalThis as unknown as { window: unknown }).window = {
    matchMedia: () => ({ matches: false }),
    setInterval: () => 0,
    addEventListener: () => {},
    removeEventListener: () => {},
    clearInterval: () => {},
  };
  (globalThis as unknown as { document: unknown }).document = {
    createElement: () => fakeElement({ tag: "div" }),
  };
  (globalThis as unknown as { CSS: unknown }).CSS = { escape: (s: string) => s };

  const harness = makeSessionDragHarness(layout);

  try {
    await Promise.resolve();
    await run(harness);
  } finally {
    (globalThis as unknown as { requestAnimationFrame?: (cb: () => void) => number }).requestAnimationFrame = originalRAF;
    (globalThis as unknown as { window?: unknown }).window = originalWindow;
    (globalThis as unknown as { document?: unknown }).document = originalDocument;
    (globalThis as unknown as { CSS?: unknown }).CSS = originalCSS;
  }
}

function sessionOrderInHtml(html: string, projectID: string): string[] {
  const railPattern = new RegExp(`id="sessions-${projectID}"[^>]*>([\\s\\S]*?)<\\/ul>`);
  const match = railPattern.exec(html);
  if (match === null) return [];
  const railHtml = match[1]!;
  const order: string[] = [];
  const pattern = /data-session="([^"]*)"/g;
  let m: RegExpExecArray | null;
  while ((m = pattern.exec(railHtml)) !== null) {
    order.push(m[1]!);
  }
  return order;
}

test("a session drag within its project rail reorders and saves the layout", async () => {
  await withSessionDragEnv({ expanded: [], projects: [], sessions: {} }, async ({ sessionRows, rails, saved }) => {
    const source = sessionRows.p1[0]!;
    const target = sessionRows.p1[2]!;

    const dataTransfer = { effectAllowed: "", dropEffect: "", setData() {} };
    source.dispatchEvent({ type: "dragstart", dataTransfer, stopPropagation() {} });
    assert.ok(source.classList.contains("project-row-drag"), "dragged session takes the shared opacity class");

    // Rows lay out at 0..30, 32..62, 64..94: a pointer at 80 sits in s3's
    // bottom half, so the gap opens after it.
    rails.p1.dispatchEvent({ type: "dragover", clientY: 80, dataTransfer, preventDefault() {}, stopPropagation() {} });
    assert.ok(target.classList.contains("project-row-drop-gap-after"), "hover opens the shared gap after the target");

    rails.p1.dispatchEvent({ type: "drop", clientY: 80, dataTransfer, preventDefault() {}, stopPropagation() {} });
    assert.ok(!target.classList.contains("project-row-drop-gap-after"), "drop clears the gap");
    assert.equal(saved.length, 1, "drop saves the layout");
    assert.deepEqual(saved[0]!.sessions.p1, ["s2", "s3", "s1"], "sessions are reordered within the project");
  });
});

test("a session drag that ends in a different project's rail is a no-op", async () => {
  await withSessionDragEnv({ expanded: [], projects: [], sessions: {} }, async ({ sessionRows, rails, saved }) => {
    const source = sessionRows.p1[0]!;
    const target = sessionRows.p2[0]!;

    const dataTransfer = { effectAllowed: "", dropEffect: "", setData() {} };
    source.dispatchEvent({ type: "dragstart", dataTransfer, stopPropagation() {} });
    rails.p2.dispatchEvent({ type: "dragover", clientY: 10, dataTransfer, preventDefault() {}, stopPropagation() {} });
    assert.ok(!target.classList.contains("project-row-drop-gap-after"), "cross-project hover does not open a gap");
    rails.p2.dispatchEvent({ type: "drop", dataTransfer, preventDefault() {}, stopPropagation() {} });
    assert.equal(saved.length, 0, "cross-project drop does not save a layout");
  });
});

test("a saved session order round-trips through the layout document", async () => {
  await withSessionDragEnv(
    { expanded: [], projects: [], sessions: { p1: ["s3", "s2", "s1"] } },
    async ({ container, sessionRows, rails, saved }) => {
      assert.deepEqual(sessionOrderInHtml(container.innerHTML, "p1"), ["s3", "s2", "s1"], "loaded order is rendered");

      const source = sessionRows.p1.find((r) => r.dataset.session === "s2")!;
      // The rendered rail lays out s3 at 0..30, s2 at 32..62, s1 at 64..94.
      // A pointer at 5 sits in s3's top half: s2 moves to before it.
      const dataTransfer = { effectAllowed: "", dropEffect: "", setData() {} };
      source.dispatchEvent({ type: "dragstart", dataTransfer, stopPropagation() {} });
      rails.p1.dispatchEvent({ type: "dragover", clientY: 5, dataTransfer, preventDefault() {}, stopPropagation() {} });
      rails.p1.dispatchEvent({ type: "drop", clientY: 5, dataTransfer, preventDefault() {}, stopPropagation() {} });
      await Promise.resolve();

      assert.equal(saved.length, 1, "drop saves the layout");
      assert.deepEqual(saved[0]!.sessions.p1, ["s2", "s3", "s1"], "reordered sessions round-trip through the layout");
      assert.equal(saved[0]!.sessions.p2, undefined, "other projects' session entries are left untouched");
    },
  );
});

test("a drop whose source left the rail mid-drag commits nothing", async () => {
  await withSessionDragEnv({ expanded: [], projects: [], sessions: {} }, async ({ sessionRows, rails, saved }) => {
    const source = sessionRows.p1[0]!;
    const dataTransfer = { effectAllowed: "", dropEffect: "", setData() {} };
    const dropEvent = (clientY: number) => ({ clientY, dataTransfer, preventDefault() {}, stopPropagation() {} });

    source.dispatchEvent({ type: "dragstart", dataTransfer, stopPropagation() {} });
    rails.p1.dispatchEvent({ type: "dragover", ...dropEvent(68) });
    assert.ok(sessionRows.p1[2]!.classList.contains("project-row-drop-gap-before"), "gap opens before s3");

    // The dragged session itself is removed mid-gesture: the rail no longer
    // names it. Any release must refuse — committing would re-insert the
    // dead id into the saved order at whatever slot sits under the pointer.
    sessionRows.p1.splice(0, 1);
    rails.p1.dispatchEvent({ type: "dragover", ...dropEvent(68) });
    rails.p1.dispatchEvent({ type: "drop", ...dropEvent(40) });
    assert.equal(saved.length, 0, "a vanished source commits nothing");
  });
});

test("a drop whose target left the rail mid-drag commits nothing", async () => {
  await withSessionDragEnv({ expanded: [], projects: [], sessions: {} }, async ({ sessionRows, rails, saved }) => {
    const source = sessionRows.p1[0]!;
    const dataTransfer = { effectAllowed: "", dropEffect: "", setData() {} };
    const dropEvent = (clientY: number) => ({ clientY, dataTransfer, preventDefault() {}, stopPropagation() {} });

    source.dispatchEvent({ type: "dragstart", dataTransfer, stopPropagation() {} });
    rails.p1.dispatchEvent({ type: "dragover", ...dropEvent(68) });
    assert.ok(sessionRows.p1[2]!.classList.contains("project-row-drop-gap-before"), "gap opens before s3");

    // A mid-drag refresh removes s3: the rail no longer names it, and the
    // release point sits below every remaining row. The drop must refuse —
    // committing the remembered target would append the source as an
    // unknown id, a slot the user never pointed at.
    sessionRows.p1.splice(2, 1);
    rails.p1.dispatchEvent({ type: "drop", ...dropEvent(68) });
    assert.equal(saved.length, 0, "a vanished target commits nothing");
  });
});

test("a session drop over the opened gap commits the slot the gap shows", async () => {
  await withSessionDragEnv({ expanded: [], projects: [], sessions: {} }, async ({ sessionRows, rails, saved }) => {
    const source = sessionRows.p1[0]!;
    const target = sessionRows.p1[2]!;
    const neighbour = sessionRows.p1[1]!;
    const dataTransfer = { effectAllowed: "", dropEffect: "", setData() {} };

    source.dispatchEvent({ type: "dragstart", dataTransfer, stopPropagation() {} });
    // Pointer at 68 sits in s3's top half: the gap before s3 opens, and
    // its margin pushes s3 down a full row height under the stationary
    // pointer — over dead rail space, or over s2, in the live layout.
    rails.p1.dispatchEvent({ type: "dragover", clientY: 68, dataTransfer, preventDefault() {}, stopPropagation() {} });
    assert.ok(target.classList.contains("project-row-drop-gap-before"), "gap opens before the target");
    target.shiftTo(94);

    rails.p1.dispatchEvent({ type: "dragover", clientY: 68, dataTransfer, preventDefault() {}, stopPropagation() {} });
    assert.ok(target.classList.contains("project-row-drop-gap-before"), "the gap holds its row against the shifted layout");
    assert.ok(!neighbour.classList.contains("project-row-drop-gap-after"), "the slot does not slide onto the neighbour");

    // The release lands on the rail itself, not any row.
    rails.p1.dispatchEvent({ type: "drop", clientY: 68, dataTransfer, preventDefault() {}, stopPropagation() {} });
    assert.equal(saved.length, 1, "drop over the gap saves the layout");
    assert.deepEqual(saved[0]!.sessions.p1, ["s2", "s1", "s3"], "the visible slot is the committed order");
    assert.ok(!target.classList.contains("project-row-drop-gap-before"), "drop clears the gap");
  });
});

interface LayoutHarness {
  sidebar: Sidebar;
  container: FakeElement;
  saved: SidebarLayout[];
  clickToggle(id: string): void;
}

function makeLayoutHarness(raf: (() => void)[], layout: SidebarLayout | null): LayoutHarness {
  const saved: SidebarLayout[] = [];
  const toggleListeners = new Map<string, ((ev: unknown) => void)[]>();

  function fakeToggleButton(id: string): FakeElement {
    const listeners: ((ev: unknown) => void)[] = [];
    toggleListeners.set(id, listeners);
    return fakeElement({
      tag: "button",
      dataset: { projectToggle: id },
      getAttribute(name) {
        return name === "data-project-toggle" ? id : null;
      },
      addEventListener(type, fn) {
        if (type === "click") listeners.push(fn);
      },
    });
  }

  const workspace = fakeElement({
    tag: "div",
    dataset: { workspace: "" },
    querySelector() {
      return null;
    },
  });
  const container = fakeElement({
    tag: "aside",
    dataset: { sidebar: "" },
    closest(sel) {
      return sel === "[data-workspace]" ? workspace : null;
    },
    querySelector(sel) {
      const match = /^\[data-project-toggle="([^"]+)"\]$/.exec(sel);
      return match !== null ? fakeToggleButton(match[1]!) : null;
    },
    querySelectorAll(sel) {
      return sel === "[data-project-toggle]"
        ? groups.map((g) => fakeToggleButton(g.project.id))
        : [];
    },
  });

  const groups: ProjectWithSessions[] = [
    { project: { id: "p1", name: "alpha", canonical_path: "/srv/alpha", last_used_at: "t" }, sessions: [] },
    { project: { id: "p2", name: "beta", canonical_path: "/srv/beta", last_used_at: "t" }, sessions: [] },
    { project: { id: "p3", name: "gamma", canonical_path: "/srv/gamma", last_used_at: "t" }, sessions: [] },
  ];

  const sidebarModel = {
    groups,
    selectedSessionID: null,
    error: null,
    gitIdentities: null,
    creatingSession: false,
    applyProjectColors() {},
        api: {
      getLayout: () => Promise.resolve(layout),
      saveLayout: (next: SidebarLayout) => {
        saved.push(next);
        return Promise.resolve(next);
      },
    },
  } as unknown as SidebarModel;

  const sidebar = new Sidebar(container as unknown as HTMLElement, sidebarModel);

  function clickToggle(id: string): void {
    for (const fn of toggleListeners.get(id) ?? []) {
      fn({});
    }
  }

  return { sidebar, container, saved, clickToggle };
}

async function withLayoutEnv(
  layout: SidebarLayout | null,
  run: (harness: LayoutHarness) => void | Promise<void>,
): Promise<void> {
  const originalRAF = (globalThis as unknown as { requestAnimationFrame?: (cb: () => void) => number }).requestAnimationFrame;
  const originalWindow = (globalThis as unknown as { window?: unknown }).window;
  const originalDocument = (globalThis as unknown as { document?: unknown }).document;
  const originalCSS = (globalThis as unknown as { CSS?: unknown }).CSS;

  const raf: (() => void)[] = [];
  (globalThis as unknown as { requestAnimationFrame: (cb: () => void) => number }).requestAnimationFrame = (cb) => {
    raf.push(cb);
    return 0;
  };
  (globalThis as unknown as { window: unknown }).window = {
    matchMedia: () => ({ matches: false }),
    setInterval: () => 0,
    addEventListener: () => {},
    removeEventListener: () => {},
    clearInterval: () => {},
  };
  (globalThis as unknown as { document: unknown }).document = {
    createElement: () => fakeElement({ tag: "div" }),
  };
  (globalThis as unknown as { CSS: unknown }).CSS = { escape: (s: string) => s };

  const harness = makeLayoutHarness(raf, layout);

  try {
    await run(harness);
  } finally {
    (globalThis as unknown as { requestAnimationFrame?: (cb: () => void) => number }).requestAnimationFrame = originalRAF;
    (globalThis as unknown as { window?: unknown }).window = originalWindow;
    (globalThis as unknown as { document?: unknown }).document = originalDocument;
    (globalThis as unknown as { CSS?: unknown }).CSS = originalCSS;
  }
}

function projectOrderInHtml(html: string): string[] {
  const order: string[] = [];
  const pattern = /data-project="([^"]+)"/g;
  let match: RegExpExecArray | null;
  while ((match = pattern.exec(html)) !== null) {
    order.push(match[1]!);
  }
  return [...new Set(order)];
}

test("the sidebar applies the loaded project order after mount", async () => {
  await withLayoutEnv({ expanded: [], projects: ["p3", "p1", "p2"], sessions: {} }, async ({ container }) => {
    await Promise.resolve();
    assert.deepEqual(projectOrderInHtml(container.innerHTML), ["p3", "p1", "p2"]);
  });
});

test("the sidebar applies loaded expansion after mount", async () => {
  await withLayoutEnv({ expanded: ["p2"], projects: [], sessions: {} }, async ({ container }) => {
    await Promise.resolve();
    const p2Rail = container.innerHTML.match(/id="sessions-p2"[^>]*>/);
    assert.notEqual(p2Rail, null, "p2 sessions rail should render");
    assert.ok(p2Rail !== null && !p2Rail[0]!.includes("hidden"), "p2 sessions rail should be visible");
    const p1Rail = container.innerHTML.match(/id="sessions-p1"[^>]*>/);
    assert.notEqual(p1Rail, null, "p1 sessions rail should render");
    assert.ok(p1Rail !== null && p1Rail[0]!.includes("hidden"), "p1 sessions rail should be hidden");
  });
});

test("stale layout ids are ignored, falling back to defaults", async () => {
  await withLayoutEnv({ expanded: ["gone"], projects: ["gone", "p2"], sessions: {} }, async ({ container }) => {
    await Promise.resolve();
    assert.deepEqual(projectOrderInHtml(container.innerHTML), ["p1", "p2", "p3"]);
    const p1Rail = container.innerHTML.match(/id="sessions-p1"[^>]*>/);
    assert.notEqual(p1Rail, null, "p1 sessions rail should render");
    assert.ok(p1Rail !== null && !p1Rail[0]!.includes("hidden"), "p1 should expand by default when no valid saved expansion exists");
    const p2Rail = container.innerHTML.match(/id="sessions-p2"[^>]*>/);
    assert.notEqual(p2Rail, null, "p2 sessions rail should render");
    assert.ok(p2Rail !== null && p2Rail[0]!.includes("hidden"), "p2 should stay collapsed");
  });
});

test("a 404 layout response leaves the default expansion in place", async () => {
  await withLayoutEnv(null, async ({ container }) => {
    await Promise.resolve();
    const p1Rail = container.innerHTML.match(/id="sessions-p1"[^>]*>/);
    assert.notEqual(p1Rail, null, "p1 sessions rail should render");
    assert.ok(p1Rail !== null && !p1Rail[0]!.includes("hidden"), "p1 should expand by default when no layout is saved");
    const p2Rail = container.innerHTML.match(/id="sessions-p2"[^>]*>/);
    assert.notEqual(p2Rail, null, "p2 sessions rail should render");
    assert.ok(p2Rail !== null && p2Rail[0]!.includes("hidden"), "p2 should stay collapsed");
  });
});

test("a written-but-empty layout collapses every project and survives re-render", async () => {
  await withLayoutEnv({ expanded: [], projects: [], sessions: {} }, async ({ container, sidebar }) => {
    await Promise.resolve();
    for (const id of ["p1", "p2", "p3"]) {
      const rail = container.innerHTML.match(new RegExp(`id="sessions-${id}"[^>]*>`));
      assert.notEqual(rail, null, `${id} sessions rail should render`);
      assert.ok(rail !== null && rail[0]!.includes("hidden"), `${id} should be collapsed`);
    }
    sidebar.render();
    for (const id of ["p1", "p2", "p3"]) {
      const rail = container.innerHTML.match(new RegExp(`id="sessions-${id}"[^>]*>`));
      assert.notEqual(rail, null, `${id} sessions rail should render after re-render`);
      assert.ok(rail !== null && rail[0]!.includes("hidden"), `${id} should stay collapsed after re-render`);
    }
  });
});

test("toggling a project saves the layout", async () => {
  await withLayoutEnv({ expanded: [], projects: [], sessions: {} }, async ({ saved, clickToggle }) => {
    await Promise.resolve();
    clickToggle("p1");
    await Promise.resolve();
    assert.equal(saved.length, 1, "toggle should persist the layout");
    assert.deepEqual(saved[0]!.expanded, ["p1"]);
    assert.deepEqual(saved[0]!.projects, []);
  });
});

test("a failed layout load leaves the default arrangement", async () => {
  const originalRAF = (globalThis as unknown as { requestAnimationFrame?: (cb: () => void) => number }).requestAnimationFrame;
  const originalWindow = (globalThis as unknown as { window?: unknown }).window;
  const originalDocument = (globalThis as unknown as { document?: unknown }).document;
  const originalCSS = (globalThis as unknown as { CSS?: unknown }).CSS;

  const raf: (() => void)[] = [];
  (globalThis as unknown as { requestAnimationFrame: (cb: () => void) => number }).requestAnimationFrame = (cb) => {
    raf.push(cb);
    return 0;
  };
  (globalThis as unknown as { window: unknown }).window = {
    matchMedia: () => ({ matches: false }),
    setInterval: () => 0,
    addEventListener: () => {},
    removeEventListener: () => {},
    clearInterval: () => {},
  };
  (globalThis as unknown as { document: unknown }).document = {
    createElement: () => fakeElement({ tag: "div" }),
  };
  (globalThis as unknown as { CSS: unknown }).CSS = { escape: (s: string) => s };

  try {
    const workspace = fakeElement({
      tag: "div",
      dataset: { workspace: "" },
      querySelector() { return null; },
    });
    const container = fakeElement({
      tag: "aside",
      dataset: { sidebar: "" },
      closest(sel) { return sel === "[data-workspace]" ? workspace : null; },
      querySelector() { return null; },
      querySelectorAll() { return []; },
    });
    const groups: ProjectWithSessions[] = [
      { project: { id: "p1", name: "alpha", canonical_path: "/srv/alpha", last_used_at: "t" }, sessions: [] },
      { project: { id: "p2", name: "beta", canonical_path: "/srv/beta", last_used_at: "t" }, sessions: [] },
    ];
    const sidebarModel = {
      groups,
      selectedSessionID: null,
      error: null,
      gitIdentities: null,
      creatingSession: false,
    applyProjectColors() {},
          api: {
        getLayout: () => Promise.reject(new Error("unavailable")),
        saveLayout: (next: SidebarLayout) => Promise.resolve(next),
      },
    } as unknown as SidebarModel;
    const sidebar = new Sidebar(container as unknown as HTMLElement, sidebarModel);
    await Promise.resolve();
    assert.deepEqual(projectOrderInHtml(container.innerHTML), ["p1", "p2"]);
    sidebar.dispose();
  } finally {
    (globalThis as unknown as { requestAnimationFrame?: (cb: () => void) => number }).requestAnimationFrame = originalRAF;
    (globalThis as unknown as { window?: unknown }).window = originalWindow;
    (globalThis as unknown as { document?: unknown }).document = originalDocument;
    (globalThis as unknown as { CSS?: unknown }).CSS = originalCSS;
  }
});


interface DeferredLayoutHarness {
  sidebar: Sidebar;
  container: FakeElement;
  resolveLayout(layout: SidebarLayout): void;
  setGroups(groups: ProjectWithSessions[]): void;
}

function makeDeferredLayoutHarness(raf: (() => void)[]): DeferredLayoutHarness {
  let layoutResolve: (layout: SidebarLayout) => void;
  const layoutPromise = new Promise<SidebarLayout>((resolve) => {
    layoutResolve = resolve;
  });

  const workspace = fakeElement({
    tag: "div",
    dataset: { workspace: "" },
    querySelector() {
      return null;
    },
  });

  const toggleListeners = new Map<string, ((ev: unknown) => void)[]>();
  function fakeToggleButton(id: string): FakeElement {
    const listeners: ((ev: unknown) => void)[] = [];
    toggleListeners.set(id, listeners);
    return fakeElement({
      tag: "button",
      dataset: { projectToggle: id },
      getAttribute(name) {
        return name === "data-project-toggle" ? id : null;
      },
      addEventListener(type, fn) {
        if (type === "click") listeners.push(fn);
      },
    });
  }

  const groups: ProjectWithSessions[] = [];

  const container = fakeElement({
    tag: "aside",
    dataset: { sidebar: "" },
    closest(sel) {
      return sel === "[data-workspace]" ? workspace : null;
    },
    querySelector(sel) {
      const match = /^\[data-project-toggle="([^"]+)"\]$/.exec(sel);
      return match !== null ? fakeToggleButton(match[1]!) : null;
    },
    querySelectorAll(sel) {
      return sel === "[data-project-toggle]"
        ? groups.map((g) => fakeToggleButton(g.project.id))
        : [];
    },
  });

  const sidebarModel = {
    groups,
    selectedSessionID: null,
    error: null,
    gitIdentities: null,
    creatingSession: false,
    applyProjectColors() {},
        api: {
      getLayout: () => layoutPromise,
      saveLayout: (next: SidebarLayout) => Promise.resolve(next),
    },
  } as unknown as SidebarModel;

  const sidebar = new Sidebar(container as unknown as HTMLElement, sidebarModel);

  return {
    sidebar,
    container,
    resolveLayout: layoutResolve!,
    setGroups(nextGroups) {
      groups.length = 0;
      groups.push(...nextGroups);
    },
  };
}

test("the sidebar applies a saved layout that resolves before the projects load", async () => {
  const originalRAF = (globalThis as unknown as { requestAnimationFrame?: (cb: () => void) => number }).requestAnimationFrame;
  const originalWindow = (globalThis as unknown as { window?: unknown }).window;
  const originalDocument = (globalThis as unknown as { document?: unknown }).document;
  const originalCSS = (globalThis as unknown as { CSS?: unknown }).CSS;

  const raf: (() => void)[] = [];
  (globalThis as unknown as { requestAnimationFrame: (cb: () => void) => number }).requestAnimationFrame = (cb) => {
    raf.push(cb);
    return 0;
  };
  (globalThis as unknown as { window: unknown }).window = {
    matchMedia: () => ({ matches: false }),
    setInterval: () => 0,
    addEventListener: () => {},
    removeEventListener: () => {},
    clearInterval: () => {},
  };
  (globalThis as unknown as { document: unknown }).document = {
    createElement: () => fakeElement({ tag: "div" }),
  };
  (globalThis as unknown as { CSS: unknown }).CSS = { escape: (s: string) => s };

  try {
    const harness = makeDeferredLayoutHarness(raf);
    const layout: SidebarLayout = { expanded: ["p2"], projects: ["p3", "p1", "p2"], sessions: {} };

    // The layout document wins the race and resolves before any project arrives.
    harness.resolveLayout(layout);
    await Promise.resolve();
    assert.deepEqual(projectOrderInHtml(harness.container.innerHTML), [],
      "no project rows render before the project list is known");

    // Projects arrive afterwards, the way live.ts loads them through refresh().
    harness.setGroups([
      { project: { id: "p1", name: "alpha", canonical_path: "/srv/alpha", last_used_at: "t" }, sessions: [] },
      { project: { id: "p2", name: "beta", canonical_path: "/srv/beta", last_used_at: "t" }, sessions: [] },
      { project: { id: "p3", name: "gamma", canonical_path: "/srv/gamma", last_used_at: "t" }, sessions: [] },
    ]);
    harness.sidebar.render();

    assert.deepEqual(projectOrderInHtml(harness.container.innerHTML), ["p3", "p1", "p2"],
      "saved project order is applied once projects are known");
    const p2Rail = harness.container.innerHTML.match(/id="sessions-p2"[^>]*>/);
    assert.notEqual(p2Rail, null, "p2 sessions rail should render");
    assert.ok(p2Rail !== null && !p2Rail[0]!.includes("hidden"),
      "p2 should be expanded from the saved layout");
    const p1Rail = harness.container.innerHTML.match(/id="sessions-p1"[^>]*>/);
    assert.notEqual(p1Rail, null, "p1 sessions rail should render");
    assert.ok(p1Rail !== null && p1Rail[0]!.includes("hidden"),
      "p1 should stay collapsed when the saved layout names a different project");

    harness.sidebar.dispose();
  } finally {
    (globalThis as unknown as { requestAnimationFrame?: (cb: () => void) => number }).requestAnimationFrame = originalRAF;
    (globalThis as unknown as { window?: unknown }).window = originalWindow;
    (globalThis as unknown as { document?: unknown }).document = originalDocument;
    (globalThis as unknown as { CSS?: unknown }).CSS = originalCSS;
  }
});

// Keyboard-navigation tests for the sidebar list. The class needs
// a DOM, so this harness builds a small tree by hand and wires the minimal
// selector/event support the list handler requires.

interface FakeDomElement {
  tag: string;
  tagName: string;
  attributes: Record<string, string>;
  classNames: Set<string>;
  hidden: boolean;
  children: FakeDomElement[];
  parent: FakeDomElement | null;
  listeners: Map<string, ((ev: unknown) => void)[]>;
  getAttribute(name: string): string | null;
  setAttribute(name: string, value: string): void;
  hasAttribute(name: string): boolean;
  closest(selector: string): FakeDomElement | null;
  querySelector(selector: string): FakeDomElement | null;
  querySelectorAll(selector: string): FakeDomElement[];
  matches(selector: string): boolean;
  addEventListener(type: string, fn: (ev: unknown) => void): void;
  removeEventListener(type: string, fn: (ev: unknown) => void): void;
  dispatchEvent(event: FakeKeyboardEvent): void;
  focus(): void;
  append(child: FakeDomElement): void;
  replaceChildren(): void;
  classList: { contains(cls: string): boolean };
  dataset: Record<string, string>;
  style: Record<string, string>;
  isConnected: boolean;
  innerHTML: string;
  offsetWidth: number;
  getBoundingClientRect(): FakeRect;
}

interface FakeKeyboardEvent {
  type?: string;
  target: FakeDomElement;
  currentTarget?: FakeDomElement;
  relatedTarget?: FakeDomElement | null;
  key?: string;
  stopped?: boolean;
  preventDefault(): void;
  stopPropagation(): void;
}

function fakeDomElement(tag: string, attrs: Record<string, string | boolean | undefined> = {}): FakeDomElement {
  const attributes: Record<string, string> = {};
  const classNames = new Set<string>();
  let hidden = false;
  for (const [key, value] of Object.entries(attrs)) {
    if (value === undefined) continue;
    if (key === "class") {
      for (const cls of String(value).split(/\s+/).filter(Boolean)) classNames.add(cls);
    } else if (key === "hidden") {
      hidden = value === true || value === "true" || value === "";
    } else {
      attributes[key] = String(value);
    }
  }
  const listeners = new Map<string, ((ev: unknown) => void)[]>();
  const self: FakeDomElement = {
    tag,
    tagName: tag.toUpperCase(),
    attributes,
    classNames,
    hidden,
    children: [],
    parent: null,
    listeners,
    getAttribute(name) {
      if (name === "hidden") return hidden ? "" : null;
      return attributes[name] ?? null;
    },
    setAttribute(name, value) {
      if (name === "hidden") {
        hidden = value === "" || value === "true";
      } else {
        attributes[name] = value;
        if (name === "class") {
          classNames.clear();
          for (const cls of value.split(/\s+/).filter(Boolean)) classNames.add(cls);
        }
      }
    },
    hasAttribute(name) {
      return name === "hidden" ? hidden : attributes[name] !== undefined;
    },
    closest(selector) {
      let node: FakeDomElement | null = self;
      while (node !== null) {
        if (node.matches(selector)) return node;
        node = node.parent;
      }
      return null;
    },
    querySelector(selector) {
      return queryDom(self, selector)[0] ?? null;
    },
    querySelectorAll(selector) {
      return queryDom(self, selector);
    },
    matches(selector) {
      return matchesSelector(self, selector);
    },
    addEventListener(type, fn) {
      const list = listeners.get(type) ?? [];
      list.push(fn);
      listeners.set(type, list);
    },
    removeEventListener(type, fn) {
      const list = listeners.get(type);
      if (list === undefined) return;
      const index = list.indexOf(fn);
      if (index !== -1) list.splice(index, 1);
    },
    dispatchEvent(event) {
      const type = event.type ?? "keydown";
      const path: FakeDomElement[] = [];
      let node: FakeDomElement | null = self;
      while (node !== null) {
        path.unshift(node);
        node = node.parent;
      }
      for (const el of path) {
        if (event.stopped) break;
        event.currentTarget = el;
        const list = el.listeners.get(type) ?? [];
        for (const fn of list) fn(event);
      }
    },
    focus() {
      const doc = (globalThis as unknown as { document?: { activeElement?: FakeDomElement | null } }).document;
      const previous = doc?.activeElement ?? null;
      if (doc !== undefined) doc.activeElement = self;
      const event: FakeKeyboardEvent = {
        type: "focusin",
        target: self,
        relatedTarget: previous,
        preventDefault() {},
        stopPropagation() { event.stopped = true; },
        stopped: false,
      };
      self.dispatchEvent(event);
    },
    append(child) {
      child.parent = self;
      self.children.push(child);
    },
    replaceChildren() {
      for (const child of self.children) child.parent = null;
      self.children = [];
    },
    classList: {
      contains(cls) {
        return classNames.has(cls);
      },
    },
    dataset: {},
    style: { top: "", left: "" },
    isConnected: true,
    innerHTML: "",
    offsetWidth: 0,
    getBoundingClientRect: () => rect(0, 0, 0, 0),
  };
  const syncDataset = () => {
    self.dataset = {};
    for (const [key, value] of Object.entries(attributes)) {
      if (key.startsWith("data-")) {
        const dataKey = key.slice(5).replace(/-([a-z])/g, (_, letter) => letter.toUpperCase());
        self.dataset[dataKey] = value;
      }
    }
  };
  syncDataset();
  const originalSetAttribute = self.setAttribute.bind(self);
  self.setAttribute = (name, value) => {
    originalSetAttribute(name, value);
    syncDataset();
  };
  return self;
}

function tokenizeSelector(selector: string): string[] {
  return selector.trim().split(/\s+/).filter(Boolean);
}

function matchesSimple(el: FakeDomElement, part: string): boolean {
  // Attribute selectors: [attr], [attr=value], [attr="value"]
  const attrMatch = /^\[([^\]=]+)(?:=["']?([^"\]]*)["']?)?\]$/.exec(part);
  if (attrMatch !== null) {
    const name = attrMatch[1]!;
    const value = attrMatch[2];
    if (value === undefined) return el.hasAttribute(name);
    return el.getAttribute(name) === value;
  }
  // ID selector
  if (part.startsWith("#")) return el.getAttribute("id") === part.slice(1);
  // Compound: tag.class1.class2 or .class1.class2 or tag
  const pieces = part.split(".");
  const tag = pieces[0]!;
  if (tag !== "" && el.tag.toLowerCase() !== tag.toLowerCase()) return false;
  for (const cls of pieces.slice(1)) {
    if (!el.classList.contains(cls)) return false;
  }
  return true;
}

function matchesSelector(el: FakeDomElement, selector: string): boolean {
  const parts = tokenizeSelector(selector);
  if (parts.length === 0) return false;
  return parts.every((part) => matchesSimple(el, part));
}

function queryDom(root: FakeDomElement, selector: string): FakeDomElement[] {
  const result: FakeDomElement[] = [];
  const parts = tokenizeSelector(selector);
  function walk(node: FakeDomElement, depth: number): void {
    if (depth >= parts.length) return;
    const part = parts[depth]!;
    const isLast = depth === parts.length - 1;
    for (const child of node.children) {
      const matched = matchesSimple(child, part);
      if (matched && isLast) {
        result.push(child);
      }
      if (!matched || !isLast) {
        walk(child, matched ? depth + 1 : depth);
      }
    }
  }
  walk(root, 0);
  return result;
}

interface KeyboardHarness {
  sidebar: Sidebar;
  container: FakeDomElement;
  list: FakeDomElement;
  toggleP1: FakeDomElement;
  toggleP2: FakeDomElement;
  sessionS1: FakeDomElement;
  sessionS2: FakeDomElement;
  sessionS3: FakeDomElement;
  railP1: FakeDomElement;
  railP2: FakeDomElement;
  input: FakeDomElement;
  focused(): FakeDomElement | null;
  keydown(target: FakeDomElement, key: string): { defaultPrevented: boolean; propagationStopped: boolean };
}

function makeKeyboardHarness(): KeyboardHarness {
  const workspace = fakeDomElement("div", { "data-workspace": "" });
  const modalsRegion = fakeDomElement("div", { "data-region": "modals" });
  const popovers = fakeDomElement("div", { "data-region": "popovers" });
  workspace.append(modalsRegion);
  workspace.append(popovers);

  const container = fakeDomElement("aside", { "data-sidebar": "" });
  workspace.append(container);

  const collapse = fakeDomElement("button", { "data-collapse": "" });
  const chrome = fakeDomElement("div", { class: "chrome chrome-flush" });
  chrome.append(collapse);

  const list = fakeDomElement("ul", { "data-project-list": "", role: "listbox" });

  const liP1 = fakeDomElement("li", { "data-project": "p1", "data-project-row": "", class: "rounded-control" });
  const toggleP1 = fakeDomElement("button", {
    "data-project-toggle": "p1",
    "data-list-row": "",
    role: "option",
    tabindex: "-1",
    "aria-expanded": "true",
    "aria-controls": "sessions-p1",
  });
  const railP1 = fakeDomElement("ul", { id: "sessions-p1", class: "session-rail" });
  const sessionS1 = fakeDomElement("li", { "data-session": "s1", "data-list-row": "", role: "option", tabindex: "-1", "aria-selected": "false" });
  const sessionS2 = fakeDomElement("li", { "data-session": "s2", "data-list-row": "", role: "option", tabindex: "-1", "aria-selected": "false" });
  railP1.append(sessionS1);
  railP1.append(sessionS2);
  liP1.append(toggleP1);
  liP1.append(railP1);

  const liP2 = fakeDomElement("li", { "data-project": "p2", "data-project-row": "", class: "rounded-control" });
  const toggleP2 = fakeDomElement("button", {
    "data-project-toggle": "p2",
    "data-list-row": "",
    role: "option",
    tabindex: "-1",
    "aria-expanded": "false",
    "aria-controls": "sessions-p2",
  });
  const railP2 = fakeDomElement("ul", { id: "sessions-p2", class: "session-rail", hidden: true });
  const sessionS3 = fakeDomElement("li", { "data-session": "s3", "data-list-row": "", role: "option", tabindex: "-1", "aria-selected": "false" });
  railP2.append(sessionS3);
  liP2.append(toggleP2);
  liP2.append(railP2);

  list.append(liP1);
  list.append(liP2);

  const scroll = fakeDomElement("div", { class: "flex min-h-0 flex-1 flex-col gap-px overflow-y-auto px-4 pb-4 pt-2" });
  scroll.append(fakeDomElement("div", { "data-projects-label": "" }));
  scroll.append(list);

  container.append(chrome);
  container.append(scroll);

  // A search input sitting outside the list lets us verify the handler returns
  // early for fields that handle their own keys.
  const input = fakeDomElement("input", { type: "search", "data-sidebar-search": "" });
  container.append(input);

  const session1: SessionSummary = {
    id: "s1",
    project_id: "p1",
    provider: "claude",
    display_name: "",
    status: "running",
    generation: 1,
    created_at: "t",
    updated_at: "t",
    recovery_capability: "none",
    recovery_state: "unavailable",
    presentation_state: "running",
    color: null,
  };
  const session2: SessionSummary = { ...session1, id: "s2" };
  const session3: SessionSummary = { ...session1, id: "s3", project_id: "p2" };

  const sidebarModel = {
    groups: [
      { project: { id: "p1", name: "alpha", canonical_path: "/srv/alpha", last_used_at: "t" }, sessions: [session1, session2], color: null, loadError: null },
      { project: { id: "p2", name: "beta", canonical_path: "/srv/beta", last_used_at: "t" }, sessions: [session3], color: null, loadError: null },
    ],
    selectedSessionID: null,
    error: null,
    gitIdentities: null,
    creatingSession: false,
    applyProjectColors() {},
        select(id: string) {
      (this as { selectedSessionID: string | null }).selectedSessionID = id;
      return true;
    },
    api: {
      getLayout: () => Promise.resolve(null),
      saveLayout: (next: unknown) => Promise.resolve(next),
    },
  } as unknown as SidebarModel;

  const sidebar = new Sidebar(container as unknown as HTMLElement, sidebarModel);

  // renderProjectList mutates state but cannot rebuild this fake DOM from an
  // HTML string. After the real method runs, mirror the new expansion state
  // onto the existing tree so visibleListRows() stays accurate.
  const realRenderProjectList = (sidebar as unknown as { renderProjectList(): void }).renderProjectList.bind(sidebar);
  (sidebar as unknown as { renderProjectList(): void }).renderProjectList = () => {
    realRenderProjectList();
    const expanded = (sidebar as unknown as { expandedProjects: Set<string> }).expandedProjects;
    for (const { toggle, rail } of [{ toggle: toggleP1, rail: railP1 }, { toggle: toggleP2, rail: railP2 }]) {
      const id = toggle.getAttribute("data-project-toggle")!;
      const isExpanded = expanded.has(id);
      rail.hidden = !isExpanded;
      toggle.setAttribute("aria-expanded", String(isExpanded));
    }
  };

  return {
    sidebar,
    container,
    list,
    toggleP1,
    toggleP2,
    sessionS1,
    sessionS2,
    sessionS3,
    railP1,
    railP2,
    input,
    focused() {
      return (globalThis as unknown as { document?: { activeElement?: FakeDomElement | null } }).document?.activeElement ?? null;
    },
    keydown(target, key) {
      const outcome = { defaultPrevented: false, propagationStopped: false };
      const event: FakeKeyboardEvent = {
        target,
        key,
        preventDefault() { outcome.defaultPrevented = true; },
        stopPropagation() { outcome.propagationStopped = true; event.stopped = true; },
        stopped: false,
      };
      target.dispatchEvent(event);
      return outcome;
    },
  };
}

function withKeyboardEnv(run: (harness: KeyboardHarness) => void): void {
  const originalRAF = (globalThis as unknown as { requestAnimationFrame?: (cb: () => void) => number }).requestAnimationFrame;
  const originalWindow = (globalThis as unknown as { window?: unknown }).window;
  const originalDocument = (globalThis as unknown as { document?: unknown }).document;
  const originalCSS = (globalThis as unknown as { CSS?: unknown }).CSS;

  (globalThis as unknown as { requestAnimationFrame: (cb: () => void) => number }).requestAnimationFrame = () => 0;
  (globalThis as unknown as { window: unknown }).window = {
    matchMedia: () => ({ matches: false }),
    setInterval: () => 0,
    addEventListener: () => {},
    removeEventListener: () => {},
  };
  (globalThis as unknown as { document: unknown }).document = {
    activeElement: null,
    createElement: () => fakeDomElement("div"),
  };
  (globalThis as unknown as { CSS: unknown }).CSS = { escape: (s: string) => s };

  const harness = makeKeyboardHarness();

  try {
    run(harness);
  } finally {
    (globalThis as unknown as { requestAnimationFrame?: (cb: () => void) => number }).requestAnimationFrame = originalRAF;
    (globalThis as unknown as { window?: unknown }).window = originalWindow;
    (globalThis as unknown as { document?: unknown }).document = originalDocument;
    (globalThis as unknown as { CSS?: unknown }).CSS = originalCSS;
  }
}

test("Down from the last visible row of an expanded project reaches the next project", () => {
  withKeyboardEnv(({ sessionS2, toggleP2, keydown }) => {
    sessionS2.focus();
    keydown(sessionS2, "ArrowDown");
    assert.equal(sessionS2.getAttribute("tabindex"), "-1");
    assert.equal(toggleP2.getAttribute("tabindex"), "0");
    assert.equal((globalThis as unknown as { document?: { activeElement?: FakeDomElement | null } }).document?.activeElement, toggleP2);
  });
});

test("Left on a project collapses it and its sessions leave the walk", () => {
  withKeyboardEnv(({ toggleP1, sessionS1, sessionS2, toggleP2, keydown }) => {
    toggleP1.focus();
    const outcome = keydown(toggleP1, "ArrowLeft");
    assert.equal(outcome.defaultPrevented, true, "the collapse key is handled");
    assert.equal(toggleP1.getAttribute("aria-expanded"), "false");
    assert.equal(sessionS1.closest("ul.session-rail")?.hidden, true, "the session rail is hidden");
    // With sessions hidden, Down from the project should jump to the next project.
    keydown(toggleP1, "ArrowDown");
    assert.equal((globalThis as unknown as { document?: { activeElement?: FakeDomElement | null } }).document?.activeElement, toggleP2);
  });
});

test("Right on a project expands it and its sessions rejoin the walk", () => {
  withKeyboardEnv(({ toggleP2, sessionS3, keydown }) => {
    toggleP2.focus();
    keydown(toggleP2, "ArrowRight");
    assert.equal(toggleP2.getAttribute("aria-expanded"), "true");
    assert.equal(sessionS3.closest("ul.session-rail")?.hidden, false, "the session rail is visible");
    // Down from the project header now reaches its first session.
    keydown(toggleP2, "ArrowDown");
    assert.equal((globalThis as unknown as { document?: { activeElement?: FakeDomElement | null } }).document?.activeElement, sessionS3);
  });
});

test("Left and Right on a session row change nothing", () => {
  withKeyboardEnv(({ sessionS1, railP1, keydown }) => {
    sessionS1.focus();
    const left = keydown(sessionS1, "ArrowLeft");
    const right = keydown(sessionS1, "ArrowRight");
    assert.equal(left.defaultPrevented, false, "Left on a session is not handled");
    assert.equal(right.defaultPrevented, false, "Right on a session is not handled");
    assert.equal(railP1.hidden, false, "the parent project stays expanded");
  });
});

test("Enter on a control inside a row is left to the control", () => {
  withKeyboardEnv(({ sessionS1, keydown }) => {
    // The session row menu trigger is a tab stop of its own; the list must
    // not swallow its activation and select the session instead.
    const trigger = fakeDomElement("button", { "data-session-menu": "s1" });
    sessionS1.append(trigger);
    trigger.focus();
    const outcome = keydown(trigger, "Enter");
    assert.equal(outcome.defaultPrevented, false, "Enter reaches the trigger");
    assert.equal(outcome.propagationStopped, false, "the event is not stopped");
  });
});

test("arrows do nothing while a search input has focus", () => {
  withKeyboardEnv(({ input, toggleP1, keydown }) => {
    input.focus();
    const outcome = keydown(input, "ArrowDown");
    assert.equal(outcome.defaultPrevented, false, "arrow is ignored in the input");
    assert.equal((globalThis as unknown as { document?: { activeElement?: FakeDomElement | null } }).document?.activeElement, input);
    assert.equal(toggleP1.getAttribute("tabindex"), "0", "the roving tabindex target is unchanged");
  });
});

test("the focused row survives a re-render", () => {
  withKeyboardEnv(({ sidebar, sessionS2, keydown }) => {
    sessionS2.focus();
    assert.equal((globalThis as unknown as { document?: { activeElement?: FakeDomElement | null } }).document?.activeElement, sessionS2);
    (sidebar as unknown as { renderProjectList(): void }).renderProjectList();
    assert.equal(sessionS2.getAttribute("tabindex"), "0");
    assert.equal((globalThis as unknown as { document?: { activeElement?: FakeDomElement | null } }).document?.activeElement, sessionS2);
  });
});




// Auto-color project tints must reach the session tiles at render time,
// without overwriting a user-assigned session tag and without touching
// the backend. These tests render through the real Sidebar class with a fake
// DOM so the wiring is exercised, not just the pure colour helper.

interface ColorHarness {
  sidebar: Sidebar;
  container: FakeElement;
  list: FakeElement;
  sidebarModel: SidebarModel;
  popovers: FakeElement;
  groups: ProjectWithSessions[];
  raf: (() => void)[];
  savedLayouts: SidebarLayout[];
  lastMenuHtml(): string;
  // menuRow returns the mounted menu's row button for a bare attribute
  // selector (for example "data-project-menu-apply-color"), so a test can
  // click the row the production wiring bound its listener to.
  menuRow(attr: string): FakeElement;
  // menuSwatch returns the mounted menu's swatch button for a full
  // attribute selector (for example 'data-sess-swatch="silver"'), so a
  // test can click the exact swatch the strip rendered.
  menuSwatch(selector: string): FakeElement;
}

function makeColorHarness(
  initialGroups: ProjectWithSessions[],
  raf: (() => void)[],
  model?: SidebarModel,
): ColorHarness {
  const groups = [...initialGroups];

  let menuHtml = "";
  let currentBackdrop: FakeElement | null = null;
  const menuRows = new Map<string, FakeElement>();
  const menuSwatches = new Map<string, FakeElement>();
  const popovers = fakeElement({
    tag: "div",
    querySelector(sel) {
      return sel === "[data-popover-backdrop]" ? currentBackdrop : null;
    },
  });
  Object.defineProperty(popovers, "innerHTML", {
    get() {
      return menuHtml;
    },
    set(value: string) {
      menuHtml = value;
      menuRows.clear();
      menuSwatches.clear();
      const menu = fakeElement({
        tag: "div",
        className: "menu absolute",
        offsetWidth: 120,
        // The mounted menu resolves a row exactly when its markup carries
        // the attribute, the way parsing the same HTML would: a selector
        // the surface never rendered stays unreachable.
        querySelector(sel) {
          const attr = /^\[([a-z][a-z0-9-]*)\]$/.exec(sel)?.[1];
          if (attr === undefined || !menuHtml.includes(attr)) return null;
          let row = menuRows.get(attr);
          if (row === undefined) {
            row = fakeElement({ tag: "button" });
            menuRows.set(attr, row);
          }
          return row;
        },
        // The swatch strip binds one listener per keyed swatch through
        // querySelectorAll, so it resolves one element per key the markup
        // actually rendered: a click lands on the exact swatch the strip
        // shows, and a key the surface never rendered stays unreachable.
        querySelectorAll(sel) {
          const attr = /^\[([a-z][a-z0-9-]*)\]$/.exec(sel)?.[1];
          if (attr === undefined || !menuHtml.includes(attr)) return [];
          const prop = attr.replace(/^data-/, "").replace(/-([a-z])/g, (_dash: string, c: string) => c.toUpperCase());
          return [...menuHtml.matchAll(new RegExp(`${attr}="([^"]+)"`, "g"))].map((match) => {
            const selector = `${attr}="${match[1]!}"`;
            let swatch = menuSwatches.get(selector);
            if (swatch === undefined) {
              swatch = fakeElement({ tag: "button", dataset: { [prop]: match[1]! } });
              menuSwatches.set(selector, swatch);
            }
            return swatch;
          });
        },
      });
      currentBackdrop = fakeElement({
        tag: "div",
        dataset: { "popover-backdrop": "" },
        querySelector(sel) {
          return sel === "[role=menu]" ? menu : null;
        },
        querySelectorAll() {
          return [];
        },
      });
    },
  });

  const workspace = fakeElement({
    tag: "div",
    dataset: { workspace: "" },
    querySelector(sel) {
      if (sel === '[data-region="popovers"]') return popovers;
      if (sel === '[data-region="modals"]') return fakeElement({ tag: "div" });
      return null;
    },
  });

  const list = fakeElement({
    tag: "ul",
    dataset: { projectList: "" },
    querySelectorAll(sel: string) {
      return sel === "[data-project-row]" ? [...rowEls.values()] : [];
    },
    querySelector(sel: string) {
      const match = /^\[data-project="([^"]+)"\]$/.exec(sel);
      if (match === null) return null;
      return rowEls.get(match[1]!) ?? null;
    },
  });

  const menuButtons = new Map<string, FakeElement>();
  const sessionMenuButtons = new Map<string, FakeElement>();
  const projectRows = new Map<string, FakeElement>();
  const sessionRows = new Map<string, FakeElement>();
  const rowEls = new Map<string, FakeElement>();

  const container = fakeElement({
    tag: "aside",
    dataset: { sidebar: "" },
    closest(sel) {
      return sel === "[data-workspace]" ? workspace : null;
    },
    querySelector(sel) {
      if (sel === "[data-project-list]") return list;
      const menuMatch = /^\[data-project-menu="([^"]+)"\]$/.exec(sel);
      if (menuMatch !== null) return menuButtons.get(menuMatch[1]!) ?? null;
      const sessionMenuMatch = /^\[data-session-menu="([^"]+)"\]$/.exec(sel);
      if (sessionMenuMatch !== null) return sessionMenuButtons.get(sessionMenuMatch[1]!) ?? null;
      const rowMatch = /^\[data-project="([^"]+)"\] \[class~="group\/project"\]$/.exec(sel);
      if (rowMatch !== null) return projectRows.get(rowMatch[1]!) ?? null;
      const sessionRowMatch = /^\[data-session="([^"]+)"\]$/.exec(sel);
      if (sessionRowMatch !== null) return sessionRows.get(sessionRowMatch[1]!) ?? null;
      return null;
    },
    querySelectorAll(sel) {
      if (sel === "[data-project-menu]") return [...menuButtons.values()];
      if (sel === "[data-project-row]") return [...rowEls.values()];
      return [];
    },
  });

  for (const g of groups) {
    const id = g.project.id;
    menuButtons.set(
      id,
      fakeElement({
        tag: "button",
        dataset: { projectMenu: id },
        className: "project-menu-trigger",
      }),
    );
    projectRows.set(
      id,
      fakeElement({
        tag: "div",
        className: "group/project relative flex items-center",
        getBoundingClientRect() {
          return rect(40, 20, 80, 240);
        },
      }),
    );
    for (const session of g.sessions) {
      sessionMenuButtons.set(
        session.id,
        fakeElement({
          tag: "button",
          dataset: { sessionMenu: session.id },
        }),
      );
      sessionRows.set(
        session.id,
        fakeElement({
          tag: "li",
          dataset: { session: session.id },
          // A session row is drawn inside its project's <li data-project>,
          // which is what an ancestry walk from the row reads.
          closest(sel) {
            return sel === "[data-project]" ? rowEls.get(id) ?? null : null;
          },
          getBoundingClientRect() {
            return rect(60, 20, 84, 240);
          },
        }),
      );
    }
  }
  for (const [index, g] of groups.entries()) {
    const top = 44 + index * 44;
    rowEls.set(g.project.id, fakeElement({
      tag: "li",
      dataset: { projectRow: "", project: g.project.id },
      getBoundingClientRect: () => rect(top, 20, top + 40, 240),
    }));
  }

  const savedLayouts: SidebarLayout[] = [];
  // The render choke point re-applies saved project colours through the
  // model and re-derives the tints over the presented row order, exactly
  // as the real SidebarModel does; a pick flows back through
  // setProjectColor over the same shared groups array. The preference
  // flag lives in a closure because the literal is cast to SidebarModel,
  // whose own flag is private.
  let autoColorProjects = true;
  const sidebarModel = model ?? ({
    groups,
    selectedSessionID: null,
    error: null,
    gitIdentities: null,
    creatingSession: false,
    applyProjectColors(colors: Record<string, string>, presentedOrder?: readonly string[] | null) {
      for (const g of groups) g.color = colors[g.project.id] ?? null;
      applyAutoColors(groups, autoColorProjects, presentedOrder);
    },
    setAutoColorProjects(value: boolean) {
      autoColorProjects = value;
      applyAutoColors(groups, value);
    },
    setProjectColor(projectID: string, color: string | null) {
      const g = groups.find((x) => x.project.id === projectID);
      if (!g) return Promise.resolve(false);
      g.color = color;
      applyAutoColors(groups, autoColorProjects);
      return Promise.resolve(true);
    },
    api: {
      getLayout: () => Promise.resolve({ expanded: [], projects: [], sessions: {} }),
      saveLayout: (next: SidebarLayout) => {
        savedLayouts.push(next);
        return Promise.resolve(next);
      },
    },
  } as unknown as SidebarModel);

  const sidebar = new Sidebar(container as unknown as HTMLElement, sidebarModel);

  return {
    sidebar,
    container,
    list,
    sidebarModel,
    popovers,
    groups,
    raf,
    savedLayouts,
    lastMenuHtml() {
      return menuHtml;
    },
    menuRow(attr) {
      const row = menuRows.get(attr);
      assert.ok(row !== undefined, `the mounted menu carries a [${attr}] row`);
      return row;
    },
    menuSwatch(selector) {
      const swatch = menuSwatches.get(selector);
      assert.ok(swatch !== undefined, `the mounted menu carries a [${selector}] swatch`);
      return swatch;
    },
  };
}

function withColorEnv(
  groups: ProjectWithSessions[],
  run: (harness: ColorHarness) => void,
): void {
  const originalRAF = (globalThis as unknown as { requestAnimationFrame?: (cb: () => void) => number }).requestAnimationFrame;
  const originalWindow = (globalThis as unknown as { window?: unknown }).window;
  const originalDocument = (globalThis as unknown as { document?: unknown }).document;
  const originalCSS = (globalThis as unknown as { CSS?: unknown }).CSS;

  const raf: (() => void)[] = [];
  (globalThis as unknown as { requestAnimationFrame: (cb: () => void) => number }).requestAnimationFrame = (cb) => {
    raf.push(cb);
    return 0;
  };
  (globalThis as unknown as { window: unknown }).window = {
    matchMedia: () => ({ matches: false }),
    setInterval: () => 0,
    addEventListener: () => {},
    removeEventListener: () => {},
    clearInterval: () => {},
  };
  (globalThis as unknown as { document: unknown }).document = {
    createElement: () => fakeElement({ tag: "div" }),
  };
  (globalThis as unknown as { CSS: unknown }).CSS = { escape: (s: string) => s };

  const harness = makeColorHarness(groups, raf);

  try {
    run(harness);
  } finally {
    (globalThis as unknown as { requestAnimationFrame?: (cb: () => void) => number }).requestAnimationFrame = originalRAF;
    (globalThis as unknown as { window?: unknown }).window = originalWindow;
    (globalThis as unknown as { document?: unknown }).document = originalDocument;
    (globalThis as unknown as { CSS?: unknown }).CSS = originalCSS;
  }
}

// The apply-color click runs the real model's async apply chain before the
// re-render, so this env keeps the DOM globals stubbed until the awaited
// body settles; withColorEnv restores them the moment its synchronous run
// returns, which would pull document and CSS out from under the pending
// render. The model is real over a recording fake api, so the click drives
// the same tint-to-key derivation and session writes the live app does.
interface ModelHarness extends ColorHarness {
  model: SidebarModel;
  writes: { id: string; color: string | null }[];
}

async function withModelEnv(run: (harness: ModelHarness) => Promise<void>): Promise<void> {
  const originalRAF = (globalThis as unknown as { requestAnimationFrame?: (cb: () => void) => number }).requestAnimationFrame;
  const originalWindow = (globalThis as unknown as { window?: unknown }).window;
  const originalDocument = (globalThis as unknown as { document?: unknown }).document;
  const originalCSS = (globalThis as unknown as { CSS?: unknown }).CSS;

  const raf: (() => void)[] = [];
  (globalThis as unknown as { requestAnimationFrame: (cb: () => void) => number }).requestAnimationFrame = (cb) => {
    raf.push(cb);
    return 0;
  };
  (globalThis as unknown as { window: unknown }).window = {
    matchMedia: () => ({ matches: false }),
    setInterval: () => 0,
    addEventListener: () => {},
    removeEventListener: () => {},
    clearInterval: () => {},
  };
  (globalThis as unknown as { document: unknown }).document = {
    createElement: () => fakeElement({ tag: "div" }),
  };
  (globalThis as unknown as { CSS: unknown }).CSS = { escape: (s: string) => s };

  try {
    const projects: Project[] = [
      { id: "p1", name: "alpha", canonical_path: "/srv/alpha", last_used_at: "t" },
    ];
    const stored = [sessionFixture("s1")];
    const writes: { id: string; color: string | null }[] = [];
    const identity: GitIdentityProfile = {
      id: "identity-1", name: "Test Author", email: "test@example.invalid", revision: 1,
      created_at: "t", updated_at: "t",
    };
    const api = {
      listProjects: () => Promise.resolve(projects),
      listProviders: () => Promise.resolve(["claude", "codex", "shell"]),
      listGitIdentities: () => Promise.resolve({ profiles: [identity], default_profile_id: identity.id, revision: 1 }),
      listSessions: (id: string) => Promise.resolve(id === "p1" ? stored : []),
      setSessionColor: (id: string, color: string | null) => {
        writes.push({ id, color });
        const session = stored.find((candidate) => candidate.id === id);
        if (session !== undefined) session.color = color;
        return Promise.resolve();
      },
      getLayout: () => Promise.resolve({ expanded: [], projects: [], sessions: {} }),
      saveLayout: (next: SidebarLayout) => Promise.resolve(next),
    } as unknown as ApiClient;
    const model = new SidebarModel(api, {}, false);
    await model.refresh();
    await run({ ...makeColorHarness(model.groups, raf, model), model, writes });
  } finally {
    (globalThis as unknown as { requestAnimationFrame?: (cb: () => void) => number }).requestAnimationFrame = originalRAF;
    (globalThis as unknown as { window?: unknown }).window = originalWindow;
    (globalThis as unknown as { document?: unknown }).document = originalDocument;
    (globalThis as unknown as { CSS?: unknown }).CSS = originalCSS;
  }
}

function tileSpanForSession(html: string, sessionID: string): string | null {
  // The row's first span is the phone's leading status column; the tile is
  // the rounded-mark span after it.
  const pattern = new RegExp(`<li data-session="${sessionID}"[^>]*>[\\s\\S]*?(<span[^>]*rounded-mark[^>]*>\\s*<\\/span>)`);
  const match = pattern.exec(html);
  return match?.[1] ?? null;
}

// Each project row's chevron carries the row's tint as an inline colour, so
// the (row, hue) pairs read straight off rendered HTML in row order — the
// pairing the rail paints, not the model's array order.
function rowTintsInHtml(html: string): Array<[string, string]> {
  const opens: Array<[string, number]> = [];
  const rowOpen = /<li data-project="([^"]+)"[^>]*>/g;
  for (let match = rowOpen.exec(html); match !== null; match = rowOpen.exec(html)) {
    opens.push([match[1]!, match.index + match[0].length]);
  }
  return opens.map(([id, from], i) => {
    const to = i + 1 < opens.length ? opens[i + 1]![1]! : html.length;
    const tint = /style="color:(var\(--color-mark-[a-z]+\))"/.exec(html.slice(from, to));
    assert.notEqual(tint, null, `row ${id} renders a tinted chevron`);
    return [id, tint![1]!];
  });
}

const sessionFixture = (id: string, overrides: Partial<SessionSummary> = {}): SessionSummary => ({
  id,
  project_id: "p1",
  provider: "claude",
  display_name: "",
  status: "running",
  generation: 1,
  created_at: "t",
  updated_at: "t",
  recovery_capability: "none",
  recovery_state: "unavailable",
  presentation_state: "running",
  color: null,
  ...overrides,
});

const projectFixture = (
  id: string,
  sessions: SessionSummary[],
  color?: string | null,
  autoColor?: string | null,
): ProjectWithSessions => ({
  project: { id, name: id, canonical_path: `/srv/${id}`, last_used_at: "t" },
  sessions,
  color,
  autoColor,
});

test("auto-color reaches a session tile when the session has no colour tag", () => {
  withColorEnv(
    [projectFixture("p1", [sessionFixture("s1")], null, "var(--color-mark-crimson)")],
    ({ container }) => {
      const tile = tileSpanForSession(container.innerHTML, "s1");
      assert.notEqual(tile, null, "session row renders a tile");
      assert.ok(tile!.includes('style="background:var(--color-mark-crimson)"'), "tile inherits the project's auto-color token");
    },
  );
});

test("auto-color does not overwrite a user-assigned session colour tag", () => {
  withColorEnv(
    [projectFixture("p1", [sessionFixture("s1", { color: "amber" })], null, "var(--color-mark-crimson)")],
    ({ container }) => {
      const tile = tileSpanForSession(container.innerHTML, "s1");
      assert.ok(tile!.includes('style="background:var(--color-mark-gold)"'), "session tag wins over project auto-color");
      assert.ok(!tile!.includes("var(--color-mark-crimson)"), "auto-color token is not used");
    },
  );
});

// The render choke point is load-bearing on both of its paths: a menu
// move redraws the list through renderProjectList, and every later full
// render goes through render. Each path must feed the displayed row order
// into the tint derivation — drop the order from either call and the rows
// redraw in the moved sequence while every project keeps its alphabetical
// hue, so two neighbouring rows can wear near-identical hues and the rail
// stops reading as a ramp. The assertions pair each rendered row, in row
// order, with the hue its chevron paints.
test("a moved row takes the hue of the position it lands in", () => {
  withColorEnv(
    [
      projectFixture("p1", []),
      projectFixture("p2", []),
      projectFixture("p3", []),
      projectFixture("p4", []),
    ],
    ({ sidebar, container, list }) => {
      // The fixture names sort p1..p4, so the pre-move ramp is the
      // alphabetical spread.
      assert.deepEqual(rowTintsInHtml(container.innerHTML), [
        ["p1", "var(--color-mark-crimson)"],
        ["p2", "var(--color-mark-lime)"],
        ["p3", "var(--color-mark-cerulean)"],
        ["p4", "var(--color-mark-indigo)"],
      ]);
      // Commit p1 to the last slot the way the row menu does: the saved
      // order changes and renderProjectList repaints the rail from it.
      const shell = sidebar as unknown as {
        projectOrder: string[] | null;
        renderProjectList(): void;
      };
      shell.projectOrder = ["p2", "p3", "p4", "p1"];
      shell.renderProjectList();
      assert.deepEqual(rowTintsInHtml(list.innerHTML), [
        ["p2", "var(--color-mark-crimson)"],
        ["p3", "var(--color-mark-lime)"],
        ["p4", "var(--color-mark-cerulean)"],
        ["p1", "var(--color-mark-indigo)"],
      ], "the list redraw re-derives the ramp over the moved order");
      sidebar.render();
      assert.deepEqual(rowTintsInHtml(container.innerHTML), [
        ["p2", "var(--color-mark-crimson)"],
        ["p3", "var(--color-mark-lime)"],
        ["p4", "var(--color-mark-cerulean)"],
        ["p1", "var(--color-mark-indigo)"],
      ], "a full render derives the ramp from the same order");
    },
  );
});

// The rendered markup is the affordance: a project row without draggable
// starts no browser drag at all, while the session row keeps its in-rail
// reorder gesture.
test("rendered project rows are not draggable while session rows are", () => {
  withColorEnv(
    [projectFixture("p1", [sessionFixture("s1")])],
    ({ container }) => {
      const html = container.innerHTML;
      const projectRow = /<li data-project="p1"[^>]*>/.exec(html);
      assert.notEqual(projectRow, null, "the project row rendered");
      assert.ok(!projectRow![0].includes("draggable"),
        "the project row carries no draggable attribute");
      const sessionRow = /<li data-session="s1"[^>]*>/.exec(html);
      assert.notEqual(sessionRow, null, "the session row rendered");
      assert.ok(sessionRow![0].includes('draggable="true"'),
        "the session row keeps its in-rail drag");
    },
  );
});

// The rename field is chromeless: the row wash plus the
// accent selection on the auto-selected text is the whole affordance. The
// field carries no inset of its own, because an inset shifts the text right on
// entry, and keeps the row's body size and medium weight so the
// glyphs hold their weight while editable.
test("the rename inputs are chromeless and keep the row's type", () => {
  withColorEnv(
    [projectFixture("p1", [sessionFixture("s1")], null, "var(--color-mark-crimson)")],
    ({ sidebar, container }) => {
      sidebar.startRenameSession("s1");
      const sessionInput = /<input data-rename-input[^>]*/.exec(container.innerHTML)?.[0] ?? "";
      assert.ok(sessionInput.includes("text-body font-medium"), "the session input carries the row's body size and medium weight");
      assert.ok(sessionInput.includes("h-[19px]"), "the 19px session box");
      // The class, not the painted pixel: preflight makes an input inherit the
      // body colour, so a field that declares nothing still paints primary.
      assert.ok(sessionInput.includes("text-text-primary"), "the session field declares primary text");
      assert.ok(!sessionInput.includes("border"), "no border on the session field");
      assert.ok(!sessionInput.includes("bg-field"), "no fill behind the editing text");
      assert.ok(!sessionInput.includes("px-[7px]"), "no inset: the text must not move on entry");
      const sessionRow = /<li data-session="s1"[^>]*/.exec(container.innerHTML)?.[0] ?? "";
      assert.ok(sessionRow.includes("bg-wash-pressed"), "the renaming session row holds the pressed wash");
      assert.ok(!sessionRow.includes("hover:bg-wash-hover"), "no hover wash while the row is an input");

      sidebar.startRenameProject("p1");
      const projectInput = /<input data-rename-project-input[^>]*/.exec(container.innerHTML)?.[0] ?? "";
      assert.ok(projectInput.includes("text-body font-medium"), "the project input carries the row's body size and medium weight");
      assert.ok(projectInput.includes("h-5 "), "the 20px project box");
      assert.ok(projectInput.includes("text-text-primary"), "the project field declares primary text");
      assert.ok(!projectInput.includes("border"), "no border on the project field either");
      assert.ok(!projectInput.includes("bg-field"), "no fill here either");
      assert.ok(!projectInput.includes("px-[7px]"), "no inset here either");
      const projectHeader = /<div[^>]*>(?:(?!<\/div>).)*?data-rename-project-input/s.exec(container.innerHTML)?.[0] ?? "";
      assert.ok(projectHeader.includes("bg-wash-selected"), "the renaming project row takes the selected wash");
      assert.ok(projectHeader.includes("rounded-control"), "the wash is rounded like the row it replaces");
    },
  );
});

// A project colour pick is durable or it is decorative: the layout document
// is the store, and a model refresh — which rebuilds groups from the server
// with no colours — must not drop or resurrect it. The map is what survives;
// the render choke point re-applies it.
test("a project colour survives a group rebuild and persists through the layout document", () => {
  withColorEnv(
    [projectFixture("p1", []), projectFixture("p2", [])],
    ({ sidebar, groups, savedLayouts }) => {
      const internals = sidebar as unknown as {
        projectColors: Record<string, string>;
        saveLayout(): void;
        renderProjectList(): void;
        model: { groups: ProjectWithSessions[] };
      };
      internals.projectColors["p1"] = "gray";
      sidebar.render();
      assert.equal(groups[0]!.color, "gray", "the saved map paints the live group");

      // A refresh rebuilt every group: replace the array contents in place,
      // exactly as the model does — same reference, fresh objects, no colours.
      const rebuilt = [projectFixture("p1", []), projectFixture("p2", [])];
      groups.length = 0;
      groups.push(...rebuilt);
      internals.renderProjectList();
      assert.equal(rebuilt[0]!.color, "gray", "the colour re-applies onto the rebuilt groups");
      assert.equal(rebuilt[1]!.color, null, "an id absent from the map is an explicit clear");

      // The document carries the map minus ids the project list no longer has.
      internals.projectColors["gone"] = "red";
      internals.saveLayout();
      assert.deepEqual(savedLayouts[0]!.colors, { p1: "gray" });
    },
  );
});

// An expanded project must not pin its + / cluster visible: expanding every
// project litters the rail with a + on each row. The cluster reveals on the
// row's hover/focus in both states - the Fastmail model - so the resting
// expanded sidebar carries no extra chrome.
test("an expanded project's action cluster stays hidden until the row is hovered", () => {
  withColorEnv(
    [projectFixture("p1", [sessionFixture("s1")])],
    ({ sidebar, container }) => {
      const internals = sidebar as unknown as {
        expandedProjects: Set<string>;
        expansionTouched: boolean;
        render(): void;
      };
      internals.expandedProjects.add("p1");
      internals.expansionTouched = true;
      internals.render();
      const actions = /<span data-project-actions[^>]*/.exec(container.innerHTML)?.[0] ?? "";
      assert.ok(actions.includes("pointer-events-none opacity-0"), "the cluster rests hidden on an expanded row");
      assert.ok(actions.includes("group-hover/project:opacity-100"), "the row's hover reveals it");
    },
  );
});

// The chevron and the session rail read one effective project colour, so a
// clear must clear them together — a chevron that drops its tint while the
// rail keeps painting the old hue is the report that motivated this pin.
test("clearing a project tag clears the chevron and the rail together", () => {
  withColorEnv(
    [projectFixture("p1", [sessionFixture("s1")])],
    ({ sidebar, container, sidebarModel }) => {
      // With the preference on, clearing a tag legitimately shows the auto
      // tint again; this pin is about the tag clear itself, so the tints
      // are off and the cleared state shows nothing.
      sidebarModel.setAutoColorProjects(false);
      const internals = sidebar as unknown as {
        projectColors: Record<string, string>;
        model: { setProjectColor(id: string, c: string | null): Promise<boolean> };
        render(): void;
      };
      internals.projectColors["p1"] = "crimson";
      internals.model.setProjectColor("p1", "crimson");
      sidebar.render();
      const tinted = container.innerHTML;
      assert.match(tinted, /<span aria-hidden="true" style="color:var\(--color-mark-crimson\)"/, "the chevron carries the tint");
      assert.match(tinted, /border-left-color:var\(--color-mark-crimson\)/, "the rail carries the tint");

      delete internals.projectColors["p1"];
      internals.model.setProjectColor("p1", null);
      sidebar.render();
      const cleared = container.innerHTML;
      assert.ok(!cleared.includes("var(--color-mark-crimson)"), "neither the chevron nor the rail keeps the cleared tint");
    },
  );
});

test("a user-assigned project colour does not flow to sessions automatically", () => {
  withColorEnv(
    [projectFixture("p1", [sessionFixture("s1")], "red", null)],
    ({ sidebar, container }) => {
      // A user-assigned colour reaches a row through the saved layout map at
      // the render choke point, not by a fixture pre-setting the group.
      const internals = sidebar as unknown as {
        projectColors: Record<string, string>;
        model: { setProjectColor(id: string, c: string | null): Promise<boolean> };
        render(): void;
      };
      internals.projectColors["p1"] = "red";
      void internals.model.setProjectColor("p1", "red");
      sidebar.render();
      const tile = tileSpanForSession(container.innerHTML, "s1");
      assert.ok(!tile!.includes("style="), "tile keeps its provider hue with no inline override");
      assert.ok(!tile!.includes("var(--color-mark-"), "no auto-color token leaks in");
    },
  );
});

test("turning auto-color off restores the provider hue on every tile", () => {
  withColorEnv(
    [projectFixture("p1", [sessionFixture("s1")], null, "var(--color-mark-crimson)")],
    ({ sidebar, container, sidebarModel }) => {
      assert.ok(tileSpanForSession(container.innerHTML, "s1")!.includes("var(--color-mark-crimson)"), "tile starts tinted");
      sidebarModel.setAutoColorProjects(false);
      sidebar.render();
      const tile = tileSpanForSession(container.innerHTML, "s1");
      assert.ok(!tile!.includes("style="), "tile reverts to provider hue");
      assert.ok(!tile!.includes("var(--color-mark-"), "no auto-color residue remains");
    },
  );
});

test("a project with only an automatic colour offers the clearing row, not the tint", () => {
  withColorEnv(
    [projectFixture("p1", [sessionFixture("s1")], null, "var(--color-mark-crimson)")],
    ({ sidebar, popovers, raf, lastMenuHtml }) => {
      sidebar.openProjectMenu("p1");
      // Drain the single positioning animation frame.
      for (const cb of raf) cb();
      const menu = lastMenuHtml();
      assert.ok(menu.includes("data-project-menu-apply-color"), "the row is offered either way");
      assert.ok(menu.includes("Clear color from sessions"), "an auto tint is not a colour to apply");
      assert.ok(!menu.includes("Apply color to sessions"));
      // Scoped to the row: every hue appears in the swatch strip above it.
      const row = /data-project-menu-apply-color[\s\S]*?<\/button>/.exec(menu)?.[0] ?? "";
      assert.ok(!row.includes("var(--color-mark-crimson)"),
        "the tint the row happens to paint never reaches its dot");
      assert.ok(row.includes("background:transparent"), "the dot is the empty swatch");
      popovers.replaceChildren();
    },
  );
});

test("a user-tagged project still offers its colour to sessions", () => {
  withColorEnv(
    [projectFixture("p1", [sessionFixture("s1")])],
    ({ sidebar, lastMenuHtml }) => {
      (sidebar as unknown as { projectColors: Record<string, string> }).projectColors.p1 = "violet";
      sidebar.render();
      sidebar.openProjectMenu("p1");
      const menu = lastMenuHtml();
      assert.ok(menu.includes("data-project-menu-apply-color"));
      assert.ok(menu.includes("background:var(--color-mark-violet)"));
    },
  );
});

// The row's click wiring is the whole path from the mounted menu to the
// model: the click must close the menu, persist the project's OWN colour as
// every session's tag, and repaint the rail through the session renderer.
test("clicking the colour row persists the project's own tag and repaints the rail", async () => {
  await withModelEnv(async ({ sidebar, model, container, menuRow, writes, raf }) => {
    model.groups[0]!.color = "emerald";
    model.groups[0]!.autoColor = "var(--color-mark-crimson)";
    const outcomes: boolean[] = [];
    let applied: Promise<boolean> | null = null;
    const apply = model.applyProjectColorToSessions.bind(model);
    model.applyProjectColorToSessions = (projectID: string) => {
      const pending = apply(projectID).then((outcome) => {
        outcomes.push(outcome);
        return outcome;
      });
      applied = pending;
      return pending;
    };

    sidebar.openProjectMenu("p1");
    for (const cb of raf) cb();
    menuRow("data-project-menu-apply-color").dispatchEvent({ type: "click" });
    assert.notEqual(applied, null, "the click invoked the model's apply");
    // The shell's re-render is a then-callback on this same promise,
    // registered at click time, so it runs before this await resumes.
    await applied!;
    assert.deepEqual(outcomes, [true], "the click reaches the model's apply");
    assert.deepEqual(writes, [{ id: "s1", color: "emerald" }],
      "the user's key is written, and the tint under it is not");
    const group = model.groups.find((g) => g.project.id === "p1")!;
    assert.equal(group.sessions[0]!.color, "emerald", "the tag survives the model's own refresh");
    const tile = tileSpanForSession(container.innerHTML, "s1");
    assert.notEqual(tile, null, "the session row renders after the re-render");
    assert.ok(tile!.includes('style="background:var(--color-mark-emerald)"'),
      "the renderer paints the session through its tag");
    const internals = sidebar as unknown as { openRowMenuTarget: unknown };
    assert.equal(internals.openRowMenuTarget, null, "the row commits and closes");
  });
});

test("clicking the colour row on an uncoloured project clears its sessions", async () => {
  await withModelEnv(async ({ sidebar, model, menuRow, writes, raf }) => {
    const outcomes: boolean[] = [];
    let applied: Promise<boolean> | null = null;
    const apply = model.applyProjectColorToSessions.bind(model);
    model.applyProjectColorToSessions = (projectID: string) => {
      const pending = apply(projectID).then((outcome) => {
        outcomes.push(outcome);
        return outcome;
      });
      applied = pending;
      return pending;
    };

    model.groups[0]!.autoColor = "var(--color-mark-emerald)";
    sidebar.openProjectMenu("p1");
    for (const cb of raf) cb();
    // The tint can move under the open menu — auto-color re-derives as
    // projects come and go — and neither hue is the project's own, so the
    // write is the same either way.
    model.groups[0]!.autoColor = "var(--color-mark-crimson)";
    menuRow("data-project-menu-apply-color").dispatchEvent({ type: "click" });
    await applied!;

    assert.deepEqual(outcomes, [true], "the clearing action reports success");
    assert.deepEqual(writes, [{ id: "s1", color: null }],
      "no tint is written, and the sessions are cleared instead");
  });
});

// Every swatch the strip offers must persist through the same session
// colour endpoint the marks use. A strip key the server's allowlist
// refuses answers 400 with the menu already closed, so the row keeps its
// old tag and the pick is silently lost; each neutral is clicked through
// the mounted menu and its persisted tag asserted, the exact path a
// user's pick takes.
test("clicking each neutral swatch persists its tag key", async () => {
  await withModelEnv(async ({ sidebar, model, container, menuSwatch, writes, raf }) => {
    const picks: Promise<boolean>[] = [];
    const real = model.setSessionColor.bind(model);
    model.setSessionColor = (sessionID: string, color: string | null) => {
      const pending = real(sessionID, color);
      picks.push(pending);
      return pending;
    };

    for (const key of ["pearl", "silver", "graphite", "ink"]) {
      sidebar.openSessionMenu("s1");
      for (const cb of raf) cb();
      menuSwatch(`data-sess-swatch="${key}"`).dispatchEvent({ type: "click" });
      await picks[picks.length - 1]!;
    }

    assert.deepEqual(writes, [
      { id: "s1", color: "pearl" },
      { id: "s1", color: "silver" },
      { id: "s1", color: "graphite" },
      { id: "s1", color: "ink" },
    ], "each neutral pick posts its own strip key");
    const group = model.groups.find((g) => g.project.id === "p1")!;
    assert.equal(group.sessions[0]!.color, "ink", "the tag survives the model's own refresh");
    const tile = tileSpanForSession(container.innerHTML, "s1");
    assert.notEqual(tile, null, "the session row renders after the re-render");
    assert.ok(tile!.includes('style="background:#0E1B2C"'),
      "the renderer paints the picked neutral through its tag");
  });
});

// A boundary move rewrites the move rows' hints in place. The row holding
// focus showed its old hint in the info bar, so the strip and the attribute
// would disagree until the next focus change; the move re-announces on the
// bar's one update path, the delegated focusin listener on the app root.
test("a boundary move re-announces the focused row's new hint through focusin", () => {
  withColorEnv(
    [projectFixture("p1", []), projectFixture("p2", [])],
    ({ sidebar, menuRow, raf }) => {
      const globals = globalThis as unknown as { FocusEvent?: unknown };
      const originalFocusEvent = globals.FocusEvent;
      globals.FocusEvent = class {
        readonly type: string;
        readonly bubbles: boolean;
        constructor(type: string, init: { bubbles?: boolean } = {}) {
          this.type = type;
          this.bubbles = init.bubbles === true;
        }
      };
      try {
        sidebar.openProjectMenu("p1");
        for (const cb of raf) cb();
        const moveDown = menuRow("data-project-menu-move-down");
        moveDown.matches = (selector: string) => selector === ":focus";
        const announced: { type: string; bubbles: boolean; hint: string | null }[] = [];
        moveDown.addEventListener("focusin", (ev) => {
          const event = ev as { type: string; bubbles: boolean };
          announced.push({ type: event.type, bubbles: event.bubbles, hint: moveDown.getAttribute("data-hint") });
        });

        moveDown.dispatchEvent({ type: "click" });

        assert.equal(moveDown.getAttribute("aria-disabled"), "true", "p1 is last after the move");
        assert.equal(moveDown.getAttribute("data-hint"), "Already last in the sidebar");
        assert.deepEqual(
          announced,
          [{ type: "focusin", bubbles: true, hint: "Already last in the sidebar" }],
          "the focused row re-announces its new hint, bubbling to the info bar's root listener",
        );
      } finally {
        if (originalFocusEvent === undefined) delete globals.FocusEvent;
        else globals.FocusEvent = originalFocusEvent;
      }
    },
  );
});

test("a project-menu move redraws the rail without closing its popover", () => {
  withColorEnv(
    [projectFixture("p1", []), projectFixture("p2", [])],
    ({ sidebar, container, popovers, raf, lastMenuHtml }) => {
      sidebar.openProjectMenu("p1");
      for (const cb of raf) cb();
      const menu = popovers.querySelector("[data-popover-backdrop]")?.querySelector("[role=menu]");
      assert.notEqual(menu, null);

      (sidebar as unknown as {
        moveProjectFromMenu(id: string, direction: "up" | "down", menu: FakeElement): void;
        openRowMenuTarget: { kind: string; id: string } | null;
      }).moveProjectFromMenu("p1", "down", menu!);

      const list = container.querySelector("[data-project-list]");
      assert.notEqual(list, null);
      assert.deepEqual(projectOrderInHtml(list!.innerHTML), ["p2", "p1"], "the rail redraws in its new order");
      assert.ok(lastMenuHtml().includes("data-project-menu-popover"), "the existing popover stays mounted");
      assert.deepEqual(
        (sidebar as unknown as { openRowMenuTarget: { kind: string; id: string } | null }).openRowMenuTarget,
        { kind: "project", id: "p1" },
        "the open menu remains the explicit target",
      );
    },
  );
});

// Move to top is the long move, and with three rows it is distinguishable
// from Move up: the row skips every project above it in one press.
test("a project-menu Move to top lands the row first in one press", () => {
  withColorEnv(
    [projectFixture("p1", []), projectFixture("p2", []), projectFixture("p3", [])],
    ({ sidebar, container, popovers, raf, lastMenuHtml }) => {
      sidebar.openProjectMenu("p3");
      for (const cb of raf) cb();
      const menu = popovers.querySelector("[data-popover-backdrop]")?.querySelector("[role=menu]");
      assert.notEqual(menu, null);
      (sidebar as unknown as {
        moveProjectFromMenu(id: string, move: "up" | "down" | "top", menu: FakeElement): void;
      }).moveProjectFromMenu("p3", "top", menu!);
      const list = container.querySelector("[data-project-list]");
      assert.deepEqual(projectOrderInHtml(list!.innerHTML), ["p3", "p1", "p2"], "one press, not one step");
      assert.ok(lastMenuHtml().includes("data-project-menu-popover"), "the popover stays mounted for the next move");
    },
  );
});

// --- PROJECTS header menu ----------------------------------------------------
// The eyebrow's menubar-style menu: mounted on the shared popover region,
// dropped below-left of the trigger, pressed wash gated on the open state,
// and the disclosure/order rows acting on the sidebar's own state.

interface HeaderRow {
  fire(type: string, ev?: Record<string, unknown>): void;
  el: FakeElement;
}

interface HeaderHarness {
  sidebar: Sidebar;
  anchor: FakeElement;
  popovers: FakeElement;
  raf: (() => void)[];
  menu(): FakeElement | null;
  backdropFire(type: string, ev: Record<string, unknown>): void;
  row(action: string): HeaderRow;
  lastHtml(): string;
  // The menu's keyboard contract and anchor sync live on document- and
  // window-level listeners, so the harness owns fake event targets for
  // both and records every focus() call the production code makes.
  window: FakeEventTarget;
  document: FakeEventTarget & { createElement(): FakeElement };
  fireDocument(type: string, ev?: Record<string, unknown>): void;
  fireWindow(type: string, ev?: Record<string, unknown>): void;
  focused: FakeElement[];
  rowEls(): FakeElement[];
  detachAnchor(): void;
}

interface FakeEventTarget {
  addEventListener(type: string, fn: (ev: unknown) => void): void;
  removeEventListener(type: string, fn: (ev: unknown) => void): void;
}

function listenerRegistry(): Map<string, ((ev: unknown) => void)[]> & FakeEventTarget {
  const listeners = new Map<string, ((ev: unknown) => void)[]>();
  return Object.assign(listeners, {
    addEventListener(type: string, fn: (ev: unknown) => void): void {
      const list = listeners.get(type) ?? [];
      list.push(fn);
      listeners.set(type, list);
    },
    removeEventListener(type: string, fn: (ev: unknown) => void): void {
      const list = listeners.get(type);
      if (list === undefined) return;
      const index = list.indexOf(fn);
      if (index !== -1) list.splice(index, 1);
    },
  });
}

function makeHeaderHarness(
  raf: (() => void)[],
  env: {
    window: HeaderHarness["window"];
    document: HeaderHarness["document"];
    fireDocument(type: string, ev?: Record<string, unknown>): void;
    fireWindow(type: string, ev?: Record<string, unknown>): void;
  },
): HeaderHarness {
  let backdrop: FakeElement | null = null;
  let menuEl: FakeElement | null = null;
  let html = "";
  const rows = new Map<string, HeaderRow>();
  const backdropListeners = new Map<string, ((ev: unknown) => void)[]>();
  const focused: FakeElement[] = [];
  const popovers = fakeElement({
    querySelector(sel) {
      return sel === "[data-popover-backdrop]" ? backdrop : null;
    },
    replaceChildren() {
      backdrop = null;
      menuEl = null;
    },
  });
  Object.defineProperty(popovers, "innerHTML", {
    get() {
      return html;
    },
    set(value: string) {
      html = value;
      rows.clear();
      for (const match of value.matchAll(/<button[^>]*role="menuitem"[^>]*data-(projects-menu-[a-z-]+)/g)) {
        const listeners = new Map<string, ((ev: unknown) => void)[]>();
        const el = fakeElement({
          tag: "button",
          addEventListener(type, fn) {
            const list = listeners.get(type) ?? [];
            list.push(fn);
            listeners.set(type, list);
          },
        });
        el.focus = () => focused.push(el);
        rows.set(match[1]!, {
          el,
          fire(type, ev = {}) {
            for (const fn of listeners.get(type) ?? []) fn(ev);
          },
        });
      }
      menuEl = fakeElement({
        tag: "div",
        className: "menu absolute",
        querySelector(sel) {
          const action = /^\[data-(projects-menu-[a-z-]+)\]$/.exec(sel);
          if (action !== null) return rows.get(action[1]!)?.el ?? null;
          if (sel === "[role=menuitem]") return rows.values().next().value?.el ?? null;
          return null;
        },
        querySelectorAll(sel) {
          return sel === '[role="menuitem"]' ? [...rows.values()].map((r) => r.el) : [];
        },
      });
      backdrop = fakeElement({
        tag: "div",
        addEventListener(type, fn) {
          const list = backdropListeners.get(type) ?? [];
          list.push(fn);
          backdropListeners.set(type, list);
        },
        querySelector(sel) {
          return sel === "[role=menu]" ? menuEl : null;
        },
      });
    },
  });

  const modalsRegion = fakeElement({ tag: "div" });
  const workspace = fakeElement({
    tag: "div",
    dataset: { workspace: "" },
    querySelector(sel) {
      if (sel === '[data-region="popovers"]') return popovers;
      if (sel === '[data-region="modals"]') return modalsRegion;
      return null;
    },
  });

  const anchor = fakeElement({
    tag: "button",
    dataset: { projectsMenu: "" },
    getBoundingClientRect() {
      return rect(10, 16, 36, 90);
    },
  });
  anchor.setAttribute("aria-expanded", "false");
  anchor.focus = () => focused.push(anchor);

  const container = fakeElement({
    tag: "aside",
    closest(sel) {
      return sel === "[data-workspace]" ? workspace : null;
    },
    querySelector(sel) {
      return sel === "[data-projects-menu]" ? anchor : null;
    },
    querySelectorAll() {
      return [];
    },
  });

  const project = (id: string, name: string): ProjectWithSessions => ({
    project: { id, name, canonical_path: `/srv/${id}`, last_used_at: "t" },
    sessions: [],
  });
  const sidebarModel = {
    groups: [project("p1", "zeta"), project("p2", "alpha")],
    selectedSessionID: null,
    error: null,
    gitIdentities: null,
    creatingSession: false,
    applyProjectColors() {},
    api: {
      getLayout: async () => null,
      saveLayout: async (layout: SidebarLayout) => layout,
    },
  } as unknown as SidebarModel;

  const sidebar = new Sidebar(container as unknown as HTMLElement, sidebarModel);

  return {
    sidebar,
    anchor,
    popovers,
    raf,
    menu: () => menuEl,
    backdropFire(type, ev) {
      for (const fn of backdropListeners.get(type) ?? []) fn(ev);
    },
    row(action) {
      const found = rows.get(action);
      assert.notEqual(found, undefined, `menu row ${action} was not mounted`);
      return found!;
    },
    lastHtml: () => html,
    window: env.window,
    document: env.document,
    fireDocument: env.fireDocument,
    fireWindow: env.fireWindow,
    focused,
    rowEls: () => [...rows.values()].map((r) => r.el),
    detachAnchor() {
      anchor.isConnected = false;
    },
  };
}

function withHeaderEnv(run: (harness: HeaderHarness) => void): void {
  const originalRAF = (globalThis as unknown as { requestAnimationFrame?: (cb: () => void) => number }).requestAnimationFrame;
  const originalWindow = (globalThis as unknown as { window?: unknown }).window;
  const originalDocument = (globalThis as unknown as { document?: unknown }).document;
  const originalCSS = (globalThis as unknown as { CSS?: unknown }).CSS;

  const raf: (() => void)[] = [];
  // The Sidebar constructor renders, so the fake window and document must
  // be globals before the harness builds one; the same objects are handed
  // back so a test can fire the listeners production code registered.
  const winReg = listenerRegistry();
  const docReg = listenerRegistry();
  const fakeWindow = {
    matchMedia: () => ({ matches: false }),
    setInterval: () => 0,
    addEventListener: winReg.addEventListener,
    removeEventListener: winReg.removeEventListener,
  };
  const fakeDocument = {
    createElement: () => fakeElement({ tag: "div" }),
    addEventListener: docReg.addEventListener,
    removeEventListener: docReg.removeEventListener,
  };
  const fire = (registry: Map<string, ((ev: unknown) => void)[]>, type: string, ev: Record<string, unknown>) => {
    for (const fn of registry.get(type) ?? []) fn(ev);
  };
  (globalThis as unknown as { requestAnimationFrame: (cb: () => void) => number }).requestAnimationFrame = (cb) => {
    raf.push(cb);
    return 0;
  };
  (globalThis as unknown as { window: unknown }).window = fakeWindow;
  (globalThis as unknown as { document: unknown }).document = fakeDocument;
  (globalThis as unknown as { CSS: unknown }).CSS = { escape: (s: string) => s };

  try {
    run(makeHeaderHarness(raf, {
      window: fakeWindow,
      document: fakeDocument,
      fireDocument: (type, ev = {}) => fire(docReg, type, ev),
      fireWindow: (type, ev = {}) => fire(winReg, type, ev),
    }));
  } finally {
    (globalThis as unknown as { requestAnimationFrame?: (cb: () => void) => number }).requestAnimationFrame = originalRAF;
    (globalThis as unknown as { window?: unknown }).window = originalWindow;
    (globalThis as unknown as { document?: unknown }).document = originalDocument;
    (globalThis as unknown as { CSS?: unknown }).CSS = originalCSS;
  }
}

test("the projects header menu drops below-left with its edge on the trigger's pill edge", () => {
  withHeaderEnv(({ sidebar, anchor, raf, menu }) => {
    sidebar.openProjectsMenu();
    const mounted = menu();
    assert.notEqual(mounted, null, "menu should be mounted");
    raf.at(-1)!();
    const r = anchor.getBoundingClientRect();
    assert.equal(mounted!.style.top, `${r.bottom + 2}px`, "menu drops below the trigger");
    assert.equal(mounted!.style.left, `${r.left}px`, "panel edge sits on the pill edge, no negative offset");
  });
});

test("the trigger takes the pressed state only while its menu is open", () => {
  withHeaderEnv(({ sidebar, anchor, backdropFire }) => {
    assert.equal(anchor.getAttribute("aria-expanded"), "false");
    sidebar.openProjectsMenu();
    assert.equal(anchor.getAttribute("aria-expanded"), "true", "open menu presses the trigger");
    backdropFire("click", { target: null });
    assert.equal(anchor.getAttribute("aria-expanded"), "true", "a click inside the menu does not dismiss");
    sidebar.openProjectsMenu();
    const backdropSelf = (sidebar as unknown as { popoverRegion(): FakeElement | null })
      .popoverRegion()!.querySelector("[data-popover-backdrop]");
    backdropFire("click", { target: backdropSelf });
    assert.equal(anchor.getAttribute("aria-expanded"), "false", "dismissal releases the pressed state");
  });
});

test("Expand all expands every project and Collapse all still collapses them", () => {
  withHeaderEnv(({ sidebar, row }) => {
    const state = sidebar as unknown as { expandedProjects: Set<string> };
    sidebar.openProjectsMenu();
    row("projects-menu-expand-all").fire("click");
    assert.deepEqual([...state.expandedProjects].sort(), ["p1", "p2"], "every project is expanded");
    sidebar.openProjectsMenu();
    row("projects-menu-collapse-all").fire("click");
    assert.equal(state.expandedProjects.size, 0, "every project is collapsed");
  });
});

test("Sort by name rewrites the manual order once through the menu", () => {
  withHeaderEnv(({ sidebar, row }) => {
    const state = sidebar as unknown as { projectOrder: string[] | null };
    state.projectOrder = ["p1", "p2"];
    sidebar.openProjectsMenu();
    row("projects-menu-sort").fire("click");
    assert.deepEqual(state.projectOrder, ["p2", "p1"], "alpha sorts before zeta");
  });
});

test("the header menu names the project in context as the New session target, else the first", () => {
  withHeaderEnv(({ sidebar, lastHtml }) => {
    // The row shows no caption naming the target — that moved to the dialog —
    // so the resolved target is read from the row's hint, which still names it.
    sidebar.openProjectsMenu();
    assert.match(lastHtml(), /data-projects-menu-new-session data-hint="[^"]*alpha/,
      "first presented project is the default target");
    (sidebar as unknown as { contextProjectID: string | null }).contextProjectID = "p1";
    sidebar.openProjectsMenu();
    assert.match(lastHtml(), /data-projects-menu-new-session data-hint="[^"]*zeta/,
      "the project in context wins");
  });
});

test("arrow keys walk the menu rows in order and wrap at both ends", () => {
  withHeaderEnv(({ sidebar, rowEls, focused, fireDocument }) => {
    sidebar.openProjectsMenu();
    const rows = rowEls();
    assert.equal(rows.length, 5, "the menu carries five rows");
    assert.equal(focused.at(-1), rows[0], "opening the menu focuses the first row");
    for (const row of [rows[1], rows[2], rows[3], rows[4]]) {
      fireDocument("keydown", { key: "ArrowDown", preventDefault() {} });
      assert.equal(focused.at(-1), row);
    }
    fireDocument("keydown", { key: "ArrowDown", preventDefault() {} });
    assert.equal(focused.at(-1), rows[0], "ArrowDown wraps from the last row to the first");
    fireDocument("keydown", { key: "ArrowUp", preventDefault() {} });
    assert.equal(focused.at(-1), rows[4], "ArrowUp wraps from the first row to the last");
    fireDocument("keydown", { key: "End", preventDefault() {} });
    assert.equal(focused.at(-1), rows[4], "End lands on the last row");
    fireDocument("keydown", { key: "Home", preventDefault() {} });
    assert.equal(focused.at(-1), rows[0], "Home lands on the first row");
  });
});

test("Tab stays inside the open menu instead of walking out to the page", () => {
  withHeaderEnv(({ sidebar, rowEls, focused, fireDocument }) => {
    sidebar.openProjectsMenu();
    const rows = new Set(rowEls());
    let prevented = 0;
    for (let tab = 0; tab < 8; tab += 1) {
      fireDocument("keydown", {
        key: "Tab",
        shiftKey: tab % 2 === 1,
        preventDefault() { prevented += 1; },
      });
      assert.ok(rows.has(focused.at(-1)!), `Tab ${tab} keeps focus on a menu row`);
    }
    assert.equal(prevented, 8, "every Tab is claimed so the browser default never moves focus");
  });
});

test("Escape closes the menu from anywhere and restores focus to the trigger", () => {
  withHeaderEnv(({ sidebar, anchor, menu, focused, fireDocument }) => {
    sidebar.openProjectsMenu();
    // The keydown listener is on the document, not the menu, so Escape
    // still lands with focus nowhere near a menu row.
    fireDocument("keydown", { key: "Escape", preventDefault() {} });
    assert.equal(menu(), null, "the menu is unmounted");
    assert.equal(anchor.getAttribute("aria-expanded"), "false");
    assert.equal(focused.at(-1), anchor, "focus returns to the trigger");
  });
});

test("closing the menu releases its document and window listeners", () => {
  withHeaderEnv(({ sidebar, fireDocument, fireWindow }) => {
    sidebar.openProjectsMenu();
    fireDocument("keydown", { key: "Escape", preventDefault() {} });
    let closedAgain = false;
    fireDocument("keydown", {
      key: "Escape",
      preventDefault() { closedAgain = true; },
    });
    assert.ok(!closedAgain, "the dismissed menu's Escape handler is gone");
    fireWindow("resize", {});
    fireDocument("keydown", { key: "Tab", preventDefault() {} });
  });
});

test("a resize after the render detached the trigger closes the menu safely", () => {
  withHeaderEnv(({ sidebar, anchor, menu, focused, fireWindow, raf }) => {
    sidebar.openProjectsMenu();
    raf.at(-1)!();
    anchor.isConnected = false;
    fireWindow("resize", {});
    assert.equal(menu(), null, "no menu stays pinned to a detached trigger");
    assert.equal(anchor.getAttribute("aria-expanded"), "false");
    assert.equal(focused.at(-1), anchor, "focus lands back on the trigger");
  });
});

test("New session with no projects closes to the trigger instead of dropping focus", () => {
  withHeaderEnv(({ sidebar, anchor, menu, row, focused }) => {
    const internals = sidebar as unknown as {
      model: { groups: ProjectWithSessions[] };
      newSessionProject: string | null;
    };
    internals.model.groups = [];
    sidebar.openProjectsMenu();
    row("projects-menu-new-session").fire("click");
    assert.equal(menu(), null, "the menu closes");
    assert.equal(internals.newSessionProject, null, "no session dialog follows");
    assert.equal(focused.at(-1), anchor, "focus lands on the trigger, not <body>");
  });
});

// The context a click installs is a question about the tree, so these run
// the real model over two projects: the second one holds the row that is
// clicked, and the first is the one an unresolved context would fall back
// to (it sorts first, so a revert is visible as "alpha").
async function withSelectionEnv(
  run: (harness: ModelHarness & { sidebar: Sidebar }) => Promise<void>,
): Promise<void> {
  const originalRAF = (globalThis as unknown as { requestAnimationFrame?: (cb: () => void) => number }).requestAnimationFrame;
  const originalWindow = (globalThis as unknown as { window?: unknown }).window;
  const originalDocument = (globalThis as unknown as { document?: unknown }).document;
  const originalCSS = (globalThis as unknown as { CSS?: unknown }).CSS;

  const raf: (() => void)[] = [];
  (globalThis as unknown as { requestAnimationFrame: (cb: () => void) => number }).requestAnimationFrame = (cb) => {
    raf.push(cb);
    return 0;
  };
  (globalThis as unknown as { window: unknown }).window = {
    matchMedia: () => ({ matches: false }),
    setInterval: () => 0,
    addEventListener: () => {},
    removeEventListener: () => {},
    clearInterval: () => {},
  };
  (globalThis as unknown as { document: unknown }).document = {
    createElement: () => fakeElement({ tag: "div" }),
  };
  (globalThis as unknown as { CSS: unknown }).CSS = { escape: (s: string) => s };

  try {
    const projects: Project[] = [
      { id: "p1", name: "alpha", canonical_path: "/srv/alpha", last_used_at: "t" },
      { id: "p2", name: "zeta", canonical_path: "/srv/zeta", last_used_at: "t" },
    ];
    // The clicked row is not running: attachability is not parentage, and
    // no existing test walks the tree from a row in this state.
    const sessions: Record<string, SessionSummary[]> = {
      p1: [sessionFixture("s1")],
      p2: [sessionFixture("s2", {
        project_id: "p2",
        presentation_state: "attachment_unavailable",
      })],
    };
    const identity: GitIdentityProfile = {
      id: "identity-1", name: "Test Author", email: "test@example.invalid", revision: 1,
      created_at: "t", updated_at: "t",
    };
    const api = {
      listProjects: () => Promise.resolve(projects),
      listProviders: () => Promise.resolve(["claude"]),
      // A console with no identity draws the onboarding modal over
      // everything, which is a different surface than this one.
      listGitIdentities: () => Promise.resolve({
        profiles: [identity], default_profile_id: identity.id, revision: 1,
      }),
      listSessions: (id: string) => Promise.resolve(sessions[id] ?? []),
      getLayout: () => Promise.resolve({ expanded: [], projects: [], sessions: {} }),
      saveLayout: (next: SidebarLayout) => Promise.resolve(next),
    } as unknown as ApiClient;
    const model = new SidebarModel(api, {}, false);
    await model.refresh();
    await run({ ...makeColorHarness(model.groups, raf, model), model, writes: [] });
  } finally {
    (globalThis as unknown as { requestAnimationFrame?: (cb: () => void) => number }).requestAnimationFrame = originalRAF;
    (globalThis as unknown as { window?: unknown }).window = originalWindow;
    (globalThis as unknown as { document?: unknown }).document = originalDocument;
    (globalThis as unknown as { CSS?: unknown }).CSS = originalCSS;
  }
}

interface SelectionInternals {
  selectSession(sessionID: string, restoreFocus: boolean): void;
  contextProjectID: string | null;
  selectedProjectID: string | null;
  renamingSession: string | null;
  openRowMenuTarget: { kind: "project" | "session"; id: string } | null;
}

const internals = (sidebar: Sidebar): SelectionInternals =>
  sidebar as unknown as SelectionInternals;

test("clicking a session row targets its own project even when the row cannot be attached", async () => {
  await withSelectionEnv(async ({ sidebar }) => {
    internals(sidebar).selectSession("s2", false);
    assert.equal(sidebar.newSessionTarget()?.name, "zeta",
      "the row sits in zeta's rail, so New session means zeta");
    assert.equal(internals(sidebar).selectedProjectID, null,
      "selecting a session releases the project row's selection");
  });
});

test("a session row the poll has already dropped still names its own project", async () => {
  await withSelectionEnv(async ({ sidebar, model }) => {
    // The user was working in alpha, so a click that resolves nothing
    // would leave the target there — or, worse, null it back to the first
    // project.
    sidebar.setProjectContext("p1");
    // The poll rebuilt the groups without this row while the click was in
    // flight; the row it was aimed at is still on screen, in zeta's rail.
    const zeta = model.groups.find((g) => g.project.id === "p2")!;
    zeta.sessions = [];
    internals(sidebar).selectSession("s2", false);
    assert.equal(sidebar.newSessionTarget()?.name, "zeta",
      "the clicked row's rail names the project, whether or not it can be selected");
  });
});

test("a click on a row mid-rename leaves the previous context standing", async () => {
  await withSelectionEnv(async ({ sidebar }) => {
    sidebar.setProjectContext("p1");
    internals(sidebar).renamingSession = "s2";
    internals(sidebar).selectSession("s2", false);
    assert.equal(sidebar.newSessionTarget()?.name, "alpha",
      "a rename click is not a selection and changes no context");
  });
});

test("an open row menu names the New session target, as it already names the rename target", async () => {
  await withSelectionEnv(async ({ sidebar }) => {
    sidebar.setProjectContext("p1");
    internals(sidebar).openRowMenuTarget = { kind: "project", id: "p2" };
    assert.deepEqual(sidebar.renameTarget(), { kind: "project", id: "p2" });
    assert.equal(sidebar.newSessionTarget()?.name, "zeta",
      "both resolvers point at the row whose menu is open");
    internals(sidebar).openRowMenuTarget = { kind: "session", id: "s2" };
    assert.equal(sidebar.newSessionTarget()?.name, "zeta",
      "a session row's menu names the project it sits in");
    internals(sidebar).openRowMenuTarget = { kind: "project", id: "gone" };
    assert.equal(sidebar.newSessionTarget()?.name, "alpha",
      "a menu target whose row no longer exists falls back to the context");
  });
});
