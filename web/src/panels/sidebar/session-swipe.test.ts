import assert from "node:assert/strict";
import test from "node:test";
import { SessionSwipeGesture, SWIPE_REVEAL_PX } from "./session-swipe.ts";

// The sheet scrolls vertically, so the row claims only a decisively
// horizontal drag; a diagonal or vertical drift hands the gesture back.
test("a vertical drift cancels before the row claims the drag", () => {
  const g = new SessionSwipeGesture();
  g.begin(200, 100, false);
  assert.equal(g.move(196, 104).phase, "pending", "a jitter is not yet a claim");
  assert.equal(g.move(192, 130).phase, "idle", "vertical travel belongs to the scroller");
  assert.equal(g.end(192), false, "an unclaimed gesture never opens");
});

test("a left swipe past half the reveal commits open", () => {
  const g = new SessionSwipeGesture();
  g.begin(200, 100, false);
  assert.equal(g.move(180, 102).phase, "dragging");
  const mid = g.move(140, 103);
  assert.equal(mid.travel, 60, "travel follows the pointer");
  assert.equal(g.end(140), true, "past-half release parks the row open");
});

test("a short left swipe springs back closed", () => {
  const g = new SessionSwipeGesture();
  g.begin(200, 100, false);
  g.move(180, 100);
  assert.equal(g.end(170), false, "under half the reveal returns home");
});

test("travel clamps at the 100px park", () => {
  const g = new SessionSwipeGesture();
  g.begin(300, 100, false);
  g.move(280, 100);
  assert.equal(g.move(50, 100).travel, SWIPE_REVEAL_PX);
});

test("a right swipe on an open row walks it home", () => {
  const g = new SessionSwipeGesture();
  g.begin(100, 100, true);
  g.move(120, 100);
  assert.equal(g.move(170, 100).travel, 30, "rightward travel closes from the park");
  assert.equal(g.end(170), false, "past-half homeward release closes");
});

test("a plain tap keeps whatever state the row had", () => {
  const closed = new SessionSwipeGesture();
  closed.begin(100, 100, false);
  assert.equal(closed.end(101), false);
  const open = new SessionSwipeGesture();
  open.begin(100, 100, true);
  assert.equal(open.end(99), true, "a tap on an open row does not close it here — the shell's tap-away does");
});
