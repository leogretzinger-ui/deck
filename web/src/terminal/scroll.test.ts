import { test } from "node:test";
import assert from "node:assert/strict";
import {
  TouchScrollPlanner,
  AlternateTouchGesturePlanner,
  WheelScrollPlanner,
  planScrollKey,
  type WheelDecision,
  type WheelState,
  alternateTouchRoute,
  wheelReports,
} from "./scroll.ts";

function touchPoint(clientX: number, clientY: number, timeStamp: number) {
  return { clientX, clientY, timeStamp };
}

test("a downward touch drag reveals older history only after the threshold", () => {
  const planner = new TouchScrollPlanner();
  planner.start(touchPoint(100, 200, 0));
  assert.deepEqual(planner.move(touchPoint(102, 209, 10), { selectionActive: false }),
    { kind: "native" });
  assert.deepEqual(planner.move(touchPoint(102, 232, 30), { selectionActive: false }), {
    kind: "scroll",
    command: { direction: "up", lines: 2 },
  });
  assert.equal(planner.end(), true);
});

test("repeated touch moves are rate-limited and reverse toward live output", () => {
  const planner = new TouchScrollPlanner();
  planner.start(touchPoint(100, 200, 0));
  assert.equal(planner.move(touchPoint(100, 232, 30), { selectionActive: false }).kind, "scroll");
  assert.deepEqual(planner.move(touchPoint(100, 264, 40), { selectionActive: false }),
    { kind: "consume" });
  assert.deepEqual(planner.move(touchPoint(100, 184, 70), { selectionActive: false }), {
    kind: "scroll",
    command: { direction: "down", lines: 3 },
  });
});

test("horizontal, long-press, and selection gestures remain native", () => {
  const horizontal = new TouchScrollPlanner();
  horizontal.start(touchPoint(100, 200, 0));
  assert.deepEqual(horizontal.move(touchPoint(130, 204, 20), { selectionActive: false }),
    { kind: "native" });
  assert.equal(horizontal.end(), false);

  const held = new TouchScrollPlanner();
  held.start(touchPoint(100, 200, 0));
  assert.deepEqual(held.move(touchPoint(100, 180, 500), { selectionActive: false }),
    { kind: "native" });
  assert.equal(held.end(), false);

  const selection = new TouchScrollPlanner();
  selection.start(touchPoint(100, 200, 0));
  assert.deepEqual(selection.move(touchPoint(100, 180, 20), { selectionActive: true }),
    { kind: "native" });
  assert.equal(selection.end(), false);
});

test("touch commands clamp huge moves and reset without stale momentum", () => {
  const planner = new TouchScrollPlanner();
  planner.start(touchPoint(100, 0, 0));
  assert.deepEqual(planner.move(touchPoint(100, 10_000, 30), { selectionActive: false }), {
    kind: "scroll",
    command: { direction: "up", lines: 120 },
  });
  planner.reset();
  assert.deepEqual(planner.move(touchPoint(100, 100, 60), { selectionActive: false }),
    { kind: "native" });
});

test("alternate upward touch latches one live command through moves and reversals", () => {
  const alternate = new AlternateTouchGesturePlanner();
  const touch = new TouchScrollPlanner();
  touch.start(touchPoint(100, 200, 0));
  const routed = [
    touchPoint(100, 168, 30),
    touchPoint(100, 136, 60),
    touchPoint(100, 104, 90),
    touchPoint(100, 136, 120),
    touchPoint(100, 88, 150),
  ].flatMap((point) => {
    const decision = touch.move(point, { selectionActive: false });
    assert.notEqual(decision.kind, "native", "claimed gesture remains consumed through touch end");
    if (decision.kind !== "scroll") return [];
    const command = alternate.plan(decision.command);
    return command === null ? [] : [command];
  });
  assert.deepEqual(routed, [{ direction: "live" }]);
  assert.equal(touch.end(), true);

  alternate.reset();
  touch.start(touchPoint(100, 100, 200));
  const up = touch.move(touchPoint(100, 132, 230), { selectionActive: false });
  assert.equal(up.kind, "scroll");
  if (up.kind === "scroll") {
    assert.deepEqual(alternate.plan(up.command), { direction: "up", lines: 2 });
  }
  const down = touch.move(touchPoint(100, 84, 260), { selectionActive: false });
  assert.equal(down.kind, "scroll");
  if (down.kind === "scroll") {
    assert.deepEqual(alternate.plan(down.command), { direction: "live" });
  }
});

const alt: WheelState = { bufferType: "alternate", mouseTracking: false, pageLines: 24 };

function wheel(deltaY: number, opts: { deltaMode?: number; timeStamp?: number } = {}) {
  return { deltaY, deltaMode: opts.deltaMode ?? 0, timeStamp: opts.timeStamp ?? 0 };
}

function command(decision: WheelDecision): { direction: string; lines: number } {
  assert.equal(decision.kind, "scroll", `expected a scroll, got ${decision.kind}`);
  return (decision as { kind: "scroll"; command: { direction: string; lines: number } }).command;
}

test("normal buffer wheel stays native: xterm's own scrollback handles it", () => {
  const planner = new WheelScrollPlanner();
  const decision = planner.plan(wheel(-100), { ...alt, bufferType: "normal" });
  assert.deepEqual(decision, { kind: "native" });
});

test("mouse-tracking wheel stays native: the application owns the wheel", () => {
  const planner = new WheelScrollPlanner();
  const decision = planner.plan(wheel(-100), { ...alt, mouseTracking: true });
  assert.deepEqual(decision, { kind: "native" });
});

test("alternate-screen wheel-up becomes a copy-mode scroll command", () => {
  const planner = new WheelScrollPlanner();
  // One notch: deltaY -100 px = ~6 lines up.
  const decision = planner.plan(wheel(-100), alt);
  assert.deepEqual(command(decision), { direction: "up", lines: 6 });
});

test("alternate-screen wheel-down scrolls down", () => {
  const planner = new WheelScrollPlanner();
  const decision = planner.plan(wheel(100), alt);
  assert.deepEqual(command(decision), { direction: "down", lines: 6 });
});

test("line-mode deltas map one to one", () => {
  const planner = new WheelScrollPlanner();
  const decision = planner.plan(wheel(-3, { deltaMode: 1 }), alt);
  assert.deepEqual(command(decision), { direction: "up", lines: 3 });
});

test("page-mode deltas scroll whole pages", () => {
  const planner = new WheelScrollPlanner();
  const decision = planner.plan(wheel(-1, { deltaMode: 2 }), alt);
  assert.deepEqual(command(decision), { direction: "up", lines: 24 });
});

test("sub-line trackpad deltas are consumed, accumulate, then emit", () => {
  const planner = new WheelScrollPlanner();
  // Events of -8px = 0.5 lines each: every one is consumed — none may fall
  // through to become arrow keys — and each accumulated whole line emits.
  assert.deepEqual(planner.plan(wheel(-8, { timeStamp: 0 }), alt), { kind: "consume" });
  const second = planner.plan(wheel(-8, { timeStamp: 100 }), alt);
  assert.deepEqual(command(second), { direction: "up", lines: 1 });
  assert.deepEqual(planner.plan(wheel(-8, { timeStamp: 200 }), alt), { kind: "consume" });
  const fourth = planner.plan(wheel(-8, { timeStamp: 300 }), alt);
  assert.deepEqual(command(fourth), { direction: "up", lines: 1 });
});

test("commands are rate-limited: a burst coalesces into fewer, larger commands", () => {
  const planner = new WheelScrollPlanner();
  assert.equal(planner.plan(wheel(-100, { timeStamp: 0 }), alt).kind, "scroll");
  // 10ms later: within the interval, consumed but accumulated.
  assert.deepEqual(planner.plan(wheel(-100, { timeStamp: 10 }), alt), { kind: "consume" });
  assert.deepEqual(planner.plan(wheel(-100, { timeStamp: 20 }), alt), { kind: "consume" });
  // Past the interval: the accumulated lines go out as one command
  // (3 consumed notches of 6.25 lines plus the 0.25 carried remainder).
  const later = planner.plan(wheel(-100, { timeStamp: 60 }), alt);
  assert.deepEqual(command(later), { direction: "up", lines: 19 });
});

test("direction flips drain the accumulator sign-correctly", () => {
  const planner = new WheelScrollPlanner();
  assert.deepEqual(command(planner.plan(wheel(-100, { timeStamp: 0 }), alt)),
    { direction: "up", lines: 6 });
  const flipped = planner.plan(wheel(160, { timeStamp: 100 }), alt);
  // -0.25 carry + 10 down = 9.75 -> 9 lines down.
  assert.deepEqual(command(flipped), { direction: "down", lines: 9 });
});

test("switching to a native context clears any accumulated carry", () => {
  const planner = new WheelScrollPlanner();
  assert.deepEqual(planner.plan(wheel(-8, { timeStamp: 0 }), alt), { kind: "consume" });
  assert.deepEqual(
    planner.plan(wheel(-8, { timeStamp: 100 }), { ...alt, bufferType: "normal" }),
    { kind: "native" },
  );
  // Back in the alternate screen the old half-line must be gone.
  assert.deepEqual(planner.plan(wheel(-8, { timeStamp: 200 }), alt), { kind: "consume" });
});

test("one command is bounded even for an enormous delta", () => {
  const planner = new WheelScrollPlanner();
  const decision = planner.plan(wheel(-1_000_000, { timeStamp: 0 }), alt);
  assert.deepEqual(command(decision), { direction: "up", lines: 120 });
});

// An oversized delta must not leave a residue that dominates
// later events — after a clamped command, the NEXT wheel in the opposite
// direction must scroll that direction, not replay the old one.
test("a clamped command leaves no residue: the next wheel-down scrolls down", () => {
  const planner = new WheelScrollPlanner();
  // An OS page-scroll fling: 6 pages on a 24-row terminal = 144 lines,
  // above the 120-line command bound.
  const flung = planner.plan(wheel(-6, { deltaMode: 2, timeStamp: 0 }), alt);
  assert.deepEqual(command(flung), { direction: "up", lines: 120 });
  // The user reverses. Pre-fix the planner still owed 24 lines UP and kept
  // emitting up; the excess intent must have been dropped instead.
  const reversed = planner.plan(wheel(100, { timeStamp: 100 }), alt);
  assert.deepEqual(command(reversed), { direction: "down", lines: 6 });
});

test("repeated enormous deltas never accumulate beyond the bound", () => {
  const planner = new WheelScrollPlanner();
  assert.deepEqual(command(planner.plan(wheel(-1_000_000, { timeStamp: 0 }), alt)),
    { direction: "up", lines: 120 });
  assert.deepEqual(command(planner.plan(wheel(-1_000_000, { timeStamp: 100 }), alt)),
    { direction: "up", lines: 120 });
  // A single ordinary notch down now behaves as if the flings never happened.
  assert.deepEqual(command(planner.plan(wheel(100, { timeStamp: 200 }), alt)),
    { direction: "down", lines: 6 });
});

const altKeys = { bufferType: "alternate" as const, pageLines: 24 };

function key(
  k: string,
  opts: { type?: string; shift?: boolean; ctrl?: boolean; alt?: boolean; meta?: boolean } = {},
) {
  return {
    type: opts.type ?? "keydown",
    key: k,
    shiftKey: opts.shift ?? true,
    ctrlKey: opts.ctrl ?? false,
    altKey: opts.alt ?? false,
    metaKey: opts.meta ?? false,
  };
}

test("Shift+PageUp over the alternate screen opens copy-mode one page back", () => {
  assert.deepEqual(planScrollKey(key("PageUp"), altKeys), { direction: "up", lines: 24 });
});

test("Shift+PageDown pages toward the bottom", () => {
  assert.deepEqual(planScrollKey(key("PageDown"), altKeys), { direction: "down", lines: 24 });
});

test("normal buffer keys stay native: xterm pages its own viewport", () => {
  assert.equal(planScrollKey(key("PageUp"), { ...altKeys, bufferType: "normal" }), null);
});

test("only plain Shift+Page keys are claimed", () => {
  assert.equal(planScrollKey(key("PageUp", { shift: false }), altKeys), null);
  assert.equal(planScrollKey(key("PageUp", { ctrl: true }), altKeys), null);
  assert.equal(planScrollKey(key("PageUp", { alt: true }), altKeys), null);
  assert.equal(planScrollKey(key("PageUp", { meta: true }), altKeys), null);
  assert.equal(planScrollKey(key("PageUp", { type: "keyup" }), altKeys), null);
  assert.equal(planScrollKey(key("Home"), altKeys), null);
  assert.equal(
    planScrollKey(key("Escape", { shift: false }), altKeys),
    null,
    "Escape belongs to the provider and is never a scrollback shortcut",
  );
});

// A mouse-tracking application owns its transcript; the route decision and
// the report encoding are what the mobile drag rides on.
test("alternate touch routes to mouse reports only under mouse tracking", () => {
  assert.equal(alternateTouchRoute(true), "mouse-reports");
  assert.equal(alternateTouchRoute(false), "copy-mode");
});

test("wheel reports encode SGR wheel events at the clamped cell", () => {
  assert.deepEqual(
    wheelReports({ direction: "up", lines: 6 }, { column: 10.7, row: 4.2 }, { columns: 80, rows: 24 }),
    ["\x1b[<64;10;4M", "\x1b[<64;10;4M"],
    "six planned lines become two up-reports at the touch cell",
  );
  assert.deepEqual(
    wheelReports({ direction: "down", lines: 1 }, { column: 0, row: 99 }, { columns: 80, rows: 24 }),
    ["\x1b[<65;1;24M"],
    "a small drag still emits one report, clamped inside the grid",
  );
});
